package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/panel"
	"github.com/ashiqfardus/claude-sessions-sync/internal/render"
	"github.com/ashiqfardus/claude-sessions-sync/internal/rewrite"
)

// uiAction is one command the panel runs as a job. Run calls the command's runX, which
// makes every check the CLI makes; decode refuses a body before any job starts.
type uiAction struct {
	Label   string // the job as the page names it: "Another action is running: <Label>."
	Changes bool   // a run that changed something writes a UI: line, which Activity shows
	Run     func(u *uiServer, body json.RawMessage, s streams) (any, error)
	decode  func(body json.RawMessage) error
}

// action makes a uiAction whose body decodes into B, strictly: a field the command's
// body does not have, such as one for --quiet, is refused.
func action[B any](label string, changes bool, run func(u *uiServer, b B, s streams) (any, error)) uiAction {
	return uiAction{Label: label, Changes: changes,
		Run: func(u *uiServer, body json.RawMessage, s streams) (any, error) {
			var b B
			if err := decodeBody(body, &b); err != nil {
				return nil, err
			}
			return run(u, b, s)
		},
		decode: func(body json.RawMessage) error {
			var b B
			return decodeBody(body, &b)
		},
	}
}

// decodeBody reads one JSON object into v, refusing unknown fields and anything after
// it; an empty body is {}. A body type with a check method is checked too.
func decodeBody(raw json.RawMessage, v any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage("{}")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("more than one JSON value")
	}
	if c, ok := v.(interface{ check() error }); ok {
		return c.check()
	}
	return nil
}

// The bodies, one per action. Their fields are the command's flags in lowerCamelCase;
// no flag the command map exempts has one.
type (
	pushBody struct {
		DryRun      bool `json:"dryRun"`
		ScanHistory bool `json:"scanHistory"`
	}
	importBody struct {
		DryRun          bool   `json:"dryRun"`
		Update          bool   `json:"update"`
		NoWork          bool   `json:"noWork"`
		NoPlugins       bool   `json:"noPlugins"`
		UpdateProfile   bool   `json:"updateProfile"`
		Repoint         bool   `json:"repoint"`
		ProfileOnly     bool   `json:"profileOnly"`
		IncludeBareName bool   `json:"includeBareName"`
		SearchRoot      string `json:"searchRoot"`
		Depth           int    `json:"depth"` // 0 is the CLI's default, 3 (Ruling P39)
	}
	pullBody struct {
		DryRun        bool `json:"dryRun"`
		Update        bool `json:"update"`
		UpdateProfile bool `json:"updateProfile"`
		NoPlugins     bool `json:"noPlugins"`
		NoWork        bool `json:"noWork"`
	}
	restoreBody struct {
		Source          string `json:"source"`
		ProjectPath     string `json:"projectPath"`
		RewriteFrom     string `json:"rewriteFrom"`
		IncludeBareName bool   `json:"includeBareName"`
		DryRun          bool   `json:"dryRun"`
		Update          bool   `json:"update"`
	}
	renderBody struct {
		Force   bool     `json:"force"`
		Exclude []string `json:"exclude"`
		Include []string `json:"include"`
	}
	cleanBody struct {
		Yes bool `json:"yes"`
	}
	pruneBody struct {
		OlderThanDays int      `json:"olderThanDays"` // 0 for none
		Projects      []string `json:"projects"`
		IDs           []string `json:"ids"`
		Yes           bool     `json:"yes"`
	}
	undoBody struct {
		IDs []string `json:"ids"`
	}
	backupBody struct {
		Out    string `json:"out"`
		DryRun bool   `json:"dryRun"`
	}
	installBody struct {
		Every   int  `json:"every"` // minutes; 0 is the CLI's default, 30
		NoSweep bool `json:"noSweep"`
	}
	forgetBody struct {
		Name string `json:"name"`
	}
	configBody struct {
		Action string   `json:"action"`
		Args   []string `json:"args"`
	}
	noBody struct{}
)

// panelConfigActions are the config actions config -h lists, which the panel runs.
var panelConfigActions = []string{"show", "set-destination", "map", "unmap", "set-machine", "work", "work-placement",
	"map-folder", "unmap-folder", "exclude-folder", "unexclude-folder", "keep", "unkeep", "keep-days", "zip-sessions"}

// refusal is a body refused with a sentence the page shows as it is.
type refusal string

func (r refusal) Error() string { return string(r) }

// notFull refuses a relative path: the panel's working folder is no one's choice, so
// a path resolved against it lands where nobody picked.
const notFull refusal = "Give the full path, starting from the top of the drive or from ~."

