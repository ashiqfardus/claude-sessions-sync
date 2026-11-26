package main

import (
	"context"
	"crypto/hmac"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/panel"
)

// openBrowser shows a redirect file in the default browser; a test replaces it.
var openBrowser = panel.Open

func cmdUI(args []string) error {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	claudeDir := fs.String("claude-dir", "", "override $CLAUDE_CONFIG_DIR / ~/.claude")
	noOpen := fs.Bool("no-open", false, "print the address instead of opening the browser")
	port := fs.Int("port", 0, "listen on this port (default: a free one)")
	fromShortcut := fs.Bool("from-shortcut", false, "stop 10 minutes after the page was last open (the desktop shortcut sets it)")
	if err := parseFlags(fs, args, false); err != nil {
		return err
	}
	u, err := newUIServer(uiOpts{ClaudeDir: *claudeDir, Port: *port, NoOpen: *noOpen, FromShortcut: *fromShortcut,
		IdleAfter: 10 * time.Minute, Tick: 30 * time.Second})
	if err != nil {
		return err
	}
	if err := u.start(os.Stdout); err != nil {
		if *fromShortcut {
			u.logf("UI: %v", err) // a windowless start has no console: the log is the only place to say why
		}
		return err
	}
	return nil
}

type uiOpts struct {
	ClaudeDir            string
	Port                 int
	NoOpen, FromShortcut bool
	// From the shortcut, the panel stops IdleAfter after the last heartbeat; it looks every Tick.
	IdleAfter, Tick time.Duration
}

// cleanSummary is this run's last duplicates check, which Storage shows.
type cleanSummary struct {
	At     time.Time `json:"at"`
	Copies int       `json:"copies"`
	Bytes  int64     `json:"bytes"`
}

// panelFile tells a second `ui` where the running panel is and how to ask it for a key.
type panelFile struct {
	PID          int    `json:"pid"`
	Port         int    `json:"port"`
	Launch       string `json:"launch"`
	ProofKey     string `json:"proofKey"` // checks the reply to /launch; never sent
	FromShortcut bool   `json:"fromShortcut"`
}

// ownPanelFile is what this panel writes to its panel file.
func (u *uiServer) ownPanelFile() panelFile {
	return panelFile{PID: os.Getpid(), Port: u.panel.Port(), Launch: u.panel.LaunchSecret(), ProofKey: u.panel.ProofKey(), FromShortcut: u.opts.FromShortcut}
}

func panelFilePath(root string) string { return filepath.Join(root, "session-sync-base", "panel.json") }

type uiServer struct {
	root  string
	opts  uiOpts
	panel *panel.Server
	jobs  *panel.Runner
	logf  func(string, ...any)

	stop     chan struct{} // closed when the panel is to stop
	stopOnce sync.Once

	mu           sync.Mutex
	lastBeat     time.Time
	stopAfterJob bool
	lastClean    *cleanSummary
}

func newUIServer(o uiOpts) (*uiServer, error) {
	root, _, err := resolveRoot(o.ClaudeDir)
	if err != nil {
		return nil, err
	}
	u := &uiServer{root: root, opts: o, logf: newLogger(root, true, nil), stop: make(chan struct{})}
	u.jobs = panel.NewRunner(plainError, u.logf)
	if u.panel, err = panel.New(panel.Options{Routes: u.routes(), Logf: u.logf}); err != nil {
		return nil, err
	}
	return u, nil
}

// start opens a new tab on the panel already running for this profile, or starts one.
func (u *uiServer) start(out io.Writer) error {
	if u.opts.FromShortcut {
		out = io.Discard // no terminal: whatever catches the output must not get a keyed address
	}
	panel.CleanRedirects(time.Minute)
	if ok, err := reuseRunning(u.root, u.opts.NoOpen, out); ok {
		return err
	}
	l, err := u.panel.Listen(u.opts.Port)
	if err != nil {
		// Windows reports WSAEADDRINUSE, 10048, which is not its syscall.EADDRINUSE.
		if u.opts.Port != 0 && (errors.Is(err, syscall.EADDRINUSE) || errors.Is(err, syscall.Errno(10048))) {
			return fmt.Errorf("port %d is in use: choose another with --port, or leave it out", u.opts.Port)
		}
		return err
	}
	return u.run(l, out)
}

