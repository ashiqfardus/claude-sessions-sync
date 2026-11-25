package convzip

// Two machines sharing one bucket: the zip one of them writes can arrive before its
// deletions of the loose files do, and both can zip the same conversation at once.
// Nothing either does may take the last copy of a turn.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/work"
)

// Another machine's zip lands at the zip's name while this one checks its own: a check
// that fails, or one that passes, never removes or writes over that zip, and the loose
// files stay.
func TestZipChecksBeforeItsZipIsInPlace(t *testing.T) {
	for name, fail := range map[string]bool{"check fails": true, "check passes": false} {
		t.Run(name, func(t *testing.T) {
			dir, _ := bucket(t)
			before := snapshot(t, dir)
			other := "another machine's zip"
			defer func(old func(string) (map[string]work.PackEntry, error)) { readBack = old }(readBack)
			readBack = func(p string) (map[string]work.PackEntry, error) {
				put(t, Path(dir, testID), other, time.Now())
				if fail {
					return nil, os.ErrClosed
				}
				return work.ReadPackIndex(p)
			}
			if _, err := Zip(dir, testID); err == nil {
				t.Fatal("want error")
			}
			if b, err := os.ReadFile(Path(dir, testID)); err != nil || string(b) != other {
				t.Fatalf("the other machine's zip was removed or written over: %q, %v", b, err)
			}
			after := snapshot(t, dir)
			delete(after, Path(dir, testID))
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("loose files changed: %d of %d left", len(after), len(before))
			}
			ents, _ := os.ReadDir(dir)
			for _, e := range ents {
				if strings.HasPrefix(e.Name(), ".tmp-") {
					t.Fatalf("left %s", e.Name())
				}
			}
		})
	}
}

// A link in the conversation's folder is never packed, so the folder would outlive the
// zip, and every later sweep take it for a run killed half way: it stays loose.
func TestZipRefusesAFolderWithALink(t *testing.T) {
	dir, _ := bucket(t)
	symlink(t, filepath.Join(dir, testID+".jsonl"), filepath.Join(dir, testID, "subagents", "odd"))
	before := snapshot(t, dir)
	if _, err := Zip(dir, testID); err == nil {
		t.Fatal("zipped a folder holding a link")
	}
	if exists(Path(dir, testID)) || !reflect.DeepEqual(before, snapshot(t, dir)) {
		t.Fatal("touched")
	}
}

// saved is a loose file as machine A zipped it.
type saved struct {
	path, data string
	mt         time.Time
}

func save(t *testing.T, paths ...string) []saved {
	t.Helper()
	var out []saved
	for _, p := range paths {
		b, err := os.ReadFile(p)
		st, serr := os.Stat(p)
		if err != nil || serr != nil {
			t.Fatal(err, serr)
		}
		out = append(out, saved{p, string(b), st.ModTime()})
	}
	return out
}

// deletesLand applies A's deletions on B: each file A zipped, where it is still as A
// left it.
func deletesLand(files []saved) {
	for _, f := range files {
		if b, err := os.ReadFile(f.path); err == nil && string(b) == f.data {
			os.Remove(f.path)
		}
	}
}

// holdsEverything fails unless the bucket still holds the conversation: every turn of
// the transcript, and each file of its folder.
func holdsEverything(t *testing.T, dir string) {
	t.Helper()
	got, cleanup, err := Materialize(dir, testID, t.TempDir())
	if err != nil {
		t.Fatalf("the conversation is gone from the bucket: %v", err)
	}
	defer cleanup()
	if b, _ := os.ReadFile(got); string(b) != turn(1)+turn(2)+turn(3) {
		t.Errorf("the transcript lost turns:\n%s", b)
	}
	for rel, want := range map[string]string{"subagents/agent-a.jsonl": turn(4), "tool-results/x.txt": strings.Repeat("out\n", 100)} {
		if b, err := os.ReadFile(filepath.Join(filepath.Dir(got), testID, filepath.FromSlash(rel))); err != nil || string(b) != want {
			t.Errorf("%s is gone or changed: %v", rel, err)
		}
	}
}

// A zipped the conversation in the shared bucket; on B the zip has arrived, A's
// deletions of the loose files not yet. B finds the zip holds every loose file, so it
// removes them, as A is doing, and leaves A's zip: once A's deletions land, the zip
// still holds the conversation. Also when A's deletion of the transcript came first.
func TestDropHeldLeavesTheOtherMachinesZip(t *testing.T) {
	for name, transcript := range map[string]bool{"every loose file": true, "folder files only": false} {
		t.Run(name, func(t *testing.T) {
			dir, _ := bucket(t)
			paths := []string{filepath.Join(dir, testID, "subagents", "agent-a.jsonl"), filepath.Join(dir, testID, "tool-results", "x.txt")}
			if transcript {
				paths = append(paths, filepath.Join(dir, testID+".jsonl"))
			}
			files := save(t, paths...)
			if _, err := Zip(dir, testID); err != nil { // A
				t.Fatal(err)
			}
			for _, f := range files { // B's view: the zip, and A's loose files
				put(t, f.path, f.data, f.mt)
			}
			zst, _ := os.Stat(Path(dir, testID))
			if err := DropHeld(dir, testID); err != nil { // B's sweep
				t.Fatal(err)
			}
			if st, err := os.Stat(Path(dir, testID)); err != nil || !os.SameFile(zst, st) {
				t.Fatalf("A's zip was removed or written again: %v", err)
			}
			if left := snapshot(t, dir); len(left) != 1 {
				t.Errorf("loose files stay beside the zip: %v", left)
			}
			deletesLand(files)
			holdsEverything(t, dir)
		})
	}
}

