package mcpserver

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/eval/requestledger"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRequestToolsRunCompleteAgentWorkflow(t *testing.T) {
	c, st := newTestClient(t)
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y", Branch: "feature", Machine: "workstation-a"}}
	_, created, err := s.handleRequestCreate(context.Background(), nil, RequestCreateInput{
		Type: "feature", Title: "agent ledger", Description: "associate work", Criteria: []string{"workflow passes"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Asserted on the concise reply rather than the full detail object: a
	// mutation that answers with the whole request charges the agent for every
	// status update out of the context it needs for the work.
	if created.State != "open" || len(created.Criteria) != 1 {
		t.Fatalf("created = %+v", created)
	}
	_, _ = st.UpsertSession(store.Session{Harness: "codex", ExternalID: "mcp-work", Scope: s.ctxAxes})
	otherBranch := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y", Branch: "other", Machine: "workstation-a"}}
	_, found, err := otherBranch.handleRequestSearch(context.Background(), nil, RequestSearchInput{Query: "agent ledger"})
	if err != nil || len(found.Results) != 1 {
		t.Fatalf("project-wide search=%+v err=%v", found, err)
	}
	_, started, err := s.handleRequestStartWork(context.Background(), nil, RequestStartWorkInput{RequestID: created.RequestID, Role: "primary"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.handleRequestProgress(context.Background(), nil, RequestProgressInput{
		RequestID: created.RequestID, Action: "criterion_met", CriterionID: created.Criteria[0].ID,
		EvidenceKind: "test", EvidenceRef: "go test ./internal/mcpserver",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.handleRequestFinishWork(context.Background(), nil, RequestFinishWorkInput{WorkID: started.Work.ID, State: "completed", Summary: "MCP flow passed"})
	if err != nil {
		t.Fatal(err)
	}
	_, completed, err := s.handleRequestProgress(context.Background(), nil, RequestProgressInput{
		RequestID: created.RequestID, Action: "complete", EvidenceKind: "commit", EvidenceRef: "abc",
	})
	if err != nil || completed.State != "done" {
		t.Fatalf("completed = %+v, err = %v", completed, err)
	}
}

func TestRequestSearchAnswersInterruptedWorkInsteadOfFTSGuess(t *testing.T) {
	c, st := newTestClient(t)
	project := "github.com/x/y"
	detail, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{
		Type: "bug", Title: "Der echte liegengebliebene Faden", Scope: scope.Axes{Project: project},
	}})
	if err != nil {
		t.Fatal(err)
	}
	sessionID, err := st.UpsertSession(store.Session{Harness: "codex", ExternalID: "earlier", Scope: scope.Axes{Project: project}})
	if err != nil {
		t.Fatal(err)
	}
	work, _, err := st.StartRequestWork(detail.Request.ID, sessionID, "primary", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.FinishRequestWork(work.ID, "paused", "Tests fehlen noch", "alice"); err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"Zuletzt ausgelieferte Version", "Fertig gebaute Suche"} {
		if _, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{
			Type: "change", Title: title, Scope: scope.Axes{Project: project},
		}}); err != nil {
			t.Fatal(err)
		}
	}

	s := &Server{client: c, ctxAxes: scope.Axes{Project: project}, sessionRef: "current"}
	_, got, err := s.handleRequestSearch(context.Background(), nil, RequestSearchInput{Query: "zuletzt gearbeitet nicht fertig"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Interpretation != "interrupted_work" {
		t.Fatalf("interpretation = %q, want interrupted_work", got.Interpretation)
	}
	if len(got.Results) != 1 || got.Results[0].ID != detail.Request.ID || got.Results[0].Handoff != "Tests fehlen noch" {
		t.Fatalf("results = %+v, want the paused request with its handoff", got.Results)
	}
}

func TestRequestInventoryLabelsItselfAsAnInventory(t *testing.T) {
	c, st := newTestClient(t)
	if _, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{
		Type: "feature", Title: "Offener Bestand", Scope: scope.Axes{Project: "github.com/x/y"},
	}}); err != nil {
		t.Fatal(err)
	}
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}}
	_, got, err := s.handleRequestSearch(context.Background(), nil, RequestSearchInput{Query: "was ist noch zu tun"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Interpretation != "open_request_inventory" {
		t.Fatalf("interpretation = %q, want an explicit inventory label", got.Interpretation)
	}
}

// A request is an intention about work, not a property of the workstation it
// was filed from. Inheriting branch and machine from the session would hide it
// from every scope-exact query made on a different branch or machine.
func TestRequestCreateFilesUnderProjectOnly(t *testing.T) {
	c, st := newTestClient(t)
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y", Branch: "feature", Machine: "workstation-a"}}
	_, created, err := s.handleRequestCreate(context.Background(), nil, RequestCreateInput{
		Type: "bug", Title: "scoped too narrowly", Description: "...", Criteria: []string{"filed project-wide"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Read back rather than trusting the reply: the mutation answers concisely
	// and no longer echoes the stored scope.
	stored, err := c.GetRequest(created.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.Request.Scope; got.Branch != "" || got.Machine != "" || got.Project != "github.com/x/y" {
		t.Fatalf("scope = %+v, want project only", got)
	}
	// The REST path filters axes exactly, so a scope-exact query from another
	// machine must still see it.
	page, err := st.SearchRequests(requestdomain.SearchFilter{
		Scope: scope.Axes{Project: "github.com/x/y", Branch: "main", Machine: "laptop"}, State: "open"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 1 {
		t.Fatalf("results = %d, want 1: request invisible from another branch and machine", len(page.Results))
	}
}

func TestRequestToolsAreDiscoverableWithSchemasAndAnnotations(t *testing.T) {
	c, _ := newTestClient(t)
	ghosttree := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}}
	protocolServer := mcp.NewServer(&mcp.Implementation{Name: "ghosttree-test", Version: "test"}, nil)
	ghosttree.Register(protocolServer)
	protocolClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	serverSession, err := protocolServer.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	clientSession, err := protocolClient.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	listed, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"request_search", "request_get", "request_create", "request_start_work", "request_finish_work", "request_record_progress"}
	for _, tool := range listed.Tools {
		if !slices.Contains(want, tool.Name) {
			continue
		}
		if tool.InputSchema == nil || tool.OutputSchema == nil || tool.Annotations == nil {
			t.Errorf("tool %s missing schema or annotations: %+v", tool.Name, tool)
		}
		want = slices.DeleteFunc(want, func(name string) bool { return name == tool.Name })
	}
	if len(want) != 0 {
		t.Fatalf("missing tools: %v", want)
	}
}

// A mutation answers "what changed". Returning the whole request instead means
// the agent pays for every status update with the context it needs for the
// work, which makes neglecting the ledger cheaper than keeping it — measured
// while working the backlog: six calls to mark five criteria on one request
// cost more context than the code change they recorded.
func TestMutationsAnswerWithoutTheWholeRequest(t *testing.T) {
	c, st := newTestClient(t)
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y", Branch: "feature", Machine: "workstation-a"}}
	description := strings.Repeat("why this matters, at the length a real request is written. ", 40)
	_, created, err := s.handleRequestCreate(context.Background(), nil, RequestCreateInput{
		Type: "bug", Title: "oversized replies", Description: description,
		Criteria: []string{"first", "second", "third"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.RequestID == 0 || len(created.Criteria) != 3 || created.OpenCriteria != 3 {
		t.Fatalf("created = %+v, want the ids needed to record progress against", created)
	}
	_, _ = st.UpsertSession(store.Session{Harness: "codex", ExternalID: "mcp-terse", Scope: s.ctxAxes})
	_, started, err := s.handleRequestStartWork(context.Background(), nil, RequestStartWorkInput{RequestID: created.RequestID})
	if err != nil {
		t.Fatal(err)
	}
	_, progressed, err := s.handleRequestProgress(context.Background(), nil, RequestProgressInput{
		RequestID: created.RequestID, Action: "criterion_met", CriterionID: created.Criteria[0].ID,
		EvidenceKind: "test", EvidenceRef: "TestMutationsAnswerWithoutTheWholeRequest",
	})
	if err != nil {
		t.Fatal(err)
	}
	if progressed.Criterion == nil || progressed.Criterion.State != "met" || progressed.OpenCriteria != 2 {
		t.Fatalf("progress = %+v, want the criterion state and the remaining count", progressed)
	}

	for name, out := range map[string]any{
		"create": created, "start_work": started, "progress": progressed,
	} {
		raw, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "why this matters") {
			t.Errorf("%s response repeats the request description", name)
		}
		// The budget is per call, not per session: an agent marking ten
		// criteria absorbs it ten times. Shared with the eval suite so the
		// contract has one number, measured here against real output rather
		// than against a figure written into a fixture.
		if len(raw) > requestledger.MaxMutationResponseBytes {
			t.Errorf("%s response is %d bytes, over the %d-byte budget: %s",
				name, len(raw), requestledger.MaxMutationResponseBytes, raw)
		}
	}
}

func TestRequestGetConcisePreservesParagraphsAndNamesOmissions(t *testing.T) {
	c, st := newTestClient(t)
	detail, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{
		Type: "change", Title: "lesbare Antwort", Description: "Erster Absatz.\n\n- Zweiter Absatz.",
		Scope: scope.Axes{Project: "github.com/x/y"},
	}, Criteria: []string{"Kriterium mit Absatz"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetRequestCriterion(detail.Criteria[0].ID, "met", requestdomain.Evidence{Kind: "test", Ref: "evidence-ref"}); err != nil {
		t.Fatal(err)
	}

	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}}
	result, _, err := s.handleRequestGet(context.Background(), nil, RequestGetInput{RequestID: detail.Request.ID})
	if err != nil {
		t.Fatal(err)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "Erster Absatz.\n\n- Zweiter Absatz.") {
		t.Fatalf("request description was flattened or escaped: %q", text)
	}
	if strings.Contains(text, "evidence-ref") {
		t.Fatalf("concise response contains omitted criterion evidence: %q", text)
	}
	if !strings.Contains(text, "concise omits activity, older work, and criterion evidence") {
		t.Fatalf("concise omissions are not named: %q", text)
	}
}

// distilledWish legt einen Ledger-Eintrag an, wie ihn der Lastenheft-Modus
// erzeugt: aus einer Nutzernachricht, mit dem Zitat als Beleg.
func distilledWish(t *testing.T, st *store.Store, ax scope.Axes, external, said, quote string) {
	t.Helper()
	sid, err := st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: external, Scope: ax})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendChunks(sid, []store.Chunk{{Seq: 1, Role: "user", Text: said, Raw: `{}`}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApplyRequestDistillation(sid, "d-"+external, "req-v1", ax, []store.DistilledRequest{{
		Type: "feature", Title: "CSV-Export", Body: "Der Nutzer möchte exportieren können.",
		Quote: quote, ChunkSeq: 1}}); err != nil {
		t.Fatal(err)
	}
}

// Wer einen destillierten Eintrag beurteilen soll, muss die Sätze sehen, aus
// denen er entstand. Ohne sie kostet jede Beurteilung einen Griff ins
// Transkript — und der Betreiber hat grosszügiges Aufnehmen nur unter der
// Bedingung erlaubt, dass sich ein zweifelhafter Eintrag in Sekunden beurteilen
// lässt.
func TestRequestGetShowsTheWordsBehindADistilledEntry(t *testing.T) {
	c, st := newTestClient(t)
	ax := scope.Axes{Project: "github.com/x/y"}
	distilledWish(t, st, ax, "a", "und exportieren als csv wär auch nice", "exportieren als csv")
	s := &Server{client: c, ctxAxes: ax}
	_, found, err := s.handleRequestSearch(context.Background(), nil, RequestSearchInput{})
	if err != nil || len(found.Results) != 1 {
		t.Fatalf("ledger = %+v err=%v", found.Results, err)
	}
	detailed, _, err := s.handleRequestGet(context.Background(), nil,
		RequestGetInput{RequestID: found.Results[0].ID, ResponseFormat: "detailed"})
	if err != nil {
		t.Fatal(err)
	}
	text := detailed.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "exportieren als csv") {
		t.Errorf("the detail omits what was actually said:\n%s", text)
	}
}

// Ein Wunsch aus zwei Sessions ist etwas anderes als einer, der einmal nebenbei
// fiel. Die Zahl gehört in die Liste, weil dort entschieden wird, was man
// überhaupt aufmacht.
func TestTheRequestListCarriesTheSightingCount(t *testing.T) {
	c, st := newTestClient(t)
	ax := scope.Axes{Project: "github.com/x/y"}
	distilledWish(t, st, ax, "a", "export wär nice", "export")
	distilledWish(t, st, ax, "b", "wann kommt der export", "export")
	s := &Server{client: c, ctxAxes: ax}
	_, found, err := s.handleRequestSearch(context.Background(), nil, RequestSearchInput{})
	if err != nil || len(found.Results) != 1 {
		t.Fatalf("ledger = %+v err=%v", found.Results, err)
	}
	if found.Results[0].Sightings != 2 {
		t.Errorf("list item = %+v, want 2 sightings", found.Results[0])
	}
}

func TestRequestGetDetailedIsLargerAndIncludesEvidence(t *testing.T) {
	c, st := newTestClient(t)
	detail, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{
		Type: "bug", Title: "gewachsener Auftrag", Description: strings.Repeat("Beschreibung. ", 80),
		Scope: scope.Axes{Project: "github.com/x/y"},
	}, Criteria: []string{"erstes", "zweites", "drittes"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, criterion := range detail.Criteria {
		if err := c.SetRequestCriterion(criterion.ID, "met", requestdomain.Evidence{Kind: "test", Ref: strings.Repeat("evidence-", 20)}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}}
	concise, _, err := s.handleRequestGet(context.Background(), nil, RequestGetInput{RequestID: detail.Request.ID, ResponseFormat: "concise"})
	if err != nil {
		t.Fatal(err)
	}
	detailed, _, err := s.handleRequestGet(context.Background(), nil, RequestGetInput{RequestID: detail.Request.ID, ResponseFormat: "detailed"})
	if err != nil {
		t.Fatal(err)
	}
	conciseText := concise.Content[0].(*mcp.TextContent).Text
	detailedText := detailed.Content[0].(*mcp.TextContent).Text
	if len([]byte(conciseText)) >= len([]byte(detailedText)) {
		t.Fatalf("concise response is not smaller: concise=%d detailed=%d", len(conciseText), len(detailedText))
	}
	if !strings.Contains(detailedText, "evidence-") {
		t.Fatalf("detailed response omits criterion evidence: %q", detailedText)
	}
}
