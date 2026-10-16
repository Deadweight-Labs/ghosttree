package web

import (
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const identityAgent = "claude:laptop:aaaa"

func identityEnv(t *testing.T) (shellEnv, string) {
	t.Helper()
	e := shellWebAll(t)
	room, _ := memberReadSeed(t, e)
	if _, err := e.St.RegisterCoordAgent(store.CoordAgent{ExternalID: identityAgent, Provider: "claude", RoomKey: room,
		PrincipalID: "person:2", Machine: "mac-lab", Branch: "feat/mac-sync", HookActivity: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.St.SetCoordWorkNote(identityAgent, "person:2", "PR #77 feat/mac-sync"); err != nil {
		t.Fatal(err)
	}
	return e, room
}

// Anforderungen 1 und 2: Agentenliste, Raum-Mitglieder und Absenderblock zeigen
// Person, Maschine, Branch und die Notiz, gekennzeichnet als selbstgemeldet.
func TestWebShowsAgentIdentityAndSelfReportedNote(t *testing.T) {
	e, room := identityEnv(t)
	roomPage := namesGetShell(t, e, "/ui/coord?room="+url.QueryEscape(room))
	text := visibleText(roomPage)
	for _, want := range []string{"mac-lab", "feat/mac-sync", "PR #77 feat/mac-sync"} {
		if strings.Count(text, want) < 2 {
			t.Errorf("room page shows %q fewer than twice (members and sender block): %s", want, text)
		}
	}
	if !strings.Contains(roomPage, `class="pp-title" title="`+messages["coord.self_reported"]+`"`) || !strings.Contains(roomPage, `class="cm-note" title="`+messages["coord.self_reported"]+`"`) {
		t.Error("the note is not marked as self-reported in the member list and next to the message")
	}
	agents := namesGetShell(t, e, "/ui/agents")
	if !strings.Contains(visibleText(agents), "mac-lab") || !strings.Contains(agents, `class="ag-note" title="`+messages["coord.self_reported"]+`"`) || !strings.Contains(agents, "PR #77 feat/mac-sync") {
		t.Errorf("agent list lacks identity or note: %s", visibleText(agents))
	}
}

func namesGetShell(t *testing.T, e shellEnv, path string) string {
	t.Helper()
	status, page := fetchPage(t, e.Lead, e.Base+path)
	if status != 200 {
		t.Fatalf("GET %s = %d", path, status)
	}
	return page
}

// Anforderung 3: der Chip im Composer stellt @agents voran; ein Gast sieht ihn
// nicht, und die Mitglieder-Liste bleibt ihm verborgen.
func TestComposerOffersTheBroadcastChipAndItAddressesActiveAgents(t *testing.T) {
	e, room := identityEnv(t)
	page := namesGetShell(t, e, "/ui/coord?room="+url.QueryEscape(room))
	if !strings.Contains(page, `name="mentions" value="all-agents"`) || !strings.Contains(visibleText(page), messages["coord.broadcast"]) {
		t.Error("the broadcast chip is missing in the composer")
	}
	if st := postRoom(t, e, e.Lead, "/ui/coord/send", room, url.Values{"body": {"wer arbeitet an welchem PR?"}, "mentions": {"all-agents"}}); st != 303 {
		t.Fatalf("send = %d", st)
	}
	msgs, err := e.St.CoordinationFor(store.Principal{ID: "person:2"}, identityAgent).Messages(store.DestinationRoom, room, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range msgs {
		if m.Body == "@agents wer arbeitet an welchem PR?" {
			found = true
			if m.ToYou != store.ToYouMention {
				t.Errorf("the active agent is not addressed: ToYou=%q", m.ToYou)
			}
		}
	}
	if !found {
		t.Fatalf("message with the @agents token not stored: %+v", msgs)
	}
	if status, guest := fetchPage(t, e.Guest, e.Base+"/ui/coord?room="+url.QueryEscape(room)); status == 200 && strings.Contains(guest, `value="all-agents"`) {
		t.Error("a guest is offered the broadcast chip")
	}
}

// Frage oder Blocker an "Alle aktiven Agenten", ohne dass ein Agent aktiv ist:
// nichts wird gesendet, die Seite zeigt einen Hinweis statt einer Fehlerseite.
func TestBroadcastChipWithQuestionAndNoActiveAgentShowsHintNotError(t *testing.T) {
	e := shellWebAll(t)
	room, _ := memberReadSeed(t, e)
	for _, intent := range []string{"question", "blocker"} {
		form := url.Values{"body": {"wer kann das übernehmen?"}, "mentions": {"all-agents"}, "intent": {intent}}
		if st := postRoom(t, e, e.Lead, "/ui/coord/send", room, form); st != 303 {
			t.Fatalf("%s: send = %d, want redirect to the room", intent, st)
		}
	}
	page := namesGetShell(t, e, "/ui/coord?room="+url.QueryEscape(room)+"&unreached=agents&unsent=1")
	if !strings.Contains(visibleText(page), "@agents reached no one. Nothing was sent") {
		t.Errorf("hint missing: %s", visibleText(page))
	}
}
