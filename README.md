# claude-sessions-sync

Back up your [Claude Code](https://claude.com/claude-code) conversations to any synced
folder, and put them back on any machine — **matched by project identity, not by file
path**.

```
$ claude-sessions ls
UPDATED           SIZE  PROJECT                 SESSION   FIRST PROMPT
2026-09-01 09:14  630K  C:\Users\Administrator  6487e497  let's continue previous conversation
2026-08-31 17:35  1.8M  E:\airos-frontend       9ee16efa  fix the login redirect loop on refresh
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
   desktop and `~/dev/api` on your laptop, and they are two different buckets.
   `claude --resume` on one machine cannot see the other machine's conversations —
   even after you copy the files across.

This tool fixes all three.

> ### Status: early
>
> `ls` and `doctor` work and are tested. `push`, `pull`, `import`, `restore`,
> `render`, `install` and `backup` are fully designed but **not yet built** — see
> [DESIGN.md](DESIGN.md) for the architecture and build order.
>
> Developed and tested on **Windows**. The macOS and Linux code paths compile and run
> in CI, but have not been used in anger. Please report what breaks.

---

## Install

### With Go (any platform)

```sh
go install github.com/ashiqfardus/claude-sessions-sync/cmd/claude-sessions@latest
```

This puts a `claude-sessions` binary in `$(go env GOPATH)/bin` — add that to your
`PATH` if it is not there already. Go 1.27 or newer.

### From source

```sh
git clone https://github.com/ashiqfardus/claude-sessions-sync.git
cd claude-sessions-sync
go build -o claude-sessions ./cmd/claude-sessions
```

Prebuilt binaries will come with the first tagged release.

---

## Quick start

**1. See what Claude Code has stored on this machine.**

```sh
claude-sessions ls
```

**2. Check whether anything is actually protected.**

```sh
claude-sessions doctor
```

This is the command to run first. It tells you, in plain terms, whether your
conversations are being backed up, whether Claude Code is quietly deleting them, and
whether anything in your archive could not be restored onto a new machine.

Real output:

```
  + claude root      C:\Users\you\.claude (default)
  + projects         5 bucket(s), 3 transcript(s)
  ! retention        cleanupPeriodDays unset, so Claude Code applies its 30-day default
  + archive          G:\My Drive\claude-sessions (session-sync.json)
  ! manifest         3 of 5 archived bucket(s) have no usable identity: e--legacy-app, ...
  + session hook     claude-sessions push --quiet
  + sweep            Task Scheduler: ready, last run 9/1/2026 8:51:04 AM, result 0

  retention: Local transcripts are deleted a month after they are written. Raise it in settings.json.
  manifest: Identity is read from a transcript's cwd, so a bucket holding only memory
            files records nothing. `import` cannot place these.

2 warning(s), 0 failure(s).
```

Every warning comes with a line telling you what to do about it.

---

## Commands

### `claude-sessions ls`

Lists sessions on this machine, newest first. The **PROJECT** column is the project's
real path, read from inside the transcript — not the mangled bucket name.

| Flag | Default | Meaning |
|---|---|---|
| `--project <text>` | — | Only sessions whose path or bucket contains this text |
| `--limit <n>` | `0` (all) | Show at most this many |
| `--json` | off | Machine-readable output |
| `--claude-dir <path>` | `$CLAUDE_CONFIG_DIR` or `~/.claude` | Read a different Claude profile |

```sh
claude-sessions ls --project airos --limit 10
claude-sessions ls --json | jq '.[] | select(.sizeBytes > 100000)'
```

### `claude-sessions doctor`

Checks your setup and reports anything that would cost you history.

| Check | What it means when it warns |
|---|---|
| `claude root` | Where Claude Code's config was found |
| `projects` | How many project buckets and transcripts exist locally |
| `retention` | `cleanupPeriodDays` is unset or low — Claude Code is deleting your history on a timer |
| `archive` | Your synced folder is unset, or not currently reachable — **nothing is being backed up right now** |
| `manifest` | An archived project has no recoverable identity, so it could not be placed on a new machine |
| `session hook` | No `SessionEnd` hook, so only the periodic sweep archives anything |
| `sweep` | The scheduled job is missing, so an abruptly-killed session is never archived |
| `secrets` | **A credentials file is sitting in your synced cloud folder.** Fix immediately |

| Flag | Meaning |
|---|---|
| `--archive <path>` | Check a specific folder instead of the configured/detected one |
| `--claude-dir <path>` | Check a different Claude profile |
| `--json` | Machine-readable output, for monitoring |

### `claude-sessions version`

---

## How the identity matching works

This is the part other tools do not do. When you bring an archive to a new machine,
each project has to be matched to a local folder. Comparable tools make you hand-write
a path map. This works it out:

```
1. The project's recorded absolute path        -> if that folder exists here, done.
2. Git remote match                            -> scan your dev folders, compare
                                                  `git remote get-url origin`,
                                                  normalised so that
                                                  git@github.com:you/api.git
                                                  == https://github.com/you/api
3. A unique folder-name match                  -> exactly one folder named `api`.
4. Ambiguous                                   -> report it and skip. Never guess.
```

**Why not just decode the bucket name?** Because you can't. The flattening is lossy:
`E:\Laravel Applications\innovix`, `E:\Laravel-Applications\innovix` and
`E:\Laravel-Applications-innovix` all collapse to the same bucket name. There is no way
back. So the real path is read from the `cwd` field *inside* the transcript, and cached
in a manifest that travels with the archive.

---

## Works with any cloud

The tool writes to **a plain folder**. That means it already works with:

Google Drive · OneDrive · Dropbox · iCloud Drive · Syncthing · a NAS mount · a USB
stick · anything else that syncs a directory.

There are no provider integrations, no API keys and no accounts to connect — and
switching provider means changing one path. This is deliberate, not a gap.

It auto-detects the usual locations; otherwise pass `--archive '<folder>'`.

---

## Design principles

These come from real failures in the predecessor implementation, and they are enforced
by tests:

- **Your `.jsonl` files are never modified in place.** The archive only ever receives
  completed files. A sync client cannot corrupt a live session.
- **A rewritten transcript is re-validated before it is kept.** If any line no longer
  parses as JSON, the output is deleted and the operation aborts. A transcript that
  does not parse is worse than no transcript.
- **Unknown data is skipped and counted, never fatal.** The transcript format is
  internal to Claude Code and changes between releases; the docs say so. If an upgrade
  breaks the reader, your transcripts are untouched and still resumable.
- **The session-end hook can never fail.** Missing drive, offline mount, unexpected
  error — it logs and exits 0. A hook that fails would break the end of your session.
- **Credentials are never copied.** `.credentials.json` and `.claude.json` are excluded
  by design, and `doctor` fails loudly if it finds one in your archive.
- **Ambiguity is reported, never guessed.** Two folders match and no git remote settles
  it? It tells you and stops.

---

## Troubleshooting

**`ls` shows nothing.**
Claude Code files history by the terminal's working directory, not by your editor's
workspace. A multi-root workspace has a separate bucket per folder, so there is no
single "workspace history". Check `--claude-dir` if you use `CLAUDE_CONFIG_DIR`.

**`doctor` says my archive is not reachable.**
Your sync client is not mounted. Nothing is being backed up until it is. This is
exactly the situation the check exists to surface.

**`doctor` warns that buckets have no usable identity.**
Those projects have memory files but no session transcripts, so there is no recorded
path to match on. They are safely archived — they just cannot be auto-placed on
another machine yet.

**A session I copied across does not show in `claude --resume`.**
It is in the wrong bucket. That is what `import`/`restore` are for (not yet built).
Note that since Claude Code v2.1.223, `claude --resume <session-id>` searches every
project on the machine, and `Ctrl+A` in the picker widens to all projects — so you can
often find it without moving anything.

**Windows: drive letters look wrong in bucket names (`e--` not `E--`).**
Bucket casing follows the string Claude recorded, not what is on disk. All lookups
here are case-insensitive on purpose.

---

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). [DESIGN.md](DESIGN.md) is the architecture
document — read it before adding a command; it explains the constraints each one has
to satisfy and why.

Especially wanted: **macOS and Linux testing.** The author has only a Windows machine.

## License

[MIT](LICENSE).
