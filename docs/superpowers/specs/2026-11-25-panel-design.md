# Browser panel: design

Date: 2026-11-25. Status: ready for the owner's approval.
Mockups (approved by the owner 2026-11-25): https://claude.ai/artifact/EhxAMwwqmLs72xChPRLJ3J.
The mockups are private to the owner; the screens are described below.

## Why

claude-sessions has grown to 18 commands, about 60 flags, and a dozen `config` actions.
The owner wants one place to see and manage everything without remembering commands:
- whether every computer is backing up;
- what the archive holds and what uses the space;
- finding and reading old conversations;
- bringing in another computer's work;
- every setting.

**The owner's rule:** everything the terminal can do, the panel can do too. Only shell
tab-completion (`completion`) stays terminal-only. It also has to be simple enough for
someone who has never used the command line, because the setup page
(`docs/setup-with-claude.md`) brings in people who are not.

## Goals

- `claude-sessions ui` opens the panel in the default browser on this computer.
- Every command and flag in the command map below is reachable from the panel.
- Every action that changes something shows what it will do first, as the CLI's
  `--dry-run` does. Deleting needs an explicit confirmation naming what goes.
- The panel and the CLI always agree, because the panel runs the same code, not a
  second implementation.
- It works with any number of computers: the mockups show three.
- Plain language throughout, in the mockups' wording:
  - "Compress old conversations", not "zip";
  - "Bring in work from another computer", not "import";
  - "Never delete", not "keep list".

## Non-goals

- Remote access. The panel is reachable from this computer only. Phones read the
  existing pages in the sync client, as today.
- A desktop app, a tray icon, or a background service. The scheduled sweep already
  backs up in the background; the panel runs only while it is open (see the shortcut
  under Decisions).
- Accounts or multiple users. It runs as the user who started it, with their files.
- Editing conversations, or changing anything the CLI cannot change.
- Shell completion setup.

## Anti-goals

- **A side door for other software.** Any website, browser extension or local program
  must not be able to make the panel do anything. That covers cross-site requests, DNS
  rebinding and guessable ports.
- **Shortcuts the CLI refuses.** The panel can never delete, overwrite or upload
  something the CLI would refuse, nor skip a check the CLI makes. That includes `clean`'s
  and `prune`'s gates, the lock, and the dry-run-first confirmations.
- **Breaking the install.** No new install step and no runtime dependency: it stays one
  binary installed by Homebrew, Scoop or `go install`.
