# Setting up claude-sessions: instructions for Claude

You are Claude Code, helping a user install and set up
[claude-sessions](https://github.com/ashiqfardus/claude-sessions-sync) on this computer. It
backs up their Claude Code conversations, plus the folders Claude works in, to a folder
their sync client (Google Drive, OneDrive, Dropbox, iCloud Drive, Syncthing, a NAS)
already syncs. It can also bring them back on another computer.

Work through the steps in order. **Ask the user before anything that installs software,
changes a file outside the tool's own state, or uploads their files.** Show what a
command would do (`--dry-run`) before running it for real. Keep your messages short and
plain, and say what you did after each step.

## Rules for you

- Never run `import --update-profile`, `import --repoint`, `push --scan-history` or
  `config exclude-folder` unless the user asked for that specific thing after you
  explained it.
- Never delete or move the user's files, conversations or archive folder.
- Never run `prune --yes` or set `keep-days` unless the user asked for it.
- Never paste secrets (tokens, passwords) into commands or messages. If a step needs one,
  ask the user to enter it themselves.
- The archive holds full conversations and work files. Only ever suggest a folder that
  is private to the user. Never suggest a shared or public folder.
- If a command fails, show the error and stop. Don't improvise workarounds that change
  system settings.

## 1. Check what is already there

Run `claude-sessions --version`, or `claude-sessions.exe --version` on Windows.

- If it is installed, run `claude-sessions doctor` and skip to the step its output
  points at.
- If it is not installed, go on to step 2.

## 2. Install the tool (ask first)

Pick the first that applies, and tell the user which one you'll use:

- macOS or Linux with Homebrew: `brew install --cask ashiqfardus/tap/claude-sessions`
- Windows with Scoop: `scoop bucket add ashiqfardus https://github.com/ashiqfardus/scoop-bucket`,
  then `scoop install ashiqfardus/claude-sessions`
- Any system with Go 1.21 or newer:
  `go install github.com/ashiqfardus/claude-sessions-sync/cmd/claude-sessions@latest`
- Otherwise, a release archive from
  https://github.com/ashiqfardus/claude-sessions-sync/releases/latest. Put the binary on
  the PATH.

Confirm with `claude-sessions --version`.

## 3. Choose the archive folder (ask)

Look for synced folders the user already has. List what you find and ask which to use.
Typical places:

- macOS: `~/Library/CloudStorage/` (Google Drive, OneDrive, Dropbox), and
  `~/Library/Mobile Documents/com~apple~CloudDocs` (iCloud Drive)
- Windows: `G:\My Drive` (Google Drive), `%USERPROFILE%\OneDrive`,
  `%USERPROFILE%\Dropbox`
- Linux: `~/Dropbox`, `~/Insync/<account>/Google Drive`, `~/Nextcloud`, a Syncthing folder, a mounted NAS. Google Drive on Linux needs Insync or rclone, never GNOME's Drive view: see "Linux and other sync apps" in the README.

Propose `<that folder>/claude-sessions`. If another computer already uses this tool, use
**the same** folder. Then run:

```
claude-sessions config set-destination "<folder>/claude-sessions"
```

If the folder is in Google Drive, OneDrive, Dropbox or iCloud Drive, it must be
**available offline**. A folder that downloads each file only when it is opened makes
every backup and import crawl. You cannot change this setting yourself: explain it, ask
the user to do it, and wait until they say it is done.

- Google Drive: right-click the `claude-sessions` folder, choose **Offline access**, then
  **Available offline**.
- OneDrive: right-click it and choose **Always keep on this device**.
- Dropbox: right-click it and choose **Make available offline**.
- iCloud Drive: right-click it in Finder and choose **Keep Downloaded**.

## 4. See the state, then back up once

1. Run `claude-sessions doctor` and summarize its warnings in a few lines.
2. Run `claude-sessions push --dry-run`. Show the user a short summary: how many
   conversations, and which work folders with their sizes. Work folders are folders
   Claude wrote files in or `cd`'d into, outside git repos.
3. Point out anything that looks private or bulky, and ask whether to exclude it. Examples:
   client data, database dumps, logs, build output. Exclude with
   `claude-sessions config exclude-folder "<folder>"`. To turn work folders off
   entirely: `claude-sessions config work off`.
4. When the user is happy, run `claude-sessions push`.

Conversations from before this install are not scanned for work folders. If the user
wants those folders too, explain the trade-off before running
`claude-sessions push --scan-history` once: it also reads conversations imported from
other computers, so it is only safe if they trust everyone who can write to the archive
folder.

## 5. Make it automatic

Run `claude-sessions install`. It adds a Claude Code hook, plus a scheduled sweep every
30 minutes. Tell the user to restart Claude Code, or run `/hooks`, so open sessions pick
up the hook.

Claude Code deletes conversations after 30 days by default. Ask whether to keep them:
if yes, add `"cleanupPeriodDays": 3650` to `~/.claude/settings.json`. Edit the JSON
carefully, keep the other settings, and show the change before saving it.

## 6. On a second computer

Do steps 1 to 3 with the **same** archive folder. Then:

1. Run `claude-sessions import --dry-run`. Summarize where each project and work folder
   would go.
2. If a project lives somewhere else on this computer, map it:
   - a project: `claude-sessions config map <bucket> "<local folder>"`
   - a work folder with a different path (for example `E:\SQL` on Windows and `~/SQL`
     on a Mac): `claude-sessions config map-folder "<path from the other computer>" "<local folder>"`
3. Run `claude-sessions import`, then do steps 4 and 5 here too.

`import` never replaces anything already on this computer.
- `--update` refreshes conversations that continued elsewhere.
- `--update-profile` also replaces this computer's CLAUDE.md, rules and skills with
  newer copies. Only use it if the user wants that.

## 7. Finish

Run `claude-sessions doctor`, then give the user a five-line summary:
- the archive folder;
- what is backed up (conversations, and which work folders);
- what runs automatically;
- what they should do on their next computer;
- any warning left.

Link them to the README for details:
https://github.com/ashiqfardus/claude-sessions-sync#readme
