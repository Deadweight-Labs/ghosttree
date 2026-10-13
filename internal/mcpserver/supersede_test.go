package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func TestSupersedeAndRetireOverTheWire(t *testing.T) {
	c, st := targetFixture(t)
	session := connect(t, targetServer(c, tgtHere))
	remember := func(title string, extra map[string]any) (string, bool) {
		args := map[string]any{"type": "note", "title": title, "body": "details of " + title, "scope_hint": "project"}
		for k, v := range extra {
			args[k] = v
		}
		return callTool(t, session, "context_remember", args)
	}
	if got, failed := remember("share location created", nil); failed {
		t.Fatal(got)
	}
	old := knowledgeNamed(t, st, "share location created")

	// The follow-up replaces the old entry instead of standing next to it.
	got, failed := remember("share location removed again", map[string]any{
		"supersedes": []int64{old.ID}, "supersede_reason": "the share was removed",
	})
	if failed || !strings.Contains(got, fmt.Sprintf("superseded #%d", old.ID)) {
		t.Fatalf("remember with supersedes: %v %s", failed, got)
	}
	found, _ := callTool(t, session, "context_search", map[string]any{"query": "share location", "kind": "knowledge"})
	if strings.Contains(found, "created") || !strings.Contains(found, "removed again") {
		t.Errorf("search still shows the superseded entry or lost the new one:\n%s", found)
	}
	// Reachable through the id.
	full, failed := callTool(t, session, "context_search", map[string]any{"knowledge_id": old.ID})
	if failed || !strings.Contains(full, "share location created") {
		t.Errorf("the superseded entry is not readable by id: %v %s", failed, full)
	}

	// Retire: a reason is mandatory, then it leaves search.
	if got, failed := remember("temporary tunnel open", nil); failed {
		t.Fatal(got)
	}
	tunnel := knowledgeNamed(t, st, "temporary tunnel open")
	if got, failed := callTool(t, session, "context_retire", map[string]any{"id": tunnel.ID, "reason": "  "}); !failed || !strings.Contains(got, "reason") {
		t.Errorf("retire without a reason: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "context_retire", map[string]any{"id": tunnel.ID, "reason": "the tunnel was closed"}); failed || !strings.Contains(got, "retired") {
		t.Fatalf("retire: %v %s", failed, got)
	}
	found, _ = callTool(t, session, "context_search", map[string]any{"query": "temporary tunnel", "kind": "knowledge"})
	if strings.Contains(found, "tunnel") {
		t.Errorf("a retired entry is still found:\n%s", found)
	}
	if rs, _ := st.KnowledgeRetirements(tunnel.ID); len(rs) != 1 || rs[0].Reason != "the tunnel was closed" {
		t.Errorf("retirements = %+v", rs)
	}
}

func TestSupersedeNeedsAReasonAndWritesNothingWithout(t *testing.T) {
	c, st := targetFixture(t)
	s := targetServer(c, tgtHere)
	ctx := context.Background()
	if _, _, err := s.handleRemember(ctx, nil, RememberInput{Type: "note", Title: "first state", Body: "b", ScopeHint: "project"}); err != nil {
		t.Fatal(err)
	}
	old := knowledgeNamed(t, st, "first state")
	_, _, err := s.handleRemember(ctx, nil, RememberInput{Type: "note", Title: "zulu", Body: "b", ScopeHint: "project", Supersedes: []int64{old.ID}})
	if err == nil || !strings.Contains(err.Error(), "supersede_reason") {
		t.Fatalf("no reason: %v", err)
	}
	_, _, err = s.handleRemember(ctx, nil, RememberInput{Type: "note", Title: "yankee", Body: "b", ScopeHint: "project", SupersedeReason: "why"})
	if err == nil {
		t.Error("a reason without supersedes was accepted")
	}
	if rows, _ := st.SearchKnowledge("zulu", scope.Axes{}, 10); len(rows) != 0 {
		t.Errorf("refused calls stored an entry: %+v", rows)
	}
	if k, _ := st.KnowledgeByID(old.ID); k.Status != "active" {
		t.Errorf("old entry changed: %+v", k)
	}
}

// The agents are told what to do in the tool descriptions and the schema.
func TestToolsTellAgentsToSearchFirstAndSupersedeInsteadOfAFollowUp(t *testing.T) {
	c, _ := targetFixture(t)
	session := connect(t, targetServer(c, tgtHere))
	tools, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	desc := map[string]string{}
	for _, tool := range tools.Tools {
		desc[tool.Name] = tool.Description
	}
	for _, want := range []string{"context_search first", "supersedes", "follow-up", "context_retire"} {
		if !strings.Contains(desc["context_remember"], want) {
			t.Errorf("context_remember does not say %q:\n%s", want, desc["context_remember"])
		}
	}
	for _, want := range []string{"no longer exists", "reason", "supersedes"} {
		if !strings.Contains(desc["context_retire"], want) {
			t.Errorf("context_retire does not say %q:\n%s", want, desc["context_retire"])
		}
	}
}
