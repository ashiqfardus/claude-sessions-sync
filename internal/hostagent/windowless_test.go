package hostagent

import (
	"debug/pe"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// fakePE is the smallest header Windowless has to understand: MZ, the PE offset, the
// PE signature, a COFF header, and an optional header long enough to hold Subsystem.
func fakePE(subsystem uint16) []byte {
	b := make([]byte, 0x80+4+20+96)
	b[0], b[1] = 'M', 'Z'
	binary.LittleEndian.PutUint32(b[0x3C:], 0x80)
	copy(b[0x80:], "PE\x00\x00")
	binary.LittleEndian.PutUint16(b[0x80+4+20+68:], subsystem)
	return b
}

func TestWindowlessSetsGUISubsystem(t *testing.T) {
	in := fakePE(subsystemConsole)
	out, err := Windowless(in)
	if err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint16(out[0x80+4+20+68:]); got != subsystemGUI {
		t.Errorf("subsystem = %d, want %d", got, subsystemGUI)
	}
	if binary.LittleEndian.Uint16(in[0x80+4+20+68:]) != subsystemConsole {
		t.Error("the input was modified")
	}
}

// Anything that is not a console PE is refused rather than patched blindly: writing
// two bytes into an arbitrary file makes a broken sweep that looks installed, and a copy
// already windowless taken for the console program puts it where the hooks run it.
func TestWindowlessRejectsOtherFiles(t *testing.T) {
	elf := append([]byte("\x7fELF"), make([]byte, 200)...)
	truncated := fakePE(subsystemConsole)[:0x90]
	native := fakePE(1)
	badOffset := fakePE(subsystemConsole)
	binary.LittleEndian.PutUint32(badOffset[0x3C:], 0xFFFFFFF0)
	for name, b := range map[string][]byte{"elf": elf, "truncated": truncated, "native": native, "offset": badOffset, "empty": nil,
		"already windowless": fakePE(subsystemGUI)} {
		if _, err := Windowless(b); err == nil {
			t.Errorf("%s: patched, want an error", name)
		}
	}
}

// On Windows, patch this real test binary and run it: the header must parse as GUI,
// Go's runtime must still start without a console, and the exit code must come back.
// That exit code is what the task's last result reports - the signal conhost lost.
func TestWindowlessRealBinaryRuns(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("needs a Windows executable")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Windowless(data)
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), WindowlessName)
	if err := os.WriteFile(exe, out, 0o755); err != nil {
		t.Fatal(err)
	}

	f, err := pe.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	h, ok := f.OptionalHeader.(*pe.OptionalHeader64)
	f.Close()
	if !ok || h.Subsystem != pe.IMAGE_SUBSYSTEM_WINDOWS_GUI {
		t.Fatalf("patched header = %#v", f.OptionalHeader)
	}

	if err := exec.Command(exe, "-test.run=^$").Run(); err != nil {
		t.Fatalf("patched binary did not run cleanly: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=TestExitCodeHelper")
	cmd.Env = append(os.Environ(), "CLAUDE_SESSIONS_EXIT_HELPER=1")
	err = cmd.Run()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 7 {
		t.Fatalf("exit code did not come back: %v", err)
	}
}

func TestExitCodeHelper(t *testing.T) {
	if os.Getenv("CLAUDE_SESSIONS_EXIT_HELPER") == "1" {
		os.Exit(7)
	}
}

// A task is ours only when the program it runs is ours. Matching the whole command
// line claimed a foreign task that merely passed a path containing
// "claude-sessions", and uninstall deletes whatever is claimed.
func TestOwnsTask(t *testing.T) {
	cases := []struct {
		command, args string
		want          bool
	}{
		{`C:\Users\me\.claude\bin\claude-sessionsw.exe`, "push --quiet", true},
		{`"C:\Users\First Last\.claude\bin\CLAUDE-SESSIONS.EXE"`, "push --quiet", true},
		{`C:\tools\backup.exe`, `--src D:\claude-sessions-archive`, false},
		{`%SystemRoot%\System32\conhost.exe`, `--headless C:\x\claude-sessions.exe push --quiet`, false},
		{`powershell.exe`, `-File C:\x\sync-claude-sessions.ps1`, false},
		{``, ``, false},
	}
	for _, tc := range cases {
		if got := ownsTask(taskDefinition{Command: tc.command, Arguments: tc.args}); got != tc.want {
			t.Errorf("ownsTask(%q %q) = %v, want %v", tc.command, tc.args, got, tc.want)
		}
	}
}

// Backslashes before a closing quote escape it, so a quoted argument ending in one
// swallowed the quote and every argument after it.
func TestWindowsArgsTrailingBackslash(t *testing.T) {
	got := windowsArgs([]string{"push", "--claude-dir", `\\srv\my share\`, "--quiet"})
	if want := `push --claude-dir "\\srv\my share\\" --quiet`; got != want {
		t.Errorf("windowsArgs = %s, want %s", got, want)
	}
}

// A run killed before it finished - the one-hour limit on a hung push - is not a
// healthy sweep.
func TestTerminatedRunIsUnhealthy(t *testing.T) {
	if resultIsHealthy("267014") {
		t.Error("267014 (terminated) reported as healthy")
	}
	for _, ok := range []string{"0", "267009", "267011", ""} {
		if !resultIsHealthy(ok) {
			t.Errorf("%q reported as unhealthy", ok)
		}
	}
}
