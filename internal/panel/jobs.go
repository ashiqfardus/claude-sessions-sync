package panel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"
)

var (
	// ErrBusy is Start's answer while a job runs.
	ErrBusy = errors.New("another action is running")
	// ErrClosed is Start's answer after Close.
	ErrClosed = errors.New("the panel is stopping")
)

const (
	jobsKept        = 20
	jobLinesKept    = 10000
	jobProgressKept = 16       // progress texts a slow stream can still catch up on
	jobTextMax      = 64 << 10 // bytes in one line or progress text, "…" included
)

var jobPingEvery = 15 * time.Second // a var so a test can shorten it

// State is a job as the page sees it.
type State struct {
	ID      string   `json:"id"`
	Action  string   `json:"action"`
	Label   string   `json:"label"`
	Running bool     `json:"running"`
	OK      bool     `json:"ok"`
	Result  any      `json:"result"`
	Error   string   `json:"error"`
	Lines   []string `json:"lines"`
}

// Runner runs one job at a time and keeps the last few.
type Runner struct {
	plain func(error) string
	logf  func(string, ...any)

	mu     sync.Mutex
	seq    int
	cur    *Job
	jobs   []*Job // oldest first
	closed bool

	flushFailed sync.Once // a writer that cannot flush fails every request alike
}

// NewRunner makes a runner. plain turns a job's error into the sentence the page
// shows; the raw error goes only to logf.
func NewRunner(plain func(error) string, logf func(string, ...any)) *Runner {
	return &Runner{plain: plain, logf: logf}
}

// Job is one run of an action, with its log.
type Job struct {
	id, action, label string

	mu       sync.Mutex
	lines    []string // a ring of the last jobLinesKept lines
	total    int      // lines ever written; line n (from 1) is lines[(n-1)%jobLinesKept]
	partial  []byte   // text not yet ended by \n or \r
	progress []progressText
	progN    int // progress texts ever written
	running  bool
	ok       bool
	result   any
	errText  string
	changed  chan struct{} // closed and replaced on every change
	done     chan struct{}
}

type progressText struct {
	after int // lines written before it, to keep it in order with them
	text  string
}

// jobLog is the writer a job's fn gets; Job itself is not a writer.
type jobLog struct{ j *Job }

func (l jobLog) Write(p []byte) (int, error) { return l.j.write(p) }

// Start runs fn as a job. While another job runs it returns ErrBusy, and the job
// returned is the running one. After Close it returns nil and ErrClosed.
func (r *Runner) Start(action, label string, fn func(log io.Writer) (any, error)) (*Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrClosed
	}
	if r.cur != nil {
		return r.cur, ErrBusy
	}
	r.seq++
	j := &Job{id: strconv.Itoa(r.seq), action: action, label: label, running: true,
		changed: make(chan struct{}), done: make(chan struct{})}
	r.cur = j
	r.jobs = append(r.jobs, j)
	if len(r.jobs) > jobsKept {
		r.jobs = append([]*Job(nil), r.jobs[1:]...)
	}
	go r.run(j, fn)
	return j, nil
}

func (r *Runner) run(j *Job, fn func(io.Writer) (any, error)) {
	var res any
	var err error
	var stack []byte
	func() {
		defer func() {
			if p := recover(); p != nil {
				err, stack = fmt.Errorf("panicked: %v", p), debug.Stack()
			}
		}()
		res, err = fn(jobLog{j})
	}()
	text := ""
	if stack != nil {
		r.logf("ERROR: %s: %v\n%s", j.action, err, stack) // the stack goes to the log only
	} else if err != nil {
		r.logf("ERROR: %s: %v", j.action, err)
	}
	if err != nil {
		text = r.plain(err)
	}

	// Under both locks, so whoever sees the job done also finds the runner free.
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cur = nil
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.partial) > 0 {
		j.addLine(clipText(j.partial))
		j.partial = nil
	}
	j.running, j.ok, j.result, j.errText = false, err == nil, res, text
	j.notify()
	close(j.done)
}

// Close refuses every later Start and gives the running job, or nil, so the
// panel can stop now or after that job.
func (r *Runner) Close() *Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return r.cur
}

func (r *Runner) Busy() bool { return r.Current() != nil }

// Current is the running job, or nil.
func (r *Runner) Current() *Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cur
}

// Get finds one of the last jobsKept jobs, or gives nil.
func (r *Runner) Get(id string) *Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, j := range r.jobs {
		if j.id == id {
			return j
		}
	}
	return nil
}

func (j *Job) State() State {
	j.mu.Lock()
	defer j.mu.Unlock()
	lines, _ := j.linesAfter(0)
	return State{ID: j.id, Action: j.action, Label: j.label, Running: j.running,
		OK: j.ok, Result: j.result, Error: j.errText, Lines: lines}
}

// Summary is a job without its log or result, for views polled often: the log comes
// from ServeEvents, the result from its done event (Ruling P47).
type Summary struct {
	ID      string `json:"id"`
	Action  string `json:"action"`
	Label   string `json:"label"`
	Running bool   `json:"running"`
}

// Summary copies no lines, so it holds the job's lock only to read one bool.
func (j *Job) Summary() Summary {
	j.mu.Lock()
	running := j.running
	j.mu.Unlock()
	return Summary{ID: j.id, Action: j.action, Label: j.label, Running: running}
}