// A loose file the zip does not hold - a turn it lacks, a tool output of another size -
// or a zip that cannot be read in full changes nothing.
func TestDropHeldChangesNothingUnlessTheZipHoldsAll(t *testing.T) {
	for name, change := range map[string]func(dir string){
		"a turn the zip lacks": func(dir string) {
			put(t, filepath.Join(dir, testID+".jsonl"), turn(1)+turn(2)+turn(3)+turn(9), time.Now())
		},
		"a tool output of another size": func(dir string) {
			put(t, filepath.Join(dir, testID, "tool-results", "x.txt"), "other", time.Now())
		},
		"a zip that cannot be read": func(dir string) {
			zp := Path(dir, testID)
			b, _ := os.ReadFile(zp)
			os.WriteFile(zp, []byte(strings.Repeat("x", len(b))), 0o644)
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir, _ := bucket(t)
			files := save(t, filepath.Join(dir, testID+".jsonl"), filepath.Join(dir, testID, "subagents", "agent-a.jsonl"),
				filepath.Join(dir, testID, "tool-results", "x.txt"))
			if _, err := Zip(dir, testID); err != nil {
				t.Fatal(err)
			}
			for _, f := range files {
				put(t, f.path, f.data, f.mt)
			}
			change(dir)
			before := snapshot(t, dir)
			if err := DropHeld(dir, testID); err == nil {
				t.Fatal("dropped loose files the zip does not hold")
			}
			if !reflect.DeepEqual(before, snapshot(t, dir)) {
				t.Fatal("touched")
			}
		})
	}
}

// unzipKilledAfter is a bucket whose Unzip was killed after k of its 3 entries were
// written, before the zip was removed.
func unzipKilledAfter(t *testing.T, k int) (dir string, zip string) {
	t.Helper()
	dir, _ = bucket(t)
	if _, err := Zip(dir, testID); err != nil {
		t.Fatal(err)
	}
	zp := Path(dir, testID)
	zip = snapshot(t, dir)[zp]
	zst, _ := os.Stat(zp)
	defer func(old func(string, string) error) { copyFile = old }(copyFile)
	written := 0
	copyFile = func(src, dst string) error {
		if written == k {
			return os.ErrDeadlineExceeded // killed here
		}
		written++
		return archive.CopyFile(src, dst)
	}
	err := Unzip(dir, testID)
	if k == 3 { // every entry written: killed just before the zip's removal
		put(t, zp, zip, zst.ModTime())
	} else if err == nil {
		t.Fatalf("after %d entries: want the kill", k)
	}
	if b, err := os.ReadFile(zp); err != nil || string(b) != zip {
		t.Fatalf("after %d entries: the zip went or changed: %v", k, err)
	}
	if n := len(snapshot(t, dir)) - 1; n != k {
		t.Fatalf("after %d entries: %d loose file(s) written", k, n)
	}
	return dir, zip
}

// A run killed at each step of Unzip - after some entries are written, or all of them -
// leaves the zip in place and every turn readable, and the next run finishes the job:
// the sweep, which keeps the zip, or another unzip.
func TestUnzipKilledAtEachStep(t *testing.T) {
	for k := 0; k <= 3; k++ {
		dir, zip := unzipKilledAfter(t, k)
		holdsEverything(t, dir)
		if err := DropHeld(dir, testID); err != nil {
			t.Fatalf("after %d entries: the sweep: %v", k, err)
		}
		if left := snapshot(t, dir); len(left) != 1 || left[Path(dir, testID)] != zip {
			t.Fatalf("after %d entries: the sweep left %d file(s), or changed the zip", k, len(left))
		}
		holdsEverything(t, dir)

		dir, _ = unzipKilledAfter(t, k)
		if err := Unzip(dir, testID); err != nil {
			t.Fatalf("after %d entries: %v", k, err)
		}
		if exists(Path(dir, testID)) {
			t.Fatalf("after %d entries: the next unzip left the zip", k)
		}
		holdsEverything(t, dir)
	}
}

// Another machine's zip replaces the one Unzip read while Unzip writes the entries:
// that zip stays, beside the loose files written, for the sweep to settle later.
func TestUnzipKeepsAZipReplacedMeanwhile(t *testing.T) {
	dir, _ := bucket(t)
	if _, err := Zip(dir, testID); err != nil {
		t.Fatal(err)
	}
	zp := Path(dir, testID)
	other := "another machine's newer zip"
	defer func(old func(string, string) error) { copyFile = old }(copyFile)
	once := false
	copyFile = func(src, dst string) error {
		if !once {
			once = true
			put(t, zp, other, time.Now())
		}
		return archive.CopyFile(src, dst)
	}
	if err := Unzip(dir, testID); err != nil {
		t.Fatalf("unzip: %v", err)
	}
	if b, err := os.ReadFile(zp); err != nil || string(b) != other {
		t.Errorf("Unzip removed the zip that replaced the one it read: %q %v", b, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, testID+".jsonl")); string(b) != turn(1)+turn(2)+turn(3) {
		t.Errorf("the loose transcript was not written: %q", b)
	}
}
