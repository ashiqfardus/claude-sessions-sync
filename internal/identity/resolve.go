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
func Resolve(recordedPath, leaf, remote string, searchRoots []string, maxDepth int) Match {
	if recordedPath != "" {
		if st, err := os.Stat(recordedPath); err == nil && st.IsDir() {
			return Match{How: ByRecordedPath, Path: recordedPath}
		}
	}

	if leaf == "" {
		return Match{How: NotFound}
	}

	candidates := findByLeaf(leaf, searchRoots, maxDepth)
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

// findByLeaf walks the search roots breadth-first looking for directories with the
// given name, comparing case-insensitively.
func findByLeaf(leaf string, roots []string, maxDepth int) []string {
	if maxDepth <= 0 {
		maxDepth = 3
	}

	seen := map[string]bool{}
	var found []string

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
				if strings.EqualFold(e.Name(), leaf) && !seen[strings.ToLower(child)] {
					seen[strings.ToLower(child)] = true
					found = append(found, child)
				}
				if cur.depth+1 < maxDepth {
					queue = append(queue, node{path: child, depth: cur.depth + 1})
				}
			}
		}
	}
	return found
}
