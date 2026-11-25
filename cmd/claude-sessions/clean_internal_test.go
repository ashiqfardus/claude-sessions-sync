package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
)

const cleanID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

func writeFile(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The last check before deleting: a copy and the one it is kept in must be two.
func TestVerifyNeverKeepsACopyForItself(t *testing.T) {
	dest := t.TempDir()
	writeFile(t, filepath.Join(archive.ProjectsDir(dest), "B", cleanID+".jsonl"), `{"type":"user","uuid":"u1"}`+"\n")
	for _, in := range []string{"B", "b"} {
		if err := (dupCopy{Bucket: "B", ID: cleanID, In: in}).verify(dest); err == nil {
			t.Errorf("verify let B's copy be deleted for %s's, the same file", in)
		}
	}
}

// On a case-sensitive drive a second file, its id in capitals, made the one real copy
// look held by itself; so did a planted bucket named as a listed one in another case.
func TestCleanLeavesASessionWhoseBucketIsNamedTwice(t *testing.T) {
	for _, twin := range []struct{ bucket, id string }{
		{"B", strings.ToUpper(cleanID)},
		{"b", cleanID},
	} {
		dest := t.TempDir()
		projects := archive.ProjectsDir(dest)
		if err := os.MkdirAll(projects, 0o755); err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS == "windows" {
			_ = exec.Command("fsutil.exe", "file", "setCaseSensitiveInfo", projects, "enable").Run()
		}
		if err := os.MkdirAll(filepath.Join(projects, "B"), 0o755); err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS == "windows" {
			_ = exec.Command("fsutil.exe", "file", "setCaseSensitiveInfo", filepath.Join(projects, "B"), "enable").Run()
		}
		own := filepath.Join(projects, "B", cleanID+".jsonl")
		writeFile(t, own, `{"type":"user","uuid":"u1"}`+"\n"+`{"type":"assistant","uuid":"m1"}`+"\n")
		planted := filepath.Join(projects, twin.bucket, twin.id+".jsonl")
		if twin.bucket != "B" {
			_ = os.Mkdir(filepath.Dir(planted), 0o755)
		}
		if _, err := os.Stat(planted); err == nil {
			t.Skip("this drive is not case-sensitive")
		}
		writeFile(t, planted, `{"type":"user","uuid":"u1"}`+"\n"+`{"type":"assistant","uuid":"m1"}`+"\n"+`{"type":"assistant","uuid":"zz"}`+"\n")
		writeFile(t, filepath.Join(archive.ManifestDir(dest), "M.json"), `{"machine":"M","skipsHeld":true,"projects":{"B":{"path":"/x"}}}`)

		if plan := planClean(dest, false); len(plan.Remove) > 0 {
			t.Errorf("%s/%s: clean would delete %+v", twin.bucket, twin.id, plan.Remove)
		}
	}
}
