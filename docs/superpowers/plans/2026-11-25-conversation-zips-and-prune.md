# Conversation Zips and Prune Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Each archived conversation idle for 30 days becomes one zip that every reader sees as if it were loose. The owner can delete archived conversations by hand (`prune`) or by an optional age rule, with a shared keep list and a deleted list that stops re-uploads.

**Architecture:**
- **One small package, `internal/convzip`.** It writes, verifies, finds, extracts and unzips a conversation's zip. It reuses the hardened zip code in `internal/work/pack.go`.
- **Readers.** Each reader that used to look only for loose `<id>.jsonl` in an archive bucket asks `convzip` for zipped copies too. Code that needs a file path gets a temp extract.
- **Pruning.** Two archive-level files, `retention.json` (keep list and age rule) and `deleted/<machine>.json` (what was pruned), plus a `prune` command and a step in push.
- **The gate.** Two shard flags hold everything back until every machine in the archive runs this build.

**Tech Stack:** Go (minimum 1.21), standard library only (`archive/zip`, `compress/flate`).

**Spec:** `docs/superpowers/specs/2026-11-25-conversation-zips-and-prune-design.md`. Read it first; this plan argues from it.

## Global Constraints

- **Go and dependencies:** Go 1.21 minimum, standard library only, one binary. Before every commit, run `go test ./... && GOTOOLCHAIN=go1.21.13 go test ./...`.
- **CI** runs on ubuntu, macos and windows. Write paths with `filepath`, and keep tests clear of AppData and short names: `TestMain` already moves TMP to `C:\cs-test-*`.
- **The archive is untrusted.**
  - Every zip goes through `work.ReadPackIndex` or `work.ExtractPack`, which walk the central directory, apply caps, check names with `SafeJoin`, and check each entry before it is written.
  - Never follow links.
  - Accept only exact names: `<id>.conv.zip` where `claude.IsSessionID(id)`. A sync client's conflict copy, such as `<id>.conv (1).zip`, is ignored.
