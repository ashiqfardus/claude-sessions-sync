package main

// mapEntry is one row of the command map: what the terminal says, and where the panel
// does it.
//   - Screen is the screen's route, #/<Screen>; "" for a row that is terminal only.
//   - Place is the row's words as Commands.dc.html has them. It starts with the screen's
//     link text, up to its first comma.
//   - Route is the endpoint the control uses: /api/run/<action> or a GET path.
//   - Covers are the CLI's keys the row accounts for: "<command>", "<command> --<flag>"
//     or "config <action>". TestCommandMapCoversTheCLI checks them against the CLI.
type mapEntry struct {
	Group    string   `json:"group"`
	Screen   string   `json:"screen"`
	Place    string   `json:"place"`
	Route    string   `json:"route"`
	Terminal []string `json:"terminal"`
	Covers   []string `json:"covers"`
}

// mapExempt are the CLI keys the panel has no control for, each on purpose. A bare
// command or flag is exempt for every command:
//   - completion changes the shell, not the backups;
//   - --json, --claude-dir and --archive are what the panel already is: one profile,
//     its archive, results as JSON;
//   - --quiet, --from-hook, --sweep and --lock-wait are the hooks' and the sweep's;
//   - push --no-html, doctor --no-write-probe and doctor --exclude are diagnostics;
//   - the PowerShell flags only migrate the old Windows setup;
//   - ui's launch options cannot be set from inside a running panel;
//   - import --no-profile is the terminal's: the panel's Bring in files rules and skills
//     as it always has, and Settings, Rules and skills takes them on their own.
var mapExempt = []string{"completion", "--json", "--claude-dir", "--archive", "--quiet", "--from-hook", "--sweep", "--lock-wait",
	"push --no-html", "doctor --no-write-probe", "doctor --exclude", "import --no-profile",
	"install --keep-powershell-hook", "install --replace-powershell-sweep", "uninstall --keep-powershell-hook",
	"ui --no-open", "ui --port", "ui --from-shortcut"}

