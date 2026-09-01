// Command claude-sessions archives Claude Code session transcripts to a synced
// folder and files them onto any machine by project identity rather than by path.
//
// The read-only commands come first by design: format handling is proved against a
// real archive before any command can damage one.
package main

import (
	"errors"
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
	case "search", "grep":
		err = cmdSearch(os.Args[2:])
	case "stats":
		err = cmdStats(os.Args[2:])
	case "doctor":
		err = cmdDoctor(os.Args[2:])
	case "config":
		err = cmdConfig(os.Args[2:])
	case "completion":
		err = cmdCompletion(os.Args[2:])
	case "version", "--version", "-v", "-V":
		fmt.Printf("claude-sessions %s\n", version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}

	if err != nil {
		// doctor has already printed its report; exit non-zero without adding noise
		// so the command is usable from a monitoring script.
		if errors.Is(err, errChecksFailed) {
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `claude-sessions - archive Claude Code sessions and file them by project identity

usage:
  claude-sessions <command> [flags]

commands:
  ls          list sessions on this machine
  search      search the text of every session
  stats       session counts and sizes per project
  doctor      check the archive, hook, retention and manifest drift
  config      show or set the archive destination
  completion  print a shell completion script (bash|zsh|fish)
  version     print the version

not yet implemented (see DESIGN.md for the build order):
  push pull import restore render install uninstall backup

common flags:
  --claude-dir <path>   override $CLAUDE_CONFIG_DIR / ~/.claude
  --archive <path>      override the synced destination folder
  --json                machine-readable output

examples:
  claude-sessions doctor
  claude-sessions config set-destination "G:\My Drive\claude-sessions"
  claude-sessions ls --project airos --since 2026-08-01
  claude-sessions search "connection pool"
`)
}
