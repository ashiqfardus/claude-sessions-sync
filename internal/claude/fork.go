package claude

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"time"
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
	ua, _, ta := lastTurn(a)
	ub, _, tb := lastTurn(b)
	switch {
	case ua == "" || ub == "":
		return Unknown
	case ua == ub:
		// The same last turn. Claude Code then appends entries that are no turn - a
		// title from /rename, cost, the last prompt - and the copy with more of them is
		// the later one: a rename with no new turn is still a change.
		switch {
		case ta > tb:
			return Extends
		case ta < tb:
			return Behind
		}
		return Same
	case HasUUID(a, ub):
		return Extends
	case HasUUID(b, ua):
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

// LastTurn is the uuid and timestamp of a transcript's last turn, from one read, so
// both name the same turn while it grows: "" when it has none, and a zero time when
// that turn has no timestamp that parses.
func LastTurn(path string) (uuid string, at time.Time) {
	uuid, at, _ = lastTurn(path)
	return uuid, at
}

// LastUUID is the uuid of a transcript's last turn, "" when it has none.
func LastUUID(path string) string {
	u, _, _ := lastTurn(path)
	return u
}

// LastTurnTime is the timestamp of the turn LastUUID names. It is false when there is
// no turn, or that turn has no timestamp that parses: an earlier turn's would make the
// conversation look older than it is.
func LastTurnTime(path string) (time.Time, bool) {
	_, at, _ := lastTurn(path)
	return at, !at.IsZero()
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

// turnTypes are the entries that make up the conversation. Claude Code also writes
// lines that add no turn - cost-state, last-prompt, tools-used, derived-from,
// bridge-session with no uuid; hook attachments with one, on resume - and a copy that
// only gained those must not look forked from, or newer than, one with more turns.
// A queued prompt is an attachment too, but it is the user's input: a copy ending in
// one not yet run holds a turn the other lacks.
var turnTypes = map[string]bool{"user": true, "assistant": true, "system": true}

// lastTurn is the uuid and time of the newest turn, read from the file's end, and how
// many other entries follow it.
func lastTurn(path string) (uuid string, at time.Time, trailing int) {
	const tail = 4 << 20 // one entry can hold a large tool result
	f, err := os.Open(path)
	if err != nil {
		return "", time.Time{}, 0
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", time.Time{}, 0
	}
	off := max(st.Size()-tail, 0)
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
		return "", time.Time{}, 0
	}
	lines := bytes.Split(buf, []byte("\n"))
	if off > 0 {
		lines = lines[1:] // starts mid-line
	}
	for i := len(lines) - 1; i >= 0; i-- {
		var e struct {
			Type       string          `json:"type"`
			UUID       string          `json:"uuid"`
			Timestamp  string          `json:"timestamp"`
			Attachment json.RawMessage `json:"attachment"` // raw: any shape still parses the line
		}
		if json.Unmarshal(lines[i], &e) != nil {
			continue // blank, or a truncated last line
		}
		if e.UUID != "" && (turnTypes[e.Type] || e.Type == "attachment" && queuedPrompt(e.Attachment)) {
			return e.UUID, parseTime(e.Timestamp), trailing
		}
		trailing++
	}
	return "", time.Time{}, 0
}

// queuedPrompt reports whether an attachment is a prompt the user queued.
func queuedPrompt(raw json.RawMessage) bool {
	var a struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(raw, &a) == nil && a.Type == "queued_command"
}

// HasUUID reports whether the transcript has an entry with this uuid, reading it in
// chunks so a transcript of hundreds of megabytes is never held in memory at once.
func HasUUID(path, uuid string) bool {
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

// HoldsAll reports whether transcript a has every turn transcript b has, matched by
// uuid: the proof wanted before b is deleted for a. Compare judges by b's last turn
// alone, which a file holding just that one uuid also passes. A line that is not JSON,
// in either, is a no.
func HoldsAll(a, b string) bool {
	want, ok := turnUUIDs(b)
	if !ok {
		return false
	}
	if len(want) == 0 {
		return true // nothing of b's to lose
	}
	have, ok := turnUUIDs(a)
	if !ok {
		return false
	}
	for u := range want {
		if !have[u] {
			return false
		}
	}
	return true
}

// turnUUIDs reads every turn's uuid in a transcript, a line at a time.
func turnUUIDs(path string) (map[string]bool, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	out := map[string]bool{}
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var e struct {
				Type       string          `json:"type"`
				UUID       string          `json:"uuid"`
				Attachment json.RawMessage `json:"attachment"`
			}
			if json.Unmarshal(line, &e) != nil {
				return nil, false
			}
			if e.UUID != "" && (turnTypes[e.Type] || e.Type == "attachment" && queuedPrompt(e.Attachment)) {
				out[e.UUID] = true
			}
		}
		if err == io.EOF {
			return out, true
		}
		if err != nil {
			return nil, false
		}
	}
}
