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

	out, err := exec.Command(systemBinary("schtasks"), "/query", "/tn", Name, "/fo", "LIST", "/v").Output()
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

	// A task that has never run reports the sentinel date 30/11/1999 and result
	// 267011 (0x41303, "the task has not yet run"). Printing those verbatim reads as
	// a failure from the last century, which is how it looked the first time this was
	// run against a fresh install.
	if neverRun(lastRun, lastResult) {
		parts = append(parts, "not yet run")
	} else {
		if lastRun != "" {
			parts = append(parts, "last run "+lastRun)
		}
		if lastResult != "" {
			parts = append(parts, "result "+describeResult(lastResult))
		}
	}

	s.Detail = strings.Join(parts, ", ")
	return s, nil
}

func neverRun(lastRun, lastResult string) bool {
	return strings.Contains(lastRun, "1999") || strings.TrimSpace(lastResult) == "267011"
}

// describeResult translates the Task Scheduler codes worth naming. Anything else is
// passed through: an unexplained number the user can search for beats a wrong guess.
func describeResult(code string) string {
	switch strings.TrimSpace(code) {
	case "0":
		return "0 (success)"
	case "267009":
		return "currently running"
	case "267010":
		return "not yet run"
	case "267011":
		return "not yet run"
	case "267014":
		return "terminated by user"
	case "2147750687":
		return "an instance was already running"
	case "2147943645":
		return "the service is not available (is the user logged on?)"
	default:
		return code
	}
}
