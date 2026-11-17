package web

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/aiproxy"
	"github.com/Deadweight-Labs/ghosttree/internal/secretbox"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/Deadweight-Labs/ghosttree/internal/workflow"
)

const (
	aiKeyA    = "sk-proj-AAAAsecretOPENAIkeyA-1111"
	aiKeyB    = "sk-proj-BBBBsecretOPENAIkeyB-2222"
	aiKeyTeam = "sk-ant-api03-TEAMsecretANTHROPIC-3333"
	aiKeyAnna = "sk-anna-PROFILEsecretKEY-4444"
)

var aiKeys = []string{aiKeyA, aiKeyB, aiKeyTeam, aiKeyAnna}

type aiWebEnv struct {
	shellEnv
	box  *secretbox.Box
	logs *bytes.Buffer
	org  store.Org
}

// aiWeb is the shell (alice owner of alpha and server admin, anna member)
// with a server key and a log the test can read.
func aiWeb(t *testing.T) aiWebEnv {
	t.Helper()
	box, _ := secretbox.New(bytes.Repeat([]byte{3}, 32))
	logs := &bytes.Buffer{}
	old := testWebOptions
	testWebOptions = append(append([]Option{}, old...), WithAIConnections(box), WithLogger(slog.New(slog.NewJSONHandler(logs, nil))))
	e := shellWebAll(t)
	testWebOptions = old
	org, err := e.St.OrgByRef("alpha")
	if err != nil {
		t.Fatal(err)
	}
	return aiWebEnv{shellEnv: e, box: box, logs: logs, org: org}
}

// aiPost sends a form of the settings page of slug and returns status and the
// page it lands on (the redirect is not followed; the body is read).
func (e aiWebEnv) aiPost(t *testing.T, c *http.Client, slug, path string, form url.Values) (int, string) {
	t.Helper()
	_, page := fetchPage(t, c, e.Base+"/ui/orgs?org="+slug)
	form.Set("csrf_token", csrfOn(t, page))
	form.Set("org", slug)
	resp := sameOriginPostForm(t, c, e.Base+path, form)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func noKeys(t *testing.T, where, text string) {
	t.Helper()
	for _, k := range aiKeys {
		if strings.Contains(text, k) || strings.Contains(text, k[3:len(k)-4]) {
			t.Fatalf("%s shows a key (%s…)", where, k[:10])
		}
	}
}

func connForm(name, format, key, models string) url.Values {
	return url.Values{"name": {name}, "format": {format}, "base_url": {"https://api.example.com"}, "key": {key}, "models": {models}, "cap": {"20"}}
}

// AC-694.1 in the browser: the owner adds three connections, the page shows
// the last four characters and never the key, and a member may add none.
func TestOwnerAddsThreeConnectionsAndTheKeyIsNeverShown(t *testing.T) {
	e := aiWeb(t)
	for _, f := range []url.Values{
		connForm("openai-a", "openai", aiKeyA, "gpt-a 1 2"),
		connForm("openai-b", "openai", aiKeyB, "gpt-b 1,5 3"),
		connForm("anthropic-team", "anthropic", aiKeyTeam, "claude-haiku-5-5 0.10 0.50\nclaude-opus-5-5 5 25"),
	} {
		status, body := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/connection", f)
		if status != http.StatusSeeOther {
			t.Fatalf("add %s: %d %s", f.Get("name"), status, body)
		}
		noKeys(t, "the answer to the form", body)
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/orgs?org=alpha&notice=ai")
	mustContain(t, "owner page", page, `id="ai"`, `href="#ai"`, "openai-a", "openai-b", "anthropic-team", "••••1111", "••••2222", "••••3333", "claude-opus-5-5", `type="password"`, "Saved")
	noKeys(t, "the owner's page", page)
	_, member := fetchPage(t, e.Member, e.Base+"/ui/orgs?org=alpha")
	mustContain(t, "member page", member, "anthropic-team")
	mustNotContain(t, "member page", member, "••••", "api.example.com", `action="/ui/orgs/ai/connection"`)
	noKeys(t, "the member's page", member)
	if status, _ := e.aiPost(t, e.Member, "alpha", "/ui/orgs/ai/connection", connForm("mine", "openai", aiKeyA, "m 1 1")); status != http.StatusForbidden {
		t.Fatalf("a member adds a connection: %d", status)
	}
	// A refused form does not hand the key back.
	status, body := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/connection", connForm("openai-a", "openai", aiKeyB, "gpt-a 1 2"))
	if status != http.StatusConflict {
		t.Fatalf("same name: %d", status)
	}
	noKeys(t, "the refusal", body)
	// Editing without a key keeps the key.
	conns, _ := e.St.AIConnectionsFor(e.org.ID)
	f := connForm("openai-a", "openai", "", "gpt-a 1 2\ngpt-a-mini 0.1 0.2")
	f.Set("id", fmt.Sprint(conns[1].ID))
	if status, body := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/connection", f); status != http.StatusSeeOther {
		t.Fatalf("edit: %d %s", status, body)
	}
	sealed, _, _ := e.St.AIConnectionKey(conns[1].ID)
	if key, err := e.box.Open(sealed, store.AIKeyPurpose); err != nil || key != aiKeyA {
		t.Fatalf("key after the edit: %v", err)
	}
	noKeys(t, "the server's log", e.logs.String())
}

// AC-694.2 in the browser: only the admin sees and keeps the server's
// connections; an org sees them once they are released to it.
func TestServerConnectionsAreTheAdminsAndShownOnlyAfterTheRelease(t *testing.T) {
	e := aiWeb(t)
	server := connForm("standard", "anthropic", aiKeyTeam, "claude-haiku-5-5 0.1 0.5")
	server.Set("scope", "server")
	if status, _ := e.aiPost(t, e.Member, "alpha", "/ui/orgs/ai/connection", server); status != http.StatusForbidden {
		t.Fatalf("a member keeps a server connection: %d", status)
	}
	if status, body := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/connection", server); status != http.StatusSeeOther {
		t.Fatalf("admin: %d %s", status, body)
	}
	_, admin := fetchPage(t, e.Owner, e.Base+"/ui/orgs?org=alpha")
	mustContain(t, "admin page", admin, `id="ai-server"`, "••••3333", `name="mode"`)
	noKeys(t, "the admin page", admin)
	_, member := fetchPage(t, e.Member, e.Base+"/ui/orgs?org=alpha")
	mustNotContain(t, "member before the release", member, `id="ai-server"`, "standard")
	if status, _ := e.aiPost(t, e.Member, "alpha", "/ui/orgs/ai/release", url.Values{"mode": {"all"}}); status != http.StatusForbidden {
		t.Fatalf("a member releases: %d", status)
	}
	if status, _ := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/release", url.Values{"mode": {"selected"}}); status != http.StatusSeeOther {
		t.Fatalf("release: %d", status)
	}
	if status, _ := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/grant", url.Values{"target": {"alpha"}, "cap": {"15"}}); status != http.StatusSeeOther {
		t.Fatalf("grant: %d", status)
	}
	_, member = fetchPage(t, e.Member, e.Base+"/ui/orgs?org=alpha")
	mustContain(t, "member after the release", member, "standard", "Server")
	mustNotContain(t, "member after the release", member, `id="ai-server"`, "••••3333")
	_, admin = fetchPage(t, e.Owner, e.Base+"/ui/orgs?org=alpha")
	mustContain(t, "grant row", admin, "0.00 $ of 15.00 $ this month")
}

