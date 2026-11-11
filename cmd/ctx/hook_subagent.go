package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/deliverylog"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The SubagentStart hook (REQ-436): Claude Code starts a subagent with only
// the prompt its parent wrote. The hook adds a short briefing in front of it:
// the working rules, the branch's work package with its open criteria, the
// pitfalls the task text gives a reason to mention, and where the mirror is.
const (
	subagentBriefDeadline   = 1500 * time.Millisecond
	subagentBriefMaxRunes   = 6000
	subagentTranscriptTail  = 1 << 20
	subagentCriteriaPerItem = 3
	subagentCriterionRunes  = 140
	subagentRelevantLimit   = 6
)

// subagentRules are the three lines on which tool to use when, plus the rule
// on comments. They name the core tools as the subagent sees them.
const subagentRules = `### How to work with ghosttree here
- Before you write or re-derive something: context_search (knowledge, past sessions, file descriptions); the mirror below is readable with grep and cat at no tool cost.
- What you learn: a pitfall or a decision with its why -> context_remember; what a file does that the code does not show -> context_remember action=describe_file, instead of an explanatory comment (the gardener describes new files after the merge).
- Progress and open ends: a met criterion -> request action=criterion_met with evidence; a problem you cannot solve -> pain action=record; what only the human can give -> human_task action=create.
`

func subagentStart(stdin io.Reader, harness string, stdout io.Writer) int {
	type output struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext,omitempty"`
		} `json:"hookSpecificOutput"`
	}
	var out output
	out.HookSpecificOutput.HookEventName = "SubagentStart"
	if os.Getenv("GHOSTTREE_HOOK_SYNTHETIC") != "1" && harness == "claude" {
		if raw, err := io.ReadAll(io.LimitReader(stdin, 4<<20)); err == nil {
			trace, endTrace := beginTrace()
			out.HookSpecificOutput.AdditionalContext = subagentBriefing(raw)
			endTrace()
			// The briefing goes to the subagent without passing the session budget,
			// so it is logged under the session it was started from, flagged as a
			// subagent's (REQ-636). Its size is what the briefing has.
			logDelivery(trace, hookSessionID(raw), harness, deliverylog.ViaSubagent, true, out.HookSpecificOutput.AdditionalContext)
		}
	}
	_ = json.NewEncoder(stdout).Encode(out)
	return 0
}

type subagentStartInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	AgentID        string `json:"agent_id"`
	AgentType      string `json:"agent_type"`
}

func subagentBriefing(raw []byte) string {
	var in subagentStartInput
	if json.Unmarshal(raw, &in) != nil {
		return ""
	}
	cwd := in.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	git := collector.ResolveGitContext(cwd)
	task := subagentTask(in)

	traceProject(git.Project)
	var pkg, relevant string
	var relevantParts []deliverylog.Part
	if cfg, err := config.Load(); err == nil && git.Project != "" {
		c := client.NewWithTimeout(cfg, subagentBriefDeadline)
		ax := scope.Axes{Project: git.Project, Branch: git.Branch, Lineage: git.Lineage, Machine: cfg.Machine}
		pkgDone := make(chan string, 1)
		go func() {
			detail, err := c.FindPackage(ax, 0, true)
			if err != nil {
				pkgDone <- ""
				return
			}
			pkgDone <- renderSubagentPackage(detail)
		}()
		if strings.TrimSpace(task) != "" {
			// No session: the subagent has seen none of what its parent was given.
			if d, err := c.RelevantDelivered(task, ax, subagentRelevantLimit, ""); err == nil {
				relevant, relevantParts = d.Text, d.Parts
			}
		}
		pkg = <-pkgDone
	}

	var b strings.Builder
	b.WriteString("## ghosttree briefing for this subagent\n\n")
	b.WriteString(subagentRules)
	if pkg != "" {
		b.WriteString("\n" + pkg)
		traceParts(deliverylog.Part{Via: deliverylog.ViaSubagent, Kind: deliverylog.KindLedger, Chars: deliverylog.Runes(pkg)})
	}
	if strings.TrimSpace(relevant) != "" {
		for _, p := range relevantParts {
			p.Via = deliverylog.ViaSubagent
			traceParts(p)
		}
		b.WriteString("\n### Known pitfalls and decisions for this task\n")
		b.WriteString(strings.TrimSpace(stripFirstHeading(relevant)) + "\n")
	}
	if git.Root != "" {
		index := filepath.Join(git.Root, ".ghosttree", "INDEX.md")
		if info, err := os.Stat(index); err == nil && info.Mode().IsRegular() {
			fmt.Fprintf(&b, "\n### Mirror\nWhat ghosttree knows about this repository is on disk: %s (start there; grep -ril <topic> %s).\n",
				index, filepath.Join(git.Root, ".ghosttree", "knowledge"))
		}
	}
	return cutRunesPlain(b.String(), subagentBriefMaxRunes)
}

