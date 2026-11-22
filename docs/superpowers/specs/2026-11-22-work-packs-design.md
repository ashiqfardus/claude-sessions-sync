# Work packs: fewer files for the sync client to move

Status: draft for approval · 2026-11-22 · reviewed once by a fresh builder; calls marked (call) are the author's, made on the user's "do the best option"

## Why

First syncs of work folders were slow: about 8,000 files from one machine and 5,600 from
the other. The time went into per-file overhead, not bytes. Google Drive handles each
file separately. On a streaming (online-only) mount, the importing machine downloads
each file on first read, about one per second. Most of those files sit in dated run
folders (`SQL/po_e2e_20261121`, `fuzzy-stress/pkg-a5ee3db`) that stop changing after a
few days.

**Goal:** the archive holds far fewer files, and every edit is still backed up. A
normal edit uploads only what changed.

A second, small feature: `import --no-work` (and `pull --no-work`) brings
conversations without work files, for a quick switch.

## Goals
- A first-level subfolder of a work folder (a **unit**) that has not changed for
  **7 days** is stored in the archive as one zip, a **pack**.
- After packing, a new or changed file in that unit is uploaded as a normal loose file
  beside the pack, never by re-uploading the pack.
- A pack is rebuilt (**compacted**) only when changes pile up: loose changes over 25%
  of the pack's bytes, or over 50 files.
- On another machine, import and pull unpack into normal files. The user sees plain
  folders on every machine.
- `--no-work` skips work folders entirely.

## Non-goals
- Packing the files directly in a work folder (the loose-files unit) or exact files.
  They are few, and they stay loose.
- Compression tuning. Use plain `archive/zip` with Deflate.
- Deletion sync. The archive keeps every file, as today. A pack keeps files later
  deleted locally.
- Reading packs with older builds. They skip `.zip` files (a denied extension), so they
  simply don't see packed units. Both machines must run this version (CHANGELOG note).

## Anti-goals
- An edit is never lost or silently not backed up.
- Re-uploading a whole pack for a one-file change.
- A pack from the archive writing anything that a loose file could not. Packs are
  untrusted input, exactly like loose files.
- Zip bombs, path traversal through entry names, or symlink entries reaching disk.

## Approaches considered
1. **Re-zip on any change.** Simplest, but one edit re-uploads the whole pack.
   Rejected.
2. **Pack plus loose overlay, compacted when changes pile up.** Chosen: edits cost
   only their own size, and re-uploads are rare.
3. **Never revisit packed units.** No re-uploads ever, but later edits are silently
   not backed up. Rejected as data loss.

The user asked for the best option and the 7-day threshold.

## Design

### Archive layout
- A pack is `work/<machine>/<Encode(folder)>/<unit>.cspack.zip`, beside where the
  unit's loose folder lives.
- The overlay (changes after packing) is the unit's normal loose path,
  `work/<machine>/<Encode(folder)>/<unit>/<rel>`.
- `.zip` is a denied extension for work files, so a user file can never be mistaken for
  a pack.
- Entry names are `/`-separated paths relative to the unit, with mtimes stored in the
  zip headers. There is no manifest file; the zip's central directory is the index.

### Local record
`<claude-root>/session-sync-base/work-packs.json`, written atomically:
- for each folder, then each unit: the pack's file list with size and mtime;
- the overlay files uploaded since packing;
- the pack's byte size.

It lets push decide "changed since the pack" without reading the pack from the cloud
mount. If the record is lost, push reads the pack's central directory once, and the
mount downloads it once.

### Push
After `Collect` and the per-unit cap, for each non-loose unit of a non-exact folder:
1. **Already packed:** compare each collected file with the pack record. New or changed
   files (size differs, or mtime differs beyond the 2 s tolerance) are copied loose to
   the overlay, as today. Unchanged files are not copied.
2. **Compaction:** when the overlay exceeds 25% of the pack's bytes or 50 files, build
   a new pack:
   - all entries of the old pack (raw copy),
   - plus the overlay, with the newer file winning,
   - plus the current local files.

   Write it to a temp file in the same directory and rename it over the old pack. Only
   then delete that unit's overlay files, through the `realWorkDirs` guard.
3. **Not yet packed:** pack the unit when every collected file in it has an mtime older
   than 7 days and it has at least 5 files. Build the pack from LOCAL files, rename it
   into place, then remove that unit's existing loose copies under `work/<me>/` through
   the guard.
4. **Held files** (restored from another machine and unchanged) never go into a pack,
   just as they are never pushed loose.

Other rules:
- **Budget:** packing counts against the `--quiet` budget. A unit not finished is left
  for the next run, and nothing is half-written, because of temp+rename.
- **Dry run:** prints `would pack <unit> (N files, X MB)` and `would compact …`, and
  writes nothing.
- **Logging:** `WORK: packed <folder>/<unit> (N files)` and `WORK: compacted …`.
- **Shard:** `WorkFolder` keeps `Files` and `Bytes` as the totals backed up, and gains
  `Packed int` (the number of packed units).
- **Orphans:** `RemoveOrphanTemps` already covers `work/`, so it cleans up temp packs.

