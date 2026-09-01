package hostagent

import (
	"os/exec"
	"strings"
)

// Sweep reports the state of the Task Scheduler job.
//
// schtasks is used rather than the COM API: it is present on every supported
// Windows, needs no elevation to query a task the current user owns, and keeps this
// package free of a CGO or COM dependency.
func Sweep() (Status, error) {
	s := Status{Mechanism: "Task Scheduler"}

	out, err := exec.Command("schtasks", "/query", "/tn", Name, "/fo", "LIST", "/v").Output()
	if err != nil {
		// A missing task is the normal "not installed yet" answer, not a failure:
		// schtasks exits non-zero for it.
		s.Detail = "not registered"
		return s, nil
	}

	var status, lastResult, lastRun string
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "Status":
			status = value
		case "Last Result":
			lastResult = value
		case "Last Run Time":
			lastRun = value
		}
	}

	s.Installed = true
	parts := []string{}
	if status != "" {
		parts = append(parts, strings.ToLower(status))
	}
	if lastRun != "" {
		parts = append(parts, "last run "+lastRun)
	}
	if lastResult != "" {
		parts = append(parts, "result "+lastResult)
	}
	s.Detail = strings.Join(parts, ", ")
	return s, nil
}