// run serves l until Ctrl-C, Stop, or - started from the shortcut - IdleAfter without
// a heartbeat and no job running.
func (u *uiServer) run(l net.Listener, out io.Writer) error {
	sigs := []os.Signal{os.Interrupt, syscall.SIGTERM}
	if !signal.Ignored(syscall.SIGHUP) { // under nohup it stays ignored
		sigs = append(sigs, syscall.SIGHUP) // the terminal was closed
	}
	ctx, cancel := signal.NotifyContext(context.Background(), sigs...)
	defer cancel()
	srv := &http.Server{Handler: u.panel.Handler(), ReadHeaderTimeout: 10 * time.Second}

	u.beat() // the start counts as a heartbeat
	pf := u.ownPanelFile()
	b, _ := json.Marshal(pf)
	if err := archive.WriteFileAtomic(panelFilePath(u.root), b, 0o600); err != nil {
		l.Close()
		return err
	}
	defer removePanelFile(u.root, pf)

	served := make(chan error, 1)
	go func() { served <- srv.Serve(l) }()

	key := u.panel.NewKey()
	url := u.panel.KeyURL(key)
	if u.opts.NoOpen {
		fmt.Fprintf(out, "Open this address once: %s\n", url)
	} else {
		if path, err := panel.WriteRedirect(url); err != nil {
			u.logf("UI: the browser was not opened: %v", err)
		} else {
			// Deleted when its key is used, after 60 s, or at exit, whichever comes first.
			gone := func() { os.Remove(path) }
			defer gone()
			defer time.AfterFunc(time.Minute, gone).Stop()
			u.panel.OnKeyUsed(func(k string) {
				if k == key {
					gone()
				}
			})
			if err := openBrowser(path); err != nil {
				u.logf("UI: the browser was not opened: %v", err)
			}
		}
		fmt.Fprintf(out, "The panel is open at http://127.0.0.1:%d/ - press Ctrl-C to stop it.\nIf your browser did not open, paste this address once: %s\n", u.panel.Port(), url)
	}

	tick := time.NewTicker(u.opts.Tick)
	defer tick.Stop()
	for {
		select {
		case err := <-served:
			return err
		case <-ctx.Done():
			srv.Close() // Ctrl-C stops at once, even during a job (Ruling P12)
			return nil
		case <-u.stop:
			// An event stream whose reader stopped reading keeps its connection busy, so Shutdown gets a deadline.
			sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer scancel()
			if srv.Shutdown(sctx) != nil {
				srv.Close()
			}
			return nil
		case <-tick.C:
			panel.CleanRedirects(time.Minute) // a second `ui`'s file, which it left to this panel
			if u.opts.FromShortcut && u.idle() {
				u.stopSoon()
			}
		}
	}
}

// removePanelFile removes the panel file only if it is still this panel's: two panels
// started at once both write it, and the first to exit must not take the other's.
func removePanelFile(root string, pf panelFile) {
	var cur panelFile
	if b, err := os.ReadFile(panelFilePath(root)); err == nil && json.Unmarshal(b, &cur) == nil && cur == pf {
		os.Remove(panelFilePath(root))
	}
}

