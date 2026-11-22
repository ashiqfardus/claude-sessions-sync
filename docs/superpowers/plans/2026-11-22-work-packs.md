# Work Packs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Store first-level subfolders of a work folder that have been unchanged for 7 days as one zip each (a "pack") in the archive. Later edits go up as loose overlay files. Import and pull unpack, and the overlay wins. Also add `import/pull --no-work`.

**Architecture:**
- `internal/work/pack.go` holds the pure pack code: write, index, safe read and compact, all on `archive/zip`.
- `pushWork` decides per unit whether to pack, upload an overlay, or compact. It keeps a local record (`work-packs.json`).
- `restoreWork` extracts each pack's checked entries to a private temp folder and feeds them into the existing per-file restore path. That path does `Allowed`, `SafeJoin`, exclusions, add-only, held files and mode 0644. Pack entries that an overlay file supersedes are skipped.

**Tech Stack:** Go standard library only (`archive/zip`, `compress/flate`).

**Spec:** `docs/superpowers/specs/2026-11-22-work-packs-design.md` (commit d0f71f0). Read it, including "Decisions after the builder check".

## Global Constraints

- Pack name: `<unit>.cspack.zip` in the work folder's archive directory (`workDir(...)`), beside the unit's loose folder. Entry names are `/`-separated paths relative to the unit, with mtimes in the zip headers.
- Pack a unit only if all of these hold:
  - it is not the loose-files unit `(files in the folder itself)`, and the folder is not `Exact`;
  - it has ≥ 5 collected files;
  - every collected file has an mtime older than `now - 7*24h`;
  - it has ≤ 20,000 files;
  - its total is ≤ `work.MaxFolderBytes` (250 MB).
- Compact when the overlay exceeds 25% of the pack's uncompressed bytes or 50 files. Only compact if the merged result is ≤ 20,000 entries and ≤ `MaxFolderBytes`; otherwise keep the pack plus overlay and log one WARN.
- Restore limits per pack:
  - ≤ 20,000 entries;
  - each entry ≤ `work.MaxFileBytes`, both declared and actually read through `io.LimitReader`;
  - sum ≤ `MaxFolderBytes`;
  - regular-file entries only;
  - names that are `filepath.IsLocal` after slash conversion, not empty, no `..`, not absolute or drive form;
  - a CRC mismatch refuses the entry.
- The first pack is built from LOCAL files. After the rename, push deletes only the archive loose copies whose relative path is in the pack, so archive-only files stay.
- The overlay always wins over a pack entry with the same relative path.
- Every write or delete under `work/` goes through `realWorkDirs`. Temp packs are created with `os.CreateTemp(dir, ".tmp-"+name+"-*")`, so `RemoveOrphanTemps` cleans them up.
- Held files (restored from another machine and unchanged) are never packed and never uploaded.
- Budget: check it before starting a unit, and always finish a unit once started.
- `--no-work` skips `restoreWork`, work moves, `byWorkFolder` placement, held/placed updates and the repoint hint. It prints `work folders skipped (--no-work)`. With `--repoint` it is refused.
- `Files`/`Bytes` count uncompressed content. `WorkFolder` gains `Packed int` (`json:"packed,omitempty"`).
- Before each commit: `go test -count=1 ./...`, `for os in linux darwin windows; do GOOS=$os go vet ./...; done`, and `gofmt -l .` (must print nothing). Commit on `main`; never push. Tests touch only temp dirs.
- The repo is public: no real machine, user or employer names.

## Review Focus

1. **A pack that `Collect` would filter differently on the other OS.** Example: a file allowed on macOS sits inside a pack restored on Windows, where a name check refuses it. Expected: that entry is refused on its own and the rest restore. Test in Task 3.
2. **A unit that changes while it is being packed**, for example a file appended mid-pack. Expected: the pack records each file's size and mtime as read, so the next push sees the change and uploads it to the overlay. Test in Task 2, using a file modified after packing.
3. **The pack record is lost or stale.** Examples: `work-packs.json` deleted, or a pack removed from the archive by `exclude-folder`. Expected: push rebuilds from the pack's index if the pack exists, and treats the unit as unpacked if it doesn't. No loss and no crash. Test in Task 2.
4. **A truncated or corrupt pack in the archive**, for example from a partial sync. Expected: restore refuses that pack with a note and still restores the overlay and other units. Test in Task 3.
5. **Two machines mapped onto one folder, where both pack the same unit name.** Expected: each machine's pack lives under its own `work/<machine>/`, restore follows the usual add-only and beside rules, and nothing is overwritten. Covered by the existing per-machine layout; assert it in Task 3's round-trip test.

