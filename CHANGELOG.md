# Changelog

All notable changes to this project are documented here. This project follows
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- **`install`** / **`uninstall`** - a SessionEnd hook plus a periodic sweep (Task
  Scheduler, launchd, or a systemd timer). The hook merge round-trips settings.json as
  a generic document, so every other setting and every other tool's hooks survive, and
  a timestamped backup is written first.
- **`import`** - the round trip is complete. Resolves each archived project to a
  local folder by identity (recorded path, then git remote, then a unique folder
  name), files its transcripts into the right bucket and rewrites the paths recorded
  inside them. Ambiguity is reported and skipped, never guessed, and the output names
  which rung matched.
- **`restore`** - the primitive import calls, for filing a folder by hand. Rewrites
  four path forms and re-validates every line as JSON before keeping the result.
- **`pull`** - blunt same-bucket-name copy for identical layouts.
- **`machines`** - list who has pushed to the archive, and `--forget` a retired
  machine. Shards are now keyed by hostname AND username, so two accounts on one
  machine no longer overwrite each other.
- **`push`** - the tool now actually archives. Copies new and changed transcripts and
  memory to the destination, writes this machine's manifest shard, regenerates
  `INDEX.md`, and takes a lock so a scheduled run and a session-end run cannot
  interleave. `--dry-run`, `--json` (result on stdout, progress on stderr) and
  `--quiet` (hook mode: logs, and never fails) included. Every run appends a line to
  `session-sync.log`.
- Memory-only buckets now get an identity from the `projects` map in `~/.claude.json`,
  closing the unroutable-bucket gap doctor has reported since the first review.
- `doctor --no-write-probe`, and `--json` now returns a summary object with counts and
  the version rather than a bare array.
- `search` — full-text search across every transcript, printing the `claude --resume`
  command for each hit. Archived history is only worth keeping if it can be found again.
- `stats` — session counts, sizes and date ranges per project.
- `config show` / `config set-destination` — persist the archive folder. Without it,
  `doctor` could report a missing destination with no command to fix it.
- `completion` — bash, zsh and fish completion scripts.
- `ls --since` / `ls --until` date filters.
- `doctor` now checks that the archive is **writable**, detects **two archivers
  installed at once**, reports `CLAUDE_CODE_PROJECT_DIR_NAME`, and **exits non-zero**
  when a check fails, so it can be used from monitoring.
- Tests for `internal/archive` (manifest merge, atomic writes, mtime tolerance) and
  `internal/identity` (remote parsing and normalisation).

### Security
- Git remotes are now read by **parsing `.git/config` directly instead of executing
  git**. Running `git` inside directories found by scanning the filesystem is arbitrary
  code execution, because a repository's own config can set `core.fsmonitor`,
  `core.pager` or an alias. This closes the hole before `import` ships.
- System binaries (`schtasks`, `systemctl`, `launchctl`, `crontab`, `loginctl`) are
  resolved by **absolute path**, not through `PATH`, which was a privilege-escalation
  path on Windows.
- The credentials check now **walks the whole archive** instead of only its root, where
  a recursive copy would never have placed the file.

### Fixed
- **`uninstall` deleted a scheduled task it did not create.** The Windows sweep reused
  the PowerShell predecessor's task name, so removing "ours" removed theirs - which
  happened on a live machine during testing. The task is now named `claude-sessions-sync`,
  and uninstall inspects what a task actually runs before deleting it.
- `import` restored transcripts but silently dropped every memory file, and reset
  every timestamp to the moment of import - which also made the next push re-upload
  the whole archive. Timestamps and permissions are now preserved through both the
  plain copy and the rewriting path, and memory travels with the sessions.
- `INDEX.md` was regenerated from the local machine only, so pushing from a second
  machine erased the first machine's listing - the same defect the sharded manifest
  exists to prevent. Index rows now live in `index/<machine>.json` and INDEX.md is
  built from all of them; keeping them out of the manifest also stops every import
  and doctor run parsing session rows it does not need.
- `doctor` reported two archivers installed when only the PowerShell one was, because
  "sync-claude-sessions.ps1" contains "claude-sessions".
- Nothing stopped the archive being set inside `~/.claude`, where each push would
  copy its own output without bound.
- `import` recorded a "different repository" note but never printed it, so those
  projects vanished silently from the text output.
- `push --json` wrote its progress line to stdout, making the output unparseable.
  Progress now goes to stderr.
- `WriteFileAtomic` deleted the destination and retried when rename failed, working
  around a Windows limitation that does not exist - turning a failed write into loss
  of the file already there.
- `<command> --help` exited 1 with "error: flag: help requested".
- `search` sorted only by time, so grouped output reprinted a session header each time
  the listing switched back to it; `search --json` returned null rather than [] when
  nothing matched. `search` also read every transcript twice.
- `-i` replaced by `--case-sensitive`, which can actually be toggled.
- The CI smoke test piped into `grep -q`, which exits at its first match; on Unix the
  resulting EPIPE becomes SIGPIPE and killed the writer with 141 under pipefail. It
  failed on macOS and Linux and could never reproduce on Windows, which has no
  SIGPIPE.
- A UTF-8 **BOM** made the first line of a transcript unparseable — and that is the line
  carrying `cwd`, so a single invisible marker cost the project's entire identity.
- Injected `<system-reminder>` and slash-command blocks are now **stripped** from a
  message rather than causing the whole message to be discarded, so a real question with
  a reminder appended still appears in listings.
- `ls --limit` now orders by modification time before reading transcripts, so it opens
  N files instead of all of them.
- Windows drive detection uses `GetLogicalDrives` instead of probing `A:` to `Z:`, which
  could block for seconds on a disconnected network drive.
- The systemd sweep check distinguishes **installed-but-stopped** from **not
  installed** — previously a broken sweep was reported as absent.
- `.gitignore` no longer excludes test fixtures: the negation was anchored to the
  repository root and silently dropped fixtures in nested packages.
- Minimum Go lowered from 1.27 to **1.21**, which is what the code actually requires.

## [0.1.0] - unreleased

Initial read-only milestone: `ls` and `doctor`, plus the transcript reader, bucket
handling and sharded manifest that everything else will build on.
