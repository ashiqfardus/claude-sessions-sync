package identity

import (
	"os"
	"path/filepath"
	"strings"
)

// skipDirs are never worth descending into when hunting for a project folder. They
// are large, numerous, and never a checkout of the project you are looking for.
var skipDirs = map[string]bool{
	"node_modules": true, ".git": true, "vendor": true, "AppData": true,
	"Windows": true, "ProgramData": true, "Program Files": true,
	"Program Files (x86)": true, "$Recycle.Bin": true,
	"System Volume Information": true, ".cache": true, "dist": true,
	"build": true, "venv": true, ".venv": true, "target": true,
	"Library": true, ".Trash": true, "__pycache__": true,
}

// How describes which rung of the ladder produced a match, because the confidence
// differs sharply and the user deserves to be told which one was used.
type How string

const (
	ByRecordedPath How = "recorded path"
	ByGitRemote    How = "git remote"
	ByFolderName   How = "folder name"
	NotFound       How = "not found"
	Ambiguous      How = "ambiguous"
)

// Match is the outcome of resolving one archived project to a local folder.
type Match struct {
	How        How
	Path       string
	Candidates []string // populated when Ambiguous, so the user can choose
}

// Resolve finds where an archived project lives on this machine.
//
//  1. the recorded absolute path, if it exists here      (identical layout)
//  2. a git remote match among candidate folders         (strongest identity)
//  3. a unique folder-name match
//  4. anything else is reported, never guessed
//
// Guessing is the one thing this must not do: filing a transcript under the wrong
// project is worse than leaving it unfiled, because it is silent and the user has no
// reason to go looking for it.
func Resolve(recordedPath, leaf, remote string, scanner *Scanner) Match {
	if recordedPath != "" {
		if st, err := os.Stat(recordedPath); err == nil && st.IsDir() {
			return Match{How: ByRecordedPath, Path: recordedPath}
		}
	}

	if leaf == "" {
		return Match{How: NotFound}
	}

	candidates := scanner.Find(leaf)
	switch len(candidates) {
	case 0:
		return Match{How: NotFound}
	case 1:
		// A single candidate is still worth confirming when a remote is available: a
		// same-named folder that is a different repository is a real possibility.
		if remote != "" {
			if local := Remote(candidates[0]); local != "" && !SameRemote(local, remote) {
				return Match{How: NotFound, Candidates: candidates}
			}
			if local := Remote(candidates[0]); local != "" {
				return Match{How: ByGitRemote, Path: candidates[0]}
			}
		}
		return Match{How: ByFolderName, Path: candidates[0]}
	}

	if remote != "" {
		var byRemote []string
		for _, c := range candidates {
			if SameRemote(Remote(c), remote) {
				byRemote = append(byRemote, c)
			}
		}
		if len(byRemote) == 1 {
			return Match{How: ByGitRemote, Path: byRemote[0]}
		}
		if len(byRemote) > 1 {
			return Match{How: Ambiguous, Candidates: byRemote}
		}
	}

	return Match{How: Ambiguous, Candidates: candidates}
}

// Scanner is a one-pass index of the search roots, keyed by folder name.
//
// Resolving each project independently means walking every search root once per
// project: with twenty archived projects and a large dev folder, that is twenty full
// directory walks to answer twenty single-name questions. Scan once, answer from
// memory.
type Scanner struct {
	byName map[string][]string
}

// Find returns every scanned directory with this name, compared case-insensitively.
func (s *Scanner) Find(leaf string) []string {
	if s == nil {
		return nil
	}
	return s.byName[strings.ToLower(leaf)]
}

// NewScanner walks the roots once, indexing every directory name it sees.
func NewScanner(roots []string, maxDepth int) *Scanner {
	s := &Scanner{byName: map[string][]string{}}
	if maxDepth <= 0 {
		maxDepth = 3
	}

	seen := map[string]bool{}

	type node struct {
		path  string
		depth int
	}

	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		abs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if st, err := os.Stat(abs); err != nil || !st.IsDir() {
			continue
		}

		queue := []node{{path: abs}}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]

			entries, err := os.ReadDir(cur.path)
			if err != nil {
				continue // unreadable directory: skip, never fail the whole scan
			}
			for _, e := range entries {
				if !e.IsDir() || skipDirs[e.Name()] {
					continue
				}
				child := filepath.Join(cur.path, e.Name())
				if key := strings.ToLower(child); !seen[key] {
					seen[key] = true
					name := strings.ToLower(e.Name())
					s.byName[name] = append(s.byName[name], child)
				}
				if cur.depth+1 < maxDepth {
					queue = append(queue, node{path: child, depth: cur.depth + 1})
				}
			}
		}
	}
	return s
}