---

### Task 1: `--no-work` for import and pull

**Files:** Modify `cmd/claude-sessions/import.go` (flag definitions ~line 44 and ~576; `restoreWork` calls ~154 and ~663; repoint hint ~469). Test: `cmd/claude-sessions/work_test.go`.

**Interfaces:** Produces the flag `--no-work` on `import` and `pull`. Help text: "bring conversations only; skip other machines' work folders".

- [ ] **Step 1: Write failing tests.**
  - `TestImportNoWork`: A pushes a work folder; B runs `import --no-work`. B has no work files, its conversations are not rewritten by work moves, and the output contains `work folders skipped (--no-work)` and no `--repoint` hint.
  - `TestPullNoWork`: the same for `pull`.
  - `TestNoWorkRefusesRepoint`: `import --no-work --repoint` exits non-zero with a message naming both flags.
- [ ] **Step 2: Run.** `go test ./cmd/claude-sessions/ -run 'NoWork'`. Expected: FAIL.
- [ ] **Step 3: Implement.** When `--no-work` is set:
  - skip `restoreWork`;
  - leave work moves empty;
  - make `placedPath` return `""`, so there is no `byWorkFolder`;
  - don't load or save the held/placed record;
  - suppress the `countPointing` hint.
- [ ] **Step 4: Run.** Focused, then full. Expected: PASS.
- [ ] **Step 5: Commit** with subject "Import: --no-work brings conversations only".

### Task 2: Pack format and push packing

**Files:**
- Create: `internal/work/pack.go`, `internal/work/pack_test.go`.
- Modify: `cmd/claude-sessions/work.go` (`pushWork` unit loop, about lines 150-245; `workPushResult`; `purgeWork`), `internal/archive/manifest.go` (`WorkFolder.Packed`), `cmd/claude-sessions/push.go` (dry-run lines), `cmd/claude-sessions/doctor.go` (work line), `cmd/claude-sessions/work_test.go`.

**Interfaces (produces):**
- `type PackFile struct { Rel, Path string; Size int64; ModTime time.Time }`. `Rel` is `/`-separated and relative to the unit; `Path` is the local file.
- `type PackEntry struct { Size int64; ModTime time.Time }`.
- `func WritePack(dst string, files []PackFile) error`. It writes `dst` through `os.CreateTemp(filepath.Dir(dst), ".tmp-"+filepath.Base(dst)+"-*")`, then fsync and rename. Entries use Deflate, are sorted by `Rel`, and carry `Modified = ModTime`.
- `func ReadPackIndex(path string) (map[string]PackEntry, error)`. Central directory only.
- `func CompactPack(old, dst string, add []PackFile) error`. Copies old entries raw (`zip.Writer.Copy`) except those `add` supersedes, then adds `add`. Same temp+rename as `WritePack`.
- In `cmd`: `type packRecord struct { Files map[string]work.PackEntry; Overlay map[string]work.PackEntry; Bytes int64 }`. `loadPacks(root) map[string]map[string]packRecord` and `savePacks(root, m)` use `<root>/session-sync-base/work-packs.json`, keyed folder path → unit.
- `workPushResult` gains `Packed, Compacted int` and `WouldPack []string`.
- `archive.WorkFolder.Packed int`.

- [ ] **Step 1: Write failing unit tests** (`pack_test.go`).
  - `TestWritePackRoundTrip`: write 3 files; `ReadPackIndex` returns 3 entries with equal size and mtime, within 2 s.
  - `TestWritePackAtomic`: a failing file in the list leaves no `dst` and no `.tmp-*` file.
  - `TestCompactPackOverlayWins`: the old pack holds a and b; adding a changed `a` plus a new `c` gives a = new, b = old, c.
- [ ] **Step 2: Write failing e2e tests** (`work_test.go`; set file mtimes with `os.Chtimes` to `now - 8 days`).
  - `TestPushPacksOldUnit`: a unit with 6 old files is packed. The archive holds `<unit>.cspack.zip` and no loose files for it. The shard has `Packed: 1`, and `Files` counts 6.
  - `TestPushSkipsYoungOrSmallUnit`: a unit with one file 1 day old stays loose, and a 4-file old unit stays loose.
  - `TestPushKeepsArchiveOnlyFiles`: the archive has a loose `<unit>/gone.md` that is not local. After packing, `gone.md` is still in the archive.
  - `TestPushOverlayOnly`: after packing, edit one file. The next push copies just that file loose, and the pack's size and mtime are unchanged.
  - `TestPushCompacts`: 51 changed files trigger compaction. The new pack has all the entries, the overlay is gone, and the log says `WORK: compacted`.
  - `TestPushPackRecordLost`: delete `work-packs.json`. The next push uses `ReadPackIndex` and does not re-upload unchanged files. If the pack is also deleted, the unit is treated as unpacked.
  - `TestPushPackDryRun`: dry run prints `would pack <folder>/<unit> (6 files, …)` and writes nothing.
  - `TestExcludeInsideUnitRemovesPack`: `exclude-folder <unit>/sub` removes the pack and the overlay. The next push repacks without `sub`.
