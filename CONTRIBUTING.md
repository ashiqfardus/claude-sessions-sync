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

**Real-desktop macOS and Linux reports.** CI installs, runs and uninstalls on all
three platforms every push, and the sweep registers on each of them - verified, not
assumed. What a runner cannot tell us is whether it survives a logout, a reboot, or a
machine asleep for days. That is the gap only a person on a real desktop can close.

Also useful: transcripts that break the reader (describe the shape, do not paste
private content), and any Claude Code release that changes the on-disk format.

## Pull requests

Small and focused. Explain *why* in the description — the constraints above mean a
change that looks obviously correct can still be wrong. If it touches how data is
written, say what happens when it is interrupted halfway.

## Security rules that are not negotiable

Added after a review found these before they shipped. See [SECURITY.md](SECURITY.md)
for the reasoning:

9. **Never execute `git` in a directory found by scanning.** A repository's own
   `.git/config` can make git run arbitrary commands. Use `internal/identity.Remote`,
   which parses the config file instead.
10. **Resolve system binaries by absolute path**, via `internal/hostagent.systemBinary`.
    `PATH` lookup is a privilege-escalation vector on Windows.
11. **`doctor` must fail, not warn, on credentials found in an archive** — and it walks
    the whole tree, because a recursive copy puts them inside a bucket, not at the root.

## Gotchas that have already cost time

- **Go's regexp is RE2: no backreferences.** `(?s)<(a|b)>.*?</\1>` does not compile.
- **A literal UTF-8 BOM in a Go source file is a compile error** anywhere but byte
  zero. Build one from bytes (`string([]byte{0xEF, 0xBB, 0xBF})`).
- **Transcripts can start with a BOM**, and that first line carries `cwd` - so failing
  to strip it costs the project's identity, not just one entry. There is a fixture for
  this in `internal/claude/testdata`.
- **`.gitignore` negations are anchored.** `!testdata/**/*.jsonl` only matches at the
  repository root; nested fixtures need `!**/testdata/**/*.jsonl`.
- **Windows will not rename onto an existing file**, so atomic writes need the remove
  and retry that `archive.WriteFileAtomic` implements.

## Run this before pushing

```sh
gofmt -l .            # must print nothing
go test ./...
for os in linux darwin windows; do GOOS=$os go vet ./...; done
```

**That last loop matters more than it looks.** `go build` ignores `_test.go` files, so
a test referencing something declared only in a `//go:build windows` file compiles
happily on Windows and breaks the macOS and Linux CI jobs instead. `go vet`
type-checks tests, so running it per platform catches it before you push. This has
already happened once: `ownsAction` lived in `install_windows.go` while its test did
not, and CI failed on two of three platforms while everything looked fine locally.

Anything genuinely platform-independent — the plist, the systemd units, the cron line,
the schtasks arguments, code-page and result-code handling — belongs in a file with no
build tag, so it can be tested everywhere. See `internal/hostagent/spec.go`.
