# Changelog

All notable changes to this project are documented here. This project follows
[Semantic Versioning](https://semver.org/).

## [Unreleased]

Upgrading: run `claude-sessions install` again after updating the binary. The hook
and the sweep run the copy in `~/.claude/bin`, so a new build does nothing until it
is installed - and on Windows it is what replaces the sweep that flashed a terminal.
Do it on every machine: mixed versions file forks as ordinary sessions. To go back
to an older version, run `uninstall` with this one first - an older `uninstall`
leaves the per-reply Stop hook in place. The first `import --update` after upgrading
has no record of what each machine already took, so a memory entry or file deleted
before the upgrade can come back once. Home folders match only once both machines
have pushed with this version.

### Added (2026-11-20)
- **A session continued on two machines is kept twice instead of half-lost.** Copies
  are compared by entry id rather than by date and size. `import --update` files the
  other machine's copy as a session of its own (a new, stable id), so either can be
  resumed; importing again updates it instead of adding a third, and a machine never
  gets its own copy back. On a shared layout `push` keeps the archived copy and saves
  this machine's beside it under `forks/`, where it used to overwrite one or silently
  drop the other.
- **Per-reply backup.** `install` adds a `Stop` hook running `push --quiet --from-hook`:
  the session that just replied is archived at once, so a machine that dies mid-session
  loses minutes rather than up to 30. Transcripts over 5 MB are copied at most every 5
  minutes, since a sync client re-uploads the whole file. `doctor` says when an older
  install lacks it.
- **`config map <bucket> <folder>`** says where an archived project lives on this
  machine, for projects nothing else identifies (no git remote, a renamed folder, two
  candidates). Saved per machine, used by every import; `config unmap` removes one.
  `import` prints the exact command for each project it could not place.
- **The home folder matches across machines** (#4): `C:\Users\you` and `/Users/you`
  are filed as each other, with their memory, when that machine's profile was
  `<home>/.claude`.
- **Each machine's rules and skills are kept separately** (#5) under
  `profile/machines/<machine>/`, so a newer copy pushed from elsewhere no longer drops
  this machine's from the backup. `import --update` reports a rule file it replaced
  and saves the local version under `profile/replaced/<machine>/<time>/`, which no push
  writes; `doctor` lists files that differ between machines.
- **Mixed versions are reported** (#8): each machine records its version in its
  manifest, and `doctor` names machines running a different one.
- `doctor` explains a macOS privacy refusal of the background sweep and which setting
  to grant; the launchd agent now keeps its stderr in `session-sync.err.log` (#6).

### Fixed (2026-11-20)
- **Google Drive and OneDrive were not found on current macOS** (#7). They mount under
  `~/Library/CloudStorage`; detection now looks there, and uses a folder only when
  exactly one account matches.
- **`import --update` reported a session as continued on two machines when it had only
  continued here** - the archived copy being older is not a fork.
- **`import --update` dropped the other machine's memory index.** `MEMORY.md` was
  replaced newest-wins, so one machine's entries vanished here and, after the next
  push, from the archive. It is now merged entry by entry (the newer wording wins for an
  entry both have; a future-dated copy is still refused), and any other memory file it
  replaces is kept in `memory/.replaced/<time>/`.

### Fixed in review (2026-11-20)
- A fork kept by one push was overwritten by the next, which saw nothing changed here
  and fell back to newest-and-largest wins.
- A fork copy continued on the other machine never came back: the check for a
  machine's own copy returning skipped it unread. It is now skipped only when it adds
  nothing.
- A fork copy continued here while the other machine continued the original dropped
  the other side and reported it filed. The other side is now filed as a further fork.
- On a shared layout a fork was tagged with the importing machine. The writer of each
  archived session is now taken from the machines' index files, and never this one.
- `pull --update` said it filed the other copy of a forked session and did not. `pull`
  now files through the same code as `import`, `forks/` included.
- `config map` and the home-folder rule were ignored when repointing one project's
  paths inside another project's sessions.
- The doctor check for a macOS privacy refusal could not fire: the refusal's reason was
  not logged, and any other successful push, holding a terminal's grant, cleared it.
  The reason is logged, the sweep now runs `push --sweep` and marks its log line, and
  only the sweep's own success clears the check.
- The per-reply hook walked the archive for temporary files after every reply, did not
  throttle a large forked session's fork copy, and logged a routine skip on every reply
  while the drive was offline.
- macOS detection now prefers an existing `~/Google Drive` archive over a CloudStorage
  one, so a machine that already used it does not switch.
- Entries from the PowerShell tool's `projects.json` keep the machine they name, so
  their forks are recognised when that machine returns - including after pushes
  rewrite the file.

### Fixed in second review (2026-11-20)
- On a shared layout, a session archived by the per-reply hook - which leaves the
  index behind - was forked under the wrong machine: filed twice, and its writer got
  its own conversation back. When no index row matches exactly, the one other machine
  that recorded the session is taken as its writer.
- A deleted `MEMORY.md` entry or memory file came back on every round: the merge was
  a plain union. It is now three-way, against what was last taken from each machine
  (`~/.claude/session-sync-base/`), and a machine's own archived memory is no longer merged back.
- A forked session was re-read in full on every push, and logged as forked after every
  reply. A fork copy that is current is now left alone, and `FORK` is logged once.
- A `config map` entry could lose, at random, to a related repository with the same old
  path. Projects are now repointed first, in a fixed order.
- The per-reply hook gives up after 5 seconds rather than holding up a reply when the
  archive's filesystem hangs.
- `import` reported a fork again on every run, and did not count it as filed.
- A future-dated archived file was taken when there was no local copy yet.
- The home folder is now what each machine records at push, not guessed from a profile
  path ending in `.claude`.
- A session renamed on another machine, with no new turn, kept its old title here:
  entries after the last turn (title, last prompt, cost) now count.
- `config set-machine <name>` pins a machine's name, which a macOS hostname does not.
  It is saved in `session-sync.json`, so the scheduled sweep - which sees no shell
  environment - uses it too.

### Fixed in third review (2026-11-20)
- A fork filed before its writer was known was filed a second time once that machine's
  index caught up, and on a shared layout the writer got its own conversation back.
  The copy filed without a machine is now kept current, and counted as possibly this
  machine's own.
- `config map -Users-...` failed with "flag provided but not defined": every macOS and
  Linux project name starts with "-", and the command import printed for it did not
  work as printed. Names after `map`, `unmap` and `set-machine` are never flags.
- On a shared layout, memory merges read this machine's own earlier push as the other
  machine's, dropping entries and sometimes skipping another machine's memory
  entirely. Each machine now also keeps its own copy in `memory/<machine>/<project>/`,
  and each other machine's is merged on its own.
- A fork the user deleted was filed again on the next import. It now comes back only
  once the other machine adds to it.
- doctor's macOS privacy check could never clear with `--no-sweep` or an older sweep,
  and blamed the sweep for any refusal. Without a marking sweep any successful push
  clears it, and the hint names the run that was refused.
- A per-reply push that gave up was not logged (now at most once an hour), and
  half-written files under `forks/`, `profile/` and `memory/` were never cleaned up.
- With archives in two accounts, macOS detection still fell back to the Google one.

### Added (2026-11-17)
- **Global rules, skills and plugins travel with the sessions.** `push` archives
  `CLAUDE.md` and the other top-level `.md` files beside it, `skills/`, `agents/`,
  `commands/` and `output-styles/` under `profile/`, plus each machine's enabled plugins
  and their marketplaces. `import` and `pull` restore them - added if missing, replaced
  only with `--update` and a newer copy - and install each missing plugin with
  `claude plugin install` (`--no-plugins` to skip). claude.ai account skills
  (`skills/synced/`), dot-files, `node_modules` and `settings.json` stay behind.
- `doctor` reports whether the profile is archived.

### Fixed (2026-11-17)
- **Saved tool output pointed at the other machine after an import.** A large tool
  result is saved under `~/.claude/projects/...` and the transcript names it by absolute
  path; import rewrote the project path but not the `~/.claude` in front of it, so on a
  machine with a different home the path led nowhere. Each machine now records its
  `~/.claude` in its manifest, and import repoints it.

### Fixed (review, 2026-09-30)
- **The Windows sweep flashed a terminal window every run.** `install` now also writes
  `claude-sessionsw.exe`, a copy marked as a GUI program, and the task runs that, so no
  console opens and the exit code still reaches the task's last result. `doctor` flags
  a task still running the console build.
- **An import could replace another machine's archived transcript, and that machine's
  new turns were then never archived.** A rewritten transcript kept its source's
  timestamp but usually grew, so the importing machine's next push counted it as newer
  and uploaded it over the original; the original machine's own copy was from then on
  refused as a truncation. Rewritten copies are now dated just before their source.
- **The hook skipped a session whenever another push held the lock** - the sweep, or
  a second session ending - and it then waited for the next sweep, or overnight. An
  unattended push now waits up to 45 seconds (`--lock-wait`).
- **A render killed part-way kept none of its work.** The first render of a large
  archive, or every page after a template change, outlasts the hook's 120 seconds, so
  every session end started again and none finished. Progress is saved every 10
  seconds, and an unattended push renders for at most 45 seconds, leaving the rest for
  the next run.
- **`render --exclude` and `machines --forget` were undone by the next push.** An
  exclusion given to `render` is now saved, like `renderExclude`; `forget` no longer
  deletes pages that the next push rendered straight back.
- **A sync client's conflict copy replaced a machine's manifest or index**
  (`LAP-1 (1).json`, `LAP-1.sync-conflict-....json`); in one test `INDEX.md` fell from
  3,000 sessions to 5. Only `<machine>.json` is read; `doctor` lists the others.
- **Every push rewrote every index page, `INDEX.md`, `projects.json` and the render
  state**, changed or not. Files whose content is unchanged are no longer written, and
  the indexes are dated by their newest session rather than by "now".
- A push hung on a dead network mount stopped the macOS and Linux sweep until reboot.
  The systemd unit now has a one-hour limit, as Task Scheduler does, and an unattended
  push ends itself after 55 minutes.
- `install --archive` saved a relative path as typed (the hook runs in each project's
  folder, so every session was copied into every repository) and a missing drive
  still ended with "Done". Destinations are made absolute and checked like
  `config set-destination`; a relative one saved earlier is reported. `--claude-dir`
  is made absolute too.
- Flags after a positional argument were ignored: `search "pool" --archived` searched
  for "pool --archived". Flags are accepted anywhere, and a command that takes no
  arguments rejects one instead of ignoring it.
- A leading `~` is expanded in paths, as a quoted `"~/dev"` and anything under
  PowerShell reach the program unexpanded.
- `doctor`'s credential scan stopped three levels down, skipping `memory/` and the
  session folders where subagent transcripts live.
- A profile other than the default read the default profile's `~/.claude.json`.
- `push --quiet` exited without a log line on an unknown flag, so a downgrade archived
  nothing, silently.
- `version` printed `dev` for a `go install` build.
- An upgrade that fails halfway puts the old binary back instead of leaving none.
- `CLAUDE_CONFIG_DIR` spelled as an 8.3 short path or a symlink to `~/.claude` no
  longer registers a second sweep for the default profile.
- A sweep run killed by the one-hour limit is reported as a problem, not as healthy.
- `uninstall` says when no sweep was registered for the profile instead of "removed".
- A quoted task argument ending in `\` no longer swallows the arguments after it.
- On Windows, the hook gave up waiting for the lock at the moment it was being
  released, which Windows reports as "access denied".

### Added (review, 2026-09-30)
- **`--update`** on `import`, `pull` and `restore`: brings in sessions that continued
  on another machine - only when the archived copy strictly extends the local one, so a
  session that also continued here is kept and reported - and memory files that are
  newer in the archive.
- **`import` without a manifest**: a bucket no manifest names - a forgotten machine's,
  a PowerShell-era archive's, an unzipped `backup` - is placed by the working directory
  its transcripts record. `backup` now prints that one command as the way back.
- **`doctor` checks that archiving actually happens**: it fails when a session that
  ended over 40 minutes ago is not in the archive, and warns when the last successful
  push is hours old. The hook and the sweep exit 0 by design, so a scheduler's last
  result never showed this. It also fails a hook whose program is missing, and a cron
  entry with no cron daemon running (WSL).
- PowerShell completion: `claude-sessions completion powershell`.
- `import` warns about a `--search-root` that does not exist.
- `session-sync.log` is rotated at 1 MB, and `install` keeps the three newest
  `settings.json` backups instead of one per run.
- `config --help` lists its actions.

### Changed (review, 2026-09-30)
- `ls` shows whole session IDs, which `claude --resume` needs; the search hint includes
  the `cd`, and for `--archived` results says to import first.
- `machines` labels its date column LAST SESSION, which is what it is.
- Pages are rendered once more (template 3).

### Security (review, 2026-09-30)
- **Network and device paths read from the archive or from a transcript are never
  opened.** On Windows, even checking whether `\\host\share` exists sends the user's
  NTLM hash to that host, and a path planted in the shared archive made every push do
  so.
- **Symlinks in the archive are never followed** by `import`, `pull`, `restore` or
  `render`: one in place of a memory folder copied local files into memory and out on
  the next push, and one in place of a page folder let `render` delete files elsewhere.
- **Memory is not replaced from the archive** unless `--update` is given, and never by
  a file dated in the future. Memory is loaded into every session as instructions.
- Path moves read from a manifest must be absolute paths: `"e"` rewrote every `e` in
  every imported transcript, and the result still parsed.
- Pages carry a Content-Security-Policy that forbids scripts, and links are relative
  and escaped, so a folder named `javascript:...` is inert.
- Text from the archive is stripped of control characters before it reaches the
  terminal, and of Markdown syntax before it reaches `INDEX.md`.
- More credentials are masked on pages: PGP and truncated PEM keys, JSON-escaped and
  quoted values with spaces, `DB_PASS=`/`*_KEY=`/`Cookie:`, `hf_`, `npm_`, `ya29.`,
  `SG.` tokens, Azure `AccountKey=`, and URL passwords containing `/`.
- A scheduled task is recognised as this tool's only by the program it runs, not by
  its command line mentioning the name: uninstall could delete a foreign task.
- Temporary-file cleanup stays inside the folders this tool writes.
- GitHub Actions are pinned to commit SHAs, with Dependabot to keep them current, and
  GoReleaser to an exact version.

### Fixed (audit, 2026-09-16)
- **The scheduled sweep could be deleted by a test install.** The job name was
  machine-wide, so `install --claude-dir <scratch>` then `uninstall` replaced and removed
  the real profile's sweep. Jobs are now per profile; the default profile keeps its name.
- **Subagent transcripts and saved tool output were never archived** (`<session>/subagents`,
  `<session>/tool-results`). They are now pushed, pulled, imported and restored.
- **A smaller or older file could overwrite a newer one.** Any size difference forced a
  copy, so a stale copy on a second machine replaced the archived conversation, and
  restore replaced memory edited locally. The newer file now wins, and a transcript is
  never replaced by a shorter one.
- **A killed run blocked archiving for 20 minutes** and left half-written temp files in
  the synced folder. The lock is broken as soon as its process is dead, refreshed while
  held, released only by its owner; leftover `.tmp-*` files are cleaned up.
- **Bucket names used a partial rule.** Now Claude Code's own: every non-alphanumeric
  character becomes `-`, long names get a hash suffix. Paths with `.`, `_` or
  non-ASCII characters were filed where `/resume` never looks.
- **Path rewriting matched prefixes** (`/proj` rewrote `/proj2`), aborted a transcript on
  a pre-broken line or BOM, and abandoned the rest of the project after one failure.
- **Multi-folder workspaces**: paths into sibling repositories a session worked in are
  now repointed on import, matched by git remote; Git Bash `/e/...` paths are handled.
- Manifest conflicts were decided by random map order and "today", and `projects.json`
  fed its own entries back forever; a malformed `projects.json` failed every push.
- `INDEX.md` and the manifest dropped sessions that had aged out locally.
- Hooks and crontab lines containing "claude-sessions" anywhere were treated as ours and
  removed, whole groups at a time; `settings.json` was re-sorted alphabetically.
- `doctor` passed a disabled, failing or foreign sweep; the Windows task did not run on
  battery; Linux paths with spaces broke systemd and cron.
- Search interleaved sessions and cut off the newest; prompts typed while Claude was busy
  were missing; pages did not refresh on growth inside the timestamp tolerance; index
  prompts split multi-byte characters; page times were UTC.
- `push --quiet` exited 1 on an unknown flag; `install` failed while the installed binary
  was running on Windows.

### Added (audit, 2026-09-16)
- **`backup`**: a zip of the whole profile with a manifest, never credentials, and a loud
  warning when it holds no transcripts.
- `ls`, `search` and `stats` take `--archived`; `search --all` includes tool input,
  output and thinking.
- Per-project index pages instead of a 300-session cap.
- Shell completion now covers every command, read from the binary itself.

### Security (audit, 2026-09-16)
- Rendered pages and index prompts mask common credentials.
- Render exclusions are shared through `render-exclude.txt`, so another machine can no
  longer re-publish an excluded project, and `INDEX.md` withholds its prompts.
- Bucket, session and machine names read from the shared archive are validated before
  being joined into paths that are written or deleted.

## [0.2.2] - 2026-09-01

The first release with binaries: v0.2.0 and v0.2.1 were withdrawn because their
release builds failed. Everything since the first commit is listed here.

### Added
- `render --exclude` and a `renderExclude` config key: archiving a project and
  publishing it as a readable web page are different decisions.
- `doctor` reports whether the HTML is current.
- **`render`** - self-contained mobile HTML for every archived session, plus an index,
  run automatically at the end of each push. Tool calls, results and thinking collapse
  behind disclosure triangles; oversized blocks are truncated with a pointer to the
  `.jsonl`; content is escaped before any formatting. No JavaScript and no external
  stylesheet, because these are opened from a cloud folder on a phone.
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
- `doctor` reported a project as missing its rendered page when it had been
  deliberately excluded, and advised running `render`, which could never fix it. It
  now asks the renderer the same question rather than keeping its own copy of the
  rule, and its page and transcript counts agree with each other. `doctor --exclude`
  lets you check the effect of an exclusion without editing your config first.
- **`render --exclude` did not remove pages it had already published.** The flag
  existed to stop a project being readable, reported success, and left every page
  exactly where it was - a privacy control that only appeared to work. Excluding now
  withdraws the pages and the index entry; the transcripts are untouched.
- `machines --forget` re-read every manifest and index file once per page to decide
  what to delete. Retiring a machine with 500 sessions meant 500 full manifest reads
  across a cloud filesystem.
- `doctor` told you to run `render` when nothing was rendered because everything was
  deliberately excluded, and never showed which patterns were in force - so one that
  matched nothing published what you believed was withheld.
- **Rendered pages published every injected `<system-reminder>`** into the archive.
  `contentText` had always stripped them; `contentBlocks`, added for rendering, did
  not - so the plumbing Claude Code splices into a message was written out as readable
  HTML in a cloud folder. Found by a realistic test fixture, not by review.
- Thinking blocks that Claude Code records with a signature but no text were dropped
  silently; a page now says how many were not recorded rather than showing nothing.
- `html/index.html` listed every session ever, the same defect already fixed in
  `INDEX.md`. Capped, with a note about what is not shown, and pages themselves are
  capped so one enormous session cannot produce a file no phone will open.
- A page whose transcript was replaced with OLDER content - which is what `import`
  does when it preserves archived timestamps - was treated as current forever.
- `machines --forget` left the retired machine's pages behind, browsable but absent
  from every listing.
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

## [0.1.0] - 2026-09-01

Initial read-only milestone: `ls` and `doctor`, plus the transcript reader, bucket
handling and sharded manifest that everything else will build on.
