# Changelog

All notable changes to this project are documented here. This project follows
[Semantic Versioning](https://semver.org/).

## [Unreleased]

Upgrading: update every computer and let each push once. Zipping, `prune --yes` and the
age rule wait until all of them have, and `doctor` names any they wait for.

### Added
- **`claude-sessions prune` deletes chosen conversations from the archive.** Choose them
  with `--older-than <N>d`, `--project` or conversation IDs; without `--yes` it only
  lists them, with their size. It deletes every copy, loose or zipped, in each
  computer's folder for the project, with its subagent and tool-output files, forks and
  page, and records it in `deleted/<computer>.json` so no computer uploads it again. A
  conversation continued since comes back, and `prune --undo <id>` takes one off the
  list. `config keep` puts a conversation, or a project on every computer, out of its
  reach; a project kept by its git remote is kept by its folder names too, so it stays
  kept once its folder is gone or its remote changes. `config keep-days <N>` (90 or
  more, off by default) deletes conversations older than N days by itself, at most once
  a day on each computer, in the sweep or a `push` from a terminal, never in the hooks;
  it counts days by no later than 25 hours after the newest other computer's last push,
  so a clock running ahead deletes nothing young, and a computer off for a long time
  pauses deletion of conversations newly due (`machines --forget` one no longer used).
  A computer's first run of it, or after it is changed, only notes the time, so it
  starts a day later, and setting it shows the keep list and the `prune --older-than
  <N>d` that previews it. It deletes nothing while a sync app's conflict copy of
  `retention.json` sits beside it. Nothing on any computer is touched. A project
  folder that is gone keeps the git remote it last had in the manifest. Deleting
  waits until every computer runs this version and has pushed once; the older
  PowerShell scripts are not supported alongside pruning and do not hold it back, so a
  computer still running them uploads its own copies of pruned conversations again.
- **Idle conversations are zipped.** An archived conversation unchanged for 30 days, and
  on this computer too, becomes one file, `projects/<project>/<session-id>.conv.zip`,
  with its subagent and tool-output files: usually about a third of the space, and one
  file for the sync app instead of dozens. Push reads the zip back and checks every turn
  before it deletes the loose files. A conversation over 250 MB stays loose, and
  `session-sync-base/zip-skipped.json` remembers it, and a zip or unzip that keeps
  failing, so the log says so once. Pages stay as they are. The sweep and `push` from a
  terminal zip; the hooks never do, and the backup after each reply leaves a continued
  zipped conversation to the next full push. Push also removes what a run killed while
  unpacking a zip left in the system's temp folder. `push --dry-run` lists what it would zip, and
  `config zip-sessions off` stops it. Zipping waits until every computer that pushes to
  the archive runs this version and has pushed once, as an older one would upload the
  conversation loose again; `doctor` names the computers it waits for. Update them, or
  `machines --forget` one no longer in use. The older PowerShell scripts are not
  supported alongside zipping.
- **A warning when two computers use the same name.** Each install makes a random ID
  on its first push, keeps it in `session-sync.json` and writes it into its manifest.
  When the archive's manifest for this computer's name carries another ID, `push` logs
  a warning and `doctor` fails, saying to give one of them its own name with
  `config set-machine`. Pushing is never refused.
- **`machines` and `doctor` show when each computer last backed up.** The manifest
  records the hour of the last full push, in UTC, and changes only when that hour does,
  so a sync app keeps no extra version of it per reply. A computer on an older version
  shows "-".
- `pull --json` and `restore --json`; `import --json` also lists each computer's work
  folders (`kind: "work"`) and each project's computer (`machine`).
- **`config`, `install` and `uninstall` take `--json`.** `config` prints the lines it
  would have printed, and for `show` the settings with their defaults filled in.
  `install` saves the sweep's interval in `session-sync.json` (`sweepEvery`, -1 with
  `--no-sweep`), and `config show --json` reports it.

### Fixed
- **`install` named one hook of two.** It said `hook      SessionEnd (new)`, but it adds a
  Stop hook too, which backs up after each reply; `uninstall` then removed 2 entries. It
  now says `SessionEnd + Stop`.
- **`doctor` sat silent on a sync folder that streams files.** Where Google Drive
  downloads each file only when it is opened, `doctor` printed nothing, not even a
  header, and waited minutes on the archive. It now prints its header and then each
  check as it finishes. It gives up on an archive check after 20 seconds, shows it as a
  warning, and skips the archive checks after it in one line, so a stuck folder costs
  one wait. When reading one archived file takes over 3 seconds, or a check gives up,
  it says how to make the folder available offline. `import` and `push` print the same hint when their
  first archive read is that slow.

## [0.5.0] - 2026-11-25

Upgrading: update every computer, let each push once, then run `claude-sessions clean`
to see the space duplicate copies take, and `clean --yes` to free it.

### Fixed
- **Imported sessions were archived twice.** A session imported from another computer
  was archived again by this computer's next push, under its own folder for the
  project, so a conversation used on two computers was stored twice: about half of one
  real archive. Push now leaves a session alone when another folder's copy already
  holds every turn of it, and its subagent and tool-output files, and archives it here
  only once it is continued here.
  `session-sync-base/held.json` remembers those, so they are compared again only when
  they change.

