package rewrite

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestApplyRewritesEveryForm(t *testing.T) {
	rules := Rules(`E:\airv2-frontend-web`, `E:\airos-frontend`, false)
	cases := []struct{ in, want string }{
		{`{"cwd":"E:\\airv2-frontend-web"}`, `{"cwd":"E:\\airos-frontend"}`},
		{`{"cwd":"e:\\AIRV2-frontend-web\\src\\App.tsx"}`, `{"cwd":"E:\\airos-frontend\\src\\App.tsx"}`},
		{`{"t":"E:/airv2-frontend-web/src"}`, `{"t":"E:/airos-frontend/src"}`},
		{`{"t":"projects/e--airv2-frontend-web/x.jsonl"}`, `{"t":"projects/E--airos-frontend/x.jsonl"}`},
		// A path right after a \n escape in tool output.
		{`{"t":"ok\nE:\\airv2-frontend-web\\a"}`, `{"t":"ok\nE:\\airos-frontend\\a"}`},
		// A sentence-ending full stop is not part of the path.
		{`{"t":"files in E:/airv2-frontend-web."}`, `{"t":"files in E:/airos-frontend."}`},
	}
	for _, tc := range cases {
		if got := Apply(tc.in, rules, nil); got != tc.want {
			t.Errorf("Apply(%s)\n got %s\nwant %s", tc.in, got, tc.want)
		}
	}
}

// Rewriting /home/a/proj used to rewrite every sibling that shares the prefix too,
// silently repointing sessions that mention another repository.
func TestApplyRespectsPathBoundaries(t *testing.T) {
	rules := Rules("/home/a/proj", "/work/proj", false)
	for _, untouched := range []string{
		`{"t":"/home/a/proj2/src"}`,
		`{"t":"/home/a/proj-old"}`,
		`{"t":"/home/a/proj.git"}`,
		`{"t":"/mnt/home/a/proj"}`,
		`{"t":"projects/-home-a-proj2"}`,
	} {
		if got := Apply(untouched, rules, nil); got != untouched {
			t.Errorf("a sibling path was rewritten:\n  %s\n  %s", untouched, got)
		}
	}
	hits := map[string]int{}
	got := Apply(`{"a":"/home/a/proj","b":"/home/a/proj2","c":"/home/a/proj/x"}`, rules, hits)
	if got != `{"a":"/work/proj","b":"/home/a/proj2","c":"/work/proj/x"}` {
		t.Errorf("mixed line = %s", got)
	}
	// A POSIX path is identical in the escaped, forward-slash and literal forms, so
	// the first rule takes the hit and the rest find nothing left to do.
	if len(hits) != 1 || hits["json-escaped"] != 1 {
		t.Errorf("hits = %v", hits)
	}
}

func TestApplyTreatsDollarLiterally(t *testing.T) {
	rules := Rules("/old/p", "/new/$1p", false)
	if got := Apply(`{"t":"/old/p"}`, rules, nil); got != `{"t":"/new/$1p"}` {
		t.Errorf("got %s", got)
	}
}