- **Writes are atomic** (temp beside the target, then rename), using `archive.WriteFileAtomic` or the same pattern.
- **The repository is public.** No real machine, user, project or employer names in code, tests or docs. Use placeholders such as `web-app`, `you` and `LAP-2`.
- **Commits** go on `main`. Each commit message ends with:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01PWT5vgdCpfR8xZ9xTkuWzN
  ```
  Do not push.
- **Fixed values:**
  - zip idle threshold: 30 days;
  - `MinKeepDays` = 90;
  - age rule: at most once per 24 h per machine;
  - zip cap: a conversation whose files total more than `work.MaxFolderBytes` (250 MB) stays loose.
- **No turn lost:** zipping, unzipping, pruning or a run killed at any step must never lose a turn of a transcript, a subagent transcript or a tool-output file.
- **The gate is the whole archive.**
  - Zipping runs only when every shard has `ReadsZips`.
  - `prune --yes` and the age rule run only when every shard has `HonorsDeleted`.
  - An unreadable or bad shard means no.
  - Refusals and `doctor` name the machines, and suggest updating them or `machines forget`.

## Review Focus

1. **Stale views across machines.** A zipped conversation is continued on the other machine before the zip has synced there. That machine pushes a loose transcript next to a zip it cannot see yet. Every reader then prefers the loose file, and the next push here unzips without losing the zip's extra files. Test: `TestZipAndLooseBothPresentLooseWins` (Task 1) and `TestPushContinuesAZippedConversation` (Task 2).
2. **Sync-client conflict copies.** Names such as `<id>.conv (1).zip` or `<id> (conflicted copy).conv.zip` are never read, zipped over or deleted. Test: `TestFindIgnoresConflictCopies` (Task 1).
3. **Conversations over the cap.** One over 250 MB stays loose, logs one line per run at most, and is never rewritten. Test: `TestZipRefusesOverCap` (Task 1) and `TestPushLeavesOverCapLooseQuietly` (Task 6).
4. **Clock skew and bad timestamps.**
   - A modification time in the future never counts as idle or old.
   - A transcript with no parseable turn timestamp falls back to its modification time.
   - `prune` never selects a conversation it cannot date.

   Test: `TestIdleIgnoresFutureMTime` (Task 6) and `TestPruneSkipsUndatable` (Task 8).
5. **A cloud drive that streams files on demand.**
   - Quiet runs never read every zip: zipping and the age rule stay within a time budget, and the age rule dates zipped copies from index data only.
   - A read error mid-zip leaves the loose files in place.

   Test: `TestZipReadErrorKeepsLoose` (Task 1) and `TestAgeRuleReadsNoZipContent` (Task 9).

---

### Task 1: `internal/convzip` package

**Files:**
- Create: `internal/convzip/convzip.go`, `internal/convzip/convzip_test.go`
- Uses: `internal/work/pack.go` (`WritePack`, `ReadPackIndex`, `ExtractPack`, `PackFile`, `PackEntry`), `internal/claude/fork.go` (`HoldsAll`, `IsSessionID`)

**Interfaces:**
- Produces:
  - `const Suffix = ".conv.zip"`
  - `func Path(bucketDir, id string) string`: `filepath.Join(bucketDir, id+Suffix)`.
  - `func Find(bucketDir string) map[string]string`: lower-cased id to zip path. Exact names, regular files only, `IsSessionID` only.
  - `type Result struct{ Files int; Raw, Packed int64 }`
  - `var ErrTooLarge = errors.New("conversation too large to zip")`
  - `func Zip(bucketDir, id string) (Result, error)`. It zips `<id>.jsonl` and every regular file under `<id>/`, with entry names `<id>.jsonl` and `<id>/<rel>` (slash-separated).
    1. Write to a temp file beside the target (`work.WritePack` already does temp-and-rename).
    2. Re-read it with `work.ReadPackIndex`, and require every entry's size to match.
    3. Extract to a temp dir and require `claude.HoldsAll(extracted, loose)` for the transcript and each `.jsonl` under the folder.
    4. Then delete the loose transcript first, then the folder.

    Returns `ErrTooLarge` (touching nothing) when the files total more than `work.MaxFolderBytes`. Returns an error, and leaves the loose files, on any failed check.
  - `func Entries(zipPath string) (map[string]work.PackEntry, error)`: wraps `work.ReadPackIndex`.
  - `func Materialize(bucketDir, id, tmpDir string) (transcript string, cleanup func(), err error)`. Returns a transcript path with its `<id>/` folder beside it.
    - Loose only: the archive's own path, and `cleanup` is a no-op.
    - Zip present: the zip is extracted under `tmpDir`, with modification times set from the entries, and any file also present loose is copied over its extracted entry (loose wins).
    - Neither: `os.ErrNotExist`.
  - `func Unzip(bucketDir, id string) error`. Writes each zip entry into the bucket unless a loose file is already there with a modification time at or after the entry's. Each file is written atomically, with its modification time set. Then the zip is deleted. Afterwards a loose copy exists, and no zip.
- Consumes: nothing from later tasks.

- [ ] **Step 1: Write the failing tests** in `internal/convzip/convzip_test.go`:
  - `TestZipRoundTrip`:
    - a bucket with `<id>.jsonl` (3 turns) and `<id>/subagents/agent-a.jsonl`, `<id>/tool-results/x.txt`;
    - `Zip` returns `Files == 3`, `Packed < Raw`;
    - the loose files are gone and `Find` returns the zip;
    - `Materialize` yields a transcript where `claude.HoldsAll(got, original)` holds, and identical bytes for `x.txt`, with modification times equal to the originals' within 2 s.
  - `TestZipRefusesOverCap`: shrink the cap through a test hook (`var maxBytes = work.MaxFolderBytes` in the package). `Zip` returns `ErrTooLarge`, and the loose files are untouched.
  - `TestZipReadErrorKeepsLoose`: a test hook makes the verify step fail. The loose files are byte-identical afterwards, and no `.conv.zip` and no temp file remain.
  - `TestZipAndLooseBothPresentLooseWins`:
    - zip the conversation, then write a loose `<id>.jsonl` with one more turn;
    - `Materialize` returns content holding that turn, and still has `x.txt` from the zip;
    - `Unzip` leaves the longer loose transcript, restores `x.txt`, and removes the zip.
  - `TestFindIgnoresConflictCopies`: files `<id>.conv (1).zip`, `<id> (conflicted copy).conv.zip`, `notanid.conv.zip`, and a directory named `<id>.conv.zip`. `Find` returns none of them.
  - `TestUnzipRestoresMTimes`: after `Zip` then `Unzip`, every file is back with its original size and modification time.
  - `TestMaterializeRejectsCraftedZip`: a zip with entry `../evil` and one with an entry over the cap both fail, and nothing is written outside `tmpDir`. Reuse the crafted-zip builders from `internal/work/pack_test.go` if they are exported to tests; otherwise build them with `archive/zip`.

- [ ] **Step 2: Run and see them fail.** `go test ./internal/convzip/ -v`. Expected: build failure (package does not exist).

- [ ] **Step 3: Implement** the Produces list above in `internal/convzip/convzip.go`. Build the `PackFile` list with `filepath.WalkDir` over `<id>/`, regular files only.

- [ ] **Step 4: Run the tests.** `go test ./internal/convzip/ -v && GOTOOLCHAIN=go1.21.13 go test ./internal/convzip/`. Expected: PASS.

- [ ] **Step 5: Commit.** `git add internal/convzip && git commit -m "Conversation zips: write, verify, find, extract and unzip one conversation"`

---

### Task 2: push reads zipped copies

**Files:**
- Modify: `cmd/claude-sessions/push.go`. This covers:
  - `sessionHolders` (~892);
  - `heldRecord.covers` (~852), and `hit`/`leftTo`, which use `archivedCopy`;
  - `archiveTranscript` (~692);
  - `pushOne` (~954, the per-reply hook path).
- Test: `cmd/claude-sessions/zip_test.go` (new). Use the two-machine helpers in `clean_test.go` (`machine`, `continuedOnWin`, `archived`).

**Interfaces:**
- Consumes: `convzip.Find`, `convzip.Entries`, `convzip.Materialize`, `convzip.Unzip`, `convzip.Path`.
- Produces:
  - `func zippedCopy(dest, bucket, id string) string`: the zip path, or "". Same name checks as `archivedCopy`.
  - `sessionHolders` also lists buckets holding `<id>.conv.zip`.
  - `held.covers` compares against a materialized copy when the other bucket's copy is zipped.

- [ ] **Step 1: Write the failing tests** in `cmd/claude-sessions/zip_test.go`:
  - `TestPushLeavesAnUnchangedZippedCopy`:
    - push once, then `convzip.Zip` the archived copy;
    - push again: the archive still holds only the zip (no loose `.jsonl`), and the log line counts it as unchanged.
  - `TestPushContinuesAZippedConversation`:
    - zip the archived copy, then append two turns locally, then push;
    - the archive holds a loose transcript with every turn, plus the folder files from the zip, and no zip.
  - `TestHookPushContinuesAZippedConversation`: the same, through `pushOne` (the `--session` hook path).
  - `TestHeldCheckReadsAZippedCopy`:
    - with `continuedOnWin`, zip the Mac bucket's copy;
    - the Windows machine's push still leaves its imported copy alone (no new `.jsonl` in its bucket), and `held.json` records it.

- [ ] **Step 2: Run and see them fail.** `go test ./cmd/claude-sessions/ -run 'Zipped|HeldCheckReadsAZipped' -v`. Expected: FAIL. A loose copy gets re-uploaded beside the zip, or the Windows push archives the session again.

- [ ] **Step 3: Implement.**
  1. Add `zippedCopy`.
  2. Extend `sessionHolders`.
  3. In `archiveTranscript`, before `dstInfo` is used:
     - if `zippedCopy` exists and the zip's `<id>.jsonl` entry has the local file's size and modification time (`archive.SameMTime`), return `notCopied`;
     - otherwise `convzip.Unzip` first, then fall through to the existing rules unchanged.
  4. Do the same in `pushOne`.
  5. In `covers`, `hit` and `leftTo`, use `convzip.Materialize` into a run-scoped temp dir when the other copy is zipped.

- [ ] **Step 4: Run the tests.** `go test ./cmd/claude-sessions/ -v`, the whole package, since existing push, clean and fork tests must stay green. Expected: PASS.

- [ ] **Step 5: Commit.** `git commit -am "Push: read zipped copies; unzip one before a continued conversation is archived"`

---

### Task 3: import, pull and restore read zipped copies

**Files:**
- Modify:
  - `cmd/claude-sessions/restore.go`: `restoreInto` (~142), the transcript list, and where each transcript's `<id>/` folder is resolved;
  - `cmd/claude-sessions/import.go`: `hasTranscripts` (~525), `newestCwd` (~503), the `restoreInto` call sites (~375, ~657);
  - `restoreSessionFiles` (~963), only if it resolves folders from `source` rather than from the transcript's directory.
- Test: `cmd/claude-sessions/zip_test.go`

**Interfaces:**
- Consumes: `convzip.Find`, `convzip.Materialize`.
- Produces:
  - `restoreInto` takes, besides the loose transcripts in `source`, every zipped conversation in `source` with no loose transcript. It materializes each into a temp dir that is removed before it returns. Each transcript's folder is resolved beside that transcript, not as `source/<id>`.
  - `hasTranscripts(dir)` is true for a bucket holding only zips. `newestCwd(dir)` considers zipped transcripts: the newest by entry modification time, read through `Materialize`.

- [ ] **Step 1: Write the failing tests:**
  - `TestImportRestoresAZippedConversation`:
    - machine A pushes, and its copy is zipped; machine B imports;
    - B's local bucket holds the transcript with every turn, its subagent file and its tool-output file, with paths rewritten as for a loose copy (assert the rewritten cwd);
    - the archive still holds only the zip.
  - `TestImportFindsABucketOfOnlyZips`: every conversation of a bucket is zipped. `import --dry-run` lists the bucket and its placement.
  - `TestImportUpdateFromAZippedCopy`: B holds an older local copy, and A's longer copy is zipped. `import --update` brings the new turns.
  - `TestPullReadsZips`: `pull` restores a zipped conversation.

- [ ] **Step 2: Run and see them fail.** `go test ./cmd/claude-sessions/ -run 'Zipped|OnlyZips|PullReadsZips' -v`. Expected: FAIL ("no .jsonl transcripts", or the conversation is missing).

- [ ] **Step 3: Implement** as in Produces. Keep every existing check on a transcript (name, regular file, `IsTranscriptID`). They apply to the materialized path as well.

- [ ] **Step 4: Run the tests.** `go test ./cmd/claude-sessions/ -v`. Expected: PASS.

- [ ] **Step 5: Commit.** `git commit -am "Import, pull and restore: read zipped conversations"`

---

### Task 4: render and doctor see zipped copies

**Files:**
- Modify:
  - `internal/render/render.go`: `Archive` (~82): bucket walk, state, listings;
  - `cmd/claude-sessions/doctor.go`: the "archived" check (~380) and the "html" page count.
- Test: `internal/render/render_test.go`, `cmd/claude-sessions/zip_test.go`

**Interfaces:**
- Consumes: `convzip.Find`, `convzip.Entries`, `convzip.Materialize`.
- Produces:
  - **`render.Archive` treats a zipped conversation as present.**
    - Its page is not removed.
    - It is listed in the project's `index.html`, the shared `html/index.html` and `INDEX.md`.
    - Its render state uses the zip's transcript entry size and modification time.
    - It is rendered again, via `Materialize`, only when the template version or the exclusion patterns changed. It counts toward the quiet-run budget like any render.
  - **`doctor`:**
    - "archived" accepts a session whose copy is a zip whose transcript entry matches the local file's size and modification time;
    - "html" counts zipped conversations as transcripts.

- [ ] **Step 1: Write the failing tests:**
  - `TestRenderKeepsAndListsZippedPages` (render package):
    - render, zip one conversation, then render again;
    - its page file still exists, and is byte-identical;
    - the project `index.html` still links it;
    - `Result` reports it up to date, not removed.
  - `TestRenderRerendersZippedOnTemplateBump`: after zipping, bump the template version through the existing test hook (or the state's version field). The page is rewritten and the zip is unchanged.
  - `TestDoctorCountsZippedAsArchived` (cmd): zip a pushed copy. `doctor` reports no "archived" failure, and "html  N page(s) for N transcript(s)".

- [ ] **Step 2: Run and see them fail.** `go test ./internal/render/ ./cmd/claude-sessions/ -run 'Zipped' -v`. Expected: FAIL (page removed, or session reported not archived).

- [ ] **Step 3: Implement** as in Produces.

- [ ] **Step 4: Run the tests.** `go test ./internal/render/ ./cmd/claude-sessions/ -v`. Expected: PASS.

- [ ] **Step 5: Commit.** `git commit -am "Render and doctor: zipped conversations keep their pages and count as archived"`

---

### Task 5: `ls`, `search`, `stats --archived` and `clean` with zipped copies

**Files:**
- Modify:
  - `internal/claude/bucket.go`: `ListBuckets` (~141), or the `--archived` path in `cmd/claude-sessions/project.go` (`sessionsDir`), whichever keeps local listing untouched;
  - `cmd/claude-sessions/ls.go`, `search.go`, `stats.go` as needed;
  - `cmd/claude-sessions/clean.go`: `planClean` (~108).
- Test: `cmd/claude-sessions/zip_test.go`

**Interfaces:**
- Consumes: `convzip.Find`, `convzip.Materialize`, `zippedCopy` (Task 2).
- Produces:
  - **With `--archived`,** `ls`, `search` and `stats` include zipped conversations. They read each through `Materialize`, one at a time, into a temp dir removed as they go. Local listing (no `--archived`) is unchanged.
  - **`planClean` skips any session with a zipped copy:** it is never deleted or kept for. This is the plan's call: duplicates are already prevented by `skipsHeld`, so this is the safe, simple rule. A zipped copy is never deleted by `clean`.

- [ ] **Step 1: Write the failing tests:**
  - `TestLsArchivedListsZipped`: `ls --archived` lists the zipped conversation's ID and first prompt.
  - `TestSearchArchivedFindsTextInZipped`: `search --archived <word only in the zipped transcript>` finds it.
  - `TestStatsArchivedCountsZipped`: the session count includes it.
  - `TestCleanLeavesSessionsWithAZippedCopy`:
    - two buckets hold the same session, one loose and one zipped, the loose one held in whole by the zipped one;
    - `clean --yes` deletes nothing, and both files remain.

- [ ] **Step 2: Run and see them fail.** `go test ./cmd/claude-sessions/ -run 'Archived.*Zipped|CleanLeavesSessionsWithAZipped' -v`. Expected: FAIL.

- [ ] **Step 3: Implement** as in Produces.

- [ ] **Step 4: Run the tests.** `go test ./... -v 2>&1 | tail -30`. Expected: PASS.

- [ ] **Step 5: Commit.** `git commit -am "ls, search, stats --archived read zipped conversations; clean leaves them alone"`

---

### Task 6: push zips idle conversations (behind the gate)

**Files:**
- Modify:
  - `internal/archive/manifest.go`: `Shard` (~102) gains `ReadsZips bool \`json:"readsZips,omitempty"\``. Add `func Holdouts(dest string, has func(Shard) bool) ([]string, error)`.
  - `internal/archive/config.go`: `Config` gains `ZipSessions *bool` (nil means on).
  - `cmd/claude-sessions/push.go`: `runPush`. Set `shard.ReadsZips = true`, and add the zipping step after archiving and before render.
  - `cmd/claude-sessions/config.go`: `config zip-sessions on|off`.
  - `cmd/claude-sessions/doctor.go`: a "zips" line.
  - `README.md` (a "Zipped conversations" subsection under how the archive is laid out), `CHANGELOG.md` `[Unreleased]`.
