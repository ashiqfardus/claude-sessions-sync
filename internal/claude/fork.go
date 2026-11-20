package claude

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"regexp"
)

// Relation is how two copies of one session relate to each other.
type Relation int

const (
	Unknown Relation = iota // nothing to compare by: decide as before, by date and size
	Same                    // the same newest entry: the same conversation
	Extends                 // the first holds everything the second does, and more
	Behind                  // the second holds everything the first does, and more
	Forked                  // each holds turns the other lacks: continued on two machines
)

func (r Relation) String() string {
	return [...]string{"unknown", "same", "extends", "behind", "forked"}[r]
}

// Compare tells how transcript a relates to transcript b.
//
// Transcripts are append-only, so the newest entry of one appearing in the other means
// the other holds all of it. Entries are matched by uuid, not by bytes: import rewrites
// paths, so a filed copy differs from its archived source while holding the same
// conversation.
func Compare(a, b string) Relation {
	if appendedTo(a, b) {
		return Extends // the common case, settled without reading all of a
	}
	ua, ta := lastUUID(a)
	ub, tb := lastUUID(b)
	switch {
	case ua == "" || ub == "":
		return Unknown
	case ua == ub:
		// The same last turn. Claude Code then appends entries with no uuid - a title
		// from /rename, cost, the last prompt - and the copy with more of them is the
		// later one: a rename with no new turn is still a change.
		switch {
		case ta > tb:
			return Extends
		case ta < tb:
			return Behind
		}
		return Same
	case hasUUID(a, ub):
		return Extends
	case hasUUID(b, ua):
		return Behind
	}
	return Forked
}

// appendedTo reports whether a is b with more appended, byte for byte, judged by the
// final block of b: every line carries its own uuid and timestamp, so two different
// continuations never share those bytes.
func appendedTo(a, b string) bool {
	const window = 64 << 10
	fa, err := os.Open(a)
	if err != nil {
		return false
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false
	}
	defer fb.Close()
	sa, err1 := fa.Stat()
	sb, err2 := fb.Stat()
	if err1 != nil || err2 != nil || sb.Size() == 0 || sa.Size() <= sb.Size() {
		return false
	}
	off := max(sb.Size()-window, 0)
	wa := make([]byte, sb.Size()-off)
	wb := make([]byte, len(wa))
	if _, err := fa.ReadAt(wa, off); err != nil {
		return false
	}
	if _, err := fb.ReadAt(wb, off); err != nil && err != io.EOF {
		return false
	}
	return bytes.Equal(wa, wb)
}

// lastUUID is the uuid of the newest entry that has one, read from the file's end,
// and how many entries without one follow it.
// LastUUID is the uuid of a transcript's last entry, "" when it has none.
func LastUUID(path string) string {
	u, _ := lastUUID(path)
	return u
}

// sessionIDRe is the form of every session id Claude Code writes.
var sessionIDRe = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

// IsSessionID reports whether id is a session id: a sync client's conflict copy,
// "<id> (conflicted copy).jsonl", is not one.
func IsSessionID(id string) bool { return sessionIDRe.MatchString(id) }

// legacyAgentRe names a subagent transcript as older Claude Code versions kept it,
// beside the sessions: agent-<id>.jsonl.
var legacyAgentRe = regexp.MustCompile(`^agent-[0-9A-Fa-f]{4,}$`)

// IsTranscriptID reports whether a .jsonl file named id at the top of a project
// folder is one Claude Code wrote: a session, or an older version's subagent.
func IsTranscriptID(id string) bool { return IsSessionID(id) || legacyAgentRe.MatchString(id) }

func lastUUID(path string) (uuid string, trailing int) {
	const tail = 4 << 20 // one entry can hold a large tool result
	f, err := os.Open(path)
	if err != nil {
		return "", 0
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", 0
	}
	off := max(st.Size()-tail, 0)
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
		return "", 0
	}
	lines := bytes.Split(buf, []byte("\n"))
	if off > 0 {
		lines = lines[1:] // starts mid-line
	}
	for i := len(lines) - 1; i >= 0; i-- {
		var e struct {
			UUID string `json:"uuid"`
		}
		if json.Unmarshal(lines[i], &e) != nil {
			continue // blank, or a truncated last line
		}
		if e.UUID != "" {
			return e.UUID, trailing
		}
		trailing++
	}
	return "", 0
}

// hasUUID reports whether the transcript has an entry with this uuid, reading it in
// chunks so a transcript of hundreds of megabytes is never held in memory at once.
func hasUUID(path, uuid string) bool {
	needle := []byte(`"uuid":"` + uuid + `"`)
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 1<<20+len(needle))
	keep := 0 // bytes carried over, so a match split across two reads is still seen
	for {
		n, err := f.Read(buf[keep:])
		if bytes.Contains(buf[:keep+n], needle) {
			return true
		}
		if err != nil {
			return false
		}
		if keep+n >= len(needle) {
			keep = copy(buf, buf[keep+n-len(needle)+1:keep+n])
		} else {
			keep += n
		}
	}
}

// ForkID is the session id given to the other copy of a session that was continued
// on two machines, so both can be resumed. It is derived from the original id and
// where the copy came from, so filing it again updates it rather than adding a third.
func ForkID(id, source string) string {
	sum := sha256.Sum256([]byte("claude-sessions fork\x00" + id + "\x00" + source))
	b := sum[:16]
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // RFC 4122 variant
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
