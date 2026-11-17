package web

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// REQ-677 AC-4 (P25): what an agent wrote is named the same way on every page, as
// "Robin’s Claude" with the agent's mark; a transport ("mcp") is never an author.
func TestOneNameForTheAgentThatWrote(t *testing.T) {
	for _, c := range []struct {
		lang            lang
		person, harness string
		want            string
	}{
		{langEN, "Robin", "claude-code", "Robin’s Claude"},
		{langEN, "Robin", "mcp", "Robin’s Agent"},
		{langEN, "Robin", "", "Robin’s Agent"},
		{langEN, "Robin", "api", "Robin’s Agent"},
		{langEN, "", "mcp", "Agent"},
		{langDE, "Robin", "claude-code", "Robins Claude"},
		{langDE, "Pia", "codex", "Pias Codex"},
	} {
		if got := authorAgentName(c.lang, c.person, c.harness); got != c.want {
			t.Errorf("authorAgentName(%q, %q) = %q, want %q", c.person, c.harness, got, c.want)
		}
	}
}

// The line under the head of a code page names the agent that described the
// path, not the way it came in.
func TestCodeTreeNamesTheAgentAndNeverMCP(t *testing.T) {
	e := ovEnv(t)
	for path, harness := range map[string]string{"cmd/a.go": "mcp", "cmd/b.go": "claude-code"} {
		if _, err := e.St.PutGhostFile(store.GhostFile{Project: shellProject, Path: path, Kind: "file", Description: "What " + path + " does.", Harness: harness, Person: "alice"}); err != nil {
			t.Fatal(err)
		}
	}
	a := fetchBody(t, e.Owner, e.Base+"/ui/x/shell/blob/main/cmd/a.go")
	if !strings.Contains(a, "alice’s Agent") || strings.Contains(a, ">mcp<") || strings.Contains(a, "by mcp") {
		t.Errorf("the description written through mcp is not named as an agent of alice: %.600s", a)
	}
	b := fetchBody(t, e.Owner, e.Base+"/ui/x/shell/blob/main/cmd/b.go")
	if !strings.Contains(b, "alice’s Claude") {
		t.Errorf("a Claude is not named: %.600s", b)
	}
	tree := fetchBody(t, e.Owner, e.Base+"/ui/x/shell/tree/main/cmd")
	if strings.Contains(tree, ">mcp<") || strings.Contains(tree, ">claude<") {
		t.Errorf("the last-commit line of a folder shows a bare harness: %.800s", tree)
	}
}

// The entry of an agent reads the same in its head and in its history.
func TestKnowledgeEntryOfAnAgentIsNamedTheSameInTheTimeline(t *testing.T) {
	e := ovEnv(t)
	kid, err := e.St.InsertKnowledge(store.Knowledge{Type: "note", Title: "By an agent", Body: "body", Scope: scope.Axes{Project: shellProject}, Person: "alice", Confidence: "trusted"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.St.RecordActor(actor.Knowledge, kid, actor.Actor{Kind: actor.Agent, Person: "alice", Harness: "claude-code"}); err != nil {
		t.Fatal(err)
	}
	page := fetchBody(t, e.Owner, e.Base+"/ui/x/shell/knowledge/"+itoa(kid))
	if n := strings.Count(page, "alice’s Claude"); n < 2 {
		t.Errorf("the head and the timeline both name the agent, found %d times", n)
	}
	if strings.Contains(page, "<b>alice</b> <span") && strings.Contains(page, "created the entry") && !strings.Contains(page, "alice’s Claude") {
		t.Error("the timeline names the person for the agent's entry")
	}
}

// The voice of an agent in a decision is not the voice of its person: the page
// says "anna’s Agent would take A", not "anna would take A".
func TestVoteOfAnAgentIsNamedAsTheAgentOfItsPerson(t *testing.T) {
	e, pa := listEnv(t)
	i, d := workedIdea(t, pa, shellProject)
	if _, err := pa.VoteDecision(d.ID, "A", "Agents have opinions too", idea.Actor{AccountID: 2, Person: "anna", Agent: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := pa.VoteDecision(d.ID, "B", "A person decides by hand", idea.Actor{AccountID: 4, Person: "lars"}); err != nil {
		t.Fatal(err)
	}
	page := fetchBody(t, e.Owner, e.Base+ideaPath(i))
	for _, want := range []string{"anna’s Agent", "Agents have opinions too", "A person decides by hand"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(page, "<b>anna</b>") {
		t.Error("the agent's vote is shown under the person's bare name")
	}
}

// A mention an agent wrote reaches the inbox under the agent, not under its person.
func TestMentionAnAgentWroteIsInTheInboxAsTheAgentOfItsPerson(t *testing.T) {
	e := ovEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "Spool begrenzen"})
	c, err := e.St.Access(store.Principal{ID: "person:2", Label: "anna"}).AddComment(store.CommentOnRequest, d.Request.ID, "@lars bitte prüfen", store.CommentActor{Person: "anna"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.St.RecordActor(actor.Comment, c.ID, actor.Actor{Kind: actor.Agent, Person: "anna", Harness: "claude-code"}); err != nil {
		t.Fatal(err)
	}
	inbox := fetchBody(t, e.Lead, e.Base+"/ui/inbox")
	if !strings.Contains(inbox, "anna’s Claude") || strings.Contains(inbox, "<b>anna</b> in") {
		t.Errorf("the agent's mention is shown under its person: %.1200s", inbox)
	}
}
