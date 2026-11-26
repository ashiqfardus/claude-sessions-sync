package main

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/panel"
)

// otherVersionPanel runs a real panel for root whose /api/state reports version v, as
// a panel of another release answers it; every other route is the real one, and so is
// /api/stop unless stop replaces it. Its process counts as alive until it stops.
func otherVersionPanel(t *testing.T, root, v string, stop http.HandlerFunc) (*uiServer, *uiRun) {
	t.Helper()
	isolateUI(t)
	u, err := newUIServer(uiOpts{ClaudeDir: root, NoOpen: true, IdleAfter: time.Hour, Tick: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	routes := u.routes()
	for i, r := range routes {
		switch {
		case r.Path == "/api/state":
			routes[i].Handler = func(w http.ResponseWriter, r *http.Request) {
				sendJSON(w, map[string]any{"version": v, "machine": "LAP-1", "fromShortcut": true, "stopping": false, "job": currentJob(u.jobs)})
			}
		case r.Path == "/api/stop" && stop != nil:
			routes[i].Handler = stop
		}
	}
	if u.panel, err = panel.New(panel.Options{Routes: routes, Logf: u.logf}); err != nil {
		t.Fatal(err)
	}
	run := runUI(t, u)
	waitUntil(t, "panel.json", func() bool { return panelFileExists(u.root) })
	hook(t, &panelAlive, func(pid int) bool { return pid == os.Getpid() && run.running() })
	return u, run
}

type logLines struct {
	mu    sync.Mutex
	lines []string
}

func (l *logLines) logf(f string, a ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, fmt.Sprintf(f, a...))
	l.mu.Unlock()
}

func (l *logLines) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// After an upgrade, a launch finds the old version's panel still running with its tab
// open: idle, it is stopped, and this version starts and opens the browser.
func TestLaunchReplacesAnIdlePanelOfAnotherVersion(t *testing.T) {
	rec := recordOpens(t)
	old, oldRun := otherVersionPanel(t, t.TempDir(), "0.7.2", nil)
	login(t, old) // its tab is open

	second, err := newUIServer(uiOpts{ClaudeDir: old.root, IdleAfter: time.Hour, Tick: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	var startErr error
	done := make(chan struct{})
	go func() { startErr = second.start(&out); close(done) }()
	t.Cleanup(func() {
		second.stopSoon()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("the new panel did not stop after the test")
		}
	})
	oldRun.waitDone(t, 10*time.Second, "a launch of another version")
	waitUntil(t, "the browser to be opened", func() bool { return len(rec.get()) == 1 })

	path := rec.get()[0]
	url, port, _ := redirectTarget(t, path)
	if port == old.panel.Port() {
		t.Fatalf("the tab opened on the old panel's port %d", port)
	}
	resp, _ := send(t, old, http.MethodGet, url, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the new panel's key gave %d", resp.StatusCode)
	}
	cookie := sessionCookie(t, resp)
	_, body := send(t, old, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/api/state", port), cookie)
	if !strings.Contains(body, `"version":"`+version+`"`) {
		t.Errorf("the panel started is not this version: %s", body)
	}
	second.stopSoon()
	<-done
	if startErr != nil {
		t.Errorf("start: %v", startErr)
	}
	if want := "Stopped the running panel of version 0.7.2.\nThe panel is open at "; !strings.HasPrefix(out.String(), want) {
		t.Errorf("printed %q, want it to start %q", out.String(), want)
	}
}

// A panel of another version running a job is left to finish it: the launch opens a
// tab on it as before, and says when this version takes over.
func TestLaunchLeavesABusyPanelOfAnotherVersion(t *testing.T) {
	rec := recordOpens(t)
	old, oldRun := otherVersionPanel(t, t.TempDir(), "0.7.2", nil)
	t.Cleanup(blockingJob(t, old, "Back up now"))

	var out bytes.Buffer
	var log logLines
	ok, err := reuseRunning(old.root, false, &out, log.logf)
	if !ok || err != nil {
		t.Fatalf("reuseRunning = %v, %v", ok, err)
	}
	note := "The running panel is version 0.7.2 and busy with Back up now. It is replaced by " + version + " when you open the panel after that ends."
	if want := note + "\nThe panel is already running; opened a new tab.\n"; out.String() != want {
		t.Errorf("printed %q, want %q", out.String(), want)
	}
	if !strings.Contains(log.String(), "PANEL: "+note) {
		t.Errorf("the log has %q, want the note", log.String())
	}
	if !oldRun.running() || !panelFileExists(old.root) || old.jobs.Current() == nil {
		t.Error("the busy panel, or its job, was stopped")
	}
	if paths := rec.get(); len(paths) != 1 {
		t.Fatalf("opened %v, want one tab", paths)
	} else if _, port, _ := redirectTarget(t, paths[0]); port != old.panel.Port() {
		t.Errorf("the tab opened on port %d, want the running panel's %d", port, old.panel.Port())
	}
}

// A panel of another version that refuses Stop is handed off to, and the user is told
// how to stop it: it is never killed. One whose job started after its state was read
// answers that it stops after that job: it is handed off to as a busy one.
func TestLaunchHandsOffWhenAPanelOfAnotherVersionDoesNotStopNow(t *testing.T) {
	for _, c := range []struct {
		name  string
		reply func(http.ResponseWriter)
		note  string
	}{
		{"refused", func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) },
			"The running panel is version 0.7.2 and could not be asked to stop. Use Stop the panel on its Settings page, then open the panel again to start " + version + "."},
		{"a job started", func(w http.ResponseWriter) { sendJSON(w, map[string]string{"stopping": "after", "label": "Import"}) },
			"The running panel is version 0.7.2 and busy with Import. It is replaced by " + version + " when you open the panel after that ends."},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := recordOpens(t)
			old, oldRun := otherVersionPanel(t, t.TempDir(), "0.7.2", func(w http.ResponseWriter, r *http.Request) { c.reply(w) })
			var out bytes.Buffer
			ok, err := reuseRunning(old.root, false, &out, func(string, ...any) {})
			if !ok || err != nil {
				t.Fatalf("reuseRunning = %v, %v", ok, err)
			}
			if want := c.note + "\nThe panel is already running; opened a new tab.\n"; out.String() != want {
				t.Errorf("printed %q, want %q", out.String(), want)
			}
			if !oldRun.running() || len(rec.get()) != 1 {
				t.Errorf("running %v, opened %v: want the panel kept and one tab on it", oldRun.running(), rec.get())
			}
		})
	}
}

