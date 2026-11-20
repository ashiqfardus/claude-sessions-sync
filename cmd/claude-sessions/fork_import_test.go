package main_test

// A session continued on two machines comes across as a second session, so either
// side can be resumed and neither is lost. It must not come back again as a third.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
)

// forkFixture: this machine has the api repo and its own copy of forkSession; the
// archive holds MAC-BOX's copy of the same session, recorded at a different path.
func forkFixture(t *testing.T, local, archived string) (importFixture, string) {
	t.Helper()
	f := newImportFixture(t)
	repo := f.makeRepo(t, "api", "git@github.com:example/api.git")
	mustWrite(t, filepath.Join(f.claudeDir, "projects", claude.Slug(repo), forkSession+".jsonl"), local)
	mustWrite(t, filepath.Join(f.archive, "projects", "-Users-me-api", forkSession+".jsonl"), archived)
	f.writeShardAs(t, "MAC-BOX", map[string]any{
		"-Users-me-api": map[string]any{"path": "/Users/me/api", "leaf": "api", "remote": "git@github.com:example/api.git", "seen": "2026-11-20"},
	})
	return f, repo
}

func (f importFixture) writeShardAs(t *testing.T, machine string, projects map[string]any) {
	t.Helper()
	mustWrite(t, filepath.Join(f.archive, "manifest", machine+".json"),
		`{"schemaVersion":1,"machine":"`+machine+`","projects":`+mustJSON(t, projects)+`}`)
}

func TestImportUpdateFilesTheOtherCopyOfAForkedSession(t *testing.T) {
	f, repo := forkFixture(t, uuidLines("u1", "u2", "w1"), uuidLines("u1", "u2", "m1"))

	out, err := f.run(t, "import", "--search-root", f.dev, "--update")
	if err != nil {
		t.Fatalf("import failed: %v\n%s", err, out)
	}
	bucket := filepath.Join(f.claudeDir, "projects", claude.Slug(repo))
	if got := mustRead(t, filepath.Join(bucket, forkSession+".jsonl")); !strings.Contains(got, `"w1"`) || strings.Contains(got, `"m1"`) {
		t.Errorf("the local copy was changed:\n%s", got)
	}
	fork := claude.ForkID(forkSession, "-users-me-api@mac-box")
	body := mustRead(t, filepath.Join(bucket, fork+".jsonl"))
	if !strings.Contains(body, `"m1"`) || strings.Contains(body, forkSession) || !strings.Contains(body, fork) {
		t.Errorf("the other copy was not filed as its own session:\n%s", body)
	}
	if !strings.Contains(out, fork) {
		t.Errorf("import did not name the session it filed:\n%s", out)
	}

	// Again: the fork is already here, so nothing new appears.
	if out, err := f.run(t, "import", "--search-root", f.dev, "--update"); err != nil {
		t.Fatalf("second import failed: %v\n%s", err, out)
	}
	if n := len(f.localBucket(t, claude.Slug(repo))); n != 2 {
		t.Errorf("want 2 sessions after importing twice, have %d: %v", n, f.localBucket(t, claude.Slug(repo)))
	}
}

// A session that only continued here is not a fork: nothing to file, nothing to warn.
func TestImportUpdateIgnoresAnArchivedCopyThatIsBehind(t *testing.T) {
	f, repo := forkFixture(t, uuidLines("u1", "u2", "w1"), uuidLines("u1", "u2"))

	out, err := f.run(t, "import", "--search-root", f.dev, "--update")
	if err != nil {
		t.Fatalf("import failed: %v\n%s", err, out)
	}
	if n := len(f.localBucket(t, claude.Slug(repo))); n != 1 {
		t.Errorf("an older archived copy was filed as a fork: %v", f.localBucket(t, claude.Slug(repo)))
	}
	if strings.Contains(out, "elsewhere") {
		t.Errorf("a session that only continued here was reported as forked:\n%s", out)
	}
}

// The other machine filed this machine's copy as a fork and pushed it back. It is
// this machine's own conversation, so it must not arrive as yet another session.
func TestImportDoesNotBringBackAForkOfItsOwnSession(t *testing.T) {
	f, repo := forkFixture(t, uuidLines("u1", "u2", "w1"), uuidLines("u1", "u2", "w1"))
	me := f.machineName(t)
	echo := claude.ForkID(forkSession, strings.ToLower(claude.Slug(repo)+"@"+me))
	mustWrite(t, filepath.Join(f.archive, "projects", "-Users-me-api", echo+".jsonl"), uuidLines("u1", "u2", "w1"))

	out, err := f.run(t, "import", "--search-root", f.dev, "--update")
	if err != nil {
		t.Fatalf("import failed: %v\n%s", err, out)
	}
	if got := f.localBucket(t, claude.Slug(repo)); contains(got, echo+".jsonl") {
		t.Errorf("a fork of this machine's own session came back: %v\n%s", got, out)
	}
}

// On a shared layout, push keeps a forked copy under forks/; import files the other
// machine's, and never this machine's own.
func TestImportFilesForksPushedByAnotherMachine(t *testing.T) {
	f := newImportFixture(t)
	repo := f.makeRepo(t, "api", "git@github.com:example/api.git")
	bucket := claude.Slug(repo)
	mustWrite(t, filepath.Join(f.archive, "projects", bucket, forkSession+".jsonl"), uuidLines("u1", "u2", "b1"))
	mustWrite(t, filepath.Join(f.archive, "forks", bucket, forkSession, "OTHER-BOX.jsonl"), uuidLines("u1", "u2", "a1"))
	me := f.machineName(t)
	mustWrite(t, filepath.Join(f.archive, "forks", bucket, forkSession, me+".jsonl"), uuidLines("u1", "u2", "c1"))
	f.writeShardAs(t, "OTHER-BOX", map[string]any{
		bucket: map[string]any{"path": repo, "leaf": "api", "remote": "git@github.com:example/api.git", "seen": "2026-11-20"},
	})

	out, err := f.run(t, "import", "--search-root", f.dev)
	if err != nil {
		t.Fatalf("import failed: %v\n%s", err, out)
	}
	theirs := claude.ForkID(forkSession, strings.ToLower(bucket+"@OTHER-BOX"))
	if body := mustRead(t, filepath.Join(f.claudeDir, "projects", bucket, theirs+".jsonl")); !strings.Contains(body, `"a1"`) {
		t.Errorf("the other machine's fork was not filed:\n%s", body)
	}
	if n := len(f.localBucket(t, bucket)); n != 2 {
		t.Errorf("want the session and one fork, have %v\n%s", f.localBucket(t, bucket), out)
	}
}

// machineName is the name this build writes its shard under, learnt by pushing once
// into a scratch archive.
func (f importFixture) machineName(t *testing.T) string {
	t.Helper()
	scratch := importFixture{claudeDir: f.claudeDir, archive: t.TempDir(), dev: f.dev}
	if out, err := scratch.run(t, "push", "--no-html"); err != nil {
		t.Fatalf("push failed: %v\n%s", err, out)
	}
	shards, _ := filepath.Glob(filepath.Join(scratch.archive, "manifest", "*.json"))
	if len(shards) != 1 {
		t.Fatalf("want one shard, have %v", shards)
	}
	os.RemoveAll(scratch.archive)
	return strings.TrimSuffix(filepath.Base(shards[0]), ".json")
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
