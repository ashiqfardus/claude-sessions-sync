// Package render turns archived transcripts into pages you can read on a phone.
//
// The .jsonl files are never modified or removed - HTML is a convenience layer on
// top. If a transcript fails to render, the transcript is still there and still
// resumable, which is why every failure here is counted rather than fatal.
package render

import (
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/convzip"
)

// maxBlockChars caps one block of content.
//
// A single tool result can carry an entire file. Rendering all of it produces a page
// a phone cannot open, and the full text is one click away in the .jsonl.
const maxBlockChars = 4000

// maxPageBytes caps a whole page.
//
// Per-block truncation is not enough on its own: a long session is thousands of small
// blocks, and a 40MB transcript would still produce something no phone will open.
// Turns are rendered oldest-first, so the cap drops the tail and says so.
const maxPageBytes = 2 << 20 // 2 MiB

// maxRowsPerProject caps each project's rows on the main index; the project's own
// page lists all of them. Listing every session on one page is a megabyte at a
// thousand sessions, opened on a phone from a cloud folder.
const maxRowsPerProject = 20

// Options controls a render pass.
type Options struct {
	// Force re-renders pages that are already current, for after a template change.
	Force bool

	// Exclude skips projects whose path or bucket contains any of these substrings,
	// compared case-insensitively. Rendering makes a conversation readable to anyone
	// who can open the folder, so there has to be a way to archive a project without
	// publishing it.
	Exclude []string

	// LocalProjects, when set, is this machine's ~/.claude/projects. A transcript that
	// is byte-identical there is read from local disk instead of back across the cloud
	// filesystem the push just wrote it to.
	LocalProjects string

	// Budget, when set, stops rendering new pages once it has elapsed; the rest are
	// left for the next pass. Pages already current are still listed.
	Budget time.Duration

	// Progress, when set, is called with (0, of) before the first conversation, of being
	// how many the pass takes up, then with (n, of) as it takes up the nth.
	Progress func(n, of int)
}

// Result reports what a render pass did.
type Result struct {
	Rendered    int
	UpToDate    int
	Deferred    int // needed rendering, left for the next pass by Budget
	Failed      int
	Excluded    int
	Unpublished int // pages removed because a project is now excluded
	Skipped     int // transcript lines that did not parse
	OutputDir   string
}

// stateSaveEvery is how often progress is saved during a long render, so a run
// that is killed - the SessionEnd hook's timeout - keeps what it finished.
const stateSaveEvery = 10 * time.Second

// now is the clock the render budget is measured with; a test replaces it.
var now = time.Now

// materialize unpacks a zipped conversation for its page, and zipEntries reads a zip's
// index; a test counts the calls.
var (
	materialize = convzip.Materialize
	zipEntries  = convzip.Entries
)

