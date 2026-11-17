package store

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
)

const aiProject = "github.com/dw/app"

// aiFixture: org dw (person:1 owner and server admin, person:2 member) with
// the project aiProject, and an org "other" of person:3 with its own project.
func aiFixture(t *testing.T) (*Store, Org, Org) {
	t.Helper()
	st, o := prefixFixture(t)
	if _, err := st.ClaimProject("person:1", aiProject, "dw"); err != nil {
		t.Fatal(err)
	}
	other := mustOrg(t, st, "person:3", "Other", "other")
	if _, err := st.CreateNamedProject("person:3", "app", "other"); err != nil {
		t.Fatal(err)
	}
	return st, o, other
}

func aiConn(t *testing.T, st *Store, actor string, orgID int64, name, format string, models ...string) AIConnection {
	t.Helper()
	in := AIConnectionInput{OrgID: orgID, Name: name, Format: format, BaseURL: "https://api.example.com", KeySealed: []byte("sealed-" + name), KeyLast4: "WXYZ"}
	for _, m := range models {
		in.Models = append(in.Models, AIModel{Model: m, InUSD: 1, OutUSD: 2})
	}
	c, err := st.SaveAIConnection(actor, in)
	if err != nil {
		t.Fatalf("save %s: %v", name, err)
	}
	return c
}

var aiEventSeq int

