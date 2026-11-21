package work

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestHeldRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "base", "work-held.json")
	if h := LoadHeld(path); h.Files == nil || h.Placed == nil || len(h.Files)+len(h.Placed) != 0 {
		t.Fatalf("missing file: %+v", h)
	}
	h := LoadHeld(path)
	h.Files["/Users/b/SQL/po/r.md"] = HeldFile{Machine: "WIN", From: `E:\SQL\po\r.md`, Size: 12,
		MTime: time.Date(2026, 11, 21, 5, 0, 0, 0, time.UTC)}
	h.Placed[`WIN|E:\SQL\po`] = "/Users/b/claude-work/WIN/E/SQL/po"
	if err := h.Save(path); err != nil {
		t.Fatal(err)
	}
	if got := LoadHeld(path); !reflect.DeepEqual(got, h) {
		t.Fatalf("round trip: %+v, want %+v", got, h)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := LoadHeld(path); got.Err == nil || len(got.Files)+len(got.Placed) != 0 || got.Files == nil {
		t.Fatalf("corrupt record: %+v", got)
	}
}
