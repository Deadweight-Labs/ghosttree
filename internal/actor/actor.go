// Package actor says who wrote an entry (REQ-620): a person in the web
// interface or an agent, and for an agent its harness, session and machine.
// The server decides it from the caller (token kind, owned session), never
// from a field a tool sends. The package holds the types, the names a page
// shows and the reading of the model from transcript lines; storage lives in
// the store.
package actor

import (
	"encoding/json"
	"strings"
	"time"
	"unicode"
)

// Kinds of actor. Unknown (empty) is what old entries without proof keep.
const (
	Human = "human"
	Agent = "agent"
)

// Entry kinds that carry an actor. Creation kinds name the object; Criterion
// and RequestDone name who closed it, and a later closing replaces the earlier
// one. A vote is keyed by the row of the voter's vote, which stays when the
// vote changes.
const (
	Request     = "request"
	Criterion   = "criterion"
	RequestDone = "request_done"
	Pain        = "pain"
	Idea        = "idea"
	Knowledge   = "knowledge"
	Task        = "task"
	Comment     = "comment"
	Reaction    = "reaction"
	Message     = "message"
	Vote        = "vote"
	Feature     = "feature"
)

// Replaces says whether a later stamp of this kind replaces the earlier one,
// instead of the first one staying: closing again after a reopen, and a
// reaction, whose row id SQLite may hand out again after one was taken back.
func Replaces(kind string) bool { return kind == Criterion || kind == RequestDone || kind == Reaction }

// ValidKind says whether kind is one of the kinds above.
func ValidKind(kind string) bool {
	switch kind {
	case Request, Criterion, RequestDone, Pain, Idea, Knowledge, Task, Comment, Reaction, Message, Vote, Feature:
		return true
	}
	return false
}

// Actor is who wrote one entry. AccountID and Person are the account behind
// it in both cases; for an agent, Harness, SessionID and Machine are filled
// where the server could prove them.
type Actor struct {
	Kind      string `json:"kind"`
	AccountID int64  `json:"-"`
	Person    string `json:"person,omitempty"`
	Harness   string `json:"harness,omitempty"`
	Machine   string `json:"machine,omitempty"`
	SessionID int64  `json:"-"`
	// SessionRef is the external id of the session when the server knows it
	// from the request but the session is not uploaded yet.
	SessionRef string `json:"-"`
}

// IsAgent and IsHuman read the kind.
func (a Actor) IsAgent() bool { return a.Kind == Agent }
func (a Actor) IsHuman() bool { return a.Kind == Human }

