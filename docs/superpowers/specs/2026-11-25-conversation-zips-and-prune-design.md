# Conversation zips and archive pruning: design

Date: 2026-11-25. Status: approved by the owner 2026-11-25.

## Why

The archive only ever grows. On one real two-machine archive, conversations took
4.2 GB after `clean`. September added 0.5 GB, October 0.8 GB, and November 3.1 GB. At
November's pace that is about 35 GB a year, against a free Google account's 15 GB,
which is shared with mail and photos. Most of it is conversations nobody will open
again, but nobody can say in advance which ones.

Two parts, built in this order:

1. **Conversation zips (lossless).** Each archived conversation that has been idle for
   30 days becomes one zip.
2. **Pruning (optional, lossy).** Delete archived conversations by hand or by an age
   rule. Deletion applies to the archive only, with a keep list that is never pruned.

A third part, a browser panel for managing everything the tool does, comes after these
and has its own design. These two parts must leave everything the panel will need
callable from the command line.

## Measurements behind the numbers

On the same archive:
- **Text compresses about 3.6x** with deflate.
- **Embedded screenshots** (base64 image data inside transcripts) are 17% of the bytes
  and barely compress.
- **Expected size:** about 4.2 GB becomes about 1.5 GB once everything is past the
  30-day mark.
- **Saving today:** 3.05 GB of the 4.32 GB is younger than 30 days, so today's saving is
  about 0.9 GB. The full ratio applies as conversations age.
- **File count:** the conversations folder holds about 3,700 files (transcripts,
  subagent transcripts, tool output) for 203 conversations. Zips make that about one
  file per idle conversation. File count is what made a first import over a streaming
  Google Drive crawl.

## Goals

- **Zips:** an idle conversation takes about a third of the space and one file, with
  nothing lost.
- **Readers see zipped conversations as if they were loose.** That means every command
  that reads archived conversations: `import`, `pull`, `restore`, `ls`, `search` and
  `stats --archived`, `clean`, `doctor`, push's held check, and the rendered project
  listings.
- **Pages stay loose.** A zipped conversation's page stays loose, so old conversations
  can still be read in the sync client's web view.
- **Pruning:** the owner can delete chosen conversations from the archive. Deleted
  conversations stay deleted: no computer uploads them again unless it continues them.
- **Age rule:** optional and off by default. It does the same deletion on its own.
- **Keep list:** conversations or whole projects that neither kind of deletion ever
  touches.

## Non-goals

- Deleting anything on a computer (`~/.claude`). Claude Code's own `cleanupPeriodDays`
  governs that.
- Zipping forks, memory, profile files, work folders or pages.
- Removing screenshots or any other content from conversations.
- Grouping several conversations into one zip.
- The browser panel. It is the next design, and machine-readable output for it is
  decided there.

## Anti-goals (failures even if it works)

- **Losing a turn:** a turn of any transcript, a subagent transcript or a tool-output
  file is lost, at any point, including a run killed half way.
- **Resurrection:** a computer on an older version uploads again what was zipped or
  deleted.
- **Unseen deletion:** a typo, a missing selector or an unreadable manifest deletes
  conversations the owner did not see listed.
- **Unreadable for older versions:** a computer on an older version imports a broken or
  partial conversation from a zip it does not understand. It should simply not see the
  conversation.

## Constraints

- Go 1.21 minimum, standard library only (`archive/zip`, deflate), one binary. CI runs
  on Linux, macOS and Windows.
- **The archive is untrusted.** Zip entries get the same protections as work packs in
  `internal/work/pack.go`:
  - the tool walks the central directory itself before parsing;
  - size caps;
  - entry names checked with `SafeJoin`;
  - each entry checked before it is written.
- Writes are atomic (temp, then rename), as everywhere in `internal/archive`.
- Google Drive keeps a version for each upload and keeps deleted files in its trash for
  30 days. A zip is written once, not rewritten repeatedly.
- **The repository is public.** No real machine, user, project or employer names in code,
  tests or docs.
- Older machines are handled with flags in the shards (`Shard.SkipsHeld` is the
  precedent).
  - **The gate is the whole archive** (decided after review): zipping and deleting run
    only when *every* shard in the archive has the flag, not only the shards listing
    that bucket.
  - **Why the whole archive:** an older machine that pushes the same project to its own,
    differently named bucket would otherwise undo the work in two ways:
    - its held check finds only loose `*.jsonl` copies, so it uploads again a
      conversation another bucket has zipped;
    - its render rewrites the shared listings without the zipped conversations.
  - **The cost:** a retired machine's shard stops zipping and deleting until it is
    updated or `machines forget` removes it. Both `doctor` and the refusal message must
    say exactly that, naming the machine.

## Approaches considered

1. **One zip per conversation: picked by the owner.** About 200 files instead of about
   3,700. Deleting or continuing a conversation touches only its own zip, so no overlay
   or rebuild rules are needed. Extracting one conversation by hand is easy.
