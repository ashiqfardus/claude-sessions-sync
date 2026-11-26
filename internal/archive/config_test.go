package archive

import "testing"

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
