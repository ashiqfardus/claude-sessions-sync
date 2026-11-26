package hostagent

import (
	"os"
	"strings"
)

// Sweep reports the state of this profile's launchd user agent.
//
// NOT VERIFIED ON A MAC beyond CI. Do not claim macOS support on the strength of it
// compiling.
func Sweep(j Job) (Status, error) {
	s := Status{Mechanism: "launchd"}

	path, err := plistPath(j)
	if err != nil {
		return s, err
	}
	if _, err := os.Stat(path); err != nil {
		s.Detail = "not registered"
		return s, nil
	}
	s.Installed = true
	s.Detail = path

	// `launchctl list <label>` prints the last exit status when the job is loaded.
	if out, err := Command(systemBinary("launchctl"), "list", j.LaunchdLabel()).Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "LastExitStatus") {
				s.Detail = path + ", " + strings.TrimSpace(line)
				break
			}
		}
	} else {
		s.Problem = "the agent is present but not loaded (launchctl bootstrap gui/$UID " + path + ")"
	}
	return s, nil
}