// commandMap is Commands.dc.html's table, in its order and with its words, plus a row
// for each command and flag it does not draw. GET /api/commands serves it.
var commandMap = []mapEntry{
	{Group: "Back up", Terminal: []string{"push"}, Screen: "overview", Place: "Overview, Back up now",
		Route: "/api/run/push", Covers: []string{"push"}},
	{Group: "Back up", Terminal: []string{"push --dry-run"}, Screen: "overview", Place: "Overview, Preview backup",
		Route: "/api/run/push", Covers: []string{"push --dry-run"}},
	{Group: "Back up", Terminal: []string{"push --scan-history"}, Screen: "settings", Place: "Settings, Work folders, Look through older conversations",
		Route: "/api/run/push", Covers: []string{"push --scan-history"}},
	{Group: "Back up", Terminal: []string{"install", "--every", "--no-sweep"}, Screen: "settings", Place: "Settings, Automatic backup",
		Route: "/api/run/install", Covers: []string{"install", "install --every", "install --no-sweep"}},
	{Group: "Back up", Terminal: []string{"uninstall"}, Screen: "settings", Place: "Settings, Automatic backup, Turn off automatic backup",
		Route: "/api/run/uninstall", Covers: []string{"uninstall"}},
	{Group: "Back up", Terminal: []string{"backup --out"}, Screen: "settings", Place: "Settings, Full backup file",
		Route: "/api/run/backup", Covers: []string{"backup", "backup --out"}},
	{Group: "Back up", Terminal: []string{"backup --dry-run"}, Screen: "settings", Place: "Settings, Full backup file, Preview",
		Route: "/api/run/backup", Covers: []string{"backup --dry-run"}},

	{Group: "Bring in", Terminal: []string{"import", "--dry-run"}, Screen: "import", Place: "Bring in from another computer, Bring in and Preview first",
		Route: "/api/run/import", Covers: []string{"import", "import --dry-run"}},
	{Group: "Bring in", Terminal: []string{"import --update", "--no-work", "--no-plugins"}, Screen: "import", Place: "Bring in, What to bring in",
		Route: "/api/run/import", Covers: []string{"import --update", "import --no-work", "import --no-plugins"}},
	{Group: "Bring in", Terminal: []string{"import --update-profile"}, Screen: "import", Place: "Bring in, The other computer's rules and skills; also Settings, Rules and skills",
		Route: "/api/run/import", Covers: []string{"import --update-profile"}},
	{Group: "Bring in", Terminal: []string{"import --profile-only"}, Screen: "settings", Place: "Settings, Rules and skills, Take the newer copies here",
		Route: "/api/run/import", Covers: []string{"import --profile-only"}},
	{Group: "Bring in", Terminal: []string{"import --repoint"}, Screen: "import", Place: "Bring in, Fix file paths in conversations brought in earlier",
		Route: "/api/run/import", Covers: []string{"import --repoint"}},
	{Group: "Bring in", Terminal: []string{"import --search-root", "--depth", "--include-bare-name"}, Screen: "import", Place: "Bring in, Advanced",
		Route: "/api/run/import", Covers: []string{"import --search-root", "import --depth", "import --include-bare-name"}},
	{Group: "Bring in", Terminal: []string{"pull"}, Screen: "import", Place: "Bring in, Advanced, Keep the other computer's folder names",
		Route: "/api/run/pull", Covers: []string{"pull"}},
	{Group: "Bring in", Terminal: []string{"pull --dry-run", "--update", "--update-profile", "--no-plugins", "--no-work"}, Screen: "import",
		Place: "Bring in, Advanced, Keep the other computer's folder names, with Preview first and What to bring in",
		Route: "/api/run/pull", Covers: []string{"pull --dry-run", "pull --update", "pull --update-profile", "pull --no-plugins", "pull --no-work"}},
	{Group: "Bring in", Terminal: []string{"restore", "--source", "--project-path", "--rewrite-from"}, Screen: "import", Place: "Bring in, From a folder instead",
		Route: "/api/run/restore", Covers: []string{"restore", "restore --source", "restore --project-path", "restore --rewrite-from"}},
	{Group: "Bring in", Terminal: []string{"restore --dry-run", "--update", "--include-bare-name"}, Screen: "import",
		Place: "Bring in, From a folder instead, Preview first, Update conversations that are already here, and Also update the project's old folder name inside conversations",
		Route: "/api/run/restore", Covers: []string{"restore --dry-run", "restore --update", "restore --include-bare-name"}},

	{Group: "Find conversations", Terminal: []string{"ls", "--archived", "--project", "--since", "--until", "--limit"}, Screen: "conversations",
		Place: "Conversations, filters and pages", Route: "/api/conversations",
		Covers: []string{"ls", "ls --archived", "ls --project", "ls --since", "ls --until", "ls --limit"}},
	{Group: "Find conversations", Terminal: []string{"search", "--regexp", "--case-sensitive", "--role", "--all"}, Screen: "conversations",
		Place: "Conversations, search box and Search options", Route: "/api/conversations",
		Covers: []string{"search", "search --regexp", "search --case-sensitive", "search --role", "search --all"}},
	{Group: "Find conversations", Terminal: []string{"search --project", "--archived", "--limit"}, Screen: "conversations",
		Place: "Conversations, the Project filter, and Look in and Stop after in Search options", Route: "/api/conversations",
		Covers: []string{"search --project", "search --archived", "search --limit"}},
	{Group: "Find conversations", Terminal: []string{"stats"}, Screen: "storage", Place: "Storage, Biggest projects",
		Route: "/api/storage", Covers: []string{"stats"}},
	{Group: "Find conversations", Terminal: []string{"stats --archived"}, Screen: "storage", Place: "Storage, the archive's size and Biggest projects",
		Route: "/api/storage", Covers: []string{"stats --archived"}},
	{Group: "Find conversations", Terminal: []string{"render", "--force", "--exclude"}, Screen: "settings", Place: "Settings, Readable pages",
		Route: "/api/run/render", Covers: []string{"render", "render --force", "render --exclude"}},
	{Group: "Find conversations", Terminal: []string{"render --include"}, Screen: "settings", Place: "Settings, Readable pages, Change, Publish again",
		Route: "/api/run/render", Covers: []string{"render --include"}},

	{Group: "Storage", Terminal: []string{"config zip-sessions on|off"}, Screen: "storage", Place: "Storage, Compress old conversations",
		Route: "/api/run/config", Covers: []string{"config zip-sessions"}},
	{Group: "Storage", Terminal: []string{"clean", "--yes"}, Screen: "storage", Place: "Storage, Remove duplicate copies",
		Route: "/api/run/clean", Covers: []string{"clean", "clean --yes"}},
	{Group: "Storage", Terminal: []string{"prune", "--older-than", "--project", "--yes"}, Screen: "delete",
		Place: "Delete old conversations, Choose now; or tick them on Conversations", Route: "/api/run/prune",
		Covers: []string{"prune", "prune --older-than", "prune --project", "prune --yes"}},
	{Group: "Storage", Terminal: []string{"prune --undo"}, Screen: "delete", Place: "Delete old conversations, Deleted recently, Restore",
		Route: "/api/run/prune-undo", Covers: []string{"prune --undo"}},
	{Group: "Storage", Terminal: []string{"config keep-days"}, Screen: "delete", Place: "Delete old conversations, Delete automatically",
		Route: "/api/run/config", Covers: []string{"config keep-days"}},
	// "Never delete", not the mockup's "keep list" (Ruling P7).
	{Group: "Storage", Terminal: []string{"config keep", "config unkeep"}, Screen: "delete",
		Place: "Delete old conversations, Never delete; or Add to Never delete on Conversations", Route: "/api/run/config",
		Covers: []string{"config keep", "config unkeep"}},

	{Group: "Settings and health", Terminal: []string{"doctor"}, Screen: "overview", Place: "Overview, Health",
		Route: "/api/health", Covers: []string{"doctor"}},
	{Group: "Settings and health", Terminal: []string{"doctor"}, Screen: "settings", Place: "Settings, Run full check",
		Route: "/api/run/doctor", Covers: []string{"doctor"}},
	{Group: "Settings and health", Terminal: []string{"config show"}, Screen: "settings", Place: "Settings, the whole page",
		Route: "/api/settings", Covers: []string{"config", "config show"}},
	{Group: "Settings and health", Terminal: []string{"config set-destination"}, Screen: "settings", Place: "Settings, Archive folder, Change folder",
		Route: "/api/run/config", Covers: []string{"config set-destination"}},
	{Group: "Settings and health", Terminal: []string{"config set-machine"}, Screen: "settings", Place: "Settings, This computer's name",
		Route: "/api/run/config", Covers: []string{"config set-machine"}},
	{Group: "Settings and health", Terminal: []string{"machines"}, Screen: "settings", Place: "Settings, Computers",
		Route: "/api/settings", Covers: []string{"machines"}},
	{Group: "Settings and health", Terminal: []string{"machines", "--forget"}, Screen: "settings", Place: "Settings, Computers, Forget",
		Route: "/api/run/machines-forget", Covers: []string{"machines --forget"}},
	{Group: "Settings and health", Terminal: []string{"config work on|off", "config work-placement"}, Screen: "settings", Place: "Settings, Work folders",
		Route: "/api/run/config", Covers: []string{"config work", "config work-placement"}},
	{Group: "Settings and health", Terminal: []string{"config exclude-folder", "unexclude-folder"}, Screen: "settings",
		Place: "Settings, Work folders, Exclude and Include again", Route: "/api/run/config",
		Covers: []string{"config exclude-folder", "config unexclude-folder"}},
	{Group: "Settings and health", Terminal: []string{"config map", "unmap", "map-folder", "unmap-folder"}, Screen: "settings",
		Place: "Settings, Where the other computer's folders go; also Change on Bring in", Route: "/api/run/config",
		Covers: []string{"config map", "config unmap", "config map-folder", "config unmap-folder"}},
	{Group: "Settings and health", Terminal: []string{"version"}, Screen: "settings", Place: "Settings, About",
		Route: "/api/settings", Covers: []string{"version"}},
	{Group: "Settings and health", Terminal: []string{"ui"}, Screen: "settings", Place: "Settings, About, Stop the panel",
		Route: "/api/stop", Covers: []string{"ui"}},
	{Group: "Settings and health", Terminal: []string{"completion"}, Place: "Terminal only: it sets up tab-completion in your shell",
		Covers: []string{}},
}
