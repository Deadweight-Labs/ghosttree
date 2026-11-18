package web

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/aiproxy"
	"github.com/Deadweight-Labs/ghosttree/internal/secretbox"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Usability run 5, WP-R5-5 (REQ-707): the connection form, the release of the
// server's connections, the test of a connection and the policy.

// rowsForm is a connection form with model rows.
func rowsForm(name, format, base, key string, rows [][3]string) url.Values {
	f := url.Values{"name": {name}, "format": {format}, "base_url": {base}, "key": {key}, "cap": {"20"}}
	for _, r := range rows {
		f.Add("model_name", r[0])
		f.Add("model_in", r[1])
		f.Add("model_out", r[2])
	}
	return f
}

var oneModel = [][3]string{{"modell-x", "3", "15"}, {"", "", ""}}

// AC-707.1 (S05): a form with an address that is not allowed stays on the same
// page, opened at the section, keeps what was typed, names the reason at the
// field, and does not hand the key back.
func TestRefusedConnectionFormStaysWithItsInputAndNamesTheReasonAtTheField(t *testing.T) {
	e := aiWeb(t)
	status, body := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/connection", rowsForm("eigene-anthropic", "anthropic", "http://api.beispiel.test/v1", aiKeyA, oneModel))
	if status != http.StatusBadRequest {
		t.Fatalf("status %d", status)
	}
	mustContain(t, "refused form", body, `id="ai"`, `data-open="ai"`, `<details class="ainew" open>`,
		`value="eigene-anthropic"`, `value="http://api.beispiel.test/v1"`, `value="modell-x"`, `value="3"`, `value="15"`, `value="20"`,
		"Only https addresses on the public internet", `aria-invalid="true"`, "Enter the key again")
	mustNotContain(t, "refused form", body, "Check the form", `class="flash err"`)
	noKeys(t, "the refused form", body)
	if key := regexp.MustCompile(`<input name="key"[^>]*>`).FindString(body); key == "" || strings.Contains(key, "value=") {
		t.Fatalf("the key field comes back empty: %q", key)
	}
	if conns, _ := e.St.AIConnectionsFor(e.org.ID); len(conns) != 0 {
		t.Fatalf("a refused form stored %d connections", len(conns))
	}
	// The same form with an https address passes.
	if status, body := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/connection", rowsForm("eigene-anthropic", "anthropic", "https://api.beispiel.test/v1", aiKeyA, oneModel)); status != http.StatusSeeOther {
		t.Fatalf("with https: %d %s", status, body)
	}
}

// Every reason stands at its own field, all at once: name, models with the
// row, cap.
func TestRefusedConnectionFormShowsEveryReasonAtItsField(t *testing.T) {
	e := aiWeb(t)
	f := rowsForm("", "openai", "https://api.example.com", aiKeyB, [][3]string{{"gpt-a", "1", "2"}, {"gpt-b", "teuer", "2"}})
	f.Set("cap", "viel")
	status, body := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/connection", f)
	if status != http.StatusBadRequest {
		t.Fatalf("status %d", status)
	}
	mustContain(t, "refused form", body, "A name is missing.", "Both prices are numbers from 0 to 10000", "Some rows need a look.", "A number from 0 to 1,000,000",
		`value="teuer"`, `value="gpt-a"`, `value="viel"`)
	noKeys(t, "the refused form", body)
	// The old way of writing "modell-x, 3, 15" no longer yields a model named "modell-x,".
	f = rowsForm("ok", "openai", "https://api.example.com", aiKeyB, [][3]string{{"modell-x,", "3", "15"}})
	if status, _ := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/connection", f); status != http.StatusSeeOther {
		t.Fatalf("a model name with a comma is no refusal of the form itself: %d", status)
	}
}

// A name that exists is a reason at the name field; the key is asked for again.
func TestRefusedNameIsShownAtTheNameField(t *testing.T) {
	e := aiWeb(t)
	if status, body := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/connection", rowsForm("doppelt", "openai", "https://api.example.com", aiKeyA, oneModel)); status != http.StatusSeeOther {
		t.Fatalf("first: %d %s", status, body)
	}
	status, body := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/connection", rowsForm("doppelt", "openai", "https://api.example.com", aiKeyB, oneModel))
	if status != http.StatusConflict {
		t.Fatalf("status %d", status)
	}
	mustContain(t, "refused name", body, "A connection of that name exists already.", `value="doppelt"`, "Enter the key again")
	noKeys(t, "the refused name", body)
}

