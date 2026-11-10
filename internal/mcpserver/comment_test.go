package mcpserver

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Kommentare über MCP: anlegen, listen, Karte mit Link, falsche Verweise.
func TestCommentToolsOverTheWire(t *testing.T) {
	s, _, base, _ := linkWorld(t)
	wireText(t, s, "pain_record", map[string]any{"title": "Es hakt manchmal"})
	session := connect(t, s)

	got := wireText(t, s, "comment_add", map[string]any{"ref": "PAIN-1", "body": "Das tritt nur nachts auf."})
	mustHave(t, "comment_add", got, `"body":"Das tritt nur nachts auf."`, "Comment #1", "on PAIN-1", "link: "+base+"/pains/1#comment-1")

	got = wireText(t, s, "comment_list", map[string]any{"ref": "pain-1"})
	mustHave(t, "comment_list", got, `"ref":"PAIN-1"`, `"body":"Das tritt nur nachts auf."`, `"can_edit":true`)

	for _, ref := range []string{"TASK-1", "commit:abcdef1", "nonsense"} {
		if got, failed := callTool(t, session, "comment_add", map[string]any{"ref": ref, "body": "x"}); !failed || !strings.Contains(got, "REQ-n, IDEA-n or PAIN-n") {
			t.Errorf("%s: %v %s", ref, failed, got)
		}
	}
	if got, failed := callTool(t, session, "comment_add", map[string]any{"ref": "PAIN-99", "body": "x"}); !failed || !strings.Contains(got, "not found") {
		t.Errorf("unknown pain: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "comment_add", map[string]any{"ref": "PAIN-1", "body": "  "}); !failed || !strings.Contains(got, "needs text") {
		t.Errorf("empty comment: %v %s", failed, got)
	}
}

// Earlier versions of a comment stay readable for every reader; a model that
// writes a comment has to know it before it puts something into it.
func TestCommentToolsSayThatEarlierVersionsStayVisible(t *testing.T) {
	c, _ := newTestClient(t)
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y", Machine: "testbox"}})
	tools, err := listRegistered(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, tool := range tools.Tools {
		if tool.Name != "comment_add" && tool.Name != "comment_list" {
			continue
		}
		seen++
		if !strings.Contains(tool.Description, "Earlier versions stay visible to every reader; delete what must no longer be seen.") {
			t.Errorf("%s does not say that earlier versions stay visible: %q", tool.Name, tool.Description)
		}
	}
	if seen != 2 {
		t.Fatalf("found %d of the 2 comment tools", seen)
	}
}

// Reaktionen über MCP: setzen, doppelt setzen, listen, zurücknehmen, Fehler.
func TestReactionToolsOverTheWire(t *testing.T) {
	s, _, _, rid := linkWorld(t)
	session := connect(t, s)
	ref := "REQ-" + strconv.FormatInt(rid, 10)

	got := wireText(t, s, "reaction_set", map[string]any{"ref": ref, "reaction": "+1"})
	mustHave(t, "reaction_set", got, ref+": ", "+1 x1 (yours)")
	got = wireText(t, s, "reaction_set", map[string]any{"ref": ref, "reaction": "👍"})
	mustHave(t, "reaction_set twice", got, "+1 x1 (yours)")
	got = wireText(t, s, "reaction_list", map[string]any{"ref": ref})
	mustHave(t, "reaction_list", got, "+1 x1 (yours)")
	got = wireText(t, s, "reaction_set", map[string]any{"ref": ref, "reaction": "+1", "remove": true})
	mustHave(t, "reaction_set remove", got, ref+": no reactions")

	wireText(t, s, "comment_add", map[string]any{"ref": ref, "body": "Ein Kommentar"})
	mustHave(t, "comment reaction", wireText(t, s, "reaction_set", map[string]any{"ref": "comment:1", "reaction": "heart"}), "heart x1 (yours)")

	for _, ref := range []string{"PAIN-1", "TASK-1", "nonsense", "comment:x"} {
		if got, failed := callTool(t, session, "reaction_set", map[string]any{"ref": ref, "reaction": "+1"}); !failed || !strings.Contains(got, "REQ-n, IDEA-n or comment:n") {
			t.Errorf("%s: %v %s", ref, failed, got)
		}
	}
	if got, failed := callTool(t, session, "reaction_set", map[string]any{"ref": ref, "reaction": "thumbsup"}); !failed || !strings.Contains(got, "not one of the reactions") {
		t.Errorf("a free emoji: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "reaction_list", map[string]any{"ref": "REQ-99"}); !failed || !strings.Contains(got, "not found") {
		t.Errorf("unknown issue: %v %s", failed, got)
	}
}
