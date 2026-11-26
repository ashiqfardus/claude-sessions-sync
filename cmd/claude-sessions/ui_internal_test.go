package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/panel"
)

// isolateUI points the home and temp folders at a test folder: the redirect files go
// there (the home folder on Linux), and archive detection finds nothing.
func isolateUI(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	for _, k := range []string{"HOME", "USERPROFILE", "TMPDIR", "TMP", "TEMP"} {
		t.Setenv(k, dir)
	}
}

type opens struct {
	mu    sync.Mutex
	paths []string
}

func (o *opens) get() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.paths...)
}

// recordOpens replaces the browser opener, so no test opens a browser.
func recordOpens(t *testing.T) *opens {
	t.Helper()
	o := &opens{}
	old := openBrowser
	openBrowser = func(path string) error {
		o.mu.Lock()
		o.paths = append(o.paths, path)
		o.mu.Unlock()
		return nil
	}
	t.Cleanup(func() { openBrowser = old })
	return o
}

func newTestUI(t *testing.T, o uiOpts) *uiServer {
	t.Helper()
	isolateUI(t)
	if o.ClaudeDir == "" {
		o.ClaudeDir = t.TempDir()
	}
	if o.IdleAfter == 0 {
		o.IdleAfter = time.Hour
	}
	if o.Tick == 0 {
		o.Tick = time.Hour
	}
	u, err := newUIServer(o)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

type uiRun struct {
	done chan struct{} // closed when run returns; err and out are then safe to read
	err  error
	out  bytes.Buffer
}

func (r *uiRun) running() bool {
	select {
	case <-r.done:
		return false
	default:
		return true
	}
}

// waitDone fails the test unless run returns within d.
func (r *uiRun) waitDone(t *testing.T, d time.Duration, why string) {
	t.Helper()
	select {
	case <-r.done:
	case <-time.After(d):
		t.Fatalf("the panel is still running %v after %s", d, why)
	}
}

// runUI serves u on a fresh loopback port until the test ends.
func runUI(t *testing.T, u *uiServer) *uiRun {
	t.Helper()
	l, err := u.panel.Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	r := &uiRun{done: make(chan struct{})}
	go func() {
		r.err = u.run(l, &r.out)
		close(r.done)
	}()
	t.Cleanup(func() {
		u.stopSoon()
		select {
		case <-r.done:
		case <-time.After(10 * time.Second):
			t.Error("run did not return after the test")
		}
	})
	return r
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); !ok(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func panelFileExists(root string) bool {
	_, err := os.Stat(panelFilePath(root))
	return err == nil
}

func (u *uiServer) url(p string) string {
	return "http://127.0.0.1:" + strconv.Itoa(u.panel.Port()) + p
}

// send makes one request. A POST carries the Origin and token the panel's page sends.
func send(t *testing.T, u *uiServer, method, url, cookie string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	if method == http.MethodPost {
		req.Header.Set("Origin", "http://"+req.URL.Host)
		req.Header.Set("X-CS-Token", u.panel.Token())
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(b)
}

func sessionCookie(t *testing.T, resp *http.Response) string {
	t.Helper()
	for _, c := range resp.Cookies() {
		if strings.HasPrefix(c.Name, "cs_panel_") {
			return c.Name + "=" + c.Value
		}
	}
	t.Fatal("no session cookie")
	return ""
}

// login swaps a fresh key for the session cookie.
func login(t *testing.T, u *uiServer) string {
	t.Helper()
	resp, _ := send(t, u, http.MethodGet, u.panel.KeyURL(u.panel.NewKey()), "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/k/ gave %d", resp.StatusCode)
	}
	return sessionCookie(t, resp)
}

var keyedURL = regexp.MustCompile(`url=(http://127\.0\.0\.1:(\d+)/k/([0-9a-f]{64}))"`)

// redirectTarget reads the keyed URL, its port and its key out of a redirect file.
func redirectTarget(t *testing.T, path string) (url string, port int, key string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m := keyedURL.FindStringSubmatch(string(b))
	if m == nil {
		t.Fatalf("no keyed URL in the redirect file:\n%s", b)
	}
	port, _ = strconv.Atoi(m[2])
	return m[1], port, m[3]
}

// blockingJob starts a job that runs until the returned func is called (or the test ends).
func blockingJob(t *testing.T, u *uiServer, label string) func() {
	t.Helper()
	release := make(chan struct{})
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	if _, err := u.jobs.Start("push", label, func(io.Writer) (any, error) { <-release; return nil, nil }); err != nil {
		t.Fatal(err)
	}
	return free
}

func TestUIOpensARedirectFileNotTheKey(t *testing.T) {
	rec := recordOpens(t)
	u := newTestUI(t, uiOpts{})
	r := runUI(t, u)
	waitUntil(t, "the browser to be opened", func() bool { return len(rec.get()) > 0 })

	path := rec.get()[0]
	url, port, key := redirectTarget(t, path)
	if strings.Contains(path, key) {
		t.Errorf("the key is in the path handed to the opener: %s", path)
	}
	if port != u.panel.Port() {
		t.Errorf("the redirect file points at port %d, want %d", port, u.panel.Port())
	}
	if resp, _ := send(t, u, http.MethodGet, url, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("the redirect file's key gave %d", resp.StatusCode)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the redirect file is still there after its key was used (%v)", err)
	}

	u.stopSoon()
	r.waitDone(t, 10*time.Second, "Stop")
	if got := rec.get(); len(got) != 1 {
		t.Errorf("the opener got %d paths, want 1: %v", len(got), got)
	}
	want := fmt.Sprintf("The panel is open at http://127.0.0.1:%d/ - press Ctrl-C to stop it.\nIf your browser did not open, paste this address once: %s\n", u.panel.Port(), url)
	if r.out.String() != want {
		t.Errorf("printed:\n%s\nwant:\n%s", r.out.String(), want)
	}
}

func TestSecondLaunchReusesThePanel(t *testing.T) {
	rec := recordOpens(t)
	u := newTestUI(t, uiOpts{NoOpen: true})
	runUI(t, u)
	waitUntil(t, "panel.json", func() bool { return panelFileExists(u.root) })

	var out bytes.Buffer
	ok, err := reuseRunning(u.root, false, &out)
	if err != nil || !ok {
		t.Fatalf("reuseRunning = %v, %v with a panel running", ok, err)
	}
	paths := rec.get()
	if len(paths) != 1 {
		t.Fatalf("the opener got %v, want one redirect file", paths)
	}
	t.Cleanup(func() { os.Remove(paths[0]) })
	if out.String() != "The panel is already running; opened a new tab.\n" {
		t.Errorf("printed %q", out.String())
	}
	url, port, _ := redirectTarget(t, paths[0])
	if port != u.panel.Port() {
		t.Errorf("the new tab goes to port %d, want the running panel's %d", port, u.panel.Port())
	}
	resp, _ := send(t, u, http.MethodGet, url, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the new key gave %d", resp.StatusCode)
	}
	sessionCookie(t, resp)
	if resp, _ := send(t, u, http.MethodGet, url, ""); resp.StatusCode != http.StatusForbidden {
		t.Errorf("the new key worked twice: %d", resp.StatusCode)
	}

	// --no-open prints a new keyed address instead.
	out.Reset()
	if ok, err := reuseRunning(u.root, true, &out); err != nil || !ok {
		t.Fatalf("reuseRunning --no-open = %v, %v", ok, err)
	}
	prefix := fmt.Sprintf("Open this address once: http://127.0.0.1:%d/k/", u.panel.Port())
	if !strings.HasPrefix(out.String(), prefix) || len(rec.get()) != 1 {
		t.Errorf("--no-open printed %q (opened %v)", out.String(), rec.get())
	}
}

// deadPID is the PID of a process that has exited.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("go", "version")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func TestStalePanelFileStartsANewPanel(t *testing.T) {
	dead := deadPID(t) // before newTestUI moves HOME, where the go tool keeps its toolchains
	rec := recordOpens(t)
	u := newTestUI(t, uiOpts{NoOpen: true})
	runUI(t, u)
	root := t.TempDir()
	write := func(f panelFile) {
		b, _ := json.Marshal(f)
		writeFile(t, panelFilePath(root), string(b))
	}

	// A dead PID is no panel, even when something answers on its port.
	write(panelFile{PID: dead, Port: u.panel.Port(), Launch: u.panel.LaunchSecret()})
	if ok, err := reuseRunning(root, false, io.Discard); ok || err != nil {
		t.Errorf("a dead PID: reuseRunning = %v, %v", ok, err)
	}

	// A live PID (this one) whose port has no panel: the request fails.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	write(panelFile{PID: os.Getpid(), Port: port, Launch: "x"})
	if ok, err := reuseRunning(root, false, io.Discard); ok || err != nil {
		t.Errorf("no panel on the port: reuseRunning = %v, %v", ok, err)
	}
	if got := rec.get(); len(got) != 0 {
		t.Errorf("opened %v for a stale panel file", got)
	}
}

func TestShortcutPanelStopsAfterIdle(t *testing.T) {
	u := newTestUI(t, uiOpts{NoOpen: true, FromShortcut: true, IdleAfter: 200 * time.Millisecond, Tick: 20 * time.Millisecond})
	start := time.Now()
	r := runUI(t, u)
	r.waitDone(t, 5*time.Second, "200 ms without a heartbeat")
	if took := time.Since(start); took < 200*time.Millisecond {
		t.Errorf("stopped after %v, before IdleAfter", took)
	}
	if r.err != nil {
		t.Errorf("run: %v", r.err)
	}
	if !strings.HasPrefix(r.out.String(), "Open this address once: ") {
		t.Errorf("printed %q", r.out.String()) // printed only once panel.json is written
	}
	if panelFileExists(u.root) {
		t.Error("panel.json is left behind")
	}
}

func TestShortcutPanelNeverStopsDuringJob(t *testing.T) {
	u := newTestUI(t, uiOpts{NoOpen: true, FromShortcut: true, IdleAfter: 100 * time.Millisecond, Tick: 10 * time.Millisecond})
	free := blockingJob(t, u, "Back up now") // before run, so the job outlasts IdleAfter however slow the runner
	r := runUI(t, u)
	t.Cleanup(free)

	time.Sleep(500 * time.Millisecond)
	if !r.running() {
		t.Fatal("the panel stopped while a job was running")
	}
	cookie := login(t, u)
	// Not even set to stop after the job: the page may come back before it ends.
	if _, body := send(t, u, http.MethodGet, u.url("/api/state"), cookie); !strings.Contains(body, `"stopping":false`) {
		t.Errorf("/api/state during the job: %s", body)
	}
	free()
	r.waitDone(t, 5*time.Second, "the job ended")
	if panelFileExists(u.root) {
		t.Error("panel.json is left behind")
	}
}

func TestTerminalPanelHasNoIdleStop(t *testing.T) {
	u := newTestUI(t, uiOpts{NoOpen: true, IdleAfter: 100 * time.Millisecond, Tick: 10 * time.Millisecond})
	r := runUI(t, u)
	time.Sleep(300 * time.Millisecond)
	if !r.running() {
		t.Error("a panel started from a terminal stopped by itself")
	}
}

func TestStopWaitsForTheJob(t *testing.T) {
	u := newTestUI(t, uiOpts{NoOpen: true})
	free := blockingJob(t, u, "Back up now")
	r := runUI(t, u)
	t.Cleanup(free)
	cookie := login(t, u)

	resp, body := send(t, u, http.MethodPost, u.url("/api/stop"), cookie)
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(body) != `{"label":"Back up now","stopping":"after"}` {
		t.Fatalf("/api/stop during a job gave %d %s", resp.StatusCode, body)
	}
	_, body = send(t, u, http.MethodGet, u.url("/api/state"), cookie)
	var st struct {
		Stopping bool
		Job      *panel.State
	}
	if err := json.Unmarshal([]byte(body), &st); err != nil || !st.Stopping || st.Job == nil || st.Job.Label != "Back up now" {
		t.Errorf("/api/state while stopping: %s", body)
	}
	if _, err := u.jobs.Start("push", "Back up now", func(io.Writer) (any, error) { return nil, nil }); !errors.Is(err, panel.ErrClosed) {
		t.Errorf("a job started after Stop: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if !r.running() {
		t.Fatal("Stop cut the job off")
	}
	free()
	r.waitDone(t, 5*time.Second, "the job ended")
	if panelFileExists(u.root) {
		t.Error("panel.json is left behind")
	}
}

func TestStopWhenIdleStopsNow(t *testing.T) {
	u := newTestUI(t, uiOpts{NoOpen: true})
	r := runUI(t, u)
	cookie := login(t, u)
	resp, body := send(t, u, http.MethodPost, u.url("/api/stop"), cookie)
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(body) != `{"stopping":"now"}` {
		t.Fatalf("/api/stop gave %d %s", resp.StatusCode, body)
	}
	r.waitDone(t, 5*time.Second, "Stop with no job")
	if r.err != nil {
		t.Errorf("run: %v", r.err)
	}
	if panelFileExists(u.root) {
		t.Error("panel.json is left behind")
	}
}

func TestUIStateAndLog(t *testing.T) {
	u := newTestUI(t, uiOpts{NoOpen: true, FromShortcut: true})
	var log strings.Builder
	for i := 1; i <= 250; i++ {
		fmt.Fprintf(&log, "line %d\n", i)
	}
	writeFile(t, filepath.Join(u.root, "session-sync.log"), log.String())
	runUI(t, u)
	cookie := login(t, u)

	_, body := send(t, u, http.MethodGet, u.url("/api/state"), cookie)
	want := fmt.Sprintf(`{"version":%q,"machine":%q,"fromShortcut":true,"stopping":false,"job":null}`, version, machineName())
	if strings.TrimSpace(body) != want {
		t.Errorf("/api/state = %s, want %s", body, want)
	}
	if _, body := send(t, u, http.MethodGet, u.url("/api/jobs/current"), cookie); strings.TrimSpace(body) != "null" {
		t.Errorf("/api/jobs/current with no job = %s", body)
	}
	if resp, _ := send(t, u, http.MethodPost, u.url("/api/heartbeat"), cookie); resp.StatusCode/100 != 2 {
		t.Errorf("/api/heartbeat gave %d", resp.StatusCode)
	}

	_, body = send(t, u, http.MethodGet, u.url("/api/log"), cookie)
	var lines []string
	if err := json.Unmarshal([]byte(body), &lines); err != nil {
		t.Fatalf("/api/log: %v: %s", err, body)
	}
	if len(lines) != 200 || lines[0] != "line 51" || lines[199] != "line 250" {
		t.Errorf("/api/log gave %d lines, %q ... %q", len(lines), lines[0], lines[len(lines)-1])
	}
}

func TestReadPageIsSandboxed(t *testing.T) {
	isolateUI(t)
	root, dest := t.TempDir(), t.TempDir()
	if err := archive.SaveConfig(root, archive.Config{Destination: dest}); err != nil {
		t.Fatal(err)
	}
	const id, linkedID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee"
	writeFile(t, pagePath(dest, "web-app", id), "<p>the conversation</p>")
	secretDir := t.TempDir()
	secret := filepath.Join(secretDir, linkedID+".html")
	writeFile(t, secret, "SECRET")
	writeFile(t, filepath.Join(dest, "secret", "x"), "SECRET")

	u := newTestUI(t, uiOpts{ClaudeDir: root, NoOpen: true})
	runUI(t, u)
	cookie := login(t, u)

	resp, body := send(t, u, http.MethodGet, u.url("/read/web-app/"+id), cookie)
	if resp.StatusCode != http.StatusOK || body != "<p>the conversation</p>" {
		t.Fatalf("the page gave %d %q", resp.StatusCode, body)
	}
	if csp := resp.Header.Values("Content-Security-Policy"); len(csp) != 1 || csp[0] != panel.PageCSP {
		t.Errorf("CSP %q, want exactly %q", csp, panel.PageCSP)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type %q", ct)
	}

	refused := []string{"/read/..%2f..%2fsecret/x", "/read/web-app/not-a-session-id", "/read/web-app/" + id + "/x", "/read/web-app"}
	if os.Symlink(secret, pagePath(dest, "web-app", linkedID)) == nil {
		refused = append(refused, "/read/web-app/"+linkedID)
	} else {
		t.Log("no symlinks here: the linked-page cases are skipped")
	}
	if os.Symlink(secretDir, filepath.Join(dest, "html", "linked")) == nil {
		refused = append(refused, "/read/linked/"+linkedID)
	}
	const dirID = "cccccccc-bbbb-cccc-dddd-eeeeeeeeeeee"
	if err := os.MkdirAll(pagePath(dest, "web-app", dirID), 0o755); err != nil {
		t.Fatal(err)
	}
	refused = append(refused, "/read/web-app/"+dirID) // a folder named as a page
	for _, p := range refused {
		resp, body := send(t, u, http.MethodGet, u.url(p), cookie)
		if resp.StatusCode != http.StatusNotFound || strings.Contains(body, "SECRET") || !strings.Contains(body, "This conversation has no readable page.") {
			t.Errorf("%s gave %d %q", p, resp.StatusCode, body)
		}
		if csp := resp.Header.Get("Content-Security-Policy"); csp != panel.PageCSP {
			t.Errorf("%s: CSP %q", p, csp)
		}
	}
}

func TestPlainErrorNeverLeaksRawText(t *testing.T) {
	const generic = "That didn't work. The details are in the log on the Settings page."
	for err, want := range map[error]string{
		errors.New("open /x/y: permission denied"):                                         generic,
		fmt.Errorf("push: %w", fmt.Errorf("%w (pid 7, 3s old)", archive.ErrLockHeld)):      "Another backup or import is running on this computer. Try again in a minute.",
		errors.New("no archive destination: run `claude-sessions config set-destination`"): "No archive folder yet. Choose the folder your sync app keeps in sync.",
		errors.New("/a/b is not reachable - is the sync client mounted?"):                  "The archive folder can't be reached right now. Check that your sync app is running, then try again.",
		errors.New("/a is not available - is the sync client mounted?"):                    "The archive folder can't be reached right now. Check that your sync app is running, then try again.",
		errors.New(`"a b" cannot name a machine: use letters, digits, dots and dashes`):    "Use letters, digits, dots and dashes in the name.",
		errors.New("/x does not exist or is not a folder"):                                 "That folder doesn't exist on this computer.",
	} {
		if got := plainError(err); got != want {
			t.Errorf("plainError(%q) = %q, want %q", err, got, want)
		}
	}
}
