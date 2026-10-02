// Package agentpause is the local side of a human pause (REQ-361, AC-1229).
//
// ctx channel mirrors the server's control state into a flag file; the
// PreToolUse hook `ctx hook pause-gate` only reads that file, so a tool call
// pays for one failed or successful file read and never for an HTTP round trip
// or the hookbudget lock. The hook appends one ack line per blocked call; the
// channel reports those lines to the server.
//
// What this package does NOT prove: a flag on disk says nothing about what the
// harness did. "Paused" is only claimed by the server once an ack and the
// transcript entry hook_stopped_continuation for the same tool call exist.
package agentpause

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/privatefile"
)

const (
	flagName = "flag.json"
	ackName  = "acks.jsonl"
	// maxStdin bounds what the gate reads; it only needs a few identifiers.
	maxStdin = 1 << 20
)

// Flag is the local copy of an active control.
type Flag struct {
	ControlID int64  `json:"control_id"`
	Action    string `json:"action,omitempty"`
	By        string `json:"by,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// Ack is what the hook records when it blocked a tool call.
type Ack struct {
	ControlID int64  `json:"control_id"`
	ToolUseID string `json:"tool_use_id"`
	ToolName  string `json:"tool_name,omitempty"`
	AgentID   string `json:"agent_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	At        string `json:"at"`
	// End is the byte offset after this line in the ack file (set by ReadAcks),
	// so a caller can resume right behind the last line it handled.
	End int64 `json:"-"`
}

// Dir is the per-agent state directory. The agent id is hashed so the path
// carries no identity and needs no escaping.
func Dir(agent string) (string, error) {
	root := os.Getenv("XDG_STATE_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(home, ".local", "state")
	}
	if !filepath.IsAbs(root) {
		return "", errors.New("pause state directory must be absolute (XDG_STATE_HOME or HOME)")
	}
	sum := sha256.Sum256([]byte(agent))
	return filepath.Join(root, "ghosttree", "pause", hex.EncodeToString(sum[:12])), nil
}

// WriteFlag sets the flag atomically.
func WriteFlag(agent string, f Flag) error {
	dir, err := Dir(agent)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return privatefile.Write(filepath.Join(dir, flagName), raw)
}

// RemoveFlag lifts the flag; a missing flag is fine. RemoveAll also clears a
// non-regular object someone put at the flag path (directory, FIFO): it never
// follows a symlink, it removes the link itself. The channel calls this when the
// server reports no active control, so a same-user self-pause without a server
// control does not stay for ever.
func RemoveFlag(agent string) {
	if dir, err := Dir(agent); err == nil {
		_ = os.RemoveAll(filepath.Join(dir, flagName))
	}
}

// maxFlagSize bounds what the gate reads. A real flag is under 1 KiB.
const maxFlagSize = 64 << 10

// ReadFlag reports whether the agent is flagged.
//
//   - Nothing at the flag path: not flagged. This is the normal case and the
//     cost of every tool call is this one Lstat.
//   - A regular file of at most 64 KiB: read and parsed. If it cannot be
//     parsed it still counts as flagged: the gate must not turn a damaged file
//     into a silent un-pause.
//   - Anything else at that path (FIFO, device, symlink, directory, a huge
//     file): flagged, WITHOUT reading it. ghosttree only ever writes a small
//     regular file there, so such an object is evidence that someone put
//     something at the pause path on purpose, and reading it could block or
//     follow a link out of the state directory.
//   - The state directory is unreadable (Lstat fails for a reason other than
//     "does not exist"): not flagged, fail-open. The gate cannot tell, and the
//     server never shows "paused" without a hook ack plus a transcript proof,
//     so a pause that did not take hold is never displayed as one.
func ReadFlag(agent string) (Flag, bool) {
	dir, err := Dir(agent)
	if err != nil {
		return Flag{}, false
	}
	path := filepath.Join(dir, flagName)
	info, err := os.Lstat(path)
	if err != nil {
		return Flag{}, false
	}
	if !info.Mode().IsRegular() || info.Size() > maxFlagSize {
		return Flag{}, true
	}
	f, err := os.OpenFile(path, os.O_RDONLY|openFlags, 0)
	if err != nil {
		return Flag{}, true // present, but cannot be opened
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxFlagSize))
	if err != nil {
		return Flag{}, true
	}
	var fl Flag
	_ = json.Unmarshal(raw, &fl)
	return fl, true
}