- **An AI-made look.** No generic dashboard look (see the owner's CLAUDE.md rule):
  follow the mockups.

## Constraints

- Go 1.21 minimum, standard library only (`net/http`, `embed`, `html/template` or
  plain static files). No Node, npm or build step. The page's HTML, CSS and JavaScript
  are hand-written and embedded in the binary.
- **Fonts:** IBM Plex Sans and IBM Plex Mono (SIL Open Font License), embedded as woff2
  subsets, with the licence text shipped beside them. No request leaves the computer:
  no Google Fonts, no CDN.
- Windows, macOS and Linux, with CI on all three as today.
- The repository is public: no real names in code, tests or fixtures.

## Approaches considered (decided with the owner)

1. **A web page that the tool serves on this computer (picked).** It uses the same
   binary and install, and works the same on all three systems. It can later be wrapped
   in a window if ever wanted.
2. **A desktop app** (Wails, Fyne or Electron). It needs a build per system, Apple
   signing and notarization, Windows SmartScreen warnings and a much bigger download,
   and it breaks the simple installs.
3. **A terminal UI.** It is poor at showing conversations and hard for non-terminal
   users.

## How it works

**Starting and stopping.**
- `claude-sessions ui` starts a server on `127.0.0.1` with a random free port. It prints
  the address and opens it in the default browser: `open` on macOS, `xdg-open` on
  Linux, and the Windows equivalent.
- **Flags:** `--no-open` prints the address only, and `--port <n>` picks the port.
- **Stopping:** it stops on Ctrl-C (my call: no idle timeout, because a long import must
  not be cut off). Closing the browser tab does not stop it. The page shows "The panel
  has stopped. Run `claude-sessions ui` again" if the server goes away.

**Security (my calls; they protect the anti-goals).**
- **Loopback only:** it listens on `127.0.0.1` only, never `0.0.0.0`.
- **Host check:** every request must carry `Host: 127.0.0.1:<port>` or
  `localhost:<port>`. Anything else is refused, which blocks DNS rebinding.
- **One-time key:** the address it opens carries a random 32-byte key, used once. The
  server swaps it for an `HttpOnly`, `SameSite=Strict` cookie and redirects to a clean
  address. Requests without the cookie are refused. Another user's process on the same
  computer cannot guess the key.
- **Changing requests:** every request that changes something is a POST. It needs the
  cookie, an `Origin` header that matches the panel's own, and a per-run token header.
- **Strict CSP:** a Content-Security-Policy that allows only the panel's own scripts,
  styles and fonts, with no inline event handlers.
- **Content is escaped:** conversation text, file names and paths come from the archive,
  which is untrusted. They are escaped everywhere and never inserted as HTML. Pages are
  served with `sandbox` CSP and no script.

**One code path.** Each API endpoint calls the same function the CLI command calls, with
the same flags, as a struct.
- **What changes in the commands:** they write their results to an `io.Writer` and
  return a structured result, instead of printing to `os.Stdout` directly. The CLI
  prints that result as today, byte for byte (pinned by the existing tests). The panel
  sends it as JSON.
- **Changing actions** take the same local lock as push, and refuse or wait the same
  way.
- **Long actions** (push, import, pull, restore, render, clean, prune, backup, push
  `--scan-history`) run as jobs. Their log lines stream to the page with Server-Sent
  Events, and the page shows progress and the final result. At most one changing job
  runs at a time.

**Screens** (as in the mockups). The left sidebar can be resized by dragging (180-360
px) or with arrow keys, and remembers its width in `localStorage`. On a phone-width
window it collapses to the top. Every dropdown can be searched.
- **Overview:**
  - the archive and every computer: last backup, version, conversations, status;
  - Back up now, and Preview backup;
  - health, which is `doctor`'s checks in plain words with a fix link each;
  - storage summary and recent activity;
  - a link to Bring in.
- **Conversations:**
  - search the text, with these filters: project, computer, last message, state
    (compressed, never delete, forked, in use), where to look (archive or this computer
    only), whose messages, dates, match case, pattern, and tool output;
  - a paged table;
  - tick several to add them to Never delete, or to delete them from the archive;
  - Read opens the conversation's page.
- **Storage:**
  - one usage bar;
  - three ways to free space: compress (switch), duplicates (Check now), and delete
    old conversations (which opens Delete);
  - Never delete and recently deleted, in summary;
  - the biggest projects.
- **Delete old conversations:** delete automatically (a choice of never, 3 months,
  6 months, 1 year or 2 years), choose now (preview list, then confirm), Never delete
  (protect a project), and recently deleted (Restore).
- **Bring in work from another computer:**
  - where each project or folder will go, with Change or Choose folder for those not
    found;
  - what to bring in, which maps to `--update`, `--no-work`, `--no-plugins`,
    `--update-profile` and `--repoint`;
  - Advanced: the search root, depth, folder-name matching, and pull;
  - Preview first, then Bring in;
  - From a folder, which is `restore`.
- **Settings:**
  - the archive folder;
  - computers: this one's name, the list with Forget, and readiness for compression
    and deleting;
  - work folders: the switch, list, Exclude, Include again, placement, and look through
    older conversations;
  - where the other computers' folders go (`map`, `map-folder`);
  - automatic backup: how often, set up again, turn off;
  - rules and skills (compare, take the newer copies);
  - readable pages (leave out, rebuild);
  - the full backup file;
  - log and About.

**Command map.** The mockups' "Every command in the panel" page is the contract. A test
walks the CLI's command and flag list and fails if one is missing from the panel's
endpoint table, unless it is on a short, named list (`completion`, `--json`,
`--claude-dir`, `--archive`, `--quiet`, `--from-hook`, `--sweep`, `--lock-wait`).
A new flag added later must then either get a panel control or join that list on
purpose.

**States.**
- Every page has a loading, an empty and an error state, in plain words with a next
  step. For example: "No archive folder yet. Choose the folder your sync app keeps in
  sync." Raw errors are never shown.
- Every changing action reports what it did, using the same word as its button: Delete
  3 conversations, then "Deleted 3 conversations".

**Accessibility.**
- WCAG 2.2 AA: contrast as in the mockups, a visible focus ring, and full keyboard use.
- The comboboxes follow the ARIA combobox pattern, and the sidebar handle is an ARIA
  separator.
- Real `<button>`, `<a>` and `<label>` elements throughout.
- Motion respects `prefers-reduced-motion`.

## Testing

- **Go `httptest` tests for:**
  - every endpoint;
  - security: wrong Host, missing cookie, wrong Origin, a reused key, GET on a
    changing endpoint;
  - the job stream;
  - the one-code-path rule: the panel's result for a command equals the CLI's `--json`
    for the same flags.
- **The command-map test** above.
- **A manual browser checklist** in the plan, run on macOS and Windows before release.
  No browser automation in CI, since that would bring in Node.

## Delivery (my call)

Conversation zips, pruning and the several-computers work ship first as 0.6.0, then get
rolled out to all three computers. The panel ships next as 0.7.0: it depends on all of
them, and is large enough to deserve its own review and release.

## Left to the builder

- The API's URL layout and JSON shapes.
- How the page's JavaScript is split into files.
- How each command is turned into a function that takes a writer and returns a result.
- Exact copy for errors and empty states, in the mockups' voice.
- Port selection details.

## Decisions after review (my calls unless marked)

- **"Last backup" for other computers.** The archive does not record it today, so
  0.6.0 adds `lastPush` to each computer's shard, cut to the hour and written at most
  once an hour (see the 0.6.0 spec addendum, item 4).
  - "Backing up normally" means a `lastPush` within the last 2 hours.
  - Recent activity lists each computer's `lastPush`, plus this computer's own log.
- **One code path binds actions, not views.** Read-only views get a small query layer
  built on the same library code (`internal/...`). That layer handles:
  - one row per conversation across computers;
  - project matching by identity, not substring;
  - state, age and date filters, and paging;
  - the deleted list and the rules-and-skills comparison.

  The equality test (panel result = CLI `--json`) covers every changing action. The
  CLI does not grow new listing flags for this.
- **Parity both ways.** Two controls in the mockups have no CLI command today. 0.7.0
  adds both, so the panel still never does what the terminal cannot:
  - `render --include <pattern>` takes a project off the pages' exclusion list;
  - `import --profile-only` brings in only the newer rules and skills, without
    conversations or work folders.
- **Choosing a folder.** A folder browser inside the panel, backed by a read-only
  server endpoint.
  - **What it lists:** folder names only, never file contents. It starts at the home
    folder and the drives, and leaves hidden folders out unless asked.
  - **Also available:** a typed path, always, checked by the server, and the detected
    sync folders (`archive` detection) as one-click choices.
  - **Why not native dialogs:** they would need per-system code, they can open behind
    the browser, and Linux has no built-in one.
- **Flags without a drawn control.**
  - `backup --dry-run` becomes "Preview" beside Create backup file.
  - `restore --include-bare-name` becomes "Match by folder name alone" under From a
    folder.
  - These join the exempt list:
    - `push --no-html`, `doctor --no-write-probe` and `doctor --exclude`, which are
      diagnostics;
    - `install --keep-powershell-hook`, `install --replace-powershell-sweep` and
      `uninstall --keep-powershell-hook`, which only migrate the old PowerShell setup
      on Windows.
- **Pinning CLI output.** Existing tests mostly check substrings. Before any command
  is refactored to return a result, the plan adds exact-output (golden) tests for it,
  so "the CLI prints the same as before" is proven, not assumed.
- **The one-time key never appears in process arguments.** The address handed to
  `open`, `xdg-open` or `start` is a private file (mode 0600, in the user's temp
  folder). That file redirects to the address with the key and is deleted after use.
  Other users on the computer cannot read it from the process list.
- **Opening it without a terminal (the owner's choice, 2026-11-25): a shortcut that
  stops by itself.**
  - **Where the shortcut goes:** `claude-sessions install` adds a "claude-sessions
    panel" shortcut, and `uninstall` removes it:
    - Windows: the Start menu, launching with no console window (the same windowless
      start the sweep uses, `internal/hostagent`);
    - macOS: `~/Applications`, a small app made with the built-in `osacompile`;
    - Linux: a `.desktop` entry in `~/.local/share/applications`.
  - **What it runs:** `claude-sessions ui --from-shortcut`. A second launch while the
    panel is running just opens a new tab on the running one.
  - **When it stops:** the page sends a heartbeat every 30 seconds. Started from the
    shortcut, the panel stops 10 minutes after the last heartbeat, never while a job is
    running. A Stop panel button in Settings stops it at once.
  - **Started from a terminal:** `claude-sessions ui` keeps running until Ctrl-C, as
    above.
