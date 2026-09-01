# Contributing

Thanks for looking. This is a small tool with an unusually strict set of constraints,
because it handles data that cannot be regenerated if it is lost.

## Getting started

```sh
git clone https://github.com/ashiqfardus/claude-sessions-sync.git
cd claude-sessions-sync
go build ./...
go test ./...
```

Go 1.27+. No dependencies outside the standard library, and it should stay that way
unless there is a strong reason — a single binary with nothing to install is the whole
point.

Run it against a throwaway profile rather than your real one:

```sh
claude-sessions ls --claude-dir /tmp/fake-claude
claude-sessions doctor --claude-dir /tmp/fake-claude --archive /tmp/fake-archive
```

## Non-negotiables

These are not style preferences. Each one comes from a real failure:

1. **Never modify a `.jsonl` in place.** Rewrites happen on the way to a *new*
   destination. The source is read-only, always.
2. **Re-validate a rewritten transcript before keeping it.** Every output line must
   parse as JSON; on any failure, delete the output and abort.
3. **Parse defensively.** An entry shape you do not recognise must parse and
   contribute nothing. Count what you skip and report the count. The transcript format
   is internal to Claude Code and changes between releases.
4. **The `SessionEnd` hook path must exit 0 on every error.** A failing hook breaks the
   end of the user's session.
5. **Never read or copy `.credentials.json`.** `.claude.json` may be *read* for project
   paths, but never copied to an archive.
6. **Compare timestamps with a ~2s tolerance.** Synced and removable filesystems round
   mtimes, so an exact comparison re-copies every file on every run, forever. This bug
   shipped once already.
7. **Ambiguity is reported, never guessed.** If two folders could be the target and
   nothing settles it, say so and stop.
8. **Writes are atomic.** Temp file in the same directory, then rename. A sync client
   watching the folder must never see a half-written file.

## Tests

Every bug fix gets a test that fails without it. Run `go test ./...` before opening a
PR; `gofmt -l .` must come back empty and `go vet ./...` must be clean.

- `internal/claude` — unit tests for format handling. Add a case here whenever you meet
  a transcript shape that surprises you.
- `cmd/claude-sessions` — end-to-end tests that build the real binary and run it
  against a synthetic profile. Prefer these for anything user-visible.

Test fixtures must be **hand-scrubbed**. Never commit a real transcript: they contain
file contents, paths, and whatever you happened to be discussing.

## Especially wanted

**macOS and Linux testing.** The author has only a Windows machine. The launchd and
systemd code in `internal/hostagent` compiles and runs in CI but has never been used
day to day. If you run either platform, real-world reports are more valuable than
anything else you could contribute.

Also useful: transcripts that break the reader (describe the shape, do not paste
private content), and any Claude Code release that changes the on-disk format.

## Pull requests

Small and focused. Explain *why* in the description — the constraints above mean a
change that looks obviously correct can still be wrong. If it touches how data is
written, say what happens when it is interrupted halfway.
