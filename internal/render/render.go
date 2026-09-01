// Package render turns archived transcripts into pages you can read on a phone.
//
// The .jsonl files are never modified or removed - HTML is a convenience layer on
// top. If a transcript fails to render, the transcript is still there and still
// resumable, which is why every failure here is counted rather than fatal.
package render

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
)

// maxBlockChars caps one block of content.
//
// A single tool result can carry an entire file. Rendering all of it produces a page
// a phone cannot open, and the full text is one click away in the .jsonl.
const maxBlockChars = 4000

// Result reports what a render pass did.
type Result struct {
	Rendered  int
	UpToDate  int
	Failed    int
	Skipped   int // transcript lines that did not parse
	OutputDir string
}

// Archive renders every transcript in the archive.
//
// force re-renders pages that are already current, which is what you want after
// changing the template.
func Archive(dest string, force bool) (Result, error) {
	res := Result{OutputDir: filepath.Join(dest, "html")}

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

	for _, bucket := range buckets {
		bucketDir := filepath.Join(archive.ProjectsDir(dest), bucket)
		files, err := os.ReadDir(bucketDir)
		if err != nil {
			continue
		}

		project := bucket
		if p, ok := manifest[bucket]; ok && p.Path != "" {
			project = p.Path
		}

		for _, f := range files {
			if f.IsDir() || !strings.EqualFold(filepath.Ext(f.Name()), ".jsonl") {
				continue
			}
			src := filepath.Join(bucketDir, f.Name())
			id := strings.TrimSuffix(f.Name(), filepath.Ext(f.Name()))
			out := filepath.Join(res.OutputDir, bucket, id+".html")

			info, err := f.Info()
			if err != nil {
				res.Failed++
				continue
			}

			page, err := renderOne(src, project, id, force, out, info)
			switch {
			case err != nil:
				// One bad transcript must not stop the rest: the point of the archive
				// is that it survives the tooling.
				res.Failed++
				continue
			case page.upToDate:
				res.UpToDate++
			default:
				res.Rendered++
				res.Skipped += page.skipped
			}

			entries = append(entries, indexEntry{
				Project: project, Bucket: bucket, ID: id,
				Href:    filepath.ToSlash(filepath.Join(bucket, id+".html")),
				Updated: info.ModTime(), Size: info.Size(), Prompt: page.prompt,
			})
		}
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Updated.After(entries[j].Updated) })

	var b strings.Builder
	b.WriteString(pageHead("Claude sessions"))
	b.WriteString(`<h1>Claude sessions</h1>`)
	fmt.Fprintf(&b, `<p class="meta">%d session(s) &middot; updated %s</p>`,
		len(entries), html.EscapeString(time.Now().Format("2006-01-02 15:04")))

	byProject := map[string][]indexEntry{}
	for _, e := range entries {
		byProject[e.Project] = append(byProject[e.Project], e)
	}
	names := make([]string, 0, len(byProject))
	for k := range byProject {
		names = append(names, k)
	}
	sort.Strings(names)

	for _, name := range names {
		fmt.Fprintf(&b, `<h2>%s</h2><ul class="sessions">`, html.EscapeString(name))
		for _, e := range byProject[name] {
			prompt := e.Prompt
			if prompt == "" {
				prompt = "(no opening message)"
			}
			fmt.Fprintf(&b,
				`<li><a href="%s"><span class="when">%s</span><span class="prompt">%s</span><span class="size">%s</span></a></li>`,
				html.EscapeString(e.Href),
				html.EscapeString(e.Updated.Format("2006-01-02 15:04")),
				html.EscapeString(prompt),
				html.EscapeString(humanSize(e.Size)))
		}
		b.WriteString(`</ul>`)
	}
	b.WriteString(pageFoot())

	if err := archive.WriteFileAtomic(filepath.Join(res.OutputDir, "index.html"), []byte(b.String()), 0o644); err != nil {
		return res, err
	}
	return res, nil
}

type pageInfo struct {
	upToDate bool
	prompt   string
	skipped  int
}