### Changed
- **The per-reply hook copies a transcript over 5 MB at most every 30 minutes**, not
  every 5. The sync app uploads the whole file each time, and Google Drive can keep
  every upload as a version that counts toward storage. The session-end hook and the
  sweep still copy it.

### Added
- **`claude-sessions clean`** lists the archived copies of a session that another copy
  holds in whole, with the space they take; `--yes` deletes them, with their subagent
  and tool-output files and their page. Before each deletion it reads both copies in
  full. It never deletes a copy that holds a turn, a subagent turn or a larger
  tool-output file the kept copy lacks, a copy with a fork filed beside it, a copy for
  one in a folder no computer pushes to, or a copy that a computer on an older version
  would upload again: those wait until that computer has updated and pushed. With a
  manifest it cannot read, `--yes` deletes nothing.
- **Let Claude set it up.** `docs/setup-with-claude.md` is a page Claude Code can follow:
  it installs the tool, picks the synced folder, shows what would be backed up before
  anything is uploaded, and sets up a second computer, asking before each step that
  changes something. The README links it from Quick start.

## [0.4.0] - 2026-11-22

Upgrading: update the binary and run `claude-sessions install` again on **every**
machine before the first push that packs anything; a machine still on 0.3.x skips packed
folders (it prints `refused: type` once per pack). To back up work folders from
conversations you had before upgrading, run `claude-sessions push --scan-history` once
on each machine, and only if you trust everyone who can write to your archive. See
"Upgrading" in the README.

### Added
- **Unchanged work subfolders are packed.** A first-level subfolder of a work folder
  whose files have not changed for 7 days, with at least 5 of them, is stored as one
  zip, `<subfolder>.cspack.zip`, so the sync client moves one file instead of thousands.
  A later edit or new file goes up on its own beside it, and the zip is rebuilt when
  those pass 50 files or a quarter of its size and have not changed for 7 days.
  `import` and `pull` unpack it into plain files under the same rules as any other
  restored file, after checking every entry's name, type, size and checksum, and only
  the entries they would file; of a zipped file and a later copy beside it, the newer
  wins. Older versions skip the zips: upgrade every computer.
- **`import --no-work` and `pull --no-work`** bring conversations only and skip other
  computers' work folders. `--no-work` with `--repoint` is refused.
- **An excluded folder is not restored into either.** `config exclude-folder` now also
  stops `import` placing another computer's work folder there, or files below an excluded
  subfolder. The import counts them as "excluded here".
- **Work folders are backed up.** Folders Claude worked in outside `~/.claude` and git
  repositories, such as a notes or scripts folder, are copied to `work/<computer>/` in
  the archive. Only text-like files go in. Images, video and audio, archives, binaries,
  databases, secret-shaped names, anything over 10 MB and every dot file or folder are
  never copied, and a first-level subfolder over 250 MB after filtering is left out
  while the rest of the folder is backed up. On by default; `config work off` stops it. Folders come only from what sessions on this
  computer wrote. A conversation imported from another computer adds none, except
  through a fork Claude Code makes of it (`--fork-session`). Someone who can write the
  archive can still reach you through plugins (`--no-plugins` stops those) and through
  rules and skills, which are only added. Keep the archive private.
- **`push --scan-history`** looks for work folders in every conversation that was here
  before this version first ran, once: after upgrading, or after installing on a
  computer that already has history. It reads imported conversations too: use it only
  if you trust everyone who can write to the archive.
- **`import` and `pull` put work folders back** under your home folder or a folder you
  have mapped. `import` also rewrites the paths in the conversations to match; `pull`
  restores the files at their recorded paths and leaves the conversations alone.
  Restoring only
  adds files, and never writes dot files, system or startup folders, code folders or
  git repositories.
- **`config map-folder`, `unmap-folder`, `work-placement`, `exclude-folder` and
  `unexclude-folder`.** `exclude-folder` also removes this computer's archived copy.
- **`import --repoint`** fixes conversations already on this computer that still name
  another computer's folders. It is opt-in, skips conversations touched in the last
  hour and backs each one up first.
- **`doctor` has a `work folders` check:** it warns when the archived work folders
  total over 500 MB or a subfolder was too large to back up.
- **`import --update-profile` and `pull --update-profile`** replace rule and skill files
  here with newer archived copies, keeping this computer's in `profile/replaced/` first.

### Changed
- **`--update` no longer replaces rule and skill files.** It refreshes sessions and
  memory; a `CLAUDE.md` or skill already here is only replaced with `--update-profile`.
  On the first two-computer run, a Windows `CLAUDE.md` of 2.6 KB would have been
  replaced by the Mac's shorter, later one.

