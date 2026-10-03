package server

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

var knowledgeVolatile = regexp.MustCompile(`"(created_at|updated_at|observed_at|id)":("[^"]*"|\d+)`)

// knowledgeOracleBodies legt Wissen an, das auf eine lesbare und eine verborgene
// Session verweist (so entsteht es in der Destillation), und liefert, was ein
// Gast über die Wissens-API sieht.
func knowledgeOracleBodies(t *testing.T, hidden int) map[string]string {
	t.Helper()
	f := oracleFixture(t, hidden)
	shared := f.id["o-shared"]
	hiddenID := shared + 1
	if hidden == 0 {
		hiddenID = 999 // gibt es nicht: für den Gast nicht von verborgen zu unterscheiden
	}
	p := scope.Axes{Project: accProject}
	add := func(title, ref string) int64 {
		id, err := f.st.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: title, Body: "body", Scope: p, Person: "rex", Confidence: "trusted", Origin: "distilled", SessionRef: ref})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	seen := add("kanarienvogel sichtbar", fmt.Sprintf("session:%d#2", shared))
	hid := add("kanarienvogel verborgen", fmt.Sprintf("session:%d#5", hiddenID))
	sess, err := f.st.SessionByID(shared)
	if err != nil {
		t.Fatal(err)
	}
	norm := func(s string) string {
		s = knowledgeVolatile.ReplaceAllString(s, `"$1":""`)
		return strings.ReplaceAll(s, sess.PublicID, "PUB")
	}
	out := map[string]string{
		"list":   f.expect(t, "gus", 200, "GET", "/api/knowledge?project="+accProject, nil),
		"search": f.expect(t, "gus", 200, "GET", "/api/search?kind=knowledge&q=kanarienvogel&project="+accProject, nil),
		"seen":   f.expect(t, "gus", 200, "GET", fmt.Sprintf("/api/knowledge/%d", seen), nil),
		"hidden": f.expect(t, "gus", 200, "GET", fmt.Sprintf("/api/knowledge/%d", hid), nil),
	}
	for k, v := range out {
		out[k] = norm(v)
	}
	// Mitglieder behalten die Nummer.
	if body := f.expect(t, "mia", 200, "GET", fmt.Sprintf("/api/knowledge/%d", seen), nil); !strings.Contains(body, fmt.Sprintf("session:%d#2", shared)) {
		t.Errorf("member lost the session number: %s", body)
	}
	return out
}

func TestGuestKnowledgeSessionRefsDoNotDependOnHiddenSessions(t *testing.T) {
	zero, three := knowledgeOracleBodies(t, 0), knowledgeOracleBodies(t, 3)
	numeric := regexp.MustCompile(`session:\d`)
	for name, body := range three {
		if numeric.MatchString(body) {
			t.Errorf("%s: numeric session ref for a guest: %s", name, body)
		}
		if body != zero[name] {
			t.Errorf("%s depends on hidden sessions:\n0: %s\n3: %s", name, zero[name], body)
		}
	}
	if !strings.Contains(three["seen"], "session:PUB#2") {
		t.Errorf("readable session should be addressed by its public id: %s", three["seen"])
	}
	if strings.Contains(three["hidden"], "session_ref") {
		t.Errorf("hidden session ref survived: %s", three["hidden"])
	}
}

// Die Prüfliste zeigt Zitate und zählt Sessions: für Gäste nur lesbare.
func TestGuestPendingKnowledgeShowsOnlyReadableEvidence(t *testing.T) {
	f := oracleFixture(t, 2)
	shared := f.id["o-shared"]
	id, err := f.st.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: "wartend", Body: "b", Scope: scope.Axes{Project: accProject}, Person: "rex", Confidence: "staged", Origin: "distilled", SessionRef: fmt.Sprintf("session:%d#1", shared+1)})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.AddEvidence(id, []store.Evidence{{SessionID: shared, ChunkSeq: 0, Quote: "SICHTBAR"}, {SessionID: shared + 1, ChunkSeq: 0, Quote: "GEHEIMES-ZITAT"}, {SessionID: shared + 2, ChunkSeq: 0, Quote: "GEHEIMES-ZITAT"}}); err != nil {
		t.Fatal(err)
	}
	// Ein Gast sieht keine wartenden Einträge; die Sicht selbst muss trotzdem
	// dicht sein, falls die Regel sich ändert.
	pa := f.st.Access(store.Principal{ID: "person:5", Label: "gus"})
	ev, err := f.st.EvidenceFor(id)
	if err != nil {
		t.Fatal(err)
	}
	got, n := pa.EvidenceView(accProject, ev, 3)
	if len(got) != 1 || n != 1 || got[0].SessionID != 0 || got[0].SessionPublicID == "" || got[0].Quote != "SICHTBAR" {
		t.Errorf("evidence view = %+v (%d)", got, n)
	}
	k, _ := f.st.KnowledgeByID(id)
	if ref := pa.KnowledgeView(k).SessionRef; ref != "" {
		t.Errorf("hidden session ref survived: %q", ref)
	}
}
