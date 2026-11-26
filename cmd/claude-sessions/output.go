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

// heartbeat says on a terminal, once every heartbeatEvery until stop, how far import's
// read of the archive has got: on a drive that downloads each file as it is opened,
// it reads for minutes without a line. Anywhere else it writes nothing.
type heartbeat struct {
	mu      sync.Mutex
	w       io.Writer // nil: not a terminal
	start   time.Time
	what    string // what is being read
	n, of   int    // how many of what are read, of how many; of is 0 when not counted
	stopped bool
	done    chan struct{}
}

func startHeartbeat(w io.Writer) *heartbeat {
	h := &heartbeat{start: time.Now(), done: make(chan struct{})}
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
	case h.of > 0:
		line += fmt.Sprintf(": %d of %d %s", h.n, h.of, h.what)
	case h.what != "":
		line += ": " + h.what
	}
	fmt.Fprintf(h.w, "%s (%s)\n", line, time.Since(h.start).Round(time.Second))
}

// on reports whether the heartbeat writes anywhere.
func (h *heartbeat) on() bool { return h.w != nil }

// reading starts a stage: of things named what, or of 0 when they are not counted.
func (h *heartbeat) reading(what string, of int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.what, h.n, h.of = what, 0, of
}

// step counts one more thing read.
func (h *heartbeat) step() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.n++
}

// stop ends the heartbeat: once it returns, nothing more is written. Again is a no-op.
func (h *heartbeat) stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.stopped {
		h.stopped = true
		close(h.done)
	}
}
