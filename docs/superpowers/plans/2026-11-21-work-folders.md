# Work Folders Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `push` archives the folders Claude works in outside `~/.claude` and outside
git repos. `import` places them on another machine and repoints the conversations,
including ones already filed (`import --repoint`).

**Architecture:** A new pure package, `internal/work`, holds detection, folder
selection, the copy filter, path encoding, placement and its safety rules, and the
held-files state. Every function takes explicit inputs, so Windows-to-macOS cases test
on any OS. `cmd/claude-sessions/work.go` wires that package into push, import, pull
and doctor. `internal/rewrite` gains one flag that converts separators in the rest of
a path. The archive gains `work/<machine>/…` and two shard fields.

**Tech Stack:** Go (standard library only, as today); tests are `go test` unit tests
plus the existing end-to-end harness that builds the real binary
(`cmd/claude-sessions/e2e_test.go`, `two_machines_test.go`).

**Spec:** `docs/superpowers/specs/2026-11-21-work-folders-design.md` (commit b404b6f).
Read it before any task.

## Global Constraints

- Limits: `MaxFileBytes = 10 << 20` (10 MB); `MaxFolderBytes = 250 << 20` (250 MB
  after filtering); doctor warns above `500 << 20` (500 MB); `quietWorkBudget = 30 * time.Second`.
- Archive layout: `<archive>/work/<machine>/<Encode(absolute path)>/…`. Each machine
  writes only its own `work/<machine>/`.
- Skipped directories: `.git`, any directory holding a `.git` entry (nested repo),
  `node_modules`, `.next`, `vendor`, `.venv`, `venv`, `__pycache__`, `.cache`, `dist`,
  `build`, `target`, `.gradle`.
- Denied extensions (case-insensitive): images `.png .jpg .jpeg .gif .webp .bmp .tif
  .tiff .heic .ico .svg`, video/audio `.mp4 .mov .webm .avi .mkv .mp3 .wav .m4a`,
  archives `.zip .gz .tgz .tar .7z .rar .bz2 .xz`, binaries `.apk .aab .ipa .exe .dll
  .so .dylib .jar .class .o .a .bin`, databases `.sqlite .sqlite3 .db .bak .dump`.
- Secret-shaped names (case-insensitive, base name): `.env*`, `*.pem`, `*.key`,
  `*.p12`, `*.pfx`, `id_rsa*`, `id_ed25519*`, `*credentials*`, `*secret*`.
- **Dot entries:** push skips every file or folder whose name starts with `.`, and
  restore refuses any archive path with a segment starting with `.`. This is stricter
  than the spec's secret list. It stops a hostile archive from planting `.git/config`
  (git runs `core.fsmonitor`) or `.vscode/tasks.json`, and covers `.env`, `.pgpass`
  and `.netrc`.
- Repoint: skip transcripts modified within 1 hour; back up to
  `<claude-root>/repoint-backup/<yyyymmdd-hhmm>/<bucket>/<relative path>`; write
  through a temp file and rename.
- Never run `git`. Repo detection is `identity.RepoRoot`, which reads files.
- Old builds must keep working: new shard and config fields use `omitempty`.
  `SaveConfig` must persist the new keys and keep unknown ones.
- Before every commit touching code: `go test ./...` passes, and
  `for os in linux darwin windows; do GOOS=$os go vet ./...; done` is clean.
