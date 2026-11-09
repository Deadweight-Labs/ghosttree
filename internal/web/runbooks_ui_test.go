package web

import (
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/runbook"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestRunbooksListAndPageShowCommandsToCopyAndTheHandToAnAgent(t *testing.T) {
	e := ovEnv(t)
	spec := runbook.Spec{Purpose: "Roll out the new version.", Params: []runbook.Param{{Name: "tag", Type: runbook.TypeString, Description: "Image tag"}},
		Steps: []runbook.Step{{Title: "Build", Command: "make check", Check: "exit code 0"}}, Rollback: []runbook.Step{{Title: "Restore", Command: "echo back"}},
		Targets: []runbook.Target{{Name: "prod", Comment: "the server", Steps: []runbook.ExecStep{{Argv: []string{"make", "check"}}}}}}
	if _, err := e.St.WriteRunbook(store.RunbookWrite{Project: shellProject, Name: "rollout-prod", Spec: spec, Person: "alice", AuthorID: 1}); err != nil {
		t.Fatal(err)
	}
	_, list := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/runbooks")
	for _, want := range []string{"rollout-prod", "Roll out the new version.", `href="/ui/x/shell/runbooks/rollout-prod"`, `data-list-filter=".rbrow"`, "1 runbooks"} {
		if !strings.Contains(list, want) {
			t.Errorf("list lacks %q", want)
		}
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/runbooks/rollout-prod")
	for _, want := range []string{"ctx run rollout-prod --target prod", "ctx run rollout-prod --target prod --run", "make check", "exit code 0", "Image tag", "echo back", "Dry run", "Hand to an agent"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	roomKey, _ := memberReadSeed(t, e)
	room := url.QueryEscape(roomKey)
	if !strings.Contains(page, "room="+room) {
		t.Error("the hand to an agent does not open the room")
	}
	// The draft arrives in the composer, escaped.
	_, composer := fetchPage(t, e.Owner, e.Base+"/ui/coord?room="+room+"&draft="+url.QueryEscape("run <it>"))
	if !strings.Contains(composer, "run &lt;it&gt;</textarea>") {
		t.Errorf("the draft is not in the composer: %s", composer[strings.Index(composer, "<textarea"):][:300])
	}
	if status, _ := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/runbooks/nope"); status != 404 {
		t.Errorf("an unknown runbook answers %d", status)
	}
}

// Ein Eintrag mit Projekt UND Maschine ist Projektwissen (Entscheidung #4349): wer
// das Projekt liest, sieht ihn, ein Gast aber nicht den Maschinennamen.
func TestSessionHandleHidesTheMachineFromGuests(t *testing.T) {
	e := ovEnv(t)
	sess := store.Session{Harness: "claude-code", Scope: scope.Axes{Project: shellProject, Machine: "secret-box"}}
	guest := e.St.Access(store.Principal{ID: "person:3", Label: "gina", TokenKind: store.WebSessionKind})
	member := e.St.Access(store.Principal{ID: "person:2", Label: "anna", TokenKind: store.WebSessionKind})
	if got := sessionHandle(member, sess); got != "claude@secret-box" {
		t.Errorf("member handle %q", got)
	}
	if got := sessionHandle(guest, sess); got != "claude" {
		t.Errorf("guest handle %q, want the machine left out", got)
	}
}