- Test: `internal/archive/manifest_test.go` (or `archive_test.go`), `cmd/claude-sessions/zip_test.go`

**Interfaces:**
- Consumes: `convzip.Zip`, `convzip.ErrTooLarge`, `convzip.Find`.
- Produces:
  - `archive.Holdouts`: the machine names of shards for which `has` is false. A non-nil error when `ReadShards` fails or reports bad files, which the caller treats as "not allowed".
  - `const zipIdle = 30 * 24 * time.Hour` in `push.go`.
  - `func zipIdleCopies(root, dest string, buckets []string, now time.Time, budget time.Duration, dryRun bool, logf func(string, ...any)) (zipped int, raw, packed int64)`.
- **Rules:**
  - **Gate and switch:** it runs only when `Holdouts(dest, func(s) bool { return s.ReadsZips })` is empty with no error, and `ZipSessions` is not off.
  - **Not on the hook:** never when `opts.FromHook`.
  - **Whose buckets:** only buckets this machine's shard lists.
  - **A copy is idle** when:
    - every file of the loose copy has a modification time at or before `now - zipIdle`, and none is after `now`;
    - and this machine's local transcript, if it has one, has a modification time at or before `now - zipIdle`.
  - **Budget:** when `opts.Quiet`, stop at `quietRenderBudget`, and what is left waits for the next run.
  - **Over the cap:** an `ErrTooLarge` conversation is remembered in `session-sync-base/zip-skipped.json` (key: bucket/id plus size). It is not tried again until its size changes. It logs once.
  - **Logging:** one log line per run when something was zipped: `ZIP: %d conversation(s), %s -> %s`.
  - **Dry run:** lists `would zip` lines instead.

