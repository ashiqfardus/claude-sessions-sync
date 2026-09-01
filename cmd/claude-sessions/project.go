package main

import (
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
)

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
		if s, err := claude.ScanHead(newest.Path, headScanLines); err == nil && s.Cwd != "" {
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
