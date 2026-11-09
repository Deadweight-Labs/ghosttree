package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestSectionAgentsSortsByWhatTheViewerHasToDoWithThem(t *testing.T) {
	mk := func(id, presence string) agentCard {
		return agentCard{agentRow: agentRow{ExternalID: id, Presence: presence}}
	}
	s := sectionAgents([]agentCard{mk("a", "work"), mk("b", "wait_you"), mk("c", "idle"), mk("d", "off"), mk("e", "wait_other"), mk("f", "stuck")}, "tok", "/ui/agents")
	ids := func(cs []agentCard) string {
		var out []string
		for _, c := range cs {
			out = append(out, c.ExternalID)
		}
		return strings.Join(out, "")
	}
	if ids(s.Waiting) != "b" || ids(s.Active) != "aef" || ids(s.Inactive) != "cd" {
		t.Fatalf("sections: waiting %q active %q inactive %q", ids(s.Waiting), ids(s.Active), ids(s.Inactive))
	}
	if s.Waiting[0].CSRFToken != "tok" || s.Waiting[0].Back != "/ui/agents" || s.Active[0].CSRFToken != "" {
		t.Errorf("only a row that waits carries the form of the check: %+v %+v", s.Waiting[0], s.Active[0])
	}
	if (agentSections{}).Any() || !s.Any() {
		t.Error("Any")
	}
}

func TestLocalBackKeepsToTheInterface(t *testing.T) {
	for back, want := range map[string]string{
		"/ui/agents":            "/ui/agents",
		"/ui/alpha/agents?x=1":  "/ui/alpha/agents?x=1",
		"//evil.example/ui/":    "/ui/agents",
		"https://evil.example/": "/ui/agents",
		"/ui/\\evil":            "/ui/agents",
		"/ui/a\r\nLocation: /x": "/ui/agents",
		"":                      "/ui/agents",
		"/other":                "/ui/agents",
	} {
		if got := localBack(back, "/ui/agents"); got != want {
			t.Errorf("localBack(%q) = %q, want %q", back, got, want)
		}
	}
}

// An agent waits on Robin. "Seen" quits that wait without an answer; only the
// person the agent waits on can, and only the wait of that one agent.
func TestAgentSeenQuitsOnlyTheViewersOwnWait(t *testing.T) {
	e := ovEnv(t)
	const id, other = "claude:laptop:aaaa4444", "claude:laptop:bbbb5555"
	room := store.RoomKeyForProject(shellProject)
	ovAgent(t, e.St, shellProject, id, "worker", "person:2")
	ovAgent(t, e.St, shellProject, other, "second", "person:2")
	ask := func(agent, client, body string) {
		t.Helper()
		if _, err := e.St.CoordinationFor(store.Principal{ID: "person:2", Label: "anna"}, agent).Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room,
			SenderExternalID: agent, ClientID: client, Body: body, Intent: store.IntentQuestion, Mentions: []string{"person:1"}}); err != nil {
			t.Fatal(err)
		}
	}
	ask(id, "q1", "Merge now?")
	ask(other, "q2", "Release first?")
	list := e.Base + "/ui/agents"
	_, page := fetchPage(t, e.Owner, list)
	if !strings.Contains(page, `class="box agroup asec awaits"`) || !strings.Contains(page, `action="/ui/agents/`+url.PathEscape(id)+`/seen"`) {
		t.Fatalf("the agent that waits has no section and no check:\n%s", visibleText(page))
	}
	// The agent's owner is not the one it waits on: no check for them.
	if _, mine := fetchPage(t, e.Member, list); strings.Contains(mine, "/seen\"") {
		t.Error("a check is offered to somebody the agent does not wait on")
	}
	post := func(c *http.Client, agent string, form url.Values) *http.Response {
		form.Set("csrf_token", renderedCSRFToken(t, c, list))
		res := sameOriginPostForm(t, c, e.Base+"/ui/agents/"+url.PathEscape(agent)+"/seen", form)
		res.Body.Close()
		return res
	}
	open := func() int {
		items, err := e.St.CoordinationFor(store.Principal{ID: "person:1", Label: "alice", TokenKind: store.WebSessionKind}, "").Attention()
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, it := range items {
			if it.IsRecipient && it.State == store.AttentionOpen {
				n++
			}
		}
		return n
	}
	if open() != 2 {
		t.Fatalf("open waits = %d, want 2", open())
	}
	// Somebody else cannot quit Robin's wait.
	post(e.Member, id, url.Values{})
	if open() != 2 {
		t.Fatalf("a stranger quit the wait: %d open", open())
	}
	// Without the token nothing happens either.
	res := sameOriginPostForm(t, e.Owner, e.Base+"/ui/agents/"+url.PathEscape(id)+"/seen", url.Values{})
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden || open() != 2 {
		t.Fatalf("no token: %d, %d open", res.StatusCode, open())
	}
	res = post(e.Owner, id, url.Values{"back": {"//evil.example/"}})
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/ui/agents" {
		t.Fatalf("seen: %d -> %q", res.StatusCode, res.Header.Get("Location"))
	}
	if open() != 1 {
		t.Fatalf("after seen %d waits are open, want the other agent's only", open())
	}
	_, page = fetchPage(t, e.Owner, list)
	if strings.Contains(page, url.PathEscape(id)+`/seen`) {
		t.Error("the agent still waits after seen")
	}
	if !strings.Contains(page, url.PathEscape(other)+`/seen`) {
		t.Error("the other agent's wait was quit as well")
	}
	// Seen twice is not an error.
	if res = post(e.Owner, id, url.Values{"back": {"/ui/agents"}}); res.StatusCode != http.StatusSeeOther {
		t.Errorf("second seen: %d", res.StatusCode)
	}
}
