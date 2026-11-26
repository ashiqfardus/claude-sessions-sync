package archive

import (
	"os"
	"testing"
)

func TestSaveConfigKeepsWorkKeys(t *testing.T) {
	root := t.TempDir()
	full := Config{
		Destination:   "/a",
		WorkBackup:    "off",
		WorkPlacement: "machine",
		WorkMap:       map[string]string{`E:\SQL`: "/x"},
		WorkExclude:   []string{"/y"},
	}
	if err := SaveConfig(root, full); err != nil {
		t.Fatal(err)
	}
	if err := SaveConfig(root, Config{Destination: "/b"}); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if c.Destination != "/b" || c.WorkBackup != "off" || c.WorkPlacement != "machine" ||
		c.WorkMap[`E:\SQL`] != "/x" || len(c.WorkExclude) != 1 || c.WorkOn() {
		t.Fatalf("work keys lost: %+v", c)
	}
	if err := SaveConfig(root, Config{Destination: "/b", WorkExclude: []string{}, WorkMap: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	c, _ = LoadConfig(root)
	if len(c.WorkExclude) != 0 || len(c.WorkMap) != 0 {
		t.Fatalf("empty lists did not clear: %+v", c)
	}
	if !(Config{}).WorkOn() || !(Config{WorkBackup: "on"}).WorkOn() {
		t.Fatal("work should default to on")
	}
}

func TestSaveConfigKeepsSweepEvery(t *testing.T) {
	root := t.TempDir()
	if err := SaveConfig(root, Config{Destination: "/a", SweepEvery: 60}); err != nil {
		t.Fatal(err)
	}
	if c, err := LoadConfig(root); err != nil || c.SweepEvery != 60 {
		t.Fatalf("SweepEvery = %d, %v; want 60", c.SweepEvery, err)
	}
	if err := SaveConfig(root, Config{Destination: "/a", Machine: "LAP-1"}); err != nil {
		t.Fatal(err)
	}
	if c, err := LoadConfig(root); err != nil || c.SweepEvery != 60 || c.Machine != "LAP-1" {
		t.Fatalf("a later save lost SweepEvery: %+v, %v", c, err)
	}
}

func TestIncludeRenderRemovesAndMarks(t *testing.T) {
	root, dest := t.TempDir(), t.TempDir()
	if err := SaveConfig(root, Config{Destination: dest, SweepEvery: 15}); err != nil {
		t.Fatal(err)
	}
	if err := ExcludeRender(root, dest, []string{"notes"}); err != nil {
		t.Fatal(err)
	}
	RenderExcludes(root, dest, nil, true)
	if err := IncludeRender(root, dest, []string{"NOTES"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := LoadConfig(root)
	if cfg.RenderExclude == nil || len(cfg.RenderExclude) != 0 || cfg.SweepEvery != 15 {
		t.Errorf("config = %+v", cfg)
	}
	shared, incl := readShared(dest)
	if len(shared) != 0 || len(incl) != 1 || incl[0] != "NOTES" {
		t.Errorf("shared = %v, incl = %v", shared, incl)
	}
	if got := RenderExcludes(root, dest, nil, true); len(got) != 0 {
		t.Errorf("still excluded: %v", got)
	}
}

func TestIncludeRenderUnknown(t *testing.T) {
	root, dest := t.TempDir(), t.TempDir()
	if err := SaveConfig(root, Config{Destination: dest}); err != nil {
		t.Fatal(err)
	}
	if err := ExcludeRender(root, dest, []string{"notes"}); err != nil {
		t.Fatal(err)
	}
	RenderExcludes(root, dest, nil, true)
	cfgBefore, _ := os.ReadFile(ConfigPath(root))
	sharedBefore, _ := os.ReadFile(SharedExcludePath(dest))
	err := IncludeRender(root, dest, []string{"notes", "ghost"})
	if err == nil || err.Error() != "ghost is not left out of pages" {
		t.Fatalf("err = %v", err)
	}
	cfgAfter, _ := os.ReadFile(ConfigPath(root))
	sharedAfter, _ := os.ReadFile(SharedExcludePath(dest))
	if string(cfgBefore) != string(cfgAfter) || string(sharedBefore) != string(sharedAfter) {
		t.Error("a refused include wrote something")
	}
}

func TestSharedExcludeEmptyInclusionIgnored(t *testing.T) {
	root, dest := t.TempDir(), t.TempDir()
	if err := os.WriteFile(SharedExcludePath(dest), []byte("notes\n!\n!  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := RenderExcludes(root, dest, nil, false); len(got) != 1 || got[0] != "notes" {
		t.Errorf("got %v", got)
	}
}

func TestRenderExcludesInclusionLines(t *testing.T) {
	root, dest := t.TempDir(), t.TempDir()
	if err := SaveConfig(root, Config{Destination: dest, RenderExclude: []string{"notes", "keep"}}); err != nil {
		t.Fatal(err)
	}
	// CRLF endings, a mixed-case mark, and a blank mark.
	if err := os.WriteFile(SharedExcludePath(dest), []byte("# c\r\n!NoTeS\r\n!\r\nother\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(ConfigPath(root))

	got, dropped := RenderExcludesDropped(root, dest, nil, false)
	if len(got) != 2 || got[0] != "other" || got[1] != "keep" || dropped != nil {
		t.Errorf("persist=false: got %v, dropped %v", got, dropped)
	}
	if after, _ := os.ReadFile(ConfigPath(root)); string(after) != string(before) {
		t.Error("persist=false changed the config")
	}

	got, dropped = RenderExcludesDropped(root, dest, nil, true)
	if len(got) != 2 || len(dropped) != 1 || dropped[0] != "notes" {
		t.Errorf("persist=true: got %v, dropped %v", got, dropped)
	}
	if cfg, _ := LoadConfig(root); len(cfg.RenderExclude) != 1 || cfg.RenderExclude[0] != "keep" {
		t.Errorf("config = %v", cfg.RenderExclude)
	}
	if _, dropped = RenderExcludesDropped(root, dest, nil, true); dropped != nil {
		t.Errorf("reported twice: %v", dropped)
	}
}

func TestIncludeRenderDedupesInput(t *testing.T) {
	root, dest := t.TempDir(), t.TempDir()
	if err := SaveConfig(root, Config{Destination: dest, RenderExclude: []string{"notes"}}); err != nil {
		t.Fatal(err)
	}
	if err := IncludeRender(root, dest, []string{"notes", "NOTES"}); err != nil {
		t.Fatal(err)
	}
	if _, incl := readShared(dest); len(incl) != 1 {
		t.Errorf("marks = %v", incl)
	}
}