func TestBareNameAcrossOperatingSystems(t *testing.T) {
	// On Linux filepath.Base of a Windows path is the whole string.
	rules := Rules(`E:\old-name`, "/home/me/new-name", true)
	found := false
	for _, r := range rules {
		if r.Label == "bare-name" {
			found = r.Find == "old-name" && r.Replace == "new-name"
		}
	}
	if !found {
		t.Errorf("bare-name rule wrong: %+v", rules)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A truncated last line from a killed session, or a BOM, used to abort the whole
// transcript - and import then abandoned the rest of the project.
func TestFileKeepsPreBrokenLinesAndStripsBOM(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.jsonl")
	dst := filepath.Join(dir, "out", "in.jsonl")
	writeFile(t, src, "\xef\xbb\xbf"+`{"cwd":"/home/a/proj"}`+"\r\n"+
		`{"type":"assistant","t":"/home/a/proj/x"}`+"\n"+
		`{"type":"user","message":{"content":"/home/a/pr`) // truncated, no newline

	old := time.Date(2026, 3, 14, 9, 26, 53, 0, time.Local)
	if err := os.Chtimes(src, old, old); err != nil {
		t.Fatal(err)
	}

	res, err := File(src, dst, Rules("/home/a/proj", "/work/proj", false), "")
	if err != nil {
		t.Fatalf("a pre-broken line must not fail the transcript: %v", err)
	}
	if res.Unparseable != 1 || res.Lines != 3 {
		t.Errorf("result = %+v", res)
	}
	raw, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"cwd":"/work/proj"}` + "\n" + `{"type":"assistant","t":"/work/proj/x"}` + "\n" +
		`{"type":"user","message":{"content":"/home/a/pr` + "\n"
	if string(raw) != want {
		t.Errorf("output:\n%s\nwant:\n%s", raw, want)
	}
	if info, _ := os.Stat(dst); !info.ModTime().Equal(old) {
		t.Errorf("mtime not preserved: %v", info.ModTime())
	}
}

// A line that was valid and is not after the rewrite means the rules are wrong: keep
// nothing rather than a corrupt transcript.
func TestFileRejectsRewriteThatBreaksJSON(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.jsonl")
	dst := filepath.Join(dir, "in-out.jsonl")
	writeFile(t, src, `{"cwd":"/home/a/proj"}`+"\n")

	_, err := File(src, dst, Rules("/home/a/proj", `/work/"quoted`, false), "")
	if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Error("a corrupt destination was left behind")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temporary files left behind: %d entries", len(entries))
	}
}

// A session in a multi-folder workspace names several repositories, sometimes in
// Git Bash form, and a parent folder must not claim a child repository that moved.
func TestRulesForMovesWorkspace(t *testing.T) {
	rules := RulesForMoves([]Move{
		{From: `E:\work`, To: `D:\code`},
		{From: `E:\work\api`, To: `D:\elsewhere\api`},
		{From: `E:\work\api`, To: `D:\ignored-duplicate`},
	})
	in := `{"a":"E:\work\web","b":"E:\work\api\src","c":"cd /e/work/api && ls"}`
	want := `{"a":"D:\code\web","b":"D:\elsewhere\api\src","c":"cd /d/elsewhere/api && ls"}`
	if got := Apply(in, rules, nil); got != want {
		t.Errorf("\n got %s\nwant %s", got, want)
	}
}

func tailRules(convert bool) []Rule {
	return RulesForMoves([]Move{{From: `E:\SQL`, To: "/Users/b/SQL", ConvertTail: convert}})
}

func TestConvertTail(t *testing.T) {
	got := Apply(`{"t":"open E:\\SQL\\po\\x.md now"}`, tailRules(true), nil)
	if want := `{"t":"open /Users/b/SQL/po/x.md now"}`; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestConvertTailStopsAtEscapes(t *testing.T) {
	for in, want := range map[string]string{
		`E:\\SQL\\po\nnext`:   `/Users/b/SQL/po\nnext`,
		`"E:\\SQL\\a\"`:       `"/Users/b/SQL/a\"`,
		`E:\\SQL\\My Work\\x`: `/Users/b/SQL/My Work\\x`,
	} {
		if got := Apply(in, tailRules(true), nil); got != want {
			t.Errorf("%s: got %s want %s", in, got, want)
		}
	}
}

// The same move twice, once converting the tail: the first is kept, converting too.
func TestConvertTailSurvivesDedupe(t *testing.T) {
	rules := RulesForMoves([]Move{{From: `E:\SQL`, To: "/Users/b/SQL"}, {From: `e:\sql\`, To: "/Users/b/SQL", ConvertTail: true}})
	got := Apply(`{"t":"open E:\\SQL\\po\\x.md now"}`, rules, nil)
	if want := `{"t":"open /Users/b/SQL/po/x.md now"}`; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestNoTailByDefault(t *testing.T) {
	got := Apply(`{"t":"open E:\\SQL\\po\\x.md now"}`, tailRules(false), nil)
	if want := `{"t":"open /Users/b/SQL\\po\\x.md now"}`; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestTailStillValidJSON(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "s.jsonl"), filepath.Join(dir, "d.jsonl")
	in := `{"cwd":"E:\\SQL\\po"}` + "\n" + `{"t":"E:\\SQL\\a\\b.md\nE:\\SQL\\c"}` + "\n" + `{"t":"none"}` + "\n"
	if err := os.WriteFile(src, []byte(in), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := File(src, dst, tailRules(true), "")
	if err != nil || res.Unparseable != 0 {
		t.Fatalf("err=%v unparseable=%d", err, res.Unparseable)
	}
	b, _ := os.ReadFile(dst)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	var v struct{ Cwd, T string }
	json.Unmarshal([]byte(lines[0]), &v)
	if v.Cwd != "/Users/b/SQL/po" {
		t.Errorf("cwd %q", v.Cwd)
	}
	json.Unmarshal([]byte(lines[1]), &v)
	if want := "/Users/b/SQL/a/b.md\n/Users/b/SQL/c"; v.T != want {
		t.Errorf("t %q want %q", v.T, want)
	}
}

// Moves for one place, and for its parent, all converting: each path is converted
// once. Of two moves from one path to different places the first wins, and does not
// take the other's flag.
func TestConvertTailNoDoubleConversion(t *testing.T) {
	rules := RulesForMoves([]Move{
		{From: `E:\SQL\po`, To: "/Users/b/SQL/po", ConvertTail: true}, // project
		{From: `E:\SQL\po`, To: "/Users/b/SQL/po", ConvertTail: true}, // work
		{From: `E:\SQL`, To: "/Users/b/SQL", ConvertTail: true},       // remap/work parent
		{From: `C:\Users\a\.claude`, To: "/Users/b/.claude"},
	})
	in := `{"a":"E:\\SQL\\po\\sub\\x.md","b":"E:\\SQL\\other\\y.md","c":"cd E:\\SQL\\po && dir","d":"C:\\Users\\a\\.claude\\projects\\p"}`
	want := `{"a":"/Users/b/SQL/po/sub/x.md","b":"/Users/b/SQL/other/y.md","c":"cd /Users/b/SQL/po && dir","d":"/Users/b/.claude\\projects\\p"}`
	if got := Apply(in, rules, nil); got != want {
		t.Fatalf("\ngot  %s\nwant %s", got, want)
	}
	rules = RulesForMoves([]Move{{From: `E:\SQL`, To: "/Users/b/A"}, {From: `E:\SQL`, To: "/Users/b/B", ConvertTail: true}})
	if got := Apply(`{"a":"E:\\SQL\\x"}`, rules, nil); got != `{"a":"/Users/b/A\\x"}` {
		t.Fatalf("got %s", got)
	}
}
