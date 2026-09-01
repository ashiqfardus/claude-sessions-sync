# claude-sessions — design

**Status:** design agreed, no code written. Written 2026-09-01 on LAP-100203.
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

Single binary, subcommands. Global flags: `--archive <path>`, `--claude-dir <path>`,
`--quiet`, `--json`, `--dry-run`, `--verbose`.

| Command | Replaces | Purpose |
|---|---|---|
| `push` | `sync-claude-sessions.ps1` (default) | Copy changed transcripts + memory to the archive; update the manifest and `INDEX.md`; render HTML. |
| `pull` | `sync-claude-sessions.ps1 -Pull` | Copy archived transcripts into local buckets by exact bucket name, skipping any that exist. The dumb, same-layout case. |
| `import` | `import-sessions.ps1` | The smart case: resolve each archived project to a local folder by identity, then file and rewrite. |
| `restore` | `restore-claude-history.ps1` | Place a folder of `.jsonl` into the right bucket, rewriting an old project path to a new one. The primitive `import` calls. |
| `render` | `render-sessions-html.ps1` | Transcripts → mobile-readable HTML + `index.html`. |
| `install` / `uninstall` | `install-claude-sync.ps1` | Per-OS: merge the SessionEnd hook, register the periodic sweep, persist config, first push. |
| `backup` | `backup-claude.ps1` | Whole-profile snapshot to a folder or zip, with a manifest and a loud warning at zero transcripts. |
| `doctor` | *(new)* | Diagnose: archive reachable, hook installed and parseable, sweep registered and last result, `cleanupPeriodDays`, bucket/manifest drift, clock skew against the archive. |
| `ls` | *(new, cheap)* | List sessions with project, date, size, first prompt — the `INDEX.md` content on the terminal. |

Notes on the surface:

- `push` is the default when the binary is invoked with no subcommand, because that is
  what the hook and the sweep run.
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
  projects/<bucket>/memory/*.md
  manifest/<machine>.json        <- CHANGED: was a single projects.json
  projects.json                  <- kept, generated, read-only compatibility view
  html/<bucket>/<session-id>.html
  html/index.html
  INDEX.md
  README.md
```

### Local

- Claude root: `$CLAUDE_CONFIG_DIR` if set, else `~/.claude`. **Always resolve through
  this** — the current scripts hardcode `%USERPROFILE%\.claude` and would silently
  operate on the wrong profile.
- Config: `<claude-root>/session-sync.json` (keep the existing filename and the
  `destination` key, so an existing install keeps working).
- Log: `<claude-root>/session-sync.log`, one line per run.
- Tools/binary: `<claude-root>/bin/claude-sessions` (+`.exe`). The hook and the sweep
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
manifest/LAP-100203.json
manifest/macbook-pro.json
```

Readers glob `manifest/*.json` and merge in memory. Conflicts on the same bucket are
resolved by the newest `seen` date, with the local machine's entry preferred on a tie.
No machine ever writes another machine's file, so concurrent pushes cannot lose data.
`projects.json` at the root is still written on every push as a merged, read-only
compatibility view for the existing PowerShell scripts and for anyone browsing the
folder by hand.

### Entry schema

```jsonc
{
  "schemaVersion": 1,
  "machine": "LAP-100203",
  "projects": {
    "e--airos-frontend": {
      "path":     "E:\\airos-frontend",   // recovered from the transcript's cwd
      "leaf":     "airos-frontend",
      "remote":   "https://github.com/savoyit/airos-frontend",
      "os":       "windows",              // NEW: disambiguates path separators
      "seen":     "2026-09-01",
      "sessions": 12,                     // NEW: cheap drift check for doctor
      "source":   "transcript"            // NEW: transcript | claude-json | manual
    }
  }
}
```

`schemaVersion` is mandatory from v1 — the reference implementation has no version
field and would need a guess-the-shape migration later.

### Identity for memory-only buckets — a gap found in the live archive

`Get-ProjectIdentity` reads `cwd` from the newest `.jsonl` in a bucket. Three buckets
in the live archive (`e--airos-frontend`, `e--airv2-frontend-web`,
`e--Laravel-Applications-innovix`) hold memory `.md` files and **zero transcripts**, so
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
claude-sessions/
  cmd/claude-sessions/main.go        thin: flag parsing, exit codes
  internal/claude/                   Claude Code's own layout
      root.go         resolve CLAUDE_CONFIG_DIR / ~/.claude
      bucket.go       slug computation, case-insensitive bucket lookup
      settings.go     settings.json read/merge/write, hook shapes
      transcript.go   streaming JSONL reader (tolerant)
  internal/archive/
      config.go       destination resolution, session-sync.json
      manifest.go     sharded read/merge/write
      copy.go         change detection, atomic writes
      index.go        INDEX.md
  internal/identity/
      resolve.go      the 4-step matching ladder
      remote.go       git remote normalisation
      scan.go         bounded folder scan with the skip-list
  internal/rewrite/
      rules.go        the 4 path forms
      apply.go        stream rewrite + JSON re-validation
  internal/render/
      html.go         templates
      assets/         embedded CSS via embed.FS
  internal/hostagent/                per-OS install
      windows.go      Task Scheduler (schtasks / COM)
      darwin.go       launchd plist
      linux.go        systemd user timer, cron fallback
  testdata/           golden transcripts + fixture trees
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
9. **The archive is append-and-update only.** Never delete from the destination; a
   session that ages out locally must survive in the archive.
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
    `<claude-root>`; a second instance logs and exits 0.
13. **UTF-8, no BOM, LF endings** on every file written, on every OS.

---

## 7. Per-OS install

The one genuinely new engineering. `install` must be reversible and must not require
admin/root anywhere.

| | Windows | macOS | Linux |
|---|---|---|---|
| Sweep | Task Scheduler, `Claude Session Sync`, 30 min | launchd user agent, `StartInterval` | systemd user timer; cron fallback if no systemd |
| Registration | `schtasks` or the COM API | `~/Library/LaunchAgents/*.plist` + `launchctl bootstrap` | `~/.config/systemd/user/*.timer` + `systemctl --user enable --now` |
| Gotcha | PS 5.1 cannot express indefinite repetition — the current script uses a daily 00:05 trigger with a 30-min repetition over 1 day. Go calling `schtasks` directly can set this cleanly. | `launchctl bootstrap gui/$UID` (not the deprecated `load`); needs a real login session. | `systemctl --user enable --now` needs lingering (`loginctl enable-linger`) to run when logged out — detect and say so rather than silently not running. |

**Hook form.** Use the `args` exec form (`"command": "powershell.exe", "args": [...]`)
rather than a shell string. There is **no `pwsh` and no Git Bash on LAP-100203**, so
`"shell": "powershell"` — which invokes pwsh — would fail. With a Go binary the hook
becomes `"command": "<claude-root>/bin/claude-sessions", "args": ["push", "--quiet"]`
on every OS, which removes this whole class of problem.

`install` must also handle the binary itself: copy the running executable to
`<claude-root>/bin/` (`os.Executable()` + copy, not a symlink, so an upgrade of the
source doesn't break a running hook) and refuse to install a hook pointing at a path
inside the synced archive.

---

## 8. Testing

The constraint that decides the release: **macOS and Linux builds cannot be compiled or
tested from LAP-100203 (Windows only).** So CI is not optional and the README must not
claim cross-platform support until the matrix is actually green.

- **Unit, table-driven:** slug computation, remote normalisation, the 4 rewrite forms,
  mtime tolerance at the boundary (0s / 1.9s / 2.1s), manifest merge with conflicting
  `seen` dates.
- **Golden fixtures** in `testdata/`: a handful of real transcripts, scrubbed. Include
  one with an unknown entry shape, one with a truncated final line (a killed session),
  and one memory-only bucket.
- **The scenario that proved the PowerShell version** (2026-08-31), as an integration
  test: a session recorded at `/home/aislam/dev/airos-frontend` on Linux, imported onto
  Windows where two different folders are both named `airos-frontend`. The git remote
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

---

## 11. Still outstanding, unrelated to this port

The **LAP-100056 transcripts have never been retrieved**. They are at
`C:\Users\administrator.GG\.claude\projects\e--airv2-frontend-web\` on that machine,
which is not reachable from LAP-100203 — no ping, no `C$` share — so the copy has to be
done by hand off that box. The old `E:\airv2-frontend-web` is now `E:\airos-frontend`
(`github.com/savoyit/airos-frontend`), so those transcripts need path rewriting, not
just a move:

```
restore --source '<copied e--airv2-frontend-web>' \
        --project-path 'E:\airos-frontend' \
        --rewrite-from 'E:\airv2-frontend-web'
```

Run it once without `--include-bare-name` first; it reports how many lines still
mention the bare name so that call can be made separately.
