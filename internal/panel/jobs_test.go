package panel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

type jobEvent struct{ Event, ID, Data string }

// readJobEvent reads one event from an SSE stream, skipping comments.
func readJobEvent(t *testing.T, br *bufio.Reader) jobEvent {
	t.Helper()
	var e jobEvent
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended before an event: %v (have %+v)", err, e)
		}
		l = strings.TrimSuffix(l, "\n")
		switch {
		case l == "":
			if e.Event != "" {
				return e
			}
		case strings.HasPrefix(l, "event: "):
			e.Event = l[len("event: "):]
		case strings.HasPrefix(l, "id: "):
			e.ID = l[len("id: "):]
		case strings.HasPrefix(l, "data: "):
			e.Data = l[len("data: "):]
		}
	}
}

// readJobEvents reads events up to and including done, then expects the stream to end.
func readJobEvents(t *testing.T, br *bufio.Reader) []jobEvent {
	t.Helper()
	var out []jobEvent
	for {
		e := readJobEvent(t, br)
		out = append(out, e)
		if e.Event == "done" {
			if rest, _ := io.ReadAll(br); len(rest) != 0 {
				t.Fatalf("stream went on after done: %q", rest)
			}
			return out
		}
	}
}

type jobLogRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (l *jobLogRecorder) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *jobLogRecorder) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

const jobPlainText = "That didn't work. The details are in the log on the Settings page."

func newJobRunner() (*Runner, *jobLogRecorder) {
	rec := &jobLogRecorder{}
	return NewRunner(func(error) string { return jobPlainText }, rec.logf), rec
}

