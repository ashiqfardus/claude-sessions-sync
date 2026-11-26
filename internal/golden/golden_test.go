package golden

import "testing"

func TestNormalize(t *testing.T) {
	roots := map[string]string{"<ARCHIVE>": `C:\x\arch`, "<R>": "/x/001", "<D>": "/y/dir."}
	for in, want := range map[string]string{
		`saved in C:\x\arch\projects`:          "saved in <ARCHIVE>/projects",
		`{"dest": "C:\\x\\arch\\projects"}`:    `{"dest": "<ARCHIVE>/projects"}`,
		"2026-11-26 14:03:05  PUSH: 2 copied":  "<TIME>  PUSH: 2 copied",
		`a\b`:                                  "a/b",
		"one\r\ntwo\r\n":                       "one\ntwo\n",
		"/x/001 /x/0012 /x/001/a /x/001: gone": "<R> /x/0012 <R>/a <R>: gone",
		// A root that ends in neither a letter nor a digit is still whole at its end.
		"/y/dir. /y/dir.x /y/dir./a /y/dir.": "<D> /y/dir.x <D>/a <D>",
		"/x/001/x/001":                       "<R><R>",
	} {
		if got := Normalize(in, roots); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
