# Changelog

All notable changes to this project are documented here. This project follows
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Changed
- **The panel shows that it is working.** A button you click keeps its place and spins
  until the work it started ends, then shows a check for a moment when it succeeded. While
  any job runs, a thin bar moves across the top of the page, however far you have
  scrolled. With reduced motion turned on, the button says "Working…" and the bar is a
  still line that says what is running.
- **Bring in no longer looks stuck while it looks for your projects.** A bar runs beside
  "Looking for your projects…", a count fills it as the search reports one, and after 10
  seconds with no news it says that an archive folder that downloads files only when
  they're opened can take a few minutes.
- **Commands is in the menu.** The page of every command and where it is in the panel has
  its own link in the sidebar, after Settings.
- **The panel fits phones and tablets.** On a phone the sidebar is a top bar with a Menu
  button that opens every page and what is running. On a tablet the sidebar starts folded
  to icons. Text and fields are larger on a phone, and a page that is still loading shows
  the shape of what is coming instead of a bare "Loading…".
- **Overview shows the archive at a glance.** The archive card lists its conversations,
  work folders, computers and last backup ("13 min ago"; the exact time is in its tooltip)
  as figures that count up when the page opens. Each computer has a status dot, and this
  computer's dot pulses while it backs up. On Storage, the biggest projects are one card
  each on a phone.
- **Overview's Health says first how things stand.** One line, "Everything is fine" or
  "3 things to look at", with the icon and colour of the worst; the items open under it,
  and open by themselves when one is a Problem.
