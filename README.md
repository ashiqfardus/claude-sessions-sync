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
- Runs by itself once installed: when a session ends, **after every reply**, and on a
  sweep every 30 minutes.
- Copies only what changed. It never deletes a transcript from the archive, and never
  copies credentials.

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
  credentials that ended up in the archive. Most warnings say what to do.

**Other tools**
- `backup` writes the whole profile to one zip, for a move with no synced folder;
  `import` brings it back, rules, skills and plugins included.
- `pull` is a plain copy for computers with identical paths. `restore` files one folder
  of transcripts by hand.
- `machines` lists, and forgets, the computers that push to the archive.

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

On your **first computer**:

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
- it copies a transcript over 5 MB at most every 5 minutes, because the sync app
  re-uploads the whole file each time.

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
  this computer had it. One that exists is replaced only with `--update` and a newer
  copy, and the output says so. This computer's version is saved first in the archive
  under `profile/replaced/<computer>/<time>/`.
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
rewrote still counts as the same conversation.

---

## Several computers

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
- `claude-sessions machines` lists every computer that has pushed, with its newest
  session. `machines --forget <name>` removes a retired one's manifest and index; its
  sessions stay.

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
`--update` and a newer copy, keeping this computer's version in
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

**Going back to an older version:** run `claude-sessions uninstall` with **this**
version first, then install the older one. An older `uninstall` does not know the
per-reply hook and would leave it running.

---

## If something goes wrong

**`doctor` says the archive is not reachable.** Your cloud drive is not mounted.
Nothing is backed up until it is.

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
| `config` | `show` (default), `set-destination <folder>`, `map [<project> <folder>]`, `unmap <project>`, `set-machine <name>` |
| `push` | Copy new and changed sessions, memory, rules, skills and the plugin list to the archive, then write the pages |
| `install` / `uninstall` | Turn automatic archiving on or off |
| `import` | Bring an archive onto this computer, matching projects by identity |
| `pull` | The same, by project name only, for computers with identical paths |
| `restore` | File one folder of transcripts into one project |
| `backup` | Zip the whole profile, without credentials |
| `render` | Rebuild the HTML pages |
| `ls`, `search`, `stats` | List, search and count sessions |
| `machines` | List the computers that pushed here; `--forget` one |
| `completion` | Print a shell completion script: `bash`, `zsh`, `fish` or `powershell` |
| `version` | Print the version |

Every command except `completion` and `version` takes `--claude-dir <path>`, for a
profile other than `~/.claude` (the tool already follows `CLAUDE_CONFIG_DIR`).
`--archive <path>` reads or writes another archive than the saved one on `push`,
`pull`, `import`, `render`, `doctor`, `machines`, `install`, `ls`, `search` and
`stats` (on the last three it implies `--archived`). `--json` gives machine-readable
output on `push`, `import`, `render`, `doctor`, `machines`, `ls`, `search` and
`stats`. Flags can go before or after other arguments; `--help` on any command lists
its flags. `grep` is another name for `search`, and `-v` or `--version` for `version`.

### Flags

| Flag | On | What it does |
|---|---|---|
| `--dry-run` | `push`, `import`, `pull`, `restore`, `backup` | Show what would happen; write nothing |
| `--update` | `import`, `pull`, `restore` | Also bring in sessions continued elsewhere and newer memory; with `import` and `pull`, newer rules and skills too |
| `--no-plugins` | `import`, `pull` | Do not install plugins enabled on another computer |
| `--search-root <dirs>` | `import` | Where to look for projects (comma-separated; default: the parents of known projects, and your home) |
| `--depth <n>` | `import` | How deep to look under each search root (default 3) |
| `--include-bare-name` | `import`, `restore` | Also replace the bare folder name in transcripts (it can hit package names and prose) |
| `--source`, `--project-path`, `--rewrite-from` | `restore` | The folder to file, the project here, and the old path to rewrite |
| `--no-html` | `push` | Do not write pages |
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
| `manifest`, `manifest format` | Archived projects `import` could not place, ignored manifest files (unreadable, or a sync app's conflict copy), and an archive still in the PowerShell tools' format |
| `html`, `html excluded` | Whether pages are current, and which projects are withheld |
| `session hook`, `reply hook`, `sweep` | The hooks and the scheduled sweep are installed and run this program; duplicates or the older PowerShell hook |
| `archived`, `last push` | A session that ended over 40 minutes ago is not in the archive; no push for three hours |
| `archive access` | A push was refused access, for example by macOS privacy settings, and by which run |
| `versions` | Other computers run a different version |
| `profile` | Rule and skill files that differ between computers, or are not archived |
| `secrets` | `.credentials.json` or `.claude.json` anywhere in the archive outside the pages |

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
  projects/<project>/memory/                the project's memory, newest file from any computer
  memory/<computer>/<project>/              each computer's own copy of its memory
  forks/<project>/<session-id>/<computer>.jsonl
                                            a session continued on two computers that share a path
  profile/                                  rules, skills, agents, commands, output styles (newest)
  profile/machines/<computer>/              each computer's own copy
  profile/replaced/<computer>/<time>/       rule files an import replaced on that computer
  profile/plugins/<computer>.json           each computer's enabled plugins
  manifest/<computer>.json                  each computer's projects: path, git remote, home, version
  index/<computer>.json                     each computer's list of sessions
  projects.json                             a merged view, for the older PowerShell scripts
  INDEX.md, html/                           the readable index and pages
  render-exclude.txt                        projects never published as pages
```

Each computer writes only its own files under `memory/<computer>/`, `profile/machines/`,
`manifest/`, `index/` and `forks/…/<computer>.jsonl`, so two computers pushing at once
cannot overwrite each other's.

---

## What it will not do

Each of these is deliberate:

- **It never changes your local transcripts on push.** `import`, `pull` and `restore`
  add new ones, and with `--update` replace one only by a copy that holds everything it
  does and more.
- **It never deletes a transcript from the archive.** A session that ages out locally
  survives there.
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