// reuseRunning opens a new tab on the panel already running for this profile, and
// reports whether there is one. A stale panel file, or a panel that does not answer,
// is no panel: the caller starts a new one.
func reuseRunning(root string, noOpen bool, out io.Writer) (bool, error) {
	var pf panelFile
	b, err := os.ReadFile(panelFilePath(root))
	if err != nil || json.Unmarshal(b, &pf) != nil || pf.PID <= 0 || !archive.ProcessAlive(pf.PID) {
		return false, nil
	}
	key, ok := launchKey(pf)
	if !ok {
		return false, nil
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/k/%s", pf.Port, key)
	if noOpen {
		fmt.Fprintf(out, "Open this address once: %s\n", url)
		return true, nil
	}
	path, err := panel.WriteRedirect(url)
	if err == nil {
		if err = openBrowser(path); err != nil {
			os.Remove(path)
		}
	}
	if err != nil {
		fmt.Fprintf(out, "The panel is already running. Open this address once: %s\n", url)
		return true, err
	}
	// This process exits now; the running panel removes the file after 60 s.
	fmt.Fprintln(out, "The panel is already running; opened a new tab.")
	return true, nil
}

// launchKey asks the panel pf names for a new one-time key.
func launchKey(pf panelFile) (string, bool) {
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/launch", pf.Port), nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("X-CS-Launch", pf.Launch)
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	var k struct {
		Key   string `json:"key"`
		Proof string `json:"proof"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<10)).Decode(&k) != nil {
		return "", false
	}
	// The port may be another program's by now, which has just seen pf.Launch: only the
	// panel itself holds pf.ProofKey, so only its key is opened.
	if b, err := hex.DecodeString(k.Key); err != nil || len(b) != 32 || pf.ProofKey == "" ||
		!hmac.Equal([]byte(k.Proof), []byte(panel.LaunchProof(pf.ProofKey, k.Key))) {
		return "", false
	}
	return k.Key, true
}

func (u *uiServer) beat() {
	u.mu.Lock()
	u.lastBeat = time.Now()
	u.mu.Unlock()
}

func (u *uiServer) idle() bool {
	u.mu.Lock()
	quiet := time.Since(u.lastBeat) > u.opts.IdleAfter
	u.mu.Unlock()
	return quiet && !u.jobs.Busy()
}

// stopSoon stops the panel now, or when the running job ends, and gives that job.
// Runner.Close decides and refuses every later job in one step, so no job starts once
// the panel is stopping (Ruling P22). A job that started just after an idle check is
// waited for like any other.
func (u *uiServer) stopSoon() *panel.Job {
	removePanelFile(u.root, u.ownPanelFile()) // the next `ui` starts a new panel, not a tab on this one
	j := u.jobs.Close()
	if j == nil {
		u.stopOnce.Do(func() { close(u.stop) })
		return nil
	}
	u.mu.Lock()
	u.stopAfterJob = true
	u.mu.Unlock()
	go func() {
		j.Wait()
		u.stopSoon() // the runner is free now, so this stops it
	}()
	return j
}

func (u *uiServer) routes() []panel.Route {
	get, post := http.MethodGet, http.MethodPost
	return []panel.Route{
		{Method: get, Path: "/api/state", Handler: u.apiState},
		{Method: post, Path: "/api/heartbeat", Handler: func(w http.ResponseWriter, r *http.Request) {
			u.beat()
			w.WriteHeader(http.StatusNoContent)
		}},
		{Method: post, Path: "/api/stop", Handler: u.apiStop},
		{Method: get, Path: "/api/jobs/current", Handler: func(w http.ResponseWriter, r *http.Request) { sendJSON(w, currentJob(u.jobs)) }},
		{Method: get, Path: "/api/jobs/events", Handler: u.jobs.ServeEvents},
		{Method: get, Path: "/api/log", Handler: u.apiLog},
		{Method: get, Path: "/api/projects", Handler: u.apiProjects},
		{Method: get, Path: "/api/conversations", Handler: u.apiConversations},
		{Method: get, Path: "/read/", Prefix: true, Handler: u.readPage},
	}
}

func sendJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func sendStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func currentJob(r *panel.Runner) *panel.Summary {
	if j := r.Current(); j != nil {
		s := j.Summary()
		return &s
	}
	return nil
}

func (u *uiServer) apiState(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	stopping := u.stopAfterJob
	u.mu.Unlock()
	select {
	case <-u.stop:
		stopping = true
	default:
	}
	sendJSON(w, struct {
		Version      string         `json:"version"`
		Machine      string         `json:"machine"`
		FromShortcut bool           `json:"fromShortcut"`
		Stopping     bool           `json:"stopping"`
		Job          *panel.Summary `json:"job"`
	}{version, machineName(), u.opts.FromShortcut, stopping, currentJob(u.jobs)})
}

// apiStop stops at once with no job running, else when the job ends (Ruling P12).
// Shutdown waits for this handler, so the answer is always sent.
func (u *uiServer) apiStop(w http.ResponseWriter, r *http.Request) {
	if j := u.stopSoon(); j != nil {
		sendJSON(w, map[string]string{"stopping": "after", "label": j.Label()})
		return
	}
	sendJSON(w, map[string]string{"stopping": "now"})
}

// apiLog gives the last 200 lines of this computer's log.
func (u *uiServer) apiLog(w http.ResponseWriter, r *http.Request) {
	raw, _ := os.ReadFile(filepath.Join(u.root, "session-sync.log"))
	lines := []string{}
	if s := strings.TrimRight(string(raw), "\n"); s != "" {
		lines = strings.Split(s, "\n")
	}
	if len(lines) > 200 {
		lines = lines[len(lines)-200:]
	}
	sendJSON(w, lines)
}

const noPage = "This conversation has no readable page. Rebuild pages in Settings, or check that its project is not left out of pages."

// readPage serves a conversation's page, under the guard's PageCSP: no script.
func (u *uiServer) readPage(w http.ResponseWriter, r *http.Request) {
	bucket, id, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/read/"), "/")
	f := openPage(u.root, bucket, id)
	if f == nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, noPage+"\n")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Security-Policy", panel.PageCSP)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.Copy(w, f)
}

// openPage opens a page in the archive, or gives nil. The archive is shared, so a name
// or a link planted there must not reach a file outside it.
func openPage(root, bucket, id string) *os.File {
	if !archive.ValidName(bucket) || !claude.IsSessionID(id) {
		return nil
	}
	dest, _, err := archive.ResolveDestination(root, "")
	if err != nil || dest == "" {
		return nil
	}
	p := pagePath(dest, bucket, id)
	if !archive.IsRealDir(filepath.Join(dest, "html")) || !archive.IsRealDir(filepath.Dir(p)) {
		return nil
	}
	st, err := os.Lstat(p)
	if err != nil || !st.Mode().IsRegular() {
		return nil
	}
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	// The file opened must be the one checked, not a link swapped in between.
	if fst, err := f.Stat(); err != nil || !os.SameFile(st, fst) {
		f.Close()
		return nil
	}
	return f
}

// plainError is the sentence the page shows for a failed action. The raw error goes
// only to the log: it can hold paths and other detail.
func plainError(err error) string {
	msg := err.Error()
	switch {
	case errors.Is(err, archive.ErrLockHeld):
		return "Another backup or import is running on this computer. Try again in a minute."
	case strings.Contains(msg, "no archive destination"):
		return "No archive folder yet. Choose the folder your sync app keeps in sync."
	case strings.Contains(msg, "is not reachable"), strings.Contains(msg, "is not available"):
		return "The archive folder can't be reached right now. Check that your sync app is running, then try again."
	case strings.Contains(msg, "cannot name a machine"): // config set-machine refused the name
		return "Use letters, digits, dots and dashes in the name."
	case strings.Contains(msg, "does not exist or is not a folder"):
		return "That folder doesn't exist on this computer."
	}
	return "That didn't work. The details are in the log on the Settings page."
}