// stripFirstHeading drops the heading the relevance answer starts with; the
// briefing has its own.
func stripFirstHeading(md string) string {
	md = strings.TrimLeft(md, "\n")
	if strings.HasPrefix(md, "#") {
		if i := strings.Index(md, "\n"); i >= 0 {
			return md[i+1:]
		}
	}
	return md
}

func cutRunesPlain(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "\n[briefing cut; context_search finds more]\n"
}

func renderSubagentPackage(d requestdomain.PackageDetail) string {
	if d.ID == 0 {
		return ""
	}
	var b strings.Builder
	name := d.Name
	if d.NameHidden {
		name = ""
	}
	fmt.Fprintf(&b, "### Work package PKG-%d %s\n", d.ID, strings.TrimSpace(name))
	if d.PR > 0 {
		fmt.Fprintf(&b, "Branch %s, PR #%d.\n", d.Branch, d.PR)
	} else if d.Branch != "" {
		fmt.Fprintf(&b, "Branch %s.\n", d.Branch)
	}
	open := 0
	for _, m := range d.Members {
		if m.Role != "" && m.Role != "pflicht" {
			continue
		}
		fmt.Fprintf(&b, "- REQ-%d %s: %d open, %d met\n", m.RequestID, briefLine(m.Title, 100), m.Open, m.Met)
		shown := 0
		for _, c := range m.Criteria {
			if c.State != "open" {
				continue
			}
			open++
			if shown < subagentCriteriaPerItem {
				fmt.Fprintf(&b, "  - criterion %d (id %d): %s\n", c.Number, c.ID, briefLine(c.Description, subagentCriterionRunes))
				shown++
			}
		}
		if m.Open > shown && shown > 0 {
			fmt.Fprintf(&b, "  - and %d more (package action=get)\n", m.Open-shown)
		}
	}
	if open == 0 && len(d.Members) == 0 {
		return ""
	}
	return b.String()
}

func briefLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// subagentSpawnWindow is how old a recorded Agent call may be to count as the
// one a starting subagent belongs to.
const subagentSpawnWindow = 2 * time.Minute

// noteAgentSpawn records the prompt of an Agent (or older Task) call from the
// PreToolUse payload, before the subagent starts. Claude Code passes the
// prompt to no subagent hook, and the parent's transcript may not hold the
// call yet when SubagentStart runs.
func noteAgentSpawn(raw []byte) {
	var in struct {
		SessionID string `json:"session_id"`
		ToolName  string `json:"tool_name"`
		ToolUseID string `json:"tool_use_id"`
		ToolInput struct {
			Prompt       string `json:"prompt"`
			Description  string `json:"description"`
			SubagentType string `json:"subagent_type"`
		} `json:"tool_input"`
	}
	if json.Unmarshal(raw, &in) != nil || (in.ToolName != "Agent" && in.ToolName != "Task") ||
		!store.ValidExternalID(strings.TrimSpace(in.SessionID)) || strings.TrimSpace(in.ToolInput.Prompt) == "" {
		return
	}
	dir, err := sessionLogDir(in.SessionID)
	if err != nil || os.MkdirAll(dir, 0o700) != nil {
		return
	}
	kind := in.ToolInput.SubagentType
	if kind == "" {
		kind = "general-purpose"
	}
	line, err := json.Marshal(subagentSpawn{ID: in.ToolUseID, Kind: kind, At: time.Now().Unix(),
		Text: cutRunesPlain(strings.TrimSpace(in.ToolInput.Description+"\n"+in.ToolInput.Prompt), 8000)})
	if err == nil {
		appendLine(filepath.Join(dir, "spawns"), string(line), 1<<20)
	}
}

