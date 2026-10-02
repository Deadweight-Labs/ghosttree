package collector

import (
	"encoding/json"
	"regexp"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// ControlProof is the harness's own record that a pause took effect: Claude
// Code writes an attachment hook_stopped_continuation into the transcript when
// a hook answers continue:false (measured 2026-10-02, Claude Code 2.1.287; the
// message is the hook's stopReason, toolUseID the call it stopped).
//
// This is the proof AC-1229 asks for. The hook's own ack only shows that the
// hook was called; the transcript entry shows that the loop stopped.
type ControlProof struct {
	ControlID int64
	ToolUseID string
	HookName  string
}

// ControlProofRecorder is optional, like ActivityRecorder: a collector without
// it still archives transcripts. Unlike the activity recorder, a failure here
// fails the sync, because losing a proof would leave a pause that did take
// effect shown as unproven for good. The offset stays put and the lines are
// replayed; the server keeps each proof once.
type ControlProofRecorder interface {
	RecordControlProof(controlID int64, ev store.ControlEvent) error
}

var stopReasonPattern = regexp.MustCompile(`^Paused by ghosttree \(control #(\d+)\)`)

// ControlProofFrom recognises a ghosttree pause in one Claude transcript line.
// Only the attachment type and ghosttree's own stopReason text count; user or
// assistant text that merely quotes them does not.
func ControlProofFrom(line []byte) (ControlProof, bool) {
	var l struct {
		Type       string `json:"type"`
		Attachment struct {
			Type      string `json:"type"`
			Message   string `json:"message"`
			HookName  string `json:"hookName"`
			ToolUseID string `json:"toolUseID"`
		} `json:"attachment"`
	}
	if err := json.Unmarshal(line, &l); err != nil || l.Type != "attachment" || l.Attachment.Type != "hook_stopped_continuation" {
		return ControlProof{}, false
	}
	m := stopReasonPattern.FindStringSubmatch(l.Attachment.Message)
	if m == nil || l.Attachment.ToolUseID == "" {
		return ControlProof{}, false
	}
	id, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || id <= 0 {
		return ControlProof{}, false
	}
	return ControlProof{ControlID: id, ToolUseID: l.Attachment.ToolUseID, HookName: l.Attachment.HookName}, true
}
