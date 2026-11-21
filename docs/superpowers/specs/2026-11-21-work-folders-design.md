# Work folders: back up the files Claude makes outside `~/.claude`

Status: draft for approval · 2026-11-21 · reviewed once by a fresh builder; nine gaps
resolved (one by the user: §7a)

## Why

`claude-sessions` archives everything under the Claude root: transcripts, subagent
runs, tool output, memory, rules and skills. It archives nothing else.

Most real work lands somewhere else. One user keeps every test run, review report,
SQL script and mapping CSV in `~/SQL` on macOS and `E:\SQL` on Windows. Neither
folder is a git repo or in a synced drive, and the user assumed `claude-sessions`
covered them. It did not. When a session moves to the other computer with `import`,
the conversation still points at `/Users/<user>/SQL/...` and Claude finds nothing
there.

Other users keep that kind of output somewhere else, so no path can be hard-coded.

**Goal:** files Claude creates or works on outside git repos are archived
automatically. On another machine, `import` puts them back and repoints the
conversations, so a resumed session finds them.

## How detection was chosen (measured on the motivating machine)

| Signal | Text files covered in `~/SQL` (1,632 total) |
|---|---|
| Only files Claude wrote with Write/Edit (50 files) | 50 (3%) |
| Folders containing those files, recursively | 1,567 (96%) |
| + folders that Bash commands `cd` into | 1,632 (100%) |
| Session `cwd` inside `~/SQL` | 1 folder; `cd` in Bash rarely persists |

Most files come from scripts Claude runs (query output, test results, generated
CSVs), not from its file-writing tools. So the unit is **the folder Claude worked
in**, not the file.

## Goals

- Zero setup: on by default, detected from transcripts.
- Small: text-like work only. The motivating `~/SQL` folder is 3.3 GB, but only
  ~65 MB of it is reports, scripts and mappings. The rest is a copied repo, its
  `.next` cache, APKs and screenshots.
- A session imported on another machine finds its work files at the paths the
  conversation now names.
- Users choose where another machine's files land, and can opt out.

## Non-goals (v1)

- Backing up git repos or anything inside one. The code lives in git.
- Images, video, archives, binaries, build caches. The user explicitly rejected the
  size.
- Deleting from the archive when a local file is deleted. The archive keeps
  everything, as it does for transcripts.
- A manual include list. Detection covered 100% of the motivating case. Revisit if
  users report misses.
- Including work files in the `backup` zip, or in HTML render.
- Live two-way sync or merging of file contents.

## Anti-goals (a failure even if it works)

- Copying secrets (`.env`, keys, credentials) into a synced cloud folder.
- Silently filling the user's cloud drive with gigabytes.
- Overwriting a local file the user changed since it was last pushed or restored.
- Writing anywhere outside the computed target folders because of what the archive
  says. Archive contents come from other machines and are untrusted.
- Pushing the SessionEnd hook past its time budget.

## Approaches considered

1. **Part of the existing push/import (chosen).** push detects folders and copies
   their files into `work/<machine>/`. import places them by the user's setting and
   adds the moves to the existing transcript rewrite. This reuses `RulesForMoves`, the
   add-only restore rule (`shouldRestore`), `NeedsCopy` with its 2 s tolerance,
   per-machine archive folders and the shard. No new command to schedule.
2. **A separate `work push/pull` command.** Rejected: a second thing to install and
   schedule, and a resumed session only works if both ran.
3. **Tell users to add the folders to Google Drive for desktop.** Rejected: it can't
   filter (3.3 GB), doesn't work for Dropbox or OneDrive users, and can't repoint
   conversations.

## Design

### 1. Detection (push)

For each transcript push scans, collect candidate folders:

- the parent folder of every `Write`, `Edit`, `MultiEdit` and `NotebookEdit` target
  (`file_path` / `notebook_path` in `tool_use` input);
- the target of every `cd <absolute path>` in a `Bash` command;
- every recorded `cwd` (`claude.Cwds` already reads these).

Windows paths recorded in MSYS form (`/e/SQL`) go through `nativePath`. Then drop a
candidate when it is:

- inside a git repo (`identity.RepoRoot` != ""), or the Claude root, or inside it;
- a temp location: `os.TempDir()`, `/tmp`, `/private/tmp`, `/var/folders`, Claude
  Code scratchpads;
