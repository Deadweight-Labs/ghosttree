package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestGroupAgentsPutsCardsUnderTheirMachineAndCountsTheActive(t *testing.T) {
	mk := func(machine, presence string) agentCard {
		return agentCard{agentRow: agentRow{Machine: machine, Presence: presence}}
	}
	groups := groupAgents([]agentCard{mk("lap", "work"), mk("box", "off"), mk("lap", "idle"), mk("lap", "wait_you")}, []machineRow{{Name: "lap", State: "active", StateText: "connected"}})
	if len(groups) != 2 || groups[0].Machine != "lap" || len(groups[0].Cards) != 3 || groups[0].Active != 2 || groups[0].StateText != "connected" {
		t.Fatalf("groups = %+v", groups)
	}
	if groups[1].Machine != "box" || groups[1].Active != 0 || groups[1].State != "" {
		t.Errorf("second group = %+v", groups[1])
	}
}

func TestAgentPageShowsTheAgentAndTheComposerWithItsSignals(t *testing.T) {
	e := ovEnv(t)
	ovAgent(t, e.St, shellProject, "claude:laptop:aaaa1111", "worker", "person:2")
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/agents/"+url.PathEscape("claude:laptop:aaaa1111"))
	for _, want := range []string{"worker", "@laptop", `action="/ui/agents/claude:laptop:aaaa1111/signal"`, `name="mode"`, `value="pause"`, `value="interrupt"`, `value="request"`, "Takes effect at the next tool call"} {
		if !strings.Contains(page, want) {
			t.Errorf("agent page lacks %q", want)
		}
	}
	// The hint belongs to the tooltip, not to the text of the page.
	if strings.Contains(visibleText(page), "Takes effect at the next tool call") {
		t.Error("the hint is visible text")
	}
	if status, _ := fetchPage(t, e.Owner, e.Base+"/ui/agents/"+url.PathEscape("claude:nobody:ffff")); status != http.StatusNotFound {
		t.Errorf("an unknown agent answers %d, want 404", status)
	}
}

func TestAgentSignalSendsAMessageOrARequestToTheAgentsRoomAndPauses(t *testing.T) {
	e := ovEnv(t)
	const id = "claude:laptop:bbbb2222"
	ovAgent(t, e.St, shellProject, id, "worker", "person:1")
	page := "/ui/agents/" + url.PathEscape(id)
	post := func(c *http.Client, form url.Values) *http.Response {
		form.Set("csrf_token", renderedCSRFToken(t, c, e.Base+page))
		return sameOriginPostForm(t, c, e.Base+page+"/signal", form)
	}
	res := post(e.Owner, url.Values{"mode": {"message"}, "body": {"please look at the migration"}})
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/ui/agents/"+id {
		t.Fatalf("message: %d -> %q", res.StatusCode, res.Header.Get("Location"))
	}
	res = post(e.Owner, url.Values{"mode": {"request"}, "body": {"can you take over?"}})
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("request: %d", res.StatusCode)
	}
	_, room := fetchPage(t, e.Owner, e.Base+"/ui/coord?room="+url.QueryEscape(store.RoomKeyForProject(shellProject)))
	if !strings.Contains(room, "please look at the migration") || !strings.Contains(room, "can you take over?") {
		t.Error("the signals did not arrive in the room")
	}
	if res = post(e.Owner, url.Values{"mode": {"message"}, "body": {" "}}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("an empty message answers %d", res.StatusCode)
	}
	res.Body.Close()
	if res = post(e.Owner, url.Values{"mode": {"dance"}, "body": {"x"}}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("an unknown signal answers %d", res.StatusCode)
	}
	res.Body.Close()
	// A stranger may not signal what it may not see.
	res = sameOriginPostForm(t, e.Guest, e.Base+page+"/signal", url.Values{"mode": {"message"}, "body": {"hi"}, "csrf_token": {renderedCSRFToken(t, e.Guest, e.Base+"/ui/overview")}})
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound && res.StatusCode != http.StatusForbidden {
		t.Errorf("a guest's signal answers %d", res.StatusCode)
	}
}
