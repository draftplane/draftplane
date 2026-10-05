package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/draftplane/draftplane/theme"
)

// runTheme is `draftplane theme`, the one gesture that writes theme.json: with
// no argument it prints the configured setting, with one it writes it.
//
// It never deletes the file. "system" is a value the file may hold rather than
// the absence of the file, so returning to detection is a write like any other.
// An absent file and a "system" file mean the same thing to startup, which is
// why the no-argument arm reports the absent case as "system" rather than
// inventing a third answer no argument can produce.
func runTheme(w io.Writer, args []string) error {
	path, err := theme.DefaultPath()
	if err != nil {
		return err
	}

	switch len(args) {
	case 0:
		name, _, err := theme.Load(path)
		if err != nil {
			return err
		}
		if name == "" {
			name = theme.SystemName
		}
		_, err = fmt.Fprintln(w, name)
		return err

	case 1:
		// Trimmed, and the empty case named rather than left to theme.Setting's
		// `unknown theme ""`.
		name := strings.TrimSpace(args[0])
		if name == "" {
			return fmt.Errorf("draftplane theme: name a setting, or run \"draftplane theme\" to see the current one\n%s", usage)
		}
		if err := theme.Save(path, name); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "theme set to %s in %s\n", name, path); err != nil {
			return err
		}
		if name == theme.SystemName {
			// The only setting whose effect the word does not already state.
			_, err := fmt.Fprintln(w, "draftplane will match your terminal's background each time it starts")
			return err
		}
		return nil

	default:
		// Name the bound as well as the offending word: the extra arguments are
		// often a shell-split value ("theme solarized dark"), where the word alone
		// reads as though it were the unknown one.
		return fmt.Errorf("draftplane theme takes at most one setting; unexpected argument %q\n%s", args[1], usage)
	}
}
