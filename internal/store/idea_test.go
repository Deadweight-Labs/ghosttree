package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
)

func newIdea(t *testing.T, w *painWorld, who, title string) idea.Idea {
	t.Helper()
	i, err := w.pa[who].CreateIdea(CreateIdeaInput{Project: roleProject, Title: title, Body: "ein Satz", Actor: ideaActor(who)})
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func TestAnIdeaHasATitleATextAndNothingElse(t *testing.T) {
	w := newPainWorld(t)
	i := newIdea(t, w, "mia", "Dunkles Thema")
	if i.ID == 0 || i.State != idea.Open || i.HumanID() != "IDEA-1" || i.Person != "mia" {
		t.Fatalf("idea: %+v", i)
	}
	var requests, criteria int
	w.st.DB().QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&requests)
	w.st.DB().QueryRow(`SELECT COUNT(*) FROM request_criteria`).Scan(&criteria)
	if requests != 0 || criteria != 0 {
		t.Fatalf("an idea created requests=%d criteria=%d", requests, criteria)
	}
	for _, bad := range []string{"", "  ", "zwei\nZeilen", "a\x07b", strings.Repeat("x", idea.MaxTitle+1)} {
		if _, err := w.pa["mia"].CreateIdea(CreateIdeaInput{Project: roleProject, Title: bad}); err == nil {
			t.Errorf("title %q accepted", bad)
		}
	}
	if _, err := w.pa["mia"].CreateIdea(CreateIdeaInput{Project: roleProject, Title: "ok", Body: strings.Repeat("x", idea.MaxBody+1)}); ruleCode(err) != "body_too_long" {
		t.Fatalf("long body: %v", err)
	}
	if _, err := w.pa["mia"].CreateIdea(CreateIdeaInput{Title: "ok"}); ruleCode(err) != "project_required" {
		t.Fatalf("no project: %v", err)
	}
}

func TestRecordingASimilarIdeaNamesTheExistingOne(t *testing.T) {
	w := newPainWorld(t)
	first := newIdea(t, w, "mia", "Dunkles Thema für die Oberfläche")
	_, err := w.pa["lena"].CreateIdea(CreateIdeaInput{Project: roleProject, Title: "dunkles thema für die oberfläche!", Actor: ideaActor("lena")})
	if ruleCode(err) != "idea_duplicate" || !strings.Contains(err.Error(), first.HumanID()) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := w.pa["lena"].CreateIdea(CreateIdeaInput{Project: roleProject, Title: "Ganz etwas anderes", Actor: ideaActor("lena")}); err != nil {
		t.Fatalf("different idea: %v", err)
	}
	if _, err := w.pa["lena"].CreateIdea(CreateIdeaInput{Project: roleProject, Title: "Dunkles Thema für die Oberfläche", AllowDuplicate: true, Actor: ideaActor("lena")}); err != nil {
		t.Fatalf("allowed duplicate: %v", err)
	}
}

func TestIdeaStatesAndPromotionLink(t *testing.T) {
	w := newPainWorld(t)
	i := newIdea(t, w, "mia", "Schnellsuche")
	if _, err := w.pa["mia"].SetIdeaState(i.ID, idea.Dismissed, " "); ruleCode(err) != "reason_required" {
		t.Fatalf("dismiss needs a reason: %v", err)
	}
	if _, err := w.pa["mia"].SetIdeaState(i.ID, idea.Accepted, ""); ruleCode(err) != "request_required" {
		t.Fatalf("accepting without a request: %v", err)
	}
	if _, err := w.pa["mia"].SetIdeaState(i.ID, idea.Dismissed, "zwei\nZeilen"); ruleCode(err) != "reason_invalid" {
		t.Fatalf("a multi-line reason: %v", err)
	}
	d, err := w.pa["mia"].SetIdeaState(i.ID, idea.Dismissed, "gibt es schon")
	if err != nil || d.State != idea.Dismissed || d.Reason != "gibt es schon" {
		t.Fatalf("dismiss: %+v %v", d, err)
	}
	// Verworfene Ideen bleiben suchbar.
	list, _ := w.pa["mia"].SearchIdeas(idea.Filter{Project: roleProject, State: "dismissed"})
	if len(list) != 1 {
		t.Fatalf("dismissed ideas are searchable: %v", list)
	}
	open, _ := w.pa["mia"].SearchIdeas(idea.Filter{Project: roleProject})
	if len(open) != 0 {
		t.Fatalf("default is the open ones: %v", open)
	}
	if d, err = w.pa["mia"].SetIdeaState(i.ID, idea.Open, ""); err != nil || d.State != idea.Open || d.Reason != "" {
		t.Fatalf("reopen: %+v %v", d, err)
	}
	// Daraus Issues machen: Verknüpfung und Zustand, beidseitig sichtbar.
	req1, req2 := w.request(t, "Suche bauen"), w.request(t, "Suche testen")
	d, err = w.pa["mia"].LinkIdeaRequest(i.ID, req1, "mia")
	if err != nil || d.State != idea.Accepted || len(d.Requests) != 1 {
		t.Fatalf("link: %+v %v", d, err)
	}
	if d, err = w.pa["mia"].LinkIdeaRequest(i.ID, req2, "mia"); err != nil || len(d.Requests) != 2 {
		t.Fatalf("second request: %+v %v", d, err)
	}
	if d, err = w.pa["mia"].LinkIdeaRequest(i.ID, req2, "mia"); err != nil || len(d.Requests) != 2 {
		t.Fatalf("linking twice: %+v %v", d, err)
	}
	if _, err := w.pa["mia"].SetIdeaState(i.ID, idea.Open, ""); ruleCode(err) != "invalid_transition" {
		t.Fatalf("an accepted idea stays accepted: %v", err)
	}
	detail, err := w.st.RequestByID(req1)
	if err != nil {
		t.Fatal(err)
	}
	view := w.pa["mia"].RequestDetailView(detail)
	if len(view.Ideas) != 1 || view.Ideas[0].ID != i.ID || view.Ideas[0].State != idea.Accepted {
		t.Fatalf("the request shows the idea it came from: %+v", view.Ideas)
	}
	// Ein Request eines anderen Projekts lässt sich nicht verknüpfen.
	if _, err := w.pa["mia"].LinkIdeaRequest(i.ID, 99999, "mia"); ruleCode(err) != "request_not_found" {
		t.Fatalf("unknown request: %v", err)
	}
}