// install stops an idle panel of another version before it replaces the binaries, so
// the program is free and the shortcut starts the new version at once.
func TestInstallStopsAnIdlePanelOfAnotherVersion(t *testing.T) {
	root, _, _ := installEnv(t)
	_, oldRun := otherVersionPanel(t, root, "0.7.2", nil)
	var buf bytes.Buffer
	if _, err := runInstall(streams{&buf, &buf}, installOpts{ClaudeDir: root}); err != nil {
		t.Fatal(err)
	}
	if oldRun.running() {
		t.Error("install left the old panel running")
	}
	if !strings.Contains(buf.String(), "  archive ") || !strings.Contains(buf.String(), "\n  panel     Stopped the running panel of version 0.7.2.\n            Open the panel again to start "+version+".\n  binary ") {
		t.Errorf("install printed:\n%s", buf.String())
	}
}

// A busy one is left to finish its job.
func TestInstallLeavesABusyPanelOfAnotherVersion(t *testing.T) {
	root, _, _ := installEnv(t)
	old, oldRun := otherVersionPanel(t, root, "0.7.2", nil)
	t.Cleanup(blockingJob(t, old, "Back up now"))
	var buf bytes.Buffer
	if _, err := runInstall(streams{&buf, &buf}, installOpts{ClaudeDir: root}); err != nil {
		t.Fatal(err)
	}
	if !oldRun.running() {
		t.Error("install stopped a panel running a job")
	}
	if !strings.Contains(buf.String(), "  panel     The running panel is version 0.7.2 and busy with Back up now.") {
		t.Errorf("install printed:\n%s", buf.String())
	}
}