2. **One zip per project per month,** like work packs. This gives the fewest files.
   But deleting one conversation, or continuing an old one, means rebuilding a zip that
   holds others. That needs the overlay and rebuild rules work packs have, and
   re-uploads the whole zip each time.
3. **Compress each file on its own (`.gz`).** The simplest to read, but the file count
   stays about 3,700.

The compression is the same for all three: zip compresses each entry separately.

## Part 1: conversation zips

**Unit.** One archived copy of a conversation in a bucket:
- `projects/<bucket>/<id>.jsonl`;
- everything under `projects/<bucket>/<id>/` (subagent transcripts, their meta files,
  tool output).

It becomes `projects/<bucket>/<id>.<ext>.zip`. The exact name is the builder's; it
must not end in `.jsonl`, so older builds never mistake it for a transcript. Entries
keep their relative paths, sizes and modification times.

**When.** Every file of the copy has been unchanged for 30 days. The archive keeps
source modification times, so this is the conversation's last activity. Zipping runs
during push, for copies in buckets this machine pushes to. A copy whose session is
still on this machine and changed in the last 30 days is not zipped.

**How (no turn lost).**
1. Write the zip to a temp file.
2. Read it back, checking every entry's size and CRC.
3. Check that its transcript and every subagent transcript hold all turns of the loose
   ones (`claude.HoldsAll`).
4. Rename it into place.
5. Delete the loose files, transcript first, then the folder. The builder's call
   after review: a transcript is never left without its subagent files.

A run killed at any point leaves loose files, a zip, or both.

**Both present.** Readers treat a file present loose as winning over the same entry in
the zip. Entries that exist only in the zip are still read from it. The next push
finishes the job.

**Continued after zipping.** A conversation is either loose or zipped, never both for
long. When push finds a local session whose archived copy is zipped, and the local copy
has changed since:
1. it unzips the copy back to loose files in the archive (atomic, with original
   modification times);
2. it deletes the zip;
3. the normal push rules run: extends, same, behind, fork, held.

It zips again after 30 more idle days. The cost is one extra upload of the
conversation's folder files when an old conversation is resumed, which is rare.

**Reading.**
- One helper finds and opens an archived copy, whether it is loose, zipped or both. It
  replaces `archivedCopy`, `sessionHolders` and the import and render walks that look
  only for `*.jsonl`.
- Code that needs a file path, such as `claude.Compare`, gets a temp extract.
- The helper's shape is the builder's choice.

**Pages.**
- A zipped conversation's page is not re-rendered or removed just because it was
  zipped. A template-version bump or a changed render exclusion still reaches it, by
  reading the zip.
- Project listings (`index.html`, `INDEX.md`) still list zipped conversations.
- `doctor`'s page count counts them.

**Switch** (my call; the owner did not ask for it): on by default;
`config zip-sessions off` stops new zipping. Existing zips stay readable.

**Older machines.** A new shard flag says the writer reads zips and does not re-upload a
zipped copy as missing. Zipping runs only when every shard in the archive has the flag
(see Constraints). An older build ignores the zip file, so the conversation is simply
absent for it.

**Pushing a session whose copy is zipped.** Push, the per-reply hook included, never
writes a loose transcript next to a zipped copy without first comparing it with the
zipped one.

**Output.** A dry run lists what would be zipped. The log gets one line per run, for
example `ZIP: 12 conversations, 340 MB -> 95 MB`. The wording is the builder's.

## Part 2: pruning

**Scope: the archive only.** Nothing on any computer is touched. A pruned conversation
lives on wherever a computer still has it locally.

**The command.** `claude-sessions prune`:
- **Selectors:** one or more of `--older-than <N>d`, `--project <name or path>`, and
  conversation IDs. With no selector it is an error, never "everything". Different
  kinds combine with AND. Several of one kind combine with OR.
- **Without `--yes`, it only lists:**
  - each conversation: project, date of last message, first prompt (truncated), and
    size including all copies;
  - then the totals;
  - then how many matching conversations were skipped because they are on the keep
    list.
- **With `--yes`:** it deletes what it listed, under the local lock, as `clean --yes`
  does.

**Age** is the time since the last message of the newest copy: across all buckets,
loose or zipped, forks included, and this machine's local copy if it has one. It is read
from the transcript's last turn timestamp, falling back to the modification time.

**What "a project" means** (decided after review; my call). The same project lives in
a different bucket on each computer when its path differs, for example `E:\web-app` and
`~/dev/web-app`. Keeping it on one computer must keep it on both, or the age rule run
by the other computer deletes that computer's copies.
- **Identity.** A project is every bucket that `import` already treats as the same
  project: the same git remote, otherwise the same folder name. The match is exact
  (case-insensitive), never a substring.
- **The keep list** stores that identity, the remote or the folder name, never a
  bucket name or a path. It therefore covers every computer's bucket, including ones
  that appear later.
- **`prune --project`** lists every bucket it matched, with the computer each belongs
  to. If the name matches two different projects, for example two unrelated repositories
  both called `api`, it refuses and lists them so the owner can use a path.
