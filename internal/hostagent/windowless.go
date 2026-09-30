package hostagent

import (
	"encoding/binary"
	"errors"
)

// WindowlessName is the copy of the binary the Windows sweep runs.
//
// Task Scheduler opens a console window for every console program it starts, so a
// sweep running claude-sessions.exe flashed a terminal every 30 minutes. The copy is
// marked as a GUI program, which Windows starts with no console at all. Wrapping the
// binary in `conhost --headless` hid the window too, but swallowed the exit code (the
// task reported success even when the binary was missing) and is a launch pattern
// endpoint security products flag.
const WindowlessName = "claude-sessionsw.exe"

const (
	subsystemGUI     = 2
	subsystemConsole = 3
)

// Windowless returns a copy of a Windows executable with its PE subsystem set to GUI.
//
// Only the two-byte Subsystem field of the optional header changes. It sits at the
// same offset in PE32 and PE32+ headers. The header checksum is not updated because
// Windows only verifies it for drivers. A signed binary would lose its signature, and
// this one is not signed.
func Windowless(exe []byte) ([]byte, error) {
	bad := errors.New("not a Windows console executable")
	if len(exe) < 0x40 || exe[0] != 'M' || exe[1] != 'Z' {
		return nil, bad
	}
	pe := int(binary.LittleEndian.Uint32(exe[0x3C:]))
	field := pe + 4 + 20 + 68 // signature, COFF header, then Subsystem in the optional header
	if pe < 0 || field+2 > len(exe) || string(exe[pe:pe+4]) != "PE\x00\x00" {
		return nil, bad
	}
	switch binary.LittleEndian.Uint16(exe[field:]) {
	case subsystemConsole, subsystemGUI:
	default:
		return nil, bad
	}
	out := append([]byte(nil), exe...)
	binary.LittleEndian.PutUint16(out[field:], subsystemGUI)
	return out, nil
}
