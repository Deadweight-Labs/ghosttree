package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// Jedes Kriterium verweist auf seine Anforderung; umgesetzt ist die Idee erst,
// wenn jede Anforderung ein erfülltes Kriterium hat oder erlassen ist.
func TestEveryCriterionPointsAtItsRequirementAndTheIdeaIsDoneOnlyWhenCovered(t *testing.T) {
	w := newPainWorld(t)
	i := w.approvedIdea(t, "Rückverfolgung", "Texte bleiben erhalten", "Freigabe nur durch Owner", "Bilder werden geprüft")
	res, err := w.pa["mia"].CreateIssuesFromIdea(IdeaIssuesInput{Idea: i.ID, Branch: "feat/rueck", Actor: w.actor("mia"),
		Issues: []IssueSpec{{Title: "Texte und Freigabe", Requirements: []int{1, 2}, Criteria: []string{"Eigenes Kriterium ohne Anforderung"}}, {Title: "Bilder", Requirements: []int{3}}}})
	if err != nil {
		t.Fatal(err)
	}
	d1, _ := w.st.RequestByID(res.Requests[0])
	view := w.pa["gus"].RequestDetailView(d1)
	for n, want := range []string{"Texte bleiben erhalten", "Freigabe nur durch Owner"} {
		r := view.Criteria[n].Requirement
		if r == nil || r.Idea != i.ID || r.No != n+1 || r.Text != want {
			t.Fatalf("criterion %d does not point at its requirement: %+v", n+1, r)
		}
	}
	if view.Criteria[2].Requirement != nil {
		t.Fatalf("a criterion of its own points at a requirement: %+v", view.Criteria[2].Requirement)
	}
	// Wer die Idee nicht lesen darf, sieht den Verweis nicht.
	if got := w.pa["nora"].RequestDetailView(d1); got.Criteria[0].Requirement != nil {
		t.Fatalf("a stranger sees the requirement: %+v", got.Criteria[0].Requirement)
	}
	// Alles unabgedeckt: an der Idee, in der Prüfansicht des Pakets.
	cov, err := w.pa["gus"].IdeaCoverage(i.ID)
	if err != nil || len(cov) != 3 || cov[0].State != idea.CoverOpen || len(cov[0].Criteria) != 1 {
		t.Fatalf("coverage: %+v %v", cov, err)
	}
	got, _ := w.pa["mia"].IdeaView(i.ID)
	if got.UncoveredRequirements != 3 || got.Phase != idea.PhaseBuilding {
		t.Fatalf("the idea does not name its uncovered requirements: %+v", got)
	}
	pkg, _ := w.st.PackageByID(res.Package.ID)
	review := w.pa["mia"].PackageView(pkg, true)
	if len(review.Uncovered) != 3 || review.Uncovered[0].Idea != i.ID || review.Uncovered[0].State != idea.CoverOpen {
		t.Fatalf("package review: %+v", review.Uncovered)
	}
	text := requestdomain.RenderPackageReview(review)
	if !strings.Contains(text, "NOT READY") || !strings.Contains(text, "IDEA-1#1 Texte bleiben erhalten") || !strings.Contains(text, "3 requirements of the ideas not covered") {
		t.Fatalf("review text:\n%s", text)
	}
	if brief := w.pa["mia"].PackageView(pkg, false); len(brief.Uncovered) != 0 {
		t.Error("the brief view carries the review list")
	}
	// Eine erfüllte Anforderung ist abgedeckt, nicht mehr aufgeführt.
	ev := requestdomain.Evidence{Kind: "test", Ref: "TestX", Person: "mia"}
	met := func(requestID int64, number int) {
		t.Helper()
		d, _ := w.st.RequestByID(requestID)
		if err := w.st.SetCriterionState(d.Criteria[number-1].ID, "met", ev); err != nil {
			t.Fatal(err)
		}
	}
	met(res.Requests[0], 1)
	if cov, _ = w.pa["mia"].IdeaCoverage(i.ID); cov[0].State != idea.CoverMet || cov[1].State != idea.CoverOpen {
		t.Fatalf("after one met criterion: %+v", cov)
	}
	// Nicht fertig: Issues offen, Anforderungen offen.
	if _, err := w.pa["mia"].SetIdeaPhase(i.ID, idea.PhaseDone, w.actor("mia"), headRev(w.st, i.ID)); ruleCode(err) != "requests_open" {
		t.Fatalf("done with open issues: %v", err)
	}
	// Alle Issues zu, Anforderungen 2 und 3 ohne erfülltes Kriterium.
	d1, _ = w.st.RequestByID(res.Requests[0])
	d2, _ := w.st.RequestByID(res.Requests[1])
	for _, c := range d1.Criteria[1:] {
		w.st.SetCriterionState(c.ID, "waived", ev)
	}
	for _, c := range d2.Criteria {
		w.st.SetCriterionState(c.ID, "waived", ev)
	}
	for _, r := range res.Requests {
		if err := w.st.CompleteRequest(r, ev); err != nil {
			t.Fatal(err)
		}
	}
	_, err = w.pa["mia"].SetIdeaPhase(i.ID, idea.PhaseDone, w.actor("mia"), headRev(w.st, i.ID))
	if ruleCode(err) != "requirements_uncovered" || !strings.Contains(err.Error(), "2, 3") {
		t.Fatalf("done with uncovered requirements: %v", err)
	}
	// Ein verzichtetes Kriterium deckt nichts. Erlassen wird ausdrücklich, mit Grund, von einem Menschen.
	if _, err := w.pa["mia"].WaiveIdeaRequirement(i.ID, 2, "brauchen wir nicht", w.agent("mia")); ruleCode(err) != "human_only" {
		t.Fatalf("an agent waives a requirement: %v", err)
	}
	if _, err := w.pa["mia"].WaiveIdeaRequirement(i.ID, 2, " ", w.actor("mia")); ruleCode(err) != "reason_required" {
		t.Fatalf("waiving needs a reason: %v", err)
	}
	if _, err := w.pa["mia"].WaiveIdeaRequirement(i.ID, 9, "x", w.actor("mia")); ruleCode(err) != "requirement_not_found" {
		t.Fatalf("a requirement that does not exist: %v", err)
	}
	if _, err := w.pa["rex"].WaiveIdeaRequirement(i.ID, 2, "x", w.actor("rex")); !errors.Is(err, ErrAccessForbidden) {
		t.Fatalf("a member who does not own the idea waives: %v", err)
	}
	if cov, err = w.pa["mia"].WaiveIdeaRequirement(i.ID, 2, "brauchen wir nicht", w.actor("mia")); err != nil || cov[1].State != idea.CoverWaived || cov[1].Reason != "brauchen wir nicht" {
		t.Fatalf("waive: %+v %v", cov, err)
	}
	if _, err := w.pa["mia"].SetIdeaPhase(i.ID, idea.PhaseDone, w.actor("mia"), headRev(w.st, i.ID)); ruleCode(err) != "requirements_uncovered" || !strings.Contains(err.Error(), "requirements 3 ") {
		t.Fatalf("one requirement still uncovered: %v", err)
	}
	// Die letzte Anforderung wird ebenfalls ausdrücklich erlassen; erfüllt waren die anderen.
	if _, err := w.pa["mia"].WaiveIdeaRequirement(i.ID, 3, "kommt mit einer späteren Idee", w.actor("mia")); err != nil {
		t.Fatal(err)
	}
	done, err := w.pa["mia"].SetIdeaPhase(i.ID, idea.PhaseDone, w.actor("mia"), headRev(w.st, i.ID))
	if err != nil || done.Phase != idea.PhaseDone || done.UncoveredRequirements != 0 {
		t.Fatalf("done: %+v %v", done, err)
	}
	if after := w.pa["mia"].PackageView(pkg, true); len(after.Uncovered) != 0 {
		t.Fatalf("the review still lists %+v", after.Uncovered)
	}
	if _, err := w.pa["gus"].IdeaCoverage(i.ID + 40); ruleCode(err) != "idea_not_found" {
		t.Fatalf("%v", err)
	}
	if _, err := w.pa["nora"].IdeaCoverage(i.ID); ruleCode(err) != "idea_not_found" {
		t.Fatalf("a stranger reads the coverage: %v", err)
	}
}