// Archive renders every transcript in the archive.
func Archive(dest string, opts Options) (Result, error) {
	res := Result{OutputDir: filepath.Join(dest, "html")}
	start := now()

	// Pages are written into, and withdrawn from, html/<bucket>. If html - or a bucket
	// folder in it - is a symlink planted in the shared archive, those writes and
	// deletions land wherever it points.
	if _, err := os.Lstat(res.OutputDir); err == nil && !archive.IsRealDir(res.OutputDir) {
		return res, fmt.Errorf("%s is not a plain folder (a link?); not rendering into it", res.OutputDir)
	}

	buckets, err := archive.BucketNames(dest)
	if err != nil {
		return res, err
	}

	// The manifest gives each bucket its real project path, so pages are titled with
	// something a human recognises rather than a flattened slug.
	manifest, _, err := archive.Merged(dest)
	if err != nil {
		return res, err
	}

	type indexEntry struct {
		Project string
		Bucket  string
		ID      string
		Href    string
		Updated time.Time
		Size    int64
		Prompt  string
	}
	var entries []indexEntry

	// A page is current only if it was rendered by this template, from a transcript
	// of this size. Comparing timestamps alone missed an append inside the tolerance
	// window, and a renderer fix - such as redaction - never reached pages that were
	// already published.
	prev := loadState(res.OutputDir)
	next := renderState{Version: templateVersion, Pages: map[string]pageState{}}
	lastSave := time.Now()

	// Each bucket to render is listed first, for the count Progress is given.
	type listed struct {
		bucket, dir, project string
		files                []os.DirEntry
		zips                 map[string]string // by lower-cased id
	}
	var todo []listed
	total := 0
	for _, bucket := range buckets {
		bucketDir := filepath.Join(archive.ProjectsDir(dest), bucket)
		files, err := os.ReadDir(bucketDir)
		if err != nil {
			continue
		}
		if pages := filepath.Join(res.OutputDir, bucket); !archive.IsRealDir(pages) {
			if _, err := os.Lstat(pages); err == nil {
				res.Failed++ // see the check on html itself above
				continue
			}
		}

		project := bucket
		if p, ok := manifest[bucket]; ok && p.Path != "" {
			project = p.Path
		}

		if IsExcluded(project, bucket, opts.Exclude) {
			res.Excluded++
			// Withdraw anything already published for this project.
			//
			// Excluding without removing is worse than having no exclude at all: the
			// user asks for a project to stop being readable, is told it worked, and
			// the pages stay exactly where they were. Only generated HTML is deleted -
			// the transcripts it was rendered from are untouched.
			n, err := removePages(res.OutputDir, bucket)
			if err != nil {
				res.Failed++
			}
			res.Unpublished += n
			continue
		}

		zips := convzip.Find(bucketDir)
		loose := map[string]bool{}
		for _, f := range files { // as the loop below takes them up
			if id, ok := strings.CutSuffix(strings.ToLower(f.Name()), ".jsonl"); ok && f.Type().IsRegular() {
				loose[id] = true
				total++
			}
		}
		for lid := range zips {
			if !loose[lid] {
				total++
			}
		}
		todo = append(todo, listed{bucket, bucketDir, project, files, zips})
	}
	done := 0
	step := func() {
		if opts.Progress != nil {
			opts.Progress(done, total)
			done++
		}
	}
	step()

	for _, l := range todo {
		bucket, bucketDir, project, files, zips := l.bucket, l.dir, l.project, l.files, l.zips

		// one renders or lists the transcript name, of size and mtime; open gives its path,
		// and is called only when its page is due. z is its zip, for a zipped one.
		one := func(name string, size int64, mtime time.Time, z *zipState, open func() (string, func(), error)) {
			id := strings.TrimSuffix(name, filepath.Ext(name))
			out := filepath.Join(res.OutputDir, bucket, id+".html")

			// A loose copy with a zip beside it: its page is current only while that zip is.
			var beside *zipState
			if zp, ok := zips[strings.ToLower(id)]; ok && z == nil {
				beside = &zipState{}
				if st, err := os.Lstat(zp); err == nil {
					beside.Size, beside.MTime = st.Size(), st.ModTime()
				}
			}

			// Prefer this machine's own copy when it is byte-identical: push has just
			// written it across, and reading it straight back is a needless round trip
			// through a cloud filesystem. Not for a loose copy with a zip beside it, which
			// may hold more.
			if opts.LocalProjects != "" && beside == nil {
				local := filepath.Join(opts.LocalProjects, bucket, name)
				// Size AND timestamp: same size with different content is unlikely but
				// possible, and rendering local bytes while the page claims to be the
				// archive is not a trade worth making to save one read.
				if li, err := os.Stat(local); err == nil && li.Size() == size && archive.SameMTime(li.ModTime(), mtime) {
					open = func() (string, func(), error) { return local, func() {}, nil }
				}
			}

			key := bucket + "/" + id
			var known *pageState
			if ps, ok := prev.Pages[key]; ok && !opts.Force && prev.Version == templateVersion && ps.Size == size && sameZipFile(ps.Beside, beside) {
				known = &ps
			}
			mayRender := opts.Budget <= 0 || now().Sub(start) < opts.Budget
			page, err := renderOne(open, project, id, known, out, mtime, mayRender)
			if err == nil && !page.deferred {
				next.Pages[key] = pageState{Size: size, Prompt: page.prompt, Zip: z, Beside: beside}
			}
			switch {
			case err != nil:
				// One bad transcript must not stop the rest: the point of the archive
				// is that it survives the tooling.
				res.Failed++
				return
			case page.deferred:
				res.Deferred++
				// Still listed when an older page exists, so the index does not lose it.
				if _, err := os.Stat(out); err != nil {
					return
				}
				page.prompt = prev.Pages[key].Prompt
			case page.upToDate:
				res.UpToDate++
			default:
				res.Rendered++
				res.Skipped += page.skipped
				if time.Since(lastSave) >= stateSaveEvery {
					_ = saveState(res.OutputDir, progress(prev, next))
					lastSave = time.Now()
				}
			}

			entries = append(entries, indexEntry{
				Project: project, Bucket: bucket, ID: id,
				Href:    pageHref(bucket, id),
				Updated: mtime.Local(), Size: size, Prompt: page.prompt,
			})
		}

		loose := map[string]bool{}
		for _, f := range files {
			// Regular files only: a symlink would be read through to whatever it names.
			if !f.Type().IsRegular() || !strings.EqualFold(filepath.Ext(f.Name()), ".jsonl") {
				continue
			}
			step()
			info, err := f.Info()
			if err != nil {
				res.Failed++
				continue
			}
			src := filepath.Join(bucketDir, f.Name())
			id := strings.TrimSuffix(f.Name(), filepath.Ext(f.Name()))
			loose[strings.ToLower(id)] = true
			open := func() (string, func(), error) { return src, func() {}, nil }
			// A zip beside it - rare - may hold turns it lacks: the page shows whichever holds
			// more, as import files it. A zip that cannot be read leaves the loose copy.
			if _, ok := zips[strings.ToLower(id)]; ok {
				open = func() (string, func(), error) {
					if p, done, err := materialize(bucketDir, id, ""); err == nil {
						return p, done, nil
					}
					return src, func() {}, nil
				}
			}
			one(f.Name(), info.Size(), info.ModTime(), nil, open)
		}

		// A zipped conversation keeps its page. It is dated by its transcript's entry in
		// the zip, and unpacked only when the page is due: a template change, or a page
		// withdrawn by an exclusion since lifted. One with a loose copy beside it is
		// rendered above.
		for lid, zp := range zips {
			if loose[lid] {
				continue
			}
			step()
			id := strings.TrimSuffix(filepath.Base(zp), convzip.Suffix)
			key := bucket + "/" + id
			ps, had := prev.Pages[key]
			z, size, err := zipTranscript(zp, id, ps)
			if err == nil {
				one(id+".jsonl", size, z.Entry, z, func() (string, func(), error) { return materialize(bucketDir, id, "") })
				continue
			}
			// Unreadable: garbled, or a placeholder not downloaded yet. The page stays,
			// listed as the last pass knew it, and so does that pass's record of it.
			res.Failed++
			pst, err := os.Stat(filepath.Join(res.OutputDir, bucket, id+".html"))
			if !had || err != nil {
				continue
			}
			if prev.Version == templateVersion { // an older template's page is not current
				next.Pages[key] = ps
			}
			// Dated as Zip dates the zip, by the last activity, not when the page was written.
			updated := pst.ModTime()
			if zst, err := os.Lstat(zp); err == nil {
				updated = zst.ModTime()
			}
			entries = append(entries, indexEntry{
				Project: project, Bucket: bucket, ID: id,
				Href:    pageHref(bucket, id),
				Updated: updated.Local(), Size: ps.Size, Prompt: ps.Prompt,
			})
		}
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Updated.After(entries[j].Updated) })

	byBucket := map[string][]indexEntry{}
	for _, e := range entries {
		byBucket[e.Bucket] = append(byBucket[e.Bucket], e)
	}
	order := make([]string, 0, len(byBucket))
	for k := range byBucket {
		order = append(order, k)
	}
	sort.Slice(order, func(i, j int) bool {
		pi, pj := byBucket[order[i]][0].Project, byBucket[order[j]][0].Project
		if pi != pj {
			return pi < pj
		}
		return order[i] < order[j]
	})

	writeRows := func(b *strings.Builder, rows []indexEntry, href func(indexEntry) string) {
		b.WriteString(`<ul class="sessions">`)
		for _, e := range rows {
			prompt := e.Prompt
			if prompt == "" {
				prompt = "(no opening message)"
			}
			fmt.Fprintf(b,
				`<li><a href="%s"><span class="when">%s</span><span class="prompt">%s</span><span class="size">%s</span></a></li>`,
				html.EscapeString(href(e)),
				html.EscapeString(e.Updated.Format("2006-01-02 15:04")),
				html.EscapeString(prompt),
				html.EscapeString(humanSize(e.Size)))
		}
		b.WriteString(`</ul>`)
	}

	var b strings.Builder
	b.WriteString(pageHead("Claude sessions"))
	b.WriteString(`<h1>Claude sessions</h1>`)
	// Dated by the newest session rather than by "now", so a pass that changed nothing
	// writes nothing for the sync client to upload.
	fmt.Fprintf(&b, `<p class="meta">%d session(s)`, len(entries))
	if len(entries) > 0 {
		fmt.Fprintf(&b, ` &middot; newest %s`, html.EscapeString(entries[0].Updated.Format("2006-01-02 15:04")))
	}
	b.WriteString(`</p>`)

	for _, bucket := range order {
		group := byBucket[bucket]
		project := group[0].Project

		// Every project gets its own complete list. The main index shows only each
		// project's most recent sessions and links there: it used to stop at 300
		// sessions overall and point at JSON, leaving the rest unreachable from a phone.
		shown := group
		if len(shown) > maxRowsPerProject {
			shown = shown[:maxRowsPerProject]
		}
		fmt.Fprintf(&b, `<h2>%s</h2>`, html.EscapeString(project))
		writeRows(&b, shown, func(e indexEntry) string { return e.Href })
		if len(group) > len(shown) {
			fmt.Fprintf(&b, `<p class="meta"><a href="%s">all %d sessions &rarr;</a></p>`,
				html.EscapeString("./"+url.PathEscape(bucket)+"/index.html"), len(group))
		}

		var p strings.Builder
		p.WriteString(pageHead(project))
		p.WriteString(`<p class="back"><a href="../index.html">&larr; all projects</a></p>`)
		fmt.Fprintf(&p, `<h1>%s</h1><p class="meta">%d session(s)</p>`, html.EscapeString(project), len(group))
		writeRows(&p, group, func(e indexEntry) string { return "./" + url.PathEscape(e.ID) + ".html" })
		p.WriteString(pageFoot())
		if err := archive.WriteFileAtomic(filepath.Join(res.OutputDir, bucket, "index.html"), []byte(p.String()), 0o644); err != nil {
			res.Failed++
		}
	}
	b.WriteString(pageFoot())

	if err := archive.WriteFileAtomic(filepath.Join(res.OutputDir, "index.html"), []byte(b.String()), 0o644); err != nil {
		return res, err
	}
	return res, saveState(res.OutputDir, next)
}

