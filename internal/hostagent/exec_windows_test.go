package hostagent

import (
	"context"
	"os/exec"
	"testing"
)

// Without CREATE_NO_WINDOW each schtasks run from the windowless panel opened a
// Windows Terminal window.
func TestCommandStartsConsoleChildrenWithoutAWindow(t *testing.T) {
	for _, cmd := range []*exec.Cmd{Command("schtasks"), CommandContext(context.Background(), "schtasks")} {
		a := cmd.SysProcAttr
		if a == nil || a.CreationFlags&createNoWindow == 0 || !a.HideWindow {
			t.Errorf("SysProcAttr %+v, want HideWindow and CREATE_NO_WINDOW", a)
		}
	}
}