// Ändert jemand den Text einer Anforderung, ist sie wieder unabgedeckt, bis ein
// Kriterium zu ihr passt.
func TestEditingARequirementMakesItUncoveredAgain(t *testing.T) {
	w := newPainWorld(t)
	i := w.approvedIdea(t, "Textänderung", "Alt")
	res, _ := w.pa["mia"].CreateIssuesFromIdea(IdeaIssuesInput{Idea: i.ID, Actor: w.actor("mia")})
	d, _ := w.st.RequestByID(res.Requests[0])
	w.st.SetCriterionState(d.Criteria[0].ID, "met", requestdomain.Evidence{Kind: "test", Ref: "T", Person: "mia"})
	if cov, _ := w.pa["mia"].IdeaCoverage(i.ID); cov[0].State != idea.CoverMet {
		t.Fatalf("%+v", cov)
	}
	neu := []string{"Neu formuliert"}
	if _, _, err := w.pa["mia"].PutIdeaConcept(i.ID, PutIdeaConceptInput{Patch: idea.ConceptPatch{Requirements: &neu}, Actor: w.actor("mia")}); err != nil {
		t.Fatal(err)
	}
	// Nach der Freigabe bleibt die alte Anforderung (met) in der Liste, die neu
	// formulierte kommt unabgedeckt dazu.
	cov, _ := w.pa["mia"].IdeaCoverage(i.ID)
	if len(cov) != 2 || cov[0].Text != "Alt" || cov[0].State != idea.CoverMet || cov[1].Text != "Neu formuliert" || cov[1].State != idea.CoverNone {
		t.Fatalf("an edited requirement: %+v", cov)
	}
}

