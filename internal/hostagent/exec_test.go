package hostagent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// systemBinary exists to keep PATH out of the resolution of system tools: on Windows a
// writable directory earlier in PATH is silent code execution as the user.
func TestSystemBinaryPrefersKnownLocation(t *testing.T) {
	name := "systemctl"
	if runtime.GOOS == "windows" {
		name = "schtasks"
	}

	got := systemBinary(name)

	if !filepath.IsAbs(got) {
		// A relative result means it fell through to the bare name, which is the case
		// this function exists to avoid. Only acceptable when the tool is genuinely
		// absent from the machine, as systemctl is on macOS and Windows.
		if runtime.GOOS == "windows" {
			t.Errorf("schtasks should resolve to an absolute path on Windows, got %q", got)
		}
		return
	}
	if _, err := os.Stat(got); err != nil {
		t.Errorf("systemBinary returned %q which does not exist: %v", got, err)
	}
}

// A PATH entry an attacker controls must not win.
func TestSystemBinaryIgnoresPath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the PATH-hijack case this guards against is Windows-specific")
	}

	evil := t.TempDir()
	if err := os.WriteFile(filepath.Join(evil, "schtasks.exe"), []byte("not really"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", evil+string(os.PathListSeparator)+os.Getenv("PATH"))

	got := systemBinary("schtasks")
	if strings.HasPrefix(strings.ToLower(got), strings.ToLower(evil)) {
		t.Errorf("systemBinary resolved to the PATH-injected copy: %q", got)
	}
	if !strings.Contains(strings.ToLower(got), "system32") {
		t.Errorf("expected the System32 copy, got %q", got)
	}
}

func TestSystemBinaryFallsBackToName(t *testing.T) {
	got := systemBinary("definitely-not-a-real-system-tool")
	// Nothing found anywhere: returning the bare name lets exec report a normal
	// "not found" rather than this function inventing a path.
	if got != expectedFallback("definitely-not-a-real-system-tool") {
		t.Errorf("got %q", got)
	}
}

func expectedFallback(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// Sweep must always answer, never error out, on every platform: doctor reports its
// result as one line and a missing scheduler is a normal state, not a failure.
func TestSweepReportsSomething(t *testing.T) {
	s, err := Sweep()
	if err != nil {
		t.Fatalf("Sweep returned an error: %v", err)
	}
	if s.Mechanism == "" {
		t.Error("Sweep must name the mechanism it checked")
	}
	if s.Detail == "" {
		t.Error("Sweep must explain what it found")
	}
}
