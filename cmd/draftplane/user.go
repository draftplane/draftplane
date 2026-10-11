package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/draftplane/draftplane/client/config"
)

// runUser is `draftplane user`, which shows or sets the name this machine's
// comments carry. Modeled on runTheme (theme.go): no argument reads
// config.json, one argument writes it.
//
// The no-argument arm reads through config.MachinePseudonym, which mints a
// pseudonym on first read, so what this prints is exactly what a comment left
// on this machine would show. It never falls back to pseudonymFallback: that
// fallback exists for a write whose attribution names nobody, and printing it
// here would claim a name that is not actually configured -- so a config
// error is reported outward instead.
func runUser(w io.Writer, args []string) error {
	path, err := config.DefaultPath()
	if err != nil {
		return err
	}

	switch len(args) {
	case 0:
		name, err := config.MachinePseudonym(path)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, name)
		return err

	case 1:
		// Trimmed, and the empty case named rather than left to
		// config.SetMachinePseudonym's own "name must not be empty".
		name := strings.TrimSpace(args[0])
		if name == "" {
			return fmt.Errorf("draftplane user: name a user name, or run \"draftplane user\" to see the current one\n%s", usage)
		}
		if err := config.SetMachinePseudonym(path, name); err != nil {
			return err
		}
		_, err := fmt.Fprintf(w, "user name set to %s in %s\n", name, path)
		return err

	default:
		// Name the bound as well as the offending word: the extra arguments are
		// often an unquoted name ("user Luke Skywalker"), where the word alone
		// reads as though it were the unknown one.
		return fmt.Errorf("draftplane user takes at most one name; unexpected argument %q\n%s", args[1], usage)
	}
}
