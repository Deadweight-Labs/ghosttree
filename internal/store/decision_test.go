package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
)

func (w *painWorld) openOn(t *testing.T, who string, subject string, id int64, q string, actor idea.Actor, opts ...func(*OpenDecisionInput)) idea.Decision {
	t.Helper()
	in := OpenDecisionInput{Subject: subject, SubjectID: id, Question: q, Actor: actor}
	for _, o := range opts {
		o(&in)
	}
	d, err := w.pa[who].OpenDecision(in)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func withAspects(a ...string) func(*OpenDecisionInput) {
	return func(in *OpenDecisionInput) { in.Aspects = a }
}

func withVariants(v ...idea.VariantInput) func(*OpenDecisionInput) {
	return func(in *OpenDecisionInput) { in.Variants = v }
}

func knowledgeOf(t *testing.T, w *painWorld, id int64) Knowledge {
	t.Helper()
	k, err := w.st.KnowledgeByID(id)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// (b) Der Agent entscheidet, was umkehrbar, lokal und konventionsgemäß ist: das
// hält als Wissen decision „vom Agenten“ fest, blockiert nichts, ein Mensch kippt es.
func TestAnAgentDecidesWhatIsReversibleLocalAndConventional(t *testing.T) {
	w := newPainWorld(t)
	i := w.workshopIdea(t, "Agentenentscheidung")
	d := w.openOn(t, "mia", idea.SubjectIdea, i.ID, "Wie heißt die Tabelle", w.agent("mia"))
	if d.Way != idea.WayAgent || d.State != idea.DecisionOpen || d.NeedsHuman() {
		t.Fatalf("rule: %+v", d)
	}
	if _, err := w.pa["mia"].CloseDecision(CloseDecisionInput{ID: d.ID, Outcome: idea.DecisionDecided, Answer: "decision_events", Actor: w.agent("mia")}); ruleCode(err) != "reason_required" {
		t.Fatalf("a decision needs its reason: %v", err)
	}
	got, err := w.pa["mia"].CloseDecision(CloseDecisionInput{ID: d.ID, Outcome: idea.DecisionDecided, Answer: "decision_events", Reason: "so heißen die anderen Tabellen auch", Actor: w.agent("mia")})
	if err != nil || got.State != idea.DecisionDecided || !got.ByAgent || got.KnowledgeID == 0 {
		t.Fatalf("%+v %v", got, err)
	}
	k := knowledgeOf(t, w, got.KnowledgeID)
	if k.Type != "decision" || !strings.HasPrefix(k.Title, "vom Agenten: ") || !strings.Contains(k.Body, "so heißen die anderen Tabellen auch") ||
		!strings.Contains(k.Body, "overturn") || k.Scope.Project != roleProject || k.Status != "active" || k.Confidence != "trusted" {
		t.Fatalf("knowledge: %+v", k)
	}
	// Der Agent hat nichts blockiert: die Idee läuft weiter, und der Mensch sieht den Hinweis.
	if hints, _ := w.pa["mia"].SearchDecisions(DecisionFilter{Project: roleProject, State: "all", Way: idea.WayAgent}); len(hints) != 1 {
		t.Fatalf("hint list: %+v", hints)
	}
	if inbox, _ := w.pa["mia"].SearchDecisions(DecisionFilter{Project: roleProject, NeedsHuman: true}); len(inbox) != 0 {
		t.Fatalf("an agent decision is in the inbox: %+v", inbox)
	}
	// Ein Agent kippt nicht, ein Mensch schon; neu entschieden löst es das alte Wissen ab.
	if _, err := w.pa["mia"].ReopenDecision(d.ID, "doch anders", w.agent("mia")); ruleCode(err) != "human_only" {
		t.Fatalf("an agent overturns: %v", err)
	}
	re, err := w.pa["mia"].ReopenDecision(d.ID, "ich sehe das anders", w.actor("mia"))
	if err != nil || re.State != idea.DecisionOpen || re.Way != idea.WayHuman {
		t.Fatalf("overturned: %+v %v", re, err)
	}
	again, err := w.pa["mia"].CloseDecision(CloseDecisionInput{ID: d.ID, Outcome: idea.DecisionDecided, Answer: "idea_decisions", Reason: "klarer", Actor: w.actor("mia")})
	if err != nil || again.ByAgent || again.KnowledgeID == got.KnowledgeID {
		t.Fatalf("%+v %v", again, err)
	}
	if old := knowledgeOf(t, w, got.KnowledgeID); old.Status != "superseded" || old.SupersededBy != again.KnowledgeID {
		t.Fatalf("the agent's entry should be superseded: %+v", old)
	}
	if k := knowledgeOf(t, w, again.KnowledgeID); strings.HasPrefix(k.Title, "vom Agenten") || k.Origin != "human" {
		t.Fatalf("a person's decision is not marked as the agent's: %+v", k)
	}
}

// (a) Was einen Menschen verlangt, entscheidet kein Agent und steht im Posteingang.
func TestAHumanDecisionWaitsForAPersonAndShowsInTheInbox(t *testing.T) {
	w := newPainWorld(t)
	i := w.workshopIdea(t, "Menschenentscheidung")
	for _, aspect := range []string{idea.AspectSecurity, idea.AspectCost, idea.AspectExternal, idea.AspectDataModel, idea.AspectProduct, idea.AspectPeople, idea.AspectIrreversible} {
		d := w.openOn(t, "mia", idea.SubjectIdea, i.ID, "Frage "+aspect, w.agent("mia"), withAspects(aspect))
		if d.Way != idea.WayHuman || !d.NeedsHuman() || len(d.WayWhy) != 1 || d.Aspects[0] != aspect {
			t.Errorf("%s: %+v", aspect, d)
		}
	}
	d := w.openOn(t, "mia", idea.SubjectIdea, i.ID, "Wer darf das", w.agent("mia"), withAspects(idea.AspectSecurity))
	if _, err := w.pa["mia"].OpenDecision(OpenDecisionInput{Subject: idea.SubjectIdea, SubjectID: i.ID, Question: "x", Way: idea.WayAgent, Aspects: []string{idea.AspectCost}, Actor: w.agent("mia")}); ruleCode(err) != "way_not_allowed" {
		t.Fatalf("an agent takes a human decision: %v", err)
	}
	if _, err := w.pa["mia"].OpenDecision(OpenDecisionInput{Subject: idea.SubjectIdea, SubjectID: i.ID, Question: "x", Aspects: []string{"secuirty"}, Actor: w.agent("mia")}); ruleCode(err) != "aspect_unknown" {
		t.Fatalf("a typo in an aspect: %v", err)
	}
	// Ein Agent kann sie weder entscheiden noch vertagen noch fallenlassen.
	for _, outcome := range []string{idea.DecisionDecided, idea.DecisionDeferred, idea.DecisionDropped} {
		_, err := w.pa["mia"].CloseDecision(CloseDecisionInput{ID: d.ID, Outcome: outcome, Answer: "ja", Reason: "weil", Trigger: "später", Actor: w.agent("mia")})
		if ruleCode(err) != "human_decision" {
			t.Errorf("agent closes a human decision as %s: %v", outcome, err)
		}
	}
	// Ein Mitglied, das die Idee nicht besitzt, entscheidet auch nicht; Owner und Maintainer schon.
	if _, err := w.pa["rex"].CloseDecision(CloseDecisionInput{ID: d.ID, Outcome: idea.DecisionDecided, Answer: "ja", Reason: "weil", Actor: w.actor("rex")}); !errors.Is(err, ErrAccessForbidden) {
		t.Fatalf("a member who is not the owner: %v", err)
	}
	inbox, err := w.pa["gus"].SearchDecisions(DecisionFilter{Project: roleProject, NeedsHuman: true})
	if err != nil || len(inbox) != 8 {
		t.Fatalf("inbox: %d %v", len(inbox), err)
	}
	if got, err := w.pa["lena"].CloseDecision(CloseDecisionInput{ID: d.ID, Outcome: idea.DecisionDecided, Answer: "Nur Maintainer", Reason: "Rechtefrage", Actor: w.actor("lena")}); err != nil || got.ByAgent || got.DecidedBy != "lena" {
		t.Fatalf("a maintainer decides: %+v %v", got, err)
	}
	inbox, _ = w.pa["mia"].SearchDecisions(DecisionFilter{Project: roleProject, NeedsHuman: true})
	if got, err := w.pa["mia"].CloseDecision(CloseDecisionInput{ID: inbox[0].ID, Outcome: idea.DecisionDropped, Reason: "entscheiden wir doch nicht", Actor: w.actor("mia")}); err != nil || got.State != idea.DecisionDropped || got.Way != idea.WayDropped {
		t.Fatalf("the owner drops one: %+v %v", got, err)
	}
	if inbox, _ = w.pa["mia"].SearchDecisions(DecisionFilter{Project: roleProject, NeedsHuman: true}); len(inbox) != 6 {
		t.Fatalf("the inbox shrinks as decisions close: %d", len(inbox))
	}
	// Nach Rechten: wer das Projekt nicht sieht, sieht keine Entscheidung und keinen Posteingang.
	if list, _ := w.pa["nora"].SearchDecisions(DecisionFilter{Project: roleProject, NeedsHuman: true}); len(list) != 0 {
		t.Fatalf("a stranger sees decisions: %+v", list)
	}
}

// (c) Vertagt: kehrt im Briefing zurück, wenn der Auslöser zum Branch oder Paket passt.
func TestADeferredDecisionComesBackWhenItsTriggerMatches(t *testing.T) {
	w := newPainWorld(t)
	i := w.workshopIdea(t, "Vertagt")
	d := w.openOn(t, "mia", idea.SubjectIdea, i.ID, "Wie genau die Gewichtung geht", w.actor("mia"))
	if _, err := w.pa["mia"].CloseDecision(CloseDecisionInput{ID: d.ID, Outcome: idea.DecisionDeferred, Actor: w.actor("mia")}); ruleCode(err) != "trigger_required" {
		t.Fatalf("a deferral needs a trigger: %v", err)
	}
	if _, err := w.pa["mia"].CloseDecision(CloseDecisionInput{ID: d.ID, Outcome: idea.DecisionDeferred, Trigger: "wenn\nwir", Actor: w.actor("mia")}); err == nil {
		t.Fatal("a multi-line trigger")
	}
	got, err := w.pa["mia"].CloseDecision(CloseDecisionInput{ID: d.ID, Outcome: idea.DecisionDeferred, Trigger: "wenn wir die Gewichtung bauen", Actor: w.actor("mia")})
	if err != nil || got.State != idea.DecisionDeferred || got.Way != idea.WayDeferred || got.Trigger == "" || got.Decider != idea.WayHuman {
		t.Fatalf("%+v %v", got, err)
	}
	if got.KnowledgeID != 0 {
		t.Error("a deferral is not knowledge")
	}
	if inbox, _ := w.pa["mia"].SearchDecisions(DecisionFilter{Project: roleProject, NeedsHuman: true}); len(inbox) != 0 {
		t.Fatalf("a deferred decision is not an inbox item: %+v", inbox)
	}
	due, _ := w.pa["mia"].DueDecisions(roleProject, "feat/gewichtung", "Gewichtung der Stimmen")
	if len(due) != 1 || due[0].ID != d.ID {
		t.Fatalf("due: %+v", due)
	}
	if due, _ := w.pa["mia"].DueDecisions(roleProject, "feat/anderes", "Etwas anderes"); len(due) != 0 {
		t.Fatalf("not due: %+v", due)
	}
	if due, _ := w.pa["nora"].DueDecisions(roleProject, "feat/gewichtung"); len(due) != 0 {
		t.Fatalf("a stranger sees a deferred decision: %+v", due)
	}
	// Jeder Mitarbeitende holt sie zurück, auch ein Agent: sie war nur vertagt.
	re, err := w.pa["rex"].ReopenDecision(d.ID, "Auslöser ist da", w.agent("rex"))
	if err != nil || re.State != idea.DecisionOpen || re.Way != idea.WayHuman {
		t.Fatalf("%+v %v", re, err)
	}
}

// (d) Fallengelassen mit Grund.
func TestADroppedDecisionNeedsAReasonAndStaysReadable(t *testing.T) {
	w := newPainWorld(t)
	i := w.workshopIdea(t, "Fallengelassen")
	d := w.openOn(t, "mia", idea.SubjectIdea, i.ID, "Brauchen wir das", w.actor("mia"))
	if _, err := w.pa["mia"].CloseDecision(CloseDecisionInput{ID: d.ID, Outcome: idea.DecisionDropped, Actor: w.actor("mia")}); ruleCode(err) != "reason_required" {
		t.Fatalf("%v", err)
	}
	got, err := w.pa["mia"].CloseDecision(CloseDecisionInput{ID: d.ID, Outcome: idea.DecisionDropped, Reason: "entscheiden wir doch nicht", Actor: w.actor("mia")})
	if err != nil || got.State != idea.DecisionDropped || got.Reason != "entscheiden wir doch nicht" || got.KnowledgeID != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := w.pa["mia"].CloseDecision(CloseDecisionInput{ID: d.ID, Outcome: idea.DecisionDropped, Reason: "nochmal", Actor: w.actor("mia")}); ruleCode(err) != "decision_closed" {
		t.Fatalf("closing twice: %v", err)
	}
	if _, err := w.pa["mia"].CloseDecision(CloseDecisionInput{ID: d.ID, Outcome: "maybe", Actor: w.actor("mia")}); ruleCode(err) != "decision_closed" && ruleCode(err) != "outcome_invalid" {
		t.Fatalf("%v", err)
	}
}

func TestVariantsVotesAndAdoptionIntoKnowledgeAndConcept(t *testing.T) {
	w := newPainWorld(t)
	i := w.workshopIdea(t, "Varianten")
	about := "Anfang"
	w.pa["mia"].PutIdeaConcept(i.ID, PutIdeaConceptInput{Patch: idea.ConceptPatch{About: &about}, Actor: w.actor("mia")})
	d := w.openOn(t, "mia", idea.SubjectIdea, i.ID, "Wo liegen die Bilder", w.actor("mia"), withVariants(
		idea.VariantInput{Title: "In der Datenbank", Text: "Blob-Tabelle", Pro: "ein Backup", Contra: "größere Datei"},
		idea.VariantInput{Title: "Als Datei", Pro: "klein", Contra: "zweites Backup"}))
	if len(d.Variants) != 2 || d.Variants[0].Label != "A" || d.Variants[1].Label != "B" {
		t.Fatalf("variants: %+v", d.Variants)
	}
	if _, err := w.pa["mia"].OpenDecision(OpenDecisionInput{Subject: idea.SubjectIdea, SubjectID: i.ID, Question: "einzeln", Variants: []idea.VariantInput{{Title: "A"}}, Actor: w.actor("mia")}); ruleCode(err) != "variants_too_few" {
		t.Fatalf("one variant: %v", err)
	}
	c, err := w.pa["mia"].AddVariant(d.ID, idea.VariantInput{Title: "Extern", Pro: "billig"}, w.agent("rex"))
	if err != nil || len(c.Variants) != 3 || c.Variants[2].Label != "C" {
		t.Fatalf("%+v %v", c, err)
	}
	// Stimmen je Person, mit Begründung, je Art die letzte; ein Gast stimmt nicht.
	if _, err := w.pa["gus"].VoteDecision(d.ID, "A", "ich auch", w.actor("gus")); !errors.Is(err, ErrAccessForbidden) {
		t.Fatalf("guest votes: %v", err)
	}
	if _, err := w.pa["rex"].VoteDecision(d.ID, "Z", "x", w.actor("rex")); ruleCode(err) != "variant_not_found" {
		t.Fatalf("unknown variant: %v", err)
	}
	if _, err := w.pa["rex"].VoteDecision(d.ID, "A", "", w.actor("rex")); ruleCode(err) != "reason_required" {
		t.Fatalf("a vote needs a reason: %v", err)
	}
	w.pa["rex"].VoteDecision(d.ID, "B", "Dateien sind einfach", w.actor("rex"))
	w.pa["rex"].VoteDecision(d.ID, "A", "ich würde A nehmen, weil ein Backup reicht", w.actor("rex"))
	w.pa["rex"].VoteDecision(d.ID, "C", "als Agent sehe ich C", w.agent("rex"))
	voted, err := w.pa["lena"].VoteDecision(d.ID, "A", "Backup ist das Argument", w.actor("lena"))
	if err != nil || len(voted.Votes) != 3 {
		t.Fatalf("votes: %+v %v", voted, err)
	}
	seen := map[string]string{}
	for _, v := range voted.Votes {
		key := v.Voter
		if v.ByAgent {
			key += "/agent"
		}
		seen[key] = v.Variant + ":" + v.Reason
	}
	if seen["rex"] != "A:ich würde A nehmen, weil ein Backup reicht" || seen["rex/agent"] != "C:als Agent sehe ich C" || seen["lena"] != "A:Backup ist das Argument" {
		t.Fatalf("votes per person: %v", seen)
	}
	// Ein Bild an Variante A.
	att, err := w.pa["mia"].Attach(AttachInput{Subject: idea.SubjectIdea, SubjectID: i.ID, Owner: idea.OwnerVariant, VariantID: voted.Variants[0].ID, Data: testPNG(t, 4), Alt: "Skizze A", Actor: w.actor("mia")})
	if err != nil {
		t.Fatal(err)
	}
	// Die Entscheidung übernimmt Variante A samt Bild in Wissen und Konzept.
	if _, err := w.pa["mia"].CloseDecision(CloseDecisionInput{ID: d.ID, Outcome: idea.DecisionDecided, Reason: "ein Backup", Actor: w.actor("mia")}); ruleCode(err) != "variant_required" {
		t.Fatalf("variants need a choice: %v", err)
	}
	got, err := w.pa["mia"].CloseDecision(CloseDecisionInput{ID: d.ID, Outcome: idea.DecisionDecided, Variant: "a", Reason: "ein Backup reicht", Actor: w.actor("mia")})
	if err != nil || got.ChosenVariant != "A" || got.KnowledgeID == 0 || !got.Variants[0].Chosen {
		t.Fatalf("%+v %v", got, err)
	}
	k := knowledgeOf(t, w, got.KnowledgeID)
	for _, want := range []string{"**Variant A:** In der Datenbank", "**Pro:** ein Backup", "**Contra:** größere Datei", "ein Backup reicht", "ATT-1", att.URL, "lena: A, Backup ist das Argument", "rex (agent): C"} {
		if !strings.Contains(k.Body, want) {
			t.Errorf("knowledge lacks %q:\n%s", want, k.Body)
		}
	}
	if strings.Contains(k.Body, "Dateien sind einfach") {
		t.Error("a replaced vote is still in the knowledge")
	}
	cv, _, _ := w.pa["mia"].IdeaConcept(i.ID, 0)
	if !strings.Contains(cv.Concept.How, got.HumanID()) || !strings.Contains(cv.Concept.How, "In der Datenbank") || !strings.Contains(cv.Concept.How, "ATT-1") || cv.Revision != 2 {
		t.Fatalf("concept does not carry the decision: %+v", cv)
	}
	if _, err := w.pa["rex"].VoteDecision(d.ID, "B", "zu spät", w.actor("rex")); ruleCode(err) != "decision_closed" {
		t.Fatalf("vote after the decision: %v", err)
	}
}

func TestDecisionsOnAnIssueAndAskingViaTheIdea(t *testing.T) {
	w := newPainWorld(t)
	r := w.request(t, "Issue mit Frage")
	d := w.openOn(t, "mia", idea.SubjectRequest, r, "Welches Format", w.actor("mia"))
	if d.Subject != idea.SubjectRequest || d.SubjectID != r {
		t.Fatalf("%+v", d)
	}
	if list, err := w.pa["gus"].DecisionsOf(idea.SubjectRequest, r); err != nil || len(list) != 1 {
		t.Fatalf("%v %v", list, err)
	}
	if _, err := w.pa["gus"].OpenDecision(OpenDecisionInput{Subject: idea.SubjectRequest, SubjectID: r, Question: "x", Actor: w.actor("gus")}); !errors.Is(err, ErrAccessForbidden) {
		t.Fatalf("a guest asks: %v", err)
	}
	_, errStranger := w.pa["nora"].DecisionsOf(idea.SubjectRequest, r)
	_, errMissing := w.pa["nora"].DecisionsOf(idea.SubjectRequest, r+500)
	if ruleCode(errStranger) != "request_not_found" || ruleCode(errMissing) != "request_not_found" {
		t.Fatalf("%v %v", errStranger, errMissing)
	}
	if _, err := w.pa["nora"].DecisionView(d.ID); ruleCode(err) != "decision_not_found" {
		t.Fatalf("a stranger reads a decision: %v", err)
	}
	if _, err := w.pa["nora"].DecisionView(d.ID + 99); ruleCode(err) != "decision_not_found" {
		t.Fatalf("%v", err)
	}
	// Rückfrage über das Issue landet an der Idee.
	i := w.approvedIdea(t, "Mit Issue", "geht")
	res, _ := w.pa["mia"].CreateIssuesFromIdea(IdeaIssuesInput{Idea: i.ID, Actor: w.actor("mia")})
	found, ok := w.pa["mia"].IdeaOfRequest(res.Requests[0])
	if !ok || found.ID != i.ID {
		t.Fatalf("%+v %v", found, ok)
	}
	q := w.openOn(t, "mia", idea.SubjectIdea, found.ID, "Rückfrage aus der Umsetzung", w.agent("mia"), withAspects(idea.AspectProduct))
	if ideaNow, _ := w.pa["mia"].IdeaView(i.ID); ideaNow.OpenDecisions != 1 || !q.NeedsHuman() {
		t.Fatalf("the question sits on the idea: %+v", ideaNow)
	}
}

func TestProjectPolicyAndPerDecisionWay(t *testing.T) {
	w := newPainWorld(t)
	i := w.workshopIdea(t, "Policy")
	if err := w.pa["mia"].SetDecisionPolicy(roleProject, idea.PolicyHuman, "mia", w.actor("mia")); !errors.Is(err, ErrAccessForbidden) {
		t.Fatalf("a member sets the policy: %v", err)
	}
	if err := w.pa["lena"].SetDecisionPolicy(roleProject, "weird", "lena", w.actor("lena")); ruleCode(err) != "policy_invalid" {
		t.Fatalf("%v", err)
	}
	if err := w.pa["lena"].SetDecisionPolicy(roleProject, idea.PolicyHuman, "lena", w.actor("lena")); err != nil {
		t.Fatal(err)
	}
	d := w.openOn(t, "mia", idea.SubjectIdea, i.ID, "Klein und lokal", w.agent("mia"))
	if d.Way != idea.WayHuman {
		t.Fatalf("the project keeps it with people: %+v", d)
	}
	if err := w.pa["lena"].SetDecisionPolicy(roleProject, idea.PolicyRule, "lena", w.actor("lena")); err != nil {
		t.Fatal(err)
	}
	e := w.openOn(t, "mia", idea.SubjectIdea, i.ID, "Wieder Regel", w.agent("mia"))
	if e.Way != idea.WayAgent {
		t.Fatalf("%+v", e)
	}
	// Je Entscheidung stellt nur ein Mensch mit Bearbeitungsrecht um.
	if _, err := w.pa["mia"].SetDecisionWay(e.ID, idea.WayHuman, w.agent("mia")); ruleCode(err) != "human_only" {
		t.Fatalf("%v", err)
	}
	if _, err := w.pa["rex"].SetDecisionWay(e.ID, idea.WayHuman, w.actor("rex")); !errors.Is(err, ErrAccessForbidden) {
		t.Fatalf("%v", err)
	}
	got, err := w.pa["mia"].SetDecisionWay(e.ID, idea.WayHuman, w.actor("mia"))
	if err != nil || got.Way != idea.WayHuman {
		t.Fatalf("%+v %v", got, err)
	}
	if pol, _ := w.pa["gus"].DecisionPolicy(roleProject); pol != idea.PolicyRule {
		t.Fatalf("%s", pol)
	}
}

func TestDecisionTextsStayOnOneLine(t *testing.T) {
	w := newPainWorld(t)
	i := w.workshopIdea(t, "Einzeilig")
	for name, in := range map[string]OpenDecisionInput{
		"newline question": {Question: "a\nb"},
		"bidi question":    {Question: "a\u202eb"},
		"empty question":   {Question: " "},
		"long question":    {Question: strings.Repeat("q", idea.MaxQuestion+1)},
		"bad variant":      {Question: "ok", Variants: []idea.VariantInput{{Title: "a\nb"}, {Title: "b"}}},
		"bad detail":       {Question: "ok", Detail: "a\x00b"},
	} {
		in.Subject, in.SubjectID, in.Actor = idea.SubjectIdea, i.ID, w.actor("mia")
		if _, err := w.pa["mia"].OpenDecision(in); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	d := w.openOn(t, "mia", idea.SubjectIdea, i.ID, "Ein ganz normales Thema", w.actor("mia"))
	for name, in := range map[string]CloseDecisionInput{
		"multi-line reason": {Outcome: idea.DecisionDecided, Answer: "x", Reason: "a\nb"},
		"bidi reason":       {Outcome: idea.DecisionDropped, Reason: "a\u202eb"},
	} {
		in.ID, in.Actor = d.ID, w.actor("mia")
		if _, err := w.pa["mia"].CloseDecision(in); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func countDecisionKnowledge(w *painWorld) (n int) {
	w.st.DB().QueryRow(`SELECT COUNT(*) FROM knowledge WHERE type='decision'`).Scan(&n)
	return
}

func conceptRevisions(w *painWorld, id int64) (n int) {
	w.st.DB().QueryRow(`SELECT COUNT(*) FROM idea_texts WHERE idea_id=? AND part='concept'`, id).Scan(&n)
	return
}

// Wer an der Idee nur mitarbeiten darf (ActWork), stimmt ab und schlägt vor, aber
// schließt nicht und kippt nichts: kein Wissen, kein Konzept-Anhang, und die
// Antwort ist die bei fehlendem Recht.
func TestOnlyTheOwnerOrAMaintainerClosesAndOverturnsDecisions(t *testing.T) {
	w := newPainWorld(t)
	i := w.workshopIdea(t, "Schließen ist Bearbeiten")
	about := "Anfang"
	w.pa["mia"].PutIdeaConcept(i.ID, PutIdeaConceptInput{Patch: idea.ConceptPatch{About: &about}, Actor: w.actor("mia")})
	agentWay := w.openOn(t, "mia", idea.SubjectIdea, i.ID, "Kleine lokale Frage", w.agent("mia"))
	humanWay := w.openOn(t, "mia", idea.SubjectIdea, i.ID, "Rechtefrage", w.agent("mia"), withAspects(idea.AspectSecurity))
	revs, knowledge := conceptRevisions(w, i.ID), countDecisionKnowledge(w)
	for _, who := range []struct {
		name  string
		actor idea.Actor
	}{{"rex human", w.actor("rex")}, {"rex agent", w.agent("rex")}} {
		for _, d := range []idea.Decision{agentWay, humanWay} {
			for _, outcome := range []string{idea.DecisionDecided, idea.DecisionDeferred, idea.DecisionDropped} {
				_, err := w.pa["rex"].CloseDecision(CloseDecisionInput{ID: d.ID, Outcome: outcome, Answer: "ja", Reason: "weil", Trigger: "später", Actor: who.actor})
				if !errors.Is(err, ErrAccessForbidden) {
					t.Errorf("%s closes %s as %s: %v", who.name, d.HumanID(), outcome, err)
				}
			}
		}
	}
	if conceptRevisions(w, i.ID) != revs || countDecisionKnowledge(w) != knowledge {
		t.Fatal("a refused close wrote to the concept or to knowledge")
	}
	// Abstimmen und Varianten vorschlagen darf er.
	with := w.openOn(t, "mia", idea.SubjectIdea, i.ID, "Mit Varianten", w.actor("mia"), withVariants(idea.VariantInput{Title: "A-Weg"}, idea.VariantInput{Title: "B-Weg"}))
	if _, err := w.pa["rex"].VoteDecision(with.ID, "A", "ich würde A nehmen", w.agent("rex")); err != nil {
		t.Fatalf("a member votes: %v", err)
	}
	if _, err := w.pa["rex"].AddVariant(with.ID, idea.VariantInput{Title: "C-Weg"}, w.agent("rex")); err != nil {
		t.Fatalf("a member proposes: %v", err)
	}
	// Der Owner schließt, dann kippt nur ein Mensch mit Bearbeitungsrecht.
	closed, err := w.pa["mia"].CloseDecision(CloseDecisionInput{ID: humanWay.ID, Outcome: idea.DecisionDecided, Answer: "nur Maintainer", Reason: "Rechtefrage", Actor: w.actor("mia")})
	if err != nil || closed.State != idea.DecisionDecided {
		t.Fatalf("%+v %v", closed, err)
	}
	for _, who := range []idea.Actor{w.actor("rex"), w.agent("rex"), w.agent("mia")} {
		if _, err := w.pa["rex"].ReopenDecision(humanWay.ID, "ich sehe es anders", who); !errors.Is(err, ErrAccessForbidden) && ruleCode(err) != "human_only" {
			t.Errorf("%+v overturns a human decision: %v", who, err)
		}
	}
	// Ein Agent mit eigenem Bearbeitungsrecht kippt trotzdem nicht still.
	if _, err := w.pa["mia"].ReopenDecision(humanWay.ID, "nochmal", w.agent("mia")); ruleCode(err) != "human_only" {
		t.Fatalf("an agent with edit rights overturns: %v", err)
	}
	if got, _ := w.pa["mia"].DecisionView(humanWay.ID); got.State != idea.DecisionDecided {
		t.Fatalf("the decision was overturned: %+v", got)
	}
	if _, err := w.pa["lena"].ReopenDecision(humanWay.ID, "Maintainer kippt", w.actor("lena")); err != nil {
		t.Fatalf("a maintainer overturns: %v", err)
	}
	// Die Antwort ist die bei fehlendem Recht, auch für eine Idee, die man nicht sieht.
	_, err1 := w.pa["nora"].CloseDecision(CloseDecisionInput{ID: agentWay.ID, Outcome: idea.DecisionDropped, Reason: "x", Actor: w.actor("nora")})
	_, err2 := w.pa["nora"].CloseDecision(CloseDecisionInput{ID: agentWay.ID + 500, Outcome: idea.DecisionDropped, Reason: "x", Actor: w.actor("nora")})
	if ruleCode(err1) != "decision_not_found" || ruleCode(err2) != "decision_not_found" {
		t.Fatalf("%v / %v", err1, err2)
	}
}

// Im Log-Modus der Rechteprüfung wird nichts durchgesetzt, aber jede dieser Wege
// läuft durch dieselbe Prüfung und wird als „würde verweigert“ gezählt.
func TestClosingAndOverturningAreCheckedInLogMode(t *testing.T) {
	w := newPainWorld(t)
	w.st.SetAccessMode(AccessMode{Enforce: false})
	i := w.workshopIdea(t, "Log-Modus")
	d := w.openOn(t, "mia", idea.SubjectIdea, i.ID, "Frage", w.agent("mia"))
	before, _ := w.st.AccessCounters()
	if _, err := w.pa["rex"].CloseDecision(CloseDecisionInput{ID: d.ID, Outcome: idea.DecisionDropped, Reason: "x", Actor: w.agent("rex")}); err != nil {
		t.Fatalf("log mode does not enforce: %v", err)
	}
	after, _ := w.st.AccessCounters()
	if after <= before {
		t.Fatalf("closing as a member who does not own the idea was not checked for edit rights (would-deny %d -> %d)", before, after)
	}
	before = after
	w.pa["rex"].ReopenDecision(d.ID, "x", w.actor("rex"))
	if after, _ = w.st.AccessCounters(); after <= before {
		t.Fatalf("overturning was not checked (would-deny %d -> %d)", before, after)
	}
}
