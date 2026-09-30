package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedactMasksCredentials(t *testing.T) {
	for _, secret := range []string{
		"sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789",
		"sk-proj-abcdefghijklmnopqrstuvwx1234",
		"ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"AKIAIOSFODNN7EXAMPLE",
		"AIzaSyA1234567890abcdefghijklmnopqrstuv",
		"xoxb-123456789012-abcdefghijkl",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N",
		"hf_abcdefghijklmnopqrstuvwxyzABCDEF12",
		"npm_abcdefghijklmnopqrstuvwxyz0123456789",
		"ya29.a0AfH6SMBabcdefghijklmnopqrstuvwxyz",
		"SG.abcdefghijklmnopqrst.uvwxyzABCDEFGHIJKLMN",
	} {
		got := Redact("token here: " + secret + " end")
		if strings.Contains(got, secret) {
			t.Errorf("not redacted: %s -> %s", secret, got)
		}
	}

	cases := []struct{ in, want string }{
		{"DB_PASSWORD=hunter2hunter2", "DB_PASSWORD=[REDACTED]"},
		{`export OPENAI_API_KEY="abc123def456"`, `export OPENAI_API_KEY="[REDACTED]"`},
		{`{"apiKey": "abcdef123456"}`, `{"apiKey": "[REDACTED]"}`},
		{"Authorization: Bearer abcdefghijkl", "Authorization: Bearer [REDACTED]"},
		{"postgres://app:s3cretpass@db.local/prod", "postgres://app:[REDACTED]@db.local/prod"},
		{"-----BEGIN RSA PRIVATE KEY-----\nMIIEow\n-----END RSA PRIVATE KEY-----", "[REDACTED]"},
		// Formats that used to slip through.
		{"-----BEGIN PGP PRIVATE KEY BLOCK-----\nlQOYBF\n-----END PGP PRIVATE KEY BLOCK-----", "[REDACTED]"},
		{"key:\n-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEA (truncated)", "key:\n[REDACTED]"},
		{`{\"password\":\"hunter2hunter2\"}`, `{\"password\":\"[REDACTED]\"}`},
		{`password = "correct horse battery staple"`, `password = "[REDACTED]"`},
		{"DB_PASS=s3cretvalue", "DB_PASS=[REDACTED]"},
		{"OPENAI_KEY=abcdef123456", "OPENAI_KEY=[REDACTED]"},
		{"Cookie: session=abc123def456", "Cookie: [REDACTED]"},
		{"AccountName=x;AccountKey=abcDEF123456==;", "AccountName=x;AccountKey=[REDACTED];"},
		{"mysql://root:pa/ss@word1@db", "mysql://root:[REDACTED]@word1@db"},
		// Ordinary prose and code survive.
		{"the token count is high", "the token count is high"},
		{"passwords must be hashed", "passwords must be hashed"},
		{"func authenticate(user string)", "func authenticate(user string)"},
		{"https://github.com/example/repo", "https://github.com/example/repo"},
	}
	for _, tc := range cases {
		if got := Redact(tc.in); got != tc.want {
			t.Errorf("Redact(%q)\n got %q\nwant %q", tc.in, got, tc.want)
		}
	}
}

// Pages already published before redaction existed must be rendered again, and a
// transcript that grew inside the timestamp tolerance must not keep a stale page.
func TestRenderRefreshesOnTemplateChangeAndGrowth(t *testing.T) {
	dest := t.TempDir()
	bucket := filepath.Join(dest, "projects", "e--app")
	if err := os.MkdirAll(bucket, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(bucket, "s1.jsonl")
	write := func(content string) {
		t.Helper()
		info, statErr := os.Stat(transcript)
		if err := os.WriteFile(transcript, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if statErr == nil { // keep the old timestamp: growth within the tolerance
			_ = os.Chtimes(transcript, info.ModTime(), info.ModTime())
		}
	}
	write(`{"type":"user","message":{"role":"user","content":"DB_PASSWORD=hunter2hunter2"}}` + "\n")

	if _, err := Archive(dest, Options{}); err != nil {
		t.Fatal(err)
	}
	page := filepath.Join(dest, "html", "e--app", "s1.html")
	if body, _ := os.ReadFile(page); strings.Contains(string(body), "hunter2") {
		t.Fatal("a secret was published")
	}

	// Growth with the same mtime.
	write(`{"type":"user","message":{"role":"user","content":"DB_PASSWORD=hunter2hunter2"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"second turn"}]}}` + "\n")
	res, err := Archive(dest, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Rendered != 1 {
		t.Errorf("a grown transcript was treated as up to date: %+v", res)
	}
	if body, _ := os.ReadFile(page); !strings.Contains(string(body), "second turn") {
		t.Error("page does not show the appended turn")
	}

	// An older template version re-renders everything once.
	if err := os.WriteFile(statePath(filepath.Join(dest, "html")), []byte(`{"version":1,"pages":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if res, _ := Archive(dest, Options{}); res.Rendered != 1 {
		t.Errorf("an older template did not re-render: %+v", res)
	}
	if res, _ := Archive(dest, Options{}); res.UpToDate != 1 || res.Rendered != 0 {
		t.Errorf("a current page was rendered again: %+v", res)
	}
}

// The index prompt was cut by bytes, splitting multi-byte characters.
func TestIndexPromptCutsWholeCharacters(t *testing.T) {
	dest := t.TempDir()
	bucket := filepath.Join(dest, "projects", "e--app")
	if err := os.MkdirAll(bucket, 0o755); err != nil {
		t.Fatal(err)
	}
	prompt := strings.Repeat("আমার", 60)
	if err := os.WriteFile(filepath.Join(bucket, "s1.jsonl"),
		[]byte(`{"type":"user","message":{"role":"user","content":"`+prompt+`"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Archive(dest, Options{}); err != nil {
		t.Fatal(err)
	}
	index, _ := os.ReadFile(filepath.Join(dest, "html", "index.html"))
	if strings.ContainsRune(string(index), '�') || !strings.Contains(string(index), "আমার...") {
		t.Error("the prompt was cut mid-character")
	}
}