- [ ] **Step 1: Write the failing tests:**
  - `TestHoldouts` (archive): shards A with and B without the flag give `["B"]`; a bad shard file gives an error.
  - `TestPushZipsIdleConversations`:
    - push, then set the archive copy's and the local file's modification times to 31 days ago, then push again;
    - `<id>.conv.zip` exists, the loose transcript and folder are gone, and the log has a `ZIP: 1 conversation(s)` line.
  - `TestPushKeepsRecentLoose`: at 29 days, nothing is zipped.
  - `TestIdleIgnoresFutureMTime`: an archive file dated in the future means the conversation is not zipped.
  - `TestZipWaitsForOlderShard`:
    - a second shard without `readsZips` means nothing is zipped;
    - `doctor` prints that zipping waits for that machine, and suggests updating it or `machines forget`.
  - `TestZipSessionsOff`: `config zip-sessions off` means nothing is zipped, and existing zips are still read.
  - `TestHookPushNeverZips`: `push --session <id>` (`FromHook`) zips nothing.
  - `TestPushLeavesOverCapLooseQuietly`: with the cap lowered, two pushes give one log line in total, and the copy stays loose.
  - `TestDryRunZipsNothing`: `push --dry-run` prints `would zip`, and the archive is unchanged.

- [ ] **Step 2: Run and see them fail.** `go test ./internal/archive/ ./cmd/claude-sessions/ -run 'Holdouts|Zip|Idle' -v`. Expected: FAIL.

