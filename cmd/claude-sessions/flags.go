package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// parseFlags parses args, accepting flags before, between and after positional
// arguments, and expands a leading ~ in every string flag.
//
// Go's flag package stops at the first positional argument and leaves the rest
// unparsed, so `search "pool" --archived` searched for "pool --archived" and
// `config set-destination D:\a --claude-dir D:\work` wrote the default profile's
// config - both silently. Everything after "--" is positional, as usual.
//
// positional says whether the command takes positional arguments at all; one that
// does not rejects them rather than ignoring them.
func parseFlags(fs *flag.FlagSet, args []string, positional bool) error {
	var flags, pos []string
	for rest := args; len(rest) > 0; {
		if err := fs.Parse(rest); err != nil {
			return err
		}
		consumed := rest[:len(rest)-fs.NArg()]
		rest = fs.Args()
		if n := len(consumed); n > 0 && consumed[n-1] == "--" {
			flags = append(flags, consumed[:n-1]...)
			pos = append(pos, rest...)
			break
		}
		flags = append(flags, consumed...)
		if len(rest) > 0 {
			pos = append(pos, rest[0])
			rest = rest[1:]
		}
	}
	if len(pos) > 0 && !positional {
		return fmt.Errorf("%s takes no arguments, only flags (got %q)", fs.Name(), pos[0])
	}
	// Once more over the flags alone, then the positionals, so fs.Args() is exactly
	// the positional arguments whatever order they were given in.
	if err := fs.Parse(append(append(flags, "--"), pos...)); err != nil {
		return err
	}

	// A quoted "~/dev", or any ~ under PowerShell, reaches the program unexpanded.
	fs.Visit(func(f *flag.Flag) {
		if g, ok := f.Value.(flag.Getter); ok {
			if s, ok := g.Get().(string); ok && s != expandHome(s) {
				_ = f.Value.Set(expandHome(s))
			}
		}
	})
	return nil
}

// expandHome replaces a leading ~ with the home directory.
func expandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, `~\`) {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[1:])
}
