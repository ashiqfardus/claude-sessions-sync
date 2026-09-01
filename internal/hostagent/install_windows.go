package hostagent

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Install registers the periodic sweep with Task Scheduler.
//
// schtasks is used rather than the COM API: it is on every supported Windows, needs
// no elevation for a task the current user owns, and keeps this package free of a COM
// dependency.
//
// /SC MINUTE /MO <n> repeats indefinitely. The PowerShell predecessor could not
// express that and used a daily trigger with a repetition window as a workaround;
// calling schtasks directly avoids the whole problem.
func Install(binary string, everyMinutes int) (Status, error) {
	s := Status{Mechanism: "Task Scheduler"}
	if everyMinutes <= 0 {
		everyMinutes = 30
	}

	// The command must be one quoted string: the binary path routinely contains
	// spaces (C:\Users\First Last\...).
	action := fmt.Sprintf(`"%s" push --quiet`, binary)

	out, err := exec.Command(systemBinary("schtasks"), "/Create",
		"/TN", Name,
		"/TR", action,
		"/SC", "MINUTE",
		"/MO", strconv.Itoa(everyMinutes),
		"/F", // replace OUR OWN task rather than failing; the name is ours alone
	).CombinedOutput()
	if err != nil {
		return s, fmt.Errorf("schtasks failed: %v: %s", err, strings.TrimSpace(string(out)))
	}

	s.Installed = true
	s.Detail = fmt.Sprintf("every %d minutes", everyMinutes)
	return s, nil
}

// Uninstall removes this tool's scheduled task, and only this tool's.
//
// The task is queried and its action inspected first. Deleting by name alone removed
// a live task belonging to the PowerShell predecessor during testing on a real
// machine; a tool must never remove automation it did not install.
func Uninstall() error {
	action, exists, err := taskAction(Name)
	if err != nil {
		return err
	}
	if !exists {
		return nil // nothing of ours registered: not an error
	}
	if !ownsAction(action) {
		return fmt.Errorf("the task %q does not run this tool (%s) - refusing to remove it", Name, action)
	}

	out, err := exec.Command(systemBinary("schtasks"), "/Delete", "/TN", Name, "/F").CombinedOutput()
	if err != nil {
		text := strings.ToLower(string(out))
		if strings.Contains(text, "cannot find") || strings.Contains(text, "does not exist") {
			return nil
		}
		return fmt.Errorf("schtasks failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// LegacySweep reports whether the PowerShell predecessor's task is registered, so the
// installer can tell the user rather than quietly running two archivers.
func LegacySweep() (string, bool) {
	action, exists, err := taskAction(LegacyName)
	if err != nil || !exists {
		return "", false
	}
	return action, true
}

// RemoveLegacySweep deletes the predecessor's task. Only ever called on an explicit
// request, and only when the task really is the PowerShell script.
func RemoveLegacySweep() error {
	action, exists, err := taskAction(LegacyName)
	if err != nil || !exists {
		return err
	}
	if !strings.Contains(strings.ToLower(action), "sync-claude-sessions") {
		return fmt.Errorf("the task %q does not run the PowerShell script - refusing to remove it", LegacyName)
	}
	out, err := exec.Command(systemBinary("schtasks"), "/Delete", "/TN", LegacyName, "/F").CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// taskAction returns the command a task runs.
func taskAction(name string) (action string, exists bool, err error) {
	out, cmdErr := exec.Command(systemBinary("schtasks"), "/query", "/tn", name, "/fo", "LIST", "/v").Output()
	if cmdErr != nil {
		// schtasks exits non-zero for a task that does not exist, which is a normal
		// answer rather than a failure.
		return "", false, nil
	}
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(key), "Task To Run") {
			return strings.TrimSpace(value), true, nil
		}
	}
	return "", true, nil
}

// ownsAction reports whether a task's command line is this tool's binary.
//
// "sync-claude-sessions.ps1" contains "claude-sessions", so the PowerShell script has
// to be excluded explicitly - the same substring trap that made doctor report two
// archivers when only one was installed.
func ownsAction(action string) bool {
	a := strings.ToLower(action)
	if strings.Contains(a, "sync-claude-sessions") {
		return false
	}
	return strings.Contains(a, "claude-sessions")
}
