//go:build !windows

package main

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// sameContent reads neither file when one is missing or their sizes differ: on a drive
// that downloads each file as it is opened, the read was a download. A named pipe
// stands for such a file - reading it would wait for a writer that never comes.
func TestSameContentReadsNothingTheSizesAnswer(t *testing.T) {
	dir := t.TempDir()
	slow, other := filepath.Join(dir, "slow.md"), filepath.Join(dir, "other.md")
	if err := syscall.Mkfifo(slow, 0o644); err != nil {
		t.Skip("no named pipes here:", err)
	}
	writeFile(t, other, "a different size")
	for _, b := range []string{filepath.Join(dir, "missing.md"), other} {
		done := make(chan bool)
		go func() { done <- sameContent(slow, b) }()
		select {
		case same := <-done:
			if same {
				t.Errorf("sameContent(pipe, %s) = true", filepath.Base(b))
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("sameContent read the archived file to compare it with %s", filepath.Base(b))
		}
	}
	writeFile(t, other+"2", "same")
	writeFile(t, other+"3", "same")
	if !sameContent(other+"2", other+"3") {
		t.Error("two equal files differ")
	}
}