- [ ] **Step 3: Run** both test sets. Expected: FAIL.
- [ ] **Step 4: Implement** `pack.go`, then the per-unit logic in `pushWork`, following the Global Constraints:
  - check whether the record matches the pack on disk; if the pack is missing, drop the record;
  - choose unchanged / overlay / compact / first pack;
  - write log lines `WORK: packed <folder>/<unit> (N files)` and `WORK: compacted <folder>/<unit>`;
  - in `purgeWork`, also remove `<unit>.cspack.zip` and the unit's loose dir when the excluded path lies inside a unit;
  - doctor's work line appends `, N packed unit(s)`.
- [ ] **Step 5: Run.** Focused tests, the full suite, three-OS vet, gofmt. Expected: PASS.
- [ ] **Step 6: Commit** with subject "Work folders: pack unchanged subfolders into one zip each".

### Task 3: Restore packs (import and pull)

**Files:**
- Modify: `internal/work/pack.go`, adding the safe reader.
- Modify: `cmd/claude-sessions/work.go`, in the `restoreWork` folder loop around line 660-770. The per-file `file(src, rel, fi)` closure already does all the checks.
- Tests: `internal/work/pack_test.go`, `cmd/claude-sessions/work_test.go`.

**Interfaces (produces):** `func ExtractPack(path, tmpDir string, skip func(rel string) bool) ([]PackFile, []string, error)`.
- It validates the whole pack against the restore limits before writing anything.
- It writes each accepted entry, read through `io.LimitReader(MaxFileBytes+1)` with its CRC checked, to `tmpDir/<rel>`, with its mtime set from the header.
- It returns the extracted files and the notes for refused entries.
- `skip` lets the caller drop entries the overlay supersedes.
- The error is for a pack refused as a whole: too many entries, total too large, or a corrupt or truncated zip.

- [ ] **Step 1: Write failing unit tests,** building the hostile zips with `archive/zip` in the test.
  - `TestExtractPackRefusesTraversal`: entries `../x`, `/abs`, `C:\x` and `a/../../b` are refused with nothing written outside `tmpDir`. Valid entries are extracted.
  - `TestExtractPackRefusesSymlinkAndDirEntries`.
  - `TestExtractPackLimits`:
    - an entry declaring 1 KB but holding 11 MB is refused at 10 MB + 1;
    - 20,001 entries refuse the whole pack;
    - a 260 MB total (sparse via the declared sizes) refuses the whole pack;
    - a CRC mismatch refuses that entry.
  - `TestExtractPackCorrupt`: a truncated zip returns an error and writes nothing.
- [ ] **Step 2: Write failing e2e tests.**
  - `TestImportPackWithOverlay`: A packs a unit and then edits one file, which goes to the overlay. B imports. All files arrive, the edited one has the overlay content, and mtimes come from the pack.
  - `TestImportPackHeldNotPushedBack`: after B imports and pushes, `work/<B>/` contains no pack and no files from A.
  - `TestImportHostilePack`: an `EVIL` shard plus pack with entries `.pth`, `node_modules/x.js`, `.git/config`, `../../x` and `a.sh`. Only `a.sh` arrives, mode 0644. The others are counted as refused.
  - `TestImportCorruptPack`: a truncated pack is refused with a note, and the same folder's other units and overlay are still restored.
  - `TestPullRestoresPack`: the same-layout case.
  - Review Focus 1: a pack entry that only Windows refuses (a `~` name) is skipped alone on a Windows host. Skip this test elsewhere.
