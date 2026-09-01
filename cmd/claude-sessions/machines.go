package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

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
	if err := fs.Parse(args); err != nil {
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
		pages, err := archive.ForgetMachine(dest, *forget)
		if err != nil {
			return err
		}
		fmt.Printf("Forgot %q: manifest, index and %d rendered page(s) removed.\n", *forget, pages)
		fmt.Println("Its transcripts are still in the archive under projects/ - they are the")
		fmt.Println("irreplaceable part and are never deleted here.")
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
	fmt.Fprintln(w, "MACHINE\tPROJECTS\tSESSIONS\tLAST SEEN")
	for _, m := range machines {
		fmt.Fprintf(w, "%s\t%d\t%d\t%s\n", m.Name, m.Projects, m.Sessions, m.LastSeen)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Println("\nRemove a machine you no longer use with --forget <name>.")
	fmt.Println("Its transcripts are kept; only its manifest and index entries go.")
	return nil
}
