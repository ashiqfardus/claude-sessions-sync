package main

import (
	"archive/zip"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
)

// backupDirs and backupFiles are what a whole-profile snapshot carries: every session
// with its subagents and memory, plus the configuration a person wrote by hand.
//
// Never included, whatever the pattern: .credentials.json (auth tokens, useless on
// another machine) and .claude.json (rewritten by any live session on exit; restoring
// it over a working profile does more harm than good). Caches, downloads and plugin
// installs are left out because Claude Code rebuilds them.
var (
	backupDirs  = append([]string{"projects"}, profileDirs...)
	backupFiles = []string{"settings.json", "settings.local.json", "history.jsonl", "session-sync.json", "keybindings.json"}
)

// backupMarker is the first line of every backup's MANIFEST.txt; import reads it to
// know an unzipped backup, whose rules and skills sit at its top.
const backupMarker = "Claude Code backup"

func cmdBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	claudeDir := fs.String("claude-dir", "", "override $CLAUDE_CONFIG_DIR / ~/.claude")
	out := fs.String("out", "", "folder to write the zip into (default ~/claude-backups)")
	dryRun := fs.Bool("dry-run", false, "report what would be backed up without writing")
	if err := parseFlags(fs, args, false); err != nil {
		return err
	}

	root, _, err := resolveRoot(*claudeDir)
	if err != nil {
		return err
	}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return fmt.Errorf("no Claude profile at %s", root)
	}

	dir := *out
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		dir = filepath.Join(home, "claude-backups")
	}
	if abs, err := filepath.Abs(dir); err == nil && archive.ValidateDestination(root, abs) != nil {
		return fmt.Errorf("%s is inside the Claude profile: the backup would contain itself", abs)
	}

	files := backupSet(root)
	buckets, _ := claude.ListBuckets(claude.ProjectsDir(root))
	transcripts := 0
	for _, b := range buckets {
		transcripts += len(b.Transcripts)
	}

	name := fmt.Sprintf("claude-backup-%s-%s.zip", machineName(), time.Now().Format("20060102-150405"))
	target := filepath.Join(dir, name)
	fmt.Printf("source  %s\ntarget  %s\n\n%d file(s), %d transcript(s) in %d project bucket(s)\n",
		root, target, len(files), transcripts, len(buckets))

	// The failure this command exists to prevent: a "backup" that captured memory and
	// settings but not one conversation, discovered only after the machine is gone.
	if transcripts == 0 {
		fmt.Fprintf(os.Stderr, "\nWARNING: no session transcripts found under %s.\n"+
			"This backup holds NO conversation history. Check --claude-dir and CLAUDE_CONFIG_DIR.\n",
			claude.ProjectsDir(root))
	}
	if *dryRun {
		return nil
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeBackup(root, target, files, manifestText(root, buckets, transcripts)); err != nil {
		return err
	}
	if info, err := os.Stat(target); err == nil {
		fmt.Printf("\nWrote %s (%s)\n", target, humanSize(info.Size()))
	}
	// The zip has the archive's layout (projects/<bucket>/...), and import reads a
	// project's path from its transcripts when there is no manifest - so it takes the
	// whole backup at once, on any machine, rather than one project by hand.
	fmt.Println("To bring it back, unzip it anywhere and run:")
	fmt.Println("  claude-sessions import --archive <unzipped folder>")
	return nil
}

// backupSet lists every file to include, relative to root, with forward slashes.
func backupSet(root string) []string {
	var files []string
	add := func(path string) {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return
		}
		switch strings.ToLower(filepath.Base(rel)) {
		case ".credentials.json", ".claude.json":
			return
		}
		files = append(files, filepath.ToSlash(rel))
	}

	for _, d := range backupDirs {
		_ = filepath.WalkDir(filepath.Join(root, d), func(path string, e fs.DirEntry, err error) error {
			if err == nil && e.Type().IsRegular() {
				add(path)
			}
			return nil
		})
	}
	for _, f := range backupFiles {
		if st, err := os.Stat(filepath.Join(root, f)); err == nil && st.Mode().IsRegular() {
			add(filepath.Join(root, f))
		}
	}
	// CLAUDE.md and anything it @-includes beside it.
	if mds, err := filepath.Glob(filepath.Join(root, "*.md")); err == nil {
		for _, m := range mds {
			add(m)
		}
	}
	sort.Strings(files)
	return files
}

// writeBackup builds the zip beside the target and renames it into place, so an
// interrupted run never leaves a truncated file that looks like a backup.
func writeBackup(root, target string, files []string, manifest string) error {
	tmp, err := os.CreateTemp(filepath.Dir(target), ".tmp-"+filepath.Base(target)+"-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	fail := func(e error) error {
		tmp.Close()
		os.Remove(tmpName)
		return e
	}

	zw := zip.NewWriter(tmp)
	w, err := zw.Create("MANIFEST.txt")
	if err != nil {
		return fail(err)
	}
	if _, err := io.WriteString(w, manifest); err != nil {
		return fail(err)
	}

	for _, rel := range files {
		if err := addToZip(zw, root, rel); err != nil {
			return fail(fmt.Errorf("%s: %w", rel, err))
		}
	}
	if err := zw.Close(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

func addToZip(zw *zip.Writer, root, rel string) error {
	f, err := os.Open(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	hdr, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	hdr.Name = rel
	hdr.Method = zip.Deflate
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, f)
	return err
}

func manifestText(root string, buckets []claude.Bucket, transcripts int) string {
	var b strings.Builder
	fmt.Fprintf(&b, backupMarker+"\ncreated   %s\nmachine   %s\nsource    %s\n",
		time.Now().Format("2006-01-02 15:04:05"), machineName(), root)
	b.WriteString("excluded  .credentials.json, .claude.json (by design)\n\ntranscripts per project bucket:\n")
	for _, bk := range buckets {
		fmt.Fprintf(&b, "  %-50s %d\n", bk.Name, len(bk.Transcripts))
	}
	fmt.Fprintf(&b, "\ntotal transcripts: %d\n", transcripts)
	if transcripts == 0 {
		b.WriteString("\nWARNING: this backup contains NO conversation history.\n")
	}
	return b.String()
}