### Restore (import, pull)
In the restore walk of an archive work folder, a regular top-level file named
`*.cspack.zip` is a pack, not a work file. For each pack:
1. Run `realArchivePath` (real directories only), then Lstat (regular file), then
   `zip.OpenReader` with limits:
   - at most 20,000 entries;
   - each entry's declared and actual uncompressed size ≤ `MaxFileBytes`, read through
     `io.LimitReader`, and a CRC mismatch refuses the entry;
   - the sum ≤ `MaxFolderBytes`;
   - only regular-file entries (no symlinks, dirs or other modes);
   - entry names that are `filepath.IsLocal` after slash conversion: no `..`, no
     absolute or drive form, no empty name.
2. Treat each entry exactly like a loose file at `<unit>/<name>`: `Allowed`, the skip
   names, the R20/R21 code-folder rules, `SafeJoin`, the exclusions, add-only /
   `--update` / held, 0644, never through a link. Set its mtime from the entry header.
3. Then apply the unit's overlay as loose files, as today. An overlay file with the
   same relative path as a pack entry supersedes it: same restore rules, newer content
   wins.
4. Record restored pack entries in `work-held.json` like any restored file, so they are
   never pushed back.

`--dry-run` reports the counts and writes nothing.

### Exclusions and purge
- `exclude-folder` on a packed unit, or on a path inside one, removes that unit's pack
  and overlay from `work/<me>/` through the guard. The next push repacks the unit from
  local files, without the excluded path, once it qualifies.
- `purgeWork` treats `<unit>.cspack.zip` as part of the unit.

### `--no-work`
- `import --no-work` and `pull --no-work` skip `restoreWork` entirely: no files and no
  work moves.
- `--no-work` with `--repoint` is refused with a message, because repoint needs the
  work moves.

### Doctor
The work-folders line adds `, N packed unit(s)`.


## Decisions after the builder check (call)
1. **The first pack never destroys archive-only files.** The pack is built from local
   files. Afterwards, push deletes only the archive loose copies whose relative path
   IS in the new pack. Loose archive copies of files deleted locally stay loose and
   remain restorable. Compaction already keeps old entries.
2. **On restore, the overlay always wins.** For the same relative path, a loose
   overlay file replaces the pack entry, because it was written after the pack.
   Leftovers after a crash have the same content, so this is safe. Restore merges pack
   and overlay per relative path before writing anything. The add-only and `--update`
   rules then apply to the merged file.
3. **Push never makes a pack restore would refuse.** A unit with more than 20,000
   files, or a merged total over `MaxFolderBytes`, is not packed or compacted. It stays
   loose (or keeps its pack plus a growing overlay), and push logs one WARN.
4. **`exclude-folder` inside a unit.** A helper resolves a path to its (work-folder
   entry, unit), using the shard entries under the current and former machine names
   and the same case-fold rules. It removes that unit's pack, its overlay and its
   `work-packs.json` entry. The next push repacks at once if the unit still qualifies,
   without the excluded files.
5. **Budget.** The budget is checked before a unit starts. Every run finishes at least
   one unit it started, which may overrun the budget by that unit's build time. That
   is a few seconds for 250 MB.
6. **`--no-work` skips everything work-related:**
   - file restore;
   - work moves;
   - `byWorkFolder` project placement;
   - held and placed record updates;
   - the `--repoint` hint.

   It prints `work folders skipped (--no-work)`.
7. **Totals.** `Files` and `Bytes` count uncompressed content, the same as loose files.

## Testing
- **Pack creation:**
  - a unit whose files are all older than 7 days is packed;
  - its loose copies are removed;
  - a newer unit is not packed;
  - a unit with fewer than 5 files is not packed;
  - dry run writes nothing.
- **Overlay:**
  - after packing, editing one file uploads only that file loose;
  - a new file is uploaded loose;
  - the pack is untouched (same mtime and size).
- **Compaction:**
  - 51 changed files, or more than 25% of the bytes, triggers it;
  - the new pack has every old entry plus the changes;
  - the overlay is removed;
  - an interrupted compaction leaves the old pack valid.
- **Restore:**
  - a pack plus overlay unpacks to the right files, with the overlay winning;
  - mtimes are kept;
  - held entries are not pushed back.
- **Hostile packs**, each refused and nothing written outside the target:
  - an entry with `..`;
  - an absolute entry;
  - a symlink entry;
  - a dot entry;
  - an entry over 10 MB (declared or actual);
  - total over 250 MB;
  - more than 20,000 entries;
  - a CRC mismatch;
  - a `.pth` entry, a `node_modules/` entry, and a `.git/` entry.
- **Exclusions:** excluding a packed unit removes its pack and overlay.
- **`--no-work`:** no work files and no moves; refused with `--repoint`.
- **Lost record:** delete `work-packs.json`; push still detects changes by reading the
  pack.
- **Cross-OS:** packs made on one OS restore on another, using `/` entry names.

## Docs
- README: packing in "What is copied", `--no-work` in the flags, and a note that both
  machines need this version.
- DESIGN.md: one paragraph.
- CHANGELOG.

## Left to the builder
- Exact names of the helpers and of the record's JSON.
- Whether compaction reads the old pack with `zip.Writer.Copy`.
- Wording of the log and summary lines.
