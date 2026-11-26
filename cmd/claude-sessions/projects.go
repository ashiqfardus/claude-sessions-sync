package main

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/identity"
	"github.com/ashiqfardus/claude-sessions-sync/internal/rewrite"
)

// A project is every bucket import treats as the same project: the same git remote,
// otherwise the same folder name. The same project has a different bucket on each
// computer whose path differs, so keeping it on one must keep it on all of them.

// projectsOf is every bucket any machine recorded. A manifest that cannot be read is
// an error: a bucket missing from the view could not be seen to be kept.
func projectsOf(dest string) (map[string]archive.Project, error) {
	projects, bad, err := archive.Merged(dest)
	if err != nil {
		return nil, fmt.Errorf("cannot read the archive's manifest: %w", err)
	}
	if len(bad) > 0 {
		names := make([]string, len(bad))
		for i, b := range bad {
			names[i] = b
			if b != "projects.json" { // the legacy file sits at the root, the rest under manifest/
				names[i] = "manifest/" + b
			}
		}
		return nil, fmt.Errorf("cannot read %s in the archive: if it is a sync conflict copy, delete it; otherwise let it finish syncing", strings.Join(names, ", "))
	}
	return projects, nil
}

// identityOf is what the keep list stores for a project: its remote, else its folder
// name.
func identityOf(p archive.Project) archive.KeepEntry {
	if strings.TrimSpace(p.Remote) != "" {
		return archive.KeepEntry{Remote: p.Remote}
	}
	return archive.KeepEntry{Leaf: strings.ToLower(p.Leaf)}
}

// sameProject is import's rule between two buckets: two remotes must agree, and
// without one on either side the folder names must, exactly.
func sameProject(a, b archive.Project) bool {
	if strings.TrimSpace(a.Remote) != "" && strings.TrimSpace(b.Remote) != "" {
		return identity.SameRemote(a.Remote, b.Remote)
	}
	return a.Leaf != "" && strings.EqualFold(a.Leaf, b.Leaf)
}

// matchProject finds the project arg names - an absolute path from any computer, or a
// folder name, compared exactly and ignoring case - and every bucket of it. A name
// shared by different repositories is refused with the candidates, so the owner can
// give a path instead.
func matchProject(projects map[string]archive.Project, arg string) (archive.KeepEntry, []string, error) {
	byPath := rewrite.AbsoluteAnywhere(arg)
	if byPath && arg[1] == ':' { // E:/web-app as typed; the manifest records E:\web-app
		arg = strings.ReplaceAll(arg, "/", `\`)
	}
	var seeds []string
	for b, p := range projects {
		if byPath && samePath(p.Path, arg) || !byPath && p.Leaf != "" && strings.EqualFold(p.Leaf, arg) {
			seeds = append(seeds, b)
		}
	}
	if len(seeds) == 0 {
		if byPath {
			return archive.KeepEntry{}, nil, fmt.Errorf("no archived project has the path %s", arg)
		}
		return archive.KeepEntry{}, nil, fmt.Errorf("no archived project is named %q (the whole folder name, or a path)", arg)
	}
	sort.Strings(seeds)

	var buckets []string
	for b, p := range projects {
		if slices.ContainsFunc(seeds, func(s string) bool { return s == b || sameProject(p, projects[s]) }) {
			buckets = append(buckets, b)
		}
	}
	sort.Strings(buckets)

	// Group by identity: two remotes in one match are two repositories.
	var groups []archive.KeepEntry
	members := map[int][]string{}
	for _, b := range buckets {
		id := identityOf(projects[b])
		i := slices.IndexFunc(groups, func(g archive.KeepEntry) bool {
			return g.Leaf == id.Leaf && (g.Remote == id.Remote || identity.SameRemote(g.Remote, id.Remote))
		})
		if i < 0 {
			i = len(groups)
			groups = append(groups, id)
		}
		members[i] = append(members[i], b)
	}
	remotes := slices.DeleteFunc(slices.Clone(groups), func(g archive.KeepEntry) bool { return g.Remote == "" })
	if len(remotes) > 1 {
		var msg strings.Builder
		if byPath {
			fmt.Fprintf(&msg, "%s matches %d different projects by its folder name; keep each remote's project by its own path:", arg, len(remotes))
		} else {
			fmt.Fprintf(&msg, "%q matches %d different projects; give the path of the one you mean:", arg, len(remotes))
		}
		for i, g := range groups {
			label := "remote " + identity.NormalRemote(g.Remote) // a token in the remote never reaches the log
			if g.Remote == "" {
				label = "no git remote, folder " + g.Leaf
			}
			fmt.Fprintf(&msg, "\n  %s", label)
			for _, b := range members[i] {
				p := projects[b]
				fmt.Fprintf(&msg, "\n    %s  %s  %s", b, p.Owner(), p.Path)
			}
		}
		return archive.KeepEntry{}, nil, fmt.Errorf("%s", msg.String())
	}

	id := identityOf(projects[seeds[0]])
	if len(remotes) == 1 {
		id = remotes[0] // the stronger identity, when one bucket of the project has it
	}
	if id.Remote == "" && id.Leaf == "" {
		return archive.KeepEntry{}, nil, fmt.Errorf("%s has no git remote or folder name to know it by", arg)
	}
	return id, buckets, nil
}

// kept reports whether prune and the age rule must skip a conversation: it is kept
// itself, or any of its buckets belongs to a kept project. While any project is kept, a
// bucket no manifest lists - its machine forgotten, say - counts as kept: its identity
// is unknown, so it cannot be shown not to be that project.
func kept(r archive.Retention, id string, buckets []string, projects map[string]archive.Project) bool {
	return keptBy(r, projects)(id, buckets)
}

// keptBy is kept for a pass over many conversations: the buckets kept projects cover are
// worked out once, as SameRemote is a regex per call.
func keptBy(r archive.Retention, projects map[string]archive.Project) func(id string, buckets []string) bool {
	sessions, inKept, byProject := map[string]bool{}, map[string]bool{}, false
	for _, e := range r.Keep {
		if e.Session != "" {
			sessions[strings.ToLower(e.Session)] = true
			continue
		}
		byProject = true
		for b, p := range projects {
			if !inKept[b] && keeps(e, p, projects) {
				inKept[b] = true
			}
		}
	}
	return func(id string, buckets []string) bool {
		if sessions[strings.ToLower(id)] {
			return true
		}
		for _, b := range buckets {
			if _, listed := projects[b]; byProject && !listed || inKept[b] {
				return true
			}
		}
		return false
	}
}

// keeps reports whether a project entry covers bucket p. A bucket with no remote of its
// own - its machine could not read one - belongs to a remote's project when its folder
// name matches a bucket that has the remote, as sameProject would say.
func keeps(e archive.KeepEntry, p archive.Project, projects map[string]archive.Project) bool {
	if e.Leaf != "" {
		return p.Leaf != "" && strings.EqualFold(p.Leaf, e.Leaf)
	}
	if e.Remote == "" {
		return false
	}
	if strings.TrimSpace(p.Remote) != "" {
		return identity.SameRemote(p.Remote, e.Remote)
	}
	for _, q := range projects {
		if identity.SameRemote(q.Remote, e.Remote) && sameProject(p, q) {
			return true
		}
	}
	return false
}