// aiRun makes a running run of workflow in project.
func aiRun(t *testing.T, st *Store, project, workflow string) WorkflowRun {
	t.Helper()
	if _, err := st.SetWorkflowTriggerAsOperator(WorkflowTrigger{Project: project, Workflow: workflow, EventKind: EventIdeaCreated, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	aiEventSeq++
	if _, _, err := st.RecordWorkflowEvent(WorkflowEventInput{Kind: EventIdeaCreated, Project: project, SubjectKind: "idea", SubjectID: strconv.Itoa(aiEventSeq),
		Origin: EventOrigin{Kind: OriginHuman, Person: "robin"}, DedupeKey: "ai-test-" + strconv.Itoa(aiEventSeq)}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DispatchWorkflowEvents(0); err != nil {
		t.Fatal(err)
	}
	run, _, ok, err := st.ClaimWorkflowRun()
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	for ok {
		// Other triggers of the same project may have queued runs too; only the
		// run of this workflow is wanted running.
		if run.Workflow == workflow {
			return run
		}
		if err := st.FinishWorkflowRun(run.ID, WorkflowRunOutcome{State: RunSkipped}); err != nil {
			t.Fatal(err)
		}
		run, _, ok, err = st.ClaimWorkflowRun()
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Fatalf("no run of %s", workflow)
	return WorkflowRun{}
}

// AC-694.1: an owner keeps three named connections; a member keeps none; no
// listing carries the key.
func TestOwnerKeepsThreeConnectionsAndNoListingCarriesTheKey(t *testing.T) {
	st, o, _ := aiFixture(t)
	aiConn(t, st, "person:1", o.ID, "openai-a", AIFormatOpenAI, "gpt-a")
	aiConn(t, st, "person:1", o.ID, "openai-b", AIFormatOpenAI, "gpt-b")
	aiConn(t, st, "person:1", o.ID, "anthropic-team", AIFormatAnthropic, "claude-haiku-5-5", "claude-opus-5-5")
	if _, err := st.SaveAIConnection("person:2", AIConnectionInput{OrgID: o.ID, Name: "mine", Format: AIFormatOpenAI, BaseURL: "https://x.example",
		KeySealed: []byte("k"), Models: []AIModel{{Model: "m"}}}); !errors.Is(err, ErrNotOrgOwner) {
		t.Fatalf("member: %v", err)
	}
	if _, err := st.SaveAIConnection("person:1", AIConnectionInput{OrgID: o.ID, Name: "openai-a", Format: AIFormatOpenAI, BaseURL: "https://x.example",
		KeySealed: []byte("k"), Models: []AIModel{{Model: "m"}}}); !errors.Is(err, ErrAINameTaken) {
		t.Fatalf("same name: %v", err)
	}
	if _, err := st.SaveAIConnection("person:1", AIConnectionInput{OrgID: o.ID, Name: "plain", Format: AIFormatOpenAI, BaseURL: "http://127.0.0.1:9",
		KeySealed: []byte("k"), Models: []AIModel{{Model: "m"}}}); !errors.Is(err, ErrAIConnection) {
		t.Fatalf("plain http for an org: %v", err)
	}
	if _, err := st.SaveAIConnection("person:1", AIConnectionInput{OrgID: o.ID, Name: "nokey", Format: AIFormatOpenAI, BaseURL: "https://x.example",
		Models: []AIModel{{Model: "m"}}}); !errors.Is(err, ErrAIKeyMissing) {
		t.Fatalf("no key: %v", err)
	}
	list, err := st.AIConnectionsFor(o.ID)
	if err != nil || len(list) != 3 {
		t.Fatalf("list: %+v %v", list, err)
	}
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), "sealed-") || !strings.Contains(string(raw), "WXYZ") {
		t.Fatalf("a listing carries the sealed key or misses the last four: %s", raw)
	}
	// Changing a connection without a new key keeps the stored one.
	c := list[0]
	if _, err := st.SaveAIConnection("person:1", AIConnectionInput{ID: c.ID, Name: c.Name, Format: c.Format, BaseURL: c.BaseURL, MonthlyCapUSD: 5, Models: c.Models}); err != nil {
		t.Fatal(err)
	}
	if sealed, _, _ := st.AIConnectionKey(c.ID); string(sealed) != "sealed-"+c.Name {
		t.Fatalf("key after a change: %q", sealed)
	}
	// The stored key never follows a new address or format.
	otherFormat := AIFormatOpenAI
	if c.Format == AIFormatOpenAI {
		otherFormat = AIFormatAnthropic
	}
	for _, in := range []AIConnectionInput{
		{ID: c.ID, Name: c.Name, Format: c.Format, BaseURL: "https://elsewhere.example", Models: c.Models},
		{ID: c.ID, Name: c.Name, Format: otherFormat, BaseURL: c.BaseURL, Models: c.Models},
	} {
		if _, err := st.SaveAIConnection("person:1", in); !errors.Is(err, ErrAIKeyMissing) {
			t.Fatalf("change of address or format without a key: %v", err)
		}
	}
	in := AIConnectionInput{ID: c.ID, Name: c.Name, Format: c.Format, BaseURL: "https://elsewhere.example", Models: c.Models, KeySealed: []byte("sealed-new"), KeyLast4: "NEW4"}
	if got, err := st.SaveAIConnection("person:1", in); err != nil || got.BaseURL != "https://elsewhere.example" || got.KeyLast4 != "NEW4" {
		t.Fatalf("new address with a new key: %+v %v", got, err)
	}
}

// AC-694.2: the admin releases the server's connections to all, selected or
// no namespaces; without a release they are neither listed nor usable.
func TestServerConnectionsOnlyForReleasedNamespaces(t *testing.T) {
	st, o, other := aiFixture(t)
	if _, err := st.SaveAIConnection("person:3", AIConnectionInput{Name: "x", Format: AIFormatOpenAI, BaseURL: "https://x.example", KeySealed: []byte("k"),
		Models: []AIModel{{Model: "m"}}}); !errors.Is(err, ErrNotServerAdmin) {
		t.Fatalf("a non-admin keeps a server connection: %v", err)
	}
	srv := aiConn(t, st, "person:1", 0, "standard", AIFormatAnthropic, "claude-haiku-5-5")
	run := aiRun(t, st, aiProject, "wf/a")
	usable := func(org Org) bool {
		list, _ := st.AIConnectionsFor(org.ID)
		for _, c := range list {
			if c.ID == srv.ID {
				return true
			}
		}
		return false
	}
	if usable(o) || usable(other) {
		t.Fatal("no release yet: the server connection is listed")
	}
	if _, err := st.AIRouteFor(run, "standard/claude-haiku-5-5"); !errors.Is(err, ErrAIUnavailable) {
		t.Fatalf("no release yet: %v", err)
	}
	if err := st.SetAIRelease("person:3", AIReleaseAll); !errors.Is(err, ErrNotServerAdmin) {
		t.Fatalf("a non-admin sets the release: %v", err)
	}
	if err := st.SetAIRelease("person:1", AIReleaseSelected); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAIGrant("person:1", o.ID, 7); err != nil {
		t.Fatal(err)
	}
	if !usable(o) || usable(other) {
		t.Fatal("selected: only dw sees it")
	}
	route, err := st.AIRouteFor(run, "standard/claude-haiku-5-5")
	if err != nil || route.Via != AIViaServer || route.TightestCap != AICapServer || route.Remaining != 7 {
		t.Fatalf("route after the grant: %+v %v", route, err)
	}
	if err := st.SetAIRelease("person:1", AIReleaseAll); err != nil {
		t.Fatal(err)
	}
	if !usable(other) {
		t.Fatal("all: every namespace sees it")
	}
	if err := st.SetAIRelease("person:1", AIReleaseNone); err != nil {
		t.Fatal(err)
	}
	if usable(o) {
		t.Fatal("none: nobody sees it")
	}
	if _, err := st.AIRouteFor(run, "standard/claude-haiku-5-5"); !errors.Is(err, ErrAIUnavailable) {
		t.Fatalf("after none: %v", err)
	}
	if aliases, _ := st.AIAliasesFor(o.ID); len(aliases) != 0 {
		t.Fatalf("server aliases are not shown without a release: %+v", aliases)
	}
}

// AC-694.3: a person lends a connection of the profile to an org; the org
// sees its name only, and the loan taken back stops the next call.
func TestLoanShowsOnlyTheNameAndRevokingStopsTheNextCall(t *testing.T) {
	st, o, other := aiFixture(t)
	ns, err := st.EnsureUserNamespace("person:2")
	if err != nil {
		t.Fatal(err)
	}
	mine := aiConn(t, st, "person:2", ns.ID, "anna-key", AIFormatOpenAI, "gpt-x")
	if _, err := st.LendAIConnection("person:1", mine.ID, o.ID, 3); !errors.Is(err, ErrNotOrgOwner) {
		t.Fatalf("someone else lends it: %v", err)
	}
	if _, err := st.LendAIConnection("person:2", mine.ID, other.ID, 3); !errors.Is(err, ErrOrgNotFound) {
		t.Fatalf("lent to an org the person is not in: %v", err)
	}
	if _, err := st.LendAIConnection("person:2", mine.ID, o.ID, 0); !errors.Is(err, ErrAILoan) {
		t.Fatalf("a loan needs a cap: %v", err)
	}
	loan, err := st.LendAIConnection("person:2", mine.ID, o.ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := st.AIConnectionsFor(o.ID)
	if len(list) != 1 || list[0].Name != "anna-key" || list[0].Via != AIViaLoan || list[0].BaseURL != "" || list[0].Format != "" || list[0].KeyLast4 != "" || len(list[0].Models) != 0 {
		t.Fatalf("the org sees more than the name: %+v", list)
	}
	run := aiRun(t, st, aiProject, "wf/a")
	// The lender named the connection and its models; by those names alone it
	// draws no call of the org to itself, even one named like the org's own.
	own := aiConn(t, st, "person:1", o.ID, "team", AIFormatOpenAI, "gpt-x")
	for _, model := range []string{"gpt-x", "anna-key/gpt-x"} {
		if r, err := st.AIRouteFor(run, model); err == nil && r.Via == AIViaLoan {
			t.Fatalf("%s reaches the loan without an alias of the org", model)
		}
	}
	if r, _ := st.AIRouteFor(run, "gpt-x"); r.Connection.ID != own.ID {
		t.Fatalf("a bare model name stays on the org's own connection: %+v", r)
	}
	if _, err := st.SetAIAlias("person:1", AIAlias{Level: AILevelOwner, OrgID: o.ID, Alias: "klein", ConnectionID: mine.ID, Model: "gpt-x"}); err != nil {
		t.Fatal(err)
	}
	route, err := st.AIRouteFor(run, "klein")
	if err != nil || route.Via != AIViaLoan || route.LoanID != loan.ID || route.TightestCap != AICapLoan {
		t.Fatalf("route over the loan: %+v %v", route, err)
	}
	if err := st.RevokeAILoan("person:1", loan.ID); !errors.Is(err, ErrNotOrgOwner) {
		t.Fatalf("the org owner takes the loan back: %v", err)
	}
	if err := st.RevokeAILoan("person:2", loan.ID); err != nil {
		t.Fatal(err)
	}
	if r, err := st.AIRouteFor(run, "klein"); !errors.Is(err, ErrAIUnavailable) {
		t.Fatalf("after the revocation the alias still routes: %+v %v", r, err)
	}
	if list, _ := st.AIConnectionsFor(o.ID); len(list) != 1 || list[0].Name != "team" {
		t.Fatalf("after the revocation: %+v", list)
	}
}

// AC-694.4: repository before owner before server.
func TestAliasOfTheRepositoryWinsOverTheOwnerAndTheServer(t *testing.T) {
	st, o, _ := aiFixture(t)
	srv := aiConn(t, st, "person:1", 0, "standard", AIFormatAnthropic, "claude-haiku-5-5")
	if err := st.SetAIRelease("person:1", AIReleaseAll); err != nil {
		t.Fatal(err)
	}
	own := aiConn(t, st, "person:1", o.ID, "openai-a", AIFormatOpenAI, "gpt-a", "gpt-b")
	set := func(a AIAlias) {
		t.Helper()
		if _, err := st.SetAIAlias("person:1", a); err != nil {
			t.Fatalf("alias %+v: %v", a, err)
		}
	}
	set(AIAlias{Level: AILevelServer, Alias: "klein", ConnectionID: srv.ID, Model: "claude-haiku-5-5"})
	run := aiRun(t, st, aiProject, "wf/a")
	check := func(want, model string) {
		t.Helper()
		r, err := st.AIRouteFor(run, "Klein")
		if err != nil || r.Connection.Name != want || r.Model.Model != model {
			t.Fatalf("klein: %s %s %v, want %s %s", r.Connection.Name, r.Model.Model, err, want, model)
		}
	}
	check("standard", "claude-haiku-5-5")
	set(AIAlias{Level: AILevelOwner, OrgID: o.ID, Alias: "klein", ConnectionID: own.ID, Model: "gpt-a"})
	check("openai-a", "gpt-a")
	set(AIAlias{Level: AILevelRepo, OrgID: o.ID, Project: aiProject, Alias: "klein", ConnectionID: own.ID, Model: "gpt-b"})
	check("openai-a", "gpt-b")
	if _, err := st.SetAIAlias("person:1", AIAlias{Level: AILevelOwner, OrgID: o.ID, Alias: "x", ConnectionID: own.ID, Model: "not-on-it"}); !errors.Is(err, ErrAIUnavailable) {
		t.Fatalf("a model the connection does not serve: %v", err)
	}
	if _, err := st.SetAIAlias("person:2", AIAlias{Level: AILevelOwner, OrgID: o.ID, Alias: "x", ConnectionID: own.ID, Model: "gpt-a"}); !errors.Is(err, ErrNotOrgOwner) {
		t.Fatalf("a member sets an alias: %v", err)
	}
	if _, err := st.SetAIAlias("person:1", AIAlias{Level: AILevelRepo, OrgID: o.ID, Project: "other/app", Alias: "x", ConnectionID: own.ID, Model: "gpt-a"}); !errors.Is(err, ErrAIAlias) {
		t.Fatalf("an alias for a repository of another org: %v", err)
	}
}

// AC-694.6 in the store: a token is valid only while its run runs.
func TestRunTokenEndsWithItsRun(t *testing.T) {
	st, _, _ := aiFixture(t)
	run := aiRun(t, st, aiProject, "wf/a")
	token, err := st.IssueAIRunToken(run.ID)
	if err != nil || !strings.HasPrefix(token, AIRunTokenPrefix) {
		t.Fatalf("issue: %q %v", token, err)
	}
	var stored string
	st.db.QueryRow(`SELECT token_hash FROM ai_run_tokens`).Scan(&stored)
	if stored == token || strings.Contains(stored, token[4:]) {
		t.Fatal("the token is stored in the clear")
	}
	if got, err := st.AIRunOfToken(token); err != nil || got.ID != run.ID {
		t.Fatalf("token while running: %+v %v", got, err)
	}
	if _, err := st.AIRunOfToken(token + "x"); !errors.Is(err, ErrAIToken) {
		t.Fatalf("a wrong token: %v", err)
	}
	if err := st.FinishWorkflowRun(run.ID, WorkflowRunOutcome{State: RunDone}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AIRunOfToken(token); !errors.Is(err, ErrAIToken) {
		t.Fatalf("token after the run: %v", err)
	}
	if _, err := st.IssueAIRunToken(run.ID); !errors.Is(err, ErrWorkflowRun) {
		t.Fatalf("a token for a finished run: %v", err)
	}
	// A run cut off by a restart takes its token with it.
	second := aiRun(t, st, aiProject, "wf/b")
	token2, err := st.IssueAIRunToken(second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := st.AbandonWorkflowRuns(); err != nil || n != 1 {
		t.Fatalf("abandon: %d %v", n, err)
	}
	if _, err := st.AIRunOfToken(token2); !errors.Is(err, ErrAIToken) {
		t.Fatalf("token after the restart: %v", err)
	}
	var left int
	st.db.QueryRow(`SELECT COUNT(*) FROM ai_run_tokens`).Scan(&left)
	if left != 0 {
		t.Fatalf("tokens left: %d", left)
	}
}

// AC-694.7 in the store: each cap stops the run that reached it, under its
// own name, and the tightest decides.
func TestEachCapStopsTheRunUnderItsName(t *testing.T) {
	st, o, _ := aiFixture(t)
	conn := aiConn(t, st, "person:1", o.ID, "openai-a", AIFormatOpenAI, "gpt-a")
	book := func(run WorkflowRun, usd float64) string {
		t.Helper()
		r, err := st.AIRouteFor(run, "gpt-a")
		if err != nil {
			t.Fatalf("route: %v", err)
		}
		capName, err := st.RecordAICall(AICall{RunID: run.ID, Route: r, InputTokens: 10, OutputTokens: 5, CostUSD: usd, Status: 200})
		if err != nil {
			t.Fatal(err)
		}
		return capName
	}
	for _, tc := range []struct {
		name string
		set  func()
		want string
	}{
		{"run", func() { _ = st.SetAICap("person:1", AICap{OrgID: o.ID, RunUSD: 1}) }, AICapRun},
		{"workflow", func() {
			_ = st.SetAICap("person:1", AICap{OrgID: o.ID})
			_ = st.SetAICap("person:1", AICap{OrgID: o.ID, Workflow: "wf/w", MonthUSD: 2})
		}, AICapWorkflow},
		{"owner", func() {
			_ = st.SetAICap("person:1", AICap{OrgID: o.ID, Workflow: "wf/w"})
			_ = st.SetAICap("person:1", AICap{OrgID: o.ID, MonthUSD: 3.5})
		}, AICapOwner},
		{"connection", func() {
			_ = st.SetAICap("person:1", AICap{OrgID: o.ID})
			c := conn
			if _, err := st.SaveAIConnection("person:1", AIConnectionInput{ID: c.ID, Name: c.Name, Format: c.Format, BaseURL: c.BaseURL, Models: c.Models, MonthlyCapUSD: 4.5}); err != nil {
				t.Fatal(err)
			}
		}, AICapConnection},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.set()
			run := aiRun(t, st, aiProject, "wf/w")
			got := ""
			for i := 0; i < 20 && got == ""; i++ {
				got = book(run, 0.5)
			}
			if got != tc.want {
				t.Fatalf("cap reached: %q, want %q", got, tc.want)
			}
			var be *AIBudgetError
			if _, err := st.AIRouteFor(run, "gpt-a"); !errors.As(err, &be) || be.Cap != tc.want {
				t.Fatalf("a further call: %v", err)
			}
			if u, _ := st.AIRunUsageOf(run.ID); u.Cap != tc.want || u.Connections[0] != "openai-a" || u.Models[0] != "gpt-a" {
				t.Fatalf("usage: %+v", u)
			}
			if err := st.FinishWorkflowRun(run.ID, WorkflowRunOutcome{State: RunBudget, Reason: tc.want}); err != nil {
				t.Fatal(err)
			}
		})
	}
	if err := st.SetAICap("person:2", AICap{OrgID: o.ID, MonthUSD: 1}); !errors.Is(err, ErrNotOrgOwner) {
		t.Fatalf("a member sets a cap: %v", err)
	}
}

// AC-694.5 in the store: two workflows over two connections and models in
// the same month; every amount has its workflow, run, connection and model.
func TestCostReportAssignsEveryAmount(t *testing.T) {
	st, o, other := aiFixture(t)
	a := aiConn(t, st, "person:1", o.ID, "openai-a", AIFormatOpenAI, "gpt-a")
	b := aiConn(t, st, "person:1", o.ID, "anthropic-team", AIFormatAnthropic, "claude-haiku-5-5")
	if _, err := st.SetAIAlias("person:1", AIAlias{Level: AILevelOwner, OrgID: o.ID, Alias: "klein", ConnectionID: b.ID, Model: "claude-haiku-5-5"}); err != nil {
		t.Fatal(err)
	}
	_ = a
	r1 := aiRun(t, st, aiProject, "wf/one")
	r2 := aiRun(t, st, aiProject, "wf/two")
	for _, c := range []struct {
		run   WorkflowRun
		model string
		usd   float64
	}{{r1, "gpt-a", 0.25}, {r1, "gpt-a", 0.25}, {r2, "klein", 1.5}} {
		route, err := st.AIRouteFor(c.run, c.model)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.RecordAICall(AICall{RunID: c.run.ID, Route: route, InputTokens: 100, OutputTokens: 10, CostUSD: c.usd, Status: 200}); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := st.AICostReportOf(o.ID, AIMonthStart())
	if err != nil {
		t.Fatal(err)
	}
	if rep.TotalUSD != 2 || rep.Calls != 3 || len(rep.ByWorkflow) != 2 || len(rep.ByRun) != 2 || len(rep.ByConnection) != 2 || len(rep.ByModel) != 2 {
		t.Fatalf("report: %+v", rep)
	}
	if rep.ByWorkflow[0].Key != "wf/two" || rep.ByWorkflow[0].CostUSD != 1.5 || rep.ByConnection[0].Key != "anthropic-team" || rep.ByModel[0].Key != "claude-haiku-5-5" ||
		rep.ByRun[0].RunID != r2.ID || rep.ByRun[1].RunID != r1.ID || rep.ByRun[1].CostUSD != 0.5 || rep.ByRun[0].Project != aiProject {
		t.Fatalf("assignment: %+v", rep)
	}
	if rep, _ := st.AICostReportOf(other.ID, AIMonthStart()); rep.Calls != 0 {
		t.Fatalf("another org sees the costs: %+v", rep)
	}
}

// The connection from llm.json comes back on every start, keeps the key out
// of the database and seeds the default aliases once.
func TestFileConnectionIsImportedWithoutItsKey(t *testing.T) {
	st, _, _ := aiFixture(t)
	in := AIFileConnection{Name: "standard", Format: AIFormatAnthropic, BaseURL: "http://127.0.0.1:4000", Model: "claude-sonnet-5-5", InUSD: 2, OutUSD: 10, KeyLast4: "abcd"}
	c, err := st.ImportFileAIConnection(in)
	if err != nil || !c.FromFile || c.KeyLast4 != "abcd" {
		t.Fatalf("import: %+v %v", c, err)
	}
	if sealed, file, _ := st.AIConnectionKey(c.ID); sealed != nil || !file {
		t.Fatalf("key of a file connection: %q %v", sealed, file)
	}
	aliases, _ := st.ServerAIAliases()
	if len(aliases) != 3 || aliases[0].Model != "claude-sonnet-5-5" {
		t.Fatalf("default aliases: %+v", aliases)
	}
	if _, err := st.ImportFileAIConnection(in); err != nil {
		t.Fatal(err)
	}
	if aliases, _ := st.ServerAIAliases(); len(aliases) != 3 {
		t.Fatalf("aliases after a second start: %+v", aliases)
	}
	if err := st.DeleteAIConnection("person:1", c.ID); !errors.Is(err, ErrAIFromFile) {
		t.Fatalf("delete a file connection: %v", err)
	}
}
