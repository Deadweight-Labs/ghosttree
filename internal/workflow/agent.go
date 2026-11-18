package workflow

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The loop of an own prompt (stage 3, REQ-698, DEC-42): ghosttree's own loop
// in Go, not Claude Code or Codex. It talks with the model through the proxy
// with the run's token, in the format of the connection (Anthropic or OpenAI
// tool use): message, tool calls, results, again, until the model calls
// "submit" with its results or the loop runs out of rounds, time or money. The
// model can call only the tools the definition declared; any other call is
// refused and stands on the run page. What it submits goes through the gate
// like the results of a block.

// SubmitTool is the tool that ends the loop with the results.
const SubmitTool = "submit"

// Bounds of the loop beyond its rounds.
const (
	// agentTimeout bounds one loop; the engine's run timeout bounds it too.
	agentTimeout = 8 * time.Minute
	// maxCallsPerRound bounds the tool calls of one turn.
	maxCallsPerRound = 8
	// maxAgentTokens bounds one answer of the model.
	maxAgentTokens = 4000
)

// States and reasons of a tool call in the run's full text.
const (
	ToolOK      = "ok"
	ToolRefused = "refused"
	ToolFailed  = "failed"

	ToolReasonNotDeclared = "not_declared"
	ToolReasonPerRound    = "per_round"
	// ToolReasonAfterSubmit: the call came in the turn of submit, which ends
	// the work; the model would never see its result.
	ToolReasonAfterSubmit = "after_submit"
)

//go:embed prompts/agent.txt
var promptAgent string

// AgentName stands for the loop where a block's name would stand.
const AgentName = "ghosttree/agent"

