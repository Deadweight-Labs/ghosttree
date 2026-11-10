package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/coretools"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// What a session did, as the PostToolUse hook sees it (REQ-436): which files it
// changed, whether it recorded anything in ghosttree, and which ghosttree tools
// it called. Append-only files and empty marker files per session, so that
// hooks running in parallel for parallel tool calls never overwrite each other:
//
//	edits:    "e <path>" a file was changed
//	uses:     "u <tool> <action> <0|1>" one ghosttree call, 1 from a subagent
//	recorded: exists once something was recorded in ghosttree
//	reminded: exists once the reminder at the end of a turn was given
//
// The uses are sent to the server in batches: the sender claims the file by
// renaming it, so two hooks never send the same calls.
const (
	sessionLogMaxFacts   = 64 << 10 // edits beyond this are not listed; the reminder names four
	sessionLogMaxAge     = 7 * 24 * time.Hour
	usesFlushInterval    = 5 * time.Minute
	usesReportDeadline   = 1500 * time.Millisecond
	turnReminderMaxPaths = 4
)

// editingTools change files; their target is under file_path or notebook_path.
var editingTools = map[string]bool{"Edit": true, "Write": true, "MultiEdit": true, "NotebookEdit": true}

func sessionLogDir(sessionID string) (string, error) {
	root := os.Getenv("XDG_STATE_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(home, ".local", "state")
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("state directory must be absolute")
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(sessionID)))
	return filepath.Join(root, "ghosttree", "session-log", hex.EncodeToString(sum[:12])), nil
}

func appendLine(path, line string, limit int64) {
	if limit > 0 {
		if info, err := os.Stat(path); err == nil && info.Size() >= limit {
			return
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line + "\n")
}

// hookToolCall is the part of a PostToolUse payload the session log reads.
type hookToolCall struct {
	SessionID string          `json:"session_id"`
	CWD       string          `json:"cwd"`
	AgentID   string          `json:"agent_id"`
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

// recordsKnowledge says whether a call writes something a later session can
// find: an entry, a file description, a pain, an idea or progress on a request.
func recordsKnowledge(core, action string) bool {
	switch core {
	case "context_remember":
		return true
	case "pain":
		return action == "record" || action == "update"
	case "idea":
		return action == "record"
	case "request":
		return action != "search" && action != "get" && action != "start_work"
	}
	return false
}

// noteSessionCall writes one tool call into the session log. It never fails
// the hook.
func noteSessionCall(raw []byte) {
	var in hookToolCall
	if json.Unmarshal(raw, &in) != nil || !store.ValidExternalID(strings.TrimSpace(in.SessionID)) {
		return
	}
	dir, err := sessionLogDir(in.SessionID)
	if err != nil || os.MkdirAll(dir, 0o700) != nil {
		return
	}
	if editingTools[in.ToolName] {
		var target struct {
			FilePath     string `json:"file_path"`
			NotebookPath string `json:"notebook_path"`
		}
		_ = json.Unmarshal(in.ToolInput, &target)
		p := target.FilePath
		if p == "" {
			p = target.NotebookPath
		}
		if p != "" {
			appendLine(filepath.Join(dir, "edits"), "e "+oneLineField(p), sessionLogMaxFacts)
		}
		return
	}
	var input struct {
		Action string `json:"action"`
	}
	_ = json.Unmarshal(in.ToolInput, &input)
	core, action, ok := coretools.Classify(in.ToolName, strings.TrimSpace(input.Action))
	if !ok {
		return
	}
	sub := "0"
	if in.AgentID != "" {
		sub = "1"
	}
	appendLine(filepath.Join(dir, "uses"), "u "+core+" "+oneLineField(action)+" "+sub, 0)
	if recordsKnowledge(core, action) {
		_ = os.WriteFile(filepath.Join(dir, "recorded"), nil, 0o600)
	}
}

func oneLineField(s string) string {
	return strings.Join(strings.Fields(s), "_")
}

// flushSessionUses sends the counted calls of a session to the server. Without
// force it only sends when the last report is older than usesFlushInterval.
// A failed report puts the calls back for the next attempt.
func flushSessionUses(raw []byte, harness string, force bool) {
	var in hookToolCall
	if json.Unmarshal(raw, &in) != nil || !store.ValidExternalID(strings.TrimSpace(in.SessionID)) {
		return
	}
	dir, err := sessionLogDir(in.SessionID)
	if err != nil {
		return
	}
	uses := filepath.Join(dir, "uses")
	stamp := filepath.Join(dir, "flushed")
	if !force {
		if info, err := os.Stat(stamp); err == nil && time.Since(info.ModTime()) < usesFlushInterval {
			return
		}
	}
	if _, err := os.Stat(uses); err != nil {
		return
	}
	claimed := uses + ".send-" + strconv.Itoa(os.Getpid())
	if os.Rename(uses, claimed) != nil {
		return
	}
	_ = os.WriteFile(stamp, nil, 0o600)
	_ = os.Chtimes(stamp, time.Now(), time.Now())
	content, err := os.ReadFile(claimed)
	_ = os.Remove(claimed)
	if err != nil || len(content) == 0 {
		return
	}
	batch := store.ToolUseBatch{SessionRef: strings.TrimSpace(in.SessionID), Harness: harness, Uses: countUses(content)}
	if len(batch.Uses) == 0 {
		return
	}
	cwd := in.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	cfg, err := config.Load()
	if err != nil {
		return
	}
	batch.Project = collector.ResolveGitContext(cwd).Project
	if batch.Project == "" {
		return
	}
	if client.NewWithTimeout(cfg, usesReportDeadline).RecordToolUses(batch) != nil {
		// Back into the log; the next turn end tries again.
		f, err := os.OpenFile(uses, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = f.Write(content)
			_ = f.Close()
		}
	}
}

func countUses(content []byte) []store.ToolUse {
	counts := map[store.ToolUse]int{}
	sc := bufio.NewScanner(bytes.NewReader(content))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 4 || f[0] != "u" {
			continue
		}
		counts[store.ToolUse{Tool: f[1], Action: f[2], Subagent: f[3] == "1"}]++
	}
	out := make([]store.ToolUse, 0, len(counts))
	for u, n := range counts {
		u.Calls = n
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		return a.Tool+a.Action+strconv.FormatBool(a.Subagent) < b.Tool+b.Action+strconv.FormatBool(b.Subagent)
	})
	return out
}