// The form of an existing connection comes back opened with the typed values,
// and a new address without the key is a reason at the key.
func TestRefusedEditOpensTheFormOfThatConnection(t *testing.T) {
	e := aiWeb(t)
	if status, _ := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/connection", rowsForm("bleibt", "openai", "https://api.example.com", aiKeyA, oneModel)); status != http.StatusSeeOther {
		t.Fatal("add")
	}
	conns, _ := e.St.AIConnectionsFor(e.org.ID)
	f := rowsForm("bleibt", "openai", "https://api.neu.example.com", "", oneModel)
	f.Set("id", fmt.Sprint(conns[0].ID))
	status, body := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/connection", f)
	if status != http.StatusBadRequest {
		t.Fatalf("status %d", status)
	}
	mustContain(t, "refused edit", body, `<details class="aiedit" open>`, `value="https://api.neu.example.com"`, "A new address or format needs the key again")
	f.Set("base_url", "http://api.neu.example.com")
	f.Set("key", aiKeyB)
	status, body = e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/connection", f)
	if status != http.StatusBadRequest {
		t.Fatalf("status %d", status)
	}
	mustContain(t, "refused edit with a key", body, `<details class="aiedit" open>`, "Only https addresses on the public internet", "Enter the key again")
	noKeys(t, "the refused edit", body)
	// The stored connection is untouched.
	after, _ := e.St.AIConnectionsFor(e.org.ID)
	if after[0].BaseURL != "https://api.example.com" {
		t.Fatalf("address after the refusals: %s", after[0].BaseURL)
	}
}

// The model rows are stored as rows and shown as rows again; the old text
// field still works for the API's sake.
func TestModelRowsAreStoredAndShownAsRows(t *testing.T) {
	e := aiWeb(t)
	rows := [][3]string{{"claude-haiku-5-5", "0,10", "0.50"}, {"claude-opus-5-5", "5", "25"}, {"", "", ""}}
	if status, body := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/connection", rowsForm("zeilen", "anthropic", "https://api.example.com", aiKeyTeam, rows)); status != http.StatusSeeOther {
		t.Fatalf("add: %d %s", status, body)
	}
	conns, _ := e.St.AIConnectionsFor(e.org.ID)
	if len(conns[0].Models) != 2 || conns[0].Models[0].InUSD != 0.10 || conns[0].Models[1].Model != "claude-opus-5-5" || conns[0].Models[1].OutUSD != 25 {
		t.Fatalf("models: %+v", conns[0].Models)
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/orgs?org=alpha")
	mustContain(t, "edit form", page, `name="model_name"`, `value="claude-opus-5-5"`, `value="0.1"`, `data-addrow`, `<template data-rowtpl>`)
}

// The server's connection takes https, or http to this machine (a local model
// server), and says so at the field.
func TestServerConnectionAddressRules(t *testing.T) {
	e := aiWeb(t)
	f := rowsForm("lokal", "openai", "http://api.beispiel.test/v1", aiKeyA, oneModel)
	f.Set("scope", "server")
	status, body := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/connection", f)
	if status != http.StatusBadRequest {
		t.Fatalf("status %d", status)
	}
	mustContain(t, "refused server form", body, `data-open="ai-server"`, "https, or http only to this machine", `value="http://api.beispiel.test/v1"`)
	f.Set("base_url", "http://127.0.0.1:11434/v1")
	if status, body := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/connection", f); status != http.StatusSeeOther {
		t.Fatalf("a local model server: %d %s", status, body)
	}
}

