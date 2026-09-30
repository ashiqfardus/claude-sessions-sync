package hostagent

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf16"
)

// Install registers the periodic sweep with Task Scheduler.
//
// schtasks is used rather than the COM API: it is on every supported Windows, needs
// no elevation for a task the current user owns, and keeps this package free of a COM
// dependency. The definition is passed as XML - see TaskXML for why.
func Install(j Job, everyMinutes int) (Status, error) {
	s := Status{Mechanism: "Task Scheduler"}
	everyMinutes = normalMinutes(everyMinutes)

	// schtasks reads /XML files as UTF-16; a UTF-8 file is rejected as malformed.
	units := utf16.Encode([]rune(TaskXML(j, everyMinutes, time.Now())))
	buf := []byte{0xFF, 0xFE}
	for _, u := range units {
		buf = append(buf, byte(u), byte(u>>8))
	}
	tmp, err := os.CreateTemp("", "claude-sessions-task-*.xml")
	if err != nil {
		return s, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(buf); err != nil {
		tmp.Close()
		return s, err
	}
	if err := tmp.Close(); err != nil {
		return s, err
	}

	out, err := exec.Command(systemBinary("schtasks"),
		"/Create", "/TN", j.TaskName(), "/XML", tmp.Name(), "/F").CombinedOutput()
	if err != nil {
		return s, fmt.Errorf("schtasks failed: %v: %s", err, strings.TrimSpace(string(out)))
	}

	s.Installed = true
	s.Detail = fmt.Sprintf("%s, every %d minutes", j.TaskName(), everyMinutes)
	return s, nil
}

// Uninstall removes this profile's scheduled task, and only this tool's. It reports
// whether there was one.
//
// The task is queried and its action inspected first. Deleting by name alone removed
// a live task belonging to the PowerShell predecessor during testing on a real
// machine; a tool must never remove automation it did not install.
func Uninstall(j Job) (bool, error) {
	def, exists, err := queryTask(j.TaskName())
	if err != nil || !exists {
		return false, err // nothing of ours registered: not an error
	}
	if !ownsTask(def) {
		return false, fmt.Errorf("the task %q does not run this tool (%s %s) - refusing to remove it",
			j.TaskName(), def.Command, def.Arguments)
	}

	out, err := exec.Command(systemBinary("schtasks"), "/Delete", "/TN", j.TaskName(), "/F").CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("schtasks failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return true, nil
}

// LegacySweep reports whether the PowerShell predecessor's task is registered, so the
// installer can tell the user rather than quietly running two archivers.
func LegacySweep() (string, bool) {
	def, exists, err := queryTask(LegacyName)
	if err != nil || !exists {
		return "", false
	}
	return strings.TrimSpace(def.Command + " " + def.Arguments), true
}

// RemoveLegacySweep deletes the predecessor's task. Only ever called on an explicit
// request, and only when the task really is the PowerShell script.
func RemoveLegacySweep() error {
	def, exists, err := queryTask(LegacyName)
	if err != nil || !exists {
		return err
	}
	if !strings.Contains(strings.ToLower(def.Command+" "+def.Arguments), "sync-claude-sessions") {
		return fmt.Errorf("the task %q does not run the PowerShell script - refusing to remove it", LegacyName)
	}
	out, err := exec.Command(systemBinary("schtasks"), "/Delete", "/TN", LegacyName, "/F").CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// queryTask reads a task's definition.
//
// schtasks exits 1 both for a task that does not exist and for one it may not read,
// and its message is translated. "Missing" is therefore only concluded when listing
// every task works and does not include the name - otherwise an access-denied error
// was reported as "sweep removed" while the task stayed registered.
func queryTask(name string) (taskDefinition, bool, error) {
	out, err := exec.Command(systemBinary("schtasks"), "/query", "/tn", name, "/xml").Output()
	if err == nil {
		def, perr := parseTaskXML(out)
		return def, true, perr
	}

	list, listErr := exec.Command(systemBinary("schtasks"), "/query", "/fo", "CSV", "/nh").Output()
	if listErr != nil {
		return taskDefinition{}, false, fmt.Errorf("schtasks could not query %q: %v", name, err)
	}
	if strings.Contains(strings.ToLower(string(list)), strings.ToLower(`"\`+name+`"`)) {
		return taskDefinition{}, true, fmt.Errorf("the task %q exists but could not be read: %v", name, err)
	}
	return taskDefinition{}, false, nil
}