- a network/UNC path (never opened; same rule as the archive);
- missing on disk at push time.

**Broad folders** are the home folder, any ancestor of it, a filesystem or drive root,
and the home's `Desktop`, `Downloads` and `Documents`. They are never walked. Only the
exact files Claude wrote there are kept, so a session started in `~` cannot pull in
the whole home folder.

Nested candidates collapse to the outermost one. A candidate inside a folder that
import placed from another machine keeps that origin (see §6, rule 0). The result is
this machine's **work folders**, recorded in its shard (see §4). Only transcripts that changed since the
last push are rescanned. Folders found earlier stay in the shard, so detection cost
does not grow with history.

### 2. What is copied

Walk each work folder recursively, without following symlinks, and copy a file when:

- it is not under a skipped directory: `.git`, any nested git repo, `node_modules`,
  `.next`, `vendor`, `.venv`/`venv`, `__pycache__`, `.cache`, `dist`, `build`,
  `target`, `.gradle` (builder may extend this list);
- its type is not denied: images, video, audio, archives (`.zip .gz .tar .7z .rar`),
  executables and packages (`.apk .ipa .exe .dll .so .dylib .jar`), database files
  (`.sqlite .db .bak .dump`);
- it is at most **10 MB**;
- it is not secret-shaped: `.env*`, `*.pem`, `*.key`, `*.p12`, `*.pfx`, `id_rsa*`,
  `id_ed25519*`, `*credentials*`, `*secret*`;
- it is not in the user's exclude list (§5).

A denylist rather than an allowlist, so users with `.ipynb`, `.yaml`, `.r` or `.xlsx`
work are covered without configuration.