// AC-707.2 (S06): one cap per organization; the page of the organization says
// what the server released to it and with which cap; the button says what it
// does; nothing is released twice in the choice; no header over an empty table.
func TestServerReleaseHasOneCapPerOrganizationAndShowsItOnTheOrgPage(t *testing.T) {
	e := aiWeb(t)
	if _, err := e.St.CreateOrg("person:1", "Beta", "beta"); err != nil {
		t.Fatal(err)
	}
	server := rowsForm("standard", "anthropic", "https://api.example.com", aiKeyTeam, oneModel)
	server.Set("scope", "server")
	if status, body := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/connection", server); status != http.StatusSeeOther {
		t.Fatalf("server connection: %d %s", status, body)
	}
	if status, _ := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/release", url.Values{"mode": {"selected"}, "cap": {"3"}}); status != http.StatusSeeOther {
		t.Fatal("release")
	}
	_, admin := fetchPage(t, e.Owner, e.Base+"/ui/orgs?org=alpha")
	mustContain(t, "release before any grant", admin, "Standard cap per organization and month", "No organization released yet", `data-autosubmit`)
	mustNotContain(t, "release before any grant", admin, `class="days aitab aigrants"`, "Cap per organization and month<", ">Release<")
	// Released without a cap of its own: the standard cap counts.
	if status, _ := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/grant", url.Values{"target": {"alpha"}}); status != http.StatusSeeOther {
		t.Fatal("grant")
	}
	_, org := fetchPage(t, e.Owner, e.Base+"/ui/orgs?org=alpha")
	mustContain(t, "page of the organization", org, "Released by the server", "3.00 $ per month", "Released by the server, cap 3.00 $ per month")
	_, admin = fetchPage(t, e.Owner, e.Base+"/ui/orgs?org=alpha")
	mustContain(t, "release with a grant", admin, `class="days aitab aigrants"`, `placeholder="Standard 3.00 $"`, "0.00 $ of 3.00 $ this month")
	// Its own cap replaces the standard one, in the row and on the page of the organization.
	if status, _ := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/grant", url.Values{"target": {"alpha"}, "cap": {"2"}}); status != http.StatusSeeOther {
		t.Fatal("grant with a cap")
	}
	_, org = fetchPage(t, e.Owner, e.Base+"/ui/orgs?org=alpha")
	mustContain(t, "page of the organization", org, "2.00 $ per month")
	_, admin = fetchPage(t, e.Owner, e.Base+"/ui/orgs?org=alpha")
	mustContain(t, "release row", admin, `value="2"`, "0.00 $ of 2.00 $ this month")
	// An organization that has its release is not offered again.
	choice := regexp.MustCompile(`(?s)<select class="gfield" name="target">.*?</select>`).FindString(admin)
	if strings.Contains(choice, `value="alpha"`) || !strings.Contains(choice, `value="beta"`) {
		t.Fatalf("an organization that is released is offered in the choice again, or one that is not is missing: %s", choice)
	}
	mustContain(t, "button", admin, ">Make available<")
	// Empty tables carry no header: the alias table of an organization without aliases.
	_, aliasesOnly := fetchPage(t, e.Owner, e.Base+"/ui/orgs?org=alpha")
	if strings.Contains(aliasesOnly, "<th>Alias</th>") {
		t.Fatal("the alias table has a header without a row")
	}
	mustContain(t, "empty alias table", aliasesOnly, "No alias")
}

// lateTester hands the test of a connection to a proxy that exists only once
// the server does.
type lateTester struct{ p atomic.Pointer[aiproxy.Proxy] }

func (l *lateTester) TestConnection(ctx context.Context, actor string, id int64) (aiproxy.TestResult, error) {
	return l.p.Load().TestConnection(ctx, actor, id)
}

// aiWebTesting is aiWeb with the button "Test connection", whose calls go to
// the fake providers by their address.
func aiWebTesting(t *testing.T, timeout time.Duration) (aiWebEnv, map[string]*http.Client) {
	t.Helper()
	box, _ := secretbox.New(bytes.Repeat([]byte{3}, 32))
	late := &lateTester{}
	old := testWebOptions
	testWebOptions = append(append([]Option{}, old...), WithAIConnections(box), WithAITester(late), WithLogger(slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))))
	e := shellWebAll(t)
	testWebOptions = old
	org, _ := e.St.OrgByRef("alpha")
	clients := map[string]*http.Client{}
	late.p.Store(&aiproxy.Proxy{Store: e.St, Box: box, TestGap: -1, TestTimeout: timeout,
		Upstream: func(c store.AIConnection, u *url.URL) (*http.Client, error) { return clients[c.BaseURL], nil }})
	return aiWebEnv{shellEnv: e, box: box, logs: &bytes.Buffer{}, org: org}, clients
}

