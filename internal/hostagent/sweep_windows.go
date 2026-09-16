package hostagent

import (
	"encoding/csv"
	"os"
	"os/exec"
	"strings"
)

// Sweep reports the state of this profile's Task Scheduler job.
func Sweep(j Job) (Status, error) {
	s := Status{Mechanism: "Task Scheduler"}

	def, exists, err := queryTask(j.TaskName())
	if err != nil {
		return s, err
	}
	if !exists {
		s.Detail = j.TaskName() + " not registered"
		return s, nil
	}
	s.Installed = true

	var parts []string
	if strings.EqualFold(strings.TrimSpace(def.Enabled), "false") {
		s.Problem = "the task is disabled, so nothing is being archived"
	}
	switch {
	case !ownsAction(def.Command + " " + def.Arguments):
		s.Problem = "the task runs " + def.Command + ", not this tool"
	case def.Command != "":
		if _, err := os.Stat(strings.Trim(def.Command, `"`)); err != nil {
			s.Problem = "the task's program is missing: " + def.Command
		}
	}

	// Last run and result come from the verbose CSV by column position: the header
	// names are translated, the columns are not.
	lastRun, lastResult := "", ""
	if out, err := exec.Command(systemBinary("schtasks"), "/query", "/tn", j.TaskName(), "/fo", "CSV", "/v", "/nh").Output(); err == nil {
		if rows, err := csv.NewReader(strings.NewReader(string(out))).ReadAll(); err == nil && len(rows) > 0 && len(rows[0]) > 6 {
			lastRun, lastResult = rows[0][5], rows[0][6]
		}
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
			if !resultIsHealthy(lastResult) && s.Problem == "" {
				s.Problem = "the last run failed to start (result " + describeResult(lastResult) + ")"
			}
		}
	}

	s.Detail = j.TaskName()
	if len(parts) > 0 {
		s.Detail += ", " + strings.Join(parts, ", ")
	}
	return s, nil
}
