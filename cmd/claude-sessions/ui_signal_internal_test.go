//go:build !windows

package main

import (
	"os/signal"
	"syscall"
	"testing"
	"time"
)

// Closing the terminal sends SIGHUP: the panel stops and takes panel.json, and with
// it the launch secret, away.
func TestClosedTerminalStopsThePanel(t *testing.T) {
	if signal.Ignored(syscall.SIGHUP) {
		t.Skip("the tests run under nohup")
	}
	u := newTestUI(t, uiOpts{NoOpen: true})
	r := runUI(t, u)
	waitUntil(t, "panel.json", func() bool { return panelFileExists(u.root) }) // written after the signals are set up
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	r.waitDone(t, 5*time.Second, "SIGHUP")
	if panelFileExists(u.root) {
		t.Error("panel.json is left behind")
	}
}

// Under nohup SIGHUP is ignored, and stays so: the panel keeps running.
func TestNohupPanelIgnoresSIGHUP(t *testing.T) {
	signal.Ignore(syscall.SIGHUP)
	t.Cleanup(func() { signal.Reset(syscall.SIGHUP) })
	u := newTestUI(t, uiOpts{NoOpen: true})
	r := runUI(t, u)
	waitUntil(t, "panel.json", func() bool { return panelFileExists(u.root) })
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if !r.running() {
		t.Error("a panel started under nohup stopped on SIGHUP")
	}
}