### Fixed
- **A session that only gained metadata looked forked.** Claude Code appends lines that
  add no turn (cost, the last prompt, a hook's output when a session is resumed), and
  the hook lines carry an entry ID. `push` and `import --update` now compare copies by
  their user, assistant and system entries only, so such a copy is behind a copy with
  more turns, never a fork of it, and `import --update` takes the continuation. A
  prompt you queued that never ran still counts as a turn: that copy is kept as a fork
  rather than replaced.
- **Subagent conversations kept the other computer's paths.** `import` now files
  `<session>/subagents/**/*.jsonl` (a workflow's, a level down, too) through the same
  path rewriting as the session itself, for a fork's copy as well, and says when one
  cannot be filed; saved tool output is still copied as it is. `push` and
  `import --repoint` read subagents at any depth too.
- **Windows paths were only half converted on a Mac or Linux.** A path under another
  computer's Windows project, `~/.claude` or backup root kept its backslashes after the
  new prefix (`/Users/you/.claude\backups\x`), which names no file. The whole path now
  takes `/`, for every kind of moved folder. It still stops at a space in the rest of a
  path: `E:\SQL\My Work\x` becomes `/Users/you/SQL/My Work\x`.
- **`import` filed projects into folders that are not here.** A project inside another
  computer's work folder is filed there only when its folder exists once the work
  folders are placed. A repository inside a container work folder (`~/Developer` with a
  few loose files) does not come with it, and is now reported as not found instead of
  getting a bucket of its own that the next push sent back.

## [0.3.1] - 2026-11-21

Upgrading: run `claude-sessions install` again after updating the binary, on every
machine, as for 0.3.0. The first runs after updating render every HTML page again; a
large archive finishes over a few runs, because each run has a time budget.

### Added
- **Install with Homebrew or Scoop.** `brew install --cask ashiqfardus/tap/claude-sessions`
  on macOS and Linux, and `scoop install ashiqfardus/claude-sessions` from the
  `ashiqfardus/scoop-bucket` bucket on Windows. Releases update both through GoReleaser
  when the repository has a `PACKAGES_GITHUB_TOKEN` secret, and skip them without it.

### Changed
- **HTML pages format Claude's Markdown:** bold, inline code, http(s) links, headings
  and pipe tables, instead of showing `**` and backticks as text. Prompts, tool input
  and tool output are still shown exactly as written. Formatting runs on text that is
  already escaped, so transcript content still cannot add markup. Blank lines next to
  a code block are no longer shown. Every page is rendered again once after updating.

## [0.3.0] - 2026-11-20

Upgrading: run `claude-sessions install` again after updating the binary. The hook
and the sweep run the copy in `~/.claude/bin`, so a new build does nothing until it
is installed - and on Windows it is what replaces the sweep that flashed a terminal.
Do it on every machine: mixed versions file forks as ordinary sessions. To go back
to an older version, run `uninstall` with this one first - an older `uninstall`
leaves the per-reply Stop hook in place. The first `import --update` after upgrading
has no record of what each machine already took, so a memory entry or file deleted
before the upgrade can come back once (the same for a rule or skill file). Home folders match only once both machines
have pushed with this version.

### Added (2026-11-20)
- **A session continued on two machines is kept twice instead of half-lost.** Copies
  are compared by entry id rather than by date and size. `import --update` files the
  other machine's copy as a session of its own (a new, stable id), so either can be
  resumed; importing again updates it instead of adding a third, and a machine never
  gets its own copy back. On a shared layout `push` keeps the archived copy and saves
  this machine's beside it under `forks/`, where it used to overwrite one or silently
  drop the other.
- **Per-reply backup.** `install` adds a `Stop` hook running `push --quiet --from-hook`:
  the session that just replied is archived at once, so a machine that dies mid-session
  loses minutes rather than up to 30. Transcripts over 5 MB are copied at most every 5
  minutes, since a sync client re-uploads the whole file. `doctor` says when an older
  install lacks it.
- **`config map <bucket> <folder>`** says where an archived project lives on this
  machine, for projects nothing else identifies (no git remote, a renamed folder, two
  candidates). Saved per machine, used by every import; `config unmap` removes one.
  `import` prints the exact command for each project it could not place.
- **The home folder matches across machines** (#4): `C:\Users\you` and `/Users/you`
  are filed as each other, with their memory, when that machine's profile was
  `<home>/.claude`.
- **Each machine's rules and skills are kept separately** (#5) under
  `profile/machines/<machine>/`, so a newer copy pushed from elsewhere no longer drops
  this machine's from the backup. `import --update` reports a rule file it replaced
  and saves the local version under `profile/replaced/<machine>/<time>/`, which no push
  writes; `doctor` lists files that differ between machines.
- **Mixed versions are reported** (#8): each machine records its version in its
  manifest, and `doctor` names machines running a different one.
- `doctor` explains a macOS privacy refusal of the background sweep and which setting
  to grant; the launchd agent now keeps its stderr in `session-sync.err.log` (#6).

### Fixed (2026-11-20)
- **Google Drive and OneDrive were not found on current macOS** (#7). They mount under
  `~/Library/CloudStorage`; detection now looks there, and uses a folder only when
  exactly one account matches.
- **`import --update` reported a session as continued on two machines when it had only
  continued here** - the archived copy being older is not a fork.
- **`import --update` dropped the other machine's memory index.** `MEMORY.md` was
  replaced newest-wins, so one machine's entries vanished here and, after the next
  push, from the archive. It is now merged entry by entry (the newer wording wins for an
  entry both have; a future-dated copy is still refused), and any other memory file it
  replaces is kept in `memory/.replaced/<time>/`.

### Fixed in review (2026-11-20)
- A fork kept by one push was overwritten by the next, which saw nothing changed here
  and fell back to newest-and-largest wins.
- A fork copy continued on the other machine never came back: the check for a
  machine's own copy returning skipped it unread. It is now skipped only when it adds
  nothing.
- A fork copy continued here while the other machine continued the original dropped
  the other side and reported it filed. The other side is now filed as a further fork.
- On a shared layout a fork was tagged with the importing machine. The writer of each
  archived session is now taken from the machines' index files, and never this one.
- `pull --update` said it filed the other copy of a forked session and did not. `pull`
  now files through the same code as `import`, `forks/` included.
- `config map` and the home-folder rule were ignored when repointing one project's
  paths inside another project's sessions.
- The doctor check for a macOS privacy refusal could not fire: the refusal's reason was
  not logged, and any other successful push, holding a terminal's grant, cleared it.
  The reason is logged, the sweep now runs `push --sweep` and marks its log line, and
  only the sweep's own success clears the check.
- The per-reply hook walked the archive for temporary files after every reply, did not
  throttle a large forked session's fork copy, and logged a routine skip on every reply
  while the drive was offline.
- macOS detection now prefers an existing `~/Google Drive` archive over a CloudStorage
  one, so a machine that already used it does not switch.
- Entries from the PowerShell tool's `projects.json` keep the machine they name, so
  their forks are recognised when that machine returns - including after pushes
  rewrite the file.

### Fixed in second review (2026-11-20)
- On a shared layout, a session archived by the per-reply hook - which leaves the
  index behind - was forked under the wrong machine: filed twice, and its writer got
  its own conversation back. When no index row matches exactly, the one other machine
  that recorded the session is taken as its writer.
- A deleted `MEMORY.md` entry or memory file came back on every round: the merge was
  a plain union. It is now three-way, against what was last taken from each machine
  (`~/.claude/session-sync-base/`), and a machine's own archived memory is no longer merged back.
- A forked session was re-read in full on every push, and logged as forked after every
  reply. A fork copy that is current is now left alone, and `FORK` is logged once.
- A `config map` entry could lose, at random, to a related repository with the same old
  path. Projects are now repointed first, in a fixed order.
- The per-reply hook gives up after 5 seconds rather than holding up a reply when the
  archive's filesystem hangs.
- `import` reported a fork again on every run, and did not count it as filed.
- A future-dated archived file was taken when there was no local copy yet.
- The home folder is now what each machine records at push, not guessed from a profile
  path ending in `.claude`.
- A session renamed on another machine, with no new turn, kept its old title here:
  entries after the last turn (title, last prompt, cost) now count.
- `config set-machine <name>` pins a machine's name, which a macOS hostname does not.
  It is saved in `session-sync.json`, so the scheduled sweep - which sees no shell
  environment - uses it too.

### Fixed in third review (2026-11-20)
- A fork filed before its writer was known was filed a second time once that machine's
  index caught up, and on a shared layout the writer got its own conversation back.
  The copy filed without a machine is now kept current, and counted as possibly this
  machine's own.
- `config map -Users-...` failed with "flag provided but not defined": every macOS and
  Linux project name starts with "-", and the command import printed for it did not
  work as printed. Names after `map`, `unmap` and `set-machine` are never flags.
- On a shared layout, memory merges read this machine's own earlier push as the other
  machine's, dropping entries and sometimes skipping another machine's memory
  entirely. Each machine now also keeps its own copy in `memory/<machine>/<project>/`,
  and each other machine's is merged on its own.
- A fork the user deleted was filed again on the next import. It now comes back only
  once the other machine adds to it.
- doctor's macOS privacy check could never clear with `--no-sweep` or an older sweep,
  and blamed the sweep for any refusal. Without a marking sweep any successful push
  clears it, and the hint names the run that was refused.
- A per-reply push that gave up was not logged (now at most once an hour), and
  half-written files under `forks/`, `profile/` and `memory/` were never cleaned up.
- With archives in two accounts, macOS detection still fell back to the Google one.

### Fixed in fourth review (2026-11-20)
- A memory entry or file deleted here came back from a machine that had taken it from
  this one, which offered it as its own addition. Every entry and memory file a machine
  has held is now recorded in `~/.claude/session-sync-base/`, and what is in that
  record and missing here stays deleted, whoever offers it back.
- A fork deleted here came back once the sweep had pushed it: it was filed again from
  this machine's own archived copy. It now comes back only if turns were added to it.
- A machine whose name changed (`config set-machine`, or a macOS host name that
  drifted) took its own files in the archive for another machine's: its own
  conversation came back as a fork, other machines filed its forks a second time, and
  its old memory copy was merged as a stranger's. The name is now saved by the first
  push; `set-machine` remembers the old name as this machine's, and records it in the
  manifest, so other machines keep the forks they filed under it.
- `import --update` from an unzipped backup wrote `profile/replaced/` into the backup,
  after which a second import from it restored no rules. Nothing is written into a
  backup now; a replaced local file is kept in
  `~/.claude/session-sync-base/replaced/<time>/`.
- Importing a backup now repoints paths under the old `~/.claude` and matches the old
  home folder, from the backup's `MANIFEST.txt` (which now records the home folder).
- `doctor` missed a background sweep that macOS had refused from its very first run.
  Every log line of a sweep is now marked, not only its PUSH line.
- A sync app's conflict copy of a transcript (`<id> (conflicted copy).jsonl`) was filed
  as a session of its own. Only files named by a session id are filed.
- Machine names read from file names in the archive are printed without control
  characters.
- A rule or skill file deleted here after this machine had it is no longer brought
  back by another machine's copy (a backup still restores everything in it). A plugin
  turned off here is no longer installed because another machine has it on.
- Windows: releasing the lock while a waiting run was reading it left the lock file
  behind, and the waiting run timed out.

### Fixed (2026-11-20, after the README fact-check)
- Importing an unzipped `backup` brought back sessions and memory but not rules,
  skills, agents, commands or output styles, which a backup keeps at its top level
  rather than under `profile/`. import and pull now recognise a backup by its
  `MANIFEST.txt`, restore those, and install the plugins its `settings.json` lists.
  Only a backup is read that way, so an archive's own `INDEX.md` or `README.md` is
  never taken for a rule. The backup's `settings.json` is still not restored: its
  hooks point at the old machine.

### Added (2026-11-17)
- **Global rules, skills and plugins travel with the sessions.** `push` archives
  `CLAUDE.md` and the other top-level `.md` files beside it, `skills/`, `agents/`,
  `commands/` and `output-styles/` under `profile/`, plus each machine's enabled plugins
  and their marketplaces. `import` and `pull` restore them - added if missing, replaced
  only with `--update` and a newer copy - and install each missing plugin with
  `claude plugin install` (`--no-plugins` to skip). claude.ai account skills
  (`skills/synced/`), dot-files, `node_modules` and `settings.json` stay behind.
- `doctor` reports whether the profile is archived.

### Fixed (2026-11-17)
- **Saved tool output pointed at the other machine after an import.** A large tool
  result is saved under `~/.claude/projects/...` and the transcript names it by absolute
  path; import rewrote the project path but not the `~/.claude` in front of it, so on a
  machine with a different home the path led nowhere. Each machine now records its
  `~/.claude` in its manifest, and import repoints it.

### Fixed (review, 2026-09-30)
- **The Windows sweep flashed a terminal window every run.** `install` now also writes
  `claude-sessionsw.exe`, a copy marked as a GUI program, and the task runs that, so no
  console opens and the exit code still reaches the task's last result. `doctor` flags
  a task still running the console build.
- **An import could replace another machine's archived transcript, and that machine's
  new turns were then never archived.** A rewritten transcript kept its source's
  timestamp but usually grew, so the importing machine's next push counted it as newer
  and uploaded it over the original; the original machine's own copy was from then on
  refused as a truncation. Rewritten copies are now dated just before their source.
- **The hook skipped a session whenever another push held the lock** - the sweep, or
  a second session ending - and it then waited for the next sweep, or overnight. An
  unattended push now waits up to 45 seconds (`--lock-wait`).
- **A render killed part-way kept none of its work.** The first render of a large
  archive, or every page after a template change, outlasts the hook's 120 seconds, so
  every session end started again and none finished. Progress is saved every 10
  seconds, and an unattended push renders for at most 45 seconds, leaving the rest for
  the next run.
- **`render --exclude` and `machines --forget` were undone by the next push.** An
  exclusion given to `render` is now saved, like `renderExclude`; `forget` no longer
  deletes pages that the next push rendered straight back.
- **A sync client's conflict copy replaced a machine's manifest or index**
  (`LAP-1 (1).json`, `LAP-1.sync-conflict-....json`); in one test `INDEX.md` fell from
  3,000 sessions to 5. Only `<machine>.json` is read; `doctor` lists the others.
- **Every push rewrote every index page, `INDEX.md`, `projects.json` and the render
  state**, changed or not. Files whose content is unchanged are no longer written, and
  the indexes are dated by their newest session rather than by "now".
- A push hung on a dead network mount stopped the macOS and Linux sweep until reboot.
  The systemd unit now has a one-hour limit, as Task Scheduler does, and an unattended
  push ends itself after 55 minutes.
- `install --archive` saved a relative path as typed (the hook runs in each project's
  folder, so every session was copied into every repository) and a missing drive
  still ended with "Done". Destinations are made absolute and checked like
  `config set-destination`; a relative one saved earlier is reported. `--claude-dir`
  is made absolute too.
- Flags after a positional argument were ignored: `search "pool" --archived` searched
  for "pool --archived". Flags are accepted anywhere, and a command that takes no
  arguments rejects one instead of ignoring it.
- A leading `~` is expanded in paths, as a quoted `"~/dev"` and anything under
  PowerShell reach the program unexpanded.
- `doctor`'s credential scan stopped three levels down, skipping `memory/` and the
  session folders where subagent transcripts live.
- A profile other than the default read the default profile's `~/.claude.json`.
- `push --quiet` exited without a log line on an unknown flag, so a downgrade archived
  nothing, silently.
- `version` printed `dev` for a `go install` build.
- An upgrade that fails halfway puts the old binary back instead of leaving none.
- `CLAUDE_CONFIG_DIR` spelled as an 8.3 short path or a symlink to `~/.claude` no
  longer registers a second sweep for the default profile.
- A sweep run killed by the one-hour limit is reported as a problem, not as healthy.
- `uninstall` says when no sweep was registered for the profile instead of "removed".
- A quoted task argument ending in `\` no longer swallows the arguments after it.
- On Windows, the hook gave up waiting for the lock at the moment it was being
  released, which Windows reports as "access denied".

### Added (review, 2026-09-30)
- **`--update`** on `import`, `pull` and `restore`: brings in sessions that continued
  on another machine - only when the archived copy strictly extends the local one, so a
  session that also continued here is kept and reported - and memory files that are
  newer in the archive.
- **`import` without a manifest**: a bucket no manifest names - a forgotten machine's,
  a PowerShell-era archive's, an unzipped `backup` - is placed by the working directory
  its transcripts record. `backup` now prints that one command as the way back.
- **`doctor` checks that archiving actually happens**: it fails when a session that
  ended over 40 minutes ago is not in the archive, and warns when the last successful
  push is hours old. The hook and the sweep exit 0 by design, so a scheduler's last
  result never showed this. It also fails a hook whose program is missing, and a cron
  entry with no cron daemon running (WSL).
- PowerShell completion: `claude-sessions completion powershell`.
- `import` warns about a `--search-root` that does not exist.
- `session-sync.log` is rotated at 1 MB, and `install` keeps the three newest
  `settings.json` backups instead of one per run.
- `config --help` lists its actions.

### Changed (review, 2026-09-30)
- `ls` shows whole session IDs, which `claude --resume` needs; the search hint includes
  the `cd`, and for `--archived` results says to import first.
- `machines` labels its date column LAST SESSION, which is what it is.
- Pages are rendered once more (template 3).

### Security (review, 2026-09-30)
- **Network and device paths read from the archive or from a transcript are never
  opened.** On Windows, even checking whether `\\host\share` exists sends the user's
  NTLM hash to that host, and a path planted in the shared archive made every push do
  so.
- **Symlinks in the archive are never followed** by `import`, `pull`, `restore` or
  `render`: one in place of a memory folder copied local files into memory and out on
  the next push, and one in place of a page folder let `render` delete files elsewhere.
- **Memory is not replaced from the archive** unless `--update` is given, and never by
  a file dated in the future. Memory is loaded into every session as instructions.
- Path moves read from a manifest must be absolute paths: `"e"` rewrote every `e` in
  every imported transcript, and the result still parsed.
- Pages carry a Content-Security-Policy that forbids scripts, and links are relative
  and escaped, so a folder named `javascript:...` is inert.
- Text from the archive is stripped of control characters before it reaches the
  terminal, and of Markdown syntax before it reaches `INDEX.md`.
- More credentials are masked on pages: PGP and truncated PEM keys, JSON-escaped and
  quoted values with spaces, `DB_PASS=`/`*_KEY=`/`Cookie:`, `hf_`, `npm_`, `ya29.`,
  `SG.` tokens, Azure `AccountKey=`, and URL passwords containing `/`.
- A scheduled task is recognised as this tool's only by the program it runs, not by
  its command line mentioning the name: uninstall could delete a foreign task.
- Temporary-file cleanup stays inside the folders this tool writes.
- GitHub Actions are pinned to commit SHAs, with Dependabot to keep them current, and
  GoReleaser to an exact version.

### Fixed (audit, 2026-09-16)
- **The scheduled sweep could be deleted by a test install.** The job name was
  machine-wide, so `install --claude-dir <scratch>` then `uninstall` replaced and removed
  the real profile's sweep. Jobs are now per profile; the default profile keeps its name.
- **Subagent transcripts and saved tool output were never archived** (`<session>/subagents`,
  `<session>/tool-results`). They are now pushed, pulled, imported and restored.
- **A smaller or older file could overwrite a newer one.** Any size difference forced a
  copy, so a stale copy on a second machine replaced the archived conversation, and
  restore replaced memory edited locally. The newer file now wins, and a transcript is
  never replaced by a shorter one.
- **A killed run blocked archiving for 20 minutes** and left half-written temp files in
  the synced folder. The lock is broken as soon as its process is dead, refreshed while
  held, released only by its owner; leftover `.tmp-*` files are cleaned up.
- **Bucket names used a partial rule.** Now Claude Code's own: every non-alphanumeric
  character becomes `-`, long names get a hash suffix. Paths with `.`, `_` or
  non-ASCII characters were filed where `/resume` never looks.
- **Path rewriting matched prefixes** (`/proj` rewrote `/proj2`), aborted a transcript on
  a pre-broken line or BOM, and abandoned the rest of the project after one failure.
- **Multi-folder workspaces**: paths into sibling repositories a session worked in are
  now repointed on import, matched by git remote; Git Bash `/e/...` paths are handled.
- Manifest conflicts were decided by random map order and "today", and `projects.json`
  fed its own entries back forever; a malformed `projects.json` failed every push.
- `INDEX.md` and the manifest dropped sessions that had aged out locally.
- Hooks and crontab lines containing "claude-sessions" anywhere were treated as ours and
  removed, whole groups at a time; `settings.json` was re-sorted alphabetically.
- `doctor` passed a disabled, failing or foreign sweep; the Windows task did not run on
  battery; Linux paths with spaces broke systemd and cron.
- Search interleaved sessions and cut off the newest; prompts typed while Claude was busy
  were missing; pages did not refresh on growth inside the timestamp tolerance; index
  prompts split multi-byte characters; page times were UTC.
- `push --quiet` exited 1 on an unknown flag; `install` failed while the installed binary
  was running on Windows.

### Added (audit, 2026-09-16)
- **`backup`**: a zip of the whole profile with a manifest, never credentials, and a loud
  warning when it holds no transcripts.
- `ls`, `search` and `stats` take `--archived`; `search --all` includes tool input,
  output and thinking.
- Per-project index pages instead of a 300-session cap.
- Shell completion now covers every command, read from the binary itself.

### Security (audit, 2026-09-16)
- Rendered pages and index prompts mask common credentials.
- Render exclusions are shared through `render-exclude.txt`, so another machine can no
  longer re-publish an excluded project, and `INDEX.md` withholds its prompts.
- Bucket, session and machine names read from the shared archive are validated before
  being joined into paths that are written or deleted.

## [0.2.2] - 2026-09-01

The first release with binaries: v0.2.0 and v0.2.1 were withdrawn because their
release builds failed. Everything since the first commit is listed here.

### Added
- `render --exclude` and a `renderExclude` config key: archiving a project and
  publishing it as a readable web page are different decisions.
- `doctor` reports whether the HTML is current.
- **`render`** - self-contained mobile HTML for every archived session, plus an index,
  run automatically at the end of each push. Tool calls, results and thinking collapse
  behind disclosure triangles; oversized blocks are truncated with a pointer to the
  `.jsonl`; content is escaped before any formatting. No JavaScript and no external
  stylesheet, because these are opened from a cloud folder on a phone.
- **`install`** / **`uninstall`** - a SessionEnd hook plus a periodic sweep (Task
  Scheduler, launchd, or a systemd timer). The hook merge round-trips settings.json as
  a generic document, so every other setting and every other tool's hooks survive, and
  a timestamped backup is written first.
- **`import`** - the round trip is complete. Resolves each archived project to a
  local folder by identity (recorded path, then git remote, then a unique folder
  name), files its transcripts into the right bucket and rewrites the paths recorded
  inside them. Ambiguity is reported and skipped, never guessed, and the output names
  which rung matched.
- **`restore`** - the primitive import calls, for filing a folder by hand. Rewrites
  four path forms and re-validates every line as JSON before keeping the result.
- **`pull`** - blunt same-bucket-name copy for identical layouts.
- **`machines`** - list who has pushed to the archive, and `--forget` a retired
  machine. Shards are now keyed by hostname AND username, so two accounts on one
  machine no longer overwrite each other.
- **`push`** - the tool now actually archives. Copies new and changed transcripts and
  memory to the destination, writes this machine's manifest shard, regenerates
  `INDEX.md`, and takes a lock so a scheduled run and a session-end run cannot
  interleave. `--dry-run`, `--json` (result on stdout, progress on stderr) and
  `--quiet` (hook mode: logs, and never fails) included. Every run appends a line to
  `session-sync.log`.
- Memory-only buckets now get an identity from the `projects` map in `~/.claude.json`,
  closing the unroutable-bucket gap doctor has reported since the first review.
- `doctor --no-write-probe`, and `--json` now returns a summary object with counts and
  the version rather than a bare array.
- `search` — full-text search across every transcript, printing the `claude --resume`
  command for each hit. Archived history is only worth keeping if it can be found again.
- `stats` — session counts, sizes and date ranges per project.
- `config show` / `config set-destination` — persist the archive folder. Without it,
  `doctor` could report a missing destination with no command to fix it.
- `completion` — bash, zsh and fish completion scripts.
- `ls --since` / `ls --until` date filters.
- `doctor` now checks that the archive is **writable**, detects **two archivers
  installed at once**, reports `CLAUDE_CODE_PROJECT_DIR_NAME`, and **exits non-zero**
  when a check fails, so it can be used from monitoring.
- Tests for `internal/archive` (manifest merge, atomic writes, mtime tolerance) and
  `internal/identity` (remote parsing and normalisation).

### Security
- Git remotes are now read by **parsing `.git/config` directly instead of executing
  git**. Running `git` inside directories found by scanning the filesystem is arbitrary
  code execution, because a repository's own config can set `core.fsmonitor`,
  `core.pager` or an alias. This closes the hole before `import` ships.
- System binaries (`schtasks`, `systemctl`, `launchctl`, `crontab`, `loginctl`) are
  resolved by **absolute path**, not through `PATH`, which was a privilege-escalation
  path on Windows.
- The credentials check now **walks the whole archive** instead of only its root, where
  a recursive copy would never have placed the file.

### Fixed
- `doctor` reported a project as missing its rendered page when it had been
  deliberately excluded, and advised running `render`, which could never fix it. It
  now asks the renderer the same question rather than keeping its own copy of the
  rule, and its page and transcript counts agree with each other. `doctor --exclude`
  lets you check the effect of an exclusion without editing your config first.
- **`render --exclude` did not remove pages it had already published.** The flag
  existed to stop a project being readable, reported success, and left every page
  exactly where it was - a privacy control that only appeared to work. Excluding now
  withdraws the pages and the index entry; the transcripts are untouched.
- `machines --forget` re-read every manifest and index file once per page to decide
  what to delete. Retiring a machine with 500 sessions meant 500 full manifest reads
  across a cloud filesystem.
- `doctor` told you to run `render` when nothing was rendered because everything was
  deliberately excluded, and never showed which patterns were in force - so one that
  matched nothing published what you believed was withheld.
- **Rendered pages published every injected `<system-reminder>`** into the archive.
  `contentText` had always stripped them; `contentBlocks`, added for rendering, did
  not - so the plumbing Claude Code splices into a message was written out as readable
  HTML in a cloud folder. Found by a realistic test fixture, not by review.
- Thinking blocks that Claude Code records with a signature but no text were dropped
  silently; a page now says how many were not recorded rather than showing nothing.
- `html/index.html` listed every session ever, the same defect already fixed in
  `INDEX.md`. Capped, with a note about what is not shown, and pages themselves are
  capped so one enormous session cannot produce a file no phone will open.
- A page whose transcript was replaced with OLDER content - which is what `import`
  does when it preserves archived timestamps - was treated as current forever.
- `machines --forget` left the retired machine's pages behind, browsable but absent
  from every listing.
- **`uninstall` deleted a scheduled task it did not create.** The Windows sweep reused
  the PowerShell predecessor's task name, so removing "ours" removed theirs - which
  happened on a live machine during testing. The task is now named `claude-sessions-sync`,
  and uninstall inspects what a task actually runs before deleting it.
- `import` restored transcripts but silently dropped every memory file, and reset
  every timestamp to the moment of import - which also made the next push re-upload
  the whole archive. Timestamps and permissions are now preserved through both the
  plain copy and the rewriting path, and memory travels with the sessions.
- `INDEX.md` was regenerated from the local machine only, so pushing from a second
  machine erased the first machine's listing - the same defect the sharded manifest
  exists to prevent. Index rows now live in `index/<machine>.json` and INDEX.md is
  built from all of them; keeping them out of the manifest also stops every import
  and doctor run parsing session rows it does not need.
- `doctor` reported two archivers installed when only the PowerShell one was, because
  "sync-claude-sessions.ps1" contains "claude-sessions".
- Nothing stopped the archive being set inside `~/.claude`, where each push would
  copy its own output without bound.
- `import` recorded a "different repository" note but never printed it, so those
  projects vanished silently from the text output.
- `push --json` wrote its progress line to stdout, making the output unparseable.
  Progress now goes to stderr.
- `WriteFileAtomic` deleted the destination and retried when rename failed, working
  around a Windows limitation that does not exist - turning a failed write into loss
  of the file already there.
- `<command> --help` exited 1 with "error: flag: help requested".
- `search` sorted only by time, so grouped output reprinted a session header each time
  the listing switched back to it; `search --json` returned null rather than [] when
  nothing matched. `search` also read every transcript twice.
- `-i` replaced by `--case-sensitive`, which can actually be toggled.
- The CI smoke test piped into `grep -q`, which exits at its first match; on Unix the
  resulting EPIPE becomes SIGPIPE and killed the writer with 141 under pipefail. It
  failed on macOS and Linux and could never reproduce on Windows, which has no
  SIGPIPE.
- A UTF-8 **BOM** made the first line of a transcript unparseable — and that is the line
  carrying `cwd`, so a single invisible marker cost the project's entire identity.
- Injected `<system-reminder>` and slash-command blocks are now **stripped** from a
  message rather than causing the whole message to be discarded, so a real question with
  a reminder appended still appears in listings.
- `ls --limit` now orders by modification time before reading transcripts, so it opens
  N files instead of all of them.
- Windows drive detection uses `GetLogicalDrives` instead of probing `A:` to `Z:`, which
  could block for seconds on a disconnected network drive.
- The systemd sweep check distinguishes **installed-but-stopped** from **not
  installed** — previously a broken sweep was reported as absent.
- `.gitignore` no longer excludes test fixtures: the negation was anchored to the
  repository root and silently dropped fixtures in nested packages.
- Minimum Go lowered from 1.27 to **1.21**, which is what the code actually requires.

## [0.1.0] - 2026-09-01

Initial read-only milestone: `ls` and `doctor`, plus the transcript reader, bucket
handling and sharded manifest that everything else will build on.
