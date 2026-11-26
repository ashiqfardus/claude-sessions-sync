// Command claude-sessions archives Claude Code session transcripts to a synced
// folder and files them onto any machine by project identity rather than by path.
//
// The read-only commands come first by design: format handling is proved against a
// real archive before any command can damage one.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	"github.com/ashiqfardus/claude-sessions-sync/internal/work"
)

var version = "dev" // overwritten at release time by -X main.version

func init() {
	// `go install ...@v0.2.3` builds without the release ldflags, but does record the
	// module version, so `version` and doctor --json can still say which build it is.
	// Without its "v", as a release build prints it, so new shards agree across both.
	if info, ok := debug.ReadBuildInfo(); ok && version == "dev" &&
		info.Main.Version != "" && info.Main.Version != "(devel)" {
		version = strings.TrimPrefix(info.Main.Version, "v")
	}
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "push":
		err = cmdPush(os.Args[2:])
	case "pull":
		err = cmdPull(os.Args[2:])
	case "import":
		err = cmdImport(os.Args[2:])
	case "restore":
		err = cmdRestore(os.Args[2:])
	case "install":
		err = cmdInstall(os.Args[2:])
	case "uninstall":
		err = cmdUninstall(os.Args[2:])
	case "render":
		err = cmdRender(os.Args[2:])
	case "backup":
		err = cmdBackup(os.Args[2:])
	case "machines":
		err = cmdMachines(os.Args[2:])
	case "clean":
		err = cmdClean(os.Args[2:])
	case "prune":
		err = cmdPrune(os.Args[2:])
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
	case "ui":
		err = cmdUI(os.Args[2:])
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
		// `<command> --help` is a successful request, not a failure. flag has already
		// printed the usage text, so exit quietly and with 0 - otherwise asking for
		// help prints "error: flag: help requested" and returns 1.
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		// doctor has already printed its report; exit non-zero without adding noise
		// so the command is usable from a monitoring script.
		if errors.Is(err, errChecksFailed) {
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// printable drops control characters from text read out of the archive before it is
// printed: project names, prompts and machine names are written by other machines.
var printable = work.Printable

func usage() {
	fmt.Fprint(os.Stderr, `claude-sessions - archive Claude Code sessions and file them by project identity

usage:
  claude-sessions <command> [flags]

commands:
  push        copy new and changed sessions to the archive
  import      file an archive onto THIS machine, matching projects by identity
  pull        copy archived sessions into buckets of the same name
  restore     file one folder of transcripts, rewriting an old project path
  machines    list machines that have pushed here, or forget one
  clean       delete archived copies of a session that another copy holds in whole
  prune       delete chosen conversations from the archive, every copy (--undo takes it back)
  render      write mobile-readable HTML pages of every archived session
  install     archive automatically: a session-end hook, a per-reply hook and a sweep
  uninstall   remove the hook and the sweep (your archive is kept)
  backup      zip the whole profile: sessions, subagents, memory and settings
  ls          list sessions (--archived: the archive's)
  search      search the text of every session (--archived: the archive's)
  stats       session counts and sizes per project
  doctor      check the archive, hook, retention and manifest drift
  config      archive destination, project mappings, this machine's name
  ui          open the panel in your browser: everything here, without commands
  completion  print a shell completion script (bash|zsh|fish|powershell)
  version     print the version

common flags:
  --claude-dir <path>   override $CLAUDE_CONFIG_DIR / ~/.claude
  --archive <path>      override the synced destination folder
  --json                machine-readable output

examples:
  claude-sessions doctor
  claude-sessions config set-destination "G:\My Drive\claude-sessions"
  claude-sessions ls --project web-app --since 2026-08-01
  claude-sessions search "connection pool"
  claude-sessions push --dry-run
  claude-sessions import --search-root ~/dev --dry-run
  claude-sessions import --search-root ~/dev --update
`)
}