// Ein Token ist kein Mensch: Freigeben, menschliche Entscheidungen, Erlassen und
// die Policy gehören der Websitzung.
func TestOnlyAWebSessionIsAPerson(t *testing.T) {
	w := newPainWorld(t)
	token := func(who string) idea.Actor {
		p := Principal{ID: w.pa[who].principal.ID, Label: who, TokenID: 7, TokenKind: "pat"}
		return IdeaActorFor(p, "session:1")
	}
	web := func(who string) idea.Actor {
		return IdeaActorFor(Principal{ID: w.pa[who].principal.ID, Label: who, TokenKind: WebSessionKind}, "")
	}
	if a := token("mia"); !a.Agent || a.AccountID == 0 || a.Person != "mia" {
		t.Fatalf("token actor: %+v", a)
	}
	if a := IdeaActorFor(Principal{ID: w.pa["mia"].principal.ID, Label: "mia", TokenID: 7, TokenKind: "machine"}, ""); !a.Agent {
		t.Fatal("a token without a session id counts as a person")
	}
	if a := web("mia"); a.Agent {
		t.Fatalf("web actor: %+v", a)
	}
	i := w.approvedUnder(t)
	// Freigeben.
	raw := w.workshopIdea(t, "Zur Freigabe")
	about := "x"
	w.pa["mia"].PutIdeaConcept(raw.ID, PutIdeaConceptInput{Patch: idea.ConceptPatch{About: &about}, Actor: token("mia")})
	if _, err := w.pa["mia"].SetIdeaPhase(raw.ID, idea.PhaseApproved, token("mia"), headRev(w.st, raw.ID)); ruleCode(err) != "human_only" {
		t.Fatalf("a token approves: %v", err)
	}
	if got, err := w.pa["mia"].SetIdeaPhase(raw.ID, idea.PhaseApproved, web("mia"), headRev(w.st, raw.ID)); err != nil || got.Phase != idea.PhaseApproved {
		t.Fatalf("a web session approves: %+v %v", got, err)
	}
	// Auch der Weg zurück aus der Freigabe.
	if _, err := w.pa["mia"].SetIdeaPhase(raw.ID, idea.PhaseDiscussion, token("mia"), headRev(w.st, raw.ID)); ruleCode(err) != "human_only" {
		t.Fatalf("a token takes the approval back: %v", err)
	}
	// Menschliche Entscheidung.
	d := w.openOn(t, "mia", idea.SubjectIdea, i.ID, "Rechtefrage", token("mia"), withAspects(idea.AspectSecurity))
	if _, err := w.pa["mia"].CloseDecision(CloseDecisionInput{ID: d.ID, Outcome: idea.DecisionDecided, Answer: "ja", Reason: "weil", Actor: token("mia")}); ruleCode(err) != "human_decision" {
		t.Fatalf("a token closes a human decision: %v", err)
	}
	// Stimmen: als Agent gekennzeichnet, solange es ein Token ist.
	v := w.openOn(t, "mia", idea.SubjectIdea, i.ID, "Mit Varianten", web("mia"), withVariants(idea.VariantInput{Title: "A"}, idea.VariantInput{Title: "B"}))
	w.pa["mia"].VoteDecision(v.ID, "A", "per Token", token("mia"))
	w.pa["mia"].VoteDecision(v.ID, "B", "als Person", web("mia"))
	got, _ := w.pa["mia"].DecisionView(v.ID)
	agentVotes, personVotes := 0, 0
	for _, vt := range got.Votes {
		if vt.ByAgent {
			agentVotes++
		} else {
			personVotes++
		}
	}
	if agentVotes != 1 || personVotes != 1 {
		t.Fatalf("votes: %+v", got.Votes)
	}
	if after, err := w.pa["mia"].CloseDecision(CloseDecisionInput{ID: d.ID, Outcome: idea.DecisionDecided, Answer: "ja", Reason: "weil", Actor: web("mia")}); err != nil || after.ByAgent {
		t.Fatalf("a web session closes it: %+v %v", after, err)
	}
	// Eine menschliche Entscheidung wieder öffnen.
	if _, err := w.pa["mia"].ReopenDecision(d.ID, "nochmal", token("mia")); ruleCode(err) != "human_only" {
		t.Fatalf("a token overturns: %v", err)
	}
	if _, err := w.pa["mia"].ReopenDecision(d.ID, "nochmal", web("mia")); err != nil {
		t.Fatalf("a web session overturns: %v", err)
	}
	// Policy und Art.
	if err := w.pa["lena"].SetDecisionPolicy(roleProject, idea.PolicyRule, "lena", token("lena")); ruleCode(err) != "human_only" {
		t.Fatalf("a token changes the policy: %v", err)
	}
	if err := w.pa["lena"].SetDecisionPolicy(roleProject, idea.PolicyRule, "lena", web("lena")); err != nil {
		t.Fatalf("a web session changes the policy: %v", err)
	}
	e := w.openOn(t, "mia", idea.SubjectIdea, i.ID, "Art", token("mia"))
	if _, err := w.pa["mia"].SetDecisionWay(e.ID, idea.WayHuman, token("mia")); ruleCode(err) != "human_only" {
		t.Fatalf("a token changes the way: %v", err)
	}
	if _, err := w.pa["mia"].SetDecisionWay(e.ID, idea.WayHuman, web("mia")); err != nil {
		t.Fatalf("a web session changes the way: %v", err)
	}
}