func renderOne(src, project, id string, force bool, out string, srcInfo os.FileInfo) (pageInfo, error) {
	var info pageInfo

	// Up to date when the page is at least as new as the transcript. The same
	// tolerance as everywhere else: a synced filesystem rounds timestamps, and an
	// exact comparison would re-render the whole archive on every run.
	if !force {
		if outInfo, err := os.Stat(out); err == nil {
			if !outInfo.ModTime().Before(srcInfo.ModTime().Add(-archive.MTimeToleranceSeconds * time.Second)) {
				if s, err := claude.ScanHead(src, 200); err == nil {
					info.prompt = s.FirstPrompt
				}
				info.upToDate = true
				return info, nil
			}
		}
	}

	var body strings.Builder
	var first time.Time
	var last time.Time

	stats, err := claude.Walk(src, func(r claude.Record) bool {
		if info.prompt == "" && r.Type == "user" && !r.IsMeta && r.Text != "" {
			info.prompt = r.Text
			if len(info.prompt) > 120 {
				info.prompt = info.prompt[:120] + "..."
			}
		}
		if !r.Timestamp.IsZero() {
			if first.IsZero() {
				first = r.Timestamp
			}
			last = r.Timestamp
		}
		writeTurn(&body, r)
		return true
	})
	if err != nil {
		return info, err
	}
	info.skipped = stats.Skipped

	var b strings.Builder
	b.WriteString(pageHead(project + " — " + id))
	b.WriteString(`<p class="back"><a href="../index.html">&larr; all sessions</a></p>`)
	fmt.Fprintf(&b, `<h1>%s</h1>`, html.EscapeString(project))
	fmt.Fprintf(&b, `<p class="meta">%s`, html.EscapeString(id))
	if !first.IsZero() {
		fmt.Fprintf(&b, ` &middot; %s → %s`,
			html.EscapeString(first.Format("2006-01-02 15:04")),
			html.EscapeString(last.Format("15:04")))
	}
	b.WriteString(`</p>`)
	b.WriteString(body.String())

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
	_ = os.Chtimes(out, srcInfo.ModTime(), srcInfo.ModTime())
	return info, nil
}

func writeTurn(b *strings.Builder, r claude.Record) {
	if len(r.Blocks) == 0 {
		return
	}

	role := r.Role
	if role == "" {
		role = r.Type
	}
	switch role {
	case "user", "assistant":
	default:
		return // system plumbing: not part of the conversation
	}

	when := ""
	if !r.Timestamp.IsZero() {
		when = r.Timestamp.Format("15:04")
	}

	fmt.Fprintf(b, `<div class="turn %s"><div class="who">%s<span class="time">%s</span></div>`,
		html.EscapeString(role), html.EscapeString(role), html.EscapeString(when))

	for _, blk := range r.Blocks {
		switch blk.Kind {
		case "text":
			b.WriteString(formatBody(blk.Text))
		case "thinking":
			// Collapsed: interesting when you want it, noise when you do not.
			b.WriteString(`<details class="thinking"><summary>thinking</summary>`)
			b.WriteString(formatBody(blk.Text))
			b.WriteString(`</details>`)
		case "tool_use":
			name := blk.Name
			if name == "" {
				name = "tool"
			}
			fmt.Fprintf(b, `<details class="tool"><summary>&#9881; %s</summary>`, html.EscapeString(name))
			b.WriteString(formatBody(blk.Text))
			b.WriteString(`</details>`)
		case "tool_result":
			if strings.TrimSpace(blk.Text) == "" {
				continue
			}
			b.WriteString(`<details class="result"><summary>result</summary>`)
			b.WriteString(formatBody(blk.Text))
			b.WriteString(`</details>`)
		default:
			if strings.TrimSpace(blk.Text) != "" {
				fmt.Fprintf(b, `<details class="tool"><summary>%s</summary>`, html.EscapeString(blk.Kind))
				b.WriteString(formatBody(blk.Text))
				b.WriteString(`</details>`)
			}
		}
	}
	b.WriteString(`</div>`)
}

// formatBody escapes everything, then promotes ``` fences to code blocks.
//
// Escaping happens first and always: transcripts contain arbitrary text, including
// HTML and script tags from pages that were being worked on.
func formatBody(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}

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
			b.WriteString(`<div class="t">`)
			b.WriteString(html.EscapeString(part))
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