// AC-694.3 in the browser: anna lends a connection of her profile to alpha;
// alpha's owner sees its name only; after the revocation it is gone.
func TestLentConnectionShowsItsNameOnlyAndCanBeTakenBack(t *testing.T) {
	e := aiWeb(t)
	ns, err := e.St.EnsureUserNamespace("person:2")
	if err != nil {
		t.Fatal(err)
	}
	if status, body := e.aiPost(t, e.Member, ns.Slug, "/ui/orgs/ai/connection", connForm("anna-key", "openai", aiKeyAnna, "gpt-x 1 2")); status != http.StatusSeeOther {
		t.Fatalf("profile connection: %d %s", status, body)
	}
	conns, _ := e.St.AIConnectionsFor(ns.ID)
	_, profile := fetchPage(t, e.Member, e.Base+"/ui/orgs?org="+ns.Slug)
	mustContain(t, "profile", profile, `action="/ui/orgs/ai/lend"`, "Alpha")
	if status, body := e.aiPost(t, e.Member, ns.Slug, "/ui/orgs/ai/lend", url.Values{"id": {fmt.Sprint(conns[0].ID)}, "to": {"alpha"}, "cap": {"4"}}); status != http.StatusSeeOther {
		t.Fatalf("lend: %d %s", status, body)
	}
	_, owner := fetchPage(t, e.Owner, e.Base+"/ui/orgs?org=alpha")
	mustContain(t, "org page", owner, "anna-key", "Lent")
	mustNotContain(t, "org page", owner, "••••4444", "gpt-x", "api.example.com")
	noKeys(t, "the org page", owner)
	_, profile = fetchPage(t, e.Member, e.Base+"/ui/orgs?org="+ns.Slug)
	mustContain(t, "profile after lending", profile, "Lent to Alpha", "0.00 $ of 4.00 $ this month")
	loans, _ := e.St.AILoansOf(conns[0].ID)
	if status, _ := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/loan/revoke", url.Values{"id": {fmt.Sprint(loans[0].ID)}}); status != http.StatusForbidden {
		t.Fatalf("the borrower takes the loan back: %d", status)
	}
	if status, _ := e.aiPost(t, e.Member, ns.Slug, "/ui/orgs/ai/loan/revoke", url.Values{"id": {fmt.Sprint(loans[0].ID)}}); status != http.StatusSeeOther {
		t.Fatalf("revoke: %d", status)
	}
	_, owner = fetchPage(t, e.Owner, e.Base+"/ui/orgs?org=alpha")
	mustNotContain(t, "org page after the revocation", owner, "anna-key")
}

