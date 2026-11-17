package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// REQ-678: agents, sessions and machines show what really runs.

// presenceOfCard is the state word of the agent card the page shows for id:
// "work", "wait_reply", "ended" and so on (the class st-<presence> of its state).
func presenceOfCard(t *testing.T, page, id string) string {
	t.Helper()
	m := regexp.MustCompile(`title="` + regexp.QuoteMeta(id) + `"[\s\S]*?class="ast st-(\w+)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no card for %s", id)
	}
	return m[1]
}

// hookRegister stands in for the hook of a session: it announces the session on
// machine with the event the hook fired.
func hookRegister(t *testing.T, e sessEnv, id, machine, event string) {
	t.Helper()
	if _, err := e.St.RegisterCoordAgent(store.CoordAgent{ExternalID: id, PrincipalID: "person:1", Person: "alice", Provider: "claude",
		DisplayName: id, Machine: machine, Branch: "main", RoomKey: store.RoomKeyForProject(shellProject), HookActivity: true, HookEvent: event}); err != nil {
		t.Fatal(err)
	}
}

// AC-678.2: an agent that is working, one that finished its turn and waits, and
// one whose run ended are three different states; a finished run is not Working.
func TestAgentStatusTellsWorkingWaitingForReplyAndEnded(t *testing.T) {
	e := machineEnv(t)
	const id = "sess-run-states"
	page := func() string {
		_, p := fetchPage(t, e.Owner, e.Base+"/ui/agents")
		return p
	}

	hookRegister(t, e, id, "mainex", store.HookTurnStart)
	p := page()
	if got := presenceOfCard(t, p, id); got != "work" {
		t.Fatalf("a running turn: %s, want work", got)
	}

	hookRegister(t, e, id, "mainex", store.HookTurnEnd)
	p = page()
	if got := presenceOfCard(t, p, id); got != "wait_reply" {
		t.Fatalf("a turn that ended must not stay Working: %s, want wait_reply", got)
	}
	if !strings.Contains(p, "Waiting for reply") {
		t.Error("the page does not say the agent waits for a reply")
	}
	if strings.Contains(p, ">Working<") {
		t.Error("an agent that finished its turn still reads Working")
	}

	hookRegister(t, e, id, "mainex", store.HookSessionEnd)
	p = page()
	if got := presenceOfCard(t, p, id); got != "ended" {
		t.Fatalf("a session that said goodbye: %s, want ended", got)
	}
	if !strings.Contains(p, "Ended") || strings.Contains(p, ">Working<") {
		t.Error("an ended run does not read Ended, or still reads Working")
	}
	// It is out of the active section, with the dormant ones, and the page says nothing works.
	if !strings.Contains(p, "No agent is working right now.") {
		t.Error("the page has no empty state for the active agents")
	}
}

// AC-678.3: the name of ctx login --machine is the one the profile, the devices
// and the agents show, also when the name of the agent already carries it.
func TestMachineNameIsTheSameInProfileDevicesAndAgents(t *testing.T) {
	e := machineEnv(t)
	hookRegister(t, e, "sess-machine-name", "mainex", store.HookTool)

	_, profile := fetchPage(t, e.Owner, e.Base+"/ui/profile")
	if strings.Contains(profile, "Unknown machine") {
		t.Error("the profile files an agent of a signed-in machine under Unknown machine")
	}
	if !strings.Contains(profile, `<a href="/ui/m/mainex">mainex</a>`) {
		t.Errorf("the profile does not name the machine as a link: %s", pageSlice(profile, "Your agents", "Notifications"))
	}
	if strings.Contains(profile, "ctx login on mainex") {
		t.Error("the devices read \"ctx login on mainex\" where the machine is called mainex everywhere else")
	}
	_, agents := fetchPage(t, e.Owner, e.Base+"/ui/agents")
	if !strings.Contains(agents, `@mainex`) {
		t.Error("the agent card does not name the machine")
	}
}

func pageSlice(s, from, to string) string {
	i := strings.Index(s, from)
	if i < 0 {
		return s
	}
	s = s[i:]
	if j := strings.Index(s, to); j > 0 {
		return s[:j]
	}
	return s
}

// AC-678.4: every mention of a machine is a link to its page; the page offers
// "Share with org", and the number on the organisation counts the machines the
// tab lists.
func TestEveryMachineMentionLinksToItsPageAndTheOrgCountsTheSameMachines(t *testing.T) {
	e := machineEnv(t)
	hookRegister(t, e, "sess-machine-link", "mainex", store.HookTool)

	_, agents := fetchPage(t, e.Owner, e.Base+"/ui/agents")
	for _, want := range []string{`<a class="mono" href="/ui/m/mainex">mainex</a>`, `href="/ui/m/mainex">@mainex</a>`} {
		if !strings.Contains(agents, want) {
			t.Errorf("the agents page lacks the machine link %q", want)
		}
	}
	_, profile := fetchPage(t, e.Owner, e.Base+"/ui/profile")
	if strings.Count(profile, `href="/ui/m/mainex"`) < 2 {
		t.Error("the profile names the machine without a link to it (agent group and device)")
	}
	_, sessions := fetchPage(t, e.Owner, e.Base+"/ui/sessions")
	if !strings.Contains(sessions, `href="/ui/m/mainex"`) {
		t.Error("the sessions list names the machine without a link")
	}
	code, machine := fetchPage(t, e.Owner, e.Base+"/ui/m/mainex")
	if code != http.StatusOK || !strings.Contains(machine, "Share with org") || !strings.Contains(machine, `action="/ui/m/mainex/share"`) {
		t.Fatalf("the machine page has no way to share it with an org: %d", code)
	}

	// Not shared yet: the tab says 0 and points at the machine instead of ending there.
	_, tab := fetchPage(t, e.Owner, e.Base+"/ui/alpha/machines")
	if !strings.Contains(tab, `<span>Machines</span><span class="counter">0</span>`) {
		t.Error("an unshared machine must not be counted by the organisation")
	}
	if !strings.Contains(tab, `href="/ui/m/mainex#shares"`) || !strings.Contains(tab, "Share with the org") {
		t.Errorf("the empty tab does not lead to sharing the machine: %s", pageSlice(tab, "Machines of the organization", "</main>"))
	}

	e.post(t, e.Owner, "/ui/m/mainex/share", url.Values{"org": {"alpha"}, "role": {"read"}})
	_, tab = fetchPage(t, e.Owner, e.Base+"/ui/alpha/machines")
	if !strings.Contains(tab, `href="/ui/alpha/machines/mainex"`) {
		t.Error("the tab does not list the machine after it was shared")
	}
	if strings.Contains(tab, `href="/ui/m/mainex#shares"`) {
		t.Error("a machine already shared is still offered for sharing")
	}
	if !strings.Contains(tab, `<span>Machines</span><span class="counter">1</span>`) {
		t.Errorf("the counter does not count the machine the tab lists: %s", pageSlice(tab, "Machines", "</a>"))
	}
}

// AC-678.5: an empty session list does not ask to connect an agent when one is
// connected; it says what is missing and offers the way on.
func TestEmptySessionsSayWhatIsMissingAndNeverContradictAConnectedAgent(t *testing.T) {
	e := shellWebAll(t)
	e.St.SetAccessMode(store.AccessMode{Enforce: true})
	se := sessEnv{shellEnv: e}

	_, p := fetchPage(t, e.Owner, e.Base+"/ui/sessions")
	if !strings.Contains(p, "Sessions of your agents appear here.") || !strings.Contains(p, "Connect an agent") {
		t.Errorf("nothing connected: the list says what comes here and offers to connect: %s", pageSlice(p, "box-empty", "</div>"))
	}

	if _, _, err := e.St.CreateDeviceToken("person:1", "mainex"); err != nil {
		t.Fatal(err)
	}
	_, p = fetchPage(t, e.Owner, e.Base+"/ui/sessions")
	if strings.Contains(p, "Connect an agent") {
		t.Error("a signed-in machine is told to connect an agent")
	}
	if !strings.Contains(p, "mainex is connected, but no session has arrived yet") || !strings.Contains(p, "ctx watch") || !strings.Contains(p, `href="/ui/m/mainex"`) {
		t.Errorf("a connected machine without sessions: %s", pageSlice(p, "box-empty", "</div>"))
	}

	hookRegister(t, se, "sess-no-transcript", "mainex", store.HookTool)
	_, p = fetchPage(t, e.Owner, e.Base+"/ui/sessions")
	if strings.Contains(p, "Connect an agent") || !strings.Contains(p, "An agent is running on mainex, but no session has arrived yet") {
		t.Errorf("a running agent without sessions: %s", pageSlice(p, "box-empty", "</div>"))
	}
}
