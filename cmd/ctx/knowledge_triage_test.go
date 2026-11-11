package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestPrintTriageGroupsByReason(t *testing.T) {
	var out bytes.Buffer
	printTriage(&out, client.TriageReport{Items: []client.TriageEntry{
		{Knowledge: store.Knowledge{ID: 12, Title: "ssh to the build host", Scope: scope.Axes{Machine: "m1"}}, Kind: store.TriageMove, Project: "github.com/dw/here"},
		{Knowledge: store.Knowledge{ID: 40, Title: "local model inventory", Scope: scope.Axes{Machine: "m1"}}, Kind: store.TriageUnused, Deliveries: 31},
	}})
	got := out.String()
	for _, want := range []string{"arose in exactly one project", "KN-12", "-> github.com/dw/here", "not picked up for 90 days", "KN-40", "(31× in the briefing)"} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "KN-12") > strings.Index(got, "KN-40") {
		t.Errorf("move proposals come before the unused ones:\n%s", got)
	}
	out.Reset()
	printTriage(&out, client.TriageReport{})
	if !strings.Contains(out.String(), "nothing to review") {
		t.Errorf("empty list: %q", out.String())
	}
}

// While the delivery log is younger than the period, the list of unused entries
// is not part of the answer, and the command says so instead of "nothing to
// review".
func TestPrintTriageSaysWhenTheUnusedListIsNotJudgedYet(t *testing.T) {
	var out bytes.Buffer
	printTriage(&out, client.TriageReport{UnusedSkipped: true, LogDays: 3})
	got := out.String()
	if !strings.Contains(got, "delivery log reaches back only 3 days") {
		t.Errorf("no note about the log:\n%s", got)
	}
	if strings.Contains(got, "nothing to review") {
		t.Errorf("claims nothing to review although the unused list was not judged:\n%s", got)
	}
}

func TestKnowledgeEverywhereNeedsAnID(t *testing.T) {
	var out bytes.Buffer
	if code := cmdKnowledge([]string{"everywhere"}, &out); code != 2 {
		t.Errorf("exit %d without an id: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "ctx knowledge everywhere") {
		t.Errorf("no usage: %s", out.String())
	}
}