// fakeAnthropic answers like the Messages API.
func fakeAnthropic(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"type":"message","content":[{"type":"text","text":"Eine Idee wurde angelegt."}],"usage":{"input_tokens":100000,"output_tokens":10000}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// AC-694.4 and AC-694.7 on the run page, AC-694.5 in the cost overview: a
// run through the repository's alias names its connection and model; a run at
// its cap says which cap; the overview assigns every amount.
func TestRunPageNamesConnectionModelAndCapAndCostsAreAssigned(t *testing.T) {
	e := aiWeb(t)
	provider := fakeAnthropic(t)
	add := func(name string) {
		f := connForm(name, "anthropic", aiKeyTeam, "claude-haiku-5-5 1 5\nclaude-opus-5-5 5 25")
		f.Set("base_url", provider.URL)
		if status, body := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/connection", f); status != http.StatusSeeOther {
			t.Fatalf("add %s: %d %s", name, status, body)
		}
	}
	add("team-a")
	add("team-b")
	conns, _ := e.St.AIConnectionsFor(e.org.ID)
	byName := map[string]int64{}
	for _, c := range conns {
		byName[c.Name] = c.ID
	}
	for _, f := range []url.Values{
		{"alias": {"klein"}, "where": {"owner"}, "connection": {fmt.Sprint(byName["team-a"])}, "model": {"claude-haiku-5-5"}},
		{"alias": {"klein"}, "where": {"repo:" + shellProject}, "connection": {fmt.Sprint(byName["team-b"])}, "model": {"claude-opus-5-5"}},
	} {
		if status, body := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/alias", f); status != http.StatusSeeOther {
			t.Fatalf("alias: %d %s", status, body)
		}
	}
	proxy := &aiproxy.Proxy{Store: e.St, Box: e.box, Upstream: func(store.AIConnection, *url.URL) (*http.Client, error) { return provider.Client(), nil }}
	ps := httptest.NewServer(proxy)
	defer ps.Close()
	eng := &workflow.Engine{Store: e.St, Resolver: workflow.Builtins{"test/ask-a": workflow.Ask, "test/ask-b": workflow.Ask}, AIBaseURL: ps.URL}
	seq := 0
	run := func(project, name string, data map[string]string) store.WorkflowRun {
		t.Helper()
		if _, err := e.St.SetWorkflowTriggerAsOperator(store.WorkflowTrigger{Project: project, Workflow: name, EventKind: store.EventIdeaCreated, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		seq++
		if _, _, err := e.St.RecordWorkflowEvent(store.WorkflowEventInput{Kind: store.EventIdeaCreated, Project: project, SubjectKind: "idea", SubjectID: fmt.Sprint(seq),
			Data: data, Origin: store.EventOrigin{Kind: store.OriginHuman}, DedupeKey: fmt.Sprint("ai-web-", seq)}); err != nil {
			t.Fatal(err)
		}
		if _, err := eng.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := e.St.SetWorkflowTriggerAsOperator(store.WorkflowTrigger{Project: project, Workflow: name, EventKind: store.EventIdeaCreated, Enabled: false}); err != nil {
			t.Fatal(err)
		}
		runs, _ := e.St.WorkflowRunsIn(project, store.WorkflowRunFilter{SubjectKind: "idea", SubjectID: fmt.Sprint(seq)})
		return runs[0]
	}
	// In the shell repository "klein" is the repository's alias.
	a := run(shellProject, "test/ask-a", nil)
	_, page := fetchPage(t, e.Member, e.Base+"/ui/x/shell/workflows/runs/"+itoa(a.ID))
	mustContain(t, "run page", page, "team-b", "claude-opus-5-5", "Eine Idee wurde angelegt.", "0.75 $")
	// In the other repository of alpha it is the owner's.
	b := run(shellHiddenProject, "test/ask-b", map[string]string{"model": "klein"})
	if b.Connection != "team-a" || b.Model != "claude-haiku-5-5" {
		t.Fatalf("owner's alias: %+v", b)
	}
	// A cap of the run below one call ends the next run in budget.
	if status, _ := e.aiPost(t, e.Owner, "alpha", "/ui/orgs/ai/cap", url.Values{"workflow": {"test/ask-a"}, "run": {"0.10"}}); status != http.StatusSeeOther {
		t.Fatal("cap")
	}
	c := run(shellProject, "test/ask-a", nil)
	_, page = fetchPage(t, e.Member, e.Base+"/ui/x/shell/workflows/runs/"+itoa(c.ID))
	mustContain(t, "run at its cap", page, "Budget", "Cap of the run reached")
	_, settings := fetchPage(t, e.Owner, e.Base+"/ui/orgs?org=alpha")
	mustContain(t, "cost overview", settings, "Costs this month", "test/ask-a", "test/ask-b", "team-a", "team-b", "claude-haiku-5-5", "claude-opus-5-5",
		`href="/ui/x/shell/workflows/runs/`+itoa(a.ID)+`"`, "0.75 $", "0.15 $")
	noKeys(t, "settings with costs", settings)
}
