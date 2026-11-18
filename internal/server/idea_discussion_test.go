package server

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// DEC-46 over the API (REQ-696, AC-696.1): an agent closes the discussion of an
// idea only with its person's words in human_said; with them the workshop
// names the agent, the person and the words, and the event says an agent did it.
// A person in the web closes it without.
func TestAnAgentClosesADiscussionOnlyWithItsPersonsWord(t *testing.T) {
	f := accessAPI(t, true)
	agent := f.id["s-mia"]
	i := decode[idea.Idea](t, f.expect(t, "mia", 201, "POST", "/api/ideas", map[string]any{"project": accProject, "title": "Diskussion per Agent beenden"}))
	f.expect(t, "mia", 200, "POST", ideaPath("/api/ideas/%d/concept", i.ID), map[string]any{"about": "x", "requirements": []string{"a"}})
	f.expect(t, "mia", 400, "POST", ideaPath("/api/ideas/%d/phase", i.ID), map[string]any{"phase": "discussed", "session_id": agent})
	f.expect(t, "mia", 400, "POST", ideaPath("/api/ideas/%d/phase", i.ID), map[string]any{"phase": "discussed", "human_said": strings.Repeat("x", idea.MaxHumanSaid+1)})
	// A guest may not, with or without words.
	f.expect(t, "gus", 403, "POST", ideaPath("/api/ideas/%d/phase", i.ID), map[string]any{"phase": "discussed", "human_said": "mach zu"})
	got := decode[idea.Idea](t, f.expect(t, "mia", 200, "POST", ideaPath("/api/ideas/%d/phase", i.ID),
		map[string]any{"phase": "discussed", "human_said": "ja, alles geklärt, mach die Diskussion zu", "session_id": agent}))
	if got.Phase != idea.PhaseDiscussed {
		t.Fatalf("phase: %+v", got)
	}
	ws := decode[store.IdeaWorkshop](t, f.expect(t, "mia", 200, "GET", ideaPath("/api/ideas/%d/workshop", i.ID), nil))
	if c := ws.Closed; c == nil || !c.ByAgent || c.Person == "" || c.Said != "ja, alles geklärt, mach die Diskussion zu" {
		t.Fatalf("closure: %+v", ws.Closed)
	}
	// Back in discussion, a person in the web needs no words.
	f.webExpect(t, "mia", 200, (*api).setIdeaPhase, "POST", "/x", i.ID, map[string]any{"phase": "discussion"})
	got = decode[idea.Idea](t, f.webExpect(t, "mia", 200, (*api).setIdeaPhase, "POST", "/x", i.ID, map[string]any{"phase": "discussed"}))
	if got.Phase != idea.PhaseDiscussed {
		t.Fatalf("phase after the web: %+v", got)
	}
	ws = decode[store.IdeaWorkshop](t, f.expect(t, "mia", 200, "GET", ideaPath("/api/ideas/%d/workshop", i.ID), nil))
	if c := ws.Closed; c == nil || c.ByAgent || c.Said != "" {
		t.Fatalf("closure by a person: %+v", ws.Closed)
	}
}