// templateVersion changes whenever a rendered page would come out differently, so
// every page is rendered again once. 2: secrets are redacted, times are local.
// 3: a Content-Security-Policy forbids scripts, and more secret formats are redacted.
// 4: Claude's Markdown is formatted (bold, inline code, links, headings, tables).
// 5: titles leave out Claude Code's wrapper blocks; only the index rows change.
const templateVersion = 5

// pageHref links a session's page from the main index.
//
// A leading "./" and escaped segments keep a bucket or file named like a URL -
// "javascript:alert(1)" is a legal folder name outside Windows - a relative path.
func pageHref(bucket, id string) string {
	return "./" + url.PathEscape(bucket) + "/" + url.PathEscape(id) + ".html"
}

// progress is the state to save mid-run: what this pass has done so far, on top of
// what the last pass left when it was the same template.
func progress(prev, next renderState) renderState {
	out := renderState{Version: templateVersion, Pages: map[string]pageState{}}
	if prev.Version == templateVersion {
		for k, v := range prev.Pages {
			out.Pages[k] = v
		}
	}
	for k, v := range next.Pages {
		out.Pages[k] = v
	}
	return out
}

type pageState struct {
	Size   int64     `json:"size"`
	Prompt string    `json:"prompt"`
	Zip    *zipState `json:"zip,omitempty"`    // for a zipped transcript
	Beside *zipState `json:"beside,omitempty"` // the zip beside a loose transcript, Entry unused
}

