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
- `import` **scans directories across the machine** looking for project folders, and
  `import`, `pull` and `restore` **write files into `~/.claude/`** — transcripts and
  memory read from the archive, which other machines write. `import` and `pull` also
  write global rules (`CLAUDE.md`), skills, agents and commands from the archive's
  `profile/`, and **run `claude plugin install`** for plugins another machine enabled.
- `install` **modifies `settings.json`** and **registers a scheduled job** that runs
  the copy of this binary in `~/.claude/bin` automatically. On Windows the job runs
  `claude-sessionsw.exe`, a copy of the same binary with its PE subsystem set to GUI so
  that no console window opens; nothing else in it differs.

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
  as a failure. It walks the whole archive, every folder at every depth, skipping only
  the rendered pages in `html/`: memory and the session folders that hold subagent
  transcripts sit four and five levels down, where an earlier depth-limited check never
  looked.
- **Never copy `.claude.json` to an archive.** It may be read locally for project paths.
- **No third-party dependencies.** The module is standard-library only, which keeps the
  supply chain to Go itself.

## The archive is a trust boundary

The archive is written by every machine that pushes to it, and by anyone the synced
folder is shared with. So everything read from it — transcripts, memory, manifests,
index files, `render-exclude.txt`, file and folder names — is untrusted input, even on
the machine that wrote most of it. A way round any of the following is in scope, even
though it needs write access to the archive. Enforced in code:

- **Bucket, session and machine names are validated** before they are joined into a
  path that is written or deleted: no separators, no `..`.
- **A path from the archive or from a transcript is never opened unless it is a local
  absolute path** — a drive letter on Windows, rooted at `/` elsewhere. On Windows even
  checking whether `\\host\share` exists connects to that host and sends it the user's
  NTLM hash, and device paths (`\\.\`, `\\?\`) reach things that are not files. Folders
  you name on the command line are yours to choose and are not filtered.
- **Symlinks in the archive are never followed** by `import`, `pull`, `restore` or
  `render`. One in place of a memory folder would copy local files into memory and out
  again on the next push; one in place of a page folder would let `render` delete files
  elsewhere.
- **Memory is only added, never replaced** unless `--update` is given, and never by a
  file dated in the future. Memory is loaded into every session as instructions.
- **Rules, skills, agents and commands follow the same rule**, and are regular files
  only: dot-files, `node_modules` and links are neither archived nor restored.
- **Plugins from the archive are installed by id and source, validated first**: plugin
  and marketplace names must be plain names, sources a GitHub `owner/repo` or an
  `https://` URL, nothing that starts with `-`, and no shell is involved. Anyone who can
  write the archive can still name a plugin to install; `--no-plugins` turns this off.
- **Path moves read from a manifest must be absolute paths.** A move from `"e"` would
  otherwise rewrite every `e` in every imported transcript, and the result would still
  parse.
- **Work folders: a conversation from the archive names no folder for push to
  upload.**
  - `import`, `pull` and `restore` record each transcript they file, journaled before
    it lands, in `session-sync-base/`. Push looks for work folders only in what was
    written after that mark.
  - The first run marks every conversation already on the machine.
  - Nothing in this record is read from the archive.
  - Links planted under `work/` are never written or deleted through.
  - Limits:
    - A fork Claude Code makes of an imported conversation (`--fork-session`) copies
      its lines into a new conversation, which push reads as the machine's own.
    - `push --scan-history` reads every conversation in full, imported ones included.
    - The archive reaches you anyway (above): through plugins, unless `--no-plugins`,
      and through rules and skills, which are added but never replace yours without
      `--update`. Keep the archive private.
- **Rendered pages carry a Content-Security-Policy that forbids scripts**, and their
  links are relative and escaped, so a folder named `javascript:...` is inert.
- **Text from the archive is sanitised before it is shown**: control characters are
  stripped before anything reaches the terminal, and Markdown syntax is escaped before
  anything reaches `INDEX.md`.

What is **not** done: the archive itself is not redacted. Credentials are masked only
in the rendered pages and in the prompts `INDEX.md` lists; the `.jsonl` transcripts are
copied byte for byte. Nor does the tool add any access control: files in the archive
are as readable as the folder they are written to allows. Keep the archive in a folder
only you can read, and treat sharing it as sharing every conversation in it.

## Out of scope

- The security of the cloud provider you point the archive at. The tool writes to a
  plain folder by design; choosing and securing that folder is yours.
- Anything requiring an attacker who already has write access to your `~/.claude`
  directory or your `settings.json` — at that point they can run arbitrary hooks
  without this tool's help.