// Only the names replaceExecutable gives a binary it moves aside are removed, and one
// Windows refuses to remove, as it does a program still running, is left.
func TestRemoveOldBinaries(t *testing.T) {
	bin := t.TempDir()
	gone := []string{"claude-sessions.old-1795719385395748400", "claude-sessions.exe.old-12", "claude-sessionsw.exe.old-7"}
	kept := []string{"claude-sessions", "claude-sessions.exe", "claude-sessionsw.exe", "claude-sessions.old-", "claude-sessions.old-12a",
		"claude-sessions.old-12.bak", "claude-sessionsx.old-12", "notes.old-12", "claude-sessions.exe.OLD-34", "claude-sessionsw.exe.old-9-running"}
	for _, n := range append(append([]string{}, gone...), kept...) {
		writeFile(t, filepath.Join(bin, n), "x")
	}
	if err := os.Mkdir(filepath.Join(bin, "claude-sessions.old-5"), 0o755); err != nil { // not a file
		t.Fatal(err)
	}
	running := filepath.Join(bin, "claude-sessionsw.exe.old-9")
	writeFile(t, running, "x")
	hook(t, &removeFile, func(p string) error {
		if p == running {
			return &os.PathError{Op: "remove", Path: p, Err: os.ErrPermission}
		}
		return os.Remove(p)
	})

	got := removeOldBinaries(bin)
	var want []string
	for _, n := range gone {
		want = append(want, filepath.Join(bin, n))
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("removed %v, want %v", got, want)
	}
	for _, n := range append(kept, "claude-sessions.old-5", "claude-sessionsw.exe.old-9") {
		if _, err := os.Stat(filepath.Join(bin, n)); err != nil {
			t.Errorf("%s was removed", n)
		}
	}
	if got := removeOldBinaries(filepath.Join(bin, "missing")); len(got) != 0 {
		t.Errorf("a missing folder: %v", got)
	}
}

// install and every push remove what an earlier upgrade moved aside, and say so.
func TestInstallAndPushRemoveOldBinaries(t *testing.T) {
	root, _, _ := installEnv(t)
	old := filepath.Join(root, "bin", "claude-sessionsw.exe.old-1795719385395748400")
	writeFile(t, old, "x")
	var buf bytes.Buffer
	if _, err := runInstall(streams{&buf, &buf}, installOpts{ClaudeDir: root}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("install left %s (%v)", old, err)
	}
	if !strings.Contains(buf.String(), "  removed   "+old+", left by an earlier upgrade\n") {
		t.Errorf("install printed:\n%s", buf.String())
	}

	writeFile(t, old, "x")
	if _, err := runPush(quietStreams(), root, "", pushOpts{NoHTML: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("push left %s (%v)", old, err)
	}
	logged, _ := os.ReadFile(filepath.Join(root, "session-sync.log"))
	if !strings.Contains(string(logged), "CLEAN: removed a binary an upgrade replaced while it ran: "+old) {
		t.Errorf("the log has:\n%s", logged)
	}
}

// Only an older release is replaced, so two builds on one computer cannot stop each
// other's panel on every launch.
func TestOlderVersion(t *testing.T) {
	for _, c := range []struct {
		running, ours string
		want          bool
	}{
		{"0.7.3", "0.7.4", true},
		{"v0.7.3", "0.7.4", true},
		{"0.7.9", "0.7.10", true},
		{"0.6.9", "0.7.0", true},
		{"0.7.4", "0.7.4", false},
		{"0.7.4", "0.7.3", false}, // a downgrade, or the stale shortcut binary
		{"0.7.10", "0.7.9", false},
		{"0.7.3-rc1", "0.7.3", false},
		{"0.7.3", "dev", true},
		{"dev", "0.7.4", false},
		{"", "0.7.4", false},
		{"garbage", "0.7.4", false},
		{"0.7.3", "garbage", false},
	} {
		if got := olderVersion(c.running, c.ours); got != c.want {
			t.Errorf("olderVersion(%q, %q) = %v, want %v", c.running, c.ours, got, c.want)
		}
	}
}