func (w *painWorld) approvedUnder(t *testing.T) idea.Idea {
	t.Helper()
	return w.workshopIdea(t, "Menschenfragen")
}

// Ein Agent kommt weder an den Anforderungen einer freigegebenen Idee vorbei noch
// setzt er „umgesetzt“ noch umgeht er die Freigabe über „in Umsetzung“.
func TestAnAgentCannotWorkAroundTheApproval(t *testing.T) {
	w := newPainWorld(t)
	token := w.agent("mia")
	i := w.workshopIdea(t, "Freigabe umgehen")
	reqs := []string{"Erste Anforderung", "Unbequeme Anforderung"}
	about := "Worum"
	if _, _, err := w.pa["mia"].PutIdeaConcept(i.ID, PutIdeaConceptInput{Patch: idea.ConceptPatch{About: &about, Requirements: &reqs}, Actor: token}); err != nil {
		t.Fatalf("before the approval an agent writes the requirements: %v", err)
	}
	// Ohne Freigabe: weder Phase building noch Issues, auch nicht mit gefälschter Phase.
	if _, err := w.pa["mia"].SetIdeaPhase(i.ID, idea.PhaseBuilding, w.actor("mia"), headRev(w.st, i.ID)); ruleCode(err) != "not_approved" {
		t.Fatalf("raw to building: %v", err)
	}
	if _, err := w.pa["mia"].CreateIssuesFromIdea(IdeaIssuesInput{Idea: i.ID, Actor: token}); ruleCode(err) != "not_approved" {
		t.Fatalf("issues from an idea nobody approved: %v", err)
	}
	if _, err := w.st.DB().Exec(`UPDATE ideas SET phase='approved' WHERE id=?`, i.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.pa["mia"].CreateIssuesFromIdea(IdeaIssuesInput{Idea: i.ID, Actor: w.actor("mia")}); ruleCode(err) != "not_approved" || !strings.Contains(err.Error(), "approval of a person") {
		t.Fatalf("a phase without the approval marker: %v", err)
	}
	if _, err := w.st.DB().Exec(`UPDATE ideas SET phase='concept' WHERE id=?`, i.ID); err != nil {
		t.Fatal(err)
	}
	// Ein Mensch gibt frei: der Marker hält Person, Fassung und Anforderungen.
	if _, err := w.pa["mia"].SetIdeaPhase(i.ID, idea.PhaseApproved, token, headRev(w.st, i.ID)); ruleCode(err) != "human_only" {
		t.Fatalf("an agent approves: %v", err)
	}
	if _, err := w.pa["mia"].SetIdeaPhase(i.ID, idea.PhaseApproved, w.actor("mia"), headRev(w.st, i.ID)); err != nil {
		t.Fatal(err)
	}
	var who string
	var revision int
	var snapshot string
	if err := w.st.DB().QueryRow(`SELECT person, concept_revision, requirements FROM idea_approvals WHERE idea_id=? AND revoked_at=''`, i.ID).Scan(&who, &revision, &snapshot); err != nil ||
		who != "mia" || revision != 1 || !strings.Contains(snapshot, "Unbequeme Anforderung") {
		t.Fatalf("approval marker: %q %d %s %v", who, revision, snapshot, err)
	}
	// Nach der Freigabe ändert ein Agent die Anforderungen nicht; den Rest des Konzepts schon.
	fewer := []string{"Erste Anforderung"}
	if _, _, err := w.pa["mia"].PutIdeaConcept(i.ID, PutIdeaConceptInput{Patch: idea.ConceptPatch{Requirements: &fewer}, Actor: token}); ruleCode(err) != "human_only" {
		t.Fatalf("an agent drops a requirement after the approval: %v", err)
	}
	how := "So geht es"
	if _, _, err := w.pa["mia"].PutIdeaConcept(i.ID, PutIdeaConceptInput{Patch: idea.ConceptPatch{How: &how}, Actor: token}); err != nil {
		t.Fatalf("an agent writes the rest of an approved concept: %v", err)
	}
	// Issues entstehen, alles wird erledigt bis auf die unbequeme Anforderung.
	res, err := w.pa["mia"].CreateIssuesFromIdea(IdeaIssuesInput{Idea: i.ID, Actor: token, Issues: []IssueSpec{{Title: "Nur die erste", Requirements: []int{1}}}})
	if err != nil || res.Idea.Phase != idea.PhaseBuilding {
		t.Fatalf("%+v %v", res, err)
	}
	ev := requestdomain.Evidence{Kind: "test", Ref: "T", Person: "mia"}
	d, _ := w.st.RequestByID(res.Requests[0])
	w.st.SetCriterionState(d.Criteria[0].ID, "met", ev)
	if err := w.st.CompleteRequest(res.Requests[0], ev); err != nil {
		t.Fatal(err)
	}
	// Ein Mensch streicht die unbequeme Anforderung aus dem Konzept: die Abdeckung rechnet gegen die Freigabe.
	if _, _, err := w.pa["mia"].PutIdeaConcept(i.ID, PutIdeaConceptInput{Patch: idea.ConceptPatch{Requirements: &fewer}, Actor: w.actor("mia")}); err != nil {
		t.Fatalf("a person edits the requirements: %v", err)
	}
	cov, _ := w.pa["mia"].IdeaCoverage(i.ID)
	if len(cov) != 2 || cov[1].Text != "Unbequeme Anforderung" || cov[1].State != idea.CoverNone {
		t.Fatalf("a dropped requirement vanished from the coverage: %+v", cov)
	}
	if got, _ := w.pa["mia"].IdeaView(i.ID); got.UncoveredRequirements != 1 {
		t.Fatalf("%+v", got)
	}
	// „Umgesetzt“ setzt nur ein Mensch, und nur wenn die Freigabe-Anforderungen gedeckt sind.
	if _, err := w.pa["mia"].SetIdeaPhase(i.ID, idea.PhaseDone, token, headRev(w.st, i.ID)); ruleCode(err) != "human_only" {
		t.Fatalf("an agent marks it done: %v", err)
	}
	if _, err := w.pa["mia"].SetIdeaPhase(i.ID, idea.PhaseDone, w.actor("mia"), headRev(w.st, i.ID)); ruleCode(err) != "requirements_uncovered" {
		t.Fatalf("done with the requirement that was dropped: %v", err)
	}
	if _, err := w.pa["mia"].WaiveIdeaRequirement(i.ID, 2, "kommt später", w.actor("mia")); err != nil {
		t.Fatal(err)
	}
	if done, err := w.pa["mia"].SetIdeaPhase(i.ID, idea.PhaseDone, w.actor("mia"), headRev(w.st, i.ID)); err != nil || done.Phase != idea.PhaseDone {
		t.Fatalf("%+v %v", done, err)
	}
}

// Wer die Freigabe zurücknimmt, widerruft den Marker: Issues brauchen eine neue.
func TestTakingTheApprovalBackRevokesTheMarker(t *testing.T) {
	w := newPainWorld(t)
	i := w.approvedIdea(t, "Widerruf", "a")
	if _, err := w.pa["mia"].SetIdeaPhase(i.ID, idea.PhaseDiscussion, w.agent("mia"), headRev(w.st, i.ID)); ruleCode(err) != "human_only" {
		t.Fatalf("an agent takes the approval back: %v", err)
	}
	if _, err := w.pa["mia"].SetIdeaPhase(i.ID, idea.PhaseDiscussion, w.actor("mia"), headRev(w.st, i.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.pa["mia"].CreateIssuesFromIdea(IdeaIssuesInput{Idea: i.ID, Actor: w.actor("mia")}); ruleCode(err) != "not_approved" {
		t.Fatalf("issues after the approval was taken back: %v", err)
	}
	if _, err := w.pa["mia"].SetIdeaPhase(i.ID, idea.PhaseApproved, w.actor("mia"), headRev(w.st, i.ID)); err != nil {
		t.Fatal(err)
	}
	var active, revoked int
	w.st.DB().QueryRow(`SELECT COUNT(*) FROM idea_approvals WHERE idea_id=? AND revoked_at=''`, i.ID).Scan(&active)
	w.st.DB().QueryRow(`SELECT COUNT(*) FROM idea_approvals WHERE idea_id=? AND revoked_at!=''`, i.ID).Scan(&revoked)
	if active != 1 || revoked != 1 {
		t.Fatalf("markers: %d active, %d revoked", active, revoked)
	}
	if _, err := w.pa["mia"].CreateIssuesFromIdea(IdeaIssuesInput{Idea: i.ID, Actor: w.agent("mia")}); err != nil {
		t.Fatalf("an agent creates issues from an approved idea: %v", err)
	}
}

// Ein Mensch gibt die Fassung frei, die er gesehen hat: schreibt ein Agent nach der
// Anzeige ein neues Konzept, wird die Freigabe mit der alten Fassung abgelehnt.
func TestApprovalIsForTheVersionThatWasSeen(t *testing.T) {
	w := newPainWorld(t)
	i := w.workshopIdea(t, "Gesehen")
	reqs := []string{"Harmlos"}
	about := "x"
	w.pa["mia"].PutIdeaConcept(i.ID, PutIdeaConceptInput{Patch: idea.ConceptPatch{About: &about, Requirements: &reqs}, Actor: w.agent("mia")})
	seen := headRev(w.st, i.ID)
	// Nach der Anzeige schreibt ein Agent etwas anderes.
	evil := []string{"Etwas, das nie gesehen wurde"}
	if _, _, err := w.pa["mia"].PutIdeaConcept(i.ID, PutIdeaConceptInput{Patch: idea.ConceptPatch{Requirements: &evil}, Actor: w.agent("mia")}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.pa["mia"].SetIdeaPhase(i.ID, idea.PhaseApproved, w.actor("mia"), seen); ruleCode(err) != "concept_changed" {
		t.Fatalf("approving a version that is not the newest: %v", err)
	}
	if _, err := w.pa["mia"].SetIdeaPhase(i.ID, idea.PhaseApproved, w.actor("mia"), 0); ruleCode(err) != "revision_required" {
		t.Fatalf("approving without a version: %v", err)
	}
	if got, _ := w.pa["mia"].IdeaView(i.ID); got.Phase == idea.PhaseApproved {
		t.Fatal("the idea was approved anyway")
	}
	if _, err := w.pa["mia"].SetIdeaPhase(i.ID, idea.PhaseApproved, w.actor("mia"), headRev(w.st, i.ID)); err != nil {
		t.Fatal(err)
	}
	var rev int
	var snap string
	w.st.DB().QueryRow(`SELECT concept_revision, requirements FROM idea_approvals WHERE idea_id=? AND revoked_at=''`, i.ID).Scan(&rev, &snap)
	if rev != seen+1 || !strings.Contains(snap, "nie gesehen") {
		t.Fatalf("the marker holds exactly the approved version: %d %s", rev, snap)
	}
}