func TestIdeaRights(t *testing.T) {
	w := newPainWorld(t)
	i := newIdea(t, w, "mia", "Etwas")
	// Der Gast liest, legt aber nichts an und ändert nichts.
	if _, err := w.pa["gus"].IdeaView(i.ID); err != nil {
		t.Fatalf("guest reads: %v", err)
	}
	if _, err := w.pa["gus"].CreateIdea(CreateIdeaInput{Project: roleProject, Title: "x"}); !errors.Is(err, ErrAccessForbidden) {
		t.Fatalf("guest creates: %v", err)
	}
	if _, err := w.pa["gus"].SetIdeaState(i.ID, idea.Dismissed, "weil"); !errors.Is(err, ErrAccessForbidden) {
		t.Fatalf("guest edits: %v", err)
	}
	// Ein anderes Mitglied ändert die Idee eines anderen nicht, ein Lead schon.
	if _, err := w.pa["rex"].SetIdeaState(i.ID, idea.Dismissed, "weil"); !errors.Is(err, ErrAccessForbidden) {
		t.Fatalf("foreign member edits: %v", err)
	}
	if _, err := w.pa["lena"].SetIdeaState(i.ID, idea.Dismissed, "weil"); err != nil {
		t.Fatalf("lead edits: %v", err)
	}
	// Ein Fremder sieht weder die Idee noch erfährt er, dass es sie gibt.
	_, errReal := w.pa["nora"].IdeaView(i.ID)
	_, errUnknown := w.pa["nora"].IdeaView(99999)
	if ruleCode(errReal) != "idea_not_found" || ruleCode(errUnknown) != "idea_not_found" || errReal.Error() == "" ||
		strings.ReplaceAll(errReal.Error(), "IDEA-1", "IDEA-N") != strings.ReplaceAll(errUnknown.Error(), "IDEA-99999", "IDEA-N") {
		t.Fatalf("stranger: real=%v unknown=%v", errReal, errUnknown)
	}
	if _, err := w.pa["nora"].SetIdeaState(i.ID, idea.Open, ""); ruleCode(err) != "idea_not_found" {
		t.Fatalf("stranger edits: %v", err)
	}
	if list, err := w.pa["nora"].SearchIdeas(idea.Filter{Project: roleProject, State: "all"}); err != nil || len(list) != 0 {
		t.Fatalf("stranger searches: %v %v", list, err)
	}
	if _, err := w.pa["nora"].CreateIdea(CreateIdeaInput{Project: roleProject, Title: "y"}); err == nil {
		t.Fatal("stranger creates")
	}
	// Eine Idee eines fremden Projekts sieht der Gast nicht.
	other := "github.com/acme/other"
	if _, err := w.st.EnsureProject("person:1", other); err != nil {
		t.Fatal(err)
	}
	if _, err := w.pa["robin"].CreateIdea(CreateIdeaInput{Project: other, Title: "Geheim", Actor: ideaActor("robin")}); err != nil {
		t.Fatal(err)
	}
	if list, err := w.pa["gus"].SearchIdeas(idea.Filter{Project: other, State: "all"}); err != nil || len(list) != 0 {
		t.Fatalf("guest sees the ideas of a project he is not in: %v %v", list, err)
	}
}

func TestIdeaStoresAnchorAndOriginLikeAPain(t *testing.T) {
	w := newPainWorld(t)
	origin := &originctx.Context{SessionRef: "sess-1", CapturedAt: "2026-11-05T10:00:00Z",
		Paths: []originctx.Path{{Path: "internal/store/x.go", Kind: "write", Weight: 3}}, Areas: []originctx.Area{{Path: "internal/store", Weight: 3}}}
	i, err := w.pa["mia"].CreateIdea(CreateIdeaInput{Project: roleProject, Title: "Mit Kontext",
		Actor: idea.Actor{Person: "mia", GitAnchor: &gitanchor.Anchor{Commit: "abcdef1234567", Branch: "feat/x"}, Origin: origin}})
	if err != nil {
		t.Fatal(err)
	}
	if i.GitAnchor == nil || i.GitAnchor.Commit != "abcdef1234567" || i.OriginContext == nil || i.OriginContext.Main() != "internal/store" {
		t.Fatalf("idea meta: %+v / %+v", i.GitAnchor, i.OriginContext)
	}
	f, err := w.st.ObjectFacts("idea", []int64{i.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got := f[i.ID]; !hasString(got.Branches, "feat/x") || !hasString(got.Areas, "internal/store") || got.OriginAt != "2026-11-05T10:00:00Z" {
		t.Fatalf("facts: %+v", got)
	}
}
