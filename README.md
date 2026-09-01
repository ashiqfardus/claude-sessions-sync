# claude-sessions-sync

Back up your [Claude Code](https://claude.com/claude-code) conversations to any synced
folder, read them on your phone, and put them back on any machine — **matched by
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

> **Status:** everything below works and is tested. Developed on **Windows**; the macOS
> and Linux paths pass CI but have had little real-world use — please
> [report what breaks](.github/ISSUE_TEMPLATE/platform_report.yml).

---

# Step-by-step guide

## Before you start

You need **one folder that syncs to the cloud**. Google Drive, OneDrive, Dropbox,
iCloud Drive, Syncthing, or a NAS share — any of them. This tool just writes files into
a folder you choose; whatever you already use does the uploading.

Make a note of that folder's path. For example:

- Windows: `G:\My Drive\claude-sessions`
- macOS: `~/Library/Mobile Documents/com~apple~CloudDocs/claude-sessions`
- Linux: `~/Dropbox/claude-sessions`

---

## Step 1 — Install the tool

You need Go 1.21 or newer ([download](https://go.dev/dl/)):

```sh
go install github.com/ashiqfardus/claude-sessions-sync/cmd/claude-sessions@latest
```

Check it worked:

```sh
claude-sessions version
```

If that says "command not found", add Go's bin folder to your `PATH`:

```sh
go env GOPATH        # then add /bin to what it prints
```

---

## Step 2 — Tell it where to save your history

```sh
claude-sessions config set-destination "G:\My Drive\claude-sessions"
```

Use your own folder from *Before you start*. It creates that folder if the parent
exists.

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
  ! session hook     no SessionEnd hook installed
  ! sweep            Task Scheduler: not registered

  retention: Local transcripts are deleted a month after they are written. Raise it in settings.json.
  session hook: Sessions are only archived by the periodic sweep, so the most recent one can be missed.

2 warning(s), 0 failure(s).
```

Every warning comes with a line telling you what to do about it. Steps 4 and 5 clear
the two above.

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
12 session(s) across 4 project(s) -> G:\My Drive\claude-sessions
```

Run it again and it should copy **0** — it only ever copies what changed. It never
deletes anything from the archive, and it never modifies your local transcripts.

---

## Step 5 — Make it automatic

```sh
claude-sessions install
```

This sets up two things:

- a **hook** that archives each session the moment it ends, and
- a **sweep** every 30 minutes, which catches sessions that ended abruptly (a closed
  terminal, a crash, a laptop going to sleep).

Then **restart Claude Code**, or run `/hooks` inside it, so a session that is already
running picks up the change.

Check it:

```sh
claude-sessions doctor
```

The hook and sweep warnings should now be `+`.

> `install` copies the binary into `~/.claude/bin/` and points the hook there, so
> archiving does not depend on where you ran the command from. It backs up your
> `settings.json` first and leaves every other setting and every other tool's hooks
> exactly as they were.

---

## Step 6 — Read your history on your phone

Already done: `push` writes HTML pages automatically. Open this file from your phone's
cloud app:

```
<your folder>/html/index.html
```

You get a list of every session with its opening message; tap one to read the whole
conversation. Tool calls, results and thinking are folded away so the conversation
reads as a conversation.

To rebuild the pages by hand:

```sh
claude-sessions render
```

**If some projects should not be readable that way** — client work, anything sensitive
— exclude them:

```sh
claude-sessions render --exclude "client-acme,secret-project"
```

They are still backed up; they just are not turned into web pages. See
[Privacy](#privacy) below.

---

## Step 7 — Find an old conversation

```sh
claude-sessions search "connection pool"
```

```
E:\airos-frontend  9ee16efa-4c1d-4a9e-9f3a-2b8e5d7c1a44
  2026-08-31 17:41  user       ...raise the connection pool size to 40 and see if...

1 match(es) in 12 session(s).
Resume one with:  claude --resume 9ee16efa-4c1d-4a9e-9f3a-2b8e5d7c1a44
```

Copy that last command to reopen the conversation in Claude Code.

Other ways to look around:

```sh
claude-sessions ls                          # every session, newest first
claude-sessions ls --project api --limit 10 # just one project
claude-sessions stats                       # how much history you have, per project
```

---

## Step 8 — Set up a second computer

This is the part other tools cannot do. On the second machine:

**8a. Install and point at the same folder** (steps 1 and 2 again):

```sh
go install github.com/ashiqfardus/claude-sessions-sync/cmd/claude-sessions@latest
claude-sessions config set-destination "~/Dropbox/claude-sessions"
```

**8b. See what it would do, without changing anything:**

```sh
claude-sessions import --search-root ~/dev --dry-run
```

`--search-root` is where you keep your code. Output looks like:

```
+ D--work-api -> /home/you/dev/api (matched by git remote): 6 filed, 0 already present
= C--Users-you -> /home/you (matched by recorded path): 3 filed, 0 already present
! e--old-thing: 2 folders named "old-thing" and no git remote settles it - skipped
```

It tells you **why** each project matched. Anything ambiguous is skipped, never
guessed — filing a conversation under the wrong project would be silent and you would
never find it again.

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

## If something goes wrong

**`doctor` says the archive is not reachable.**
Your cloud drive is not mounted. Nothing is being backed up until it is.

**`ls` shows nothing.**
Claude Code files history by the *terminal's* working directory, not your editor's
workspace. A multi-root workspace has one bucket per folder. If you use
`CLAUDE_CONFIG_DIR`, pass `--claude-dir`.

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

**I want to stop using it.**

```sh
claude-sessions uninstall
```

Removes the hook and the sweep. **Your archive and every transcript in it are left
completely alone.**

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
  conversations in a browser, including through a cloud provider's own file preview.
  Use `render --exclude`, or `push --no-html`, for anything that should not be that
  legible.
- **`INDEX.md` at the top of the archive lists the opening line of every session**, so
  the gist of your history is visible at a glance.
- **The tool itself never transmits anything.** No network calls, no telemetry, no
  accounts, no third-party dependencies. It writes files to a local path; a sync client
  you already installed does the uploading.
- **Credentials are never copied.** `.credentials.json` and `.claude.json` are excluded
  by design, and `doctor` fails loudly if it finds either anywhere in your archive.
- **Nothing is ever deleted from your archive** by this tool.

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

Git remotes are read by **parsing `.git/config`, never by running git** — see
[SECURITY.md](SECURITY.md) for why that distinction matters.

---

## Command reference

| Command | What it does |
|---|---|
| `doctor` | Check everything. Exits non-zero on a real problem, so you can monitor it |
| `config` | Show or set the archive folder |
| `push` | Copy new and changed sessions to the archive, then render the pages |
| `install` / `uninstall` | Turn automatic archiving on or off |
| `render` | Rebuild the HTML pages |
| `ls` | List sessions (`--project`, `--since`, `--until`, `--limit`) |
| `search` | Search the text of every session (`--role`, `--regexp`, `--project`) |
| `stats` | Sessions, sizes and dates per project |
| `import` | File an archive onto this machine, matching projects by identity |
| `pull` | Same-name copy, for machines with identical paths |
| `restore` | File one folder of transcripts by hand |
| `machines` | Who has pushed here; `--forget` retires one |
| `completion` | Shell completions for bash, zsh or fish |

Every command takes `--claude-dir` and most take `--json`. Add `--help` to any of them.

Useful flags worth knowing:

| Flag | On | Why |
|---|---|---|
| `--dry-run` | `push`, `import`, `restore`, `pull` | See what would happen first |
| `--quiet` | `push` | Hook mode: logs instead of printing, and **never fails** |
| `--no-html` | `push` | Archive without rendering readable pages |
| `--force` | `render` | Rebuild pages that are already current |
| `--exclude` | `render` | Do not publish these projects as web pages |

---

## What it will not do

Each of these is deliberate, and most come from a real failure:

- **It never modifies your local transcripts.** They are only ever read.
- **It never deletes from the archive.** A session that ages out locally survives.
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

**Especially wanted: macOS and Linux testing.** The author has only a Windows machine.

## License

[MIT](LICENSE) © Md. Asikul Islam
