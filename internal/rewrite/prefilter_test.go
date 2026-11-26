package rewrite

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"unicode"
)

// Go's (?i) folds an ASCII letter to a non-ASCII rune only for k (U+212A, Kelvin) and
// s (U+017F, long s): the prefilter's fallback is exact only while that holds.
func TestOnlyKAndSFoldOutsideASCII(t *testing.T) {
	for c := rune('A'); c <= 'z'; c++ {
		if !unicode.IsLetter(c) {
			continue
		}
		for f := unicode.SimpleFold(c); f != c; f = unicode.SimpleFold(f) {
			if f >= 0x80 && f != '\u017f' && f != '\u212a' {
				t.Errorf("%q folds to %U, which the prefilter does not fall back on", c, f)
			}
		}
	}
}

// prefilterMoves are moves whose Finds hold mixed case, an s and a k (which fold to ſ
// and the Kelvin sign), a Windows path and non-ASCII names. One move's destination is
// another's source, so an earlier rule's replacement makes a later rule match.
var prefilterMoves = []Move{
	{From: `C:\Users\Kim\Desk\api`, To: "/home/kim/api"},
	{From: "/Users/sKs/work", To: "/home/sks/work"},
	{From: "/Users/ſam/notes", To: "/home/sam/notes"},
	{From: "/Users/Ünal/proj", To: "/home/unal/proj"},
	{From: `D:\Wörk\Kafka`, To: "/srv/kafka"},
	{From: "/tmp/stage/a", To: "/Users/sKs/work/a"},
	{From: "/srv/kafka", To: "/opt/Kafka"}, // after D:\Wörk\Kafka's rules: their output is its Find
}

// prefilterLines mixes every case of each Find with ſ, the Kelvin sign and other runes,
// around path boundaries and JSON escapes.
func prefilterLines() []string {
	lines := []string{
		`{"cwd":"C:\\Users\\Kim\\Desk\\api","x":"c:\\users\\kim\\desk\\api\\src"}`,
		`{"cwd":"C:\\Users\\Kim\\Desk\\api"}`,                    // Kelvin K in the line
		`{"cwd":"/Users/ſKſ/work/x"}`,                            // ſKſ for sKs
		`{"cwd":"/USERS/SKS/WORK"} and /users/sks/work.`,         // all case, sentence stop
		`{"cwd":"/Users/ſam/notes/a.md"} /Users/SAM/notes`,       // non-ASCII Find, ASCII line
		`{"cwd":"/Users/ÜNAL/proj"} /Users/ünal/proj/x`,          // non-ASCII Find in other case
		`{"cwd":"D:\\WÖRK\\KAFKA\\logs"} D:/Wörk/Kafka/x`,        // non-ASCII Find, Kelvin-free
		`/tmp/stage/a/b then /tmp/stage/ab and /mnt/tmp/stage/a`, // replacement feeds a later rule
		`nothing to see here, only ASCII text with no path at all`,
		`ſ alone, K alone, and /users/sks/workers`,
		"",
	}
	frags := []string{"/Users/", "/users/", `C:\\Users\\`, "Kim", "KIM", "Kim", "k", "K", "K",
		"s", "S", "ſ", "Ks", "sKs", "SKS", "/work", `\\Desk\\api`, "/notes", "ſam", "SAM", "Ünal", "ünal",
		"/proj", `D:\\Wörk\\Kafka`, "/tmp/stage/a", " ", `"`, ".", "/", "é", "İ", "ı", "x",
		// whole Finds, in other cases and with ſ and the Kelvin sign for s and k
		"/Users/sKs/work", "/users/SKS/WORK", "/Users/ſKſ/work", `C:\\Users\\Kim\\Desk\\api`,
		`c:\\USERS\\KIM\\deſk\\API`, "c:/users/kim/desk/api", "/c/Users/Kim/Desk/api", "C--Users-Kim-Desk-api",
		"/Users/ſam/notes", "/USERS/SAM/NOTES", `D:\\WÖRK\\kafka`, "/Users/Ünal/proj", "AbcK-SsS", "abcK-ſsſ", "ſK", "SK", "ſk"}
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 3000; i++ {
		var b strings.Builder
		for n := rng.Intn(12); n >= 0; n-- {
			b.WriteString(frags[rng.Intn(len(frags))])
		}
		lines = append(lines, b.String())
	}
	return lines
}

// The prefilter only decides whether a rule's regex runs: every line comes out as it
// does with every regex run, with the same hits.
func TestApplyPrefilterChangesNothing(t *testing.T) {
	rules := RulesForMoves(prefilterMoves)
	rules = append(rules, Literal("session-id", "AbcK-SsS", "x"), Literal("non-ascii", "ſK", "y"))
	matched := 0
	for _, l := range prefilterLines() {
		want, wantHits := applyEvery(l, rules)
		gotHits := map[string]int{}
		if got := Apply(l, rules, gotHits); got != want || fmt.Sprint(gotHits) != fmt.Sprint(wantHits) {
			t.Fatalf("Apply(%q)\n  = %q, %v\nwant %q, %v", l, got, gotHits, want, wantHits)
		}
		if len(wantHits) > 0 {
			matched++
		}
	}
	if matched < 300 {
		t.Errorf("only %d lines matched a rule: the corpus tests too little", matched)
	}
}

// applyEvery is Apply with every rule's regex run.
func applyEvery(line string, rules []Rule) (string, map[string]int) {
	hits := map[string]int{}
	prefilter = false
	defer func() { prefilter = true }()
	return Apply(line, rules, hits), hits
}

// BenchmarkApply120Rules runs 24 Windows-path moves (120 rules) over transcript-like
// lines, with the prefilter and with every regex run.
func BenchmarkApply120Rules(b *testing.B) {
	var moves []Move
	for i := 0; i < 24; i++ {
		moves = append(moves, Move{From: fmt.Sprintf(`C:\Users\you\work\proj%02d`, i), To: fmt.Sprintf("/home/you/proj%02d", i)})
	}
	rules := RulesForMoves(moves)
	if len(rules) != 120 {
		b.Fatalf("%d rules", len(rules))
	}
	line := `{"type":"assistant","message":{"content":[{"type":"text","text":"` + strings.Repeat("The build passed and the tests ran. ", 40) +
		`"}]},"cwd":"C:\\Users\\you\\work\\proj07","toolUseResult":{"stdout":"` + strings.Repeat("ok  \tpkg\t0.1s\\n", 30) + `"}}`
	for _, on := range []bool{false, true} {
		name := "every-regex"
		if on {
			name = "prefilter"
		}
		b.Run(name, func(b *testing.B) {
			prefilter = on
			defer func() { prefilter = true }()
			b.SetBytes(int64(len(line)))
			for i := 0; i < b.N; i++ {
				Apply(line, rules, nil)
			}
		})
	}
}
