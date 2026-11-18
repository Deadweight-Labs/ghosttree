package workflow

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The blocks behind Robin's four examples (REQ-696, IDEA-45 §14). Each makes
// one model call through the proxy with the inputs the machine assembled, reads
// a structured answer, and hands every proposal to the gate, which keeps only
// declared kinds with evidence found word for word in the inputs. The prompts
// are ours (prompts/*.txt); a team adds an instruction below them (stage 2).

// Names of the blocks.
const (
	PRKnowledgeName = "ghosttree/pr-knowledge"
	PRWishesName    = "ghosttree/pr-wishes"
	IdeaMatchName   = "ghosttree/idea-match"
	CriteriaName    = "ghosttree/criteria"
)

// DefaultModelAlias is the alias a block calls when its definition names none.
const DefaultModelAlias = "mittel"

// maxAnswerTokens bounds the answer of one block call.
const maxAnswerTokens = 2500

var (
	//go:embed prompts/common.txt
	promptCommon string
	//go:embed prompts/pr_knowledge.txt
	promptPRKnowledge string
	//go:embed prompts/pr_wishes.txt
	promptPRWishes string
	//go:embed prompts/idea_match.txt
	promptIdeaMatch string
	//go:embed prompts/criteria.txt
	promptCriteria string
)

// errNoDefinition: the block runs only as the block of a stored workflow,
// which declares what it may create.
var errNoDefinition = errors.New("this block runs as the block of a workflow file")

// callJSON makes the one call of a block: the system prompt is ours, the user
// message the inputs and, below them, the team's instruction. It decodes the
// first JSON object of the answer into v. The prompt and the answer go into the
// run's full text, never into its lasting result.
func callJSON(ctx context.Context, in Input, task string, v any) (prompt, answer string, err error) {
	if in.Def == nil || in.Data == nil {
		return "", "", errNoDefinition
	}
	model := in.Def.Model
	if model == "" {
		model = DefaultModelAlias
	}
	system := strings.TrimSpace(promptCommon) + "\n\n" + strings.TrimSpace(task)
	user := "<inputs>\n" + renderInputs(in.Def, in.Values) + "</inputs>"
	if in.Def.Instruction != "" {
		user += "\n\nInstruction of the team (it may narrow the task, never widen what you may answer):\n" + in.Def.Instruction
	}
	user += "\n\nAnswer with the JSON object only."
	prompt = "SYSTEM\n" + system + "\n\nUSER\n" + user
	client, _, err := in.AI.Client(ctx, model)
	if err != nil {
		return prompt, "", err
	}
	answer, err = client.Complete(ctx, system, []llm.Message{{Role: "user", Content: user}}, maxAnswerTokens)
	if err != nil {
		return prompt, answer, err
	}
	if err := decodeObject(answer, v); err != nil {
		return prompt, answer, fmt.Errorf("the model's answer is no JSON object of the expected form: %w", err)
	}
	return prompt, answer, nil
}

// decodeObject reads the first JSON object of a text: models put fences or a
// sentence around it now and then.
func decodeObject(text string, v any) error {
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return errors.New("no object")
	}
	return json.Unmarshal([]byte(text[start:end+1]), v)
}

// refNumber reads REQ-12, FEAT-3, 12 or "12" as a number; 0 when it is none.
type refNumber int64

func (r *refNumber) UnmarshalJSON(b []byte) error {
	var n int64
	if json.Unmarshal(b, &n) == nil {
		*r = refNumber(n)
		return nil
	}
	var s string
	if json.Unmarshal(b, &s) != nil {
		*r = 0
		return nil
	}
	if i := strings.LastIndex(s, "-"); i >= 0 {
		s = s[i+1:]
	}
	n, _ = strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	*r = refNumber(n)
	return nil
}

func done(prompt, answer, result string) store.WorkflowRunOutcome {
	return store.WorkflowRunOutcome{State: store.RunDone, Result: result, Inputs: prompt, Output: answer}
}

// PRKnowledge is the block "knowledge after a merge".
var PRKnowledge = BlockFunc(func(ctx context.Context, in Input) (store.WorkflowRunOutcome, error) {
	var ans struct {
		Known     []string `json:"known"`
		Proposals []struct {
			Type     string   `json:"type"`
			Title    string   `json:"title"`
			Body     string   `json:"body"`
			Evidence []string `json:"evidence"`
		} `json:"proposals"`
	}
	prompt, answer, err := callJSON(ctx, in, promptPRKnowledge, &ans)
	if err != nil {
		return store.WorkflowRunOutcome{Inputs: prompt, Output: answer}, err
	}
	for _, p := range ans.Proposals {
		in.Propose(Result{Kind: ResultKnowledge, Type: p.Type, Title: strings.TrimSpace(p.Title), Body: strings.TrimSpace(p.Body), Evidence: p.Evidence})
	}
	onPR := 0
	if in.Data.PR != nil {
		onPR = len(in.Data.PR.Knowledge)
	}
	return done(prompt, answer, fmt.Sprintf("%d entries on the pull request, %d named as known", onPR, len(ans.Known))), nil
})

