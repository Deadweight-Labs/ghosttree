package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Wissen hat seine Adresse unter dem Repository; die alte Adresse prüft das Recht
// und leitet, mit der Abfrage, auf die neue weiter. Ein Eintrag ohne Repository
// bleibt an der alten Adresse; eine Nummer in einem fremden Repository ist unbekannt.
func TestKnowledgeLivesUnderItsRepositoryAndTheOldAddressLeadsThere(t *testing.T) {
	e := ovEnv(t)
	id := knInsert(t, e.St, store.Knowledge{Title: "Pitfall in shell", Confidence: "trusted", Type: "pitfall"})
	hidden := knInsert(t, e.St, store.Knowledge{Title: "SECRET-KNOWLEDGE", Confidence: "trusted", Scope: scopeOf(shellHiddenProject)})
	global, err := e.St.InsertKnowledge(store.Knowledge{Type: "note", Title: "Global habit", Body: "b", Person: "alice", Confidence: "trusted"})
	if err != nil {
		t.Fatal(err)
	}
	canonical := "/ui/x/shell/knowledge/" + itoa(id)

	status, page := fetchPage(t, e.Member, e.Base+canonical)
	if status != http.StatusOK || !strings.Contains(page, "Pitfall in shell") || !strings.Contains(page, `class="rband`) {
		t.Fatalf("canonical page: %d", status)
	}
	// The old address leads on, query and all, to a page that exists.
	status, loc := locationOf(t, e.Member, e.Base+"/ui/knowledge/"+itoa(id)+"?edit=1")
	if status != http.StatusFound || loc != canonical+"?edit=1" {
		t.Fatalf("old address -> %d %q, want 302 %s?edit=1", status, loc, canonical)
	}
	if status, _ := fetchPage(t, e.Member, e.Base+loc); status != http.StatusOK {
		t.Errorf("the target answers %d", status)
	}
	// An entry without a repository stays where it was.
	if status, loc := locationOf(t, e.Owner, e.Base+"/ui/knowledge/"+itoa(global)); status != http.StatusOK {
		t.Errorf("global entry -> %d %q, want 200", status, loc)
	}
	if status, _ := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/knowledge/"+itoa(global)); status != http.StatusNotFound {
		t.Errorf("a global entry under a repository: %d, want 404", status)
	}

	// A member without a role in the other repository: unknown at every address,
	// the same answer as for a number that does not exist.
	var want string
	for _, p := range []string{"/ui/x/hidden/knowledge/" + itoa(hidden), "/ui/knowledge/" + itoa(hidden), "/ui/x/shell/knowledge/" + itoa(hidden), "/ui/knowledge/999999"} {
		status, body := fetchPage(t, e.Member, e.Base+p)
		if status != http.StatusNotFound || strings.Contains(body, "SECRET") {
			t.Errorf("member at %s: %d", p, status)
		}
		if want == "" {
			want = body
		}
	}
	if _, unknown := fetchPage(t, e.Member, e.Base+"/ui/x/nothere/knowledge/"+itoa(hidden)); stripCSRF(unknown) != stripCSRF(want) {
		t.Error("a foreign entry answers differently from an unknown address")
	}
	// A guest does not read what is not released, at either address.
	staged := knInsert(t, e.St, store.Knowledge{Title: "Not yet released", Confidence: "staged"})
	for _, p := range []string{"/ui/knowledge/" + itoa(staged), "/ui/x/shell/knowledge/" + itoa(staged)} {
		if status, _ := fetchPage(t, e.Guest, e.Base+p); status != http.StatusNotFound {
			t.Errorf("guest at %s: %d, want 404", p, status)
		}
	}
	// The owner reaches the hidden entry; the wrong repository leads to the right one.
	if status, loc := locationOf(t, e.Owner, e.Base+"/ui/x/shell/knowledge/"+itoa(hidden)); status != http.StatusFound || loc != "/ui/x/hidden/knowledge/"+itoa(hidden) {
		t.Errorf("owner, wrong repository -> %d %q", status, loc)
	}
	if status, _ := fetchPage(t, e.Owner, e.Base+"/ui/x/hidden/knowledge/"+itoa(hidden)); status != http.StatusOK {
		t.Errorf("owner at the hidden entry: %d", status)
	}
	// The lists link to the canonical address.
	_, list := fetchPage(t, e.Member, e.Base+"/ui/x/shell/knowledge")
	if !strings.Contains(list, `href="`+canonical+`"`) {
		t.Error("the knowledge list does not link to the canonical address")
	}
}

func TestSessionLivesUnderItsRepositoryAndTheOldAddressLeadsThere(t *testing.T) {
	e := seedSessions(t)
	pid := e.pid["anna-project"]
	canonical := "/ui/x/shell/sessions/" + pid
	status, page := e.get(t, e.Member, canonical)
	if status != http.StatusOK || !strings.Contains(page, `class="rband`) {
		t.Fatalf("canonical page: %d", status)
	}
	status, loc := locationOf(t, e.Member, e.Base+"/ui/sessions/"+pid+"?q=zebrafish")
	if status != http.StatusFound || loc != canonical+"?q=zebrafish" {
		t.Fatalf("old address -> %d %q", status, loc)
	}
	if status, _ := e.get(t, e.Member, loc); status != http.StatusOK {
		t.Errorf("the target answers %d", status)
	}
	// What the viewer may not read is unknown, in the old address, in its own
	// repository and in the address of the repository it was written in.
	hidden, private := e.pid["hidden-project"], e.pid["alice-private"]
	for _, p := range []string{"/ui/sessions/" + hidden, "/ui/x/hidden/sessions/" + hidden, "/ui/x/shell/sessions/" + hidden, "/ui/sessions/" + private, "/ui/x/shell/sessions/" + private, "/ui/x/shell/sessions/nothere"} {
		if status, body := e.get(t, e.Member, p); status != http.StatusNotFound || strings.Contains(body, "zebrafish") {
			t.Errorf("member at %s: %d", p, status)
		}
	}
	// A guest sees the sessions shared with guests and nothing else.
	if status, _ := e.get(t, e.Guest, "/ui/x/shell/sessions/"+e.pid["anna-guests"]); status != http.StatusOK {
		t.Errorf("guest at a guest-shared session: %d", status)
	}
	if status, _ := e.get(t, e.Guest, "/ui/x/shell/sessions/"+pid); status != http.StatusNotFound {
		t.Errorf("guest at a project session: %d, want 404", status)
	}
	// The owner can read both repositories; the wrong one leads to the right one.
	if status, loc := locationOf(t, e.Owner, e.Base+"/ui/x/shell/sessions/"+hidden); status != http.StatusFound || loc != "/ui/x/hidden/sessions/"+hidden {
		t.Errorf("owner, wrong repository -> %d %q", status, loc)
	}
}

func scopeOf(project string) scope.Axes { return scope.Axes{Project: project} }
