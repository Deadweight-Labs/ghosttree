package server

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/deliverylog"
	"github.com/Deadweight-Labs/ghosttree/internal/profile"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const leanProject = "github.com/x/y"

// leanFixture is a project with what a working repository has: binding
// instructions, many pitfalls of the project and of the machine, decisions and
// notes, open requests.
func leanFixture(t *testing.T, opts ...Option) (*httptest.Server, string, *store.Store, []int64) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("test")
	proj := scope.Axes{Project: leanProject}
	var instructionIDs []int64
	for i := range 3 {
		id, err := st.InsertKnowledge(store.Knowledge{Type: "instruction", Title: fmt.Sprintf("rule %d", i),
			Body: "Never commit generated files; keep the order of the fields stable; ask before you delete a table. " + strings.Repeat("x", 60), Scope: proj})
		if err != nil {
			t.Fatal(err)
		}
		instructionIDs = append(instructionIDs, id)
	}
	for i := range 30 {
		if _, err := st.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: fmt.Sprintf("project pitfall %d", i),
			Body: strings.Repeat("The migration locks the table while it runs. ", 8), Scope: proj, Confidence: "trusted"}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: fmt.Sprintf("machine pitfall %d", i),
			Body: "The disk fills up on the build host.", Scope: scope.Axes{Machine: "builder"}, Confidence: "trusted"}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 8 {
		if _, err := st.InsertKnowledge(store.Knowledge{Type: "decision", Title: fmt.Sprintf("decision %d", i), Body: "why", Scope: proj, Confidence: "trusted"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "open work", Scope: proj}}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st, opts...))
	t.Cleanup(srv.Close)
	return srv, token, st, instructionIDs
}

func leanBootstrapBody(t *testing.T, srv *httptest.Server, token, query string) string {
	t.Helper()
	resp := req(t, "GET", srv.URL+"/api/context/bootstrap?project="+leanProject+"&machine=builder"+query, token, nil)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("bootstrap = %d: %s", resp.StatusCode, raw)
	}
	return string(raw)
}

// REQ-643: the briefing of a lean session is at most 1,500 characters, carries
// every binding instruction, and leaves out the work ledger and everything
// else the full briefing adds; without the profile the briefing is the one it
// always was.
func TestLeanBootstrapIsShortAndKeepsTheInstructions(t *testing.T) {
	srv, token, _, _ := leanFixture(t)
	full := leanBootstrapBody(t, srv, token, "")
	lean := leanBootstrapBody(t, srv, token, "&profile=lean")

	if n := deliverylog.Runes(lean); n > profile.LeanStartChars {
		t.Errorf("a lean briefing has %d characters, at most %d:\n%s", n, profile.LeanStartChars, lean)
	}
	for _, rule := range []string{"rule 0", "rule 1", "rule 2"} {
		if !strings.Contains(lean, rule) {
			t.Errorf("the lean briefing lacks the binding instruction %q:\n%s", rule, lean)
		}
	}
	if strings.Count(lean, "- [") < 4 || !strings.Contains(lean, "project pitfall") {
		t.Errorf("the lean briefing carries no pitfall of the project:\n%s", lean)
	}
	for _, gone := range []string{"machine pitfall", "decision ", "Work ledger", "open request", "Also in scope"} {
		if strings.Contains(lean, gone) {
			t.Errorf("the lean briefing carries %q:\n%s", gone, lean)
		}
	}
	if n := strings.Count(lean, "project pitfall"); n > 3 {
		t.Errorf("%d pitfalls in a lean briefing, at most 3", n)
	}
	for _, name := range profile.Lean.Deferred() {
		if !strings.Contains(lean, name) {
			t.Errorf("the lean briefing does not name the tool %s it does not carry up front", name)
		}
	}
	// The way back to the rest is named.
	for _, want := range []string{"context_search", ".ghosttree/INDEX.md"} {
		if !strings.Contains(lean, want) {
			t.Errorf("the lean briefing does not point to %s", want)
		}
	}

	// Without the parameter, and with a value that names no profile, the
	// briefing is the full one.
	for _, query := range []string{"&profile=full", "&profile=", "&profile=typo"} {
		if got := leanBootstrapBody(t, srv, token, query); got != full {
			t.Errorf("profile query %q changed the briefing", query)
		}
	}
	if !strings.Contains(full, "1 open request") || !strings.Contains(full, "Also in scope") || !strings.Contains(full, "project pitfall") {
		t.Fatalf("the full briefing lost its sections:\n%s", full)
	}
	t.Logf("briefing: full %d characters, lean %d characters", deliverylog.Runes(full), deliverylog.Runes(lean))
}

// Instructions bind, so a project whose instructions alone are longer than the
// budget gets all of them and no pitfall.
func TestLeanBootstrapNeverCutsAnInstruction(t *testing.T) {
	long := strings.Repeat("y", 1800)
	out, shown, _ := renderLeanBootstrap([]store.Knowledge{
		{ID: 1, Type: "instruction", Title: "long rule", Body: long, Scope: scope.Axes{Project: leanProject}},
		{ID: 2, Type: "pitfall", Title: "a pitfall", Body: "b", Scope: scope.Axes{Project: leanProject}, Confidence: "trusted"},
	}, "")
	if !strings.Contains(out, long) {
		t.Errorf("an instruction was cut")
	}
	if strings.Contains(out, "a pitfall") || len(shown) != 1 || shown[0] != 1 {
		t.Errorf("a pitfall was added past the budget; shown %v", shown)
	}
}