// fullPath refuses p, when given, unless it is absolute here once ~ is expanded.
func fullPath(p string) error {
	if p != "" && !filepath.IsAbs(expandHome(p)) {
		return notFull
	}
	return nil
}

// projectRef refuses what prune --project and config keep resolve against the working
// folder: ".", ".." or a relative path. A project's name, or a path from any computer,
// stays.
func projectRef(p string) error {
	if p = expandHome(p); p == "." || p == ".." || !rewrite.AbsoluteAnywhere(p) && strings.ContainsAny(p, `/\`) {
		return notFull
	}
	return nil
}

func (b *restoreBody) check() error { return errors.Join(fullPath(b.Source), fullPath(b.ProjectPath)) }

func (b *backupBody) check() error { return fullPath(b.Out) }

func (b *importBody) check() error {
	for _, r := range strings.Split(b.SearchRoot, ",") { // as import splits it
		if err := fullPath(strings.TrimSpace(r)); err != nil {
			return err
		}
	}
	return nil
}

func (b *pruneBody) check() error {
	for _, p := range b.Projects {
		if err := projectRef(p); err != nil {
			return err
		}
	}
	return nil
}

func (b *configBody) check() error {
	if !slices.Contains(panelConfigActions, b.Action) {
		return fmt.Errorf("%q is not a config action", b.Action)
	}
	arg := func(i int) string {
		if i < len(b.Args) {
			return b.Args[i]
		}
		return ""
	}
	switch b.Action {
	case "set-destination":
		return fullPath(arg(0))
	case "map", "map-folder": // the folder here; map-folder's first is another computer's path
		return fullPath(arg(1))
	case "unexclude-folder": // as exclude-folder stored it, which may be another system's form
		if p := expandHome(arg(0)); p != "" && !rewrite.AbsoluteAnywhere(p) {
			return notFull
		}
	case "keep", "unkeep":
		return projectRef(arg(0))
	}
	return nil
}

// uiActions are the panel's actions by the name /api/run/<name> takes. Each passes its
// body to the command's runX as the CLI passes its flags; the string flags get the same
// leading ~ the CLI expands.
var uiActions = map[string]uiAction{
	"push": action("Back up now", true, func(u *uiServer, b pushBody, s streams) (any, error) {
		// Never Quiet (Ruling P35): a quiet push's watchdog exits the process.
		return runPush(s, u.root, "", pushOpts{DryRun: b.DryRun, ScanHistory: b.ScanHistory})
	}),
	"import": action("Bring in work from another computer", true, func(u *uiServer, b importBody, s streams) (any, error) {
		res, err := runImport(s, importOpts{ClaudeDir: u.root, SearchRoot: expandHome(b.SearchRoot), Depth: b.Depth,
			DryRun: b.DryRun, BareName: b.IncludeBareName, Update: b.Update, UpdateProfile: b.UpdateProfile,
			NoPlugins: b.NoPlugins, NoWork: b.NoWork, Repoint: b.Repoint, ProfileOnly: b.ProfileOnly})
		if b.ProfileOnly { // what import --json prints
			return res.Profile, err
		}
		return res.Rows, err
	}),
	"pull": action("Bring in work from another computer", true, func(u *uiServer, b pullBody, s streams) (any, error) {
		return runPull(s, pullOpts{ClaudeDir: u.root, DryRun: b.DryRun, Update: b.Update,
			UpdateProfile: b.UpdateProfile, NoPlugins: b.NoPlugins, NoWork: b.NoWork})
	}),
	"restore": action("Bring in from a folder", true, func(u *uiServer, b restoreBody, s streams) (any, error) {
		return runRestore(s, restoreCmdOpts{ClaudeDir: u.root, Source: expandHome(b.Source), ProjectPath: expandHome(b.ProjectPath),
			RewriteFrom: expandHome(b.RewriteFrom), BareName: b.IncludeBareName, DryRun: b.DryRun, Update: b.Update})
	}),
	"render": action("Readable pages", true, func(u *uiServer, b renderBody, s streams) (any, error) {
		// Split as the CLI splits its comma-separated flags.
		return runRender(s, renderOpts{ClaudeDir: u.root, Force: b.Force,
			Exclude: splitList(strings.Join(b.Exclude, ",")), Include: splitList(strings.Join(b.Include, ","))})
	}),
	"clean": action("Remove duplicate copies", true, func(u *uiServer, b cleanBody, s streams) (any, error) {
		res, err := runClean(s, cleanOpts{ClaudeDir: u.root, Yes: b.Yes})
		if res.Plan.Remove != nil { // the copies were compared, even when removing some failed
			var found int64
			for _, c := range res.Plan.Remove {
				found += c.Size
			}
			u.mu.Lock()
			u.lastClean = &cleanSummary{At: time.Now(), Copies: len(res.Plan.Remove) - res.Deleted, Bytes: found - res.Freed}
			u.mu.Unlock()
		}
		return res, err
	}),
	"prune": action("Delete old conversations", true, func(u *uiServer, b pruneBody, s streams) (any, error) {
		o := pruneOpts{ClaudeDir: u.root, Projects: b.Projects, IDs: b.IDs, Yes: b.Yes}
		if b.OlderThanDays != 0 {
			o.OlderThan = fmt.Sprintf("%dd", b.OlderThanDays) // runPrune refuses one below 1
		}
		// A run that fails partway returns its counts with the error (Ruling P44).
		return runPrune(s, o)
	}),
	"prune-undo": action("Restore deleted conversations", true, func(u *uiServer, b undoBody, s streams) (any, error) {
		// Never nil, even with no ids: a nil Undo is a prune, not an undo (Ruling P44).
		return runPrune(s, pruneOpts{ClaudeDir: u.root, Undo: nonNil(b.IDs)})
	}),
	"backup": action("Full backup file", true, func(u *uiServer, b backupBody, s streams) (any, error) {
		return runBackup(s, backupOpts{ClaudeDir: u.root, Out: expandHome(b.Out), DryRun: b.DryRun})
	}),
	"install": action("Set up automatic backup", true, func(u *uiServer, b installBody, s streams) (any, error) {
		every := b.Every
		if every == 0 {
			every = 30 // install's --every default
		}
		return runInstall(s, installOpts{ClaudeDir: u.root, Every: every, NoSweep: b.NoSweep})
	}),
	"uninstall": action("Turn off automatic backup", true, func(u *uiServer, _ noBody, s streams) (any, error) {
		// The CLI's default, which the zero value is not: a PowerShell hook stays (Ruling P41).
		return runUninstall(s, uninstallOpts{ClaudeDir: u.root, KeepLegacy: true})
	}),
	"machines-forget": action("Forget a computer", true, func(u *uiServer, b forgetBody, s streams) (any, error) {
		return runForget(s, machinesOpts{ClaudeDir: u.root, Forget: b.Name})
	}),
	"config": action("Change a setting", true, func(u *uiServer, b configBody, s streams) (any, error) {
		res, err := runConfig(s, configOpts{ClaudeDir: u.root, Action: b.Action, Args: b.Args})
		if err == nil && b.Action == "set-machine" {
			resolveRoot(u.root) // reads the new name in, for /api/state and the jobs after this one
		}
		return res, err
	}),
	"doctor": action("Run full check", false, func(u *uiServer, _ noBody, s streams) (any, error) {
		// Every check, the write test too: Overview's health is the light one (Ruling P54).
		rep, err := runDoctor(s, doctorOpts{ClaudeDir: u.root})
		if errors.Is(err, errChecksFailed) {
			err = nil // failed checks are the report, not a failed run
		}
		return rep, err
	}),
	"open-log": action("Open the log file", false, func(u *uiServer, _ noBody, s streams) (any, error) {
		p := filepath.Join(u.root, "session-sync.log")
		f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // a new profile has none yet
		if err != nil {
			return nil, err
		}
		f.Close()
		return nil, openBrowser(p)
	}),
}

const (
	maxRunBody = 64 << 10
	badRun     = "The panel asked for something this version can't do. Reload the page and try again."
)

// apiRun starts the action /api/run/<name> names, with the request's body, and answers
// {"id"}, or with ?wait=1 the job's final State, its lines and result included: that
// is for tests and scripts, the page reads lines from /api/jobs/events.
func (u *uiServer) apiRun(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/run/")
	a, ok := uiActions[name]
	if !ok {
		sendStatus(w, http.StatusNotFound, map[string]string{"error": badRun})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRunBody))
	if err == nil {
		err = a.decode(body)
	}
	if err != nil {
		u.logf("PANEL: %s: refused the request: %v", name, err)
		msg, r := badRun, refusal("")
		if errors.As(err, &r) {
			msg = string(r)
		}
		sendStatus(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	j, err := u.jobs.Start(name, a.Label, func(log io.Writer) (any, error) {
		defer u.dropCaches() // whatever it changed, even failing partway, shows on the next read
		res, err := a.Run(u, body, streams{log, log})
		if err == nil && a.Changes && !onlyLooks(name, body) {
			if text := activityText(name, res); text != "" {
				u.logf("UI: %s", text)
			}
		}
		return res, err
	})
	switch {
	case errors.Is(err, panel.ErrBusy):
		sendStatus(w, http.StatusConflict, map[string]string{"error": "Another action is running: " + j.Label() + ". It will finish first."})
		return
	case err != nil: // panel.ErrClosed: Stop was asked for (Ruling P22)
		sendStatus(w, http.StatusConflict, map[string]string{"error": "The panel is stopping, so it starts nothing new."})
		return
	}
	if r.URL.Query().Get("wait") == "1" { // for tests: the page follows the event stream
		sendJSON(w, j.Wait()) // the full State, every line included
		return
	}
	sendJSON(w, map[string]string{"id": j.Summary().ID})
}

// onlyLooks reports whether a body asks only to look: a dry run, or a config action
// without arguments (show, or a list). A preview of clean or prune deletes nothing,
// which activityText sees in its result.
func onlyLooks(name string, body json.RawMessage) bool {
	var b struct {
		DryRun bool     `json:"dryRun"`
		Action string   `json:"action"`
		Args   []string `json:"args"`
	}
	_ = json.Unmarshal(body, &b) // decoded strictly before the job started
	return b.DryRun || name == "config" && (b.Action == "show" || len(b.Args) == 0)
}

// configActivity is what a config action changed, as Activity says it.
var configActivity = map[string]string{
	"set-destination":  "Changed the archive folder",
	"set-machine":      "Renamed this computer",
	"map":              "Changed where the other computers' folders go",
	"unmap":            "Changed where the other computers' folders go",
	"map-folder":       "Changed where the other computers' folders go",
	"unmap-folder":     "Changed where the other computers' folders go",
	"work":             "Changed work folder backup",
	"work-placement":   "Changed where work folders are filed",
	"exclude-folder":   "Stopped backing up a folder",
	"unexclude-folder": "Backing up a folder again",
	"keep":             "Added to Never delete",
	"unkeep":           "Removed from Never delete",
	"keep-days":        "Changed Delete automatically",
	"zip-sessions":     "Changed Compress old conversations",
}

// activityText is a successful changing run's line for Activity, or "" for none: push
// logs its own PUSH: line, and a run that changed nothing says nothing.
func activityText(name string, result any) string {
	switch r := result.(type) {
	case []importRow: // import and pull
		convs, work := 0, 0
		for _, row := range r {
			switch n := row.Filed + row.Updated; {
			case row.Kind != "work":
				convs += n
			case n > 0: // a folder placed again, unchanged, is no news
				work++
			}
		}
		switch {
		case work > 0:
			return "Brought in " + plural(convs, "conversation") + " and " + plural(work, "work folder")
		case convs > 0:
			return "Brought in " + plural(convs, "conversation")
		}
	case profileResult:
		if len(r.Files) > 0 {
			return "Brought in the newer rules and skills: " + plural(len(r.Files), "file")
		}
	case restoreResult:
		if n := r.Copied + r.Updated; n > 0 {
			return "Brought in " + plural(n, "conversation") + " from a folder"
		}
	case render.Result:
		switch {
		case r.Rendered > 0:
			return "Rebuilt " + plural(r.Rendered, "readable page")
		case r.Unpublished > 0:
			return "Removed " + plural(r.Unpublished, "readable page")
		}
	case cleanResult:
		if r.Deleted == 1 {
			return "Removed 1 duplicate copy and freed " + mb(r.Freed)
		} else if r.Deleted > 1 {
			return fmt.Sprintf("Removed %d duplicate copies and freed %s", r.Deleted, mb(r.Freed))
		}
	case pruneResult:
		switch {
		case r.Deleted > 0:
			return "Deleted " + plural(r.Deleted, "conversation") + " from the archive"
		case len(r.Undone) > 0:
			return "Restored " + plural(len(r.Undone), "deleted conversation")
		}
	case backupResult:
		return "Made a full backup file of " + plural(r.Transcripts, "conversation")
	case installResult:
		if r.SweepFailed != "" {
			return "Set up automatic backup after each reply; the scheduled backup could not be changed"
		}
		return "Set up automatic backup"
	case uninstallResult:
		if strings.HasPrefix(r.Sweep, "FAILED") {
			return "Turned off automatic backup after each reply; the scheduled backup could not be removed"
		}
		return "Turned off automatic backup"
	case forgetResult:
		return "Forgot the computer " + r.Forgot
	case configResult:
		return configActivity[r.Action]
	}
	return ""
}
