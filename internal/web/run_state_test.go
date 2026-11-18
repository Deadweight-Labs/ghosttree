package web

import (
	"strings"
	"testing"
	"time"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// hook is what the hooks of the agent of a session tell the server, in the way the agent registers.
func (f liveFixture) hook(t *testing.T, event string) {
	t.Helper()
	if _, err := f.e.St.RegisterCoordAgent(store.CoordAgent{ExternalID: "claude:mainex:" + f.ext, PrincipalID: "person:1", Person: "alice", Provider: "claude",
		DisplayName: "alice-agent", RoomKey: store.RoomKeyForProject(shellProject), Role: "member", SessionID: f.ext, HookActivity: true, HookEvent: event}); err != nil {
		t.Fatal(err)
	}
}

func notLive(t *testing.T, f liveFixture, why string) {
	t.Helper()
	_, page := fetchPage(t, f.e.Owner, f.e.Base+f.issue)
	if liveBoxes(page) != 0 || strings.Contains(page, "is working on this right now") || strings.Contains(page, `class="live"`) {
		t.Errorf("%s: the issue still says somebody works on it right now", why)
	}
	_, list := fetchPage(t, f.e.Owner, f.e.Base+"/ui/x/shell/issues")
	if strings.Contains(list, `class="live"`) || strings.Contains(list, "is working on this right now") {
		t.Errorf("%s: the list still shows the agent as live", why)
	}
}

// REQ-685 AC-4: a run that is over is not "working right now", however fresh the last upload or file touch is.
// The transcript keeps arriving for a minute or two after the end; the hooks say what the age cannot.
func TestEndedRunIsNotShownAsLiveAnywhere(t *testing.T) {
	f := newLiveFixture(t)
	f.touch(t, "internal/web/live.go", time.Now())
	f.hook(t, store.HookTool)
	if _, page := fetchPage(t, f.e.Owner, f.e.Base+f.issue); liveBoxes(page) != 2 {
		t.Fatal("the fixture is not live while the agent works")
	}
	_, sessions := fetchPage(t, f.e.Owner, f.e.Base+"/ui/sessions")
	if !strings.Contains(sessions, `aria-label="Live"`) {
		t.Fatalf("the session of a working agent is not listed as live:\n%.600s", sessions)
	}

	// The agent finished its turn and waits for the next prompt: alive, but not working.
	f.hook(t, store.HookTurnEnd)
	notLive(t, f, "after the Stop hook")
	if _, s := fetchPage(t, f.e.Owner, f.e.Base+"/ui/sessions"); !strings.Contains(s, `aria-label="Live"`) {
		t.Error("an open session between two turns is not running any more, but it is not done either")
	}

	// The session is closed: nothing says live, on the issue, in the list, on the sessions page.
	f.touch(t, "internal/web/live.go", time.Now())
	f.hook(t, store.HookSessionEnd)
	notLive(t, f, "after SessionEnd")
	_, s := fetchPage(t, f.e.Owner, f.e.Base+"/ui/sessions")
	if strings.Contains(s, `aria-label="Live"`) {
		t.Errorf("the sessions page lists an ended run as live:\n%.800s", s)
	}
	if !strings.Contains(s, `aria-label="Ended"`) {
		t.Error("the ended run is not listed as ended")
	}

	// A resumed session is live again.
	f.hook(t, store.HookTool)
	f.touch(t, "internal/web/live.go", time.Now())
	if _, page := fetchPage(t, f.e.Owner, f.e.Base+f.issue); liveBoxes(page) != 2 {
		t.Error("a resumed session is not shown as live")
	}
}

// REQ-685 AC-4: an issue that is done has nobody working on it, whatever the work lines say.
func TestDoneIssueHasNoLiveWork(t *testing.T) {
	f := newLiveFixture(t)
	f.touch(t, "internal/web/live.go", time.Now())
	if _, page := fetchPage(t, f.e.Owner, f.e.Base+f.issue); liveBoxes(page) != 2 {
		t.Fatal("the fixture is not live")
	}
	d, err := f.e.St.RequestByID(f.id)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.e.St.SetCriterionState(d.Criteria[0].ID, "met", requestdomain.Evidence{Kind: "test", Ref: "go test", Person: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := f.e.St.CompleteRequest(f.id, requestdomain.Evidence{Kind: "test", Ref: "go test", Person: "alice"}); err != nil {
		t.Fatal(err)
	}
	notLive(t, f, "a done issue")
}

// REQ-685 AC-4: with no agent at work the page of the agents does not hide the others behind a fold: the list of
// the inactive agents is open, also when one agent waits on the viewer (that is not work). While somebody works it
// stays closed (it would bury the few that matter).
func TestInactiveAgentsAreOpenWhenNoneWorks(t *testing.T) {
	e := ovEnv(t)
	const ended, working = "claude:laptop:ended001", "claude:laptop:working02"
	room := store.RoomKeyForProject(shellProject)
	register := func(id, event string) {
		t.Helper()
		if _, err := e.St.RegisterCoordAgent(store.CoordAgent{ExternalID: id, PrincipalID: "person:1", Person: "alice", Provider: "claude", DisplayName: id,
			RoomKey: room, Role: "member", HookActivity: true, HookEvent: event}); err != nil {
			t.Fatal(err)
		}
	}
	register(ended, store.HookSessionEnd)
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/agents")
	if !strings.Contains(page, "No agent is working right now") {
		t.Fatalf("the fixture has an agent at work:\n%s", visibleText(page))
	}
	if !strings.Contains(page, `class="box agroup asec ainact" data-list-group open>`) {
		t.Errorf("the inactive agents are folded away although nobody is at work:\n%.1500s", page)
	}
	// An agent that waits on the viewer is not at work: the inactive ones stay open next to it.
	ovAgent(t, e.St, shellProject, "claude:laptop:asker0003", "asker", "person:2")
	if _, err := e.St.CoordinationFor(store.Principal{ID: "person:2", Label: "anna"}, "claude:laptop:asker0003").Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room,
		SenderExternalID: "claude:laptop:asker0003", ClientID: "q-1", Body: "Merge now?", Intent: store.IntentQuestion, Mentions: []string{"person:1"}}); err != nil {
		t.Fatal(err)
	}
	_, waiting := fetchPage(t, e.Owner, e.Base+"/ui/agents")
	if !strings.Contains(waiting, `class="box agroup asec awaits"`) || !strings.Contains(waiting, `class="box agroup asec ainact" data-list-group open>`) {
		t.Errorf("an agent that waits on the viewer is not work: the inactive ones belong open:\n%.1500s", waiting)
	}
	register(working, store.HookTool)
	_, busy := fetchPage(t, e.Owner, e.Base+"/ui/agents")
	if strings.Contains(busy, "No agent is working right now") || !strings.Contains(busy, `class="box agroup asec ainact" data-list-group>`) {
		t.Errorf("with an agent at work the inactive ones stay folded:\n%.1500s", busy)
	}
}