// Unreviewed entries do not bind, in a lean briefing as in the full one, and a
// project without knowledge still learns where the rest is.
func TestLeanBootstrapIgnoresStagedAndAnswersForAnEmptyProject(t *testing.T) {
	out, shown, _ := renderLeanBootstrap([]store.Knowledge{
		{ID: 1, Type: "instruction", Title: "unreviewed rule", Body: "b", Confidence: "staged"},
		{ID: 2, Type: "pitfall", Title: "unreviewed pitfall", Body: "b", Scope: scope.Axes{Project: leanProject}, Confidence: "staged"},
	}, "")
	if strings.Contains(out, "unreviewed") || len(shown) != 0 {
		t.Errorf("a staged entry went out: %s", out)
	}
	if !strings.Contains(out, "context_search") {
		t.Errorf("an empty lean briefing does not say where the rest is: %s", out)
	}
}

// The delivery memory and the log know what the lean briefing carried: only
// those entries count as delivered, so relevance delivery still offers the
// pitfalls the lean briefing left out.
func TestLeanBootstrapRemembersOnlyWhatItDelivered(t *testing.T) {
	srv, token, st, _ := leanFixture(t)
	resp := req(t, "GET", srv.URL+"/api/context/bootstrap?project="+leanProject+"&profile=lean&session=lean-sess", token, nil)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	parts := deliverylog.ParseHeader(resp.Header.Get(deliverylog.HeaderName))
	sum, ids := 0, 0
	for _, p := range parts {
		if p.Via != deliverylog.ViaBootstrap || p.Kind != deliverylog.KindKnowledge || p.Chars <= 0 {
			t.Errorf("part %+v", p)
		}
		sum += p.Chars
		ids++
	}
	if ids != 3+strings.Count(string(body), "project pitfall") || sum >= deliverylog.Runes(string(body)) {
		t.Errorf("%d parts of %d characters for a body of %d", ids, sum, deliverylog.Runes(string(body)))
	}
	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM context_deliveries WHERE session_key='lean-sess' AND knowledge_id != 0`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != ids {
		t.Errorf("%d deliveries remembered, %d announced", n, ids)
	}
}

// REQ-643 review: a lean briefing carries the hint that this machine's ctx is
// too old, as the full one does, within the length of a lean briefing, and is
// as it was for a client that fits or sends no version.
func TestLeanBootstrapCarriesTheVersionNote(t *testing.T) {
	srv, token, _, _ := leanFixture(t, WithBuildVersion("0.1.0-dev+b0cd1b9"))
	get := func(query string, hdr map[string]string) string {
		t.Helper()
		rq, _ := http.NewRequest("GET", srv.URL+"/api/context/bootstrap?project="+leanProject+"&machine=builder"+query, nil)
		rq.Header.Set("Authorization", "Bearer "+token)
		for k, v := range hdr {
			rq.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(rq)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return string(raw)
	}
	stale := map[string]string{"X-Ctx-Version": "0.1.0-dev+1c48c74", "X-Ctx-Protocol": "1"}
	fits := map[string]string{"X-Ctx-Version": "0.1.0-dev+b0cd1b9", "X-Ctx-Protocol": "1"}

	lean := get("&profile=lean", stale)
	if !strings.Contains(lean, "ctx update") || !strings.Contains(lean, "0.1.0-dev+b0cd1b9") || !strings.Contains(lean, "0.1.0-dev+1c48c74") {
		t.Fatalf("the lean briefing carries no update hint:\n%s", lean)
	}
	// The full briefing says the same thing in the same words.
	if full := get("", stale); !strings.Contains(full, "> ghosttree: This server runs ctx 0.1.0-dev+b0cd1b9") ||
		!strings.Contains(lean, "> ghosttree: This server runs ctx 0.1.0-dev+b0cd1b9") {
		t.Errorf("the note differs between the profiles:\n%s\n--\n%s", full, lean)
	}
	if n := deliverylog.Runes(lean); n > profile.LeanStartChars {
		t.Errorf("a lean briefing with the hint has %d characters, at most %d:\n%s", n, profile.LeanStartChars, lean)
	}
	for _, rule := range []string{"rule 0", "rule 1", "rule 2"} {
		if !strings.Contains(lean, rule) {
			t.Errorf("the hint pushed out the binding instruction %q", rule)
		}
	}
	if !strings.Contains(lean, "project pitfall") || !strings.Contains(lean, "context_search") {
		t.Errorf("the hint pushed out the pitfalls or the way to the rest:\n%s", lean)
	}
	// No version, or one that fits: no hint, and the briefing is the same.
	plain := get("&profile=lean", nil)
	if strings.Contains(plain, "ctx update") || get("&profile=lean", fits) != plain {
		t.Errorf("a client that fits got a hint:\n%s", get("&profile=lean", fits))
	}
}