// subscribeJob opens the job's event stream with an optional Last-Event-ID. It
// returns once the response headers are in, so the handler is already attached.
func subscribeJob(t *testing.T, srv *httptest.Server, j *Job, lastID string) *bufio.Reader {
	t.Helper()
	req, err := http.NewRequest("GET", srv.URL+"/?id="+j.State().ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status %d, Content-Type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	return bufio.NewReader(resp.Body)
}

func TestRunnerOneAtATime(t *testing.T) {
	r, _ := newJobRunner()
	block := make(chan struct{})
	a, err := r.Start("push", "Back up now", func(io.Writer) (any, error) { <-block; return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	if !r.Busy() || r.Current() != a {
		t.Fatalf("Busy %v, Current %v: want the running job", r.Busy(), r.Current())
	}
	busy, err := r.Start("render", "Rebuild pages", func(io.Writer) (any, error) { return nil, nil })
	if !errors.Is(err, ErrBusy) || busy != a || busy.Label() != "Back up now" {
		t.Fatalf("second Start: %v, %v: want ErrBusy and the running job", busy, err)
	}
	close(block)
	if st := a.Wait(); st.Running || !st.OK {
		t.Fatalf("A ended as %+v", st)
	}
	if r.Busy() || r.Current() != nil {
		t.Fatal("still busy after A ended")
	}
	c, err := r.Start("render", "Rebuild pages", func(io.Writer) (any, error) { return "ok", nil })
	if err != nil {
		t.Fatalf("Start after A ended: %v", err)
	}
	if st := c.Wait(); !st.OK || st.Result != "ok" || st.Action != "render" || st.Label != "Rebuild pages" {
		t.Fatalf("C ended as %+v", st)
	}
	if r.Get(a.State().ID) != a || r.Get(c.State().ID) != c || r.Get("nope") != nil {
		t.Fatal("Get does not find the jobs by ID")
	}
}

func TestRunnerStreamsLinesInOrder(t *testing.T) {
	r, _ := newJobRunner()
	srv := httptest.NewServer(http.HandlerFunc(r.ServeEvents))
	defer srv.Close()
	release := make(chan struct{})
	j, err := r.Start("push", "Back up now", func(log io.Writer) (any, error) {
		<-release
		io.WriteString(log, "a\nb")
		io.WriteString(log, "\n")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	br := subscribeJob(t, srv, j, "")
	close(release)
	got := readJobEvents(t, br)
	want := []jobEvent{{"line", "1", `"a"`}, {"line", "2", `"b"`}, {"done", "", `{"ok":true,"result":null,"error":""}`}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("events:\n got %v\nwant %v", got, want)
	}
}

func TestRunnerReplaysForLateSubscriber(t *testing.T) {
	r, _ := newJobRunner()
	srv := httptest.NewServer(http.HandlerFunc(r.ServeEvents))
	defer srv.Close()
	wrote, release := make(chan struct{}), make(chan struct{})
	j, err := r.Start("import", "Bring in work", func(log io.Writer) (any, error) {
		io.WriteString(log, "one\ntwo\nthree\n")
		close(wrote)
		<-release
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-wrote

	all := subscribeJob(t, srv, j, "")
	for i, want := range []string{`"one"`, `"two"`, `"three"`} {
		if e := readJobEvent(t, all); e.Event != "line" || e.Data != want || e.ID != fmt.Sprint(i+1) {
			t.Fatalf("event %d: %+v, want line %s", i, e, want)
		}
	}
	resumed := subscribeJob(t, srv, j, "2")
	if e := readJobEvent(t, resumed); e.Event != "line" || e.Data != `"three"` || e.ID != "3" {
		t.Fatalf("after Last-Event-ID 2: %+v, want line \"three\"", e)
	}

	close(release)
	for _, br := range []*bufio.Reader{all, resumed} {
		if got := readJobEvents(t, br); len(got) != 1 || got[0].Event != "done" {
			t.Fatalf("after the replay: %v, want only done", got)
		}
	}
}

func TestRunnerProgressEvents(t *testing.T) {
	r, _ := newJobRunner()
	srv := httptest.NewServer(http.HandlerFunc(r.ServeEvents))
	defer srv.Close()
	release := make(chan struct{})
	j, err := r.Start("clean", "Remove copies", func(log io.Writer) (any, error) {
		<-release
		io.WriteString(log, "x 1/3\rx 2/3\r")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	br := subscribeJob(t, srv, j, "")
	close(release)
	got := readJobEvents(t, br)
	want := []jobEvent{{"progress", "", `"x 1/3"`}, {"progress", "", `"x 2/3"`}, {"done", "", `{"ok":true,"result":null,"error":""}`}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("events:\n got %v\nwant %v", got, want)
	}
	if lines := j.Wait().Lines; len(lines) != 0 {
		t.Fatalf("progress was stored as lines: %q", lines)
	}
}

func TestRunnerKeepsProgressInOrderWithLines(t *testing.T) {
	r, _ := newJobRunner()
	srv := httptest.NewServer(http.HandlerFunc(r.ServeEvents))
	defer srv.Close()
	release := make(chan struct{})
	j, err := r.Start("clean", "Remove copies", func(log io.Writer) (any, error) {
		<-release
		io.WriteString(log, "p1\rfirst\np2\rsecond\n")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	br := subscribeJob(t, srv, j, "")
	close(release)
	var got []string
	for _, e := range readJobEvents(t, br) {
		got = append(got, e.Event+" "+e.Data)
	}
	want := `progress "p1"|line "first"|progress "p2"|line "second"|done {"ok":true,"result":null,"error":""}`
	if strings.Join(got, "|") != want {
		t.Fatalf("events:\n got %s\nwant %s", strings.Join(got, "|"), want)
	}
}

func TestRunnerRecoversPanic(t *testing.T) {
	var gotErr string
	rec := &jobLogRecorder{}
	r := NewRunner(func(err error) string { gotErr = err.Error(); return jobPlainText }, rec.logf)
	j, err := r.Start("prune", "Delete", func(io.Writer) (any, error) { panic("boom") })
	if err != nil {
		t.Fatal(err)
	}
	st := j.Wait()
	if st.Running || st.OK || st.Error != jobPlainText {
		t.Fatalf("a panicking job ended as %+v", st)
	}
	if gotErr != "panicked: boom" {
		t.Fatalf("plain got %q, want %q", gotErr, "panicked: boom")
	}
	if !strings.Contains(rec.text(), "panicked: boom") {
		t.Fatalf("the panic was not logged: %q", rec.text())
	}
	next, err := r.Start("push", "Back up now", func(io.Writer) (any, error) { return nil, nil })
	if err != nil {
		t.Fatalf("Start after a panic: %v", err)
	}
	next.Wait()
}

func TestRunnerPlainErrorOnly(t *testing.T) {
	r, rec := newJobRunner()
	raw := "open /x/secret/y: permission denied"
	j, err := r.Start("restore", "Restore", func(log io.Writer) (any, error) {
		io.WriteString(log, "working\n")
		return nil, errors.New(raw)
	})
	if err != nil {
		t.Fatal(err)
	}
	j.Wait()
	w := httptest.NewRecorder()
	r.ServeEvents(w, httptest.NewRequest("GET", "/api/jobs/events?id="+j.State().ID, nil))
	body := w.Body.String()
	if strings.Contains(body, "secret") {
		t.Fatalf("the raw error reached the page: %q", body)
	}
	want := "event: done\ndata: {\"ok\":false,\"result\":null,\"error\":\"That didn't work. The details are in the log on the Settings page.\"}\n\n"
	if !strings.HasSuffix(body, want) {
		t.Fatalf("body %q\ndoes not end with %q", body, want)
	}
	if !strings.Contains(rec.text(), raw) {
		t.Fatalf("the raw error was not logged: %q", rec.text())
	}
}

func TestRunnerKeepsTheLastLines(t *testing.T) {
	r, _ := newJobRunner()
	j, err := r.Start("push", "Back up now", func(log io.Writer) (any, error) {
		for i := 1; i <= 10001; i++ {
			fmt.Fprintf(log, "l%d\n", i)
		}
		io.WriteString(log, "no newline")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := j.Wait().Lines
	if len(lines) != 10000 || lines[0] != "… 3 earlier lines" || lines[1] != "l4" || lines[9998] != "l10001" || lines[9999] != "no newline" {
		t.Fatalf("%d lines, first %q %q, last %q %q", len(lines), lines[0], lines[1], lines[len(lines)-2], lines[len(lines)-1])
	}

	// A client resuming inside the kept window gets no marker.
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/?id="+j.State().ID, nil)
	req.Header.Set("Last-Event-ID", "10000")
	r.ServeEvents(w, req)
	if !strings.HasPrefix(w.Body.String(), "event: line\nid: 10001\ndata: \"l10001\"\n\nevent: line\nid: 10002\ndata: \"no newline\"\n\nevent: done\n") {
		t.Fatalf("resumed body: %q", w.Body.String())
	}

	// A Last-Event-ID past the end gives just done.
	w = httptest.NewRecorder()
	req.Header.Set("Last-Event-ID", "99999")
	r.ServeEvents(w, req)
	if !strings.HasPrefix(w.Body.String(), "event: done\n") {
		t.Fatalf("body past the end: %q", w.Body.String())
	}
}

func TestRunnerKeepsTheLast20Jobs(t *testing.T) {
	r, _ := newJobRunner()
	var ids []string
	for i := 0; i < 21; i++ {
		j, err := r.Start("push", "Back up now", func(io.Writer) (any, error) { return nil, nil })
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, j.Wait().ID)
	}
	if r.Get(ids[0]) != nil || r.Get(ids[1]) == nil || r.Get(ids[20]) == nil {
		t.Fatal("want the oldest of 21 jobs dropped and the other 20 kept")
	}
}

func TestRunnerSurvivesADisconnectedBrowser(t *testing.T) {
	r, _ := newJobRunner()
	release := make(chan struct{})
	j, err := r.Start("import", "Bring in work", func(log io.Writer) (any, error) {
		<-release
		io.WriteString(log, "still here\n")
		return "done", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() {
		defer close(served)
		r.ServeEvents(httptest.NewRecorder(), httptest.NewRequest("GET", "/?id="+j.State().ID, nil).WithContext(ctx))
	}()
	cancel()
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream did not end when the browser went away")
	}
	if !r.Busy() {
		t.Fatal("the job stopped with the browser")
	}
	close(release)
	if st := j.Wait(); !st.OK || st.Result != "done" || len(st.Lines) != 1 {
		t.Fatalf("job ended as %+v", st)
	}

	w := httptest.NewRecorder()
	r.ServeEvents(w, httptest.NewRequest("GET", "/?id=nope", nil))
	if w.Code != 404 {
		t.Fatalf("unknown job: %d, want 404", w.Code)
	}
}

func TestRunnerConcurrentStartsRunOneJob(t *testing.T) {
	r, _ := newJobRunner()
	block := make(chan struct{})
	ready := make(chan struct{})
	type started struct {
		j   *Job
		err error
	}
	results := make(chan started, 16)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ready
			j, err := r.Start("push", "Back up now", func(io.Writer) (any, error) { <-block; return nil, nil })
			results <- started{j, err}
		}()
	}
	close(ready)
	wg.Wait()
	close(results)
	var won *Job
	var busy []*Job
	for s := range results {
		switch {
		case s.err == nil && won == nil:
			won = s.j
		case errors.Is(s.err, ErrBusy):
			busy = append(busy, s.j)
		default:
			t.Fatalf("Start: %v, %v (a second job started, or an unexpected error)", s.j, s.err)
		}
	}
	if won == nil || len(busy) != 15 {
		t.Fatalf("won %v, %d busy: want one job and 15 ErrBusy", won, len(busy))
	}
	for _, j := range busy {
		if j != won {
			t.Fatal("ErrBusy did not return the running job")
		}
	}
	close(block)
	won.Wait()
}

func TestRunnerBoundsLongText(t *testing.T) {
	r, _ := newJobRunner()
	wrote, release := make(chan struct{}), make(chan struct{})
	long := strings.Repeat("é", 1<<19) // 1 MiB, two bytes a rune
	j, err := r.Start("push", "Back up now", func(log io.Writer) (any, error) {
		io.WriteString(log, long[:1<<19])
		io.WriteString(log, long[1<<19:])
		close(wrote)
		<-release
		io.WriteString(log, long+"\r"+long+"\n")
		io.WriteString(log, long) // no newline: becomes the last line when the job ends
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-wrote
	j.mu.Lock()
	held := len(j.partial)
	j.mu.Unlock()
	if held > jobTextMax+1 {
		t.Fatalf("a 1 MiB partial line holds %d bytes, want at most %d", held, jobTextMax+1)
	}
	close(release)
	st := j.Wait()
	if len(st.Lines) != 2 {
		t.Fatalf("%d lines, want 2", len(st.Lines))
	}
	for i, l := range st.Lines {
		if len(l) > jobTextMax || !strings.HasSuffix(l, "…") || !utf8.ValidString(l) {
			t.Fatalf("line %d: %d bytes, ends %q, valid UTF-8 %v", i, len(l), l[len(l)-8:], utf8.ValidString(l))
		}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if p := j.progress[len(j.progress)-1].text; len(p) > jobTextMax || !strings.HasSuffix(p, "…") {
		t.Fatalf("progress text: %d bytes", len(p))
	}
}

func TestRunnerSkipsEmptyProgress(t *testing.T) {
	r, _ := newJobRunner()
	srv := httptest.NewServer(http.HandlerFunc(r.ServeEvents))
	defer srv.Close()
	release := make(chan struct{})
	j, err := r.Start("clean", "Remove copies", func(log io.Writer) (any, error) {
		<-release
		// clean's counter puts \r first
		io.WriteString(log, "\rcomparing copies: 1/2")
		io.WriteString(log, "\rcomparing copies: 2/2\n")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	br := subscribeJob(t, srv, j, "")
	close(release)
	var got []string
	for _, e := range readJobEvents(t, br) {
		got = append(got, e.Event+" "+e.Data)
	}
	want := `progress "comparing copies: 1/2"|line "comparing copies: 2/2"|done {"ok":true,"result":null,"error":""}`
	if strings.Join(got, "|") != want {
		t.Fatalf("events:\n got %s\nwant %s", strings.Join(got, "|"), want)
	}
}

func TestRunnerLogsThePanicStack(t *testing.T) {
	r, rec := newJobRunner()
	j, err := r.Start("prune", "Delete", func(io.Writer) (any, error) { panic("boom") })
	if err != nil {
		t.Fatal(err)
	}
	st := j.Wait()
	if !strings.Contains(rec.text(), "goroutine ") || !strings.Contains(rec.text(), "jobs_test.go") {
		t.Fatalf("the log has no stack: %q", rec.text())
	}
	if st.Error != jobPlainText {
		t.Fatalf("the page got %q", st.Error)
	}
}

func TestRunnerPingsAnIdleStream(t *testing.T) {
	old := jobPingEvery
	jobPingEvery = 20 * time.Millisecond
	t.Cleanup(func() { jobPingEvery = old })
	r, _ := newJobRunner()
	srv := httptest.NewServer(http.HandlerFunc(r.ServeEvents))
	defer srv.Close()
	release := make(chan struct{})
	j, err := r.Start("push", "Back up now", func(io.Writer) (any, error) { <-release; return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	br := subscribeJob(t, srv, j, "")
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended before a ping: %v", err)
		}
		if l == ": ping\n" {
			break
		}
	}
	close(release)
	if got := readJobEvents(t, br); got[len(got)-1].Event != "done" {
		t.Fatalf("after the ping: %v", got)
	}
}

func TestRunnerFlushFailureIsLoggedOnce(t *testing.T) {
	r, rec := newJobRunner()
	j, err := r.Start("push", "Back up now", func(io.Writer) (any, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	j.Wait()
	for i := 0; i < 2; i++ {
		r.ServeEvents(noFlushWriter{httptest.NewRecorder()}, httptest.NewRequest("GET", "/?id="+j.State().ID, nil))
	}
	if n := strings.Count(rec.text(), "cannot stream"); n != 1 {
		t.Fatalf("logged %d times: %q", n, rec.text())
	}
}

// noFlushWriter hides the recorder's Flush, as a wrapping ResponseWriter would.
type noFlushWriter struct{ w *httptest.ResponseRecorder }

func (n noFlushWriter) Header() http.Header         { return n.w.Header() }
func (n noFlushWriter) Write(b []byte) (int, error) { return n.w.Write(b) }
func (n noFlushWriter) WriteHeader(code int)        { n.w.WriteHeader(code) }

func TestRunnerCloseRefusesNewJobs(t *testing.T) {
	r, _ := newJobRunner()
	if r.Close() != nil {
		t.Fatal("Close on an idle runner gave a job")
	}
	if j, err := r.Start("push", "Back up now", func(io.Writer) (any, error) { return nil, nil }); !errors.Is(err, ErrClosed) || j != nil {
		t.Fatalf("Start after Close: %v, %v, want ErrClosed", j, err)
	}

	r, _ = newJobRunner()
	block := make(chan struct{})
	a, err := r.Start("import", "Bring in work", func(io.Writer) (any, error) { <-block; return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Close(); got != a {
		t.Fatalf("Close gave %v, want the running job", got)
	}
	if _, err := r.Start("push", "Back up now", func(io.Writer) (any, error) { return nil, nil }); !errors.Is(err, ErrClosed) {
		t.Fatalf("Start after Close during a job: %v, want ErrClosed", err)
	}
	close(block)
	if st := a.Wait(); !st.OK {
		t.Fatalf("the running job did not finish: %+v", st)
	}
	if _, err := r.Start("push", "Back up now", func(io.Writer) (any, error) { return nil, nil }); !errors.Is(err, ErrClosed) {
		t.Fatalf("Start after the job ended: %v, want ErrClosed", err)
	}
}
