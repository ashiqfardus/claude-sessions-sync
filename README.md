# claude-sessions-sync

Back up your [Claude Code](https://claude.com/claude-code) conversations to any synced
folder, read them as web pages, and put them back on any machine — **matched by
project identity, not by file path**.

---

## Why you need this

Claude Code keeps every conversation as a file on your computer:

```
~/.claude/projects/<project>/<session-id>.jsonl
```

Three things go wrong with that:

1. **They only exist on that one computer.** No cloud copy, nothing on your phone.
2. **They get deleted.** Claude Code removes transcripts after `cleanupPeriodDays`,
   which defaults to **30**. Most people never change it.
3. **They are tied to a folder path.** The same project at `D:\work\api` on your
   desktop and `~/dev/api` on your laptop counts as two different projects, so
   `claude --resume` on one machine cannot see the other's conversations — even if you
   copy the files across by hand.

> **Status: verified on Windows, macOS and Linux.**
>
> Every push runs the full test suite on all three, and separately performs a **real
> install** on each: it seeds a `settings.json` containing another tool's hook,
> installs, runs the installed binary exactly as the session hook would, checks the
> session was archived and rendered, checks the hook still exits 0 when the archive is
> unreachable, uninstalls, and asserts the other tool's hook and every unrelated
> setting survived.
>
> **The 30-minute sweep registers on all three** — launchd on macOS, a systemd user
> timer on Linux, Task Scheduler on Windows — and CI reports per-OS whether it did, so
> a regression there cannot hide.
>
> Day-to-day use is still mostly Windows, so
> [reports from real macOS and Linux desktops](.github/ISSUE_TEMPLATE/platform_report.yml)
> remain the most useful thing you can contribute — particularly whether the sweep
> survives a logout, which no CI runner can tell us.

---

# Step-by-step guide

## Before you start

### Do I need Google Drive, Dropbox or OneDrive installed?

**Only if you want your history off this computer** — which is usually the whole
point.

This tool does not talk to any cloud service. It copies files into **a folder on your
own disk**, and nothing more. Getting that folder to the cloud is somebody else's job:

| What you want | What you need installed |
|---|---|
| History in the cloud, or on another computer | **Yes** — the desktop sync app for whichever service you use, signed in, with a folder synced to this machine |
| A copy on an external drive or a NAS share | No — just point it at that path |
| Only a second copy on this same machine | No — point it at any folder you like |

The desktop app is the part that matters. A web browser tab of Drive or Dropbox is not
enough: the tool needs a **real folder it can write to**.

- **Google Drive** — install [Drive for desktop](https://www.google.com/drive/download/).
  On Windows it appears as a drive letter, usually `G:\My Drive`. On macOS it is
  `~/Library/CloudStorage/GoogleDrive-<you>/My Drive`.
- **OneDrive** — already built into Windows 11; sign in and you get `%USERPROFILE%\OneDrive`.
  On macOS, install it from the App Store.
- **Dropbox** — install the desktop app; you get `~/Dropbox`.
- **iCloud Drive** (macOS) — already there:
  `~/Library/Mobile Documents/com~apple~CloudDocs`.
- **Syncthing / NAS / USB drive** — anything that gives you a path works.

> If you skip this entirely, everything still works — your history is just backed up to
> another folder on the same computer, which protects you from Claude Code's 30-day
> deletion but not from losing the machine.

### Pick your folder

Decide where the archive should live and note the path. It does not have to exist yet,
but its **parent must** — the tool refuses to create a folder where an unmounted cloud
drive should be, because that would give you a "backup" that never leaves the machine.

Examples:

| Service | Typical path |
|---|---|
| Google Drive (Windows) | `G:\My Drive\claude-sessions` |
| Google Drive (macOS) | `~/Library/CloudStorage/GoogleDrive-you@gmail.com/My Drive/claude-sessions` |
| OneDrive (Windows) | `C:\Users\you\OneDrive\claude-sessions` |
| Dropbox | `~/Dropbox/claude-sessions` |
| iCloud Drive (macOS) | `~/Library/Mobile Documents/com~apple~CloudDocs/claude-sessions` |
| No cloud, external disk | `D:\backups\claude-sessions` |

The tool can often find Drive, OneDrive, Dropbox or iCloud on its own — but setting it
explicitly in Step 2 means every run agrees on where your history goes.

---

## Step 1 — Install the tool

It runs on Windows 10 or later, macOS and Linux.

**Download a release binary** from the
[Releases page](https://github.com/ashiqfardus/claude-sessions-sync/releases) — a zip
for Windows, a `.tar.gz` for macOS and Linux, for amd64 and arm64 — and put
`claude-sessions` (`claude-sessions.exe` on Windows) in a folder on your `PATH`. No Go
toolchain is needed.

**Until the next release is tagged**, the newest one (v0.2.2) predates much of this
guide: `backup`, `--archived`, `search --all`, `--update` and a large batch of fixes.
`go install ...@latest` resolves to the same tag. To get what is documented here now,
build from `main` with Go 1.21 or newer ([download](https://go.dev/dl/)):

```sh
go install github.com/ashiqfardus/claude-sessions-sync/cmd/claude-sessions@main
```

Check it worked:

```sh
claude-sessions version
```

If you used `go install` and that says "command not found", add Go's bin folder to
your `PATH`:

```sh
go env GOPATH        # then add /bin to what it prints
```

---

## Step 2 — Tell it where to save your history

```sh
claude-sessions config set-destination "G:\My Drive\claude-sessions"
```

Use your own folder from *Before you start*. It creates that folder if the parent
exists. A leading `~` is expanded even inside quotes or under PowerShell, and a
relative path is saved as an absolute one — the hook runs in whichever project folder
a session was in, so a relative archive would land inside every repository.

**If it says the parent does not exist**, your cloud drive is not mounted. That is
deliberate — creating an ordinary local folder where your cloud drive should be would
give you a backup that never leaves the machine.

Confirm it stuck:

```sh
claude-sessions config
```

---

## Step 3 — See what state you are in

```sh
claude-sessions doctor
```

This is the most useful command in the tool. It tells you, in plain terms, whether
your conversations are protected:

```
  + claude root      C:\Users\you\.claude (default)
  + projects         5 bucket(s), 12 transcript(s)
  ! retention        cleanupPeriodDays unset, so Claude Code applies its 30-day default
  + archive          G:\My Drive\claude-sessions (session-sync.json)
    manifest         the archive holds no projects yet
  ! session hook     no SessionEnd hook installed
  ! sweep            Task Scheduler: claude-sessions-sync not registered
  x archived         12 session(s) that ended over 40 minutes ago are not in the archive, e.g. C--Users-you-dev-api/9ee16efa-4c1d-4a9e-9f3a-2b8e5d7c1a44
  ! last push        no successful push recorded in session-sync.log

  retention: Local transcripts are deleted a month after they are written. Raise it in settings.json.
  session hook: Sessions are only archived by the periodic sweep, so the most recent one can be missed.
  sweep: A session killed abruptly will not be archived until the next manual push.
  archived: Neither the hook nor the sweep has copied them. Run `claude-sessions push` and read ~/.claude/session-sync.log for why.
  last push: Run `claude-sessions push` once, then check the hook and the sweep.

4 warning(s), 1 failure(s).
```

Every warning comes with a line telling you what to do about it. Step 4 clears
`archived` and `last push`; Step 5 clears the hook and the sweep.

The last two checks are the ones to watch from then on. The hook and the sweep exit 0
even when they fail — a failing hook would break the end of your session — so a
scheduler that reports success proves only that something started. `archived` fails
when a session that ended over 40 minutes ago is still not in the archive, and
`last push` warns when the last successful push is more than three hours old.

**Fix the retention warning now**, because it is the one actively deleting your
history. Open `~/.claude/settings.json` and add:

```json
{ "cleanupPeriodDays": 3650 }
```

---

## Step 4 — Save your sessions for the first time

```sh
claude-sessions push
```

You will see something like:

```
copied 24 file(s), 0 unchanged.
12 session(s) across 4 project(s) -> G:\My Drive\claude-sessions (session-sync.json)
```

Run it again and it should copy **0** — it only ever copies what changed. It never
deletes a transcript from the archive, and it never modifies your local transcripts.

---

## Step 5 — Make it automatic

```sh
claude-sessions install
```

This sets up two things:

- a **hook** that runs `push --quiet` when a session ends, and
- a **sweep** every 30 minutes, which catches sessions that ended abruptly (a closed
  terminal, a crash, a laptop going to sleep).

Only one push runs at a time. When a session ends while another is running — the sweep,
or a second session ending at the same moment — the hook waits up to 45 seconds for it
(`--lock-wait`); if it is still busy after that, the session is left for the next sweep.
An unattended push also spends at most 45 seconds rendering pages, so the first render
of a large archive is spread over several runs rather than starting again each time.

What the sweep is, per OS:

- **Windows**: a Task Scheduler task named `claude-sessions-sync` that runs
  `~\.claude\bin\claude-sessionsw.exe push --quiet`. That file is a copy of the binary
  marked as a GUI program. Task Scheduler opens a console window for every console
  program it starts, so without it a terminal would flash up every 30 minutes; the exit
  code still reaches the task's last result.
- **macOS**: a launchd user agent.
- **Linux**: a systemd user timer, or a crontab line where there is no systemd.

A profile other than `~/.claude` (`--claude-dir`, or `CLAUDE_CONFIG_DIR`) gets its own
sweep, with a suffix on the name, so installing one never replaces another's.

Then **restart Claude Code**, or run `/hooks` inside it, so a session that is already
running picks up the change.

Check it:

```sh
claude-sessions doctor
```

The hook and sweep warnings should now be `+`:

```
  + session hook     C:\Users\you\.claude\bin\claude-sessions.exe push --quiet
  + sweep            Task Scheduler: claude-sessions-sync, not yet run
```

> `install` copies the binary into `~/.claude/bin/` and points the hook there, so
> archiving does not depend on where you ran the command from. It backs up your
> `settings.json` first (keeping the three newest backups) and leaves every other
> setting and every other tool's hooks exactly as they were.
>
> `install --archive <folder>` sets the destination at the same time, with the same
> checks as `config set-destination`: a folder whose parent does not exist is refused.

---

## Step 6 — Read your history in a browser

Already done: `push` writes HTML pages automatically. Open this file in a browser:

```
<your folder>/html/index.html
```

You get each project's most recent sessions with their opening messages, and a link to
the project's full list; open one to read the whole conversation. Tool calls, results
and thinking are folded away so the conversation reads as a conversation. The pages are
plain files with no scripts and nothing loaded from elsewhere, laid out for a narrow
screen — but whether your cloud provider's phone app will open them is up to that app,
and has not been tested on a real device.

To rebuild the pages by hand:

```sh
claude-sessions render
```

**If some projects should not be readable that way** — client work, anything sensitive
— exclude them:

```sh
claude-sessions render --exclude "client-acme,secret-project"
```

It saves the exclusion and reports what it withdrew:

```
Excluding client-acme, secret-project from now on (saved to C:\Users\you\.claude\session-sync.json and G:\My Drive\claude-sessions\render-exclude.txt).
To publish again, remove it from renderExclude there and from render-exclude.txt.

Rendered 0, 40 already up to date -> G:\My Drive\claude-sessions\html
2 project(s) excluded; 6 previously published page(s) removed.
Open G:\My Drive\claude-sessions\html/index.html
```

Excluding **deletes pages that were already published**, not just future ones — the
whole point is to stop something being readable. The transcripts stay in the archive,
untouched; only the generated HTML goes.

The exclusion lasts. It is saved as `renderExclude` in `~/.claude/session-sync.json`,
and written to `render-exclude.txt` in the archive, so **every machine that pushes
there honours it** — not only the one you ran it on. Adding patterns to
`renderExclude` by hand works the same way; the next `push` copies them into
`render-exclude.txt`:

```json
{
  "destination": "G:\\My Drive\\claude-sessions",
  "renderExclude": ["client-acme", "secret-project"]
}
```

To publish a project again, remove it from both: the config's `renderExclude` and its
line in `render-exclude.txt`. Removing it from only one of them leaves it excluded.

`claude-sessions doctor` then lists what is being withheld, so a typo that matches
nothing does not quietly publish it anyway. See [Privacy](#privacy) below.

---

## Step 7 — Find an old conversation

```sh
claude-sessions search "connection pool"
```

```
/home/you/dev/api  9ee16efa-4c1d-4a9e-9f3a-2b8e5d7c1a44
  2026-08-31 17:41 user      ...raise the connection pool size to 40 and see if...

1 match(es); 12 session(s) searched, newest first.
Resume one with:  cd /home/you/dev/api  then  claude --resume 9ee16efa-4c1d-4a9e-9f3a-2b8e5d7c1a44
```

The last line says how to reopen the conversation in Claude Code: change into the
session's project folder, then resume it by ID.

By default only the conversation text is searched. To find a command you ran or
something a tool printed, add `--all`:

```sh
claude-sessions search --all "psql -c"
```

Other ways to look around:

```sh
claude-sessions ls                          # every session, newest first
claude-sessions ls --project api --limit 10 # just one project
claude-sessions stats                       # how much history you have, per project
```

`ls` prints each session's full ID, which is what `claude --resume` needs.

`ls`, `search` and `stats` read this machine's sessions. Claude Code deletes old ones
locally, so add **`--archived`** to read the archive instead — it still has them:

```sh
claude-sessions search --archived "connection pool"
```

A session found this way is only resumable once it is back on this machine; the output
says so, and `claude-sessions import` (Step 8) puts it back.

---

## Step 8 — Set up a second computer

This is the part other tools cannot do. On the second machine:

**8a. Install the tool as in Step 1, and point it at the same folder:**

```sh
claude-sessions config set-destination "~/Dropbox/claude-sessions"
```

**8b. See what it would do, without changing anything:**

```sh
claude-sessions import --search-root ~/dev --dry-run
```

`--search-root` is where you keep your code (several, comma-separated, are fine). Output
looks like:

```
archive      /home/you/Dropbox/claude-sessions (session-sync.json)
search roots /home/you/dev

= -home-you-dev-notes -> /home/you/dev/notes (matched by recorded path): 3 filed, 0 already present
+ D--work-api -> /home/you/dev/api (matched by git remote): 6 filed, 0 already present
! D--work-old-thing: 2 folders named "old-thing" and no git remote settles it
      /home/you/dev/old-thing
      /home/you/dev/attic/old-thing
+ D--work-scripts -> /home/you/dev/scripts (matched by folder name): 2 filed, 0 already present
- E--scratch: no folder named "scratch" under the search roots

11 transcript(s) filed. 1 same layout, 1 not found, 1 ambiguous.
Sessions already here are never replaced; --update refreshes ones that continued elsewhere.
```

followed by the `restore` command to use for anything not placed. Each line starts
with what happened: `=` the project is at the same path here, `+` it was found
elsewhere and its paths are rewritten to match, `!` it is ambiguous, `-` it was not
found. It tells you **why** each project matched. Anything ambiguous is skipped, never
guessed — filing a conversation under the wrong project would be silent and you would
never find it again.

**What is already here is kept.** Without `--update`, a session or memory file that
already exists on this machine is never replaced, however the archived copy differs.

**A session you continued on another machine** comes across with `--update`:

```sh
claude-sessions import --search-root ~/dev --update
```

A local session is replaced only when the archived copy strictly extends it — the same
conversation with more turns on the end. One that also continued here has turns the
other lacks, so neither contains the other: the local copy is kept and the output names
it. Memory files are replaced only with `--update`, only when the archived copy is
newer, and never by a file dated in the future. `pull` and `restore` take `--update`
too.

**Multi-folder workspaces are handled.** A session that starts in `frontend` and
works in `backend` too records both paths. `push` notes the other git repositories each
project's sessions touched, and `import` finds them on this machine **by git remote**
and repoints those paths as well — including Git Bash forms like `/e/work/backend`.

**Your rules, skills and plugins come too.** `push` also archives, under `profile/`:

| What | From `~/.claude` | On import |
|---|---|---|
| Global rules | `CLAUDE.md` and every other top-level `.md` (the files it `@`-includes) | Added if missing; replaced only with `--update` and a newer copy |
| Personal skills, agents, commands, output styles | `skills/`, `agents/`, `commands/`, `output-styles/` | Same rule |
| Plugins | which plugins are enabled, and their marketplaces | Each one missing here is installed with `claude plugin install`; `--no-plugins` skips this |

Left behind on purpose: `skills/synced/` (claude.ai account skills — Claude Code
downloads them on login), dot-files such as `.env` and `.trash/`, `node_modules/`,
`settings.json` (its hooks run tools at paths only the original machine has) and
credentials. Transcripts that name the other machine's `~/.claude` — saved tool
output, `CLAUDE.md`, a skill — are repointed to this machine's.

**8c. Do it for real:**

```sh
claude-sessions import --search-root ~/dev
```

Now `cd` into a project and run `claude --resume`. Your conversations from the other
machine are there.

**8d. Make this machine archive too:**

```sh
claude-sessions install
```

---

## Moving to a new computer without a synced folder

Take the whole profile with you in one file — every session, subagent conversation,
memory file and hand-written setting:

```sh
claude-sessions backup --out D:\
```

It writes `claude-backup-<machine>-<date>.zip` with a `MANIFEST.txt` counting the
transcripts per project, and warns loudly if there are none — a backup with no
conversations in it is exactly how history gets lost in a move. Credentials are never
included.

On the new machine, unzip it anywhere and import it like an archive:

```sh
claude-sessions import --archive "E:\claude-backup" --search-root "E:\work"
```

**`import` does not need a manifest.** A bucket that no machine's manifest names — an
unzipped backup, a machine you have since forgotten with `machines --forget`, or an
archive written by the older PowerShell scripts — is placed by the working directory
its transcripts record, through the same matching as everything else. Only a bucket
with no transcripts to read that from is reported and left for `restore`.

---

## Upgrading

Replace the binary the same way you installed it, then run `install` again, with the
same flags you used the first time (`--every`, `--no-sweep`):

```sh
claude-sessions install
```

The hook and the sweep do not run the binary you just downloaded. They run the copy
`install` put in `~/.claude/bin`, so a new version does nothing until it is installed
again. Re-running is safe: it replaces its own hook entry rather than adding a second,
and registers the sweep again. On Windows it is also what moves the sweep onto the
windowless `claude-sessionsw.exe`; until then `doctor` fails the sweep check with "the
task runs the console build, which opens a window every run".

---

## If something goes wrong

**`doctor` says the archive is not reachable.**
Your cloud drive is not mounted. Nothing is being backed up until it is.

**`ls` shows nothing.**
Claude Code files history by the folder a session *started* in, not your editor's
workspace — a session that later `cd`s into a sibling folder stays filed under the
first one. If you use `CLAUDE_CONFIG_DIR`, pass `--claude-dir`. Sessions deleted
locally are still in the archive: `ls --archived`.

**`doctor` says the sweep is disabled, failing, runs the console build, or its program
is missing.**
Run `claude-sessions install` again; it re-registers the scheduled job.

**`doctor` fails `archived`, or warns about `last push`.**
Sessions are ending but not reaching the archive. Run `claude-sessions push` by hand to
see the error, and read `~/.claude/session-sync.log`: the hook and the sweep log what
went wrong there instead of failing.

**A session I copied by hand does not show in `claude --resume`.**
It is in the wrong bucket. Use `claude-sessions import`, or for one folder:

```sh
claude-sessions restore --source "/path/to/old/bucket" \
    --project-path "/home/you/dev/api" --rewrite-from "D:\work\api"
```

**`import` says a project is ambiguous.**
Two folders share a name and neither has a matching git remote. Place it yourself with
`restore`, as above.

**`doctor` warns that buckets have no usable identity.**
Those projects have memory files but no conversations, so there is no recorded path to
match on. They are backed up safely; they just cannot be auto-placed elsewhere.

**Bucket names have a lowercase drive letter (`e--` not `E--`).**
Normal. Casing follows what Claude recorded, and every lookup here ignores case.

**A machine you no longer use is still listed.**
`claude-sessions machines` lists every machine that has pushed to the archive, with the
date of its newest archived session (LAST SESSION). Retire one by the name it prints
(host name and user name):

```sh
claude-sessions machines --forget LAP-1-you
```

This deletes that machine's manifest and index files, so its rows leave `INDEX.md` at
the next push. Its transcripts stay, and so do their pages: `render` publishes every
transcript in the archive, whichever machine pushed it. To withdraw a project's pages,
use `render --exclude`. If the machine is still in use, its next push adds it back.

**I want to stop using it.**

```sh
claude-sessions uninstall
```

Removes the hook and the sweep. **Your archive and every transcript in it are left
completely alone.** So are a few files in `~/.claude`, for you to delete if you want
them gone:

- `bin/claude-sessions` (`claude-sessions.exe` on Windows), and on Windows
  `bin/claude-sessionsw.exe`, the copy the sweep ran;
- `session-sync.json`, the saved destination and exclusions;
- `session-sync.log`, and `session-sync.log.1` once it has passed 1 MB and been rotated;
- up to three `settings.json.bak-*` backups written by `install`.

---

## Privacy

Read this before pointing the tool at a shared or cloud folder.

**Your transcripts are not just questions and answers.** They contain the contents of
files you worked on, absolute paths, command output, and whatever you happened to
discuss — which for most people includes commercially sensitive work.

- **`push` copies all of that into the folder you choose.** If it is a cloud drive,
  your conversations are then subject to that provider's storage, retention and
  sharing — and to anyone the folder is shared with.
- **`render` turns them into readable web pages** in `html/`. This is a real step up in
  exposure compared to raw `.jsonl`: anyone who can open the folder can now *read* your
  conversations in a browser, including through a cloud provider’s own file preview.
  Use `render --exclude` or the `renderExclude` config key for anything that should not
  be that legible, or `push --no-html` to stop rendering entirely. Excluding a project
  also **removes pages already published for it**, and is shared with every machine
  through `render-exclude.txt`.
- **Common credentials are masked on the pages** — API keys and tokens in known formats,
  private keys, `PASSWORD=`/`api_key:` style values, passwords in URLs. The `.jsonl`
  keeps everything. This is pattern matching, so it is a safety net, not a guarantee:
  a project that must not be readable belongs in the exclude list.
- **`INDEX.md` at the top of the archive lists the opening line of every session**
  (masked the same way, and withheld for excluded projects), so the gist of your
  history is visible at a glance.
- **The tool itself never transmits anything.** No network calls, no telemetry, no
  accounts, no third-party dependencies. It writes files to a local path; a sync client
  you already installed does the uploading.
- **Credentials are never copied.** `.credentials.json` and `.claude.json` are excluded
  by design, and `doctor` fails loudly if it finds either anywhere in your archive —
  every folder at every depth, except the generated pages in `html/`.
- **Transcripts are never deleted from your archive** by this tool. What it does delete
  is its own output: `render --exclude` withdraws that project's pages, and
  `machines --forget` deletes that machine's manifest and index files.
- **Access is whatever the folder allows.** The tool adds no access control of its own,
  so keep the archive in a folder only you can read. What other machines, or anyone the
  folder is shared with, write into it is treated as untrusted; [SECURITY.md](SECURITY.md)
  lists what that means.

A per-project exclude for `push` itself (as opposed to `render`) is **not yet built**.
Until it is, if a project must never leave the machine, do not point `push` at a synced
folder at all. Encryption at rest is also not implemented — the archive is plain files.

---

## How the matching works

When you bring an archive to a new machine, each project has to be matched to a local
folder. Other tools make you write a path map by hand. This works it out:

```
1. The path recorded in the transcript   -> if that folder exists here, done.
2. Git remote match                       -> compares origin URLs, normalised, so
                                             git@github.com:you/api.git
                                             == https://github.com/you/api
3. A unique folder-name match             -> exactly one folder called `api`.
4. Anything else                          -> reported and skipped. Never guessed.
```

**Why not just decode the folder name?** Because you cannot. `E:\Laravel
Applications\innovix`, `E:\Laravel-Applications\innovix` and
`E:\Laravel-Applications-innovix` all flatten to the same bucket name. So the real path
is read from *inside* the transcript and cached in a manifest that travels with the
archive.

Other repositories a session worked in — the siblings in a multi-folder workspace — are
recorded with their git remotes and placed **by remote only**; a folder that merely
shares the name is never enough to rewrite paths into it.

Git remotes are read by **parsing `.git/config`, never by running git** — see
[SECURITY.md](SECURITY.md) for why that distinction matters.

---

## Command reference

| Command | What it does |
|---|---|
| `doctor` | Check everything. Exits non-zero on a real problem, so you can monitor it |
| `config` | Show or set the archive folder |
| `push` | Copy new and changed sessions, global rules, skills and the plugin list to the archive, then render the pages |
| `install` / `uninstall` | Turn automatic archiving on or off |
| `render` | Rebuild the HTML pages |
| `ls` | List sessions (`--project`, `--since`, `--until`, `--limit`, `--archived`) |
| `search` | Search every session, newest first (`--all`, `--role`, `--regexp`, `--project`, `--archived`) |
| `stats` | Sessions, sizes and dates per project (`--archived`) |
| `backup` | Zip the whole profile — sessions, subagents, memory, settings — without credentials |
| `import` | File an archive onto this machine, matching projects by identity |
| `pull` | Same-name copy, for machines with identical paths |
| `restore` | File one folder of transcripts by hand |
| `machines` | Who has pushed here, and each one's last session; `--forget` retires one |
| `completion` | Shell completions for bash, zsh, fish or PowerShell |

Every command except `completion` and `version` takes `--claude-dir`, and most take
`--json`. Flags can go before or after other arguments. Add `--help` to any command.

To load completions, for example:

```sh
claude-sessions completion bash > /etc/bash_completion.d/claude-sessions
```

```powershell
claude-sessions completion powershell | Out-String | Add-Content $PROFILE
```

`claude-sessions completion` with no shell prints the lines for zsh and fish too.

Useful flags worth knowing:

| Flag | On | Why |
|---|---|---|
| `--dry-run` | `push`, `import`, `restore`, `pull`, `backup` | See what would happen first |
| `--archived` | `ls`, `search`, `stats` | Read the archive, which keeps sessions deleted locally |
| `--all` | `search` | Also search tool commands, tool output and thinking |
| `--update` | `import`, `pull`, `restore` | Also bring in sessions that continued on another machine, and newer memory |
| `--no-plugins` | `import`, `pull` | Do not install plugins enabled on another machine |
| `--quiet` | `push` | Hook mode: logs instead of printing, and **never fails** |
| `--lock-wait` | `push` | With `--quiet`, how long to wait for another run (default 45s) |
| `--no-html` | `push` | Archive without rendering readable pages |
| `--force` | `render` | Rebuild pages that are already current |
| `--exclude` | `render` | Stop publishing these projects as web pages, on every machine (saved) |

---

## What it will not do

Each of these is deliberate, and most come from a real failure:

- **It never modifies your local transcripts.** `push` only reads them. The one
  exception is one you ask for: `--update` replaces a local session with an archived
  copy that strictly extends it.
- **It never deletes a transcript from the archive.** A session that ages out locally
  survives, and keeps its row in `INDEX.md`. What it does remove: pages withdrawn by
  `render --exclude`, a forgotten machine's manifest and index files, and half-written
  `.tmp-*` copies an interrupted run left behind, once they are an hour old — looked for
  only in `projects/`, `manifest/`, `index/`, `html/` and the archive's top folder, so
  an archive inside a busy synced drive never touches another program's temp files.
- **An older or shorter copy never replaces a newer one**, in either direction.
- **It never creates your cloud folder's parent.** An unmounted drive is an error, not
  something to paper over.
- **It never copies credentials.**
- **It never guesses** which project a session belongs to.
- **The session hook never fails.** Missing drive, offline mount, unexpected error: it
  logs and exits 0, because a failing hook would break the end of your session.
- **It never removes automation it did not install.**
- **A rewritten transcript is re-validated as JSON before it is kept.** If a rewrite
  would corrupt a file, nothing is written at all.

---

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md); [DESIGN.md](DESIGN.md) explains the
architecture and the constraints each command satisfies. Security policy:
[SECURITY.md](SECURITY.md).

**Especially wanted: real-desktop macOS and Linux reports.** CI installs, runs and
uninstalls on all three platforms every push, and the sweep does register on each -
but a runner cannot tell us whether it survives a logout, a reboot, or a laptop lid
closing for three days. That is what a person on a real desktop can.

## License

[MIT](LICENSE) © Md. Asikul Islam
