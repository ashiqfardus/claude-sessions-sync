package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
)

// cmdMachines lists who has contributed to the archive, and can forget one.
//
// A machine that is renamed, retired or reinstalled leaves its shard behind, and its
// sessions stay in INDEX.md with no way to remove them. Nothing can infer that a
// machine is gone, so this reports what is there and lets a human decide.
func cmdMachines(args []string) error {
	fs := flag.NewFlagSet("machines", flag.ContinueOnError)
	claudeDir := fs.String("claude-dir", "", "override $CLAUDE_CONFIG_DIR / ~/.claude")
	archiveDir := fs.String("archive", "", "override the synced destination folder")
	forget := fs.String("forget", "", "remove this machine's manifest and index (its transcripts are kept)")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := parseFlags(fs, args, false); err != nil {
		return err
	}
	o := machinesOpts{ClaudeDir: *claudeDir, ArchiveDir: *archiveDir, Forget: *forget}
	s := stdStreams()
	if *asJSON {
		s = jsonStreams()
	}
	var res any
	var err error
	if o.Forget != "" {
		res, err = runForget(s, o)
	} else {
		res, err = runMachines(s, o)
	}
	if err != nil || !*asJSON {
		return err
	}
	return encodeJSON(os.Stdout, res)
}

type machinesOpts struct{ ClaudeDir, ArchiveDir, Forget string }

type forgetResult struct {
	Forgot string `json:"forgot"`
}

// machinesArchive is the archive folder machines and --forget work on; there must be one.
func machinesArchive(o machinesOpts) (string, error) {
	root, _, err := resolveRoot(o.ClaudeDir)
	if err != nil {
		return "", err
	}
	dest, _, err := archive.ResolveDestination(root, o.ArchiveDir)
	if err != nil {
		return "", err
	}
	if dest == "" {
		return "", fmt.Errorf("no archive destination: run `claude-sessions config set-destination <folder>`")
	}
	return dest, nil
}

// runForget removes o.Forget's manifest and index entries, and says what that leaves.
func runForget(s streams, o machinesOpts) (forgetResult, error) {
	dest, err := machinesArchive(o)
	if err != nil {
		return forgetResult{}, err
	}
	if err := archive.ForgetMachine(dest, o.Forget); err != nil {
		return forgetResult{}, err
	}
	fmt.Fprintf(s.Out, "Forgot %q: its manifest and index entries are removed.\n", o.Forget)
	fmt.Fprintln(s.Out, "Its transcripts, and their pages, stay in the archive: they are the")
	fmt.Fprintln(s.Out, "irreplaceable part. To stop publishing a project's pages, add it to")
	fmt.Fprintln(s.Out, "render-exclude.txt (or `claude-sessions render --exclude <project>`).")
	fmt.Fprintln(s.Out, "If that machine is still in use, its next push adds it back.")
	fmt.Fprintln(s.Out, "Run `claude-sessions push` to rebuild INDEX.md without it.")
	return forgetResult{Forgot: o.Forget}, nil
}

// runMachines lists the archive's machines on s.Out and returns them.
func runMachines(s streams, o machinesOpts) ([]archive.MachineInfo, error) {
	dest, err := machinesArchive(o)
	if err != nil {
		return nil, err
	}
	machines, err := archive.Machines(dest)
	if err != nil {
		return nil, err
	}
	if machines == nil {
		machines = []archive.MachineInfo{}
	}

	if len(machines) == 0 {
		fmt.Fprintln(s.Out, "No machines have pushed to this archive yet.")
		return machines, nil
	}

	w := tabwriter.NewWriter(s.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "MACHINE\tPROJECTS\tSESSIONS\tLAST BACKUP\tLAST SESSION")
	for _, m := range machines {
		last := "-" // a build older than the field
		if !m.LastPush.IsZero() {
			last = m.LastPush.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "%s\t%d\t%d\t%s\t%s\n", printable(m.Name), m.Projects, m.Sessions, last, localDate(m.LastSeen))
	}
	if err := w.Flush(); err != nil {
		return machines, err
	}
	fmt.Fprintln(s.Out, "\nLAST BACKUP is the hour of that machine's last full push (\"-\" for an older version);")
	fmt.Fprintln(s.Out, "LAST SESSION is the newest session archived from it.")
	fmt.Fprintln(s.Out, "\nRemove a machine you no longer use with --forget <name>.")
	fmt.Fprintln(s.Out, "Its transcripts are kept; only its manifest and index entries go.")
	return machines, nil
}

// localDate shows a manifest "seen" value - RFC 3339 UTC, or a bare date written by
// older versions - as a local date and time.
func localDate(seen string) string {
	if t, err := time.Parse(time.RFC3339, seen); err == nil {
		return t.Local().Format("2006-01-02 15:04")
	}
	return printable(seen)
}