- Commit on `main` (the owner's rule: no feature branch). Do not push.
- The repo is public. No real machine names, user names, employers or project names
  in code, tests or docs. Use `WIN`, `MAC`, `/Users/a`, `C:\Users\a`, `E:\SQL`.

## Review Focus

1. **Paths with spaces** (`/Users/a/My Work/run 1`): detection from a quoted `cd` and a
   Write path, encoding, placement and the JSON rewrite must all keep the whole path.
   Tested in Task 1 and the Task 8 end-to-end test.
2. **A work folder deleted locally after it was archived**: push keeps its shard entry
   (archive keeps everything), so other machines still restore it. Tested in Task 5.
3. **Case differences on Windows** (`e:\sql\po` vs `E:\SQL\po`, map key `e:\sql`):
   one folder, not two, and the map still applies. Tested in Tasks 2 and 6.
4. **Megabyte-long transcript lines** (big tool results): detection must read them
   (no `bufio.Scanner` token limit). Tested in Task 1.
5. **Non-ASCII file names** (`রিপোর্ট.md`, `résumé.csv`): copied, encoded, restored and
   rewritten intact. Tested in Task 3 and the Task 8 end-to-end test.

---

### Task 1: Detect candidate paths in a transcript

**Files:**
- Create: `internal/work/detect.go`
- Test: `internal/work/detect_test.go`

**Interfaces:**
- Produces:
  - `type Candidate struct { Path string; File bool }`. `File` is true for a
    Write/Edit/MultiEdit/NotebookEdit target, false for a folder (`cd` target or cwd).
  - `func Candidates(transcript, home string) ([]Candidate, error)`: deduplicated,
    case-sensitive, in first-seen order. `home` expands a leading `~/`.

- [ ] **Step 1: Write the failing tests.** In `detect_test.go`, build transcripts
  from a helper `toolUse(name string, input map[string]string) string` that emits one
  assistant JSONL line with `"cwd"` and a `tool_use` content block.
  - `TestCandidatesWriteTools`: Write `/w/a/r.md`, Edit `/w/b/x.sql`, MultiEdit
    `/w/c/y.py`, and NotebookEdit with `notebook_path` `/w/d/n.ipynb` produce four
    `File: true` candidates. A Read of `/w/e/z.md` produces none.
  - `TestCandidatesCd`: Bash `cd /w/run && python x.py` gives `{/w/run, false}`;
    `cd "/w/My Work/run 1"; ls` gives `{"/w/My Work/run 1", false}`; `cd ~/SQL/po`
    with home `/h` gives `{/h/SQL/po, false}`; `cd sub` (relative) and `pushd /w/p`
    give nothing; `cd 'E:\SQL\po'` gives `{E:\SQL\po, false}`.
  - `TestCandidatesCwd`: a line with `"cwd":"/w/proj"` gives `{/w/proj, false}`.
  - `TestCandidatesLongLine`: a 5 MB tool_result line between two Write lines; both
    Writes are found.
- [ ] **Step 2: Run `go test ./internal/work/ -run TestCandidates`.** Expected: FAIL
  (package or function missing).
- [ ] **Step 3: Implement `Candidates`.** Read with `bufio.Reader.ReadBytes('\n')`,
  as `claude.Cwds` does. Decode only lines containing `"tool_use"`, into a minimal
  struct (`message.content[].{type,name,input.{file_path,notebook_path,command}}`).
  Take cwds from `claude.Cwds(transcript)`. Parse `cd` with one regexp over the
  command: `cd` at the start or after `;`, `&`, `|`, `(`, or whitespace, followed by a
  double-quoted, single-quoted or bare argument. Keep only arguments that are
  absolute (`/…`, `X:\…`, `X:/…`) or start with `~/`. Pass MSYS forms (`/e/SQL`)
  through unchanged; Task 2 normalises them.
- [ ] **Step 4: Run the tests.** Expected: PASS.
- [ ] **Step 5: Commit** `Work folders: detect candidate paths in transcripts`.

### Task 2: Choose work folders from candidates

**Files:**
- Create: `internal/work/folders.go`
- Test: `internal/work/folders_test.go`

**Interfaces:**
- Consumes: `Candidate` (Task 1).
- Produces:
  - `type Env struct { Home, ClaudeRoot string; Temp []string; Windows bool; RepoRoot func(string) string; Exists func(string) bool }`.
    `Windows` selects MSYS conversion and case-insensitive comparison.
  - `type Folder struct { Path string; Exact bool }`. `Exact` means `Path` is a single
    file, kept from a broad folder.
  - `func Folders(cands []Candidate, env Env) []Folder`, sorted by `Path`.
  - `func Within(child, parent string, fold bool) bool`. Equal counts. It compares
    with `\` and `/` treated as the same separator, so it works on paths from either
    OS; `fold` ignores case.
  - `func IsBroad(dir string, env Env) bool`.
  - `func WindowsPath(p string) bool`: a drive-letter prefix (`X:\` or `X:/`).
  - All three are exported for Tasks 6, 8 and 9.

- [ ] **Step 1: Write the failing tests** (`folders_test.go`, fake `RepoRoot` and
  `Exists` over a map):
  - `TestFoldersParentOfWrittenFile`: File `/h/SQL/po/r.md` gives `{/h/SQL/po, false}`.
  - `TestFoldersDropped`: the dropped cases are a candidate inside a repo (fake
    RepoRoot returns `/h/dev/app` for `/h/dev/app/x`), inside `ClaudeRoot`
    `/h/.claude`, under `Temp` `/tmp`, a UNC path `\\srv\share\x`, a missing path, and
    a relative path.
  - `TestFoldersBroad`: File `/h/notes.md` gives `{/h/notes.md, true}`; folder
    candidates `/h`, `/`, `/h/Desktop`, `/h/Downloads` and `/h/Documents` give
    nothing; File `/h/Desktop/a.txt` gives `{/h/Desktop/a.txt, true}`;
    `/h/Documents/reports` is not broad.
  - `TestFoldersCollapse`: `/h/SQL` plus `/h/SQL/po` plus File `/h/SQL/po/x.md`
    gives only `{/h/SQL, false}`.
  - `TestFoldersWindowsCase` (Review Focus 3): with `Windows: true`, `E:\SQL\po` and
    `e:\sql\po\r.md` give one folder; MSYS `/e/SQL/run` becomes `E:\SQL\run`.
- [ ] **Step 2: Run `go test ./internal/work/ -run TestFolders`.** Expected: FAIL.
- [ ] **Step 3: Implement `Folders`, `Within` and `IsBroad`.** Broad means: equal to
  `Home`, an ancestor of it, a filesystem or drive root, or `Home` joined with
  `Desktop`, `Downloads` or `Documents`. Collapse: sort by length, then drop anything
  `Within` an already-kept non-exact folder. Use `filepath` only on the local side.
- [ ] **Step 4: Run the tests.** Expected: PASS.
- [ ] **Step 5: Commit** `Work folders: choose folders from candidates`.

### Task 3: Copy filter and archive path encoding

**Files:**
- Create: `internal/work/collect.go`
- Test: `internal/work/collect_test.go`

**Interfaces:**
- Produces:
  - constants `MaxFileBytes`, `MaxFolderBytes`, `WarnBytes` (Global Constraints).
  - `func Allowed(rel string, size int64) (ok bool, reason string)`. `reason` is one
    of `"dot"`, `"type"`, `"secret"`, `"large"`; `rel` uses `/`. Restore (Task 8)
    reuses it.
  - `type Collected struct { Files []string; Bytes int64; Skipped map[string]int }`
    with `Files` absolute and sorted.
  - `func Collect(f Folder, exclude []string) (Collected, error)`.
  - `func Encode(p string) string`: `/Users/a/SQL` → `Users/a/SQL`, `E:\SQL\po` →
    `E/SQL/po`, `/e/SQL` stays `e/SQL`. Always `/`-separated, with no leading
    separator.

- [ ] **Step 1: Write the failing tests** (build trees in `t.TempDir()`):
  - `TestCollectFilters`: the tree is `r.md`, `q.sql`, `m.csv`, `n.ipynb`,
    `shot.png`, `app.apk`, `big.csv` (10 MB + 1), `.env`, `prod.key`,
    `db-credentials.json`, `node_modules/x.js`, `.next/a.js`, `inner/.git/HEAD` plus
    `inner/code.go`, and `sub/deep.md`. `Files` holds exactly `m.csv`, `n.ipynb`,
    `q.sql`, `r.md` and `sub/deep.md`. `Skipped` is `type:2, large:1, dot:1,
    secret:2`.
  - `TestCollectSymlinks`: a symlinked dir `link -> /outside` and a symlinked file
    are not followed or copied (skip on Windows when symlink creation fails).
  - `TestCollectExact`: `Folder{Path: file, Exact: true}` collects just that file.
  - `TestCollectExclude`: exclude `<root>/sub` drops `sub/deep.md`.
  - `TestCollectNonASCII` (Review Focus 5): `রিপোর্ট.md` and `résumé.csv` are
    collected with their names intact.
  - `TestEncode`: the three examples above, plus
    `/Users/a/My Work` → `Users/a/My Work`.
- [ ] **Step 2: Run `go test ./internal/work/ -run 'TestCollect|TestEncode'`.**
  Expected: FAIL.
- [ ] **Step 3: Implement.** Use `filepath.WalkDir`. Skip `d.Type()&fs.ModeSymlink`
  entries. Return `fs.SkipDir` for skipped and dot directories, and for any directory
  holding a `.git` entry. `Allowed` checks dot, then secret, then type, then size.
- [ ] **Step 4: Run the tests.** Expected: PASS.
- [ ] **Step 5: Commit** `Work folders: copy filter and archive encoding`.

### Task 4: Settings, shard schema and `config` commands

**Files:**
- Modify: `internal/archive/config.go` (`Config`, `SaveConfig`)
- Modify: `internal/archive/manifest.go` (`Shard`)
- Modify: `cmd/claude-sessions/config.go` (usage text, new actions, `show`)
- Create: `cmd/claude-sessions/work.go` (`workDir`, `purgeWork`)
- Test: `internal/archive/config_test.go` (extend), `cmd/claude-sessions/work_test.go` (new, package `main_test`)

**Interfaces:**
- Consumes: `work.Encode`, `work.Within` (Tasks 2–3).
- Produces:
  - `Config` fields `WorkBackup string` (`json:"workBackup,omitempty"`, `""`/`"on"`
    = on), `WorkPlacement string` (`"home"` default, or `"machine"`),
    `WorkMap map[string]string`, `WorkExclude []string`, and the method
    `func (c Config) WorkOn() bool`.
  - `Shard` fields `WorkFolders []WorkFolder` (`json:"workFolders,omitempty"`) and
    `WorkIndexed bool` (`json:"workIndexed,omitempty"`).
  - `type WorkFolder struct { Path string; Exact bool; Files int; Bytes int64; Skipped string; Origin *WorkOrigin }`,
    with JSON keys `path`, `exactFile`, `files`, `bytes`, `skipped` and `origin`, all
    but `path` omitempty.
  - `type WorkOrigin struct { Machine, Path string }`.
  - In `cmd`: `func workDir(dest, machine, p string) string` and
    `func purgeWork(dest, machine, folder string) (removed int, err error)`.
    `purgeWork` removes this machine's archived folders `Within` `folder` and drops
    them from its shard.

- [ ] **Step 1: Write the failing tests.**
  - `TestSaveConfigKeepsWorkKeys` (archive): save a config with all four work keys,
    then save `Config{Destination: x}`. All four survive. Saving
    `WorkExclude: []string{}` clears the list.
  - `TestConfigWorkCommands` (e2e, `work_test.go`): `config work off`, then `config
    show` prints `work backup  off`. `config work-placement machine` takes effect, and
    `config work-placement sideways` fails. `config map-folder E:\SQL <tempdir>` is
    listed. `config map-folder relative/path <tempdir>` fails. `config map-folder
    /x <missing>` fails. `unmap-folder` removes the entry.
  - `TestExcludeFolderPurges` (e2e): push a machine whose archive already holds
    `work/<m>/<Encode(dir)>/r.md` and a shard entry for `dir` (seeded by hand).
    `config exclude-folder <dir>` removes that archive folder and drops the shard
    entry. It prints `removed` and `trash or version history`. It leaves
    `work/<other machine>/…` untouched.
- [ ] **Step 2: Run `go test ./internal/archive/ ./cmd/claude-sessions/ -run 'Work|Exclude'`.**
  Expected: FAIL.
- [ ] **Step 3: Implement** the fields, the `SaveConfig` keys (same pattern as
  `projectMap`), and the actions `work`, `work-placement`, `map-folder`,
  `unmap-folder`, `exclude-folder` and `unexclude-folder`.
  - `map-folder` validation: the first argument passes `rewrite.AbsoluteAnywhere`;
    the second exists as a folder.
  - `exclude-folder` stores an absolute path and calls `purgeWork` when the archive
    is reachable. Otherwise it says the next push removes the copy.
  - Add the work settings to `config show`.
- [ ] **Step 4: Run the tests.** Expected: PASS.
- [ ] **Step 5: Commit** `Work folders: settings, shard fields, config commands`.

### Task 5: Push work folders

**Files:**
- Modify: `cmd/claude-sessions/work.go` (add `pushWork`)
- Modify: `cmd/claude-sessions/push.go` (`runPush`: collect transcripts to scan; call `pushWork` before `pushProfile`)
- Test: `cmd/claude-sessions/work_test.go`

**Interfaces:**
- Consumes: Tasks 1–4. (Task 8 later adds the held-files check inside `pushWork`; the
  signature below does not change.)
- Produces:
  - `type workPushResult struct { Folders, Copied, Unchanged int; Bytes int64; New []string; Deferred bool }`.
  - `func pushWork(root, dest string, shard *archive.Shard, prev archive.Shard, scan []string, cfg archive.Config, dryRun bool, budget time.Duration, logf func(string, ...any)) workPushResult`.
  - `func workEnv(root string) work.Env`: real `identity.RepoRoot`, `os.Stat`,
    `os.TempDir()`, `/tmp`, `/private/tmp`, `/var/folders` and `/private/var/folders`,
    with `Windows` set to `runtime.GOOS == "windows"`.

- [ ] **Step 1: Write the failing e2e tests** (`work_test.go`). Add a helper that runs
  the binary with `HOME` (and `USERPROFILE`) set to a temp home, and a transcript
  helper that writes a session whose lines include Write/Bash tool uses.
  - `TestPushWorkFolder`: the session writes `<home>/SQL/po/r.md` and runs
    `cd <home>/SQL/po && python q.py`. The folder holds `r.md`, `out.csv` and
    `shot.png`. After `push`:
    - `work/<m>/<Encode(<home>/SQL/po)>/` holds `r.md` and `out.csv`, not `shot.png`;
    - the shard has one `WorkFolder` with `Files: 2`;
    - the log has `WORK: now backing up`.
  - `TestPushWorkIgnoresRepoAndTemp`: writes inside a `gitRepo(...)` folder and
    under `os.TempDir()` archive nothing.
  - `TestPushWorkOverCap`: a folder whose filtered total is over `MaxFolderBytes`
    (use 26 files of 9.9 MB) is not copied. Its shard entry has `Skipped: "over 250
    MB"` and the log names it.
  - `TestPushWorkOff`: `config work off`, then push copies nothing and carries the
    previous shard's `WorkFolders` forward unchanged.
  - `TestPushWorkDeletedFolderKept` (Review Focus 2): push, delete the folder, push
    again. The shard still lists it and the archive copy remains.
  - `TestPushWorkDryRun`: `push --dry-run` writes nothing under `work/` and prints
    the files it would copy.
  - `TestPushWorkSmallerFileReplaces`: shrink `r.md`, then push. The archive copy
    matches the new content.
  - `TestStopHookCopiesNoWork`: `--from-hook` writes nothing under `work/`.
- [ ] **Step 2: Run `go test ./cmd/claude-sessions/ -run 'TestPushWork|TestStopHook'`.**
  Expected: FAIL.
- [ ] **Step 3: Implement.**
  - **Scan list.** In `runPush`, collect into one outer slice the transcripts copied
    this run and their `subagents/*.jsonl` extras. When `!prev.WorkIndexed`, use all
    transcripts instead.
  - **Choose folders.** `pushWork` runs `work.Candidates` on each scanned file, adds
    `prev.WorkFolders` paths as candidates, and runs `work.Folders`. It drops folders
    `Within` `cfg.WorkExclude` and calls `purgeWork` for previous entries now
    excluded. A previous folder that no longer exists keeps its old entry and is not
    walked.
  - **Copy.** Each folder is collected; anything over `MaxFolderBytes` is skipped.
    Files are copied with `archive.CopyFile` when `archive.NeedsCopy`, with no
    "shorter" guard.
  - **Budget.** Under `--quiet` pass `quietWorkBudget` and stop when it runs out:
    set `Deferred`, keep the unfinished folders' previous entries, and log `WORK: …
    left for the next run`.
  - **Shard.** Set `shard.WorkFolders` and `WorkIndexed = true`.
  - **Off.** When `!cfg.WorkOn()`, return after copying `prev.WorkFolders` into the
    shard.
- [ ] **Step 4: Run the tests and the full suite** (`go test ./...`). Expected: PASS.
- [ ] **Step 5: Vet on three OSes, then commit** `Work folders: push`.

### Task 6: Placement and its safety rules

**Files:**
- Create: `internal/work/place.go`
- Test: `internal/work/place_test.go`

**Interfaces:**
- Consumes: `Encode`, `Within`, `IsBroad`, `Env`; `archive.WorkOrigin`.
- Produces:
  - `type Source struct { Machine, Home, Path string; Exact bool; Origin *archive.WorkOrigin }`.
  - `type Local struct { Env; Machine, Placement string; Map map[string]string; SameLayout bool }`.
  - `func Place(src Source, l Local) (string, error)`: the local target for the
    folder (or the file, when `Exact`).
  - `func SafeJoin(target, rel string) (string, error)`: rejects absolute `rel`,
    `..`, segments starting with `.`, and results outside `target`.
  - `var ErrRefused = errors.New("refused")`. Wrap it with the reason.

- [ ] **Step 1: Write the failing tests.** Local home is `t.TempDir()`, and expected
  paths use `filepath.Join`, so the tests pass on every OS.
  - `TestPlaceHome`: `Path /Users/a/SQL/po`, `Home /Users/a`, home placement gives
    `<home>/SQL/po`.
  - `TestPlaceOutsideHome`: `Path E:\SQL\po`, `Home C:\Users/a`, `Machine WIN` gives
    `<home>/claude-work/WIN/E/SQL/po`. The same result comes from `Placement
    "machine"` for `/Users/a/SQL/po` → `<home>/claude-work/MAC/Users/a/SQL/po`.
  - `TestPlaceMap` (Review Focus 3): `Map{"e:\\sql": <dir>}` places `E:\SQL\po\run`
    at `<dir>/po/run`. An exact-key match places at `<dir>`.
  - `TestPlaceOrigin`: `Origin{Machine: "MAC", Path: "/Users/a/SQL/po"}` with local
    `Machine "MAC"` gives `/Users/a/SQL/po` (rule 0, before the map).
  - `TestPlaceSameLayout`: `SameLayout` returns `src.Path` when it is absolute in
    local form, and errors otherwise.
  - `TestPlaceRefused` (spec "Archive safety"): each of these returns `ErrRefused`:
    - a `Home` of `/` and a non-absolute `Home`;
    - targets resolving to `<home>/.zshrc`, `<home>/.ssh/x`, `<home>/Library/LaunchAgents`,
      `<home>/AppData/Roaming/Microsoft/Windows/Start Menu/Programs/Startup` and
      `<home>/.claude/skills/x`;
    - the home itself (not `Exact`), and a target inside a fake repo.
  - `TestSafeJoin`: `../x`, `/abs`, `a/../../b`, `.git/config` and `sub/.env` are
    refused; `sub/r.md` is accepted.
- [ ] **Step 2: Run `go test ./internal/work/ -run 'TestPlace|TestSafeJoin'`.**
  Expected: FAIL.
- [ ] **Step 3: Implement the rules in spec §6 order 0–3, then the safety checks.**
  - **Comparisons.** Source-side paths are strings that may be Windows or Unix (a
    drive-letter prefix means Windows). Compare them case-insensitively and
    separator-agnostically. Convert the rest of the path to local separators.
  - **Refusals.** Refuse a target `Within` `ClaudeRoot`, temp, a repo, a broad folder
    (unless `Exact`), any path whose part under `Home` has a segment starting with
    `.`, or `Home/Library`. Also refuse any segment equal to `AppData`
    (case-insensitive), and `/etc`, `/Library`, `/System`.
- [ ] **Step 4: Run the tests.** Expected: PASS.
- [ ] **Step 5: Commit** `Work folders: placement and safety rules`.

### Task 7: Convert separators in the rest of a moved path

**Files:**
- Modify: `internal/rewrite/rewrite.go` (`Move`, `Rule`, `RulesForMoves`, `Apply`)
- Test: `internal/rewrite/rewrite_test.go`

**Interfaces:**
- Produces: `type Move struct{ From, To string; ConvertTail bool }`. With
  `ConvertTail`, the `json-escaped` rule built for that move rewrites every `\\` pair
  in the rest of a match to `/`. The rest ends at whitespace, `"`, `'`, `` ` ``, `|`,
  `<`, `>`, or a `\` not followed by `\`. Other rules and other moves are unchanged.

- [ ] **Step 1: Write the failing tests.**
  - `TestConvertTail`: `Move{From: "E:\\SQL", To: "/Users/b/SQL", ConvertTail: true}`
    on the JSON line `{"t":"open E:\\\\SQL\\\\po\\\\x.md now"}` gives
    `{"t":"open /Users/b/SQL/po/x.md now"}`.
  - `TestConvertTailStopsAtEscapes`: `E:\\SQL\\po\nnext` keeps `\n`; `"E:\\SQL\\a\"`
    keeps `\"`; `E:\\SQL\\My Work\\x` converts only up to the space (the known limit).
  - `TestNoTailByDefault`: the same line with `ConvertTail: false` keeps today's
    output exactly (prefix only).
  - `TestTailStillValidJSON`: `rewrite.File` over a 3-line transcript with
    `ConvertTail` reports no unparseable lines and decodes to the expected paths.
- [ ] **Step 2: Run `go test ./internal/rewrite/`.** Expected: FAIL.
- [ ] **Step 3: Implement.** Add an unexported `tail bool` to `Rule`, set it only on
  the json-escaped rule for a `ConvertTail` move, and extend `Apply`'s replacement
  loop to consume and convert the rest of the path.
- [ ] **Step 4: Run all tests** (`go test ./...`). Expected: PASS; existing rewrite
  tests unchanged.
- [ ] **Step 5: Commit** `Rewrite: optionally convert separators after a moved prefix`.

### Task 8: Restore work folders on import and pull

**Files:**
- Create: `internal/work/held.go`
- Modify: `cmd/claude-sessions/work.go` (add `restoreWork`)
- Modify: `cmd/claude-sessions/import.go` (`cmdImport`: call `restoreWork` before the
  moves list is built; append its moves; a project placement step in `resolve`;
  printed summary; `cmdPull`: same-layout call)
- Test: `internal/work/held_test.go`, `cmd/claude-sessions/work_test.go`

**Interfaces:**
- Consumes: `Place`, `SafeJoin`, `Allowed`, `Encode`, `Move.ConvertTail`.
- Produces:
  - `type HeldFile struct { Machine, From string; Size int64; MTime time.Time }`.
  - `type Held struct { Files map[string]HeldFile; Placed map[string]string }`.
    `Placed` is keyed `machine + "|" + source path`, with the value the local target.
  - `func LoadHeld(path string) Held` (never nil maps) and
    `func (h Held) Save(path string) error` (via `archive.WriteFileAtomic`).
  - `func heldWorkPath(root string) string` →
    `<root>/session-sync-base/work-held.json`.
  - `type workRestoreResult struct { Placed map[string]string; Filed, Kept, Beside, Refused int; Notes []string }`.
  - `func restoreWork(root, dest string, shards map[string]archive.Shard, cfg archive.Config, dryRun, update, sameLayout bool) ([]rewrite.Move, workRestoreResult)`.
  - `const byWorkFolder identity.How = "work folder"`.

- [ ] **Step 1: Write the failing tests.**
  - `TestHeldRoundTrip` (unit): save, load, equal; a missing file gives empty maps.
  - `TestImportWorkAcrossHomes` (e2e, two machines with different `HOME`s, Review
    Focus 1 and 5):
    - A's session writes `<homeA>/My Work/run 1/রিপোর্ট.md` and runs a `cd` into it;
      A pushes.
    - B imports. The file exists at `<homeB>/My Work/run 1/রিপোর্ট.md`, and B's filed
      transcript names that path, not `<homeA>`.
  - `TestImportWorkMapFolder` (e2e): B runs `config map-folder <A's folder> <dirB>`,
    then imports. Files land in `<dirB>`.
  - `TestImportWorkAddOnly`: an existing local file with different content is left
    alone without `--update`.
  - `TestImportWorkUpdate`:
    - a file B restored from A and left unchanged is replaced by A's newer copy with
      `--update`;
    - a file B created itself at that path is kept, and A's copy is written as
      `<name>.<A>.<ext>`, with the import output saying so;
    - a future-dated archive file is never restored.
  - `TestRestoredNotPushedBack`: B pushes after the import. `work/<B>/` does not
    contain A's restored files.
  - `TestImportWorkHostileShard`: a hand-written shard whose `Home` is `/`, with
    folders under `~/.ssh`, archive names containing `..` and `.git/config`. Import
    writes nothing outside the expected targets and reports refusals.
  - `TestPullWorkSameLayout`: `pull` places files at the recorded path when it is
    valid here and rewrites no transcript.
  - `TestImportProjectInsideWorkFolder`: a session whose cwd was `<homeA>/SQL/po`
    (not a repo) is filed for `<homeB>/SQL/po`.
  - `TestImportWorkOff`: with `config work off` on B, import restores no work files
    and adds no work moves.
- [ ] **Step 2: Run `go test ./internal/work/ ./cmd/claude-sessions/ -run 'Held|ImportWork|Restored|PullWork|ProjectInsideWork'`.**
  Expected: FAIL.
- [ ] **Step 3: Implement `restoreWork`** as spec §6–§7.
  - **Which folders.** Return nothing when `!cfg.WorkOn()`. Skip the own shard and
    shards with a non-absolute `Home`. `Place` each folder.
  - **Walk.** Walk only regular files under `workDir(dest, s.Machine, wf.Path)`.
    Filter each with `Allowed`, then `SafeJoin`.
  - **Write rules.** Add-only by default. With `update`, replace only a file held
    from the same machine whose stat is unchanged; otherwise write it beside as
    `<name>.<machine>.<ext>`. Never write a file dated more than 1 h in the future.
  - **State.** Record each written file in `Held.Files` and each placement in
    `Held.Placed`. Return
    `Move{From: wf.Path, To: target, ConvertTail: work.WindowsPath(wf.Path) && runtime.GOOS != "windows"}`
    for each target that differs from `wf.Path`.
  - **Import wiring.** In `cmdImport`, `resolve` returns `byWorkFolder` when `p.Path`
    is `Within` a placed source path (after `cfg.Mapped`, before `identity.Resolve`).
    Print one line per source machine: where its folders went, plus the
    `config map-folder` and `config work-placement` commands.
  - **Push side.** In `pushWork`, skip a file whose `Held.Files` entry matches its
    current stat.
- [ ] **Step 4: Run the tests and the full suite.** Expected: PASS.
- [ ] **Step 5: Vet on three OSes, then commit** `Work folders: restore on import and pull`.

### Task 9: Round trip back to the original path

**Files:**
- Modify: `cmd/claude-sessions/work.go` (`pushWork`: set `Origin` from `Held.Placed`)
- Test: `cmd/claude-sessions/work_test.go`

**Interfaces:**
- Consumes: `Held.Placed`, `archive.WorkOrigin`, `Place` rule 0.

- [ ] **Step 1: Write the failing test.** `TestWorkRoundTrip` runs A push → B import →
  B works in the placed folder (a new file) and pushes → A imports. A's new file lands
  in A's **original** folder, not under `claude-work/`. B's shard entry carries
  `origin.machine == A`.
- [ ] **Step 2: Run it.** Expected: FAIL.
- [ ] **Step 3: Implement.** For each folder `F`, find a `Held.Placed` target `T` with
  `Within(F, T)`. Set `Origin{Machine, source path + rest of F after T}`.
- [ ] **Step 4: Run the tests.** Expected: PASS.
- [ ] **Step 5: Commit** `Work folders: return a folder to the machine it came from`.

### Task 10: `import --repoint` and push acceptance

**Files:**
- Modify: `cmd/claude-sessions/work.go` (add `repointLocal`, `countPointing`)
- Modify: `cmd/claude-sessions/import.go` (`--repoint` flag; hint)
- Modify: `cmd/claude-sessions/push.go` (`archiveTranscript`: accept a repointed copy)
- Test: `cmd/claude-sessions/work_test.go`

**Interfaces:**
- Consumes: the moves from `restoreWork`; the old and new targets in `Held.Placed`.
- Produces:
  - `type repointResult struct { Rewritten, Skipped, Recent int; Backup string }`.
  - `func repointLocal(root string, moves []rewrite.Move, now time.Time, dryRun bool) repointResult`.
  - `func countPointing(root string, moves []rewrite.Move) int`.
  - `type repointedStat struct { Size int64; MTime time.Time }`.
  - `func repointedPath(root string) string` →
    `<root>/session-sync-base/repointed.json`, holding a
    `map[string]repointedStat` keyed `"bucket/id"`.
  - `archiveTranscript` gains `repointed map[string]repointedStat` (callers: `runPush`
    and `pushOne`). When the local stat matches an entry it copies over without
    `Compare` or the shorter guard, then the entry is dropped.

- [ ] **Step 1: Write the failing tests.**
  - `TestRepointExisting`:
    - B imports A's sessions before A's work folder exists in the archive. A then
      pushes its folder.
    - B runs `import`: the output says `1 conversation(s)` and `--repoint`.
    - B runs `import --repoint`: the transcript now names `<homeB>/…`. The original
      is under `repoint-backup/<stamp>/<bucket>/`.
  - `TestRepointSkipsRecent`: a transcript touched 10 minutes ago is reported as
    recent and left byte-identical.
  - `TestRepointDryRun`: no file changes and no backup folder.
  - `TestRepointRemap`: after `config map-folder` to a new folder, `import --repoint`
    moves the old target paths to the new one.
  - `TestRepointedAcceptedByPush`: when B pushes after a repoint that made a
    transcript **shorter** (use a long A home and a short B home), the archive copy is
    replaced, the log has no `shorter` WARN, and a second push reads neither file
    (stat unchanged).
- [ ] **Step 2: Run `go test ./cmd/claude-sessions/ -run 'Repoint'`.** Expected: FAIL.
- [ ] **Step 3: Implement.**
  - **Selection.** `repointLocal` walks `claude.ListBuckets`, the main transcripts
    and `subagents/*.jsonl` extras. It skips files modified after `now - 1h` and
    files containing none of the rules' `Find` strings (a cheap `bytes.Contains`).
  - **Rewrite.** Rewrite each remaining file with `rewrite.File` into a temp file
    beside it. Copy the original into the backup folder, then rename over it, and
    record the new stat in `repointed.json`.
  - **Moves.** The moves are `restoreWork`'s, plus `Move{old target, new target}`
    for every `Held.Placed` entry this import changed. Record the old value before
    overwriting it.
  - **Hint.** Without `--repoint`, print `countPointing` only when there are moves.
- [ ] **Step 4: Run the tests and the full suite.** Expected: PASS.
- [ ] **Step 5: Vet on three OSes, then commit** `Work folders: import --repoint`.

### Task 11: Doctor and documentation

**Files:**
- Modify: `cmd/claude-sessions/doctor.go` (a `work folders` check)
- Modify: `README.md`, `DESIGN.md` (§3 layout, §4 shard schema), `CHANGELOG.md` (`[Unreleased]`)
- Test: `cmd/claude-sessions/work_test.go`

- [ ] **Step 1: Write the failing tests.** `TestDoctorWork` checks four states:
  - off gives `info … off`;
  - on, with one folder, gives `ok … 1 folder(s), N file(s)`;
  - an archived total over `WarnBytes` gives `warn` advice naming
    `config exclude-folder`;
  - a folder with `Skipped` set gives a `warn` that names it.
- [ ] **Step 2: Run it.** Expected: FAIL.
- [ ] **Step 3: Implement the check** from this machine's shard, using the `check`
  struct pattern.
- [ ] **Step 4: Update the docs.**
  - **README:** add work folders to "what is backed up". Add the six `config`
    commands, `import --repoint`, and a privacy note: on by default, text-like files
    only, secrets and dot files never copied, and how to exclude a folder.
  - **DESIGN.md:** add the `work/` layout and the shard fields.
  - **CHANGELOG:** describe the feature in plain words.
- [ ] **Step 5: Run the full suite and vet on three OSes.** Expected: PASS and clean.
- [ ] **Step 6: Commit** `Work folders: doctor check and docs`.