// turnReminder is the text the Stop hook gives once per session when the
// session changed files but recorded nothing in ghosttree; "" otherwise. It
// marks the reminder as given.
func turnReminder(raw []byte) string {
	var in struct {
		SessionID      string `json:"session_id"`
		Event          string `json:"hook_event_name"`
		StopHookActive bool   `json:"stop_hook_active"`
	}
	// StopFailure runs the same command after an API error; nothing reaches the
	// model then, and the one reminder would be spent.
	if json.Unmarshal(raw, &in) != nil || in.StopHookActive || (in.Event != "" && in.Event != "Stop") ||
		!store.ValidExternalID(strings.TrimSpace(in.SessionID)) {
		return ""
	}
	dir, err := sessionLogDir(in.SessionID)
	if err != nil {
		return ""
	}
	for _, marker := range []string{"recorded", "reminded"} {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return ""
		}
	}
	content, err := os.ReadFile(filepath.Join(dir, "edits"))
	if err != nil {
		return ""
	}
	var edited []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(content), "\n") {
		if p, ok := strings.CutPrefix(line, "e "); ok && !seen[p] {
			seen[p] = true
			edited = append(edited, p)
		}
	}
	if len(edited) == 0 {
		return ""
	}
	if os.WriteFile(filepath.Join(dir, "reminded"), nil, 0o600) != nil {
		// Without the marker the reminder would come at every turn end.
		return ""
	}
	return renderTurnReminder(edited)
}

func renderTurnReminder(edited []string) string {
	shown := edited
	more := ""
	if len(shown) > turnReminderMaxPaths {
		shown = shown[:turnReminderMaxPaths]
		more = fmt.Sprintf(" and %d more", len(edited)-turnReminderMaxPaths)
	}
	names := make([]string, len(shown))
	for i, p := range shown {
		names[i] = filepath.Base(p)
	}
	return "ghosttree: this session changed " + strings.Join(names, ", ") + more +
		" but recorded nothing in ghosttree. Before you finish, keep what the next session would need, if anything: " +
		"a pitfall you ran into or a decision you took (with why) -> context_remember; " +
		"what a file does that the code does not show -> context_remember action=describe_file (the gardener describes new files after the merge); " +
		"a criterion your change meets -> request action=criterion_met with evidence; " +
		"a problem you leave unsolved -> pain action=record. " +
		"If nothing is worth keeping, just finish. This reminder comes once per session."
}

// pruneSessionLogs removes the logs of sessions not seen for a week.
func pruneSessionLogs(dir string) {
	parent := filepath.Dir(dir)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > sessionLogMaxAge {
			_ = os.RemoveAll(filepath.Join(parent, e.Name()))
		}
	}
}
