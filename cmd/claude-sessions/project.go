package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/convzip"
)

// sourceFlags adds --archived and --archive to ls, search and stats.
//
// They used to read only this machine's ~/.claude/projects - never the archive, which
// is the one place a session survives once Claude Code has deleted it locally. The
// archive has the same bucket layout, so the same code reads either.
func sourceFlags(fs *flag.FlagSet) (archived *bool, archiveDir *string) {
	archived = fs.Bool("archived", false, "read the archive instead of this machine's sessions")
	archiveDir = fs.String("archive", "", "archive folder to read (implies --archived)")
	return archived, archiveDir
}

// sessionsDir resolves where to read sessions from.
func sessionsDir(root string, archived bool, archiveDir string) (string, error) {
	if !archived && archiveDir == "" {
		return claude.ProjectsDir(root), nil
	}
	dest, _, err := archive.ResolveDestination(root, archiveDir)
	if err != nil {
		return "", err
	}
	if dest == "" {
		return "", fmt.Errorf("no archive destination: run `claude-sessions config set-destination <folder>`")
	}
	return archive.ProjectsDir(dest), nil
}

// headScanLines caps how far into a transcript a listing reads for the cwd and the
// first prompt. Both live in the opening entries; reading a 40MB file to print one
// row would make a listing unusable.
const headScanLines = 200

// resolver turns a bucket into the project's real path, once per bucket.
//
// ls, search and stats each used to work this out for themselves, which meant three
// copies of the same rule and, in search's case, reading every transcript twice.
type resolver struct {
	cache map[string]string
}

func newResolver() *resolver { return &resolver{cache: map[string]string{}} }

// Path returns the project's absolute path for a bucket, falling back to the bucket
// name when no transcript records a cwd.
//
// The bucket name is only ever a fallback label: it cannot be decoded back into a
// path, because the flattening is lossy.
func (r *resolver) Path(b claude.Bucket) string {
	if p, ok := r.cache[b.Name]; ok {
		return p
	}

	path := b.Name
	if newest, ok := b.Newest(); ok {
		if s, err := scanHead(newest.Path, headScanLines); err == nil && s.Cwd != "" {
			path = s.Cwd
		}
	}
	r.cache[b.Name] = path
	return path
}

// Set records a path discovered elsewhere - search reads the cwd during its full walk
// rather than paying for a second pass over the same file.
func (r *resolver) Set(bucket, path string) {
	if path != "" {
		r.cache[bucket] = path
	}
}

// Known reports a cached path without reading anything.
func (r *resolver) Known(bucket string) (string, bool) {
	p, ok := r.cache[bucket]
	return p, ok
}

// withZipped adds each bucket's zipped conversations to its transcripts, for --archived.
// A conversation with a loose transcript is listed once, as that. A zipped one carries
// its zip's path, size and time; scanHead and walkTranscript read it through Materialize.
func withZipped(buckets []claude.Bucket) []claude.Bucket {
	for i := range buckets {
		b := &buckets[i]
		have := map[string]bool{}
		for _, t := range b.Transcripts {
			have[strings.ToLower(t.ID)] = true
		}
		for lid, zp := range convzip.Find(b.Dir) {
			st, err := os.Lstat(zp)
			if have[lid] || err != nil {
				continue
			}
			b.Transcripts = append(b.Transcripts, claude.Transcript{
				ID: strings.TrimSuffix(filepath.Base(zp), convzip.Suffix), Path: zp,
				Size: st.Size(), ModTime: st.ModTime(),
			})
		}
		sort.Slice(b.Transcripts, func(x, y int) bool {
			return b.Transcripts[x].ModTime.After(b.Transcripts[y].ModTime)
		})
	}
	return buckets
}

func isZip(path string) bool { return strings.HasSuffix(path, convzip.Suffix) }

// materialize unpacks a zipped conversation; every command reads one through it, and
// tests count the calls.
var materialize = convzip.Materialize

// openTranscript returns a readable transcript path: a loose one as it is, a zipped
// one extracted under a temp dir that cleanup removes, even on error. A zip that cannot
// be read costs one note on stderr.
func openTranscript(path string) (string, func(), error) {
	if !isZip(path) {
		return path, func() {}, nil
	}
	tmp, err := os.MkdirTemp("", "cs-zip")
	if err != nil {
		return "", nil, err
	}
	id := strings.TrimSuffix(filepath.Base(path), convzip.Suffix)
	p, cleanup, err := materialize(filepath.Dir(path), id, tmp)
	if err != nil {
		os.RemoveAll(tmp)
		fmt.Fprintf(os.Stderr, "skipped %s: %v\n", path, err)
		return "", nil, err
	}
	return p, func() { cleanup(); os.RemoveAll(tmp) }, nil
}

// scanHead is claude.ScanHead for a loose or zipped transcript. shortcut: a zipped
// conversation is extracted whole, as Materialize offers no head-only read; upgrade if
// listing a drive of large zips proves slow.
func scanHead(path string, maxLines int) (claude.Summary, error) {
	p, cleanup, err := openTranscript(path)
	if err != nil {
		return claude.Summary{}, err
	}
	defer cleanup()
	return claude.ScanHead(p, maxLines)
}
