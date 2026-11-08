package main

import (
	"bytes"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func TestCommentCLIAddListEditDeleteAndRequestLinks(t *testing.T) {
	st := moveCLIFixture(t)
	d, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Eltern", Scope: scope.Axes{Project: cliHere}, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	ref := d.Request.HumanID()
	src, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Quelle", Scope: scope.Axes{Project: cliHere}, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	do := func(args ...string) (int, string) {
		var out bytes.Buffer
		code := run(args, &out)
		return code, out.String()
	}
	if code, out := do("comment", "add", ref, "Ein", "Kommentar"); code != 0 || !strings.Contains(out, "Ein Kommentar") || !strings.Contains(out, "link: ") {
		t.Fatalf("add: %d %s", code, out)
	}
	if code, out := do("comment", "list", ref); code != 0 || !strings.Contains(out, "Ein Kommentar") || !strings.Contains(out, "alice") {
		t.Fatalf("list: %d %s", code, out)
	}
	if code, out := do("comment", "edit", "1", "Neu"); code != 0 || !strings.Contains(out, `"revision": 2`) {
		t.Fatalf("edit: %d %s", code, out)
	}
	if code, out := do("comment", "versions", "1"); code != 0 || !strings.Contains(out, "Ein Kommentar") || !strings.Contains(out, "Neu") {
		t.Fatalf("versions: %d %s", code, out)
	}
	if code, out := do("comment", "delete", "1"); code != 0 || !strings.Contains(out, `"deleted": true`) {
		t.Fatalf("delete: %d %s", code, out)
	}
	if code, _ := do("comment", "add", "TASK-1", "x"); code != 2 {
		t.Fatalf("a task takes no comment: %d", code)
	}
	// Kind und Quelle beim Anlegen eines Requests.
	code, out := do("request", "create", "--type", "feature", "--title", "Kind", "--parent", ref, "--derived-from", src.Request.HumanID(), "--project", cliHere, "--json")
	if code != 0 || !strings.Contains(out, `"kind":"parent"`) || !strings.Contains(out, `"kind":"derived"`) {
		t.Fatalf("create with links: %d %s", code, out)
	}
}

func TestCommentHelpSaysThatEarlierVersionsStayVisible(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"comment", "help"}, &out); code != 2 {
		t.Fatalf("help: %d", code)
	}
	if !strings.Contains(out.String(), "Earlier versions stay visible to every reader; delete what must no longer be seen.") {
		t.Errorf("the help does not say that earlier versions stay visible:\n%s", out.String())
	}
}
