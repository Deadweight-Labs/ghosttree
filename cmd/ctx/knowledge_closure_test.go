package main

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestKnowledgeSubcommandsHaveTheirOwnHelp(t *testing.T) {
	for cmd, want := range map[string]string{
		"retire":    "usage: ctx knowledge retire <id> --reason R",
		"supersede": "usage: ctx knowledge supersede <id> --old",
		"show":      "usage: ctx knowledge show <id>",
	} {
		forms := [][]string{{cmd, "--help"}}
		if cmd != "show" {
			forms = append(forms, []string{cmd, "12", "-h"})
		}
		for _, args := range forms {
			var out bytes.Buffer
			code := run(append([]string{"knowledge"}, args...), &out)
			if code != 0 || !strings.Contains(out.String(), want) || strings.Contains(out.String(), "ctx knowledge move") {
				t.Errorf("%v: %d\n%s", args, code, out.String())
			}
		}
	}
}

func TestKnowledgeSupersedeAndShowCLI(t *testing.T) {
	st := moveCLIFixture(t)
	ctx := func(args ...string) (int, string) {
		var out bytes.Buffer
		code := run(append([]string{"knowledge"}, args...), &out)
		return code, out.String()
	}
	add := func(title string) (int64, string) {
		id, err := st.InsertKnowledge(store.Knowledge{Type: "note", Title: title, Body: "body of " + title, Scope: scope.Axes{Project: cliHere}, Person: "alice"})
		if err != nil {
			t.Fatal(err)
		}
		return id, strconv.FormatInt(id, 10)
	}
	oldA, ida := add("share created")
	oldB, idb := add("share location set")
	fresh, idn := add("share removed")

	if code, out := ctx("supersede", idn, "--old", ida); code != 2 || !strings.Contains(out, "--reason") {
		t.Errorf("without reason: %d %s", code, out)
	}
	if code, out := ctx("supersede", idn, "--reason", "r"); code != 2 || !strings.Contains(out, "--old") {
		t.Errorf("without --old: %d %s", code, out)
	}
	if code, out := ctx("supersede", idn, "--old", "x", "--reason", "r"); code != 2 || !strings.Contains(out, "--old takes entry ids") {
		t.Errorf("bad id: %d %s", code, out)
	}
	if k, _ := st.KnowledgeByID(oldA); k.Status != "active" {
		t.Fatalf("a refused call changed #%d: %+v", oldA, k)
	}

	code, out := ctx("supersede", idn, "--old", ida+",#"+idb, "--reason", "the share was removed")
	if code != 0 || !strings.Contains(out, "#"+idn+" now supersedes #"+ida+", #"+idb) {
		t.Fatalf("supersede: %d %s", code, out)
	}
	for _, id := range []int64{oldA, oldB} {
		if k, _ := st.KnowledgeByID(id); k.Status != "superseded" || k.SupersededBy != fresh {
			t.Errorf("#%d = %+v", id, k)
		}
	}
	if code, _ := ctx("supersede", idn, "--old", ida, "--reason", "again"); code != 1 {
		t.Errorf("superseding a superseded entry: %d", code)
	}

	code, out = ctx("show", ida)
	if code != 0 || !strings.Contains(out, "superseded by #"+idn) || !strings.Contains(out, "the share was removed") || !strings.Contains(out, "body of share created") {
		t.Errorf("show of a superseded entry: %d\n%s", code, out)
	}
	if code, out := ctx("show", idn); code != 0 || strings.Contains(out, "superseded by") {
		t.Errorf("show of a live entry: %d\n%s", code, out)
	}

	gone, idg := add("temporary tunnel")
	if code, out := ctx("retire", idg, "--reason", "tunnel closed"); code != 0 {
		t.Fatalf("retire: %d %s", code, out)
	}
	if code, out := ctx("show", idg); code != 0 || !strings.Contains(out, "retired") || !strings.Contains(out, "tunnel closed") {
		t.Errorf("show of a retired entry (#%d): %d\n%s", gone, code, out)
	}
}
