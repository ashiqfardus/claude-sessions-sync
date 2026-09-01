# Changelog

All notable changes to this project are documented here. This project follows
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
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
