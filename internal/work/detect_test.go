package work

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// toolUse emits one assistant transcript line holding a single tool_use block.
func toolUse(name string, input map[string]string) string {
	b, _ := json.Marshal(map[string]any{
		"type": "assistant", "cwd": "", "sessionId": "s",
		"message": map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "name": name, "input": input},
		}},
	})
	return strings.Replace(string(b), `"cwd":""`, `"cwd":"/nowhere"`, 1)
}

func write(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCandidatesWriteTools(t *testing.T) {
	p := write(t,
		toolUse("Write", map[string]string{"file_path": "/w/a/r.md"}),
		toolUse("Edit", map[string]string{"file_path": "/w/b/x.sql"}),
		toolUse("MultiEdit", map[string]string{"file_path": "/w/c/y.py"}),
		toolUse("NotebookEdit", map[string]string{"notebook_path": "/w/d/n.ipynb"}),
		toolUse("Read", map[string]string{"file_path": "/w/e/z.md"}),
	)
	got, err := Candidates(p, "/h", 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []Candidate{{"/nowhere", false}, {"/w/a/r.md", true}, {"/w/b/x.sql", true}, {"/w/c/y.py", true}, {"/w/d/n.ipynb", true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestCandidatesCd(t *testing.T) {
	cases := []struct{ cmd, want string }{
		{`cd /w/run && python x.py`, "/w/run"},
		{`cd "/w/My Work/run 1"; ls`, "/w/My Work/run 1"},
		{`cd ~/SQL/po`, "/h/SQL/po"},
		{`cd 'E:\SQL\po'`, `E:\SQL\po`},
		{`cd sub`, ""},
		{`pushd /w/p`, ""},
	}
	for _, c := range cases {
		p := write(t, toolUse("Bash", map[string]string{"command": c.cmd}))
		got, err := Candidates(p, "/h", 0)
		if err != nil {
			t.Fatal(err)
		}
		want := []Candidate{{"/nowhere", false}}
		if c.want != "" {
			want = append(want, Candidate{c.want, false})
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %v want %v", c.cmd, got, want)
		}
	}
}

func TestCandidatesCwd(t *testing.T) {
	p := write(t, `{"type":"user","cwd":"/w/proj"}`)
	got, _ := Candidates(p, "/h", 0)
	if want := []Candidate{{"/w/proj", false}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestCandidatesLongLine(t *testing.T) {
	big := `{"type":"user","message":{"content":"` + strings.Repeat("x", 5<<20) + `"}}`
	p := write(t,
		toolUse("Write", map[string]string{"file_path": "/w/1.md"}), big,
		toolUse("Write", map[string]string{"file_path": "/w/2.md"}))
	got, _ := Candidates(p, "/h", 0)
	if len(got) != 3 || got[1].Path != "/w/1.md" || got[2].Path != "/w/2.md" {
		t.Fatalf("got %v", got)
	}
}

// Bytes before the offset were filed from another machine: only what follows counts.
func TestCandidatesFromOffset(t *testing.T) {
	first := toolUse("Write", map[string]string{"file_path": "/w/theirs/a.md"})
	p := write(t, first, `{"type":"user","cwd":"/w/mine"}`)
	got, err := Candidates(p, "/h", int64(len(first)+1))
	if err != nil {
		t.Fatal(err)
	}
	if want := []Candidate{{"/w/mine", false}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	if got, _ := Candidates(p, "/h", 1<<20); len(got) != 0 {
		t.Fatalf("past the end: %v", got)
	}
}