- [ ] **Step 3: Implement** as in Produces and Rules. Then write the README subsection, covering:
  - what is zipped and when;
  - that pages stay readable;
  - how to open one by hand (any unzip tool);
  - `config zip-sessions off`;
  - the gate.

  Add a CHANGELOG `### Added` entry.

- [ ] **Step 4: Run the tests.** `go test ./... && GOTOOLCHAIN=go1.21.13 go test ./...`. Expected: PASS.

- [ ] **Step 5: Commit.** `git commit -am "Push zips conversations idle for 30 days once every machine reads zips"`

---

### Task 7: retention settings, keep list and project identity

**Files:**
- Create: `internal/archive/retention.go`, `internal/archive/retention_test.go`
- Modify: `cmd/claude-sessions/config.go`. Add `keep`, `unkeep` and `keep-days`, and update its usage text.
- Create: `cmd/claude-sessions/projects.go`: the project matcher. Look first at what import uses to match buckets by remote or leaf (`internal/identity`: `SameRemote`, `Find`, `Resolve`), and reuse it.
- Test: `cmd/claude-sessions/prune_test.go` (new)

**Interfaces:**
- Produces (archive):
  - `type KeepEntry struct{ Session, Remote, Leaf string }` (exactly one set).
  - `type Retention struct{ KeepDays int \`json:"keepDays,omitempty"\`; Keep []KeepEntry \`json:"keep,omitempty"\` }`.
  - `const MinKeepDays = 90`.
  - `func RetentionPath(dest string) string`: `<dest>/retention.json`.
  - `func LoadRetention(dest string) (Retention, error)`: a missing file gives the zero value and no error. An unreadable or invalid file gives an error, and callers then delete nothing.
  - `func SaveRetention(dest string, r Retention) error`: atomic.
- Produces (cmd):
  - `func projectsOf(dest string) (map[string]archive.Project, error)`: wraps `archive.Merged`.
  - `func identityOf(p archive.Project) archive.KeepEntry`: `Remote` if set, else `Leaf` (lower-cased).
  - `func sameProject(a, b archive.Project) bool`: the same remote, or the same leaf (case-insensitive) when either has no remote.
  - `func matchProject(projects map[string]archive.Project, arg string) (archive.KeepEntry, []string, error)`. An absolute path matches the bucket whose `Path` equals it (`samePath`). A plain name matches buckets whose `Leaf` equals it (case-insensitive, exact). The result is every bucket `sameProject` with the match. When the name matches buckets of two or more different identities, it returns an error listing each one's buckets and paths. No match is an error.
  - `func kept(r archive.Retention, id string, buckets []string, projects map[string]archive.Project) bool`: true when the session ID is kept, or any of its buckets belongs to a kept identity.
- **Config commands:**
  - `config keep <id|path|name>` adds an entry, and prints the buckets it covers;
  - `config unkeep <same>` removes it;
  - `config keep` with no argument lists the entries;
  - `config keep-days <N|off>` refuses N < 90 with `keep-days must be at least 90`.