- **Recent activity reads as a timeline.** Each event has an icon for its kind (a backup,
  compressing, bringing in, deleting, a setting, another computer's backup) and says how
  long ago it was ("5 min ago"; the exact time is in its tooltip). These times, and the
  archive card's last backup, move on each minute while the page is open.
- **Storage says what a way to free space would free**, beside its name, where the panel
  knows the amount: compressing old conversations and removing duplicate copies. Each
  way has an icon.
- **Dialogs and messages leave as they came**, a little quicker; with reduced motion they
  fade. On a phone a message no longer covers the bar for the conversations you ticked.
- **Conversations is a list of cards on a tablet too**, and in any window where its table
  would scroll sideways, with Read beside each title.
- **"Open the full log" on Overview goes to the log**, not to the top of Settings.
- `push` writes how far it has got on stderr when it is not a terminal, as `import`
  does; `--json` output, `--quiet` runs, the log and a terminal get none of it.
- `import` and `pull` write how far they have got on stderr when it is not a terminal,
  as `clean` does; `--json` output is unchanged. A terminal still gets its line every
  12 seconds. The text is written at most five times a second, with each stage's start
  and its last count, so a script that reads stdout before stderr never finds stderr
  full.
- `clean --yes` names each copy it removes in `session-sync.log`, with what it freed,
  and each it keeps because the last check failed. From the panel too, which logged
  only a total.

### Fixed
- **Back up now showed no progress, and a first backup showed nothing.** Overview now
  shows what the backup is doing above the cards, with a bar that fills when it reports
  a count, and the computers card says this computer's first backup is running.
- **Preview backup could list thousands of files.** It lists the first 50, then "and N
  more".
- **Delete old conversations: a long preview pushed Delete and Cancel far down the page.**
  The list scrolls in its own box, and the two buttons stay in view.
- **Remove duplicate copies said "0.0 MB" for small copies.** Its sizes use KB, MB or GB
  like the rest of Storage.
- **Another computer's last backup read as an exact time.** The archive keeps it only to
  the hour, so Overview says "About 07:00". This computer's time stays exact.
- **Conversations, Bring in, Settings and Commands fit a phone.** Each row of their tables
  is a card with its details named, and long paths wrap instead of widening the page. On
  Conversations the filters sit two by two, Select all is written above the cards, the
  page buttons are Previous, this page and Next, and the bar for the conversations you
  ticked waits at the bottom of the screen. Buttons in Bring in, Settings and dialogs share
  the width.
- **Conversations keeps still while you work in it.** Ticking a conversation no longer
  pushes the list down: the bar for the ticked ones takes the place of the summary line and
  stays at the top as you scroll. While the list reloads after a filter or a search, its
  rows stay, dimmed, under a line running along the top. On a wide screen the column names
  stay in view as you scroll.
- **Bring in's bar fills as it counts.** With a count the bar fills to it; without one, a
  short segment runs along it.
- **A dialog taller than the window scrolls**, so its title and buttons stay in reach.
- **Bring in said "1 work folder" on every run.** A work folder counts only when something
  in it is filed or updated, as in Activity, and with nothing to bring in the preview and
  the run both say "Nothing new to bring in".
- **Bring in called a project that two folders here match "Not found on this computer".**
  It now says two or more folders here match; Choose folder picks one.
- **Renaming this computer left its old name listed as another computer.** After a rename
  the panel offers to forget the old name. Forget asks first, and the old name's
  conversations stay in the archive.
- **Settings showed another computer's last backup as an exact time.** The archive keeps
  it only to the hour, so it now says "Today, about 07:00".
- **A long path under "Where the other computers' folders go here" widened the page on a
  phone.** It wraps.
- **Back up now showed no progress.** A first backup of a few thousand conversations ran
  for a minute behind a disabled button. `push` now says how far it has got, for the
  panel to show: `copying conversations: 120/2000`, then `looking for work folders`,
  `copying work folders`, `compressing old conversations` and `building readable
  pages`, each with its count. Preview backup says `checking` instead of `copying`.
- **Preview backup counted conversations it did not list.** "Would copy 2,000 files"
  came with an empty list: `push --dry-run` named only work-folder files. It now prints
  a `would copy` line for each file it counts: each conversation, the files in its
  folder, memory, rules and skills, and work-folder files.
- **Bring in sat on "Looking for your projects…" for minutes.** On an archive folder
  that downloads each file only when it is opened, the preview read the other
  computers' work folders one file at a time, each a download, and said nothing while
  it did. It now downloads up to eight at once, however few each work folder holds,
  and the page says how far it has got,
  as in `reading work folders: 40/200`, then `reading conversations: 120/3657`.
- **Bring in read compressed conversations one at a time.** Before filing anything, a
  real Bring in reads the index of each compressed conversation it files; on a folder
  that downloads each file when it is opened, each was a download of its own. Up to
  eight now download at once, as work folders do.
- **Bring in downloaded memory files it did not need.** To tell a memory file that is
  new here from one deleted here, it read the archived copy even when this computer had
  never taken it. It now reads one only when the copy it took is the same size.
- **With automatic backup off, Health and Settings said two things that did not fit.**
  "Only on the schedule" beside "the scheduled backup isn't set up". With neither set
  up they now say one thing: "Automatic backup is off. Nothing is backed up until you
  choose Back up now.", with Set up again.
- **Right after Bring in, Health showed a red Problem.** Conversations just brought in
  from another computer were said not to be in the archive until the next backup. The
  check now counts one as archived when the archive holds it under the other computer's
  folder and it has not been continued here, as a backup does.
- **The archive could be made in your home folder.** Choosing the home folder, or a
  folder of other files such as a sync app's root, as the archive folder put the
  archive's folders among your own. `config set-destination`, and Change folder in the
  panel, now make a `claude-sessions` folder inside it and say so: "Using …: a folder of
  its own inside the one you chose." An archive folder, an empty one and a new one are
  taken as before, and a folder saved earlier is not moved.
- **Rules and skills never settled after Forget.** A forgotten computer's rules and
  skills stayed in the comparison, so Settings kept saying a file differs between your
  computers, and Compare listed it. A computer with no record in the archive is now
  left out. Its copies are not deleted.
- **From a folder instead: a mistyped folder gave only "That didn't work".** The panel
  now says "That folder doesn't exist on this computer."

## [0.7.5] - 2026-11-27

### Fixed
- **0.7.4 left some conversations with no title.** A conversation opened by a request
  from another Claude session is titled by that request again, as on 0.7.3, without the
  lines Claude Code adds around it. One opened by an agent's report, with nothing typed
  in its first 200 lines, is now titled by the first thing you typed after them. To find
  it, `ls`, `push` and the panel read on, up to 8 MB into this computer's copy.
  `ls --archived` still reads only the first 200 lines of an archived copy. An agent's
  report is still never a title.

## [0.7.4] - 2026-11-27

### Fixed
- **A drive root was still named `\` in the panel.** A computer on 0.7.1 wrote `\`
  for `E:\`, and the panel showed that. It now names the project from its path. Its
  key and what keep and prune match on are unchanged.
- **A conversation titled by another agent's report.** A title could still read
  `[Subagent hand-back] The text below is the final report…`. A message from another
  agent or session is no longer used as a title; the next thing you typed is, or none.
- **After an upgrade, the panel kept running the old version.** With its tab still open,
  the old panel never stopped, and the shortcut only opened another tab on it. Opening
  the panel, or running `install`, now stops a panel of an older version and starts the
  new one. A newer panel is left running, so a stale copy of the program cannot stop it. A panel busy with a job is left to finish it; the next time you open the
  panel after that, you get the new version.
- **An upgrade on Windows left a copy of the old program in `.claude\bin`.** `install`
  moves a program that is still running aside, as `claude-sessionsw.exe.old-<number>`
  (about 13 MB), and nothing removed it. `install` and `push` now remove these copies
  once nothing runs them, and the log names each one.

## [0.7.3] - 2026-11-27

### Fixed
- **A file of yours whose name starts with `.tmp-`, in a work folder, could be deleted
  from the archive.** The leftover-file sweep took every `.tmp-` name for one of its own.
  It now removes only the names this tool gives its temporary files.
- **The log names every temporary file an automatic run deletes.** `push` logged
  only "removed 1 temporary file(s) left by an interrupted run". It now logs each path.
  On one laptop that line came back on every push because the same leftover was
  uploaded again each time; this release stops that upload.
- **A new computer's first `push` could upload copies of the conversations it had just
  imported.** `push` never archives an imported conversation that another computer's
  copy already holds, but two cases slipped through. On one laptop the first push
  uploaded about 1.7 GB of copies.
  - A power cut or a killed `import` can leave a half-written temporary file
    (`.tmp-…`) in a conversation's folder. `push` took it for a file no other copy had,
    archived the whole conversation again, and uploaded the temporary file with it.
    Temporary files are now ignored, never uploaded, and `push` and `import` delete
    the ones over an hour old from `~/.claude/projects`.
  - A conversation continued on two computers is in two archive folders, each with
    its own subagents. `import` brings both sets into one folder here, and no single
    archived copy held them all, so `push` uploaded it again. A conversation is now
    left alone when one copy holds every turn and each file of its folder is in some
    copy. `clean` still deletes a copy only for one other copy that holds all of it.

  `claude-sessions clean` lists the copies already uploaded this way in the first case,
  and `clean --yes` deletes them. Of each conversation stored twice it deletes one copy
  and keeps the other, which holds all of it. A copy from the second case holds more
  than any other single copy, so `clean` keeps it, and may not free that space yet.
- **A plugin that fails to install now says why, and is tried twice.** The import showed
  only the first line of claude's output ("Adding marketplace..."), hiding the real error,
  and a slow network could fail an install that worked a moment later. It now retries once
  after 5 seconds, shows the line that names the error, and ends with one line counting
  the plugins left to install by hand.
- **Small work folders no longer show "0.0 MB filed".** Sizes under 0.1 MB print in KB, and
  under 1 KB in bytes.
- **Conversation titles showed the text Claude Code wraps around a message.** A
  conversation opened from Claude in Chrome, an IDE, a paste, or a message from another
  session was listed in `ls`, the panel, INDEX.md and the HTML pages as
  `<ide_opened_file>The user opened the file…` and the like, and a message from another
  session put that session's id in the title. Titles now leave those wrappers out and
  show what you typed. A conversation opened with a slash command and never typed in had
  no title at all; it is now listed by the command, as in `/review 123`. One opened with
  only a paste is listed by the paste's first line. Each computer corrects the titles of
  the conversations it still has on its next push, and the HTML pages are rendered once
  more for it. A conversation already gone from the computer that backed it up keeps its
  old title in INDEX.md and the panel.
- **A project at the root of a drive was listed as `\`.** The panel showed the
  conversations of a project at `E:\` under the name `\`, and put every drive root of
  every Windows computer in that one project. It now shows `E:\`, and a project at `/`
  shows `/`. The name is corrected on that Windows computer's next push.
  A keep entry saved under the old name `\` still protects every drive root, and one
  saved as `E:\` is honoured by computers still writing `\`.

## [0.7.2] - 2026-11-26

### Added
- **`import --no-profile` brings your conversations without your rules and skills.** It
  leaves CLAUDE.md, skills, agents, commands and the other profile files here as they
  are, as `--no-work` leaves work folders and `--no-plugins` leaves plugins. Plugins
  still follow `--no-plugins`. It cannot be combined with `--profile-only` or
  `--update-profile`.
- **`import` says how far it has got on a slow archive folder.** On a drive that
  downloads each file when it is opened, such as Google Drive streaming, `import` and
  `import --dry-run` could read for many minutes with nothing on the screen. In a
  terminal, a line such as `still reading the archive: 812 of 3657 conversations
  (2m10s)` now appears at most every 12 seconds. Piped or scripted output is unchanged.

### Changed
- **`import --dry-run` no longer downloads your conversations to preview them.** It
  opened every compressed conversation and listed every conversation's folder, then
  printed nothing it found there. On Google Drive streaming that took 15 minutes for a
  4.7 GB archive. It now reads only what its report needs, and prints the same report.
  A real import also writes rewritten conversations in larger pieces, which is faster
  on a synced drive.
- **`import` is several times faster when many folders moved.** Each line of a
  conversation was checked against every moved folder's patterns in turn: 120 of them
  for 24 folders. A pattern now runs only on a line that could hold its folder's path.
  The result is the same, and in a benchmark of 120 patterns the check went from about
  1.4 MB/s to 94 MB/s.
- **`import` reports the other computers' work folders when it places them, before the
  conversations.** A real import unpacks them first, and it can take minutes, but it
  only reported them after every conversation line. A dry run lists them in the same
  place.

### Fixed
- **`import --dry-run` put a Desktop, Documents or Downloads folder in a different place
  from the real import.** When another computer's work files make that folder here,
  only the real run matched the folder's conversations to it. The dry run said "no
  folder named "Desktop" under the search roots". The dry run now places them as the
  real run does.
- **`import --dry-run` counted far fewer work-folder files than the real import
  unpacks.** It counted only the files stored loose in the archive and skipped the
  files inside each packed folder. It now counts every file that would land. Both runs,
  and `pull`, also give the unpacked size, as in `; 464.5 MB filed`.
- **`import --dry-run` read as if it had already filed conversations.** It said "N
  conversation(s) here still point at other machines' work folders". A dry run now says
  "after this import, N conversation(s) would still point at other machines' work
  folders; run claude-sessions import --repoint then".
- **A remote Go cannot read as a URL, with nothing after its host, kept its password
  in printed labels.** Since 0.7.1, `config keep` and the panel showed
  `https://user:p%zz@host` as it was written; 0.7.0 showed `https://host`. The
  password is dropped again, and an `@` in a path, as in `https://host/o/100%/r@v2.git`,
  still stays.
- **The panel's Health gave the wrong reason when this computer's settings file could
  not be read.** It said compressing old conversations waits until every computer's
  record in the archive can be read. It now says this computer's settings file can't be
  read and to choose the archive folder again.

### Documentation
- **The setup guide covers what a first-time setup on a new Windows laptop ran into.**
  The Google Drive letter can differ from `G:`. It now shows what the archive folder
  holds, how to install Scoop (it needs git), and where `session-sync.json` lives. It
  says to make the archive folder available offline before the first import, and what
  `import` brings besides conversations, with a flag to skip each part.

## [0.7.1] - 2026-11-26

### Fixed
- **`doctor` warned about mixed versions on computers running the same release.** A
  release download says `0.7.0` and a `go install` build said `v0.7.0`, so `doctor` and
  the panel's Health named the other computer. They now count as the same version, and
  a `go install` build reports itself without the `v`.
- **A kept remote whose path holds a space or a non-ASCII letter could not be unkept by
  its label.** `config keep` printed `https://host/o/%C3%BC/repo.git` for a remote
  stored as `https://tok@host/o/ü/repo.git`, and `config unkeep` with that label, or
  the panel's Stop protecting, answered "not kept". The label now keeps the path as
  written.
- **Two different repositories could count as one.** For a remote Go cannot read as a
  URL (a `/` in a password, a bad `%`) with an `@` in its path, everything before the
  last `@` was taken for a password, so `https://host/o/100%/r@v2.git` and another
  remote ending `@v2.git` both became `v2`. Only an `@` before a host and a `/` now ends
  the credentials.
- **`config set-destination` and `install --archive` could refuse a working sync folder**
  as one that "cannot replace a file by renaming", when a sync client or antivirus held
  the test file for more than about 0.3 s, or a cached read showed the old bytes just
  after the rename. The check now waits about a second and reads again.
- **With two Insync accounts that each hold a `claude-sessions` folder, the tool picked
  one by itself** when no folder was saved. It now picks neither, as it already did for
  two accounts under `~/Library/CloudStorage`; `config set-destination` chooses.
- **A conversation that once failed to zip was never tried again while it stayed the
  same,** so a moment when a sync client held a file cost its space for good. A failed
  zip or unzip is now tried again after a week. A conversation over the size limit, or
  one whose two copies differ, still waits until it changes.
- **`doctor` said idle conversations are zipped when it could not read
  session-sync.json.** `push` zips nothing then, and `doctor` now says so with the
  error.
- **With no home folder to be found, `doctor` printed only its header** and the panel's
  Health showed nothing wrong. It now fails a `claude root` check that names the
  problem, and `doctor --json` prints that report.
- **`config unkeep <remote>` for a remote not on the keep list answered "no archived
  project has the path ..."** It now says the remote is not on the keep list.
- **A Windows folder written `C:/x` in one place and `C:\x` in another counted as two
  folders,** as for a project with only memory files, whose path comes from
  `~/.claude.json`. Paths now compare with either slash.
- **`ls --archived --limit N` could list fewer than N conversations** when one of the
  newest was a zip that cannot be read. The limit now counts the conversations listed.
- **On Linux the panel's app menu entry ignored `XDG_DATA_HOME`.** `install` wrote it
  to `~/.local/share/applications` even when the desktop looks elsewhere, so the menu
  did not list it. It now goes to `$XDG_DATA_HOME/applications` when that is set, and
  `uninstall` also removes one an earlier install left in the old place. An entry is
  taken for this tool's only when it holds the tool's marker line, not a mention of it.
- **A shortcut that could not be made printed the tool's error over several lines.**
  `install` now prints it on its one `shortcut  FAILED:` line, and `install --json`
  gives the same text.
- **The archive's index dated a conversation whose zip could not be read by when its
  page was last written,** not by when it was last used. It is now dated by its zip.
- **`push --dry-run` left out conversations a real push zips** when they were archived
  from this computer and their project folder is gone from it. It now lists them too.
- Panel: Storage could show the sizes from before Remove duplicate copies when a slow
  walk of the archive had started before the job ended.
- Panel: on an archive mount that hangs (SMB, NFS, rclone), Overview, Conversations and
  Settings now say the archive can't be reached within 3 seconds instead of loading
  forever.
- Panel: turning a page or changing a filter on Conversations no longer lists the
  archive's folders again; the listings are kept a minute, or until a panel job ends.
  A cold Settings load compares rule and skill copies once, not three times.
- Panel: a cookie of the panel's name set by a page on another local port no longer
  locks the panel out.
- Panel: the Read page no longer shows an "all sessions" link that led to a blank page.
- Panel: Recent activity names a conversation deleted from another computer by its
  title (or its project folder), not "Conversation 1d7cd94c", and still shows the
  older entries right after the log is rotated.
- Panel: the folder chooser hides folders Windows marks hidden, such as AppData, and
  answers quickly in a folder with thousands of subfolders.
- Panel: Health says the scheduled backup can't be checked when the system can't be
  asked, as Settings does, rather than that it isn't set up.

## [0.7.0] - 2026-11-26

There is no 0.6.0: its compression, deleting and several-computer work ship here with the
browser panel.

Upgrading: update every computer and let each push once. Zipping, `prune --yes` and the
age rule wait until all of them have, and `doctor` names any they wait for. The
panel's compression and deleting wait for every computer too, as the CLI does.

### Added
- **`claude-sessions ui` opens a panel in your browser.** It runs on `127.0.0.1` only and
  opens through a one-time address; `--no-open` prints it, `--port <n>` picks the port,
  and a second `ui` opens a new tab on the running panel. Every action runs the CLI's own
  code, one at a time. Ctrl-C stops it at once; "Stop the panel" does too, or after the
  running job. Screens:
  - Overview: every computer, Back up now and Preview backup, health (a light check;
    "Run full check" runs `doctor`), storage and recent activity.
  - Conversations: search with filters, a paged table, Read, and Never delete or delete
    for the ticked ones.
  - Storage: usage, compress, duplicates, delete old conversations, Never delete and
    recently deleted, and the biggest projects.
  - Delete old conversations: delete automatically, choose now, Never delete and Restore.
  - Bring in: where each project goes, what to bring in, Advanced, Preview, and From a
    folder (`restore`).
  - Settings: archive folder, computers, work folders, folder mappings, automatic backup,
    rules and skills (including bringing in only rules and skills), readable pages, the
    full backup file, log, About and Stop the panel.
  - Commands: every command and option and where to find it in the panel. `completion` and
    `ui`'s own launch options stay in the terminal.
- `--json` on `pull`, `restore`, `clean`, `prune`, `backup`, `config`, `install`,
  `uninstall` and `machines --forget`. `import --json` now lists work folders too, as
  rows with `kind: "work"`, and each row names its computer (`machine`). `config
  --json` gives the lines it would have printed (`lines`), and for `show` the settings
  with their defaults filled in (`view`). `install` saves the sweep's interval in
  `session-sync.json` (`sweepEvery`, -1 with `--no-sweep`), and `config show --json`
  reports it.
- `doctor` checks that the archive folder keeps file times and can replace a file by renaming, and recognises GNOME's Google Drive view, which is not a normal folder. `config set-destination` and `install --archive` refuse a folder that fails these checks.
- With no archive folder saved, the tool also finds one in `~/Nextcloud`, `~/ownCloud`
  and Insync's `~/Insync/<account>/Google Drive` or `My Drive`.
- The README explains which sync apps work on Linux.
- `install` adds a claude-sessions panel shortcut (Start menu, ~/Applications, or the Linux app menu) that opens the panel and closes it 10 minutes after its last tab; `uninstall` removes it.
- `import --profile-only` brings in only the newer rules and skills.
- **`render --include <project>`** publishes a project's pages again, on every computer.
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

### Fixed
- **`install` named one hook of two.** It said `hook      SessionEnd (new)`, but it adds a
  Stop hook too, which backs up after each reply; `uninstall` then removed 2 entries. It
  now says `SessionEnd + Stop`.
- **`install --no-sweep` also removes a background sweep installed earlier.**
- **A git remote holding a token with `@`, `/` or `?` in it, or a query, now matches
  the same repository's plain remote** wherever remotes are compared: `import`, `config
  keep`, `prune --project` and the panel.
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
