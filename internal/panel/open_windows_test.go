package panel

import "testing"

// The browser opener keeps CREATE_NO_WINDOW but must not pass SW_HIDE to the browser.
func TestOpenerDoesNotHideTheBrowser(t *testing.T) {
	a := openCmd("x.html").SysProcAttr
	if a == nil || a.CreationFlags&0x08000000 == 0 || a.HideWindow {
		t.Errorf("SysProcAttr %+v, want CREATE_NO_WINDOW without HideWindow", a)
	}
}