- [ ] **Step 1: Write the failing tests:**
  - `TestRetentionRoundTrip`: save, then load, gives equal values. A missing file gives the zero value. Invalid JSON gives an error.
  - `TestMatchProjectAcrossMachines`:
    - buckets `-Users-you-dev-web-app` (Leaf `web-app`, no remote) and `e--web-app` (Leaf `web-app`);
    - `matchProject("web-app")` returns both and `KeepEntry{Leaf:"web-app"}`.
  - `TestMatchProjectByRemote`: two buckets with the same remote and different leaves match together through either path.
  - `TestMatchProjectAmbiguous`: two unrelated repositories called `api` (different remotes) give an error naming both.
  - `TestMatchProjectIsNotSubstring`: `matchProject("app")` fails when only `web-app` exists.
  - `TestKeptCoversEveryMachinesBucket`: keeping by name from one machine's path keeps a session whose only copy is in the other machine's bucket.
  - `TestConfigKeepDaysMinimum`: `config keep-days 30` fails, `config keep-days 365` saves, `config keep-days off` saves 0.

- [ ] **Step 2: Run and see them fail.** `go test ./internal/archive/ ./cmd/claude-sessions/ -run 'Retention|MatchProject|Kept|KeepDays' -v`. Expected: FAIL.

- [ ] **Step 3: Implement** as in Produces.

- [ ] **Step 4: Run the tests.** Same command. Expected: PASS.

- [ ] **Step 5: Commit.** `git commit -am "Retention settings: a keep list by conversation or project, and keep-days"`

---

### Task 8: deleted list and the `prune` command

**Files:**
- Create: `internal/archive/deleted.go`, `internal/archive/deleted_test.go`, `cmd/claude-sessions/prune.go`
- Modify:
  - `cmd/claude-sessions/main.go`: dispatch `prune`, and help text;
  - `internal/archive/manifest.go`: `Shard` gains `HonorsDeleted bool \`json:"honorsDeleted,omitempty"\`` (set by push in Task 9);
  - `cmd/claude-sessions/push.go`: `writeIndex` (~1133) and the session index rows leave out IDs on the deleted list.
- Test: `cmd/claude-sessions/prune_test.go`

**Interfaces:**
- Produces (archive):
  - `type DeletedEntry struct{ ID string; Buckets []string; LastUUID string; LastAt, DeletedAt time.Time; Undone bool }`.
  - `func DeletedDir(dest string) string`: `<dest>/deleted`.
  - `func LoadDeleted(dest string) (map[string]DeletedEntry, error)`: the union of `deleted/*.json`, keyed by lower-cased ID. Per ID, the entry with the latest `DeletedAt` wins (an undo is an entry with `Undone: true`). Files that are not `<valid machine name>.json` are ignored. An unreadable file gives an error.
  - `func RecordDeleted(dest, machine string, add []DeletedEntry) error`: appends to its own file, atomically.
- Produces (claude): `func LastTurnTime(path string) (time.Time, bool)`: the `timestamp` of the last user, assistant or system line, the same types `Compare` counts. Put it in `internal/claude/fork.go`, beside `LastUUID`.
- Produces (cmd):
  - `type pruneSel struct{ OlderThan time.Duration; Projects, IDs []string }`.
  - `type pruneItem struct{ ID, Project string; Buckets []string; LastAt time.Time; Prompt string; Size int64 }`.
  - `type prunePlan struct{ Remove []pruneItem; Kept int; Holdouts []string; Unread string }`.
  - `func planPrune(dest string, sel pruneSel, ret archive.Retention, now time.Time) (prunePlan, error)`.
  - `func deleteConversation(dest string, it pruneItem) error`. For every bucket in `it.Buckets`, it removes the loose transcript, the `<id>/` folder, the zip, the page `html/<bucket>/<id>.html`, and `forks/<bucket>/<id>*`. Folders first, transcript and zip last, as in `clean`'s `delete`. It never follows a link.
- **Command:**
  ```
  claude-sessions prune [--older-than <N>d] [--project <name|path>]... [<id>...] [--yes]
  claude-sessions prune --undo <id>...
  ```
