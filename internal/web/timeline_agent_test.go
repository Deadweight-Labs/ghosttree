package web

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// entryOf is the markup of the entry of a timeline with the given data-act, from its tag to the next entry.
func entryOf(t *testing.T, page, act string) string {
	t.Helper()
	i := strings.Index(page, `data-act="`+act+`"`)
	if i < 0 {
		t.Fatalf("the page has no %s entry", act)
	}
	rest := page[i:]
	if j := strings.Index(rest[1:], `class="cmt ev tev"`); j > 0 {
		rest = rest[:j+1]
	}
	return rest
}

// REQ-685 AC-5 (usability run 2, jonas/015): an agent that reopens a requirement is named as its owner's agent
// in the history of the issue, the way the one that meets it is; a person who does it stays a person.
func TestAgentThatReopenedARequirementIsNamedAsTheAgentInTheHistory(t *testing.T) {
	e := ovEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "Reopened by an agent", Criteria: []string{"one", "two"}})
	ev := requestdomain.Evidence{Kind: "test", Ref: "go test", Person: "alice"}
	for _, c := range d.Criteria {
		if err := e.St.SetCriterionState(c.ID, "met", ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.St.CompleteRequest(d.Request.ID, ev); err != nil {
		t.Fatal(err)
	}
	if err := e.St.ReopenCriterion(d.Criteria[1].ID, "the test is missing", "alice", nil); err != nil {
		t.Fatal(err)
	}
	issue := e.Base + "/ui/x/shell/issues/" + itoa(d.Request.ID)

	// Without a stamp the entry shows the person, as before (unknown is never guessed into an agent).
	_, page := fetchPage(t, e.Owner, issue)
	if got := entryOf(t, page, "criterion.reopened"); strings.Contains(got, "alice’s Claude") || !strings.Contains(got, "alice") {
		t.Fatalf("an unstamped entry:\n%s", got)
	}

	// The agent that did it: its owner's agent, with the mark, on this entry and on the reopening of the issue.
	if err := e.St.RecordActor(actor.Criterion, d.Criteria[1].ID, actor.Actor{Kind: actor.Agent, AccountID: 1, Harness: "claude-code"}); err != nil {
		t.Fatal(err)
	}
	_, page = fetchPage(t, e.Owner, issue)
	for _, act := range []string{"criterion.reopened", "request.reopened"} {
		got := entryOf(t, page, act)
		if !strings.Contains(got, "<b>alice’s Claude</b>") || !strings.Contains(got, `class="gav agent a-claude`) {
			t.Errorf("the %s entry does not name the agent:\n%s", act, got)
		}
	}
}

// REQ-685 AC-5 (usability run 2, pia/031): the revision of a concept that an agent wrote is not "Pia revised the
// concept" but its owner's agent; the text a person wrote stays theirs.
func TestAgentTextsOnTheIdeaTimelineAreNamedAsTheAgent(t *testing.T) {
	e, pa := listEnv(t)
	agent := idea.Actor{AccountID: 1, Person: "alice", Agent: true}
	i, err := pa.CreateIdea(store.CreateIdeaInput{Project: shellProject, Title: "Voice answers", Body: "Talk instead of typing.", Actor: humanActor})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := pa.PutIdeaText(i.ID, store.PutIdeaTextInput{Part: idea.PartSummary, Body: "Answer by speaking.", Actor: agent}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := pa.PutIdeaConcept(i.ID, store.PutIdeaConceptInput{Actor: agent, Patch: idea.ConceptPatch{About: nStr("Voice answers in the inbox.")}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := pa.PutIdeaConcept(i.ID, store.PutIdeaConceptInput{Actor: humanActor, Patch: idea.ConceptPatch{What: nStr("A microphone button.")}}); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Owner, e.Base+ideaPath(i))
	byAgent, byHand := entryOf(t, page, "idea.concept"), ""
	if !strings.Contains(byAgent, "alice’s Agent") {
		t.Errorf("the concept an agent revised is not named as the agent:\n%s", byAgent)
	}
	if got := entryOf(t, page, "idea.summary"); !strings.Contains(got, "alice’s Agent") {
		t.Errorf("the summary an agent wrote is not named as the agent:\n%s", got)
	}
	// The second concept revision is the person's.
	last := page[strings.LastIndex(page, `data-act="idea.concept"`):]
	if end := strings.Index(last[1:], `class="cmt ev tev"`); end > 0 {
		last = last[:end+1]
	}
	if byHand = last; !strings.Contains(byHand, "alice") || strings.Contains(byHand, "’s Agent") {
		t.Errorf("a revision the person wrote is shown as an agent's:\n%s", byHand)
	}
}