// Family is the harness as a person names it: Claude, Codex, OpenCode. An
// agent without a known harness is "Agent".
func Family(harness string) string {
	h := strings.ToLower(strings.TrimSpace(harness))
	switch {
	case h == "claude" || strings.HasPrefix(h, "claude-") || strings.HasPrefix(h, "claude_"):
		return "Claude"
	case h == "codex" || strings.HasPrefix(h, "codex-"):
		return "Codex"
	case h == "opencode":
		return "OpenCode"
	case h == "":
		return "Agent"
	}
	r := []rune(h)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// HarnessKey is the short form used by the search language and statistics:
// claude, codex, opencode, or the harness itself.
func HarnessKey(harness string) string {
	h := strings.ToLower(strings.TrimSpace(harness))
	switch {
	case h == "claude" || strings.HasPrefix(h, "claude-"):
		return "claude"
	case h == "codex" || strings.HasPrefix(h, "codex-"):
		return "codex"
	}
	return h
}

// ModelLabel is the name of a model as a person reads it: claude-opus-5-5 is
// "Opus 5.5", claude-3-5-sonnet-20241022 is "Sonnet 3.5". Other providers keep
// their identifier, which already reads well (gpt-5.6-sol).
func ModelLabel(model string) string {
	m := strings.TrimSpace(model)
	low := strings.ToLower(m)
	if !strings.HasPrefix(low, "claude-") {
		return m
	}
	var words, nums []string
	for _, part := range strings.Split(low[len("claude-"):], "-") {
		switch {
		case part == "":
		case len(part) >= 8 && allDigits(part):
			// a release date such as 20250929
		case allDigits(part):
			nums = append(nums, part)
		default:
			words = append(words, strings.ToUpper(part[:1])+part[1:])
		}
	}
	if len(words) == 0 {
		return m
	}
	out := strings.Join(words, " ")
	if len(nums) > 0 {
		out += " " + strings.Join(nums, ".")
	}
	return out
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// ignoredModels are model names in transcripts that are not the agent at work:
// Claude Code writes "<synthetic>" for messages it makes up itself, Codex runs
// approval reviews under codex-auto-review between the turns of the agent.
func ignoredModel(m string) bool {
	return m == "" || m == "<synthetic>" || strings.HasPrefix(m, "codex-auto-review")
}

// Observation is one sighting of a model in a transcript: the model and the
// time of the line, RFC3339 in UTC to the second.
type Observation struct {
	Model string
	At    string
}

type modelLine struct {
	Type        string `json:"type"`
	Timestamp   string `json:"timestamp"`
	IsSidechain bool   `json:"isSidechain"`
	Message     *struct {
		Model string `json:"model"`
	} `json:"message"`
	Payload *struct {
		Model string `json:"model"`
	} `json:"payload"`
}

// ObserveModel reads the model from one raw transcript line, if the line
// names it: Claude Code on every assistant line (message.model), Codex on each
// turn_context (payload.model). Lines of a side chain (a subagent inside the
// same file) are not the session's agent and are skipped. ok is false for any
// other line, a line without a time or a model that does not count.
func ObserveModel(raw string) (Observation, bool) {
	// Billig vor dem Parsen: jeder neue Chunk läuft hier durch, im Writer.
	if !strings.Contains(raw, `"model"`) {
		return Observation{}, false
	}
	assistant := strings.Contains(raw, `"type":"assistant"`)
	if !assistant && !strings.Contains(raw, `"type":"turn_context"`) {
		return Observation{}, false
	}
	if assistant && strings.Contains(raw, `"isSidechain":true`) {
		return Observation{}, false
	}
	var l modelLine
	if err := json.Unmarshal([]byte(raw), &l); err != nil {
		return Observation{}, false
	}
	var model string
	switch l.Type {
	case "assistant":
		if l.Message == nil || l.IsSidechain {
			return Observation{}, false
		}
		model = l.Message.Model
	case "turn_context":
		if l.Payload == nil {
			return Observation{}, false
		}
		model = l.Payload.Model
	default:
		return Observation{}, false
	}
	model = strings.TrimSpace(model)
	if ignoredModel(model) || !ValidModel(model) {
		return Observation{}, false
	}
	at, ok := NormalizeTime(l.Timestamp)
	if !ok {
		return Observation{}, false
	}
	return Observation{Model: model, At: at}, true
}

// ValidModel says whether a model name from a transcript is stored: at most 120
// characters of [A-Za-z0-9._:/@+-]. Anything else is not a model name and
// would end up in pages, search and statistics.
func ValidModel(m string) bool {
	if m == "" || len(m) > 120 {
		return false
	}
	for _, r := range m {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._:/@+-", r)) {
			return false
		}
	}
	return true
}

// NormalizeTime turns a transcript time into RFC3339 in UTC to the second, the
// form every stored time has, so that times compare as strings.
func NormalizeTime(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return "", false
	}
	return t.UTC().Format(time.RFC3339), true
}

// Segment is a stretch of a session in which one model answered: from the
// first to the last line seen with it.
type Segment struct {
	Model   string `json:"model"`
	FirstAt string `json:"first_at"`
	LastAt  string `json:"last_at"`
}

// Fold adds observations in transcript order to the segments of a session:
// the same model as the last segment extends it, another one opens a new
// segment. A switch back to an earlier model is a new segment, so the history
// says when each change happened.
func Fold(segs []Segment, obs []Observation) []Segment {
	for _, o := range obs {
		if n := len(segs); n > 0 && segs[n-1].Model == o.Model {
			if o.At > segs[n-1].LastAt {
				segs[n-1].LastAt = o.At
			}
			continue
		}
		segs = append(segs, Segment{Model: o.Model, FirstAt: o.At, LastAt: o.At})
	}
	return segs
}

// ModelAt is the model that was active at the time at: the segment that began
// last at or before it. Before the first answer of the session it is the first
// model, because the agent that writes something has already been answered by
// it in all but the most unusual cases. Empty without segments.
func ModelAt(segs []Segment, at string) string {
	if len(segs) == 0 {
		return ""
	}
	model := segs[0].Model
	for _, s := range segs {
		if s.FirstAt <= at {
			model = s.Model
		}
	}
	return model
}
