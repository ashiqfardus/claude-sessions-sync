package main

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Go's flag package stops at the first positional argument, so a flag after it was
// silently treated as text.
func TestParseFlagsAcceptsFlagsAnywhere(t *testing.T) {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	dir := fs.String("claude-dir", "", "")
	if err := parseFlags(fs, []string{"connection", "--json", "pool", "--claude-dir", "x", "--", "--literal"}, true); err != nil {
		t.Fatal(err)
	}
	if !*asJSON || *dir != "x" {
		t.Errorf("flags after arguments were not parsed: json=%v dir=%q", *asJSON, *dir)
	}
	if got := fs.Args(); !reflect.DeepEqual(got, []string{"connection", "pool", "--literal"}) {
		t.Errorf("positional arguments = %q", got)
	}

	fs = flag.NewFlagSet("push", flag.ContinueOnError)
	fs.Bool("quiet", false, "")
	if err := parseFlags(fs, []string{"--quiet", "stray"}, false); err == nil {
		t.Error("a command without positional arguments accepted one")
	}
}

// A quoted "~/dev", or any ~ under PowerShell, reaches the program unexpanded.
func TestParseFlagsExpandsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	root := fs.String("search-root", "", "")
	if err := parseFlags(fs, []string{"--search-root", "~/dev"}, false); err != nil {
		t.Fatal(err)
	}
	if *root != filepath.Join(home, "dev") {
		t.Errorf("~ not expanded: %q", *root)
	}
	if expandHome("~user/x") != "~user/x" || expandHome("a~/b") != "a~/b" {
		t.Error("only a leading ~ followed by a separator is the home directory")
	}
}

// Text from the archive is written by other machines.
func TestArchiveTextIsInert(t *testing.T) {
	// OSC 52 (set the clipboard), BEL, and the one-byte C1 CSI.
	if got := printable("proj\x1b]52;c;ZXZpbA==\x07\u009b31m"); strings.ContainsAny(got, "\x1b\x07\u009b") {
		t.Errorf("control characters reached the terminal: %q", got)
	}

	// Every Markdown metacharacter arrives escaped, and it all stays on one line.
	got := mdText("![x](http://evil.example/p.png) | `c` <b>\n## *heading*_")
	if strings.Contains(got, "\n") {
		t.Errorf("mdText kept a newline: %q", got)
	}
	r := []rune(got)
	for i, c := range r {
		if strings.ContainsRune("!`[]<>|#*_", c) && (i == 0 || r[i-1] != '\\') {
			t.Errorf("mdText left %q unescaped in %q", c, got)
		}
	}
}
