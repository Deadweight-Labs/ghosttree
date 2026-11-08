package main

import (
	"bytes"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func TestReactCLISetShowAndRemove(t *testing.T) {
	st := moveCLIFixture(t)
	d, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Eltern", Scope: scope.Axes{Project: cliHere}, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	ref := d.Request.HumanID()
	do := func(args ...string) (int, string) {
		var out bytes.Buffer
		code := run(args, &out)
		return code, out.String()
	}
	if code, out := do("react", ref); code != 0 || !strings.Contains(out, ref+": no reactions") {
		t.Fatalf("empty: %d %s", code, out)
	}
	if code, out := do("react", ref, "+1"); code != 0 || !strings.Contains(out, "+1 x1 (yours)") {
		t.Fatalf("set: %d %s", code, out)
	}
	// Zweimal setzen ändert nichts.
	if code, out := do("react", ref, "👍"); code != 0 || !strings.Contains(out, "+1 x1 (yours)") {
		t.Fatalf("set twice: %d %s", code, out)
	}
	if code, out := do("react", ref, "heart", "--json"); code != 0 || !strings.Contains(out, `"content":"heart"`) {
		t.Fatalf("json: %d %s", code, out)
	}
	if code, out := do("react", ref, "--remove", "+1"); code != 0 || strings.Contains(out, "+1 x1") || !strings.Contains(out, "heart x1") {
		t.Fatalf("remove: %d %s", code, out)
	}
	if code, out := do("react", ref, "thumbsup"); code == 0 || !strings.Contains(out, "invalid_reaction") && !strings.Contains(out, "not one of the reactions") {
		t.Fatalf("a free emoji: %d %s", code, out)
	}
	// Kommentare und falsche Verweise.
	if code, out := do("comment", "add", ref, "Ein Kommentar"); code != 0 {
		t.Fatalf("comment: %d %s", code, out)
	}
	if code, out := do("react", "comment:1", "rocket"); code != 0 || !strings.Contains(out, "rocket x1 (yours)") {
		t.Fatalf("comment reaction: %d %s", code, out)
	}
	for _, args := range [][]string{{"react"}, {"react", "TASK-1", "+1"}, {"react", "comment:x"}, {"react", ref, "+1", "heart"}, {"react", ref, "--remove"}} {
		if code, out := do(args...); code != 2 || !strings.Contains(out, "usage: ctx react") {
			t.Errorf("%v: %d %s", args, code, out)
		}
	}
	if code, _ := do("react", "REQ-99", "+1"); code == 0 {
		t.Error("an unknown issue must fail")
	}
}