// sameZipFile reports whether two records name the same zip file, or both none.
func sameZipFile(a, b *zipState) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Size == b.Size && archive.SameMTime(a.MTime, b.MTime)
}

// zipState is a zipped transcript's zip file as its page was made from it, and the
// transcript entry's time: a run reads no zip whose file is unchanged since.
type zipState struct {
	Size  int64     `json:"size"`
	MTime time.Time `json:"mtime"`
	Entry time.Time `json:"entry"`
}

// zipTranscript is the zip at zp as a page's state records it, and the size of its
// transcript id. It reads the zip's index only when the zip is not as last recorded:
// on a drive that streams files on demand, every read is a download.
func zipTranscript(zp, id string, last pageState) (*zipState, int64, error) {
	st, err := os.Lstat(zp)
	if err != nil {
		return nil, 0, err
	}
	z := &zipState{Size: st.Size(), MTime: st.ModTime()}
	if c := last.Zip; c != nil && c.Size == z.Size && archive.SameMTime(c.MTime, z.MTime) {
		z.Entry = c.Entry
		return z, last.Size, nil
	}
	ents, err := zipEntries(zp)
	if err != nil {
		return nil, 0, err
	}
	e, ok := ents[id+".jsonl"]
	if !ok {
		return nil, 0, os.ErrNotExist // not one Zip wrote: it always holds the transcript
	}
	z.Entry = e.ModTime
	return z, e.Size, nil
}

