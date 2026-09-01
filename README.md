# claude-sessions-sync

Back up your [Claude Code](https://claude.com/claude-code) conversations to any synced
folder, and put them back on any machine — **matched by project identity, not by file
path**.

```
$ claude-sessions ls
UPDATED           SIZE  PROJECT                 SESSION   FIRST PROMPT
2026-09-01 09:14  630K  C:\Users\you            6487e497  let's continue previous conversation
2026-08-31 17:35  1.8M  E:\airos-frontend       9ee16efa  fix the login redirect loop on refresh

$ claude-sessions search "connection pool"
E:\airos-frontend  9ee16efa-4c1d-4a9e-9f3a-2b8e5d7c1a44
  2026-08-31 17:41  user       ...raise the connection pool size to 40 and see if...
```

---

## Why you might need this

Claude Code keeps every conversation as a JSONL file on your machine:

```
~/.claude/projects/<bucket>/<session-id>.jsonl
```

`<bucket>` is your project's absolute path with `:`, `\`, `/` and spaces all replaced
by `-`. Three consequences, and the third is the one that bites:

1. **They are local only.** No cloud copy, no history on your phone.
2. **They expire.** `cleanupPeriodDays` defaults to **30**, so Claude Code deletes
   transcripts a month after they are written unless you have changed it. Most people
   have not.
3. **They are keyed to a path.** Check the same repo out at `D:\work\api` on your
   desktop and `~/dev/api` on your laptop and they are two different buckets.
   `claude --resume` on one machine cannot see the other's conversations — even after
   you copy the files across.

> ### Status: the round trip works
>
> `install`, `push`, `import`, `pull`, `restore`, `machines`, `ls`, `search`, `stats`,
> `doctor`, `config` and `completion` work and are tested - archiving is automatic
> once installed, and an archive can be filed onto another machine whose projects live
> at different paths. **`render` and `backup` are designed but not yet built.** See
> [DESIGN.md](DESIGN.md) for the architecture.
>
> Developed and used on **Windows**. The macOS and Linux paths pass CI but have had
> little real-world exercise. Reports welcome — see
> [the platform issue template](.github/ISSUE_TEMPLATE/platform_report.yml).

---

## Install

```sh
go install github.com/ashiqfardus/claude-sessions-sync/cmd/claude-sessions@latest
```

Go 1.21 or newer. The binary lands in `$(go env GOPATH)/bin` — put that on your `PATH`.

Or from source:

```sh
git clone https://github.com/ashiqfardus/claude-sessions-sync.git
cd claude-sessions-sync
go build -o claude-sessions ./cmd/claude-sessions
```

Prebuilt binaries arrive with the first tagged release.

---

## Quick start

```sh
claude-sessions doctor                                    # is anything actually protected?
claude-sessions config set-destination ~/Dropbox/claude-sessions
claude-sessions push                                      # copy sessions to the archive
claude-sessions ls                                        # what history exists here
claude-sessions search "that thing about retries"         # find it again
```

`install` sets up a SessionEnd hook and a periodic sweep so this happens on its own.
Until then `push` is safe to run as often as you like: it copies only what changed.

On your **second machine**, point at the same folder and let it work out where each
project lives here:

```sh
claude-sessions config set-destination ~/Dropbox/claude-sessions
claude-sessions import --search-root ~/dev --dry-run   # see what it would do
claude-sessions import --search-root ~/dev
```

`doctor` is the one to run first. It tells you in plain terms whether your
conversations are being backed up, whether Claude Code is quietly deleting them, and
whether anything in your archive could not be restored onto a new machine:

```
  + claude root      C:\Users\you\.claude (default)
  + projects         5 bucket(s), 3 transcript(s)
  ! retention        cleanupPeriodDays unset, so Claude Code applies its 30-day default
  + archive          G:\My Drive\claude-sessions (session-sync.json)
  ! manifest         3 of 5 archived bucket(s) have no usable identity: e--legacy-app, ...
  ! session hook     no SessionEnd hook installed
  ! sweep            Task Scheduler: not registered

  retention: Local transcripts are deleted a month after they are written. Raise it in settings.json.
  manifest: Identity is read from a transcript's cwd, so a bucket holding only memory
            files records nothing. `import` cannot place these.

2 warning(s), 0 failure(s).
```

Every warning comes with a line telling you what to do about it. `doctor` **exits
non-zero** when a check fails, so you can run it from a monitor or a cron job.

---

## Commands

### `push` — copy sessions to the archive

Copies new and changed transcripts and memory files, writes this machine's manifest
shard, and regenerates `INDEX.md`.

| Flag | Meaning |
|---|---|
| `--archive <path>` | Write somewhere other than the configured destination |
| `--dry-run` | Report what would be copied, write nothing |
| `--quiet` | Hook mode: log instead of printing, and **never fail** |
| `--json` | Machine-readable result on stdout (progress goes to stderr) |

What it does *not* do, deliberately:

- **It never deletes from the archive.** A session that ages out locally survives.
- **It never modifies your local transcripts.** They are only ever read.
- **It never creates the destination's parent.** If your cloud drive is not mounted,
  it refuses rather than quietly making a local folder and reporting success.
- **It never copies `.credentials.json` or `.claude.json`.**

Only one push runs at a time, guarded by a lock file, so a scheduled sweep and a
session-end run cannot interleave writes. Every run appends one line to
`<claude-dir>/session-sync.log`.

Change detection compares size and modification time **with two seconds of
tolerance**, because synced filesystems round timestamps — an exact comparison
re-uploads the whole archive on every run.

### `install` — archive automatically

Sets up a `SessionEnd` hook so every finished session is archived, plus a periodic
sweep to catch sessions that ended abruptly.

```sh
claude-sessions install                    # uses the configured destination
claude-sessions install --every 15         # sweep every 15 minutes
claude-sessions install --no-sweep         # hook only
claude-sessions uninstall
```

| Flag | Meaning |
|---|---|
| `--archive <path>` | Set and save the destination while installing |
| `--every <n>` | Sweep interval in minutes (default 30) |
| `--no-sweep` | Install the hook only |
| `--keep-powershell-hook` | Leave an existing `sync-claude-sessions.ps1` hook in place |
| `--replace-powershell-sweep` | Also remove the scheduled task the PowerShell predecessor registered |

What it does:

- Copies the binary to `<claude-dir>/bin/` so the hook never depends on where you ran
  it from, or on the sync client being mounted.
- Merges the hook into `settings.json`, **preserving every other setting and every
  other tool's hooks**, after writing a timestamped backup. Re-running replaces our
  own entry rather than adding a second.
- Registers a sweep: Task Scheduler on Windows, a launchd agent on macOS, a systemd
  user timer (or cron) on Linux.

**It will not remove automation it did not install.** If the PowerShell predecessor's
scheduled task is present it is reported, not deleted — pass
`--replace-powershell-sweep` if you want it gone. `uninstall` checks that the task
actually runs this binary before removing it.

### `import` — file an archive onto this machine

The command the project exists for. For each archived project it works out where that
project lives *here*, files its transcripts into the right bucket, and rewrites the
paths recorded inside them so `claude --resume` finds them.

| Flag | Default | Meaning |
|---|---|---|
| `--search-root <dirs>` | parents of known projects, plus `$HOME` | Comma-separated folders to scan |
| `--depth <n>` | `3` | How deep to scan under each root |
| `--dry-run` | off | Report what would happen, write nothing |
| `--include-bare-name` | off | Also replace the bare folder name inside transcripts |
| `--json` | off | Machine-readable result |

Output names which rung of the ladder matched — `recorded path`, `git remote` or
`folder name` — so you can see *why* something was placed where it was. Anything
ambiguous is listed and skipped, never guessed.

Run it with `--dry-run` first.

### `pull` — same-layout copy

Copies archived transcripts into buckets of the same name, with no matching and no
rewriting. Correct only when both machines use identical paths; `import` is what you
want otherwise, and the output says so.

### `restore` — file one folder by hand

The primitive `import` calls, for anything it could not place — say a folder of
transcripts recovered from an old laptop:

```sh
claude-sessions restore --source '/mnt/old/e--airv2-frontend-web' \
    --project-path 'E:\airos-frontend' --rewrite-from 'E:\airv2-frontend-web'
```

It reports how many lines it rewrote in each form, and warns when lines still mention
the old folder name without a path prefix — those are package names, git remotes and
prose, which `--include-bare-name` will also replace if you really want.

**Every rewritten line is re-validated as JSON before the result is kept.** If any
line would be corrupted, the output is discarded and nothing is left behind: a
transcript that no longer parses is worse than no transcript.

### `machines` — who has pushed here

```sh
claude-sessions machines                  # list contributors
claude-sessions machines --forget OLD-PC  # drop a retired machine's manifest
```

Shards are keyed by hostname **and username**, so two accounts on one machine do not
overwrite each other. `--forget` removes only a machine's manifest and index entries —
its transcripts stay in the archive.

### `ls` — list sessions

The **PROJECT** column is the project's real path, read from inside the transcript —
not the mangled bucket name.

| Flag | Default | Meaning |
|---|---|---|
| `--project <text>` | — | Only sessions whose path or bucket contains this text |
| `--since <date>` | — | Updated on or after `YYYY-MM-DD` |
| `--until <date>` | — | Updated on or before `YYYY-MM-DD` (inclusive) |
| `--limit <n>` | `0` (all) | Show at most this many |
| `--json` | off | Machine-readable output |

### `search` — find a conversation again

Searches the text of every message. This is what makes an archive worth keeping.

| Flag | Default | Meaning |
|---|---|---|
| `--project <text>` | — | Restrict to matching projects |
| `--role <role>` | — | `user` or `assistant` only |
| `--regexp` | off | Treat the query as a regular expression |
| `--case-sensitive` | off | Match case exactly |
| `--limit <n>` | `50` | Stop after this many matches |
| `--json` | off | Machine-readable output |

```sh
claude-sessions search --role user "why is the build failing"
claude-sessions search --regexp "TODO|FIXME" --project api
```

Each result prints the project, session id and a snippet, and the footer gives you the
`claude --resume <id>` command to reopen it.

### `stats` — what your history looks like

Sessions, sizes and date ranges per project.

### `doctor` — check the setup

| Check | What a warning means |
|---|---|
| `claude root` | Where Claude Code's config was found |
| `projects` | How many buckets and transcripts exist locally |
| `retention` | `cleanupPeriodDays` is unset or low — your history is on a delete timer |
| `archive` | The destination is unset or unreachable — **nothing is being backed up** |
| `archive writable` | The folder cannot be written to; a read-only mount passes every other check while saving nothing |
| `manifest` | An archived project has no recoverable identity and could not be placed on a new machine |
| `session hook` | No hook, or **two archivers installed at once** |
| `sweep` | The scheduled job is missing, or installed but **not running** |
| `secrets` | **A credentials file is sitting in your synced cloud folder.** Fix immediately |

`--json` returns the checks plus a `warnings`/`failures`/`ok` summary and the version
that produced the report, so a monitor does not have to aggregate levels itself.
`--no-write-probe` skips the writability test — the one part of this otherwise
read-only command that writes, creating and deleting a single file in the archive.

### `config` — where the archive lives

```sh
claude-sessions config                              # show current setting
claude-sessions config set-destination <folder>     # persist it
```

It refuses to create the *parent* of the destination: if your sync client is not
mounted, silently making a plain local folder where `G:\` should be is how people end
up believing in a cloud backup that does not exist.

### `completion`

```sh
claude-sessions completion bash > /etc/bash_completion.d/claude-sessions
claude-sessions completion zsh  > "${fpath[1]}/_claude-sessions"
claude-sessions completion fish > ~/.config/fish/completions/claude-sessions.fish
```

---

## Privacy — read this before pointing it at a cloud folder

**Your transcripts are not just questions and answers.** They contain the contents of
files you worked on, absolute paths, command output, environment details, and whatever
you happened to discuss — which for most people includes commercially sensitive work,
and sometimes secrets pasted in passing.

What this tool does and does not do:

- **It copies that material into the folder you choose.** If that folder is a cloud
  drive, your conversations are then subject to that provider's storage, retention,
  sharing and indexing — and to anyone you have shared the folder with.
- **It never transmits anything itself.** There are no network calls, no telemetry, no
  accounts and no third-party dependencies. It writes files to a local path; a sync
  client you already installed does the uploading.
- **Once built, `push` will write an `INDEX.md` at the archive root containing the
  first prompt of every session** so the archive is browsable from a phone. That means
  the opening line of every conversation is readable at a glance by anyone with access
  to the folder.
- **Credentials are excluded by design.** `.credentials.json` and `.claude.json` are
  never copied, and `doctor` fails loudly if it finds either anywhere in your archive.
- **Nothing is ever deleted from the archive** by this tool.

If some projects should never leave the machine, do not run `push` at all. A
per-project exclude list is planned but **not yet implemented** — until it lands,
`push` archives everything under your Claude projects directory. Encryption at rest is
also planned and not yet available; today the archive is plain files.

`doctor` writes exactly one file, a write probe it deletes immediately, to prove the
archive is usable. `--no-write-probe` turns that off.

---

## How the identity matching works

When you bring an archive to a new machine, each project must be matched to a local
folder. Comparable tools make you hand-write a path map. This works it out:

```
1. The project's recorded absolute path        -> if that folder exists here, done.
2. Git remote match                            -> compare origin URLs, normalised, so
                                                  git@github.com:you/api.git
                                                  == https://github.com/you/api
3. A unique folder-name match                  -> exactly one folder named `api`.
4. Ambiguous                                   -> report it and skip. Never guess.
```

**Why not just decode the bucket name?** Because you can't. The flattening is lossy:
`E:\Laravel Applications\innovix`, `E:\Laravel-Applications\innovix` and
`E:\Laravel-Applications-innovix` all collapse to the same bucket name. So the real
path is read from the `cwd` field *inside* the transcript, and cached in a manifest
that travels with the archive.

Git remotes are read by **parsing `.git/config`, never by running git** — see
[SECURITY.md](SECURITY.md) for why that distinction matters.

---

## Works with any cloud

The tool writes to **a plain folder**: Google Drive, OneDrive, Dropbox, iCloud Drive,
Syncthing, a NAS mount, a USB stick. No provider integrations, no API keys, no
accounts — and switching provider means changing one path. This is deliberate, not a
gap. It auto-detects the usual locations; otherwise use `config set-destination`.

---

## Design principles

Enforced by tests, each one from a real failure:

- **Your `.jsonl` files are never modified in place.** The archive only ever receives
  completed files, so a sync client cannot corrupt a live session.
- **A rewritten transcript is re-validated before it is kept.** If any line no longer
  parses as JSON, the output is deleted and the operation aborts.
- **Unknown data is skipped and counted, never fatal.** The transcript format is
  internal to Claude Code and changes between releases. If an upgrade breaks the
  reader, your transcripts are untouched and still resumable.
- **The session-end hook can never fail.** It logs and exits 0 on every error path; a
  failing hook would break the end of your session.
- **Writes are atomic** — temp file, then rename, so a sync client never uploads a
  half-written file.
- **Timestamps are compared with tolerance.** Synced filesystems round mtimes; an exact
  comparison re-uploads the entire archive on every run. This shipped as a bug once.
- **Ambiguity is reported, never guessed.**

---

## Troubleshooting

**`ls` shows nothing.** Claude Code files history by the terminal's working directory,
not your editor's workspace. A multi-root workspace has a separate bucket per folder,
so there is no single "workspace history". Pass `--claude-dir` if you use
`CLAUDE_CONFIG_DIR`.

**`doctor` says the archive is not reachable.** Your sync client is not mounted.
Nothing is being backed up until it is.

**`doctor` warns that buckets have no usable identity.** Those projects have memory
files but no transcripts, so there is no recorded path to match on. They are safely
archived; they just cannot be auto-placed on another machine.

**A session I copied across does not appear in `claude --resume`.** It is in the wrong
bucket - `claude-sessions import` fixes that. Note that since Claude Code
v2.1.223, `claude --resume <session-id>` searches every project on the machine, and
`Ctrl+A` in the picker widens to all projects, so you can often find it without moving
anything.

**Bucket names have a lowercase drive letter (`e--` not `E--`).** Casing follows the
string Claude recorded, not what is on disk. All lookups here are case-insensitive.

---

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md); [DESIGN.md](DESIGN.md) explains the constraints
each command has to satisfy. Security policy: [SECURITY.md](SECURITY.md).

**Especially wanted: macOS and Linux testing.** The author has only a Windows machine.

## License

[MIT](LICENSE) © Md. Asikul Islam
