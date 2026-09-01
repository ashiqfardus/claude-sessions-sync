# Security policy

## Reporting a vulnerability

Please report security issues privately, **not** as a public GitHub issue.

Use [GitHub's private vulnerability reporting](https://github.com/ashiqfardus/claude-sessions-sync/security/advisories/new)
on this repository. If that is unavailable, open an issue saying only that you have a
security report and asking for a contact address — no details.

This is a small project maintained by one person; expect an acknowledgement within a
week rather than within hours.

## What this tool touches

Understanding the blast radius helps when judging severity:

- It reads **every Claude Code conversation on the machine** — including whatever file
  contents, paths, credentials-in-prose and commercially sensitive discussion those
  transcripts happen to contain.
- It **writes copies of them to a folder that is usually synced to a cloud provider**,
  where they may be shared, indexed, or retained beyond your control.
- Once `import`/`restore` land, it **scans directories across the machine** looking for
  project folders, and **writes files into `~/.claude/`**.
- `install` **modifies `settings.json`** and **registers a scheduled job** that runs
  this binary automatically.

Anything that widens what is read, copies data somewhere unintended, or causes
execution during a scan is in scope and should be treated as high severity.

## Design rules that exist for security reasons

These are enforced in code and in review. A change that breaks one is a vulnerability,
not a style regression:

- **Never execute `git` in a directory the user did not create.** A repository's own
  `.git/config` can set `core.fsmonitor`, `core.pager`, `core.sshCommand` or an alias,
  and git will run them — so scanning a drive for candidate project folders would mean
  executing code from every repository ever cloned onto the machine. `internal/identity`
  therefore **parses `.git/config` directly and never invokes git**.
- **Resolve system binaries by absolute path.** `exec.Command("schtasks", ...)` searches
  `PATH`; a writable directory earlier in `PATH` is silent code execution on Windows.
  See `internal/hostagent/exec.go`.
- **Never read or copy `.credentials.json`.** `doctor` treats its presence in an archive
  as a failure, and walks the whole tree rather than only the root.
- **Never copy `.claude.json` to an archive.** It may be read locally for project paths.
- **No third-party dependencies.** The module is standard-library only, which keeps the
  supply chain to Go itself.

## Out of scope

- The security of the cloud provider you point the archive at. The tool writes to a
  plain folder by design; choosing and securing that folder is yours.
- Anything requiring an attacker who already has write access to your `~/.claude`
  directory or your `settings.json` — at that point they can run arbitrary hooks
  without this tool's help.