// agentResult is what the model hands to submit.
type agentResult struct {
	Kind      string    `json:"kind"`
	Type      string    `json:"type"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Evidence  []string  `json:"evidence"`
	RequestID refNumber `json:"request_id"`
	OtherID   refNumber `json:"other_id"`
	Relation  string    `json:"relation"`
	IdeaID    refNumber `json:"idea_id"`
	FeatureID refNumber `json:"feature_id"`
	Session   string    `json:"session"`
	SameAs    refNumber `json:"same_as"`
}

type agentSubmit struct {
	Results []agentResult `json:"results"`
	Summary string        `json:"summary"`
}

// submitSchema is the input of submit: the results, with the kinds the
// definition declared.
func submitSchema(d *Def) json.RawMessage {
	kinds := make([]string, 0, len(d.Results))
	for _, r := range d.Results {
		kinds = append(kinds, r.Kind)
	}
	num := map[string]string{"type": "integer"}
	str := map[string]string{"type": "string"}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary": map[string]any{"type": "string", "description": "one or two sentences on what you found"},
			"results": map[string]any{"type": "array", "items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"kind": map[string]any{"type": "string", "enum": kinds}, "type": str, "title": str, "body": str,
					"evidence":   map[string]any{"type": "array", "items": str},
					"request_id": num, "other_id": num, "relation": str, "idea_id": num, "feature_id": num, "session": str, "same_as": num,
				},
				"required": []string{"kind", "evidence"},
			}},
		},
		"required": []string{"results"},
	}
	raw, _ := json.Marshal(schema)
	return raw
}

// agentSystem is our part of the system prompt, then the team's task.
func agentSystem(d *Def) string {
	var kinds []string
	for _, r := range d.Results {
		kinds = append(kinds, fmt.Sprintf("- %s: at most %d", r.Kind, r.Max))
	}
	if len(kinds) == 0 {
		kinds = []string{"- none"}
	}
	s := strings.ReplaceAll(strings.TrimSpace(promptAgent), "{{KINDS}}", strings.Join(kinds, "\n"))
	s = strings.ReplaceAll(s, "{{ROUNDS}}", strconv.Itoa(d.RoundLimit()))
	return s + "\n\nTask of the team:\n" + d.TaskPrompt()
}

// Agent is the block of an own prompt. It runs only as the block of a stored
// stage 3 workflow, which brings the definition and the tools.
var Agent = BlockFunc(func(ctx context.Context, in Input) (store.WorkflowRunOutcome, error) {
	if in.Def == nil || in.Tools == nil {
		return store.WorkflowRunOutcome{}, errNoDefinition
	}
	d := in.Def
	ctx, cancel := context.WithTimeout(ctx, agentTimeout)
	defer cancel()
	system := agentSystem(d)
	user := "<inputs>\n" + renderInputs(d, in.Values) + "</inputs>\n\nWork on the task, then call submit."
	if len(d.Inputs) == 0 {
		user = "There are no inputs beyond what the tools return.\n\nWork on the task, then call submit."
	}
	l := &agentLog{prompt: "SYSTEM\n" + system + "\n\nUSER\n" + user}
	out := func(state, result string) store.WorkflowRunOutcome {
		return store.WorkflowRunOutcome{State: state, Result: result, Inputs: l.prompt, Output: l.output.String(), Tools: l.toolsJSON()}
	}
	client, _, err := in.AI.Client(ctx, d.Model)
	if err != nil {
		return out("", ""), err
	}
	tc, ok := client.(llm.ToolClient)
	if !ok {
		return out("", ""), errors.New("the connection cannot call tools")
	}
	tools := append(in.Tools.wire(), llm.Tool{Name: SubmitTool, Description: "hand in your results; ends the work", Schema: submitSchema(d)})
	turns := []llm.Turn{{Role: "user", Text: user}}
	limit := d.RoundLimit()
	var final *agentSubmit
	for round := 1; round <= limit && final == nil; round++ {
		force := ""
		if round == limit {
			force = SubmitTool
		}
		reply, err := tc.ChatTools(ctx, system, turns, tools, force, maxAgentTokens)
		if err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				err = fmt.Errorf("the loop took longer than %s", agentTimeout)
			}
			return out("", l.summary()), err
		}
		l.rounds++
		if t := strings.TrimSpace(reply.Text); t != "" {
			fmt.Fprintf(&l.output, "[%d] %s\n\n", round, t)
		}
		said := reply.Text
		if strings.TrimSpace(said) == "" && len(reply.Calls) == 0 {
			// A turn with neither text nor calls (max_tokens, an empty answer)
			// is refused when sent back; a placeholder keeps the conversation
			// valid, and the next turn asks for submit.
			said = "(no answer)"
		}
		turns = append(turns, llm.Turn{Role: "assistant", Text: said, Calls: reply.Calls})
		if len(reply.Calls) == 0 {
			// An answer in text instead of submit counts when it is the object.
			var s agentSubmit
			if decodeObject(reply.Text, &s) == nil && s.Results != nil {
				final = &s
				break
			}
			turns = append(turns, llm.Turn{Role: "user", Text: "Call submit with your results (an empty list is fine)."})
			continue
		}
		// submit ends the work: in its turn no other tool runs, before or
		// after it, since the model handed in without their results; what
		// they would return must not become evidence for the gate.
		submit := slices.IndexFunc(reply.Calls, func(c llm.ToolCall) bool { return c.Name == SubmitTool })
		var results []llm.ToolResult
		for i, call := range reply.Calls {
			if submit >= 0 && i != submit {
				results = append(results, l.skip(round, call))
				continue
			}
			results = append(results, l.call(ctx, in.Tools, round, i, call, &final))
		}
		if final != nil {
			break
		}
		turns = append(turns, llm.Turn{Role: "user", Results: results})
	}
	if final == nil {
		return out("", l.summary()), errors.New("the model handed in no results within its rounds")
	}
	for _, r := range final.Results {
		in.Propose(Result{Kind: strings.TrimSpace(r.Kind), Type: strings.TrimSpace(r.Type), Title: strings.TrimSpace(r.Title), Body: strings.TrimSpace(r.Body),
			Evidence: r.Evidence, RequestID: int64(r.RequestID), OtherID: int64(r.OtherID), Relation: strings.TrimSpace(r.Relation),
			IdeaID: int64(r.IdeaID), FeatureID: int64(r.FeatureID), Session: strings.TrimSpace(r.Session), SameAs: int64(r.SameAs)})
	}
	if s := strings.TrimSpace(final.Summary); s != "" {
		fmt.Fprintf(&l.output, "summary: %s\n", s)
	}
	raw, _ := json.MarshalIndent(final.Results, "", "  ")
	fmt.Fprintf(&l.output, "submit: %s\n", raw)
	return out(store.RunDone, l.summary()), nil
})

// agentLog keeps what the loop did for the run's full text, and the counts for
// its lasting result (never model text there, KN-4795).
type agentLog struct {
	prompt  string
	output  strings.Builder
	calls   []store.WorkflowToolCall
	rounds  int
	refused int
}

func (l *agentLog) summary() string {
	n := 0
	for _, c := range l.calls {
		if c.Tool != SubmitTool {
			n++
		}
	}
	s := fmt.Sprintf("%d rounds, %d tool calls", l.rounds, n)
	if l.refused > 0 {
		s += fmt.Sprintf(" (%d refused)", l.refused)
	}
	return s
}

func (l *agentLog) toolsJSON() string {
	if len(l.calls) == 0 {
		return ""
	}
	raw, _ := json.Marshal(l.calls)
	return string(raw)
}

// skip records a call of the turn of submit, which does not run.
func (l *agentLog) skip(round int, call llm.ToolCall) llm.ToolResult {
	const why = "not run: submit ends the work"
	l.refused++
	l.calls = append(l.calls, store.WorkflowToolCall{Round: round, Tool: cutRunes(call.Name, 80), Input: cutRunes(string(call.Input), maxRecordChars),
		State: ToolRefused, Reason: ToolReasonAfterSubmit, Result: why})
	return llm.ToolResult{CallID: call.ID, Content: why, IsError: true}
}

// call answers one tool call of the model and records it.
func (l *agentLog) call(ctx context.Context, tb *Toolbox, round, i int, call llm.ToolCall, final **agentSubmit) llm.ToolResult {
	rec := store.WorkflowToolCall{Round: round, Tool: cutRunes(call.Name, 80), Input: cutRunes(string(call.Input), maxRecordChars)}
	answer := func(state, reason, content string, isErr bool) llm.ToolResult {
		rec.State, rec.Reason, rec.Result = state, reason, cutRunes(content, maxRecordChars)
		if state == ToolRefused {
			l.refused++
		}
		l.calls = append(l.calls, rec)
		return llm.ToolResult{CallID: call.ID, Content: content, IsError: isErr}
	}
	if call.Name == SubmitTool {
		rec.Tool = SubmitTool
		var s agentSubmit
		if err := json.Unmarshal(call.Input, &s); err != nil {
			return answer(ToolFailed, "", "the results are no JSON object of the form of submit: "+err.Error(), true)
		}
		if s.Results == nil {
			s.Results = []agentResult{}
		}
		*final = &s
		return answer(ToolOK, "", fmt.Sprintf("received %d results", len(s.Results)), false)
	}
	t, ok := tb.lookup(call.Name)
	if !ok {
		return answer(ToolRefused, ToolReasonNotDeclared, fmt.Sprintf("the tool %s is not available in this run; available: %s, submit", call.Name, strings.Join(tb.Names(), ", ")), true)
	}
	rec.Tool = t.Name
	if i >= maxCallsPerRound {
		return answer(ToolRefused, ToolReasonPerRound, fmt.Sprintf("at most %d tool calls per turn", maxCallsPerRound), true)
	}
	content, err := tb.call(ctx, t, call.Input)
	if err != nil {
		return answer(ToolFailed, "", err.Error(), true)
	}
	return answer(ToolOK, "", content, false)
}

func cutRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max]) + " …"
}
