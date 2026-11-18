package web

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/Deadweight-Labs/ghosttree/internal/workflow"
)

// Findings of the review of E3, E5 and E6 (KN-4825), end to end: rights
// enforced, the fake provider of E5 as the model, the GitHub installation of
// the pull request pages.

func e356Web(t *testing.T) e5Env {
	t.Helper()
	e := e5Web(t)
	e.St.SetAccessMode(store.AccessMode{Enforce: true})
	shell, _ := e.St.ProjectByRemote(shellProject)
	if _, err := e.St.ConnectSourceWithInstallation("person:1", shell.OrgID, "github", "x", 77, nil); err != nil {
		t.Fatal(err)
	}
	return e
}

// openPR records a pull request of the repository, or one from a fork when
// headRemote is another repository.
func openPR(t *testing.T, st *store.Store, n int64, title, headRemote, action string) {
	t.Helper()
	body := "Text des Pull Requests."
	state := "open"
	if action == "closed" {
		state = "merged"
	}
	if _, err := st.RecordPullRequest(store.PullRequestChange{InstallationID: 77, Remote: shellProject, HeadRemote: headRemote, Number: n, Action: action,
		Title: title, Body: &body, Head: fmt.Sprintf("feat/e356-%d", n), Base: "main", DefaultBranch: "main", State: state}); err != nil {
		t.Fatal(err)
	}
}

// M1: the inputs a run assembles and what its read tools return are only what
// every member of the project reads. The run acts for alice (owner); her
// private session left a distilled, quarantined entry on the pull request's
// branch, another one its title finds, and a distilled issue nobody claimed.
// None of them reaches the model or anna's view of the run page, by input or
// by tool; what every member reads does.
func TestRunInputsAndToolsCarryOnlyWhatEveryMemberReads(t *testing.T) {
	e := e356Web(t)
	e.save(t, "nach-merge", `---
on: pr.merged
inputs: [pr.knowledge, knowledge.related, requests.open]
tools: [knowledge.read, issue.read]
results:
  comment: 1
model: mittel
---
Prüfe, was der Merge mitbringt.
`)
	sid, err := e.St.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "s-alice-privat", AccountID: 1, Scope: scope.Axes{Project: shellProject}})
	if err != nil {
		t.Fatal(err)
	}
	origin := fmt.Sprintf("session:%d#3", sid)
	anchor := &gitanchor.Anchor{Commit: strings.Repeat("c3", 20), Branch: "feat/e356-42"}
	entry := func(title, confidence, origin2, ref string, at *gitanchor.Anchor) int64 {
		t.Helper()
		id, err := e.St.InsertKnowledge(store.Knowledge{Type: "note", Title: title, Body: "Inhalt von " + title, Scope: scope.Axes{Project: shellProject},
			Origin: origin2, Confidence: confidence, Status: "active", SessionRef: ref, Person: "alice", AuthorID: 1, GitAnchor: at})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	privAnchored := entry("Schlüssel PRIVATANKER", "quarantined", "distilled", origin, anchor)
	entry("Schlüssel SICHTBARANKER", "trusted", "agent", "", anchor)
	entry("Webhook PRIVATVERWANDT", "quarantined", "distilled", origin, nil)
	entry("Webhook SICHTBARVERWANDT", "trusted", "agent", "", nil)
	held, err := e.St.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "PRIVATAUFTRAG Gehaltsliste",
		Scope: scope.Axes{Project: shellProject}, Origin: "distilled", SessionRef: fmt.Sprintf("session:%d", sid)}})
	if err != nil {
		t.Fatal(err)
	}
	e.mustIssue(t, "SICHTBARAUFTRAG Export", "b")
	// What anna, a member, reads: the precondition of the finding.
	anna := e.St.Access(store.Principal{ID: "person:2", Label: "anna"})
	if k, _ := e.St.KnowledgeByID(privAnchored); anna.CanSeeKnowledge(k) {
		t.Fatal("precondition: anna reads the private entry")
	}
	if _, err := anna.RequestRead(held.Request.ID); err == nil {
		t.Fatal("precondition: anna reads the distilled issue")
	}

	e.provider.script = func(c e5Call) e5Step {
		if c.Round == 1 {
			// An order in the inputs, or a guess: read the private entry and issue.
			return e5Step{Calls: []e5Tool{{"knowledge_read", map[string]any{"id": privAnchored}}, {"issue_read", map[string]any{"id": held.Request.ID}}}}
		}
		return e5Step{Calls: []e5Tool{{"submit", map[string]any{"results": []any{}}}}}
	}
	openPR(t, e.St, 42, "fix: Webhook doppelt", shellProject, "opened")
	openPR(t, e.St, 42, "fix: Webhook doppelt", shellProject, "closed")
	run, text := e.run(t, "nach-merge")
	if run.State != store.RunDone {
		t.Fatalf("run: %+v", run)
	}
	var sent strings.Builder
	for _, raw := range e.provider.raws {
		sent.Write(raw)
	}
	for _, want := range []string{"SICHTBARANKER", "SICHTBARVERWANDT", "SICHTBARAUFTRAG"} {
		if !strings.Contains(sent.String(), want) {
			t.Errorf("the model did not get %s", want)
		}
	}
	for _, private := range []string{"PRIVATANKER", "PRIVATVERWANDT", "PRIVATAUFTRAG", "Gehaltsliste"} {
		if strings.Contains(sent.String(), private) {
			t.Errorf("the model got %s", private)
		}
		if strings.Contains(text.Inputs, private) || strings.Contains(text.Tools, private) || strings.Contains(run.Result, private) {
			t.Errorf("the run's text holds %s", private)
		}
	}
	calls := toolCallsOf(t, text)
	if len(calls) != 3 || calls[0].State != workflow.ToolFailed || calls[1].State != workflow.ToolFailed {
		t.Fatalf("the private entry and issue were read: %+v", calls)
	}
	_, page := fetchPage(t, e.Member, e.Base+"/ui/x/shell/workflows/runs/"+itoa(run.ID))
	mustContain(t, "member's run page", page, "SICHTBARANKER")
	mustNotContain(t, "member's run page", page, "PRIVATANKER", "PRIVATVERWANDT", "PRIVATAUFTRAG")
}