// provider starts a fake provider that answers with status and body.
func provider(t *testing.T, clients map[string]*http.Client, status int, body string, delay time.Duration) string {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	clients[srv.URL] = srv.Client()
	return srv.URL
}

// AC-707.3: "Test connection" on the card of a connection: success and each
// failure in words, only for the keeper, the key never in the answer.
func TestTestConnectionButtonShowsTheOutcomeInWords(t *testing.T) {
	e, clients := aiWebTesting(t, 150*time.Millisecond)
	ok := `{"type":"message","content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":12,"output_tokens":2}}`
	cases := []struct {
		name         string
		status       int
		body         string
		delay        time.Duration
		want, detail string
	}{
		{"fein", 200, ok, 0, "The connection answers (model modell-x", ""},
		{"abgelehnt", 401, `{"error":{"message":"invalid x-api-key"}}`, 0, "The provider refuses the key (HTTP 401). Enter the key again.", "invalid x-api-key"},
		{"unbekannt", 404, "404 page not found", 0, "does not know this address or the model modell-x (HTTP 404)", "404 page not found"},
		{"langsam", 200, ok, 2 * time.Second, "No answer from", ""},
	}
	for _, c := range cases {
		base := provider(t, clients, c.status, c.body, c.delay)
		if status, body := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/connection", rowsForm(c.name, "anthropic", base, aiKeyTeam, oneModel)); status != http.StatusSeeOther {
			t.Fatalf("%s: add: %d %s", c.name, status, body)
		}
	}
	conns, _ := e.St.AIConnectionsFor(e.org.ID)
	ids := map[string]int64{}
	for _, c := range conns {
		ids[c.Name] = c.ID
	}
	for _, c := range cases {
		status, _ := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/connection/test", url.Values{"id": {fmt.Sprint(ids[c.name])}})
		if status != http.StatusSeeOther {
			t.Fatalf("%s: test: %d", c.name, status)
		}
		_, page := fetchPage(t, e.Owner, e.Base+"/ui/orgs?org=alpha")
		card := page[strings.Index(page, fmt.Sprintf(`id="ai-%d"`, ids[c.name])):]
		card = card[:strings.Index(card, `class="acts"`)]
		mustContain(t, c.name+" card", card, `class="aitest `, c.want)
		if c.detail != "" {
			mustContain(t, c.name+" card", card, c.detail)
		}
		noKeys(t, c.name+" page", page)
	}
	// The successful test is booked as a call with its cost.
	rep, _ := e.St.AICostReportOf(e.org.ID, store.AIMonthStart())
	found := false
	for _, w := range rep.ByWorkflow {
		found = found || (w.Key == store.AITestWorkflow && w.Calls == 2)
	}
	if !found {
		t.Fatalf("the tests of the connection that answered and the one that did not in time are booked: %+v", rep.ByWorkflow)
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/orgs?org=alpha")
	mustContain(t, "costs", page, "Connection test")
	// A member sees no button and cannot test; neither can the owner of another organization's connection.
	_, member := fetchPage(t, e.Member, e.Base+"/ui/orgs?org=alpha")
	mustNotContain(t, "member page", member, "/ui/orgs/ai/connection/test", `class="aitest `)
	if status, _ := e.aiPost(t, e.Member, "alpha", "/ui/orgs/ai/connection/test", url.Values{"id": {fmt.Sprint(ids["fein"])}}); status != http.StatusForbidden && status != http.StatusBadRequest {
		t.Fatalf("a member tests: %d", status)
	}
	if status, _ := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/connection/test", url.Values{"id": {"99999"}}); status != http.StatusBadRequest {
		t.Fatalf("a connection that does not exist: %d", status)
	}
}