- [ ] **Step 3: Run.** Expected: FAIL.
- [ ] **Step 4: Implement.**
  - In the restore walk of each non-exact work folder, a top-level regular file `*.cspack.zip` is a pack. The walk skips it silently instead of refusing it as `.zip`.
  - For each pack, the overlay set is the loose files under `dir/<unit>/`. Call `ExtractPack` into a private temp dir (`os.MkdirTemp("", "cs-pack-*")`, removed afterwards) with `skip` = in the overlay set.
  - Feed each extracted file to `file(tmpPath, unit+"/"+rel, fi)`. The loose walk then files the overlay as it does today.
  - A pack refused as a whole is counted under `Refused` with a note.
  - Dry run extracts nothing and reports `would unpack N file(s) from <unit>`.
- [ ] **Step 5: Run.** Focused tests, the full suite, three-OS vet, gofmt, and `GOOS=windows go test -c -o /dev/null ./cmd/claude-sessions/ ./internal/work/`.
- [ ] **Step 6: Docs.**
  - README:
    - "What is copied": unchanged subfolders are packed after 7 days, and later edits go up singly;
    - the flags: `--no-work`;
    - "Upgrading": both machines need this version before packs appear.
  - DESIGN.md: one paragraph.
  - CHANGELOG `[Unreleased]`: lines for both features.
- [ ] **Step 7: Commit** with subject "Work folders: restore packs, the overlay winning".

### Task 4 (added 2026-11-22): fixes from the first real two-machine run

These came from the Mac and Windows rollout, not from this plan's spec. They are recorded in the ledger as findings a–f.

**Files:**
- `internal/claude/fork.go` (`Compare`, `lastUUID`)
- `cmd/claude-sessions/restore.go` (`restoreInto` update path, `restoreSessionFiles`)
- `cmd/claude-sessions/import.go` and `cmd/claude-sessions/profile.go` (profile under `--update`)
- `cmd/claude-sessions/import.go` (the `resolve` `byWorkFolder` fallback)
- `internal/rewrite`, only if (d) needs it
- tests beside each

Items:
- (a + f) **Copies with trailing metadata look forked or older.** Claude Code appends metadata-only lines with no new turns (`cost-state`, `last-prompt`, `tools-used`, `derived-from`, `bridge-session`). They must not make a copy look newer or forked.
  - `import --update` decides by `claude.Compare`, not by mtime. Today a local copy whose newer mtime comes only from such lines blocks a real continuation.
  - `Compare` / `lastUUID` count only conversation entries (`user` / `assistant` / `system` lines). A metadata line that carries a uuid must not decide the relation. On the real data, push filed a straight continuation as a FORK because of this.
  - Inspect the tail of `<archive>/projects/e--airos-frontend/70e28154-ccf7-420d-abc9-68b2c86e2241.jsonl` (Windows copy, 47,622,190 bytes) against the `-Users-…-airos-frontend` copy to confirm which line types carry uuids.
  - Test: a copy that has the same turns plus trailing metadata lines, set against a copy with more turns, gives `Extends` or `Behind` correctly, never `Forked`.
- (b) **`--update` overwrites rule and skill files.** Today it replaces them newest-wins: a Windows `CLAUDE.md` of 2.6 KB would have been replaced by the Mac's 1.9 KB copy.
  - Under `--update`, profile files stay add-only unless the new flag `--update-profile` is given.
  - Mention it in the help, README and CHANGELOG.
  - Test: a newer, smaller archived `CLAUDE.md` does not replace the local one under `--update`, and does replace it under `--update-profile`.
- (c) **Subagent transcripts are copied without path rewriting.** `restoreSessionFiles` copies `<id>/subagents/*.jsonl` raw.
  - File them through the same rewrite rules as the main transcript, with `rewrite.File`, temp then rename, and the same `.jsonl` validity checks.
  - Other session files (`tool-results/*`) stay a byte copy.
  - Test: a subagent transcript naming the source project path is rewritten to the local path.
- (d) **Some paths before a backtick or quote were not rewritten.** Eight mentions of `/Users/ashiqfardus/SQL` followed by a backtick or a quote, with no trailing slash, survived a work move from macOS to Windows.
  - Reproduce it in a `rewrite` test with `` `/Users/a/SQL` `` and `"/Users/a/SQL"` inside a JSON string. Fix it if real; otherwise record why.
- (e) **The `byWorkFolder` fallback creates buckets for folders that don't exist.** Mac projects under a container work folder (`~/Developer`, which had 7 loose files) got identity-less buckets on Windows.
  - The fallback applies only when the placed path exists locally after `restoreWork` has run. Otherwise keep `identity.Resolve`'s not-found or ambiguous result.
  - Test: a project inside a placed folder that has no files of its own is reported as not found, not filed.

Commit subject: "Import: compare by turns, keep rules under --update, rewrite subagents".
