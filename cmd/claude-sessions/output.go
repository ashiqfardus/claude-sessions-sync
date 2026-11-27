package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// streams is where a command writes its text and progress. The CLI passes the
// terminal's; the panel, which runs commands from concurrent requests, its own.
type streams struct{ Out, Err io.Writer }

func stdStreams() streams { return streams{os.Stdout, os.Stderr} }

// jsonStreams is for --json: the text is dropped, and the result printed as JSON instead.
func jsonStreams() streams { return streams{io.Discard, os.Stderr} }

// quietStreams drops everything: the panel's queries want only the result.
func quietStreams() streams { return streams{io.Discard, io.Discard} }

// encodeJSON is how every command prints its --json result.
func encodeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// isTerminal reports whether w is a terminal. A progress line is for a person watching,
// never for a script, a log or the panel.
var isTerminal = func(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// heartbeatEvery is how often a long read says how far it has got.
var heartbeatEvery = 12 * time.Second

// progressEvery is the least time between two progress texts of one stage, its first
// and its last aside, for the panel, which reads them as they come.
var progressEvery = 200 * time.Millisecond

// heartbeat says how far import's or pull's read of the archive has got: on a drive
// that downloads each file as it is opened, it reads for minutes without a line. On a
// terminal it says so once every heartbeatEvery until stop; anywhere else - the panel's
// job, a pipe - each stage is progress text, which the panel shows as it comes and
// which no line or --json result holds: to the panel \r-ended at most every
// progressEvery, to a pipe or a file only each stage's first text and its last count,
// each a line of its own.
type heartbeat struct {
	mu      sync.Mutex
	w       io.Writer // nil: not a terminal
	p       io.Writer // the progress text's; nil on a terminal or for none
	verb    string    // before what in the progress text: "reading " for import's
	start   time.Time
	what    string // what is being read
	n, of   int    // how many of what are read, of how many; of is 0 when not counted
	own     bool   // what names the stage in full, verb and all: it is not a read of the archive
	wrote   time.Time
	behind  bool // a step not written yet, for progressEvery
	sparse  bool // a pipe or a file: a script reading stdout to its end first would hang once it is full (4 KB on Windows)
	stopped bool
	done    chan struct{}
}

func startHeartbeat(w io.Writer) *heartbeat {
	h := startProgress(w)
	h.verb = "reading "
	if !isTerminal(w) {
		return h
	}
	h.w = w
	go func() {
		t := time.NewTicker(heartbeatEvery)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				h.say()
			case <-h.done:
				return
			}
		}
	}()
	return h
}

func (h *heartbeat) say() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopped {
		return
	}
	line := "still reading the archive"
	switch {
	case h.own:
		line = fmt.Sprintf("still %s: %d of %d", h.what, h.n, h.of)
	case h.of > 0:
		line += fmt.Sprintf(": %d of %d %s", h.n, h.of, h.what)
	case h.what != "":
		line += ": " + h.what
	}
	fmt.Fprintf(h.w, "%s (%s)\n", line, time.Since(h.start).Round(time.Second))
}

// startProgress is push's heartbeat: the progress text alone. A terminal gets nothing,
// and keeps push's own lines as they were.
func startProgress(w io.Writer) *heartbeat {
	h := &heartbeat{start: time.Now(), done: make(chan struct{})}
	if w != io.Discard && !isTerminal(w) {
		h.p = w
		_, h.sparse = w.(*os.File) // the panel's job runs in this process, writing to its own writer
	}
	return h
}

// on reports whether the heartbeat writes anywhere.
func (h *heartbeat) on() bool { return h.w != nil || h.p != nil }

// reading starts a stage: of things named what, or of 0 when they are not counted. The
// last count of the stage before is written first, if it was held back.
func (h *heartbeat) reading(what string, of int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.catchUp()
	h.what, h.n, h.of, h.own = what, 0, of, false
	h.progress()
}

// checking starts a stage that reads no archive, named in full: "checking file paths
// in conversations", of things counted.
func (h *heartbeat) checking(text string, of int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.catchUp()
	h.what, h.n, h.of, h.own = text, 0, of, true
	h.progress()
}

// step counts one more thing read.
func (h *heartbeat) step() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.count(h.n + 1)
}

// upTo counts the stage on to n when it is short of it: a project passed over counts
// its conversations as read, so the count ends at its total.
func (h *heartbeat) upTo(n int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if n > h.n {
		h.count(n)
	}
}

// count sets how many are read, written when that completes the count or progressEvery
// has passed. Under h.mu.
func (h *heartbeat) count(n int) {
	h.n = n
	if h.n == h.of || !h.sparse && time.Since(h.wrote) >= progressEvery {
		h.progress()
	} else {
		h.behind = true
	}
}

// catchUp writes the count a step held back. Under h.mu.
func (h *heartbeat) catchUp() {
	if h.behind {
		h.progress()
	}
}

// progress writes the stage as clean's counter reads, "reading conversations: 3/40",
// ended by \r, which the panel shows in place. To a pipe or a file it ends a line: a
// script that captures stderr with stdout found the next line glued on. Under h.mu.
func (h *heartbeat) progress() {
	h.behind = false
	if h.p == nil || h.stopped {
		return
	}
	text := h.what
	if !h.own {
		text = h.verb + text
	}
	if h.of > 0 {
		text += fmt.Sprintf(": %d/%d", h.n, h.of)
	}
	end := "\r"
	if h.sparse {
		end = "\n"
	}
	fmt.Fprint(h.p, text+end)
	h.wrote = time.Now()
}

// stop ends the heartbeat, its last count written: once it returns, nothing more is
// written. Again is a no-op.
func (h *heartbeat) stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.stopped {
		h.catchUp()
		h.stopped = true
		close(h.done)
	}
}
