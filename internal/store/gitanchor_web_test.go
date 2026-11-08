package store

import (
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func webRequest(t *testing.T, st *Store) int64 {
	t.Helper()
	d, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "web", Person: "robin",
		Scope: scope.Axes{Project: anchorTestProject}}})
	if err != nil {
		t.Fatal(err)
	}
	return d.Request.ID
}

func activityOf(t *testing.T, st *Store, id int64, kind string) requestdomain.Activity {
	t.Helper()
	d, err := st.RequestByID(id)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(d.Activity) - 1; i >= 0; i-- {
		if d.Activity[i].Kind == kind {
			return d.Activity[i]
		}
	}
	t.Fatalf("no %s activity in %+v", kind, d.Activity)
	return requestdomain.Activity{}
}

// Ein Schreibzugriff der Weboberfläche hat keinen Anker, aber er ist als Web
// gekennzeichnet; eine Zeile ohne Weg und ohne Anker bleibt leer, und ein
// echter Anker trägt keine Web-Kennzeichnung.
func TestWebWritesAreMarkedWebAndStayWithoutAnAnchor(t *testing.T) {
	st := openAnchorStore(t)
	id := webRequest(t, st)

	if _, err := st.AddCriterion(id, "aus dem Web", "robin", gitanchor.Web()); err != nil {
		t.Fatal(err)
	}
	web := activityOf(t, st, id, "criterion.added")
	if web.Via != "web" || web.GitAnchor != nil {
		t.Fatalf("web write: via %q anchor %+v", web.Via, web.GitAnchor)
	}

	if _, err := st.AddCriterion(id, "ohne alles", "robin"); err != nil {
		t.Fatal(err)
	}
	if plain := activityOf(t, st, id, "criterion.added"); plain.Via != "" || plain.GitAnchor != nil {
		t.Fatalf("a write without anchor and without Web stays empty: via %q anchor %+v", plain.Via, plain.GitAnchor)
	}

	if _, err := st.AddCriterion(id, "vom Agenten", "robin", anchorAt(commitA, "main")); err != nil {
		t.Fatal(err)
	}
	if agent := activityOf(t, st, id, "criterion.added"); agent.Via != "" || agent.GitAnchor == nil || agent.GitAnchor.Commit != commitA {
		t.Fatalf("an agent write keeps its anchor and is not Web: via %q anchor %+v", agent.Via, agent.GitAnchor)
	}

	var anchors int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM git_anchors`).Scan(&anchors); err != nil || anchors != 1 {
		t.Fatalf("Web must not invent an anchor row: %d (%v)", anchors, err)
	}
}

func TestWebEvidenceDropAndCompleteAreMarkedWeb(t *testing.T) {
	st := openAnchorStore(t)
	done := webRequest(t, st)
	c, err := st.AddCriterion(done, "ein Kriterium", "robin")
	if err != nil {
		t.Fatal(err)
	}
	ev := requestdomain.Evidence{Kind: "test", Ref: "TestX", Person: "robin", GitAnchor: gitanchor.Web()}
	if err := st.SetCriterionState(c.ID, "met", ev); err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteRequest(done, requestdomain.Evidence{Kind: "test", Ref: "alles", Person: "robin", GitAnchor: gitanchor.Web()}); err != nil {
		t.Fatal(err)
	}
	d, _ := st.RequestByID(done)
	webEvidence := 0
	for _, cr := range d.Criteria {
		for _, e := range cr.Evidence {
			if e.Via == "web" && e.GitAnchor == nil {
				webEvidence++
			}
		}
	}
	if webEvidence != 1 {
		t.Fatalf("evidence marked Web: %d of %+v", webEvidence, d.Criteria)
	}

	dropped := webRequest(t, st)
	if err := st.DropRequest(dropped, "nicht mehr nötig", "robin", gitanchor.Web()); err != nil {
		t.Fatal(err)
	}
	if a := activityOf(t, st, dropped, "request.dropped"); a.Via != "web" || a.GitAnchor != nil {
		t.Fatalf("drop: via %q anchor %+v", a.Via, a.GitAnchor)
	}
}

// Eine inhaltliche Bearbeitung in der Weboberfläche ist eine neue Fassung ohne
// Anker, gekennzeichnet als Web. Eine Statusänderung ist keine neue Fassung und
// behält Anker und Herkunft; die alte Fassung behält ihren Anker in der Historie.
func TestWebEditOfKnowledgeIsANewRevisionMarkedWeb(t *testing.T) {
	st := openAnchorStore(t)
	id, err := st.InsertKnowledge(Knowledge{Type: "note", Title: "t", Body: "eins", Person: "robin", GitAnchor: anchorAt(commitA, "main")})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateKnowledgeFromWeb(id, map[string]string{"confidence": "verified"}, "lena"); err != nil {
		t.Fatal(err)
	}
	k, _ := st.KnowledgeByID(id)
	if k.GitAnchor == nil || k.GitAnchor.Commit != commitA || (k.OriginContext != nil && k.OriginContext.Via != "") {
		t.Fatalf("a status change is no new revision: anchor %+v origin %+v", k.GitAnchor, k.OriginContext)
	}
	if err := st.UpdateKnowledgeFromWeb(id, map[string]string{"body": "zwei"}, "lena"); err != nil {
		t.Fatal(err)
	}
	k, _ = st.KnowledgeByID(id)
	if k.Body != "zwei" || k.GitAnchor != nil || k.OriginContext == nil || k.OriginContext.Via != "web" {
		t.Fatalf("web edit: body %q anchor %+v origin %+v", k.Body, k.GitAnchor, k.OriginContext)
	}
	hist, err := st.KnowledgeHistory(id)
	if err != nil || len(hist) == 0 {
		t.Fatalf("history %d %v", len(hist), err)
	}
	for _, v := range hist {
		if v.GitAnchor == nil || v.GitAnchor.Commit != commitA {
			t.Fatalf("the replaced revision keeps its anchor: %+v", v.GitAnchor)
		}
	}
	// Der gewöhnliche Pfad (Server-API) markiert nichts als Web.
	if err := st.UpdateKnowledgeBy(id, map[string]string{"body": "drei"}, "lena"); err != nil {
		t.Fatal(err)
	}
	if k, _ = st.KnowledgeByID(id); k.OriginContext != nil && k.OriginContext.Via != "" {
		t.Fatalf("UpdateKnowledgeBy must not mark Web: %+v", k.OriginContext)
	}
}

// Web ist keine Angabe, die ein Client machen kann: der Header trägt sie nicht.
func TestTheWebMarkCannotComeFromTheHeader(t *testing.T) {
	a := anchorAt(commitA, "main")
	a.Via = gitanchor.ViaWeb
	if got := gitanchor.Decode(gitanchor.Encode(*a)); got.Via != "" {
		t.Fatalf("Via travelled through the header: %+v", got)
	}
}
