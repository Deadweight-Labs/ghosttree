package workflow

import (
	"strings"
	"testing"
)

// REQ-696: every template passes the check of a server where "mittel" is an
// alias, is stage 1 (a block without an instruction), calls a model with a
// small budget, and creates only kinds its block can create.
func TestTemplatesPassTheCheckWithAliasMittelAndASmallBudget(t *testing.T) {
	if len(Templates()) != 4 {
		t.Fatalf("templates: %d", len(Templates()))
	}
	for _, tpl := range Templates() {
		d, rep := Check(tpl.Text, Env{Alias: func(name string) bool { return name == "mittel" }})
		if !rep.OK() {
			t.Fatalf("%s: %v", tpl.Name, rep.Errors)
		}
		if d.Model != "mittel" || d.RunUSD <= 0 || d.RunUSD > 0.5 || d.MonthUSD > 10 || d.Stage() != StageBlock || len(d.Results) == 0 {
			t.Fatalf("%s: %+v", tpl.Name, d)
		}
		if !strings.HasPrefix(d.Uses, BlockPrefix) {
			t.Fatalf("%s uses %s", tpl.Name, d.Uses)
		}
	}
	if _, ok := TemplateByName("nope"); ok {
		t.Fatal("an unknown template")
	}
}

// The check shows what a block always reads, and the event of a closed
// discussion is one a workflow may wait for.
func TestCheckNamesWhatABlockReadsAndAcceptsAClosedDiscussion(t *testing.T) {
	tpl, _ := TemplateByName("kriterien-aus-diskussion")
	_, rep := Check(tpl.Text, Env{})
	if !rep.OK() || rep.Event != "idea.discussion_closed" || !strings.Contains(strings.Join(rep.Reads, ","), "idea.decisions") {
		t.Fatalf("report: %+v", rep)
	}
	_, rep = Check("---\non: idea.created\nuses: ghosttree/criteria\nmodel: mittel\nresults:\n  requirement: 3\n---\n", Env{})
	if rep.OK() || !strings.Contains(rep.Errors[0].Msg, "does not run on idea.created") {
		t.Fatalf("criteria on a new idea: %+v", rep.Errors)
	}
	_, rep = Check("---\non: pr.merged\nuses: ghosttree/pr-wishes\nmodel: mittel\nresults:\n  knowledge: 3\n---\n", Env{})
	if rep.OK() || !strings.Contains(rep.Errors[0].Msg, "cannot create knowledge") {
		t.Fatalf("wishes that write knowledge: %+v", rep.Errors)
	}
}
