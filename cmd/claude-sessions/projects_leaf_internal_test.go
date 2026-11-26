package main

import (
	"testing"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
)

// A drive root is listed as E:\, not as "\", and a POSIX root as /.
func TestProjectLeafOfARoot(t *testing.T) {
	for in, want := range map[string]string{
		`E:\`: `E:\`, `e:/`: `e:\`, `E:`: `E:\`, "/": "/",
		`C:\Users\you\dev\api`: "api", "/home/me/dev/api/": "api", `\\server\share`: "share", "": "",
	} {
		if got := projectLeaf(in); got != want {
			t.Errorf("projectLeaf(%q) = %q, want %q", in, got, want)
		}
	}
	entry, _ := projectIdentity(claude.Bucket{Name: "E--"}, `E:\`, nil)
	if opts, _ := groupProjects(map[string]archive.Project{"E--": entry}, nil); len(opts) != 1 || opts[0].Name != `E:\` {
		t.Errorf("the panel lists E:\\ as %+v", opts)
	}
}

// A keep entry saved for a drive root before 0.7.3 ("\") must still protect it once
// the manifest names it E:\, and the other way round while computers run mixed builds.
func TestKeepsMatchesRootsAcrossBuilds(t *testing.T) {
	cases := []struct {
		entry, leaf, path string
		want              bool
	}{
		{`\`, `E:\`, `E:\`, true},           // old entry, new manifest
		{`e:\`, `\`, `E:\`, true},           // new entry, old manifest
		{`E:\`, `E:\`, `E:\`, true},         // same build
		{`\`, `share`, `\\srv\share`, true}, // old entry, UNC root in a new manifest
		{`/`, `/`, `/`, true},
		{`\`, `api`, `E:\api`, false}, // a root entry protects only roots
		{`E:\`, `api`, `E:\api`, false},
		{`api`, `E:\`, `E:\`, false},
		{`\`, `share`, `\\srv\share\sub`, false},
	}
	for _, c := range cases {
		got := keeps(archive.KeepEntry{Leaf: c.entry}, archive.Project{Leaf: c.leaf, Path: c.path}, nil)
		if got != c.want {
			t.Errorf("keeps(entry %q, leaf %q, path %q) = %v, want %v", c.entry, c.leaf, c.path, got, c.want)
		}
	}
}
