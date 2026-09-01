package hostagent

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// systemBinary resolves a system tool to an absolute path.
//
// SECURITY: exec.Command("schtasks", ...) resolves through PATH, and on Windows a
// writable directory earlier in PATH gives an attacker silent code execution as the
// user. Every system tool this package runs is therefore looked up in its known
// location first, and PATH is only a fallback for unusual layouts.
func systemBinary(name string) string {
	var dirs []string

	if runtime.GOOS == "windows" {
		root := os.Getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		dirs = []string{
			filepath.Join(root, "System32"),
			filepath.Join(root, "Sysnative"), // 32-bit process on 64-bit Windows
			root,
		}
		name += ".exe"
	} else {
		dirs = []string{"/bin", "/usr/bin", "/sbin", "/usr/sbin", "/usr/local/bin"}
	}

	for _, d := range dirs {
		p := filepath.Join(d, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return name
}
