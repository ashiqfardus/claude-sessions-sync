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
