package rewrite

import "testing"

// A move's From comes from the archive. "e" or "/" there would rewrite every
// occurrence in every transcript, and the damaged lines still parse.
func TestRulesForMovesIgnoresNonPaths(t *testing.T) {
	for _, from := range []string{"e", "/", `C:\`, "C:", "//host/share", "", "work/app"} {
		if rules := RulesForMoves([]Move{{From: from, To: `D:\new`}}); len(rules) != 0 {
			t.Errorf("RulesForMoves(%q) built %d rule(s)", from, len(rules))
		}
	}
	for _, from := range []string{`E:\work\app`, "E:/work/app", "/home/me/app"} {
		if !AbsoluteAnywhere(from) || len(RulesForMoves([]Move{{From: from, To: `D:\new`}})) == 0 {
			t.Errorf("a real path %q was refused", from)
		}
	}
}