// hookInput is the part of the PreToolUse payload the gate records. agent_id
// is only present for calls made inside a subagent (measured, Claude Code
// 2.1.287).
type hookInput struct {
	SessionID string `json:"session_id"`
	ToolName  string `json:"tool_name"`
	ToolUseID string `json:"tool_use_id"`
	AgentID   string `json:"agent_id"`
}

type gateOutput struct {
	Continue           bool   `json:"continue"`
	StopReason         string `json:"stopReason"`
	HookSpecificOutput struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
	} `json:"hookSpecificOutput"`
}

// StopReason is the text the transcript keeps in hook_stopped_continuation. The
// collector recognises the control id in it.
func StopReason(controlID int64) string {
	return fmt.Sprintf("Paused by ghosttree (control #%d)", controlID)
}

// Gate answers one PreToolUse call. It returns true and writes the measured
// pause output (continue:false plus deny in one JSON: the triggering call does
// not run and the loop stops) when the agent's flag is set; otherwise it writes
// nothing and returns false, so everything else stays fail-open.
func Gate(agent string, stdin io.Reader, stdout io.Writer) bool {
	if agent == "" {
		return false
	}
	flag, set := ReadFlag(agent)
	if !set {
		return false
	}
	var in hookInput
	if raw, err := io.ReadAll(io.LimitReader(stdin, maxStdin)); err == nil {
		_ = json.Unmarshal(raw, &in)
	}
	appendAck(agent, Ack{
		ControlID: flag.ControlID, ToolUseID: in.ToolUseID, ToolName: in.ToolName,
		AgentID: in.AgentID, SessionID: in.SessionID, At: time.Now().UTC().Format(time.RFC3339Nano),
	})
	var out gateOutput
	out.Continue = false
	out.StopReason = StopReason(flag.ControlID)
	out.HookSpecificOutput.HookEventName = "PreToolUse"
	out.HookSpecificOutput.PermissionDecision = "deny"
	out.HookSpecificOutput.PermissionDecisionReason = denyReason(flag)
	_ = json.NewEncoder(stdout).Encode(out)
	return true
}

func denyReason(f Flag) string {
	var b strings.Builder
	b.WriteString(StopReason(f.ControlID))
	if f.By != "" {
		b.WriteString(" by " + oneLine(f.By, 60))
	}
	b.WriteString(". Do not retry this call or work around the pause; stop and wait until a person resumes you in ghosttree and sends a new prompt.")
	if f.Reason != "" {
		b.WriteString(" Reason given: " + oneLine(f.Reason, 200))
	}
	return b.String()
}

func oneLine(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if r := []rune(s); len(r) > max {
		s = string(r[:max])
	}
	return strings.TrimSpace(s)
}

func appendAck(agent string, a Ack) {
	dir, err := Dir(agent)
	if err != nil {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, ackName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	// One write call per line keeps concurrent hook processes from interleaving.
	_, _ = f.Write(append(raw, '\n'))
	_ = f.Close()
}

// ReadAcks returns the ack lines from byte offset on and the offset to resume
// from. An incomplete last line is left for the next read.
func ReadAcks(agent string, offset int64) ([]Ack, int64) {
	dir, err := Dir(agent)
	if err != nil {
		return nil, offset
	}
	f, err := os.Open(filepath.Join(dir, ackName))
	if err != nil {
		return nil, offset
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() < offset {
		offset = 0 // truncated or replaced
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset
	}
	var out []Ack
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			break
		}
		offset += int64(len(line))
		var a Ack
		if json.Unmarshal(line, &a) == nil && a.ToolUseID != "" {
			a.End = offset
			out = append(out, a)
		}
	}
	return out, offset
}

// ClearAcks drops the ack file once a control is over and its acks are reported.
func ClearAcks(agent string) {
	if dir, err := Dir(agent); err == nil {
		_ = os.Remove(filepath.Join(dir, ackName))
	}
}