- **Mixed copies.** A conversation with a copy in any kept project counts as kept.

**What is deleted** for each conversation:
- every archived copy in every bucket, loose and zipped;
- each copy's own folder and page;
- its files under `forks/`.

Memory, profile and work folders are untouched.

**Deleted list.**
- **Files:** each machine records what it deleted in its own file, `deleted/<machine>.json`,
  and readers take the union. One file per machine avoids two machines overwriting a
  shared file through the sync client.
- **Entries:** conversation ID, bucket or buckets, last turn's ID and timestamp, and
  deletion time.
- **Push skips** a local session on the list unless it has a turn newer than the
  recorded last turn. A conversation continued after it was pruned is archived again.
  An older or equal copy is never uploaded again.

**Undo** (my call): `prune --undo <id>` takes a conversation off the deleted list, so
any computer that still has it uploads it on its next push. Drive's trash covers the
first 30 days anyway; this covers the rest, as long as a computer has the conversation.

**Keep list.**
- **Commands:** `config keep <conversation id | project>`, `config unkeep <same>`, and
  `config keep` alone, which lists it.
- **Storage:** in the archive, shared by every machine, because it is written rarely and
  only by the owner.
- **Effect:** `prune` and the age rule both skip anything on it. `prune` lists how many
  it skipped.

**Age rule.**
- **Setting:** `config keep-days <N | off>`. It is off by default and refuses N under 90.
  It is stored in the archive beside the keep list, so every machine follows one rule.
- **When it runs:** at most once a day per machine, during a full push (my call: not on
  every per-reply hook run). It deletes exactly what `prune --older-than <N>d --yes`
  would.
- **Log:** each deleted conversation gets a log line, for example
  `PRUNE: <id> (<project>, last message <date>, 4.1 MB)`.

**Older machines.** A second shard flag says the writer honours the deleted list. Both
`prune --yes` and the age rule refuse to delete while any shard in the archive lacks
it, and name the machines. With a manifest that cannot be read, nothing is deleted, as in `clean`.

## Left to the builder

- File names and formats (zip naming, the deleted list, the shared settings file),
  shard flag names, log wording.
- The reading helper's API, and whether zipped transcripts are streamed or extracted to
  temp.
- How `ls`, `search` and `stats` show zipped and pruned conversations.
- Two machines pushing to the same bucket could zip the same copy at once. Make it
  harmless: identical temp-and-rename, and deletion only after verification.
- Tests:
  - a kill at each step of zip and unzip;
  - a zipped conversation continued on the other machine;
  - an older-version shard holding back zips and deletion;
  - prune with no selector;
  - keep list precedence;
  - the age-rule minimum;
  - a crafted zip;
  - Windows paths.

## Addendum (2026-11-25): several computers

The owner uses three computers (a Mac and two Windows laptops) and may add a Linux
one. Only one Mac and one Windows laptop have been tested together. Added with the
owner's approval:

**1. A test with several computers.** One test file simulates four computers on one
archive:
- a Mac at `~/dev/web-app`;
- Windows A at `E:\web-app`;
- Windows B at `D:\work\web-app`, with the same git remote;
- Linux at `/home/you/src/web-app`, with the same git remote.

Windows A and B also share one identical path, a project `E:\api` and a work folder
`E:\reports`, so both push into the same archive bucket. The test checks:
- Each computer files every other computer's conversations under its own path for the
  project, with paths rewritten across drives, from Windows to POSIX and back, and from
  Mac to Linux.
- No computer archives an imported conversation again: the archive holds each
  conversation once.
- A conversation continued on two or three computers keeps every turn, as forks.
- In the shared bucket, different conversations sit side by side. One continued on both
  Windows laptops becomes a fork, not an overwrite.
- Each Windows laptop's `E:\reports` is archived separately. Bringing one into the other
  never replaces a newer local file (this pins today's rule).
- Zips and pruning wait for any one of the four that lacks the flag, and resume after
  `machines forget`. A project kept by remote is kept on all four. Pruning removes the
  copies in every bucket, and no computer uploads them again.
- `clean` with four shards deletes only true duplicates.

**2. A warning when two computers use the same name.** A computer's name defaults to
hostname-user. Two computers with the same name, such as cloned laptops, would overwrite
each other's manifest shard.
- **Install ID:** each install creates a random install ID on its first push and keeps it
  in `session-sync.json`. Push writes it into its shard.
- **The check:** when the archive's shard for this name carries a different install ID,
  push logs a WARN and `doctor` fails with this message: "Another computer also uses the
  name <name>. Give one of them its own name with `claude-sessions config set-machine`."
- **No refusal (my call):** push still writes. A reinstall on the same computer loses
  its ID, and that must not block backups. The warning clears once the names differ.

**3. A real run** on the second Windows laptop during rollout: install, the same archive
folder, `import`, and `install`.

Linux is already a supported platform. CI runs on ubuntu, and `install` uses a systemd
user timer or cron there. The test above covers its paths.
