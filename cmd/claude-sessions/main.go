// Command claude-sessions archives Claude Code session transcripts to a synced
// folder and files them onto any machine by project identity rather than by path.
//
// Milestone 1 is read-only: ls and doctor. Nothing here writes to disk, by design -
// the format handling is proved against a real archive before any command can damage
// one.
package main

import (
	"fmt"
	"os"
)

var version = "0.1.0-dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "ls":
		err = cmdLs(os.Args[2:])
	case "doctor":
		err = cmdDoctor(os.Args[2:])
	case "version", "--version", "-V":
		fmt.Printf("claude-sessions %s\n", version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `claude-sessions - archive Claude Code sessions and file them by project identity

usage:
  claude-sessions <command> [flags]

commands:
  ls        list sessions on this machine
  doctor    check the archive, the hook, retention and manifest drift
  version   print the version

not yet implemented (see DESIGN.md for the porting order):
  push pull import restore render install uninstall backup

global flags:
  --claude-dir <path>   override $CLAUDE_CONFIG_DIR / ~/.claude
  --archive <path>      override the synced destination folder
  --json                machine-readable output
`)
}
