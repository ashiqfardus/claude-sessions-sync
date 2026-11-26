package main

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFoldersRoute(t *testing.T) {
	u := newTestUI(t, uiOpts{NoOpen: true})
	runUI(t, u)
	cookie := login(t, u)
	tree := t.TempDir()
	for _, d := range []string{"b", "A", ".cfg"} {
		os.Mkdir(filepath.Join(tree, d), 0o755)
	}
	os.WriteFile(filepath.Join(tree, "f.txt"), nil, 0o644)

	var v struct {
		Path, Parent string
		Entries      []struct{ Name, Path string }
		Roots        []struct{ Name, Path string }
		Detected     []struct{ Name, Path string }
		Error        string
	}
	body := getJSON(t, u, cookie, "/api/folders?path="+url.QueryEscape(tree), &v)
	if len(v.Entries) != 2 || v.Entries[0].Name != "A" || v.Entries[1].Name != "b" || v.Parent == "" || len(v.Roots) < 2 {
		t.Errorf("folders %s", body)
	}
	if !strings.Contains(body, `"entries":[{"name":"A","path":`) {
		t.Errorf("not lowerCamelCase: %s", body)
	}
	getJSON(t, u, cookie, "/api/folders?hidden=1&path="+url.QueryEscape(tree), &v)
	if len(v.Entries) != 3 {
		t.Errorf("hidden=1 gave %+v", v.Entries)
	}
	// No path: the home folder. A bad path: a plain sentence with no path in it.
	getJSON(t, u, cookie, "/api/folders", &v)
	if home, _ := os.UserHomeDir(); v.Path != filepath.Clean(home) {
		t.Errorf("default path %q, home %q", v.Path, home)
	}
	body = getJSON(t, u, cookie, "/api/folders?path="+url.QueryEscape(filepath.Join(tree, "nope")), &v)
	if v.Error == "" || strings.Contains(v.Error, tree) || strings.Contains(body, "no such file") {
		t.Errorf("error %s", body)
	}
	if resp, _ := send(t, u, http.MethodGet, u.url("/api/folders"), ""); resp.StatusCode == http.StatusOK {
		t.Errorf("no cookie gave %d", resp.StatusCode)
	}
}