// The server's connection is tested by the admin alone, over the same way.
func TestServerConnectionIsTestedByTheAdmin(t *testing.T) {
	e, clients := aiWebTesting(t, time.Second)
	base := provider(t, clients, 200, `{"type":"message","usage":{"input_tokens":5,"output_tokens":1}}`, 0)
	f := rowsForm("standard", "anthropic", base, aiKeyTeam, oneModel)
	f.Set("scope", "server")
	if status, body := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/connection", f); status != http.StatusSeeOther {
		t.Fatalf("server connection: %d %s", status, body)
	}
	servers, _ := e.St.ServerAIConnections()
	form := url.Values{"id": {fmt.Sprint(servers[0].ID)}, "scope": {"server"}}
	if status, _ := e.aiPost(t, e.Member, "alpha", "/ui/orgs/ai/connection/test", form); status != http.StatusForbidden {
		t.Fatalf("a member tests the server's connection: %d", status)
	}
	if status, _ := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/connection/test", form); status != http.StatusSeeOther {
		t.Fatal("the admin's test")
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/orgs?org=alpha")
	mustContain(t, "server section", page, "The connection answers")
}

// S10: the policy saves with its own word, in cards with a heading, names the
// workflows each choice concerns and warns about applying without review.
func TestPolicyShowsItsOwnToastCardsAndTheWorkflowsItConcerns(t *testing.T) {
	e := aiWeb(t)
	text := "---\non: pr.merged\ninputs: [pr.title]\nuses: ghosttree/echo\n---\nAchte auf Migrationen.\n"
	if _, err := e.St.SaveWorkflowDef("person:1", store.WorkflowDefSave{Project: shellProject, Name: "idee-abgleich", Text: text, Event: "pr.merged", Uses: "ghosttree/echo"}); err != nil {
		t.Fatal(err)
	}
	status, _ := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/workflow-policy", url.Values{"auto_kind": {"knowledge"}})
	if status != http.StatusSeeOther {
		t.Fatalf("policy: %d", status)
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/orgs?org=alpha&notice=workflow_policy")
	mustContain(t, "settings", page, "Policy saved", `<h3 class="aih">Policy</h3>`, `<h3 class="aih">Retention</h3>`, "Forbidden abilities", "Forbidden results", "Apply without review",
		"Blocks with an instruction of the team", "affects: idee-abgleich", `class="wfwarn on"`, "can only be undone afterwards")
	mustNotContain(t, "settings", page, "Retention saved", "stage 2", "stage 3", "research on the net")
	_, saved := fetchPage(t, e.Owner, e.Base+"/ui/orgs?org=alpha&notice=workflows")
	mustContain(t, "retention notice", saved, "Retention saved")
}

// N-B (counter-check of run 5): the test of the server's connection goes back
// to an address that names the server's own row. The number alone ("#ai-1")
// also names the row of the same connection in the organization's list once the
// server released it, so the page opened the other section and the outcome
// stood in the hidden one.
func TestServerConnectionTestReturnsToTheServerRow(t *testing.T) {
	e, clients := aiWebTesting(t, time.Second)
	base := provider(t, clients, 200, `{"type":"message","usage":{"input_tokens":5,"output_tokens":1}}`, 0)
	f := rowsForm("standard", "anthropic", base, aiKeyTeam, oneModel)
	f.Set("scope", "server")
	if status, body := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/connection", f); status != http.StatusSeeOther {
		t.Fatalf("server connection: %d %s", status, body)
	}
	servers, _ := e.St.ServerAIConnections()
	id := servers[0].ID
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/orgs?org=alpha")
	form := url.Values{"id": {fmt.Sprint(id)}, "scope": {"server"}, "org": {"alpha"}, "csrf_token": {csrfOn(t, page)}}
	resp := sameOriginPostForm(t, e.Owner, e.Base+"/ui/orgs/ai/connection/test", form)
	resp.Body.Close()
	want := fmt.Sprintf("#ai-server-%d", id)
	if loc := resp.Header.Get("Location"); !strings.HasSuffix(loc, want) {
		t.Fatalf("redirect %q does not end in %s", loc, want)
	}
	_, page = fetchPage(t, e.Owner, e.Base+"/ui/orgs?org=alpha")
	mustContain(t, "server row", page, fmt.Sprintf(`id="ai-server-%d"`, id), "The connection answers")
	mustNotContain(t, "server row", page, fmt.Sprintf(`id="ai-%d"`, id))
}
