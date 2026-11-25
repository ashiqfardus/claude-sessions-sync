# claude-sessions-sync

[![Release](https://img.shields.io/github/v/release/ashiqfardus/claude-sessions-sync)](https://github.com/ashiqfardus/claude-sessions-sync/releases/latest)
[![CI](https://github.com/ashiqfardus/claude-sessions-sync/actions/workflows/ci.yml/badge.svg)](https://github.com/ashiqfardus/claude-sessions-sync/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

Back up your [Claude Code](https://claude.com/claude-code) conversations to any synced
folder, read them as web pages, and carry on with them on another computer, even when
the project lives at a different path there.

```sh
brew install --cask ashiqfardus/tap/claude-sessions   # macOS or Linux
```

Windows uses [Scoop](#step-1-install-the-tool) or a download. Then follow the
[Quick start](#quick-start).

---

## Why you need this

Claude Code keeps every conversation as a file on your computer:

```
~/.claude/projects/<project>/<session-id>.jsonl
```

Three things go wrong with that:

1. **They exist only on that one computer.** No cloud copy, nothing on your phone.
2. **They get deleted.** Claude Code removes transcripts after `cleanupPeriodDays`,
   which defaults to **30** days.
3. **They are tied to a folder path.** The same project at `D:\work\api` on one
   computer and `~/dev/api` on another counts as two projects, so `claude --resume` on
   one cannot see the other's conversations, even if you copy the files by hand.

> **Status: tested on Windows, macOS and Linux.** CI runs the full test suite on all
> three, and on each does a real install, a hooked push, a push with the archive
> unreachable, and an uninstall that must leave every other setting alone. It also
> reports, without failing the build, whether the scheduled sweep registered on that
> runner. What CI cannot tell is whether the sweep
> survives a logout or a reboot on a real desktop, so
> [reports from real macOS and Linux machines](.github/ISSUE_TEMPLATE/platform_report.yml)
> are very welcome.

---

## What it does

**Backs up, automatically**
- Copies every session, its subagent conversations, saved tool output and the
  project's memory files to a folder you choose. Google Drive, OneDrive, Dropbox,
  iCloud, Syncthing, a NAS or a USB drive all work, because it only writes files.
- Also backs up your global rules (`CLAUDE.md` and the `.md` files beside it), personal
  skills, agents, commands, output styles, and which plugins are enabled.
- Backs up the **folders Claude worked in outside git**: a notes folder, a scripts
  folder, a project with no repository. Text-like files only; see
  [Work folders](#work-folders).
- Runs by itself once installed: when a session ends, **after every reply**, and on a
  sweep every 30 minutes.
- Copies only what changed, and never copies credentials. It deletes a conversation
  from the archive only when you ask, with `prune` or the `keep-days` rule; see
  [Deleting old conversations](#deleting-old-conversations).

**Brings history to another computer**
- `import` finds each project on the new computer even where paths differ: by a folder
  of the same name (told apart by **git remote** when there are several), or the home
  folder. Anything it cannot place for certain, a renamed folder for one, is reported,
  never guessed, and one command (`config map`) settles it.
- Rewrites the paths inside the conversations to the new computer's, including other
  repositories the session worked in, Git Bash paths, and `~/.claude`.
- `import --update` brings in sessions you continued on the other computer.
- **A session continued on both computers is kept twice, never merged or lost**: the
  other computer's copy arrives as a session of its own, and you resume whichever you
  want.
- The memory index (`MEMORY.md`) is merged entry by entry: what either computer
  added arrives, and what either deleted stays deleted. Other memory files take the
  newer copy.
- Work folders come too, filed under the matching folder on this computer, and the
  paths in the conversations are repointed to where they landed.
- Rules, skills and plugins come too. Plugins enabled on the other computer are
  installed for you. A rule or skill you delete, or a plugin you turn off, on this
  computer stays that way here.

**Lets you read and find old conversations**
- Writes readable HTML pages and an `INDEX.md`, with common credentials masked. You
  can withhold any project from being published.
- `search`, `ls` and `stats`, also over sessions Claude Code has already deleted
  locally (`--archived`).

**Tells you when something is wrong**
- `doctor` checks retention, the archive, the hooks, the sweep, sessions that never
  reached the archive, macOS privacy refusals, computers on other versions, and
  credentials that ended up in the archive, and work folders that are too big to back
  up. Most warnings say what to do.

**Other tools**
- `backup` writes the whole profile to one zip, for a move with no synced folder;
  `import` brings it back, rules, skills and plugins included.
- `pull` is a plain copy for computers with identical paths. `restore` files one folder
  of transcripts by hand.
- `machines` lists, and forgets, the computers that push to the archive.
- `import --repoint` fixes conversations already on this computer whose paths still
  name another computer's work folders.

---

## How it compares

Other tools sync Claude Code history too. Which one fits depends on where you want it
to live:

| | claude-sessions-sync | [claude-code-sync](https://github.com/perfectra1n/claude-code-sync) | [claude-sync](https://github.com/tawanorg/claude-sync) |
|---|---|---|---|
| Stored in | Any synced folder (Google Drive, OneDrive, Dropbox, a NAS) | A git repository | R2, S3, GCS or WebDAV |
| Encrypted before upload | No: relies on your provider | No | Yes |
| Project at a different path | Found by folder name (git remote tells same-named ones apart) or home folder; else one `config map` | A project map on each computer, or the folder name | Home folder automatic, other paths mapped by hand |
| Session continued on two computers | Kept as two sessions; resume either | Merged into one file | Local copy kept, the other saved as a `.conflict` file |
| Sessions Claude Code deletes locally | Kept in the archive | Kept in the repository | Removed from storage on the next push |
| Runs by itself | After every reply, at session end, every 30 minutes | Run by hand, or a cron job you set up | Optional shell hooks |
| Undo and version history | No | Yes | No |

Compared from each project's documentation and source code in November 2026; they
change, so check them for yourself. If you want encryption, or a git history you can
roll back, one of the others may suit you better.

---

## Quick start

**Or let Claude do it.** Paste this into Claude Code on each computer:

> Set up claude-sessions for me by following
> https://github.com/ashiqfardus/claude-sessions-sync/blob/main/docs/setup-with-claude.md

Claude installs the tool, finds your synced folder, shows you what it would back up
before anything is uploaded, and asks before each step that changes something.

To do it yourself, on your **first computer**:

```sh
claude-sessions config set-destination "<your synced folder>/claude-sessions"
claude-sessions doctor     # see what needs fixing
claude-sessions push       # back up everything once
claude-sessions install    # from now on, automatically
```

and stop Claude Code deleting old sessions: add `"cleanupPeriodDays": 3650` to
`~/.claude/settings.json` (Step 3).

On **each further computer**:

```sh
claude-sessions config set-destination "<the same synced folder>/claude-sessions"
claude-sessions import --search-root ~/dev --dry-run    # see where each project goes
claude-sessions import --search-root ~/dev --update     # then do it
claude-sessions push                                    # back up this computer too
claude-sessions install
```

Then `cd` into a project and run `claude --resume`. The rest of this page explains
each step.

---

# Step-by-step guide

## Before you start: a synced folder

This tool does not talk to any cloud service. It copies files into **a folder on your
own disk**; a sync app you already have gets that folder to the cloud.

| What you want | What you need |
|---|---|
| History in the cloud, or on another computer | The **desktop** sync app for your service, signed in, with a folder synced to this computer |
| A copy on an external drive or a NAS | Nothing: just use that path |
| A second copy on this computer only | Nothing: use any folder |

A browser tab of Drive or Dropbox is not enough; the tool needs a real folder.

| Service | Typical archive path |
|---|---|
| Google Drive (Windows) | `G:\My Drive\claude-sessions` |
| Google Drive (macOS) | `~/Library/CloudStorage/GoogleDrive-you@gmail.com/My Drive/claude-sessions` |
| OneDrive (Windows) | `C:\Users\you\OneDrive\claude-sessions` |
| Dropbox | `~/Dropbox/claude-sessions` |
| iCloud Drive (macOS) | `~/Library/Mobile Documents/com~apple~CloudDocs/claude-sessions` |
| External disk | `D:\backups\claude-sessions` |

The archive folder does not have to exist yet, but **its parent must**: the tool
refuses to create a folder where an unmounted cloud drive should be, because that
would be a "backup" that never leaves the computer.

## Step 1: Install the tool

It runs on Windows 10 or later, macOS and Linux.

**With a package manager** (the program lands on your `PATH`, and upgrades are one
command):

```sh
# macOS or Linux, with Homebrew
brew install --cask ashiqfardus/tap/claude-sessions
```

```powershell
# Windows, with Scoop
scoop bucket add ashiqfardus https://github.com/ashiqfardus/scoop-bucket
scoop install ashiqfardus/claude-sessions
```

**Or download** the file for your system from the
[Releases page](https://github.com/ashiqfardus/claude-sessions-sync/releases): a `.zip`
for Windows, a `.tar.gz` for macOS and Linux, for amd64 (Intel and AMD) or arm64
(Apple silicon, ARM). Unpack it and put `claude-sessions` (`claude-sessions.exe` on
Windows) in a folder on your `PATH`. No other software is needed. This guide describes
**v0.3.0** and later; older releases lack most of it.

**Or build it** with Go 1.21 or newer ([download Go](https://go.dev/dl/)):

```sh
go install github.com/ashiqfardus/claude-sessions-sync/cmd/claude-sessions@latest
```

or, from a clone of this repository, `go install ./cmd/claude-sessions`.

Check it:

```sh
claude-sessions version
```

If that says "command not found" after `go install`, add Go's `bin` folder to your
`PATH` (`go env GOPATH` prints where it is; add `/bin` to that).

**Use the same version on every computer.** See [Several computers](#several-computers).

## Step 2: Tell it where to save your history

```sh
claude-sessions config set-destination "G:\My Drive\claude-sessions"
```

Use your own path. A leading `~` works even inside quotes, and the path is saved as an
absolute one. If it says the parent does not exist, your cloud drive is not mounted.
`claude-sessions config` shows what is saved.

If you skip this step, the tool looks for an existing `claude-sessions` folder by
itself: in `<drive>:\My Drive` on Windows, in `~/Google Drive`, `~/OneDrive`,
`~/Dropbox`, iCloud Drive and `~/Sync`, and on macOS under `~/Library/CloudStorage`,
where it uses one only when exactly one account holds it. With none yet, it picks the
only Google Drive it can see. Setting it yourself is better: every run then agrees.

## Step 3: See what state you are in

```sh
claude-sessions doctor
```

It reports each check with `+` (fine), `!` (warning), `x` (failure) or a blank
(information), and for most problems a line on what to do. For example:

```
  ! retention        cleanupPeriodDays unset, so Claude Code applies its 30-day default
  + archive          G:\My Drive\claude-sessions (session-sync.json)
  ! session hook     no SessionEnd hook installed
  x archived         12 session(s) that ended over 40 minutes ago are not in the archive, e.g. ...
  ! last push        no successful push recorded in session-sync.log
```

**Fix the retention warning now**, because it is the one deleting your history. Add
this to `~/.claude/settings.json`:

```json
{ "cleanupPeriodDays": 3650 }
```

From then on, watch `archived` (a session that ended over 40 minutes ago is still not
in the archive) and `last push` (nothing pushed for three hours). The hooks and the
sweep never fail, by design, so these two checks are how you notice a problem.

## Step 4: Save your sessions once

```sh
claude-sessions push
```

```
2026-11-20 10:00:01  PUSH: 24 copied, 0 unchanged, 12 session(s), 4 project(s) -> G:\My Drive\claude-sessions
2026-11-20 10:00:03  HTML: 12 rendered, 0 up to date
copied 24 file(s), 0 unchanged.
12 session(s) across 4 project(s) -> G:\My Drive\claude-sessions (session-sync.json)
```

The two timestamped lines are what it also writes to `~/.claude/session-sync.log`.

Run it again and it copies nothing: only changes are copied. `push` never changes
your local files.

## Step 5: Make it automatic

```sh
claude-sessions install
```

This copies the program to `~/.claude/bin/` and sets up three things:

| What | When it runs | What it does |
|---|---|---|
| Session-end hook | When a session ends | A full push of everything that changed |
| Per-reply hook | After every reply (Claude Code's `Stop` event) | Copies **just that session** |
| Sweep | Every 30 minutes (`--every <minutes>`) | A full push: catches sessions that ended abruptly, and updates the index and pages |

The per-reply hook means a laptop that dies or is shut mid-session loses minutes
rather than half an hour. To keep it light:
- it copies one transcript only; memory, the index and the pages wait for the next
  full push;
- it never waits for another push that is running;
- it gives up after 5 seconds if the drive is slow;
- it copies a transcript over 5 MB at most every 30 minutes, because the sync app
  re-uploads the whole file each time, and Google Drive can keep each upload as an old
  version that counts toward your storage. The session-end hook and the sweep still
  copy it.

Only one push runs at a time. A session ending while another push runs waits up to
45 seconds (`--lock-wait`), then leaves it to the sweep.

The sweep is a Task Scheduler task on Windows (it runs `claude-sessionsw.exe`, a copy
that opens no console window), a launchd agent on macOS, and a systemd user timer
(or a crontab line) on Linux. A profile other than `~/.claude` gets its own sweep.

`install` backs up `settings.json` first (keeping the three newest backups) and leaves
every other setting and every other tool's hooks alone. `install --archive <folder>`
sets the destination at the same time; `install --no-sweep` skips the sweep.

**Then restart Claude Code**, or run `/hooks` inside it, so open sessions pick up the
hooks. Run `doctor` again: the hook and sweep lines should now be `+`.

## Step 6: Read your history in a browser

Every full push writes pages. Open `<your folder>/html/index.html`. Each project lists
its recent sessions with their opening messages; tool calls, results and thinking are
folded away. The pages are plain files with no scripts.

The front page shows each project's 20 newest sessions and links to the full list;
`INDEX.md` lists up to 200 per project. A hook or the sweep spends at most 45 seconds
on pages per run and leaves the rest for the next. To rebuild them by hand:
`claude-sessions render` (`--force` rebuilds pages that are current).

**To keep a project out of the pages** (client work, anything sensitive):

```sh
claude-sessions render --exclude "client-acme,secret-project"
```

Each name matches, ignoring case, any project whose path or archive name contains it
(so `api` would withhold every project with "api" in its path). This removes pages
already published for those projects, and the exclusion holds on every computer that
pushes to the archive. It is saved in `session-sync.json`
(`renderExclude`) and in the archive's `render-exclude.txt`. To publish one again,
remove it from both. The transcripts themselves stay in the archive.

## Step 7: Find an old conversation

```sh
claude-sessions search "connection pool"
```

```
/home/you/dev/api  9ee16efa-4c1d-4a9e-9f3a-2b8e5d7c1a44
  2026-08-31 17:41 user      ...raise the connection pool size to 40 and see if...

1 match(es); 12 session(s) searched, newest first.
Resume one with:  cd /home/you/dev/api  then  claude --resume 9ee16efa-4c1d-4a9e-9f3a-2b8e5d7c1a44
```

- `search --all` also searches commands, tool output and thinking. `--role`,
  `--project`, `--regexp` and `--case-sensitive` narrow it down. It stops after 50
  matches; `--limit 0` shows all.
- `ls` lists sessions newest first, with the full ID `claude --resume` needs.
  `--since` and `--until` take a date like `2026-08-01` (both days included);
  `--project` and `--limit` narrow it.
- `stats` shows how much history you have per project.
- Add `--archived` to any of the three to read the archive, which still has sessions
  Claude Code deleted locally. To resume one of those, bring it back with `import`.

## Step 8: Set up a second computer

**8a.** Install the tool (Step 1) and point it at the **same** folder (Step 2).

**8b.** Look first, without changing anything:

```sh
claude-sessions import --search-root ~/dev --dry-run
```

`--search-root` is where your code lives (several, comma-separated, are fine; folders
like `node_modules`, `vendor`, `build`, `dist` and `Library` are never searched). A dry
run shows where each project would go and how many new sessions it would add; what
`--update` would refresh, and copies of sessions continued on both computers, show
only on the real run. Each line says what happened to one archived project, and why:

```
= -home-you-dev-notes -> /home/you/dev/notes (matched by recorded path): 3 filed, 0 already present
+ D--work-api -> /home/you/dev/api (matched by git remote): 6 filed, 0 already present
! D--work-old-thing: 2 folders named "old-thing" and no git remote settles it
      /home/you/dev/old-thing
      /home/you/dev/attic/old-thing
      fix: claude-sessions config map D--work-old-thing '<local folder>'
- E--scratch: no folder named "scratch" under the search roots
      fix: claude-sessions config map E--scratch '<local folder>'
```

`=` same path here · `+` found elsewhere, paths rewritten · `!` ambiguous, skipped ·
`-` not found.

**8c.** Place anything it could not, once:

```sh
claude-sessions config map D--work-old-thing ~/dev/attic/old-thing
```

The mapping is saved on this computer only, wins over every other rule, and is used by
every later import. `config map` lists mappings and `config unmap <name>` removes one.
Project names that start with `-` (all macOS and Linux ones do) work as printed.

**8d.** Do it for real, then back up this computer and turn on archiving here too:

```sh
claude-sessions import --search-root ~/dev --update
claude-sessions push
claude-sessions install
```

(Until this computer has pushed, `doctor` reports the imported sessions as not
archived.)

`cd` into a project and run `claude --resume`.

### What import does, in detail

- **Without `--update`**, a session or memory file that is already here is never
  touched. New ones are added.
- **With `--update`**, a session here is replaced by the archived copy only when that
  copy holds everything this one does and more, for example the same conversation
  continued on the other computer, or renamed there.
- **Memory** with `--update` (only `MEMORY.md` is merged; a memory file deleted on the
  other computer is not deleted here):
  - `MEMORY.md`, the index Claude loads, is merged entry by entry. What either computer
    added arrives, what either deleted stays deleted, and where both changed an entry
    the newer wording wins.
  - Each other computer's memory is merged separately, against what was last taken from
    it. That record is kept in `~/.claude/session-sync-base/`.
  - Any other memory file is replaced only by a newer copy, and the old one is kept in
    `memory/.replaced/<time>/`.
  - A memory entry or file you deleted here is not brought back, even by a computer
    that took it from this one. A file dated in the future is always refused.
- **Your home folder matches your home folder.** `C:\Users\you` and `/Users/you` are
  matched as the same place, whatever the usernames, once the other computer has
  pushed with this version.
- **Other repositories a session worked in** (a multi-folder workspace) are repointed
  too, but only to a same-named folder whose git remote matches.
- **Rules and skills:** a file missing here is added, unless you deleted it here after
  this computer had it. One that exists is replaced only with `--update-profile` and a
  newer copy, and the output says so: `--update` alone never replaces one, since one
  computer's shorter `CLAUDE.md`, saved later, would otherwise win. This computer's
  version is saved first in the archive under `profile/replaced/<computer>/<time>/`.
- **Plugins** enabled on the other computer and not listed here are installed with
  `claude plugin install`, which needs `claude` on your `PATH`. A plugin turned off
  here is left off. Plugins from a marketplace on a local path are skipped. Use
  `--no-plugins` to skip all of it.

---

## Switching computers mid-session

**The safe way:**
1. Finish your turn and type `/exit`, so the session is archived.
2. Wait for your sync app to finish uploading.
3. On the other computer, run `claude-sessions import --update`, then
   `claude --resume`.
4. Commit or stash your code changes first: those are git's business, not this
   tool's.

**If you forget, or a computer dies**, nothing is lost. Say one laptop last synced at
5:00, you worked on until 5:29, and then picked the session up elsewhere from the 5:00
copy. Now each copy has turns the other lacks. Two conversations cannot be merged
into one, so:

- `import --update` keeps your local copy as it is, files the other computer's copy as
  **a session of its own** (same history, a new session ID), and prints its ID:

  ```
  ! continued here and elsewhere - 70e28154-…: the other machine's copy is session 2df28b9d-… (resume either)
  ```

- Resume whichever you want to carry on with. Importing again updates that copy
  rather than adding another, and a computer never gets its own copy back as an extra
  session.
- **Don't want the extra copy?** Delete it. It comes back only if the other computer
  adds new turns to it.
- If both computers keep the project **at the same path**, they push to one archived
  file. `push` compares the two by content, keeps the archived one, and saves this
  computer's beside it under `forks/`. The other computer's `import` or `pull` then
  files it the same way.

Copies are compared by each entry's ID, not by bytes, so a copy whose paths an import
rewrote still counts as the same conversation. Only turns count: the lines Claude Code
adds to a session without a new turn (its cost, the last prompt, a hook's output on
resume) never make a copy look newer or continued on both computers. A prompt you
queued that never ran is a turn, so a copy ending in one is kept as a fork.

---

## Several computers

Any number of computers can share one archive: macOS, Windows and Linux, each with
its projects at its own paths. Set each one up as in Step 8.

- **Every computer uses the same archive folder**, the one your sync app shares
  between them. On Linux, `install` runs the sweep from a systemd user timer, or a
  crontab line where there is no systemd.
- **Run the same version everywhere.** An older version files another computer's fork
  copies as ordinary sessions, and on a shared path still overwrites the other
  computer's archived copy. `doctor` names any computer on a different version.
- **Each computer has a name** in the archive: its host name and user name, saved in
  `session-sync.json` by its first push. A macOS host name can change with the network
  (a `-2` added after a clash); the saved name does not. To choose another:

  ```sh
  claude-sessions config set-machine my-macbook
  ```

  The hooks and the sweep use it too. The old name is remembered as this computer's,
  so its files in the archive are still its own, and other computers keep the forks
  they filed under it. Then run `claude-sessions push`, and
  `claude-sessions machines --forget <old name>` so the list stops showing it.
- **Names must differ.** Two computers under one name would overwrite each other's
  manifest. Each install makes a random ID on its first push, keeps it in
  `session-sync.json` and writes it into its manifest, so when another computer has
  pushed under the same name, `push` logs a warning and `doctor` fails:

  ```
  Another computer also uses the name LAP-2. Give one of them its own name with `claude-sessions config set-machine`.
  ```

  Pushing goes on regardless, and the warning stops once the names differ.
  - **A copy of another computer's `~/.claude`** (a cloned disk, Migration Assistant)
    carries that computer's name and ID, so it is not noticed. Run
    `claude-sessions config set-machine <name>` on the copy before it pushes.
  - `backup` leaves the ID out, so a backup restored on another computer makes its
    own. Restored on the same computer, it makes a new one too: its next push warns
    once about this computer's old ID, then the warning stops.
- `claude-sessions machines` lists every computer that has pushed, with the hour of
  its last backup and its newest session. **Retire a computer** you no longer use with
  `machines --forget <name>`: it removes its manifest and index, and its sessions stay.
  Zipping and deleting wait for any computer listed that runs an older version, so
  forget one that is gone.
- **Each conversation is archived once.** A session imported from another computer is
  not archived again under this computer's folder for the project; the other
  computer's copy is the one kept. Once you continue it here, this computer's copy
  holds more and is archived too.
- **Freeing space.** Versions up to 0.4.0 archived every imported session a second
  time. After updating every computer and letting each push once, run:

  ```sh
  claude-sessions clean          # lists the duplicate copies and their size
  claude-sessions clean --yes    # deletes them
  ```

  It deletes a copy only when another copy holds every turn of it, and every turn of its
  subagents and its tool-output files too; it reads both in full before deleting, so
  `--yes` on a big archive takes a while. It keeps both copies of a session continued
  on two computers, and any copy whose computer still runs an older version, which
  would upload it again. Run it on one computer at a time, once the other has synced.
  Google Drive keeps deleted files in its trash for 30 days, and they still count toward
  your storage until then: empty the trash to get the space back now.

---

## Deleting old conversations

The archive keeps every conversation until you say otherwise. `prune` deletes chosen
ones from it. Nothing on any computer is touched: a computer that still has a
conversation keeps it.

```sh
claude-sessions prune --older-than 365d          # lists what it would delete
claude-sessions prune --older-than 365d --yes    # deletes them
claude-sessions prune --project web-app --older-than 180d
claude-sessions prune <conversation id> --yes
```

- **Choose what to delete** with `--older-than <N>d` (days since the last message, in
  the newest copy on any computer), `--project <name or path>`, or conversation IDs.
  Different kinds combine: `--project web-app --older-than 180d` is web-app's
  conversations older than 180 days. With none it refuses; it never means "everything".
- **Without `--yes` it only lists** each conversation's project, last message, first
  prompt and size, the total, and how many it skipped because they are kept. With
  `--yes` it deletes every copy: in each computer's folder for the project, loose or
  zipped, with its subagent and tool-output files, its forks and its page. Memory,
  rules, skills and work folders stay.
- **A project is the same project on every computer**: the same git remote, otherwise
  the same folder name. `--project web-app` covers `~/dev/web-app` on a Mac and
  `E:\web-app` on Windows, and lists the folders it matched. A name that two different
  repositories share is refused: give the path.
- **Pruned stays pruned.** Each deletion is recorded in the archive's
  `deleted/<computer>.json`, and no computer uploads that conversation again. Continue
  it on a computer that still has it, and the new turns bring it back. One named by ID
  that cannot be dated (no turn times, and a file time still to come) is recorded with
  no date, so a computer that still has it uploads it again. A computer whose sync app
  delivers the deleted files before the deleted list may upload a pruned conversation
  once more; once the list arrives it is left out of the index, and the age rule, or the
  same `prune` run again, deletes it.
- **Undo:** `claude-sessions prune --undo <conversation id>` takes it off that list, and
  any computer that still has it uploads it on its next push. With `keep-days` on, also
  `config keep` it, or the age rule deletes it again.
- **Keep list:** `claude-sessions config keep <conversation id, project name or path>`
  puts a conversation, or a project on every computer, out of reach of `prune` and the
  age rule. `config keep` lists it, and `config unkeep <same>` takes one off. A
  conversation with a copy in a kept project is kept. A project known by its git remote
  is kept by its folder names too, listed as "kept with remote": it stays kept when its
  folder is deleted, moved or on a drive not plugged in, or its remote changes. That
  also keeps another project with the same folder name. `config unkeep` takes the
  remote and its folder names off together.
- **Age rule:** `claude-sessions config keep-days 365` deletes, by itself, what
  `prune --older-than 365d --yes` would. It is off until you set it, refuses fewer than
  90 days, and `config keep-days off` stops it. The setting is in the archive
  (`retention.json`), so every computer follows the same one. Each computer runs it at
  most once a day, in the sweep or a `push` from a terminal, never in the hooks, and
  logs `PRUNE: <id> (<project>, last message <date>, <size>)` for each conversation it
  deletes. So as not to fetch the whole archive from a folder that downloads files on
  demand, it picks conversations by their files' times, a zip's being the newest of the
  files in it, and reads a transcript only when its file is old enough. Each one it
  picks is then dated from its own last message, as `prune` dates it, before it is
  deleted: a file changed after the last message (a subagent's, a title) can make it
  delete a conversation a little later than `prune --older-than` would, never sooner.
  In the sweep it stops choosing after about 22 seconds and starts no new check after
  45; one large conversation being checked can run past that. What it has not reached waits for
  the next day's run, which starts there. `doctor`'s `prune` line
  shows the setting and when it last ran there.
- **Every computer must be updated first.** An older version would upload a pruned
  conversation again, so `prune --yes` and the age rule delete nothing until every
  computer that has pushed to the archive runs this version and has pushed once with
  it. They name the computers they wait for: update them, or retire one no longer in
  use with `claude-sessions machines --forget <name>`.
- **The space comes back after the trash.** Google Drive keeps deleted files in its
  trash for 30 days, and they count toward your storage until then: empty the trash to
  get the space back now. Until it is emptied, the trash is another way back.

---

## Moving to a new computer without a synced folder

```sh
claude-sessions backup --out D:\
```

This writes `claude-backup-<computer>-<date>.zip` with every session, subagent
conversation and memory file; your rules, skills, agents, commands and output styles;
and `settings.json`, `settings.local.json`, `history.jsonl` (every prompt you typed),
`keybindings.json` and `session-sync.json`. A `MANIFEST.txt` counts transcripts per
project, and it warns loudly if it found none. Without `--out` it writes to
`~/claude-backups`.

It leaves out `.credentials.json` and `.claude.json`, but **settings files can hold
tokens** (in `env`, for example), so keep the zip as private as the profile itself.

On the new computer, unzip it and import it like an archive:

```sh
claude-sessions import --archive "E:\claude-backup" --search-root "E:\work"
```

That brings back the sessions and memory, your rules, skills, agents, commands and
output styles (added if missing, even if you deleted them since; replaced only with
`--update-profile` and a newer copy, keeping this computer's version in
`~/.claude/session-sync-base/replaced/<time>/`), and installs the plugins that were
enabled, unless you pass `--no-plugins`. Nothing is written into the backup folder,
and paths under the old `~/.claude` and home folder are repointed. Three things stay in the zip on purpose, for you to copy if you
want them:
- `settings.json`, whose hooks run programs at paths only the old computer has;
- `settings.local.json` and `keybindings.json`;
- `history.jsonl`.

---

## Upgrading

Replace the program, then run `install` again with the same flags you used before:

```sh
brew upgrade --cask claude-sessions   # Homebrew
scoop update claude-sessions          # Scoop
# or download the new release and replace the file
claude-sessions install
```

The hooks and the sweep run the copy in `~/.claude/bin`, so a new version does nothing
until it is installed again. Installing again is safe: it replaces its own entries
rather than adding more. Do it on **every** computer.

After upgrading from an older version:
- **The first `import --update` has no record yet** of what each computer already
  took. A memory entry or file, rule or skill deleted on one computer before the
  upgrade can come back once. Delete it again and it stays deleted.
- **Home folders are matched** only after the other computer has pushed with this
  version.
- **Work subfolders are packed** from this version on, and an older version skips the
  zips: on a computer still running one, `import` and `pull` do not restore a subfolder
  another computer packed, and print `refused: type` once for each zip on every run.
  Upgrade every computer.
- **The first push after upgrading packs** every work subfolder whose files have not
  changed for 7 days, and deletes the archive's loose copies of them. On a large archive
  that can be thousands of deletions, all going to Google Drive's trash.
- **Conversations are zipped** only once every computer has updated and pushed. After
  that, each sweep zips about 45 seconds' worth of conversations idle for 30 days, and
  `push` from a terminal zips them all. Each zip deletes the archive's loose copies: on
  a large archive, thousands of deletions, all going to Google Drive's trash.
- **Buckets an earlier `import` made for projects that are not here stay.** A repository
  inside another computer's container work folder got a bucket of its own, named for a
  folder that does not exist on this computer. This version makes no new ones; delete
  the old ones under `~/.claude/projects/` by hand.

**Going back to an older version:** run `claude-sessions uninstall` with **this**
version first, then install the older one. An older `uninstall` does not know the
per-reply hook and would leave it running.

---

## If something goes wrong

**`doctor` says the archive is not reachable.** Your cloud drive is not mounted.
Nothing is backed up until it is.

**`doctor` warns `archive files` or `not finished after 20s`, or `import` and `push`
crawl.** Your sync app downloads each file only when it is opened. Make the archive
folder available offline: in Google Drive, right-click the `claude-sessions` folder and
choose Offline access, then Available offline; in OneDrive, right-click the folder and
choose Always keep on this device; in Dropbox, choose Make available offline; in iCloud
Drive, choose Keep Downloaded. If the folder is on a network drive instead, check that it
is connected. `doctor` gives up on the archive after one check has taken 20 seconds, so
it always finishes.

**`doctor` fails "archive access" on macOS.** macOS privacy settings refused access to
the archive. The hint says which program to allow: the background sweep needs
`~/.claude/bin/claude-sessions` added under System Settings › Privacy & Security ›
Full Disk Access. After you rebuild the program, run `install` again and re-add it.

**`doctor` says the sweep is disabled, failing, or its program is missing.** Run
`claude-sessions install` again.

**`doctor` fails `archived`, or warns about `last push`.** Sessions are not reaching
the archive. Run `claude-sessions push` by hand to see the error, and read
`~/.claude/session-sync.log`: the hooks and the sweep write there instead of failing.

**`import` says a project is ambiguous or not found.** Use the `config map` command it
prints (Step 8c).

**`ls` shows nothing.** Claude Code files a session under the folder it *started* in.
The tool follows `CLAUDE_CONFIG_DIR` if you set it; `--claude-dir` points it at any
other profile. Sessions deleted locally are still in the archive: `ls --archived`.

**A session I copied by hand does not show in `claude --resume`.** It is under the
wrong project. Use `import`, or for one folder:

```sh
claude-sessions restore --source "/path/to/old/bucket" \
    --project-path "/home/you/dev/api" --rewrite-from "D:\work\api"
```

**Project names have a lowercase drive letter (`e--` not `E--`).** Normal: every
lookup ignores case.

**I want to stop using it.** `claude-sessions uninstall` removes both hooks and the
sweep; run it before `brew uninstall --cask claude-sessions` or
`scoop uninstall claude-sessions`, which only remove the program. **Your archive is
left completely alone.** These files in `~/.claude` stay for
you to delete if you want:
- `bin/` (the program, and `claude-sessionsw.exe` on Windows);
- `session-sync.json` (destination, exclusions, mappings, computer name);
- `session-sync.log` (and `.log.1`, once it passes 1 MB), `session-sync.err.log`
  (macOS), and `session-sync.hook-timeout`;
- `session-sync-base/` (what import last took from each computer);
- up to three `settings.json.bak-*` files.

---

## Work folders

Some of what Claude works on is not in a git repository: a folder of notes, a scripts
folder, a project you never ran `git init` in. `push` finds those folders from your
sessions and copies their text-like files to `work/<computer>/` in the archive. It is
on by default.

**What is copied.** Text and code: Markdown, source, SQL, JSON, config, CSV and the
like. **Never copied:** images, video and audio, archives, binaries, databases,
`.pth` files, files whose names look like secrets (`.env`, keys, tokens), anything over
10 MB, and every dot file and dot folder. `node_modules`, `venv`, `vendor`,
`__pycache__`, `site-packages` and `dist-packages` are never work folders. Inside a
work folder, subfolders named `dist`, `build`, `target` and `.cache` are skipped too,
so a hand-written `build/` folder is not backed up. The limit is 250 MB after that
filtering for each first-level subfolder of a work folder, with the files directly in
the folder counted as one more: a subfolder over it is skipped, the rest of the folder
is backed up, and `doctor` names the subfolder. Folders that hold app settings or
caches, such as `~/Library`, `AppData` and `ProgramData`, are never backed up.

**Packed subfolders.** A first-level subfolder whose files have all been unchanged for
7 days, with at least 5 of them, goes into the archive as one zip,
`<subfolder>.cspack.zip`, so the sync client moves one file instead of thousands. A
file you edit or add there later goes up on its own, beside the zip; the zip is rebuilt
only when those pass 50 files or a quarter of its size and none of them has changed for
7 days. `import` and `pull` unpack it into plain files under the same rules as any other
restored file. Of a zipped file and a later copy beside it, the newer is restored.

**What it skips.** Git repositories (your remote has those), `~/.claude`, and temp
folders.

**Which conversations count.** Push looks for folders only in what sessions on this
computer wrote. A conversation filed from another computer by `import`, `pull` or
`restore` adds no work folder, though lines you add here by continuing it do. So a
conversation planted in your archive does not, by itself, make your next push upload a
folder of yours. There are two exceptions:

- A fork Claude Code makes of an imported conversation (`claude --resume
  --fork-session`) copies its lines into a new conversation, which push reads as this
  computer's own.
- `push --scan-history`, below.

Someone who can write to your archive can reach you in other ways too (see
[SECURITY.md](SECURITY.md)). `import` and `pull` install the plugins another computer
enabled, unless you pass `--no-plugins`. They also add rules, skills, agents and commands
from the archive; those are only added, never replacing yours unless you pass
`--update`, and no flag turns that off. Pass `--no-plugins` unless you trust every
writer, and keep the archive in a folder only you can write to.

**The first run.** The first run of this version marks every conversation already on
this computer as read, and looks for no folders in them. That applies to an upgrade, and
equally to a fresh install on a computer that already has Claude Code history. To back
up the work folders of those earlier conversations, run this once:

```sh
claude-sessions push --scan-history
```

It reads every conversation in full, including ones imported from other computers, so a
folder named in one of those is uploaded too. Use it only if you trust everyone who can
write to your archive.

**On another computer.** `import` and `pull` put the files back under your home folder,
or under a folder you have mapped, and `import` rewrites the paths in the
conversations to match. A folder that was outside the other computer's home goes to
`~/claude-work/<computer>/...` unless you map it:

```sh
# On MAC, where the other computer (WIN) kept E:\SQL:
claude-sessions config map-folder 'E:\SQL' ~/SQL
claude-sessions import

# On WIN, the same pair the other way round:
claude-sessions config map-folder /Users/a/SQL 'E:\SQL'
claude-sessions import
```

Map both ways. A folder placed from WIN goes back where it came from, and so does
anything inside it, but a folder around it does not: if MAC works in all of `~/SQL`
while only `E:\SQL\po` came from WIN, WIN files MAC's `~/SQL` in its own home, not at
`E:\SQL`.

For a folder outside your home on the computer you restore to, map it to itself:
`config map-folder 'E:\SQL' 'E:\SQL'`. Restore never writes outside your home or a mapped
folder.

Restoring only adds. A file already there is left alone. With `--update`, a file is
replaced only if it came from that same computer and you have not changed it since;
otherwise the new copy is written beside it as `<name>.<computer>.<ext>`. Restored
files are never executable.

**Conversations already here.** If you imported before work folders existed, or
conversations still point at another computer's folders, run
`claude-sessions import --repoint --dry-run` to see what would change, then without
`--dry-run`. It is opt-in. It skips conversations touched in the last hour, and copies
each one to `<claude-root>/repoint-backup/<date-time>/` before changing it.

**Commands.**

| Command | What it does |
|---|---|
| `config work on\|off` | Turn work folder backup on or off |
| `config work-placement home\|machine` | Where a folder with no local counterpart goes: under your home (default), or always under `~/claude-work/<computer>/` |
| `config map-folder <path> <folder>` | Say where another computer's work folder lives here |
| `config unmap-folder <path>` | Remove that mapping |
| `config exclude-folder <folder>` | Never backed up from, and never restored into; also remove this computer's archived copy (your sync provider's trash or version history may still hold it) |
| `config unexclude-folder <folder>` | Back it up again |

---

## Privacy

Read this before pointing the tool at a shared or cloud folder.

- **Transcripts contain more than questions and answers**: the contents of files you
  worked on, paths, command output. `push` copies all of it into the folder you choose,
  and from there your sync provider's storage and sharing rules apply.
- **The pages make it readable.** Anyone who can open the folder can read your
  conversations in a browser, including in the provider's own preview. Use
  `render --exclude`, or `push --no-html` to write no pages at all.
- **Common credentials are masked** on the pages and in `INDEX.md`: API keys and tokens
  in known formats, private keys, `PASSWORD=` style values, and passwords in URLs. The
  `.jsonl` keeps everything. This is pattern matching, a safety net and not a
  guarantee.
- **`INDEX.md` lists the opening line of every session**, except for excluded projects.
- **The tool makes no network connections of its own.** There is no telemetry, no
  account, and no third-party code. The one exception is `import` installing a missing
  plugin by running `claude plugin install`, which downloads it; `--no-plugins` turns
  that off.
- **Work folders are copied too, on by default.** Only text-like files, and never
  secret-shaped names, dot files or dot folders. A notes folder can still hold
  something private: `config work off` stops it all, and `config exclude-folder
  <folder>` keeps one folder out and removes its archived copy.
- **Restoring work folders trusts the archive as little as possible.** It never writes
  dot files or folders, `~/Library`, `AppData`, `ProgramData`, Windows system folders,
  PowerShell profile folders, `vimfiles`, `_vimrc`, `_gvimrc`, the Go module cache,
  conda `activate.d` and `deactivate.d`, Python `site-packages`, `dist-packages` and
  `.pth` files, code folders, git repositories, the Claude folder or temp folders. It
  only adds files and never overwrites one that is there. **A deny-list can never be
  complete.** Someone who can write to your synced archive folder could still add *new*
  files in other places under your home that some tool loads, such as a project folder
  without `.git`, or `~/perl5`. On Windows, a restored `.bat` or `.ps1` runs when
  opened whatever its permissions. Write access to the archive is as good as access to
  your cloud account: protect it the same way.
- **Credentials are never copied.** `.credentials.json` and `.claude.json` are never
  archived or backed up, and `doctor` fails if it finds either anywhere in the archive
  outside the generated pages.
- **Nothing is deleted from your archive** except the tool's own output: pages withdrawn
  by `render --exclude`, a forgotten computer's manifest and index, and half-written
  `.tmp-*` files over an hour old.
- **The archive has no access control or encryption of its own.** Keep it in a folder
  only you can read. What other computers write into it is treated as untrusted; see
  [SECURITY.md](SECURITY.md).
- There is not yet a way to keep a project out of `push` itself. If a project must
  never leave the computer, do not point `push` at a synced folder.

---

## How the matching works

For each archived project, `import` looks for the folder on this computer in this
order, and uses the first that answers:

```
1. Your mapping (config map)        -> always wins.
2. The path recorded in the session -> if that folder exists here.
3. The home folder                  -> another computer's home is this one's.
4. A folder with the same name      -> one: used, unless its git remote is a
                                       different repository.
                                       several: the one whose git remote matches,
                                       origin URLs compared so
                                       git@github.com:you/api.git
                                       == https://github.com/you/api
5. Anything else                    -> reported and skipped, never guessed.
```

A checkout with a different folder name is not found this way, even with the same git
remote: map it with `config map`.

The project name in the archive cannot be decoded back into a path (`E:\Laravel
Applications\x` and `E:\Laravel-Applications\x` flatten to the same name), so the real
path is read from inside the transcripts and kept in a manifest in the archive. Git
remotes are read by parsing `.git/config`; git itself is never run (see
[SECURITY.md](SECURITY.md)).

---

## Reference

### Commands

| Command | What it does |
|---|---|
| `doctor` | Check everything; exits non-zero on a real problem |
| `config` | `show` (default), `set-destination <folder>`, `map [<project> <folder>]`, `unmap <project>`, `set-machine <name>`, `work on\|off`, `work-placement home\|machine`, `map-folder <path> <folder>`, `unmap-folder <path>`, `exclude-folder <folder>`, `unexclude-folder <folder>`, `zip-sessions on\|off`, `keep [<id\|project>]`, `unkeep <id\|project>`, `keep-days <N\|off>` |
| `push` | Copy new and changed sessions, memory, rules, skills and the plugin list to the archive, then write the pages |
| `install` / `uninstall` | Turn automatic archiving on or off |
| `import` | Bring an archive onto this computer, matching projects by identity |
| `pull` | The same, by project name only, for computers with identical paths |
| `restore` | File one folder of transcripts into one project |
| `backup` | Zip the whole profile, without credentials |
| `render` | Rebuild the HTML pages |
| `ls`, `search`, `stats` | List, search and count sessions |
| `machines` | List the computers that pushed here; `--forget` one |
| `clean` | List archived copies of a session that another copy holds in whole; `--yes` deletes them |
| `prune` | List archived conversations by age, project or ID; `--yes` deletes every copy, `--undo` takes one back |
| `completion` | Print a shell completion script: `bash`, `zsh`, `fish` or `powershell` |
| `version` | Print the version |

Every command except `completion` and `version` takes `--claude-dir <path>`, for a
profile other than `~/.claude` (the tool already follows `CLAUDE_CONFIG_DIR`).
`--archive <path>` reads or writes another archive than the saved one on `push`,
`pull`, `import`, `render`, `doctor`, `machines`, `clean`, `prune`, `install`, `ls`, `search` and
`stats` (on the last three it implies `--archived`). `--json` gives machine-readable
output on `push`, `import`, `render`, `doctor`, `machines`, `ls`, `search` and
`stats`. Flags can go before or after other arguments; `--help` on any command lists
its flags. `grep` is another name for `search`, and `-v` or `--version` for `version`.

### Flags

| Flag | On | What it does |
|---|---|---|
| `--dry-run` | `push`, `import`, `pull`, `restore`, `backup` | Show what would happen; write nothing |
| `--repoint` | `import` | Fix conversations already on this computer that name another computer's work folders; skips any touched in the last hour and backs each up first |
| `--update` | `import`, `pull`, `restore` | Also bring in sessions continued elsewhere and newer memory |
| `--update-profile` | `import`, `pull` | Also replace rule and skill files here with newer archived copies; this computer's are kept in the archive's `profile/replaced/` first |
| `--no-plugins` | `import`, `pull` | Do not install plugins enabled on another computer |
| `--no-work` | `import`, `pull` | Bring conversations only; skip other computers' work folders. Not with `--repoint` |
| `--search-root <dirs>` | `import` | Where to look for projects (comma-separated; default: the parents of known projects, and your home) |
| `--depth <n>` | `import` | How deep to look under each search root (default 3) |
| `--include-bare-name` | `import`, `restore` | Also replace the bare folder name in transcripts (it can hit package names and prose) |
| `--source`, `--project-path`, `--rewrite-from` | `restore` | The folder to file, the project here, and the old path to rewrite |
| `--no-html` | `push` | Do not write pages |
| `--scan-history` | `push` | This run only, look for work folders in every conversation, imported ones too; once, for conversations from before the first run, and only if you trust everyone who can write to the archive |
| `--quiet` | `push` | Hook mode: log instead of printing, and never fail |
| `--lock-wait <time>` | `push` | With `--quiet`, how long to wait for another push, like `45s` or `2m` (default 45s) |
| `--from-hook` | `push` | Used by the per-reply hook: copy only the session named on stdin |
| `--sweep` | `push` | Used by the sweep: marks its log line for `doctor` |
| `--every <minutes>` | `install` | How often the sweep runs (default 30) |
| `--no-sweep` | `install` | Install the hooks only |
| `--archive <folder>` | `install` | Set the destination while installing |
| `--keep-powershell-hook` | `install`, `uninstall` | Leave the older PowerShell version's hook in place (on by default for `uninstall`) |
| `--replace-powershell-sweep` | `install` | Also remove the older PowerShell version's scheduled task |
| `--archived` | `ls`, `search`, `stats` | Read the archive instead of this computer |
| `--project`, `--since`, `--until`, `--limit` | `ls` | Narrow the list |
| `--all`, `--role`, `--regexp`, `--case-sensitive`, `--project`, `--limit` | `search` | Narrow the search |
| `--force`, `--exclude <text>`, `--quiet` | `render` | Rebuild current pages; withhold projects (saved, on every computer); print nothing on success |
| `--forget <name>` | `machines` | Retire a computer |
| `--older-than <N>d`, `--project <name or path>`, `--yes`, `--undo` | `prune` | Choose by age or project; delete; take conversations off the deleted list |
| `--out <folder>` | `backup` | Where to write the zip |
| `--no-write-probe`, `--exclude <text>` | `doctor` | Skip the write test; count extra exclusions |

### What `doctor` checks

| Check | What it looks at |
|---|---|
| `claude root`, `projects` | The profile it found, and how many projects and sessions are in it |
| `settings.json`, `retention` | Whether `settings.json` parses; whether `cleanupPeriodDays` is unset or under 90 days |
| `bucket naming` | Information only: `CLAUDE_CODE_PROJECT_DIR_NAME` is set, so project names are not path-based |
| `local buckets` | Information only: projects here with memory but no sessions |
| `archive`, `archive writable`, `archive config` | The archive is reachable and writable (`--no-write-probe` skips the test file), and whether its path is saved or only auto-detected |
| `archive files` | Reading the start of one archived file took over 3 seconds: the folder downloads files only when they are opened. After an archive check gave up with no slow read seen: how to make a cloud folder available offline, or that the folder is not responding |
| `archive checks` | The archive checks skipped after one did not finish in 20 seconds |
| `manifest`, `manifest format` | Archived projects `import` could not place, ignored manifest files (unreadable, or a sync app's conflict copy), and an archive still in the PowerShell tools' format |
| `html`, `html excluded` | Whether pages are current, and which projects are withheld |
| `session hook`, `reply hook`, `sweep` | The hooks and the scheduled sweep are installed and run this program; duplicates or the older PowerShell hook |
| `archived`, `last push` | A session that ended over 40 minutes ago is not in the archive; no push for three hours |
| `archive access` | A push was refused access, for example by macOS privacy settings, and by which run |
| `versions` | Other computers run a different version |
| `profile` | Rule and skill files that differ between computers, or are not archived |
| `work folders` | Off, or how many folders and files are archived; warns over 500 MB, or when a folder is too large to back up |
| `zips` | Whether idle conversations are zipped: off, or waiting for the computers it names to update |
| `prune` | The `keep-days` setting, when the age rule last ran here, and the computers `prune --yes` and the age rule wait for |
| `secrets` | `.credentials.json` or `.claude.json` anywhere in the archive outside the pages |

Each check prints as it finishes. A check that reads the archive and has not finished
after 20 seconds is left behind and shown as a warning, `<check>: not finished after 20s`.
The archive checks after it are then skipped, in one `archive checks` line, and the
checks of this computer still run.

It exits non-zero when any check fails, so it can be monitored. `doctor --json` prints
`{version, checks, warnings, failures, ok}`.

To load completions:

```sh
claude-sessions completion bash > ~/.local/share/bash-completion/completions/claude-sessions
claude-sessions completion zsh  > "${fpath[1]}/_claude-sessions"
claude-sessions completion fish > ~/.config/fish/completions/claude-sessions.fish
```

```powershell
claude-sessions completion powershell | Out-String | Add-Content $PROFILE
```

### What is in the archive

```
<archive>/
  projects/<project>/<session-id>.jsonl     sessions, with <session-id>/ for subagents and tool output
  projects/<project>/<session-id>.conv.zip  a session idle for 30 days, as one zip
  projects/<project>/memory/                the project's memory, newest file from any computer
  memory/<computer>/<project>/              each computer's own copy of its memory
  forks/<project>/<session-id>/<computer>.jsonl
                                            a session continued on two computers that share a path
  profile/                                  rules, skills, agents, commands, output styles (newest)
  profile/machines/<computer>/              each computer's own copy
  profile/replaced/<computer>/<time>/       rule files an import replaced on that computer
  profile/plugins/<computer>.json           each computer's enabled plugins
  work/<computer>/<folder path>/            each computer's work folders (text-like files only)
  work/<computer>/<folder path>/<sub>.cspack.zip
                                            a subfolder unchanged for 7 days, as one zip
  manifest/<computer>.json                  each computer's projects: path, git remote, home, version
  index/<computer>.json                     each computer's list of sessions
  projects.json                             a merged view, for the older PowerShell scripts
  deleted/<computer>.json                   the conversations each computer pruned, so none uploads them again
  retention.json                            the keep list and keep-days, shared by every computer
  INDEX.md, html/                           the readable index and pages
  render-exclude.txt                        projects never published as pages
```

Each computer writes only its own files under `memory/<computer>/`, `profile/machines/`,
`manifest/`, `index/`, `work/<computer>/` and `forks/…/<computer>.jsonl`, so two computers pushing at once
cannot overwrite each other's.

#### Zipped conversations

A conversation nobody has touched for 30 days is stored as one file,
`projects/<project>/<session-id>.conv.zip`: its transcript and everything in its
`<session-id>/` folder, with their names and times, usually in about a third of the
space. The zip's own date is the newest of theirs: when the conversation was last used. Push zips it once every archived file of it, and this computer's own transcript
of it if there is one, has gone unchanged for 30 days, in the projects this computer
pushes to. It reads the zip back and checks that it holds every turn before it deletes
the loose files. A conversation over 250 MB stays loose. The hooks never zip, so the
end of a session is never held up. The sweep starts no new zip after 45 seconds and
leaves the rest for its next run; one large conversation started just before can take
it past that. `push` from a terminal zips everything due, and `push --dry-run` lists
what it would zip.

Every command reads a zipped conversation as if it were loose. Continue one, on any
computer, and the next push unzips it and archives the new turns; it is zipped again
after 30 more quiet days. Its page under `html/` stays, so it can still be read in the
sync app or a browser.

To open one by hand, use any unzip tool (Finder, Explorer, or `unzip`). It holds
`<session-id>.jsonl` and the `<session-id>/` folder.

`claude-sessions config zip-sessions off` stops zipping on this computer; zips already
made stay readable, and `on` starts it again.

**Every computer must read zips first.** An older version does not see a zipped
conversation and would upload it loose again, so push zips nothing until every computer
that has pushed to the archive runs this version and has pushed once with it. `doctor`
names the computers it waits for: update them, or retire one no longer in use with
`claude-sessions machines --forget <name>`. The older PowerShell scripts are not
supported alongside zipping and do not hold it back: a computer still running them would
only upload its own copies of conversations again, and never loses a turn.

---

## What it will not do

Each of these is deliberate:

- **It never changes your local transcripts on push.** `import`, `pull` and `restore`
  add new ones, and with `--update` replace one only by a copy that holds everything it
  does and more.
- **It never deletes a conversation from the archive unless you ask.** A session that
  ages out locally survives there. Only `prune`, or the `keep-days` rule once you set
  it, deletes conversations; `clean` deletes only copies another copy holds in whole,
  and zipping deletes loose files only once the zip is read back and holds every turn.
- **A copy that holds less never replaces one that holds more**, in either direction.
- **It never creates your cloud folder's parent.** An unmounted drive is an error.
- **It never copies credentials.**
- **It never guesses** which project a session belongs to.
- **The hooks never fail.** Missing drive, offline mount, an error: they log and exit
  0, because a failing hook would break your session.
- **It never removes automation it did not install**, with one exception: `install`
  replaces the SessionEnd hook of its own older PowerShell version, so the two do not
  both run (`--keep-powershell-hook` leaves it).
- **A rewritten transcript is checked line by line before it is kept.** If a rewrite
  would break a file, nothing is written.

---

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). [DESIGN.md](DESIGN.md) explains the
architecture, and [SECURITY.md](SECURITY.md) the security policy.

**Especially wanted: reports from real macOS and Linux desktops**, particularly whether
the sweep survives a logout, a reboot, or a laptop lid closed for days.

## License

[MIT](LICENSE) © Md. Asikul Islam
