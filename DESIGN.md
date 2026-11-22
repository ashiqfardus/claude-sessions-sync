# claude-sessions — design

**Status:** implemented. Written 2026-09-01, before any code, as the design for the
port. The layouts (§3, §5), the manifest's merge rule and schema (§4) and the per-OS
install (§7) have since been corrected to match the code; the rest is the design as
agreed. Where it disagrees with the code or the README, they win.
**Supersedes:** the six PowerShell scripts in `%USERPROFILE%\.claude\tools\`, which are
live and working on this machine and serve as the reference implementation.

---

## 1. What this is

Claude Code stores every conversation as a JSONL transcript under
`~/.claude/projects/<bucket>/<session-id>.jsonl`, where `<bucket>` is the project's
absolute path flattened into a single name. Those transcripts are local-only, are
deleted after `cleanupPeriodDays` (default **30**), and are keyed to a path — so they
do not survive a new laptop, and they do not follow a project that lives at a
different path on a second machine.

This tool archives them to any synced folder and files them correctly on any machine,
**matching projects by identity rather than by path**.

It is a port of a working Windows setup to a single cross-platform Go binary, for
open-source release.

### Why Go

One binary per OS, no runtime to install. The current implementation needs Windows
PowerShell 5.1; a Python layer was already rejected on this machine because
`python`/`python3` here are 0-byte Microsoft Store stubs. Same choice as
[claude-sync](https://github.com/tawanorg/claude-sync) (267★), the most-adopted
comparable tool.

### The differentiator — lead with this

Nothing in the survey does **identity-based project matching**. `claude-sync` makes
the user hand-write a `path_map`. This derives the mapping automatically:

```
1. the project's recorded absolute path, if it exists on this machine
2. git remote match among candidate folders   (normalised: git@github.com:o/r.git == https://github.com/o/r)
3. a unique folder-name match
4. ambiguous -> report and skip, never guess
```

The enabling trick: **a bucket name cannot be decoded back into a path.** The
flattening is lossy — a `-` in the bucket may have been a separator, a space, or a
literal hyphen. So the real path is read from the `cwd` field *inside* the transcript
and cached in a manifest.

### Non-goals

- **No cloud provider APIs.** The tool writes to a plain folder, so Google Drive,
  OneDrive, Dropbox, iCloud, Syncthing, and NAS mounts all work today. The swappable
  folder *is* the design; say so explicitly in the README.
- **No live mirror.** Never symlink `~/.claude/projects` into the synced folder. The
  archive receives only completed files, so a sync client cannot corrupt an
  in-progress transcript and two machines cannot produce conflicted copies of a live
  session.
- **No guarantee about the transcript format.** It is internal to Claude Code and
  changes between releases (the docs say so and advise `/export` or the hook's
  `transcript_path` instead of parsing). Parse defensively; never treat a shape we
  don't recognise as fatal.
- **No secrets.** `.credentials.json` (DPAPI-bound, useless elsewhere) and
  `.claude.json` (rewritten by any live session on exit) are never synced or backed up.

---

## 2. Command surface

Single binary, subcommands. Common flags: `--archive <path>`, `--claude-dir <path>`,
`--json`; `--quiet` and `--dry-run` where they apply. Each command parses its own, and
accepts them before or after its other arguments.

| Command | Replaces | Purpose |
|---|---|---|
| `push` | `sync-claude-sessions.ps1` (default) | Copy changed transcripts + memory to the archive; update the manifest and `INDEX.md`; render HTML. |
| `pull` | `sync-claude-sessions.ps1 -Pull` | Copy archived transcripts into local buckets by exact bucket name, skipping any that exist. The dumb, same-layout case. |
| `import` | `import-sessions.ps1` | The smart case: resolve each archived project to a local folder by identity, then file and rewrite. |
| `restore` | `restore-claude-history.ps1` | Place a folder of `.jsonl` into the right bucket, rewriting an old project path to a new one. The primitive `import` calls. |
| `render` | `render-sessions-html.ps1` | Transcripts → mobile-readable HTML + `index.html`. |
| `install` / `uninstall` | `install-claude-sync.ps1` | Per-OS: merge the SessionEnd hook, register the periodic sweep, persist config, first push. |
| `backup` | `backup-claude.ps1` | Whole-profile snapshot to a zip, with a manifest and a loud warning at zero transcripts. |
| `doctor` | *(new)* | Diagnose: archive reachable and writable, hook installed and its program present, sweep registered and last result, `cleanupPeriodDays`, bucket/manifest drift, sessions that ended but never reached the archive, the last successful push, credentials in the archive. |
| `ls` | *(new, cheap)* | List sessions with project, date, size, first prompt — the `INDEX.md` content on the terminal. |

Notes on the surface:

- The hook and the sweep run `push --quiet` explicitly. With no subcommand the binary
  prints its usage and exits 2.
- `--dry-run` is honoured everywhere that writes, replacing PowerShell's `-WhatIf`.
- `--json` on `doctor`, `ls`, and `import` so the tool is scriptable — this is the main
  thing the PowerShell version cannot do well.
- `restore` keeps `--include-bare-name` (off by default: it also hits package names,
  git remotes, and prose) and keeps the two-phase report — run once without it and it
  tells you how many lines still mention the bare folder name.

---

## 3. Layout on disk

### Archive (the synced folder)

```
<archive>/
  projects/<bucket>/<session-id>.jsonl
  projects/<bucket>/<session-id>/subagents/...     subagent transcripts
  projects/<bucket>/<session-id>/tool-results/...  oversized tool output
  projects/<bucket>/memory/*.md
  work/<machine>/<encoded path>/...  <- work folders: text-like files outside git
  manifest/<machine>.json        <- CHANGED: was a single projects.json
  index/<machine>.json           <- each machine's INDEX.md rows
  projects.json                  <- kept, generated, read-only compatibility view
  render-exclude.txt             <- projects no machine publishes as pages
  html/index.html
  html/<bucket>/index.html
  html/<bucket>/<session-id>.html
  html/.render-state.json        <- what each page was rendered from
  INDEX.md
```

`<machine>` is the host name and user name, so two accounts on one computer each own a
file.

`work/<machine>/` holds the folders Claude worked in that no git repository or
`~/.claude` covers. The folder's absolute path is encoded as `/`-separated segments
with a Windows drive's colon dropped (`E:\SQL` -> `E/SQL`), so the archive reads the
same on every OS. Only text-like files go in: never media, archives, binaries,
databases, `.pth`, secret-shaped names, files over 10 MB or any dot file or folder;
code folders (`node_modules`, `venv`, `vendor`, `__pycache__`, `site-packages`,
`dist-packages`) are never work folders. Each first-level subfolder, and the folder's
own files as one more, is capped at 250 MB after filtering; one over it is not copied
and is named in `skipped`, and the rest of the folder is. Each machine writes only its
own `work/<machine>/`. The archive is untrusted when restoring: see the deny-list and
its residual risk in the README's Privacy section.

A unit - a first-level subfolder of a work folder that is not an exact file - whose
files have all been unchanged for 7 days, with 5 to 20,000 of them, is stored as
`<unit>.cspack.zip` beside its loose folder: a zip whose `/`-separated entry names are
relative to the unit, each file's time in its header. Push builds it from local files,
then deletes only the loose copies it holds. Later changes go loose beside it (the
overlay) until they pass 50 files or a quarter of the pack's bytes and every local one
is 7 days old (a burst still changing would be packed again at every push); then push
compacts them into a new pack, through a temp file and a rename. `work-packs.json` in
`session-sync-base/` records each pack's entries and overlay, so push need not read the
pack from the mount. Restore treats a pack as untrusted as a loose file. Before zip
parses it, the whole pack is refused when the file is over 266 MB (250 MB of content
plus 16 MB for headers), or when its layout is not the one push writes: the end record
closes the file with no comment, the central directory runs from its offset straight
to that record (no prepended data, no zip64), and a walk of the directory's headers
tiles it with at most 20,000 entries. zip reads headers until one fails and checks the
end record's count only modulo 65,536, so without the walk a small file could hold
millions of entries. Push in turn never commits a pack over 266 MB: the files stay
loose. Restore then refuses the whole pack over 250 MB declared, refuses alone each
entry that is not a plain relative path, not a regular file, over 10 MB declared or
read, or failing its CRC, extracts the rest to a private temp folder, and files each
through the same checks as a loose file. For one path, the newer of a pack entry and an overlay file is filed,
the overlay on a tie (within 2 s).

### Local

- Claude root: `$CLAUDE_CONFIG_DIR` if set, else `~/.claude`. **Always resolve through
  this** — the current scripts hardcode `%USERPROFILE%\.claude` and would silently
  operate on the wrong profile.
- Config: `<claude-root>/session-sync.json` (keep the existing filename and the
  `destination` key, so an existing install keeps working).
- Log: `<claude-root>/session-sync.log`, a few lines per run, rotated to
  `session-sync.log.1` at 1 MB.
- Work folder state, in `<claude-root>/session-sync-base/`: `work-folders.json` is the
  list of this machine's work folders and is the only thing push trusts for it (the
  shard merely publishes it); `work-held.json` records each restored file, with earlier
  placements under `prev`, so `--update` can tell a file it wrote from one you changed;
  `repointed.json` records what `import --repoint` rewrote. Repoint backups go to
  `<claude-root>/repoint-backup/<yyyymmdd-hhmm>[-n]/`. Config adds `workBackup`,
  `workPlacement`, `workMap` and `workExclude`.
- Tools/binary: `<claude-root>/bin/claude-sessions` (+`.exe`), and on Windows
  `claude-sessionsw.exe` beside it for the sweep (see §7). The hook and the sweep
  point at this **local** copy on purpose — pointing them into the synced folder would
  make the end of every session depend on the drive being mounted.

### Destination resolution order (unchanged)

1. `--archive` flag
2. `destination` in `session-sync.json`
3. auto-detect `<drive>:\My Drive\claude-sessions` (Windows), `~/Google Drive/…`,
   `~/OneDrive/…`, `~/Dropbox/…`, `~/Library/Mobile Documents/…` (macOS iCloud)

---

## 4. The manifest — sharded, not merged

**This is a change from the reference implementation, and it fixes a real bug.**

Today `projects.json` is one file that every machine read-merges-and-rewrites. Two
machines pushing inside the same sync window both merge onto the version they read;
the second write wins and silently drops the first machine's entries. Nothing detects
it. Drive's own conflict handling does not help, because both writes are well-formed.

Instead: **each machine owns exactly one file it alone writes.**

```
manifest/LAP-1-you.json
manifest/macbook-pro-you.json
```

Readers glob `manifest/*.json` and merge in memory, taking only files whose name
matches the machine recorded inside: a sync client's conflict copy (`LAP-1 (1).json`)
carries the same machine name and would otherwise replace the real file, so it is
skipped and `doctor` reports it. Conflicts on the same bucket are resolved by the
newest `seen` value; an exact tie goes to the machine whose name sorts first, so every
machine agrees. No machine ever writes another machine's file, so concurrent pushes
cannot lose data.
`projects.json` at the root is still written on every push as a merged, read-only
compatibility view for the existing PowerShell scripts and for anyone browsing the
folder by hand.

### Entry schema

```jsonc
{
  "schemaVersion": 1,
  "machine": "LAP-1-you",
  "projects": {
    "E--work-api": {
      "path":     "E:\\work\\api",        // recovered from the transcript's cwd
      "leaf":     "api",
      "remote":   "https://github.com/you/api",
      "os":       "windows",              // NEW: disambiguates path separators
      "seen":     "2026-09-01T09:30:00Z", // newest transcript's mtime, RFC 3339 UTC
      "sessions": 12,                     // NEW: cheap drift check for doctor
      "source":   "transcript",           // NEW: transcript | claude-json | manual
      "related":  [{ "path": "E:\\work\\web", "remote": "https://github.com/you/web" }]
    }
  }
}
```

`workFolders` lists the folders archived under `work/<machine>/`, each a `WorkFolder`:
`path`, `exactFile` (the path is one file, not a folder), `files`, `bytes`, `skipped`
(set, for example `over 250 MB: big`, naming the subfolders not copied) and `origin`, a
`WorkOrigin` `{machine, path}` naming where a restored folder first came from.
`workIndexed` records that every transcript has been scanned for work folders once, so
later pushes scan only the ones that changed. Both are omitted when empty.

`related` lists the other git repositories the project's sessions worked in — a
multi-folder workspace — so `import` can repoint paths into them too, found by remote
only. Older shards hold `seen` as a bare date, which still compares correctly as a
string against the longer form.

`schemaVersion` is mandatory from v1 — the reference implementation has no version
field and would need a guess-the-shape migration later.

### Identity for memory-only buckets — a gap found in the live archive

`Get-ProjectIdentity` reads `cwd` from the newest `.jsonl` in a bucket. Three buckets
in the live archive (`e--web-app`, `e--old-web-app`,
`e--Laravel-Applications-shop`) hold memory `.md` files and **zero transcripts**, so
they get no manifest entry at all and can never be identity-matched on import — their
memory is archived but unroutable.

Resolution order for a bucket's real path, in the port:

1. `cwd` from the newest transcript in the bucket (`source: "transcript"`)
2. the `projects` map in `~/.claude.json`, which keys project state by absolute path —
   match the path whose computed slug equals the bucket name, case-insensitively
   (`source: "claude-json"`). Read-only; `.claude.json` is still never *synced*.
3. an operator-supplied override in config (`source: "manual"`)
4. no path → keep the bucket in the manifest with `path: ""` so it is at least
   *visible*, and have `import` report it as unroutable rather than skipping silently.

### Interaction with `CLAUDE_CODE_PROJECT_DIR_NAME`

Since Claude Code v2.1.234 this env var pins the bucket name instead of deriving it
from the path — the first-party fix for the same problem `restore --rewrite-from`
solves by hand. When it is set, **a bucket name is not a slug at all** and slug
computation must not be assumed reversible or comparable. Detect it, record it in the
manifest entry, and skip the slug-based fallbacks for that bucket. Also worth a README
note: since v2.1.223, `claude --resume <session-id>` searches every project on the
machine and `Ctrl+A` in the picker widens to all projects, so a mis-filed session is
findable without any rewrite.

---

## 5. Package layout

```
claude-sessions-sync/
  cmd/claude-sessions/               one file per command, plus:
      main.go         dispatch, exit codes, printable() for archive text
      flags.go        flags anywhere on the line, ~ expansion
      project.go      --archived: read the archive instead of ~/.claude
      push.go         also INDEX.md, the log, machine names
      restore.go      also --update and memory filing, shared with import and pull
      backup.go completion.go config.go doctor.go import.go install.go
      ls.go machines.go render.go search.go stats.go
      *_test.go       e2e_test.go builds the real binary and runs it on a fixture
  internal/claude/                   Claude Code's own layout
      root.go         resolve CLAUDE_CONFIG_DIR / ~/.claude
      bucket.go       buckets, slug computation, case-insensitive lookup
      settings.go     settings.json read-only view: retention, hooks
      hooks.go        install/remove our SessionEnd hook, settings backups
      jsonorder.go    key-order-preserving JSON round trip for settings.json
      state.go        project paths from ~/.claude.json (read, never copied)
      transcript.go   streaming JSONL reader (tolerant)
      testdata/       BOM + CRLF fixture
  internal/archive/                  the synced folder
      config.go       destination resolution, session-sync.json, render-exclude.txt
      manifest.go     sharded manifest and index read/merge/write, name validation
      copy.go         copy with preserved mtime, symlink checks
      atomic.go       atomic writes, mtime tolerance
      lock.go         single-instance lock, orphaned .tmp-* cleanup
      drives_windows.go / drives_other.go     drive letters for auto-detection
      process_windows.go / process_other.go   is a lock holder still alive
  internal/identity/
      resolve.go      the 4-step matching ladder, one-pass folder scan
      remote.go       git remote from .git/config, normalisation
      local.go        which recorded paths are safe to open
  internal/rewrite/
      rewrite.go      the path forms, stream rewrite + JSON re-validation
  internal/render/
      render.go       pages, indexes, render state, time budget
      page.go         inline CSS, Content-Security-Policy
      redact.go       credential masking
  internal/hostagent/                per-OS sweep
      hostagent.go    job names, per-profile Job
      spec.go         plist, systemd units, cron line, task XML (no build tag)
      exec.go         system binaries by absolute path
      windowless.go   the GUI-subsystem copy the Windows task runs
      install_windows.go sweep_windows.go   Task Scheduler via schtasks /XML
      install_darwin.go  sweep_darwin.go    launchd user agent
      install_linux.go   sweep_linux.go     systemd user timer, cron fallback
      legacy_other.go the PowerShell predecessor's task exists only on Windows
```

`internal/claude` is the only package that knows Claude Code's on-disk shape. When a
release changes the transcript format, that is the only place to fix.

---

## 6. Invariants to carry over

Each of these cost real debugging in the PowerShell version. They are requirements,
not suggestions, and each gets a named test.

1. **Timestamp comparison needs a ~2s tolerance.** Google Drive's virtual filesystem
   stores coarser timestamps than NTFS: a file copied there reads back rounded
   (`.2718814Z` → `.2710000Z`), so it is a fraction of a millisecond *older* than its
   source. An exact `>=` therefore fails for every file on every run and re-uploads the
   entire archive — this happened, 19 files every 30 minutes. Same idea as
   `rsync --modify-window`; 2s also covers FAT/exFAT on a USB stick. **Any future
   size/mtime comparison against a synced or removable filesystem needs the same
   tolerance.**
2. **The SessionEnd hook must never fail.** Missing drive, offline mount, unexpected
   panic: log and exit 0. A hook that fails breaks the end of a session. Wrap `push` in
   a recover at the top level when `--quiet` is set.
3. **Parse transcripts defensively.** Skip and count unparseable lines; print the count
   at the foot of rendered output; never abort. If a Claude Code upgrade ever empties
   the HTML, the `.jsonl` is untouched and still resumable.
4. **A rewritten transcript is re-validated before it is kept.** Every output line must
   parse as JSON; on any failure delete the destination and abort with the first bad
   line. A transcript that no longer parses is worse than no transcript.
5. **Rewrite rule order matters.** JSON-escaped (`E:\\x`) before literal (`E:\x`),
   or the literal rule corrupts the escaped form. Four forms, all case-insensitive:
   JSON-escaped, forward-slash, literal, bucket-slug. Bare folder name is opt-in only.
6. **Never sync `.credentials.json` or `.claude.json`.**
7. **`cleanupPeriodDays` defaults to 30.** `doctor` warns when it is unset or low; this
   is the reason the tool is load-bearing rather than a nicety. (Set to 3650 on this
   machine on 2026-08-31.)
8. **Bucket casing follows the string Claude saw, not the disk.** `E:\` has been
   recorded as `e--`. Always match an existing bucket case-insensitively before
   computing a new slug.
9. **The archive is append-and-update only.** Never delete a transcript from the
   destination; a session that ages out locally must survive in the archive. (Generated
   files are another matter: `render --exclude` withdraws pages, `machines --forget`
   removes a manifest and index file, and orphaned `.tmp-*` files are cleaned up.)
10. **Idempotent install.** Re-running replaces our own hook rather than adding a
    second, preserves unrelated settings and other people's hooks, and backs up
    `settings.json` before touching it. (Verified in PowerShell against a seeded
    settings file carrying a foreign `SessionEnd` entry and a `PostToolUse` formatter —
    both survived install; uninstall removed only ours.)

### New invariants the port should add

11. **Atomic writes.** `INDEX.md`, the manifest shard, and every rendered HTML file are
    written to a temp file in the same directory and renamed into place. A sync client
    watching the folder must never see a half-written file. The PowerShell version
    writes in place.
12. **Single-instance lock.** The 30-minute sweep and a SessionEnd hook can fire
    together. Take an advisory lock (an `O_EXCL` lockfile with a stale-PID check) in
    `<claude-root>`. An unattended second instance waits up to 45 seconds
    (`--lock-wait`) and then logs and exits 0; one run at a terminal is told at once.
    Skipping straight away left a session unarchived until the next sweep, or overnight.
13. **UTF-8, no BOM, LF endings** on every file written, on every OS.

---

## 7. Per-OS install

The one genuinely new engineering. `install` must be reversible and must not require
admin/root anywhere.

| | Windows | macOS | Linux |
|---|---|---|---|
| Sweep | Task Scheduler task `claude-sessions-sync`, 30 min, running `claude-sessionsw.exe push --quiet` | launchd user agent, `StartInterval` | systemd user timer; cron fallback if no systemd |
| Registration | `schtasks /Create /XML` (not the COM API: no dependency, no elevation) | `~/Library/LaunchAgents/*.plist` + `launchctl bootstrap` | `~/.config/systemd/user/*.timer` + `systemctl --user enable --now` |
| Gotcha | `schtasks /SC MINUTE` cannot turn off "don't start on battery" or "stop when unplugged", so the task is defined in XML (read as UTF-16). Task Scheduler opens a console window for every console program, so it runs a GUI-subsystem copy of the binary. | `launchctl bootstrap gui/$UID` (not the deprecated `load`); needs a real login session. | `systemctl --user enable --now` needs lingering (`loginctl enable-linger`) to run when logged out — detect and say so rather than silently not running. |

**Names.** The task, agent and timer names are per profile. The default `~/.claude`
gets the plain names (`claude-sessions-sync` on Windows); any other profile gets the
same names with a suffix hashed from its path, and its job passes `--claude-dir`,
because a scheduler does not inherit `CLAUDE_CONFIG_DIR` from a shell. `Claude Session
Sync` is the PowerShell predecessor's task: `install` reports it, and only removes it
with `--replace-powershell-sweep`. Uninstall deletes a task only when the program it
runs is `claude-sessions.exe` or `claude-sessionsw.exe` — a shared name once cost a
working sweep that this tool had not created.

**The windowless copy.** `claude-sessionsw.exe` is the installed binary with the two-byte
PE Subsystem field changed from console to GUI, written by `install` beside
`claude-sessions.exe`. Windows starts it with no console, and its exit code still
reaches the task's last result. (`conhost --headless` also hid the window, but
swallowed the exit code and is a launch pattern endpoint security flags.) `doctor`
fails a task still running the console build, which an install from an older version
registered; running `install` again moves it over.

**Hook form.** Use the `args` exec form rather than a shell string. The hook is
`"command": "<claude-root>/bin/claude-sessions", "args": ["push", "--quiet"]` with a
120-second timeout on every OS, so no shell — pwsh, Windows PowerShell or Git Bash —
has to exist for it to run. Claude Code kills the hook at the timeout, which is why an
unattended push renders pages for at most 45 seconds and leaves the rest for the next
run.

`install` must also handle the binary itself: copy the running executable to
`<claude-root>/bin/` (`os.Executable()` + copy, not a symlink, so an upgrade of the
source doesn't break a running hook) and refuse to install a hook pointing at a path
inside the synced archive.

---

## 8. Testing

The constraint that decides the release: **macOS and Linux builds cannot be compiled or
tested from the author's Windows-only machine.** So CI is not optional and the README must not
claim cross-platform support until the matrix is actually green.

- **Unit, table-driven:** slug computation, remote normalisation, the 4 rewrite forms,
  mtime tolerance at the boundary (0s / 1.9s / 2.1s), manifest merge with conflicting
  `seen` dates.
- **Golden fixtures** in `testdata/`: a handful of real transcripts, scrubbed. Include
  one with an unknown entry shape, one with a truncated final line (a killed session),
  and one memory-only bucket.
- **The scenario that proved the PowerShell version** (2026-08-31), as an integration
  test: a session recorded at `/home/you/dev/web-app` on Linux, imported onto
  Windows where two different folders are both named `web-app`. The git remote
  must pick the right one; with the remote blanked it must refuse to choose and list
  both. Assert on both outcomes.
- **CI:** GitHub Actions matrix `windows-latest` / `macos-latest` / `ubuntu-latest`,
  `go test ./...` plus a smoke test of `install` → `push` → `doctor` → `uninstall`
  against a temp `CLAUDE_CONFIG_DIR`. Release via GoReleaser, `linux/darwin/windows` ×
  `amd64/arm64`.

---

## 9. Porting order

Each milestone is independently useful, and 1–3 already replace the daily-driver path.

| # | Milestone | Contents |
|---|---|---|
| 1 | **Read-only core** | `internal/claude` (root, bucket, transcript reader) + `ls` + `doctor`. Nothing writes. Proves the format handling against the real archive before anything can damage it. |
| 2 | **push** | Change detection with tolerance, memory files, sharded manifest, `INDEX.md`, atomic writes, lock. Run it alongside the PowerShell version and diff the archive — it must produce a byte-identical `INDEX.md`. |
| 3 | **restore + import** | The rewrite engine, re-validation, and the identity ladder. The differentiator; the most test coverage. |
| 4 | **render** | HTML templates, embedded CSS, `<details>` for tool calls/results/thinking, 4000-char truncation with a pointer back to the `.jsonl`. |
| 5 | **install/uninstall** | Windows first (it is testable here), then the macOS and Linux backends behind CI. |
| 6 | **backup + release** | `backup`, README, LICENSE, GoReleaser, tag v0.1.0. |

Retire the PowerShell scripts only after milestone 5 has run on this machine for a
week. Until then the Go binary writes to a *separate* archive folder so a bug cannot
touch the working one.

---

## 10. Decisions taken, and what is still open

**Settled 2026-09-01:**

1. **Name.** Repo `claude-sessions-sync`; the binary stays `claude-sessions`, which is
   what gets typed. The archive folder keeps its own name (`claude-sessions`) — it is
   a data directory, not the tool.
2. **Module path.** `github.com/ashiqfardus/claude-sessions-sync` — personal account,
   not the work org, since this is personal tooling.
3. **License.** MIT.

**Still open:**

4. **Does `push` also render HTML by default?** It does today (`-NoHtml` opts out).
   Rendering is the slowest step and the least essential; the alternative is
   `render` on the sweep only, not on every session end.
5. **Should `import` be offered as a first-run prompt?** `install --pull` exists, but a
   new machine arguably wants `import` (identity matching), not `pull` (exact bucket).
