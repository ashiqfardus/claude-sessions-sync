package archive

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// Warm reads each file once, eight at a time and no more, and its wait returns only
// once every read has.
func TestWarmReadsEachOnceEightAtATime(t *testing.T) {
	var paths []string
	for i := 0; i < 20; i++ {
		paths = append(paths, fmt.Sprintf("f%02d", i))
	}
	var mu sync.Mutex
	var got []string
	inFlight, most := 0, 0
	wait := Warm(paths, func(p string) {
		mu.Lock()
		got = append(got, p)
		if inFlight++; inFlight > most {
			most = inFlight
		}
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		inFlight--
		mu.Unlock()
	})
	wait()
	mu.Lock()
	defer mu.Unlock()
	if most != 8 {
		t.Errorf("%d read at once at most, want 8", most)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(paths, ",") {
		t.Errorf("read %v, want each of %v once", got, paths)
	}
	if inFlight != 0 {
		t.Errorf("wait returned with %d read(s) going", inFlight)
	}
	Warm(nil, func(string) { t.Error("read with no files") })()
}

// A read that panics - a file from the archive no reader expected - fails that read
// alone: the others are read, wait returns, and the process lives on.
func TestWarmSurvivesAReadThatPanics(t *testing.T) {
	var mu sync.Mutex
	var got []string
	Warm([]string{"a", "bad", "c"}, func(p string) {
		if p == "bad" {
			panic("a zip that breaks its reader")
		}
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
	})()
	sort.Strings(got)
	if strings.Join(got, ",") != "a,c" {
		t.Errorf("read %v", got)
	}
}