type subagentSpawn struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	At   int64  `json:"at"`
	Text string `json:"text"`
}

// subagentTask finds the prompt the parent wrote for this subagent: an Agent
// (or older Task) call whose subagent_type matches and that no earlier
// SubagentStart of this session took, first from the calls the PreToolUse
// gate recorded, else from the parent's transcript.
func subagentTask(in subagentStartInput) string {
	if !store.ValidExternalID(strings.TrimSpace(in.SessionID)) {
		return ""
	}
	dir, err := sessionLogDir(in.SessionID)
	if err != nil {
		return ""
	}
	taken := map[string]bool{}
	if raw, err := os.ReadFile(filepath.Join(dir, "briefed")); err == nil {
		for _, id := range strings.Fields(string(raw)) {
			taken[id] = true
		}
	}
	take := func(id, text string) string {
		if os.MkdirAll(dir, 0o700) == nil {
			appendLine(filepath.Join(dir, "briefed"), oneLineField(id), 0)
		}
		return text
	}
	fitsKind := func(kind string) bool { return in.AgentType == "" || kind == in.AgentType }
	if raw, err := os.ReadFile(filepath.Join(dir, "spawns")); err == nil {
		// Parallel spawns are recorded in order and start in order: the oldest
		// recent untaken one is this subagent's.
		for _, line := range strings.Split(string(raw), "\n") {
			var sp subagentSpawn
			if json.Unmarshal([]byte(line), &sp) != nil || taken[sp.ID] || !fitsKind(sp.Kind) {
				continue
			}
			if time.Since(time.Unix(sp.At, 0)) > subagentSpawnWindow {
				continue
			}
			return take(sp.ID, sp.Text)
		}
	}
	if in.TranscriptPath == "" {
		return ""
	}
	f, err := os.Open(in.TranscriptPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return ""
	} else if info.Size() > subagentTranscriptTail {
		_, _ = f.Seek(info.Size()-subagentTranscriptTail, io.SeekStart)
	}
	tail, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	type call struct {
		id, kind, text string
		msg            int
	}
	var calls []call
	sc := bufio.NewScanner(bytes.NewReader(tail))
	sc.Buffer(make([]byte, 0, 64<<10), subagentTranscriptTail)
	msg := 0
	for sc.Scan() {
		msg++
		line := sc.Bytes()
		if !bytes.Contains(line, []byte(`"tool_use"`)) {
			continue
		}
		var l struct {
			Type    string `json:"type"`
			Message struct {
				Content []struct {
					Type  string `json:"type"`
					ID    string `json:"id"`
					Name  string `json:"name"`
					Input struct {
						Prompt       string `json:"prompt"`
						Description  string `json:"description"`
						SubagentType string `json:"subagent_type"`
					} `json:"input"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &l) != nil || l.Type != "assistant" {
			continue
		}
		for _, c := range l.Message.Content {
			if c.Type != "tool_use" || (c.Name != "Agent" && c.Name != "Task") {
				continue
			}
			kind := c.Input.SubagentType
			if kind == "" {
				kind = "general-purpose"
			}
			calls = append(calls, call{id: c.ID, kind: kind, msg: msg, text: strings.TrimSpace(c.Input.Description + "\n" + c.Input.Prompt)})
		}
	}
	fits := func(c call) bool { return !taken[c.id] && fitsKind(c.kind) }
	// The newest message with a fitting call; parallel spawns of one message
	// start in order, so the first fitting call of it is this subagent's.
	for i := len(calls) - 1; i >= 0; i-- {
		if !fits(calls[i]) {
			continue
		}
		for _, c := range calls {
			if c.msg == calls[i].msg && fits(c) {
				return take(c.id, c.text)
			}
		}
	}
	return ""
}
