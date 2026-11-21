// Package work finds and archives the folders Claude worked in outside ~/.claude.
package work

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
)

// Candidate is a path a transcript suggests Claude worked at. File is true for a
// write-tool target, false for a folder (cwd or cd target).
type Candidate struct {
	Path string
	File bool
}

type toolLine struct {
	Message struct {
		Content []struct {
			Type  string `json:"type"`
			Name  string `json:"name"`
			Input struct {
				FilePath     string `json:"file_path"`
				NotebookPath string `json:"notebook_path"`
				Command      string `json:"command"`
			} `json:"input"`
		} `json:"content"`
	} `json:"message"`
}

var (
	cdRe  = regexp.MustCompile(`(?:^|[;&|(\s])cd\s+(?:"([^"]*)"|'([^']*)'|([^\s;&|)]+))`)
	absRe = regexp.MustCompile(`^(?:/|[A-Za-z]:[\\/])`)
)

// Candidates lists the cwds, cd targets and written files in a transcript from byte
// offset on, deduplicated by exact string in first-seen order. home expands a leading
// "~/". The bytes before offset are ones this machine did not write - a transcript
// another machine's import filed here - and never name a folder to back up.
func Candidates(transcript, home string, offset int64) ([]Candidate, error) {
	f, err := os.Open(transcript)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	var out []Candidate
	seen := map[Candidate]bool{}
	add := func(p string, file bool) {
		c := Candidate{p, file}
		if p != "" && !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	cwdKey := []byte(`"cwd":"`)
	r := bufio.NewReaderSize(f, 256*1024)
	for {
		line, readErr := r.ReadBytes('\n')
		if i := bytes.Index(line, cwdKey); i >= 0 {
			var cwd string
			if json.NewDecoder(bytes.NewReader(line[i+len(cwdKey)-1:])).Decode(&cwd) == nil {
				add(cwd, false)
			}
		}
		if bytes.Contains(line, []byte(`"tool_use"`)) {
			var tl toolLine
			if json.Unmarshal(line, &tl) == nil {
				for _, b := range tl.Message.Content {
					if b.Type != "tool_use" {
						continue
					}
					switch b.Name {
					case "Write", "Edit", "MultiEdit":
						add(b.Input.FilePath, true)
					case "NotebookEdit":
						add(b.Input.NotebookPath, true)
					case "Bash":
						for _, m := range cdRe.FindAllStringSubmatch(b.Input.Command, -1) {
							if p := expandHome(m[1]+m[2]+m[3], home); absRe.MatchString(p) {
								add(p, false)
							}
						}
					}
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return out, nil
			}
			return out, readErr
		}
	}
}

// expandHome joins home with the rest of a "~/..." path using home's own separator.
func expandHome(p, home string) string {
	if !strings.HasPrefix(p, "~/") {
		return p
	}
	sep := "/"
	if absRe.MatchString(home) && !strings.HasPrefix(home, "/") {
		sep = `\`
	}
	return strings.TrimRight(home, `/\`) + sep + strings.ReplaceAll(p[2:], "/", sep)
}
