// Package config stores this machine's local settings: values that describe
// how draftplane behaves on it.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/draftplane/draftplane/client/identity"
	"github.com/draftplane/draftplane/xdg"
)

// Config is this machine's local settings.
//
// MachinePseudonym is the stable name a human writes under on this machine --
// a curated word pair with no suffix (identity.MintPair), e.g.
// "calm-mountain", rendered exactly as stored. Minted once, by
// MachinePseudonym the function in this package, and reused for the life of
// this config file; empty until that first read. `draftplane user` can set it
// to anything.
//
// A field this build does not know is ignored on Load rather than refused, so
// a config.json carrying one still yields its pseudonym.
type Config struct {
	MachinePseudonym string `json:"machine_pseudonym,omitempty"`
}

// DefaultPath is $XDG_CONFIG_HOME/draftplane/config.json, falling back to
// ~/.config/draftplane/config.json: config rather than data, machine-local
// settings and not disposable cache.
func DefaultPath() (string, error) {
	dir, err := xdg.ConfigHome()
	if err != nil {
		return "", fmt.Errorf("config: %w", err)
	}
	return filepath.Join(dir, "draftplane", "config.json"), nil
}

// Load reads the config at path. A missing file is not an error but the
// ordinary state of a machine that has never minted a pseudonym, and Load
// answers it with a zero Config rather than a sentinel every caller would
// special-case.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("config: reading %s: %w", path, err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		// Never forward json.Unmarshal's own error, even wrapped: a field
		// that decodes through a custom UnmarshalJSON can echo a raw value
		// into it. Nothing in Config does today, so there is nothing for
		// this struct to leak yet, but the next field it gains is not
		// guaranteed to be as inert.
		return Config{}, fmt.Errorf("config: %s is not a valid config file", path)
	}
	return c, nil
}

// Save writes atomically at path, mode 0600, creating its parent directory if
// needed. The temp file is created in the target directory so the rename
// cannot cross a filesystem boundary, and never widens past 0600. Config holds
// no secret today, but the rename is load-bearing regardless: this directory is
// read by more than one process (a TUI session and any `draftplane mcp`
// processes the same human is running), and a reader between an in-place
// write's first byte and its last would see a torn file.
func Save(path string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config: creating directory: %w", err)
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("config: encoding: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*")
	if err != nil {
		return fmt.Errorf("config: creating temp file: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op once renamed

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("config: setting permissions: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("config: writing: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: closing temp file: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("config: replacing %s: %w", path, err)
	}
	return nil
}

// WithLock runs fn holding an exclusive lock on a sibling lockfile, so
// concurrent readers/writers -- across goroutines and processes -- serialize
// their Load-modify-Save cycles. The lock is on a separate file because Save
// replaces the config by rename, which would drop a lock held on the old
// inode. flock rather than a lockfile whose existence means "held", so the
// kernel releases it if the process dies.
func WithLock(path string, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config: creating directory: %w", err)
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("config: opening lock: %w", err)
	}
	defer func() { _ = f.Close() }()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("config: locking: %w", err)
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()

	return fn()
}

// MachinePseudonym returns the stable per-machine pseudonym at path, minting
// and saving one under lock if none exists yet.
//
// First-run minting is exactly where two processes race: a TUI session and a
// `draftplane mcp` process starting at the same moment can both find no
// pseudonym on disk. WithLock holds across the read, the mint and the save as
// one cycle, so whichever caller loses the race reads back the value the
// winner already saved instead of minting a second, different one.
func MachinePseudonym(path string) (string, error) {
	var pseudonym string
	err := WithLock(path, func() error {
		c, err := Load(path)
		if err != nil {
			return err
		}
		if c.MachinePseudonym != "" {
			pseudonym = c.MachinePseudonym
			return nil
		}
		c.MachinePseudonym = identity.MintPair()
		if err := Save(path, c); err != nil {
			return err
		}
		pseudonym = c.MachinePseudonym
		return nil
	})
	return pseudonym, err
}

// SetMachinePseudonym sets this machine's pseudonym at path to name, running
// Load, the assignment and Save inside WithLock -- the same cycle
// MachinePseudonym uses, so this cannot race a first-run mint in another TUI
// or MCP process.
//
// name is trimmed and stored verbatim otherwise: nothing else about it is
// rewritten, and a leading "user-" or "agent-" is accepted rather than
// refused -- a login that collides with one is already an accepted
// collision (TestFormatAttributionBareLoginCollidesByDesign, ui/ui_test.go),
// and namespacing it off was ruled out there.
//
// Refused: an empty or all-whitespace name, and -- GitHub's own login rule,
// so a mistyped flag like "-v" can never become a name -- anything outside
// ASCII letters, digits, and a hyphen or space separator that may not lead,
// trail or repeat, or that runs past 39 characters. This also rules out
// every control character, newline and tab included: the TUI would draw one
// as a visible escape (ui/control.go), and none of them are letters, digits
// or the one separator this rule allows.
func SetMachinePseudonym(path, name string) error {
	name = strings.TrimSpace(name)
	if err := validateUserName(name); err != nil {
		return err
	}
	return WithLock(path, func() error {
		c, err := Load(path)
		if err != nil {
			return err
		}
		c.MachinePseudonym = name
		return Save(path, c)
	})
}

// maxUserNameLength is GitHub's own login length cap.
const maxUserNameLength = 39

// userNamePattern is GitHub's login shape with space accepted anywhere a
// hyphen is: runs of ASCII letters and digits, joined by single hyphen or
// space separators that may not lead, trail or repeat.
var userNamePattern = regexp.MustCompile(`^[A-Za-z0-9]+([ -][A-Za-z0-9]+)*$`)

// validateUserName is the one rule SetMachinePseudonym enforces, run after
// trimming. It is kept as a single function rather than several checks so
// there is exactly one place that states the rule.
func validateUserName(name string) error {
	if name == "" {
		return fmt.Errorf("config: name must not be empty")
	}
	if len(name) > maxUserNameLength || !userNamePattern.MatchString(name) {
		// Describe the rule rather than printing name back: a violation can
		// be a raw control byte, which must never reach a terminal.
		return fmt.Errorf("config: a user name may contain only letters, digits, and single hyphens or spaces between them, up to %d characters", maxUserNameLength)
	}
	return nil
}