// renderState is html/.render-state.json: what each page was rendered from. It also
// carries each session's opening prompt, so the index needs no read of a transcript
// whose page is current - on a cloud filesystem that read is a download, every run.
type renderState struct {
	Version int                  `json:"version"`
	Pages   map[string]pageState `json:"pages"`
}

func statePath(htmlDir string) string { return filepath.Join(htmlDir, ".render-state.json") }

func loadState(htmlDir string) renderState {
	var s renderState
	if raw, err := os.ReadFile(statePath(htmlDir)); err == nil {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

func saveState(htmlDir string, s renderState) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return archive.WriteFileAtomic(statePath(htmlDir), data, 0o644)
}

type pageInfo struct {
	upToDate           bool
	deferred           bool // needed rendering, but mayRender was false
	prompt             string
	skipped            int
	unrecordedThinking int
}

// renderOne writes one page from the transcript open gives, last modified at mtime.
// known is what the page was last rendered from, when that was this template and a
// transcript of the same size; nil forces a render. When mayRender is false a page
// that is not current is left for later. open is called only to render.
func renderOne(open func() (string, func(), error), project, id string, known *pageState, out string, mtime time.Time, mayRender bool) (pageInfo, error) {
	var info pageInfo

	// Pages carry their source's timestamp, so "current" means the two match - not
	// merely that the page is newer. A transcript replaced with OLDER content, which
	// is exactly what import does when it preserves archived timestamps, would
	// otherwise leave a stale page that never re-renders. The same tolerance as
	// everywhere else: a synced filesystem rounds timestamps.
	if known != nil {
		if outInfo, err := os.Stat(out); err == nil && archive.SameMTime(outInfo.ModTime(), mtime) {
			info.prompt = known.Prompt
			info.upToDate = true
			return info, nil
		}
	}
	if !mayRender {
		info.deferred = true
		return info, nil
	}
	src, done, err := open()
	if err != nil {
		return info, err
	}
	defer done()

	var body strings.Builder
	var first time.Time
	var last time.Time
	var truncatedTurns int

	var title claude.Title // what ls and the panel call it too
	stats, err := claude.Walk(src, func(r claude.Record) bool {
		title.Add(r)
		if !r.Timestamp.IsZero() {
			// Transcripts record UTC; the index shows local file times. Pages used to
			// disagree with their own index row by the UTC offset.
			r.Timestamp = r.Timestamp.Local()
			if first.IsZero() {
				first = r.Timestamp
			}
			last = r.Timestamp
		}

		// Per-block truncation alone is not enough: a long session is thousands of
		// small blocks. Keep reading, so the summary counts stay honest, but stop
		// adding to the page.
		if body.Len() >= maxPageBytes {
			truncatedTurns++
			// Still counted, so the figures at the foot of the page describe the whole
			// session rather than only the part that fitted.
			info.unrecordedThinking += countUnrecordedThinking(r)
			return true
		}
		info.unrecordedThinking += writeTurn(&body, r)
		return true
	})
	if err != nil {
		return info, err
	}
	info.skipped = stats.Skipped
	// Cut by characters, not bytes: a byte slice split Bengali text and emoji
	// mid-character in the index.
	info.prompt = Redact(title.String())
	if runes := []rune(info.prompt); len(runes) > 120 {
		info.prompt = string(runes[:120]) + "..."
	}

	var b strings.Builder
	b.WriteString(pageHead(project + " — " + id))
	b.WriteString(`<p class="back"><a href="../index.html">&larr; all sessions</a></p>`)
	fmt.Fprintf(&b, `<h1>%s</h1>`, html.EscapeString(project))
	fmt.Fprintf(&b, `<p class="meta">%s`, html.EscapeString(id))
	if !first.IsZero() {
		end := last.Format("15:04")
		if last.Format("2006-01-02") != first.Format("2006-01-02") {
			end = last.Format("2006-01-02 15:04") // a session that ran past midnight
		}
		fmt.Fprintf(&b, ` &middot; %s → %s`,
			html.EscapeString(first.Format("2006-01-02 15:04")),
			html.EscapeString(end))
	}
	b.WriteString(`</p>`)
	b.WriteString(body.String())

	if truncatedTurns > 0 {
		fmt.Fprintf(&b, `<p class="skipped">This session is too long to show in full. %d later turn(s) are not on this page; the complete record is in the .jsonl.</p>`,
			truncatedTurns)
	}
	if info.unrecordedThinking > 0 {
		// Claude Code stores a signature but no text for these. Saying so is honest;
		// rendering nothing at all leaves the reader wondering what is missing.
		fmt.Fprintf(&b, `<p class="skipped">%d thinking block(s) in this session were not recorded by Claude Code, so there is nothing to show for them.</p>`,
			info.unrecordedThinking)
	}
	if stats.Skipped > 0 {
		// Say so rather than pretending the page is complete. The transcript format is
		// internal to Claude Code and changes between releases.
		fmt.Fprintf(&b, `<p class="skipped">%d line(s) in this transcript could not be parsed and were skipped. The original .jsonl is unchanged.</p>`,
			stats.Skipped)
	}
	b.WriteString(pageFoot())

	if err := archive.WriteFileAtomic(out, []byte(b.String()), 0o644); err != nil {
		return info, err
	}
	// Match the transcript's timestamp so the up-to-date check above is stable.
	_ = os.Chtimes(out, mtime, mtime)
	return info, nil
}

// writeTurn renders one message, returning how many thinking blocks carried no text.
func writeTurn(b *strings.Builder, r claude.Record) int {
	unrecorded := 0
	if len(r.Blocks) == 0 {
		return 0
	}

	role := r.Role
	if role == "" {
		role = r.Type
	}
	switch role {
	case "user", "assistant":
	default:
		return 0 // system plumbing: not part of the conversation
	}

	when := ""
	if !r.Timestamp.IsZero() {
		when = r.Timestamp.Local().Format("15:04")
	}

	fmt.Fprintf(b, `<div class="turn %s"><div class="who">%s<span class="time">%s</span></div>`,
		html.EscapeString(role), html.EscapeString(role), html.EscapeString(when))

	for _, blk := range r.Blocks {
		switch blk.Kind {
		case "text":
			// Only Claude writes Markdown; a prompt is shown exactly as typed.
			b.WriteString(formatBody(blk.Text, role == "assistant"))
		case "thinking":
			if strings.TrimSpace(blk.Text) == "" {
				unrecorded++
				continue
			}
			// Collapsed: interesting when you want it, noise when you do not.
			b.WriteString(`<details class="thinking"><summary>thinking</summary>`)
			b.WriteString(formatBody(blk.Text, true))
			b.WriteString(`</details>`)
		case "tool_use":
			name := blk.Name
			if name == "" {
				name = "tool"
			}
			fmt.Fprintf(b, `<details class="tool"><summary>&#9881; %s</summary>`, html.EscapeString(name))
			b.WriteString(formatBody(blk.Text, false))
			b.WriteString(`</details>`)
		case "tool_result":
			if strings.TrimSpace(blk.Text) == "" {
				continue
			}
			b.WriteString(`<details class="result"><summary>result</summary>`)
			b.WriteString(formatBody(blk.Text, false))
			b.WriteString(`</details>`)
		default:
			if strings.TrimSpace(blk.Text) != "" {
				fmt.Fprintf(b, `<details class="tool"><summary>%s</summary>`, html.EscapeString(blk.Kind))
				b.WriteString(formatBody(blk.Text, false))
				b.WriteString(`</details>`)
			}
		}
	}
	b.WriteString(`</div>`)
	return unrecorded
}

// countUnrecordedThinking reports thinking blocks Claude Code stored without text.
func countUnrecordedThinking(r claude.Record) int {
	n := 0
	for _, blk := range r.Blocks {
		if blk.Kind == "thinking" && strings.TrimSpace(blk.Text) == "" {
			n++
		}
	}
	return n
}

// removePages deletes the generated pages for one bucket, and the directory if it is
// then empty. Only .html files are touched: this must never reach a transcript.
func removePages(htmlDir, bucket string) (int, error) {
	dir := filepath.Join(htmlDir, bucket)
	if !archive.IsRealDir(dir) {
		return 0, nil // nothing published - or a link, whose target is not ours to empty
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, nil // nothing published for this project
	}

	removed := 0
	var firstErr error
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".html") {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !strings.EqualFold(e.Name(), "index.html") { // the project's listing, not a session
			removed++
		}
	}

	// Leaves the directory alone if anything else is in it.
	os.Remove(dir)
	return removed, firstErr
}

