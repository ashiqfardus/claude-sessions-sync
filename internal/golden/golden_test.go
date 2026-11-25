package golden

import "testing"

func TestNormalize(t *testing.T) {
	roots := map[string]string{"<ARCHIVE>": `C:\x\arch`, "<R>": "/x/001"}
	for in, want := range map[string]string{
		`saved in C:\x\arch\projects`:          "saved in <ARCHIVE>/projects",
		`{"dest": "C:\\x\\arch\\projects"}`:    `{"dest": "<ARCHIVE>/projects"}`,
		"2026-11-26 14:03:05  PUSH: 2 copied":  "<TIME>  PUSH: 2 copied",
		`a\b`:                                  "a/b",
		"one\r\ntwo\r\n":                       "one\ntwo\n",
		"/x/001 /x/0012 /x/001/a /x/001: gone": "<R> /x/0012 <R>/a <R>: gone",
	} {
		if got := Normalize(in, roots); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