// Label needs no lock: it never changes.
func (j *Job) Label() string { return j.label }

// Wait blocks until the job ends and gives its final state.
func (j *Job) Wait() State {
	<-j.done
	return j.State()
}

// write splits p into lines (ended by \n) and progress (ended by \r).
func (j *Job) write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.running {
		return len(p), nil // a goroutine fn left behind; the log is closed
	}
	for rest := p; ; {
		i := bytes.IndexAny(rest, "\r\n")
		if i < 0 {
			j.partial = appendText(j.partial, rest)
			break
		}
		text := clipText(appendText(j.partial, rest[:i]))
		j.partial = j.partial[:0]
		if rest[i] == '\n' {
			j.addLine(text)
		} else if text != "" { // clean's counter starts with \r; an empty one would blank the page's
			j.progress = append(j.progress, progressText{j.total, text})
			if len(j.progress) > jobProgressKept {
				j.progress = j.progress[1:]
			}
			j.progN++
		}
		rest = rest[i+1:]
	}
	j.notify()
	return len(p), nil
}

// appendText adds b to an unfinished text, keeping at most one byte past
// jobTextMax: enough for clipText to know the text is too long.
func appendText(buf, b []byte) []byte {
	if room := jobTextMax + 1 - len(buf); len(b) > room {
		b = b[:max(room, 0)]
	}
	return append(buf, b...)
}

// clipText cuts a text longer than jobTextMax at a rune boundary and ends it with "…".
func clipText(b []byte) string {
	if len(b) <= jobTextMax {
		return string(b)
	}
	cut := jobTextMax - len("…")
	for cut > 0 && !utf8.RuneStart(b[cut]) {
		cut--
	}
	return string(b[:cut]) + "…"
}

func (j *Job) addLine(s string) {
	if len(j.lines) < jobLinesKept {
		j.lines = append(j.lines, s)
	} else {
		j.lines[j.total%jobLinesKept] = s
	}
	j.total++
}

// linesAfter gives the kept lines numbered above n and the number of the first.
// Once lines have been dropped, the oldest kept one says how many are not shown.
func (j *Job) linesAfter(n int) ([]string, int) {
	first := j.total - len(j.lines) + 1
	if n < first-1 {
		n = first - 1
	}
	out := make([]string, 0, max(j.total-n, 0)) // n may be past the end: a forged Last-Event-ID
	for s := n + 1; s <= j.total; s++ {
		if s == first && first > 1 {
			out = append(out, fmt.Sprintf("… %d earlier lines", first))
		} else {
			out = append(out, j.lines[(s-1)%jobLinesKept])
		}
	}
	return out, n + 1
}

func (j *Job) notify() {
	close(j.changed)
	j.changed = make(chan struct{})
}

// ServeEvents streams job ?id= as server-sent events: the stored lines after
// Last-Event-ID, then live lines and progress, then done.
func (r *Runner) ServeEvents(w http.ResponseWriter, req *http.Request) {
	j := r.Get(req.URL.Query().Get("id"))
	if j == nil {
		http.NotFound(w, req)
		return
	}
	lineN, _ := strconv.Atoi(req.Header.Get("Last-Event-ID"))
	j.mu.Lock()
	progN := j.progN // progress before the connection is history, not replayed
	j.mu.Unlock()

	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{}) // a stream outlives any server write timeout
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		r.flushFailed.Do(func() { r.logf("ERROR: job events: cannot stream: %v", err) })
		return
	}
	ping := time.NewTicker(jobPingEvery)
	defer ping.Stop()

	var buf bytes.Buffer
	for {
		buf.Reset()
		j.mu.Lock()
		lines, first := j.linesAfter(lineN)
		lineN = first + len(lines) - 1
		var progress []progressText
		if skip := len(j.progress) - (j.progN - progN); skip < len(j.progress) {
			progress = append(progress, j.progress[max(skip, 0):]...)
		}
		progN = j.progN
		running, changed := j.running, j.changed
		done := struct {
			OK     bool   `json:"ok"`
			Result any    `json:"result"`
			Error  string `json:"error"`
		}{j.ok, j.result, j.errText}
		j.mu.Unlock()

		for i, line := range lines {
			for len(progress) > 0 && progress[0].after < first+i {
				writeEvent(&buf, "progress", "", progress[0].text)
				progress = progress[1:]
			}
			writeEvent(&buf, "line", strconv.Itoa(first+i), line)
		}
		for _, p := range progress {
			writeEvent(&buf, "progress", "", p.text)
		}
		if !running {
			data, err := json.Marshal(done)
			if err != nil {
				r.logf("ERROR: %s: result: %v", j.action, err)
				done.Result = nil
				data, _ = json.Marshal(done)
			}
			fmt.Fprintf(&buf, "event: done\ndata: %s\n\n", data)
		}
		if buf.Len() > 0 {
			if _, err := w.Write(buf.Bytes()); err != nil || rc.Flush() != nil {
				return
			}
		}
		if !running {
			return
		}

		select {
		case <-changed:
		case <-ping.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		case <-req.Context().Done():
			return
		}
	}
}

func writeEvent(buf *bytes.Buffer, event, id, text string) {
	data, _ := json.Marshal(text) // a string always marshals
	buf.WriteString("event: " + event + "\n")
	if id != "" {
		buf.WriteString("id: " + id + "\n")
	}
	buf.WriteString("data: " + string(data) + "\n\n")
}