**Per-folder cap: 250 MB after filtering.** A bigger folder is not copied at all. It
is logged to `session-sync.log` and shown as a doctor warning that names the folder and
points to `config exclude-folder`. The hook and the sweep run `--quiet`, so a printed
notice alone would never be seen. The cap is what keeps the "silently fill the drive"
anti-goal. (Builder's call after review.)

Copy rule: `NeedsCopy` (newer, or a different size within tolerance). Unlike
transcripts, a smaller file **is** allowed to replace a larger one: edits shrink
files. Files restored from another machine and unchanged since are **not** pushed
back as this machine's (§3, held files).

### 3. Archive layout

```
<archive>/work/<machine>/<encoded absolute path>/...
```

`<encoded absolute path>` is the source path with its root made a plain segment:
`/Users/a/SQL` becomes `Users/a/SQL`, and `E:\SQL` becomes `E/SQL`. Each machine
writes only its own `work/<machine>/`, so machines never contend.

### 4. Shard

Add to this machine's `manifest/<machine>.json`:

```json
"workFolders": [
  { "path": "/Users/a/SQL/po_e2e", "files": 132, "bytes": 1840221 },
  { "path": "/Users/a/Desktop/notes.md", "exactFile": true }
]
```

`Home` is already recorded and is needed for placement. Older builds ignore the
field.

### 5. Settings (`session-sync.json`, per machine)

| Key | Values | Default |
|---|---|---|
| `workBackup` | `on` / `off` | `on` |
| `workPlacement` | `home` / `machine` | `home` |
| `workMap` | `{ "<path on another machine>": "<local folder>" }` | empty |
| `workExclude` | local folders never backed up | empty |

Commands, named after the existing `config map`:

```
claude-sessions config work on|off
claude-sessions config work-placement home|machine
claude-sessions config map-folder <path from another machine> <local folder>
claude-sessions config unmap-folder <path from another machine>
claude-sessions config exclude-folder <local folder>
claude-sessions config unexclude-folder <local folder>
```

`map-folder` validates that the local folder exists, as `config map` does. Its first
argument is another machine's path, and `import` prints those paths so the user can
copy them.

`exclude-folder` also **removes this machine's archived copy** of that folder
(`work/<me>/<encoded path>`) and drops it from the shard. It prints what was removed
and says the cloud drive's trash and version history may still hold it. A folder is
usually excluded because it holds something private, so leaving the copy would defeat
the point. Deletion is confined to `work/<me>/`. `work off` stops copying and
restoring but keeps the archive. (Builder's call after review.)

### 6. Placement (import / pull)

For each other machine's work folder `P` (the own shard is skipped):

0. `P` carries an origin naming **this** machine (it was placed elsewhere and worked
   on there): target is the original path. Without this, a folder that goes
   Windows → Mac → Windows lands one `claude-work/` level deeper on each round trip.
1. A `workMap` key equal to `P` or an ancestor of it: target is the mapped folder plus
   the rest of `P`.
2. Otherwise, with `workPlacement = home` and `P` inside that machine's recorded
   `Home`: target is the local home plus the same relative path, with separators
   converted.
3. Otherwise (`machine` placement, or `P` outside the home, such as `E:\SQL` arriving
   on a Mac): `~/claude-work/<machine>/<encoded path>`.

**The archive is untrusted.** It is a shared folder, and with `home` placement
another machine's shard decides where files land under the local home. So:

- Every computed file target must stay inside its computed folder target. Reject
  `..`, absolute names and symlinks coming from the archive, and never open UNC paths,
  all as for other archive content.
- Re-apply push's candidate rules to the **local target**. Refuse a target inside the
  Claude root, inside a local git repo, in a temp location, or a broad folder (exact
  files excepted).
- Refuse targets in places that run or configure things: any dot-folder or dot-file
  under the home (`~/.zshrc`, `~/.config`, `~/.ssh`), `~/Library`, `AppData`, and the
  OS autostart locations. A refused folder is reported, never written.
- Ignore a shard whose `Home` or folder paths are not absolute.

Then add `rewrite.Move{From: P, To: target}` for each folder whose target differs
from `P`, before transcripts are filed, alongside the existing project, related-repo
and Claude-root moves. Apply them wherever `import` files transcripts (`restoreInto`).
`RulesForMoves` already orders by specificity and refuses non-absolute sources. Broad
folders are never a move source, only exact files are, so a move can never rewrite
the whole home path.

**Across Windows and macOS the whole path must resolve,** not only the prefix.
Today's rules swap the prefix only, so `E:\SQL\po\x.md` would become
`~/claude-work/W/E/SQL\po\x.md`, which macOS cannot open. Work-folder moves therefore
also convert separators in the rest of the path to the local form. That covers the
JSON-escaped `\\` form, which must leave JSON escapes like `\n` and `\"` alone. The
rest of the path ends at whitespace, a quote, or a JSON escape other than `\\`. A
path with a space after its first folder is converted only up to the space, a known
limit. Existing project rewrite rules are not changed. (Builder's call after review.)

**`pull`** keeps its contract (same layout, no rewriting): it places work files at
their recorded paths, under the same safety rules, and does nothing when they don't
fit this machine. **The `restore` command** reads a folder, not the archive, so it has
no shards and does not handle work files.

**Project placement:** an explicit `config map <bucket>` wins. Otherwise, a project
whose recorded path lies inside a placed work folder lives at the placed path.
`identity.Resolve` cannot find folders under `claude-work/`.

### 7. Restore rules and conflicts

- Default: **add-only**. Missing local files are written. Existing ones are left
  alone.
- `--update`: replace a local file with a newer archive copy **only if that local
  file was restored from the same source machine and is unchanged since**. Any other
  existing file is kept, including one this machine created itself even if it was
  pushed long ago. The incoming copy is written beside it as
  `<name>.<machine>.<ext>`, and import reports it. This matters when two machines are
  mapped onto one folder and both keep a `REVIEW_INDEX.md`: neither may replace the
  other's, or the replaced content is then pushed over the original everywhere.
- Never restore a future-dated file (the existing `shouldRestore` rule).
- Record every restored file's source machine, original path, size and mtime in a
  local held-files state (the same pattern as `noteHeldMemory`/`noteHeldProfile`).
  push skips a held file until it changes, so restored files don't bounce back as
  this machine's, and it reads the origin for §6 rule 0.

### 7a. Repointing conversations already here (`import --repoint`)

The rewrite in §6 only reaches transcripts as they are filed. `restoreInto` never
rewrites one that is already local, and a rewritten copy with the same entries
compares as `Same`. That leaves two gaps: conversations imported before this feature
(the motivating Mac holds 104 that point at `E:\SQL\...`), and conversations filed
before the user changed `map-folder` or `work-placement`. The user chose to cover both
with an explicit, opt-in step:

- `claude-sessions import --repoint` applies the current work-folder moves to
  transcripts **already on this machine**, including subagent transcripts. The moves
  are `P → target` from §6, plus `previous target → current target` for every folder
  whose placement changed. Earlier placements come from the held-files state (§7).
  Only work-folder moves are applied, never project moves.
- It never runs implicitly. A plain `import` prints how many local conversations
  still point at old paths and suggests `--repoint`.
- It skips any transcript modified in the last hour, since it may belong to a live
  session, and reports how many were skipped.
- Before changing a transcript it copies the original to
  `<claude-root>/repoint-backup/<yyyymmdd-hhmm>/<bucket>/...`. It writes through a
  temp file and a rename, never in place. `--dry-run` reports counts without writing.
- Files are not moved. If a placement changed, import places files at the new target
  (add-only) and the old copies stay where they are.
- **push must accept a repointed transcript.** The rewrite can make it shorter than
  this machine's archived copy (`/Users/a/SQL` → `E:\SQL`), and today's truncation
  guard would then refuse it and warn on every run. That is the same trap as the
  redacted-transcript issue. Mark repointed transcripts so push replaces the archived
  copy once, or compare them by entries (`claude.Compare`) instead of size. The
  mechanism is the builder's call; the behavior is required.

### 8. Reporting

- `push` (not `--quiet`): when a folder is backed up for the first time, print
  `now backing up <folder> (N files, X MB)`. `--dry-run` lists what would be copied.
- `import`: per source machine, list where its work folders went, plus the
  `config map-folder` and `config work-placement` commands that change it.
- `doctor`: on/off; number of work folders, files and archived size for this
  machine; a warning above **500 MB** pointing to `config exclude-folder`; skipped
  files counted by reason (too large, denied type, secret-shaped).

### 9. Budget

The SessionEnd hook runs a full `push --quiet`, which Claude Code kills at 120 s.
Today only page rendering has a budget inside it (`quietRenderBudget`, 45 s), and
`withinDeadline` (5 s) wraps only the per-reply Stop hook. Work-folder copying gets
its own budget under `--quiet`, built the same way as the render budget. Work not
finished in time is left for the 30-minute sweep. The per-reply Stop hook (`pushOne`)
copies no work folders.

## Left to the builder

- Exact parsing of `cd` in Bash commands. Absolute paths only; relative `cd` and
  `pushd` may be ignored.
- Where the held-files state lives, and its format.
- Extending the skipped-directory and denied-type lists from real data.
- Exact doctor and import wording, consistent with existing output.
- Whether detection runs inside `pushOne` or as a pass after transcripts.

## Testing

- Detection: Write/Edit/MultiEdit/NotebookEdit paths, absolute `cd`, cwd; git-repo,
  Claude-root, temp and UNC exclusions; broad folders contribute exact files only;
  nesting collapse; MSYS paths.
- Copy filter: denied types, 10 MB cap, secret names, skipped dirs, symlinks not
  followed, exclude list.
- Placement: map (exact and ancestor), home-relative, `machine`, outside-home
  fallback; Windows → macOS and macOS → Windows paths; traversal names rejected.
- Rewrite: transcript paths repointed for mapped and home-relative placements; a
  broad folder is never a move source.
- Restore: add-only; `--update` with local unchanged vs. changed (sibling file); held
  files not pushed back; future-dated skipped.
- End-to-end, two machines (extend `two_machines_test.go`): push on A, import on B,
  and the transcript's path resolves to an existing file on B.
- Off switch: `workBackup = off` copies and restores nothing.
- Repoint: an existing local transcript is rewritten with a backup kept; one modified
  within the hour is skipped; a remap moves old target → new target; `--dry-run`
  writes nothing; the next push accepts a repointed transcript that got shorter.
- Archive safety: a hostile shard (`Home` = `/`, paths into `~/.zshrc`,
  `~/Library/LaunchAgents`, `AppData\...\Startup`, `~/.claude/skills`, `..` names)
  writes nothing.
- Round trip: Windows → Mac → Windows returns the folder to its original path.
- `exclude-folder` removes only `work/<me>/<that folder>`.
- `GOOS=linux|darwin|windows go vet ./...` before pushing.

## Docs to update

README (what is backed up, the settings, privacy note), DESIGN.md §3 layout and §4
shard schema, CHANGELOG.