- **Rules:**
  - **Selectors:** with none, the error is `prune needs --older-than, --project or a conversation id`. Different kinds combine with AND; several of one kind with OR.
  - **Age:** time since the newest of each copy's last turn, using:
    - `LastTurnTime` for a loose copy;
    - the transcript entry's modification time for a zipped copy, read from `convzip.Entries`, never decompressed (the plan's call: always at or after the last turn, so the conversation can only look younger);
    - the forks' times as well;
    - this machine's local copy, if it has one (`LastTurnTime`).

    A conversation with no time at all is never selected by `--older-than`.
  - **Keep list:** a conversation that `kept` covers is counted in `Kept` and never removed.
  - **Listing:** without `--yes`, list each item as `project  last-message-date  size  prompt(60 chars)`, then the totals, then `N kept (on the keep list)`, then `Run with --yes to delete them`.
  - **With `--yes`:**
    - take the local lock (`archive.AcquireLockWait`, 1 minute);
    - refuse when `Holdouts(HonorsDeleted)` is not empty, or a manifest is unreadable, naming the machines;
    - record each `DeletedEntry` (last turn's UUID and time from the newest copy) **before** deleting it;
    - delete, print the freed size, and add the Drive-trash note that `clean` prints.
  - **`--undo`** records `Undone: true` entries, and prints that any computer which still has the conversation uploads it on its next push.

- [ ] **Step 1: Write the failing tests:**
  - `TestDeletedUnion`: machine A deletes X; machine B later undoes X; machine A deletes Y. `LoadDeleted` gives X undone and Y deleted.
  - `TestLastTurnTime`: it skips trailing metadata lines and returns the last turn's timestamp. With no turns it returns false.
  - `TestPruneNeedsASelector`: `prune` with no arguments fails with the exact message, and nothing changes.
  - `TestPruneListsWithoutYes`: `prune --older-than 100d` lists the old conversation and changes nothing.
  - `TestPruneDeletesEveryCopy`:
    - a conversation with a loose copy in bucket A, a zipped copy in bucket B, a page and a fork;
    - `prune <id> --yes` removes all of them, leaves other conversations byte-identical, and records a `DeletedEntry` with `LastUUID`.
  - `TestPruneSelectorsAnd`: `--older-than 100d --project web-app` picks only old `web-app` conversations.
  - `TestPruneHonoursKeepList`: a kept project's old conversation is listed as kept, not deleted.
  - `TestPruneWaitsForOlderShard`: a shard without `honorsDeleted` means `--yes` deletes nothing, and the output names the machine.
  - `TestPruneSkipsUndatable`: a transcript with no timestamps and a future modification time is not selected by `--older-than`.
  - `TestPruneUndo`: after `--undo`, `LoadDeleted` reports it undone.
  - `TestIndexLeavesOutPruned`: after prune and push, `INDEX.md` and `index/<machine>.json` do not list it.

- [ ] **Step 2: Run and see them fail.** `go test ./internal/archive/ ./internal/claude/ ./cmd/claude-sessions/ -run 'Deleted|LastTurnTime|Prune|IndexLeavesOutPruned' -v`. Expected: FAIL.

- [ ] **Step 3: Implement** as in Produces and Rules.

- [ ] **Step 4: Run the tests.** Same command, then `go test ./...`. Expected: PASS.

- [ ] **Step 5: Commit.** `git commit -am "prune: delete chosen conversations from the archive, recorded so they stay deleted"`

---

### Task 9: push honours the deleted list; the age rule; docs

**Files:**
- Modify:
  - `cmd/claude-sessions/push.go`:
    - `runPush` sets `shard.HonorsDeleted = true`, loads `LoadDeleted` once, skips pruned sessions in the loop before `archiveTranscript`, and runs the age rule;
    - `pushOne` skips pruned sessions too.
  - `cmd/claude-sessions/doctor.go`:
    - "archived" does not report pruned sessions;
    - a "prune" line shows the keep-days setting, the last run and the holdouts.
  - `README.md`: a "Deleting old conversations" section covering `prune`, keep list, keep-days, undo, the gate and the Drive trash. `CHANGELOG.md` `[Unreleased]`.
  - `docs/setup-with-claude.md`: one rule line, "Never run `prune --yes` or set `keep-days` unless the user asked for it."
- Test: `cmd/claude-sessions/prune_test.go`

**Interfaces:**
- Consumes: `archive.LoadDeleted`, `claude.LastTurnTime`, `planPrune`, `deleteConversation`, `archive.LoadRetention`, `archive.Holdouts`.
- Produces:
  - `func prunedHere(d map[string]archive.DeletedEntry, id, path string) bool`. True when the ID has a non-undone entry, and the local transcript's `LastTurnTime` is not after `LastAt`. A local copy with no turn time counts as not newer.
  - `func runAgeRule(root, dest string, ret archive.Retention, now time.Time, logf func(string, ...any)) (deleted int, err error)`.
    - **When:** only when `ret.KeepDays >= archive.MinKeepDays`, not `FromHook`, not `DryRun`, and the last run recorded in `session-sync-base/prune-last.json` was 24 h or more ago.
    - **What:** exactly `planPrune(dest, pruneSel{OlderThan: KeepDays days}, ret, now)`, then the `--yes` path, including the gate and the lock already held by push.
    - **Log:** one line per conversation: `PRUNE: %s (%s, last message %s, %s)`.
- **A continued conversation is archived again:** when `prunedHere` is false for an ID on the list, push archives it as usual. From then on, the entry no longer matches, because the local copy is newer.

- [ ] **Step 1: Write the failing tests:**
  - `TestPushSkipsPruned`: prune X, then push from the machine that still has X locally. No copy of X appears in the archive, and doctor reports no "archived" failure.
  - `TestPushArchivesAPrunedConversationContinued`: prune X, append a turn locally, push. X is archived again.
  - `TestPushAfterUndoArchivesAgain`: `prune --undo X`, then push. X is archived again.
  - `TestAgeRuleDeletesOldOnce`:
    - `config keep-days 90`, one conversation 120 days old and one 10 days old;
    - push deletes only the old one and logs one `PRUNE:` line;
    - a second push within 24 h does not run the rule (assert through `prune-last.json`).
  - `TestAgeRuleOffByDefault`: with no `retention.json`, nothing is deleted.
  - `TestAgeRuleWaitsForOlderShard`: a holdout shard means nothing is deleted, and doctor names it.
  - `TestAgeRuleReadsNoZipContent`: with a test hook counting `convzip.Materialize` calls, an age-rule run over zipped conversations makes zero calls.
  - `TestHookPushRunsNoAgeRule`: `push --session` never deletes.

- [ ] **Step 2: Run and see them fail.** `go test ./cmd/claude-sessions/ -run 'Pruned|AgeRule|AfterUndo' -v`. Expected: FAIL.

- [ ] **Step 3: Implement** as in Produces. Then write the README section and the CHANGELOG `### Added` entry. The CHANGELOG entry's upgrading note says to update every computer and push once before zipping or pruning starts. Add the setup-page rule line.

- [ ] **Step 4: Run the tests.** `go test ./... && GOTOOLCHAIN=go1.21.13 go test ./...`. Expected: PASS.

- [ ] **Step 5: Commit.** `git commit -am "Push honours pruned conversations; optional keep-days age rule; docs"`

---

### Task 10: several computers (matrix test and same-name warning)

Added 2026-11-25 with the owner's approval. See the spec addendum "several computers".

**Files:**
- Create: `cmd/claude-sessions/multi_test.go`
- Modify:
  - `internal/archive/config.go`: `Config` gains `InstallID string \`json:"installId,omitempty"\``;
  - `internal/archive/manifest.go`: `Shard` gains `InstallID string \`json:"installId,omitempty"\``;
  - `cmd/claude-sessions/push.go`: create the ID on the first push, write it into the shard, warn on a mismatch;
  - `cmd/claude-sessions/doctor.go`: a failure on a mismatch;
  - `README.md`: a "Several computers" note;
  - `CHANGELOG.md`.

**Interfaces:**
- Consumes: the test helpers in `clean_test.go` (`machine`, `archived`, `later`) and everything Tasks 1-9 built.
- Produces:
  - `func installID(root string) string`: reads `Config.InstallID`, or creates 16 random hex bytes (`crypto/rand`) and saves them.
  - `func nameClash(dest, machine, id string) bool`: true when `manifest/<machine>.json` exists with a non-empty `InstallID` other than `id`.
  - The message, exactly: `Another computer also uses the name %s. Give one of them its own name with ` + "`claude-sessions config set-machine`."

- [ ] **Step 1: Write the failing tests** in `multi_test.go`, using four simulated machines as in the spec addendum (`CLAUDE_SESSIONS_MACHINE`, separate claude roots, transcripts whose cwd is each machine's own path):
  - `TestFourComputersFileEachOthersWork`: every machine's conversation lands in every other machine's own bucket, with the cwd rewritten (assert one per pair: Windows A to B across drives, Windows to Linux, Linux to Windows, Mac to Linux).
  - `TestFourComputersArchiveEachConversationOnce`: after every machine imports and pushes twice, each session ID has exactly one archived copy, except real forks.
  - `TestConversationContinuedOnThreeComputers`: no turn is lost, and the forks are kept.
  - `TestTwoWindowsShareABucket`: `E:\api` on A and B are one bucket. Distinct conversations coexist, and one continued on both becomes a fork.
  - `TestTwoWindowsSameWorkFolder`: `E:\reports` is archived under each machine. An import never replaces a newer local file.
  - `TestFourComputersGateAndForget`: one machine without `readsZips` means nothing is zipped. After `machines forget` it zips. The same holds for `honorsDeleted` and prune.
  - `TestKeepByRemoteCoversFourBuckets` and `TestPruneRemovesEveryBucketsCopy`.
  - `TestCleanWithFourShards`: only true duplicates are deleted.
  - `TestSameNameWarns`: two roots with the same `CLAUDE_SESSIONS_MACHINE` and different install IDs. The second push logs the WARN and doctor fails with the exact message. With one root pushing twice, there is no warning.

- [ ] **Step 2: Run and see them fail.** `go test ./cmd/claude-sessions/ -run 'FourComputers|ThreeComputers|TwoWindows|KeepByRemoteCoversFour|PruneRemovesEvery|CleanWithFour|SameNameWarns' -v`. Expected: the name-clash test fails (no install ID yet). Any other failure is a real multi-computer bug: fix it in the code that owns it, and say so in the report.

- [ ] **Step 3: Implement** the install ID and the warning as in Produces. Then the README note covering:
  - every computer uses the same archive folder;
  - names must differ, and `config set-machine` sets one;
  - retire a computer with `machines forget`;
  - Linux uses a systemd user timer or cron.

  Add a CHANGELOG entry.

- [ ] **Step 4: Run the tests.** `go test ./... && GOTOOLCHAIN=go1.21.13 go test ./...`. Expected: PASS.

- [ ] **Step 5: Commit.** `git commit -am "Several computers: a four-machine test, and a warning when two use the same name"`

---

## Calls this plan makes (beyond the spec)

- **Zip naming:** `<id>.conv.zip`.
- **File names:** archive settings in `retention.json`, the deleted list in `deleted/<machine>.json`, and local state in `zip-skipped.json` and `prune-last.json`.
- **Shard flags:** `readsZips` and `honorsDeleted`.
- **`clean` never touches a session with a zipped copy.** Duplicates are already prevented. Deleting zipped duplicates is left for later if they ever appear.
- **A zipped copy is dated by its transcript entry's modification time,** without decompressing. This is always at or after the last turn, so a conversation can only look younger.
- **Zipping is time-budgeted in quiet runs** like rendering, and never runs from the per-reply hook.
