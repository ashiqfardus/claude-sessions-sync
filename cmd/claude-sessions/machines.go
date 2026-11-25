package main

import (
	"encoding/json"
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

	root, _, err := resolveRoot(*claudeDir)
	if err != nil {
		return err
	}
	dest, _, err := archive.ResolveDestination(root, *archiveDir)
	if err != nil {
		return err
	}
	if dest == "" {
		return fmt.Errorf("no archive destination: run `claude-sessions config set-destination <folder>`")
	}

	if *forget != "" {
		if err := archive.ForgetMachine(dest, *forget); err != nil {
			return err
		}
		fmt.Printf("Forgot %q: its manifest and index entries are removed.\n", *forget)
		fmt.Println("Its transcripts, and their pages, stay in the archive: they are the")
		fmt.Println("irreplaceable part. To stop publishing a project's pages, add it to")
		fmt.Println("render-exclude.txt (or `claude-sessions render --exclude <project>`).")
		fmt.Println("If that machine is still in use, its next push adds it back.")
		fmt.Println("Run `claude-sessions push` to rebuild INDEX.md without it.")
		return nil
	}

	machines, err := archive.Machines(dest)
	if err != nil {
		return err
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if machines == nil {
			machines = []archive.MachineInfo{}
		}
		return enc.Encode(machines)
	}

	if len(machines) == 0 {
		fmt.Println("No machines have pushed to this archive yet.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "MACHINE\tPROJECTS\tSESSIONS\tLAST BACKUP\tLAST SESSION")
	for _, m := range machines {
		last := "-" // a build older than the field
		if !m.LastPush.IsZero() {
			last = m.LastPush.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "%s\t%d\t%d\t%s\t%s\n", printable(m.Name), m.Projects, m.Sessions, last, localDate(m.LastSeen))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Println("\nLAST BACKUP is the hour of that machine's last full push (\"-\" for an older version);")
	fmt.Println("LAST SESSION is the newest session archived from it.")
	fmt.Println("\nRemove a machine you no longer use with --forget <name>.")
	fmt.Println("Its transcripts are kept; only its manifest and index entries go.")
	return nil
}

// localDate shows a manifest "seen" value - RFC 3339 UTC, or a bare date written by
// older versions - as a local date and time.
func localDate(seen string) string {
	if t, err := time.Parse(time.RFC3339, seen); err == nil {
		return t.Local().Format("2006-01-02 15:04")
	}
	return printable(seen)
}