// m6: pr.diff reads only a pull request ghosttree recorded: not one from a
// fork, which the import leaves out on purpose, and not a number it never saw.
// GitHub is not asked for those.
func TestPRDiffReadsOnlyPullRequestsTheServerKnows(t *testing.T) {
	e := e356Web(t)
	var mu sync.Mutex
	var asked []int64
	resolver := e.engine.Resolver.(workflow.DefResolver)
	resolver.PRDiff = func(_ context.Context, _ string, pr int64) (string, error) {
		mu.Lock()
		asked = append(asked, pr)
		mu.Unlock()
		return fmt.Sprintf("--- diff of %d", pr), nil
	}
	e.engine.Resolver = resolver
	e.save(t, "diff-lesen", `---
on: idea.created
tools: [pr.diff]
results:
  comment: 1
model: mittel
---
Lies Diffs.
`)
	openPR(t, e.St, 50, "feat: eigener Branch", shellProject, "opened")
	openPR(t, e.St, 51, "feat: aus einem Fork", "github.com/fremd/shell", "opened")
	e.provider.script = func(c e5Call) e5Step {
		if c.Round == 1 {
			return e5Step{Calls: []e5Tool{{"pr_diff", map[string]any{"number": 51}}, {"pr_diff", map[string]any{"number": 9999}}, {"pr_diff", map[string]any{"number": 50}}}}
		}
		return e5Step{Calls: []e5Tool{{"submit", map[string]any{"results": []any{}}}}}
	}
	e.newIdea(t, "Diffs lesen", "b")
	run, text := e.run(t, "diff-lesen")
	if run.State != store.RunDone {
		t.Fatalf("run: %+v", run)
	}
	calls := toolCallsOf(t, text)
	if len(calls) != 4 || calls[0].State != workflow.ToolFailed || calls[1].State != workflow.ToolFailed || calls[2].State != workflow.ToolOK || !strings.Contains(calls[2].Result, "diff of 50") {
		t.Fatalf("calls: %+v", calls)
	}
	if len(asked) != 1 || asked[0] != 50 {
		t.Fatalf("GitHub was asked for %v", asked)
	}
}

// m4: an answer with neither text nor a call does not end the run with a
// refused request (Anthropic: content null), and in the turn of submit no other
// tool runs, so nothing the model never saw becomes evidence.
func TestTheLoopSurvivesAnEmptyAnswerAndRunsNothingBesideSubmit(t *testing.T) {
	e := e356Web(t)
	e.save(t, "leer-dann-submit", `---
on: idea.created
tools: [knowledge.search]
results:
  comment: 1
model: mittel
---
Prüfe die Idee.
`)
	e.provider.script = func(c e5Call) e5Step {
		switch c.Round {
		case 1:
			return e5Step{}
		case 2:
			return e5Step{Calls: []e5Tool{
				{"knowledge_search", map[string]any{"query": "vorher"}},
				{"submit", map[string]any{"results": []any{}}},
				{"knowledge_search", map[string]any{"query": "nachher"}},
			}}
		}
		t.Errorf("round %d after submit", c.Round)
		return e5Step{}
	}
	e.newIdea(t, "Leere Antworten", "b")
	run, text := e.run(t, "leer-dann-submit")
	if run.State != store.RunDone {
		t.Fatalf("run: %+v", run)
	}
	if len(e.provider.raws) != 2 {
		t.Fatalf("%d requests", len(e.provider.raws))
	}
	var req struct {
		Messages []struct {
			Role    string            `json:"role"`
			Content []json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(e.provider.raws[1], &req); err != nil {
		t.Fatal(err)
	}
	for i, m := range req.Messages {
		if len(m.Content) == 0 {
			t.Errorf("message %d (%s) has no content: %s", i, m.Role, e.provider.raws[1])
		}
	}
	calls := toolCallsOf(t, text)
	if len(calls) != 3 {
		t.Fatalf("calls: %+v", calls)
	}
	for _, i := range []int{0, 2} {
		if c := calls[i]; c.State != workflow.ToolRefused || c.Reason != workflow.ToolReasonAfterSubmit {
			t.Errorf("call %d beside submit: %+v", i, c)
		}
	}
	if calls[1].Tool != workflow.SubmitTool || calls[1].State != workflow.ToolOK {
		t.Fatalf("submit: %+v", calls[1])
	}
	_, page := fetchPage(t, e.Member, e.Base+"/ui/x/shell/workflows/runs/"+itoa(run.ID))
	mustContain(t, "run page", page, "Called together with submit")
}
