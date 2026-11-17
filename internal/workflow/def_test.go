package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const goodDef = `---
description: Wissen nach dem Merge
on: pr.merged
branches: [main, "release/*"]   # merges into these
inputs:
  - pr.title
  - pr.number
uses: ghosttree/ask
results:
  knowledge: 2
model: klein
budget:
  run_usd: 0.20
  month_usd: 5
---
Achte auf Migrationen.
`

func TestParseReadsEverySection(t *testing.T) {
	d, probs := Parse(goodDef)
	if len(probs) > 0 {
		t.Fatalf("problems: %v", probs)
	}
	if d.On != store.EventPRMerged || strings.Join(d.Branches, ",") != "main,release/*" || strings.Join(d.Inputs, ",") != "pr.title,pr.number" ||
		d.Uses != AskName || d.MaxOf(ResultKnowledge) != 2 || d.Model != "klein" || d.RunUSD != 0.2 || d.MonthUSD != 5 ||
		d.Instruction != "Achte auf Migrationen." || d.Stage() != StageInstruction || d.Description != "Wissen nach dem Merge" {
		t.Fatalf("parsed: %+v", d)
	}
}

// AC-695.6: unknown keys, kinds of result that are not allowed and unknown
// aliases are errors, and the report says what the workflow reads, writes and
// may cost at most, without a model call.
func TestCheckReportsUnknownKeysKindsAndAliasesAndWhatItMayDo(t *testing.T) {
	aliases := func(name string) bool { return name == "klein" }
	cat := DefaultCatalog()
	spec := cat[AskName]
	spec.Results = []string{ResultKnowledge}
	cat[AskName] = spec
	_, rep := Check(goodDef, Env{Catalog: cat, Alias: aliases, OwnerRunUSD: 1})
	if len(rep.Errors) > 0 {
		t.Fatalf("good file has errors: %v", rep.Errors)
	}
	if rep.MaxRunUSD != 0.2 || len(rep.Writes) != 1 || rep.Writes[0] != (ResultLimit{ResultKnowledge, 2}) || strings.Join(rep.Reads, ",") != "pr.title,pr.number" || rep.Stage != StageInstruction {
		t.Fatalf("what it may do: %+v", rep)
	}
	// ask cannot create knowledge in E2: an error on the line of results.
	bad := strings.Replace(goodDef, "model: klein", "model: riesig\nretries: 3", 1)
	bad = strings.Replace(bad, "  knowledge: 2", "  knowledge: 2\n  poems: 1", 1)
	_, rep = Check(bad, Env{Alias: aliases})
	want := []string{"unknown key retries", "unknown kind of result poems", "unknown model alias riesig", "ghosttree/ask cannot create knowledge"}
	for _, w := range want {
		if !hasProblem(rep.Errors, w) {
			t.Errorf("missing error %q in %v", w, rep.Errors)
		}
	}
	for _, p := range rep.Errors {
		if strings.Contains(p.Msg, "retries") && p.Line != 13 {
			t.Errorf("unknown key on line %d, want 13", p.Line)
		}
	}
	echo := "---\non: pr.merged\ninputs: [pr.title]\nuses: ghosttree/echo\n---\n"
	_, rep = Check(echo, Env{})
	if !rep.OK() || rep.MaxRunUSD != 0 || rep.Unbounded || strings.Join(rep.Reads, ",") != "pr.title" || rep.Aliases {
		t.Fatalf("echo report: %+v", rep)
	}
	ask := strings.Replace(goodDef, "  knowledge: 2\n", "", 1)
	ask = strings.Replace(ask, "results:\n", "", 1)
	_, rep = Check(ask, Env{Alias: aliases, OwnerRunUSD: 0.1})
	if !rep.OK() || rep.MaxRunUSD != 0.1 || rep.MaxMonthUSD != 5 || rep.Model != "klein" || !rep.Aliases {
		t.Fatalf("the owner's lower cap bounds a run: %+v", rep)
	}
}

func hasProblem(ps []Problem, part string) bool {
	for _, p := range ps {
		if strings.Contains(p.Msg, part) {
			return true
		}
	}
	return false
}

