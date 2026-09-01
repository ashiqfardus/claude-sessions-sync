package claude

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Bucket is one project folder under <root>/projects.
type Bucket struct {
	Name        string // folder name exactly as it is on disk
	Dir         string // absolute path to the bucket
	Transcripts []Transcript
	Memory      []string // memory/*.md file names
}

// Transcript is a single session file inside a bucket.
type Transcript struct {
	ID      string // the file's base name, which is the session id
	Path    string
	Size    int64
	ModTime time.Time
}

// HasTranscripts reports whether the bucket holds any session at all.
//
// A bucket with memory but no transcripts is a real and awkward case: its identity
// cannot be read from a cwd, so it needs one of the fallbacks in identity
// resolution or it stays unroutable on import.
func (b Bucket) HasTranscripts() bool { return len(b.Transcripts) > 0 }

// Newest returns the most recently modified transcript. Identity is read from this
// one: it is the likeliest to carry the project's current path.
func (b Bucket) Newest() (Transcript, bool) {
	if len(b.Transcripts) == 0 {
		return Transcript{}, false
	}
	best := b.Transcripts[0]
	for _, t := range b.Transcripts[1:] {
		if t.ModTime.After(best.ModTime) {
			best = t
		}
	}
	return best, true
}

// Slug flattens an absolute project path into the bucket name Claude Code uses.
//
// This is ONE-WAY. The flattening is lossy - a '-' in the result may have been a
// separator, a space, or a literal hyphen - so a bucket name can never be decoded
// back into a path. Recover the real path from a transcript's cwd instead.
//
// Note also that CLAUDE_CODE_PROJECT_DIR_NAME (Claude Code v2.1.234+) pins the
// bucket name outright, in which case a bucket name is not a slug at all and must
// not be compared against one.
func Slug(path string) string {
	return strings.NewReplacer(":", "-", `\`, "-", "/", "-", " ", "-").Replace(path)
}

// FindBucket returns the existing bucket matching slug, compared case-insensitively.
//
// Casing follows the cwd string Claude saw, which is not always what is on disk:
// E:\ has been recorded as "e--". Always prefer an existing bucket over a freshly
// computed slug, or transcripts land in a second bucket that /resume never shows.
func FindBucket(projectsDir, slug string) (Bucket, bool) {
	buckets, err := ListBuckets(projectsDir)
	if err != nil {
		return Bucket{}, false
	}
	for _, b := range buckets {
		if strings.EqualFold(b.Name, slug) {
			return b, true
		}
	}
	return Bucket{}, false
}

// ListBuckets reads every project bucket under projectsDir.
//
// An unreadable bucket is skipped rather than failing the whole listing: one bad
// directory must not hide every other session.
func ListBuckets(projectsDir string) ([]Bucket, error) {
	dirs, err := os.ReadDir(projectsDir)
	if err != nil {
		return nil, err
	}

	var buckets []Bucket
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		b := Bucket{Name: d.Name(), Dir: filepath.Join(projectsDir, d.Name())}

		files, err := os.ReadDir(b.Dir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.EqualFold(filepath.Ext(f.Name()), ".jsonl") {
				continue
			}
			info, err := f.Info()
			if err != nil {
				continue
			}
			b.Transcripts = append(b.Transcripts, Transcript{
				ID:      strings.TrimSuffix(f.Name(), filepath.Ext(f.Name())),
				Path:    filepath.Join(b.Dir, f.Name()),
				Size:    info.Size(),
				ModTime: info.ModTime(),
			})
		}

		if mem, err := os.ReadDir(filepath.Join(b.Dir, "memory")); err == nil {
			for _, m := range mem {
				if !m.IsDir() && strings.EqualFold(filepath.Ext(m.Name()), ".md") {
					b.Memory = append(b.Memory, m.Name())
				}
			}
		}

		sort.Slice(b.Transcripts, func(i, j int) bool {
			return b.Transcripts[i].ModTime.After(b.Transcripts[j].ModTime)
		})
		buckets = append(buckets, b)
	}

	sort.Slice(buckets, func(i, j int) bool { return buckets[i].Name < buckets[j].Name })
	return buckets, nil
}