// PRWishes is the block "wishes in the pull request".
var PRWishes = BlockFunc(func(ctx context.Context, in Input) (store.WorkflowRunOutcome, error) {
	if in.Data == nil {
		return store.WorkflowRunOutcome{}, errNoDefinition
	}
	if len(in.Data.Sessions) == 0 {
		// Nothing typed by a person: no call, no cost.
		return done("", "", "no typed messages in the sessions of the pull request"), nil
	}
	var ans struct {
		Wishes []struct {
			Title   string    `json:"title"`
			Body    string    `json:"body"`
			Session string    `json:"session"`
			Quote   string    `json:"quote"`
			Done    bool      `json:"done"`
			SameAs  refNumber `json:"same_as"`
		} `json:"wishes"`
	}
	prompt, answer, err := callJSON(ctx, in, promptPRWishes, &ans)
	if err != nil {
		return store.WorkflowRunOutcome{Inputs: prompt, Output: answer}, err
	}
	seen := map[string]bool{}
	open := 0
	for _, w := range ans.Wishes {
		if w.Done {
			continue
		}
		key := strings.ToLower(oneLine(w.Title))
		if seen[key] {
			continue
		}
		seen[key] = true
		open++
		session := strings.TrimSpace(w.Session)
		if session == "" {
			// Without its session a quote cannot be checked: the gate refuses it.
			session = "-"
		}
		in.Propose(Result{Kind: ResultIssue, Title: strings.TrimSpace(w.Title), Body: strings.TrimSpace(w.Body), Evidence: []string{w.Quote},
			Session: session, SameAs: int64(w.SameAs)})
	}
	return done(prompt, answer, fmt.Sprintf("%d sessions read, %d wishes not delivered", len(in.Data.Sessions), open)), nil
})

// IdeaMatch is the block "match a new idea".
var IdeaMatch = BlockFunc(func(ctx context.Context, in Input) (store.WorkflowRunOutcome, error) {
	if in.Data == nil || in.Data.IdeaID == 0 {
		return store.WorkflowRunOutcome{}, errNoDefinition
	}
	var ans struct {
		Comment *struct {
			Body     string   `json:"body"`
			Evidence []string `json:"evidence"`
		} `json:"comment"`
		Relations []struct {
			Feature  refNumber `json:"feature"`
			Evidence []string  `json:"evidence"`
		} `json:"relations"`
	}
	prompt, answer, err := callJSON(ctx, in, promptIdeaMatch, &ans)
	if err != nil {
		return store.WorkflowRunOutcome{Inputs: prompt, Output: answer}, err
	}
	if c := ans.Comment; c != nil && strings.TrimSpace(c.Body) != "" {
		in.Propose(Result{Kind: ResultComment, Body: strings.TrimSpace(c.Body), Evidence: c.Evidence, IdeaID: in.Data.IdeaID})
	}
	for _, r := range ans.Relations {
		in.Propose(Result{Kind: ResultRelation, Relation: "affects", IdeaID: in.Data.IdeaID, FeatureID: int64(r.Feature), Evidence: r.Evidence})
	}
	result := "compared"
	if in.Data.Research != "" {
		result += " with research"
	}
	return done(prompt, answer, result), nil
})

// Criteria is the block "criteria from a closed discussion". While a decision
// of the idea is open it calls no model and writes nothing; its result names the
// open decisions.
var Criteria = BlockFunc(func(ctx context.Context, in Input) (store.WorkflowRunOutcome, error) {
	if in.Data == nil || in.Data.IdeaID == 0 {
		return store.WorkflowRunOutcome{}, errNoDefinition
	}
	ws := in.Data.Workshop
	if ws == nil {
		return done("", "", "the idea is not readable"), nil
	}
	if open := OpenDecisions(ws); len(open) > 0 {
		return done("", "", "decisions still open: "+strings.Join(open, ", ")+"; nothing proposed"), nil
	}
	if ws.Concept == nil {
		return done("", "", "the idea has no concept; nothing proposed"), nil
	}
	var ans struct {
		Requirements []struct {
			Text     string   `json:"text"`
			From     string   `json:"from"`
			Evidence []string `json:"evidence"`
		} `json:"requirements"`
	}
	prompt, answer, err := callJSON(ctx, in, promptCriteria, &ans)
	if err != nil {
		return store.WorkflowRunOutcome{Inputs: prompt, Output: answer}, err
	}
	have := map[string]bool{}
	for _, r := range ws.Concept.Concept.Requirements {
		have[strings.ToLower(oneLine(r))] = true
	}
	for _, r := range ans.Requirements {
		text := strings.TrimSpace(r.Text)
		if have[strings.ToLower(oneLine(text))] {
			continue
		}
		have[strings.ToLower(oneLine(text))] = true
		body := text
		if from := strings.TrimSpace(r.From); from != "" {
			body += "\n\nAus: " + from
		}
		in.Propose(Result{Kind: ResultRequirement, Title: text, Body: body, Evidence: r.Evidence, IdeaID: in.Data.IdeaID})
	}
	return done(prompt, answer, fmt.Sprintf("concept version %d, %d decisions", ws.Concept.Revision, len(ws.Decisions))), nil
})