// Stage 3 (an own prompt) is refused with a pointer to E5; inputs must fit the
// event; a schedule needs every.
func TestCheckRefusesAnOwnPromptAndInputsOfAnotherEvent(t *testing.T) {
	for text, want := range map[string]string{
		"---\non: pr.merged\nprompt: \"Fasse zusammen\"\n---\n":                  "stage E5",
		"---\non: idea.created\n---\nFasse zusammen.\n":                          "stage E5",
		"---\non: idea.created\ninputs: [pr.title]\nuses: ghosttree/echo\n---\n": "input pr.title is not there on idea.created",
		"---\non: schedule\nuses: ghosttree/echo\n---\n":                         "every 5 minutes to 7 days",
		"---\non: idea.discussion_closed\nuses: ghosttree/echo\n---\n":           "stage E3",
		"---\non: pr.merged\nuses: ghosttree/echo\n---\nNimm dir Zeit.\n":        "takes no instruction",
		"---\non: pr.merged\nuses: ghosttree/nope\n---\n":                        "unknown block ghosttree/nope",
		"on: pr.merged\n": "has to begin with a line ---",
		"---\non: pr.merged\nuses: ghosttree/ask\nmodel: klein\nbudget: 3\n---\n": "budget takes indented",
		"---\non: idea.created\nbranches: [main]\nuses: ghosttree/echo\n---\n":    "branches only narrows pr.merged",
		"---\non: pr.merged\nuses: ghosttree/ask\n---\n":                          "name a model alias",
		"---\non: pr.merged\non: idea.created\nuses: ghosttree/echo\n---\n":       "on is set twice",
		"---\non: pr.merged\nbranches: [\"a b\"]\nuses: ghosttree/echo\n---\n":    "branch pattern",
		"---\non: pr.merged\nuses: ghosttree/echo\nresults:\n  issue: 0\n---\n":   "a number of at least 1",
	} {
		_, rep := Check(text, Env{})
		if !hasProblem(rep.Errors, want) {
			t.Errorf("%q: want %q, got %v", text, want, rep.Errors)
		}
	}
}

// AC-695.7 at the check: the policy of the owner forbids stage 2, research and
// single kinds; Violation names the first rule broken.
func TestPolicyForbidsStagesResearchAndKinds(t *testing.T) {
	d, _ := Parse(goodDef)
	if v := (Policy{DenyInstruction: true}).Violation(d); v != PolicyInstruction {
		t.Fatalf("stage 2: %q", v)
	}
	if v := (Policy{DenyKinds: []string{ResultKnowledge}}).Violation(d); v != PolicyKind+":"+ResultKnowledge {
		t.Fatalf("kind: %q", v)
	}
	d.Research = true
	if v := (Policy{DenyResearch: true}).Violation(d); v != PolicyResearch {
		t.Fatalf("research: %q", v)
	}
	if v := (Policy{}).Violation(d); v != "" {
		t.Fatalf("no policy: %q", v)
	}
	_, rep := Check(goodDef, Env{Policy: Policy{DenyInstruction: true}})
	if !hasProblem(rep.Errors, "forbids an instruction") {
		t.Fatalf("check under policy: %v", rep.Errors)
	}
}

// AC-695.1/695.2 at the engine: a run of a stored workflow names the version
// it ran with, and a saved change applies to the very next run.
func TestTheNextRunTakesTheNewestVersionAndNamesIt(t *testing.T) {
	st := engineStore(t)
	save := func(text string, base int) {
		t.Helper()
		d, rep := Check(text, Env{})
		if !rep.OK() {
			t.Fatal(rep.Errors)
		}
		if _, err := st.SaveWorkflowDef("person:1", store.WorkflowDefSave{Project: project, Name: "melden", Text: text, Base: base,
			Event: d.On, Branches: d.Branches, Uses: d.Uses}); err != nil {
			t.Fatal(err)
		}
	}
	var seen []string
	cat := DefaultCatalog()
	spec := cat[EchoName]
	spec.Run = BlockFunc(func(_ context.Context, in Input) (store.WorkflowRunOutcome, error) {
		seen = append(seen, strings.Join(in.Def.Inputs, ","))
		return store.WorkflowRunOutcome{State: store.RunDone, Result: "ok"}, nil
	})
	cat[EchoName] = spec
	e := &Engine{Store: st, Resolver: DefResolver{Store: st, Catalog: cat}}
	save("---\non: pr.merged\ninputs: [pr.number]\nuses: ghosttree/echo\n---\n", 0)
	merged(t, st, "1")
	if _, err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	save("---\non: pr.merged\ninputs: [pr.number, pr.base]\nuses: ghosttree/echo\n---\n", 1)
	merged(t, st, "2")
	if _, err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	runs, err := st.WorkflowRunsIn(project, store.WorkflowRunFilter{Workflow: "melden"})
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs: %+v %v", runs, err)
	}
	if runs[0].WorkflowVersion != "2" || runs[1].WorkflowVersion != "1" || runs[0].State != store.RunDone {
		t.Fatalf("versions of the runs: %q %q (%s)", runs[0].WorkflowVersion, runs[1].WorkflowVersion, runs[0].State)
	}
	if strings.Join(seen, "|") != "pr.number|pr.number,pr.base" {
		t.Fatalf("the blocks saw: %v", seen)
	}
	// Switched off after its run was queued: skipped, with the reason.
	if err := st.SetWorkflowDefEnabled("person:1", project, "melden", true); err != nil {
		t.Fatal(err)
	}
	merged(t, st, "3")
	if _, err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := st.SetWorkflowDefEnabled("person:1", project, "melden", false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	runs, _ = st.WorkflowRunsIn(project, store.WorkflowRunFilter{Workflow: "melden", Limit: 1})
	if runs[0].State != store.RunSkipped || runs[0].Reason != SkipDisabled || runs[0].WorkflowVersion != "2" {
		t.Fatalf("run of a workflow switched off: %+v", runs[0])
	}
}
