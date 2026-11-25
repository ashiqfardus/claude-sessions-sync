package archive

import (
	"os"
	"reflect"
	"testing"
)

func TestRetentionRoundTrip(t *testing.T) {
	dest := t.TempDir()

	got, err := LoadRetention(dest)
	if err != nil || !reflect.DeepEqual(got, Retention{}) {
		t.Fatalf("missing file: got %+v, %v; want the zero value and no error", got, err)
	}

	want := Retention{KeepDays: 365, Keep: []KeepEntry{
		{Session: "11111111-2222-3333-4444-555555555555"},
		{Remote: "git@github.com:example/web-app.git"},
		{Leaf: "api"},
	}}
	if err := SaveRetention(dest, want); err != nil {
		t.Fatal(err)
	}
	if got, err = LoadRetention(dest); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip: got %+v, %v; want %+v", got, err, want)
	}

	// Anything that is not a valid file is an error, so that callers delete nothing:
	// a truncated download or a hand edit must never read as "nothing is kept".
	for name, body := range map[string]string{
		"invalid JSON":                  `{"keepDays": 365, "keep": [`,
		"empty":                         "",
		"keep-days under 90":            `{"keepDays": 30}`,
		"negative keep-days":            `{"keepDays": -1}`,
		"keep-days that would overflow": `{"keepDays": 200000}`,
		"entry with two fields":         `{"keep": [{"leaf": "api", "remote": "git@github.com:example/api.git"}]}`,
		"empty entry":                   `{"keep": [{}]}`,
	} {
		if err := os.WriteFile(RetentionPath(dest), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadRetention(dest); err == nil {
			t.Errorf("%s: LoadRetention gave no error", name)
		}
	}
}