// IsExcluded reports whether a project should be left unrendered.
//
// Exported so doctor asks the same question the renderer does. Two copies of this
// rule would drift, and the failure mode is doctor reporting a project as missing its
// page when it was deliberately withheld.
func IsExcluded(project, bucket string, patterns []string) bool {
	for _, p := range patterns {
		p = strings.TrimSpace(strings.ToLower(p))
		if p == "" {
			continue
		}
		if strings.Contains(strings.ToLower(project), p) || strings.Contains(strings.ToLower(bucket), p) {
			return true
		}
	}
	return false
}

// formatBody escapes everything, then promotes ``` fences to code blocks and, when md
// is set, formats the Markdown outside them.
//
// Escaping happens first and always: transcripts contain arbitrary text, including
// HTML and script tags from pages that were being worked on.
func formatBody(text string, md bool) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	text = Redact(text)

	truncated := false
	if len(text) > maxBlockChars {
		cut := maxBlockChars
		for cut > 0 && !utf8Start(text[cut]) {
			cut--
		}
		text = text[:cut]
		truncated = true
	}

	var b strings.Builder
	parts := strings.Split(text, "```")
	for i, part := range parts {
		if i%2 == 1 {
			// Drop a language tag on the opening fence line.
			if nl := strings.IndexByte(part, '\n'); nl >= 0 && len(strings.TrimSpace(part[:nl])) <= 15 {
				part = part[nl+1:]
			}
			b.WriteString(`<pre><code>`)
			b.WriteString(html.EscapeString(part))
			b.WriteString(`</code></pre>`)
		} else if strings.TrimSpace(part) != "" {
			// Edge newlines would show as blank lines next to a code block.
			escaped := html.EscapeString(strings.Trim(part, "\r\n"))
			if md {
				escaped = markdown(escaped)
			}
			b.WriteString(`<div class="t">`)
			b.WriteString(escaped)
			b.WriteString(`</div>`)
		}
	}
	if truncated {
		b.WriteString(`<div class="trunc">… truncated; the full content is in the .jsonl</div>`)
	}
	return b.String()
}

func utf8Start(c byte) bool { return c&0xC0 != 0x80 }

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
