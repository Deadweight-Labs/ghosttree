package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

type painWorld struct {
	st *Store
	pa map[string]*ProjectAccess
}

func newPainWorld(t *testing.T) *painWorld {
	t.Helper()
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	w := &painWorld{st: st, pa: map[string]*ProjectAccess{}}
	for i, n := range []string{"robin", "lena", "mia", "rex", "gus", "nora"} {
		w.pa[n] = st.Access(Principal{ID: principalOfID(int64(i + 1)), Label: n})
	}
	return w
}

func (w *painWorld) request(t *testing.T, title string) int64 {
	t.Helper()
	d, err := w.st.CreateRequest(requestdomain.CreateInput{
		Request:  requestdomain.Request{Type: "feature", Title: title, Scope: scope.Axes{Project: roleProject}, Person: "mia"},
		Criteria: []string{"it works"}, AuthorID: 3})
	if err != nil {
		t.Fatal(err)
	}
	return d.Request.ID
}

func (w *painWorld) newPain(t *testing.T, who string, targets ...pain.Target) pain.Detail {
	t.Helper()
	acct, _ := accountNumericID(w.pa[who].principal.ID)
	d, err := w.pa[who].CreatePain(CreatePainInput{Project: roleProject, Title: "Schreiben geht nur ganz", Motivates: targets,
		Actor: pain.Actor{Person: who}, AuthorID: acct})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func ruleCode(err error) string {
	var r *requestdomain.RuleError
	if errors.As(err, &r) {
		return r.ErrorCode
	}
	return ""
}

func TestPainNeedsOnlyATitleAndHasNoCriteria(t *testing.T) {
	w := newPainWorld(t)
	d := w.newPain(t, "mia")
	if d.Pain.State != pain.Active || d.Pain.ID == 0 {
		t.Fatalf("pain: %+v", d.Pain)
	}
	var requests, criteria int
	w.st.DB().QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&requests)
	w.st.DB().QueryRow(`SELECT COUNT(*) FROM request_criteria`).Scan(&criteria)
	if requests != 0 || criteria != 0 {
		t.Errorf("a pain created requests=%d criteria=%d", requests, criteria)
	}
	if _, err := w.pa["mia"].CreatePain(CreatePainInput{Project: roleProject, Title: "  "}); ruleCode(err) != "title_required" {
		t.Errorf("empty title: %v", err)
	}
}

func TestPainTransitionTable(t *testing.T) {
	w := newPainWorld(t)
	commit := pain.Evidence{Kind: "commit", Ref: "abc1234"}
	none := pain.Evidence{}
	type step struct {
		to, remaining, note string
		ev                  pain.Evidence
		want                string // rule code, "" = allowed
	}
	cases := []struct {
		name  string
		setup []step
		step  step
	}{
		{"active->mitigated ok", nil, step{pain.Mitigated, "Lesen fehlt noch", "", commit, ""}},
		{"active->mitigated without remaining", nil, step{pain.Mitigated, "  ", "", commit, "remaining_required"}},
		{"active->mitigated without evidence", nil, step{pain.Mitigated, "x", "", none, "evidence_required"}},
		{"active->resolved with evidence", nil, step{pain.Resolved, "", "", commit, ""}},
		{"active->resolved without evidence", nil, step{pain.Resolved, "", "", none, "evidence_required"}},
		{"active->obsolete with note", nil, step{pain.Obsolete, "", "gegenstandslos", none, ""}},
		{"active->obsolete without note", nil, step{pain.Obsolete, "", "", none, "note_required"}},
		{"active->active", nil, step{pain.Active, "", "x", none, "invalid_transition"}},
		{"unknown state", nil, step{"done", "", "x", commit, "invalid_transition"}},
		{"mitigated->mitigated reassessed", []step{{pain.Mitigated, "a", "", commit, ""}}, step{pain.Mitigated, "b", "", pain.Evidence{Kind: "test", Ref: "TestX"}, ""}},
		{"mitigated->mitigated without new evidence", []step{{pain.Mitigated, "a", "", commit, ""}}, step{pain.Mitigated, "b", "", none, "evidence_required"}},
		{"mitigated->resolved", []step{{pain.Mitigated, "a", "", commit, ""}}, step{pain.Resolved, "", "", commit, ""}},
		{"mitigated->obsolete", []step{{pain.Mitigated, "a", "", commit, ""}}, step{pain.Obsolete, "", "weg", none, ""}},
		{"mitigated->active setback", []step{{pain.Mitigated, "a", "", commit, ""}}, step{pain.Active, "", "Rückschlag", none, ""}},
		{"mitigated->active without note", []step{{pain.Mitigated, "a", "", commit, ""}}, step{pain.Active, "", "", none, "note_required"}},
		{"resolved->active regression", []step{{pain.Resolved, "", "", commit, ""}}, step{pain.Active, "", "Regression", none, ""}},
		{"resolved->mitigated", []step{{pain.Resolved, "", "", commit, ""}}, step{pain.Mitigated, "x", "", commit, "invalid_transition"}},
		{"resolved->obsolete", []step{{pain.Resolved, "", "", commit, ""}}, step{pain.Obsolete, "", "weg", none, ""}},
		{"obsolete->active reopened", []step{{pain.Obsolete, "", "weg", none, ""}}, step{pain.Active, "", "doch", none, ""}},
		{"obsolete->resolved", []step{{pain.Obsolete, "", "weg", none, ""}}, step{pain.Resolved, "", "", commit, "invalid_transition"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := w.newPain(t, "mia").Pain.ID
			for _, s := range c.setup {
				if _, err := w.pa["mia"].SetPainState(id, s.to, s.remaining, s.ev, s.note, pain.Actor{Person: "mia"}); err != nil {
					t.Fatalf("setup %v: %v", s, err)
				}
			}
			s := c.step
			d, err := w.pa["mia"].SetPainState(id, s.to, s.remaining, s.ev, s.note, pain.Actor{Person: "mia"})
			if got := ruleCode(err); got != s.want {
				t.Fatalf("code %q (%v), want %q", got, err, s.want)
			}
			if s.want == "" && d.Pain.State != s.to {
				t.Errorf("state %s, want %s", d.Pain.State, s.to)
			}
			if s.want == "" && s.to != pain.Mitigated && d.Pain.Remaining != "" {
				t.Errorf("remaining kept: %q", d.Pain.Remaining)
			}
		})
	}
}

func TestPainStateIsGuardedByTheDatabaseToo(t *testing.T) {
	w := newPainWorld(t)
	id := w.newPain(t, "mia").Pain.ID
	if _, err := w.st.DB().Exec(`UPDATE pains SET state='done' WHERE id=?`, id); err == nil {
		t.Error("direct UPDATE to an unknown state was accepted")
	}
	for _, remaining := range []string{"", "   "} {
		if _, err := w.st.DB().Exec(`UPDATE pains SET state='mitigated', remaining=? WHERE id=?`, remaining, id); err == nil {
			t.Errorf("mitigated with remaining %q was accepted by the database", remaining)
		}
	}
	if _, err := w.st.DB().Exec(`UPDATE pain_events SET note='x'`); err == nil {
		t.Error("pain_events accepted an update")
	}
}

func TestPainEvidenceKindsRequestAndDecisionAreChecked(t *testing.T) {
	w := newPainWorld(t)
	req := w.request(t, "Maßnahme")
	id := w.newPain(t, "mia").Pain.ID
	pa := w.pa["mia"]
	if _, err := pa.SetPainState(id, pain.Resolved, "", pain.Evidence{Kind: "request", Ref: "REQ-9999"}, "", pain.Actor{}); ruleCode(err) != "evidence_invalid" {
		t.Errorf("missing request as evidence: %v", err)
	}
	if _, err := pa.SetPainState(id, pain.Resolved, "", pain.Evidence{Kind: "nonsense", Ref: "x"}, "", pain.Actor{}); ruleCode(err) != "evidence_invalid" {
		t.Errorf("unknown evidence kind: %v", err)
	}
	d, err := pa.SetPainState(id, pain.Mitigated, "Rest", pain.Evidence{Kind: "request", Ref: idRef("", req)}, "", pain.Actor{Person: "mia"})
	if err != nil {
		t.Fatal(err)
	}
	last := d.Events[len(d.Events)-1]
	if last.EvidenceRef != idRef("REQ-", req) {
		t.Errorf("evidence not normalized: %+v", last)
	}
}

func TestPainLinksBothWaysAndVisibility(t *testing.T) {
	w := newPainWorld(t)
	r1, r2, r3 := w.request(t, "eins"), w.request(t, "zwei"), w.request(t, "drei")
	kid, err := w.st.InsertKnowledge(Knowledge{Type: "decision", Title: "Entscheidung", Body: "b", Scope: scope.Axes{Project: roleProject}, Person: "mia", Confidence: "trusted"})
	if err != nil {
		t.Fatal(err)
	}
	d := w.newPain(t, "mia", pain.Target{RequestID: r1}, pain.Target{RequestID: r2}, pain.Target{RequestID: r3},
		pain.Target{KnowledgeID: kid}, pain.Target{ExternalRef: "https://example.invalid/issues/7"})
	if n := len(d.Motivates); n != 5 {
		t.Fatalf("motivates %d, want 5", n)
	}
	// Rückweg: Request-Detail und Wissen.
	rd, _ := w.st.RequestByID(r1)
	view := w.pa["lena"].RequestDetailView(rd)
	if len(view.Pains) != 1 || view.Pains[0].ID != d.Pain.ID {
		t.Fatalf("request view pains: %+v", view.Pains)
	}
	if got := w.pa["lena"].PainsFor("knowledge", kid); len(got) != 1 {
		t.Errorf("PainsFor knowledge: %+v", got)
	}
	// Doppelte Kante ist ein Erfolg ohne neue Zeile.
	if _, err := w.pa["mia"].AddPainLink(d.Pain.ID, pain.Target{RequestID: r1}, pain.Actor{Person: "mia"}); err != nil {
		t.Fatal(err)
	}
	var n int
	w.st.DB().QueryRow(`SELECT COUNT(*) FROM entity_links WHERE src_id=?`, d.Pain.ID).Scan(&n)
	if n != 5 {
		t.Errorf("duplicate edge stored: %d rows", n)
	}
	// Genau ein Ziel.
	if _, err := w.pa["mia"].AddPainLink(d.Pain.ID, pain.Target{RequestID: r1, KnowledgeID: kid}, pain.Actor{}); ruleCode(err) != "link_target_required" {
		t.Errorf("two targets: %v", err)
	}
	// Ziel aus anderem Projekt und unbekanntes Ziel: gleiche Antwort.
	if _, err := w.pa["mia"].AddPainLink(d.Pain.ID, pain.Target{RequestID: 424242}, pain.Actor{}); ruleCode(err) != "link_target_not_found" {
		t.Errorf("unknown request: %v", err)
	}
	// Entfernen verlangt einen Grund und schreibt Verlauf und Aktivität.
	if err := w.pa["mia"].RemovePainLink(d.Motivates[0].ID, "", pain.Actor{}); ruleCode(err) != "reason_required" {
		t.Errorf("unlink without reason: %v", err)
	}
	if err := w.pa["mia"].RemovePainLink(d.Motivates[0].ID, "falsch zugeordnet", pain.Actor{Person: "mia"}); err != nil {
		t.Fatal(err)
	}
	rd, _ = w.st.RequestByID(r1)
	kinds := ""
	for _, a := range rd.Activity {
		kinds += a.Kind + " "
	}
	if !strings.Contains(kinds, "pain.linked") || !strings.Contains(kinds, "pain.unlinked") {
		t.Errorf("request activity: %s", kinds)
	}
}

func TestPainDetailHidesTargetsTheViewerCannotRead(t *testing.T) {
	w := newPainWorld(t)
	// Ein Auftrag in einem anderen Projekt ist kein erlaubtes Ziel; ein Auftrag
	// aus einer privaten Session eines anderen Kontos ist für den Betrachter nicht lesbar.
	r1 := w.request(t, "sichtbar")
	hidden, err := w.st.CreateRequest(requestdomain.CreateInput{
		Request: requestdomain.Request{Type: "feature", Title: "geheim", Scope: scope.Axes{Project: roleProject}, Origin: "distilled", SessionRef: "session:777"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sid, err := w.st.UpsertSession(Session{Harness: "claude-code", ExternalID: "priv", AccountID: 3, Scope: scope.Axes{Project: roleProject}})
	if err != nil {
		t.Fatal(err)
	}
	w.st.DB().Exec(`UPDATE requests SET session_ref=? WHERE id=?`, "session:"+idRef("", sid), hidden.Request.ID)
	d := w.newPain(t, "mia", pain.Target{RequestID: r1}, pain.Target{RequestID: hidden.Request.ID})
	if len(d.Motivates) != 2 {
		t.Fatalf("author sees %d targets", len(d.Motivates))
	}
	got, err := w.pa["gus"].PainDetailView(d.Pain.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Motivates) != 1 || got.Motivates[0].RequestID != r1 {
		t.Errorf("guest sees %+v", got.Motivates)
	}
	for _, e := range got.Events {
		if strings.Contains(e.Data, idRef("request:", hidden.Request.ID)) {
			t.Errorf("history leaks the hidden target: %+v", e)
		}
	}
	if _, err := w.pa["nora"].PainDetailView(d.Pain.ID); !errors.Is(err, ErrAccessNotFound) {
		t.Errorf("stranger: %v", err)
	}
}

func TestPainDoesNotAgeAndDoesNotResolveWithItsRequest(t *testing.T) {
	w := newPainWorld(t)
	rid := w.request(t, "Maßnahme")
	d := w.newPain(t, "mia", pain.Target{RequestID: rid})
	if _, err := w.pa["mia"].SetPainState(d.Pain.ID, pain.Mitigated, "Rest offen", pain.Evidence{Kind: "commit", Ref: "abc"}, "", pain.Actor{Person: "mia"}); err != nil {
		t.Fatal(err)
	}
	before, _ := w.st.PainByID(d.Pain.ID)
	var events int
	w.st.DB().QueryRow(`SELECT COUNT(*) FROM pain_events`).Scan(&events)
	// Alle Kriterien erfüllen, Auftrag abschließen.
	req, _ := w.st.RequestByID(rid)
	ev := requestdomain.Evidence{Kind: "commit", Ref: "def", Person: "mia"}
	if err := w.st.SetCriterionState(req.Criteria[0].ID, "met", ev); err != nil {
		t.Fatal(err)
	}
	if err := w.st.CompleteRequest(rid, ev); err != nil {
		t.Fatal(err)
	}
	// Zehn Jahre später: nur das Alter ändert sich, nie der Zustand.
	w.st.DB().Exec(`UPDATE pains SET created_at='2016-01-01T00:00:00Z'`)
	after, _ := w.st.PainByID(d.Pain.ID)
	if after.State != pain.Mitigated || after.Remaining != before.Remaining || after.UpdatedAt != before.UpdatedAt {
		t.Errorf("pain moved: %+v -> %+v", before, after)
	}
	var events2 int
	w.st.DB().QueryRow(`SELECT COUNT(*) FROM pain_events`).Scan(&events2)
	if events2 != events {
		t.Errorf("completing the request wrote %d pain events", events2-events)
	}
}

func TestPainRightsFollowTheMatrix(t *testing.T) {
	w := newPainWorld(t)
	d := w.newPain(t, "mia")
	id := d.Pain.ID
	if _, err := w.pa["gus"].CreatePain(CreatePainInput{Project: roleProject, Title: "x"}); !errors.Is(err, ErrAccessForbidden) {
		t.Errorf("guest create: %v", err)
	}
	if _, err := w.pa["gus"].PainDetailView(id); err != nil {
		t.Errorf("guest read: %v", err)
	}
	if _, err := w.pa["gus"].SetPainState(id, pain.Obsolete, "", pain.Evidence{}, "x", pain.Actor{}); !errors.Is(err, ErrAccessForbidden) {
		t.Errorf("guest state: %v", err)
	}
	title := "neu"
	if _, err := w.pa["rex"].CorrectPain(id, &title, nil, "r", pain.Actor{}); !errors.Is(err, ErrAccessForbidden) {
		t.Errorf("foreign member edit: %v", err)
	}
	if _, err := w.pa["mia"].CorrectPain(id, &title, nil, "Tippfehler", pain.Actor{Person: "mia"}); err != nil {
		t.Errorf("own edit: %v", err)
	}
	if _, err := w.pa["lena"].CorrectPain(id, &title, nil, "", pain.Actor{}); ruleCode(err) != "reason_required" {
		t.Errorf("lead edit without reason: %v", err)
	}
	if _, err := w.pa["rex"].SetPainState(id, pain.Obsolete, "", pain.Evidence{}, "weg", pain.Actor{Person: "rex"}); err != nil {
		t.Errorf("any member sets state: %v", err)
	}
}

func TestPainSearchStatesAndEscaping(t *testing.T) {
	w := newPainWorld(t)
	a := w.newPain(t, "mia").Pain.ID
	b := w.newPain(t, "mia").Pain.ID
	if _, err := w.pa["mia"].SetPainState(b, pain.Resolved, "", pain.Evidence{Kind: "commit", Ref: "x"}, "", pain.Actor{}); err != nil {
		t.Fatal(err)
	}
	got, _ := w.pa["mia"].SearchPains(pain.Filter{Project: roleProject})
	if len(got) != 1 || got[0].ID != a {
		t.Errorf("default state: %+v", got)
	}
	all, _ := w.pa["mia"].SearchPains(pain.Filter{Project: roleProject, State: "all"})
	if len(all) != 2 {
		t.Errorf("all: %d", len(all))
	}
	if hits, _ := w.pa["mia"].SearchPains(pain.Filter{Project: roleProject, State: "all", Query: "%"}); len(hits) != 0 {
		t.Errorf("a percent sign matched everything: %d", len(hits))
	}
	if hits, _ := w.pa["mia"].SearchPains(pain.Filter{Project: roleProject, State: "all", Query: "ganz"}); len(hits) != 2 {
		t.Errorf("word search: %d", len(hits))
	}
}

func TestPainAndTaskSchemaIsIdempotentOnAnExistingDatabase(t *testing.T) {
	path := t.TempDir() + "/old.db"
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`DROP TABLE human_tasks; DROP TABLE entity_links; DROP TABLE pain_events; DROP TABLE pain_authors; DROP TABLE pains`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	for i := 0; i < 2; i++ {
		st, err = Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		st.Close()
	}
}

// Kein Altern heißt: kein Hintergrundlauf (Stale, Retention, Destillation,
// Reaper) schreibt Schmerzen. Es gibt dafür keine Testnaht, die man aufrufen
// könnte, also belegt dieser Test es an der Quelle: nur pain.go enthält
// schreibende SQL-Anweisungen auf die Tabelle pains.
func TestOnlyPainGoWritesThePainsTable(t *testing.T) {
	writers := []string{"UPDATE pains", "DELETE FROM pains", "INSERT INTO pains", "INSERT OR REPLACE INTO pains"}
	err := filepath.WalkDir("../..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, w := range writers {
			if strings.Contains(string(raw), w) && filepath.Base(path) != "pain.go" && filepath.Base(path) != "pain_schema.go" {
				t.Errorf("%s writes the pains table (%q): a pain must not change state outside SetPainState/CorrectPain", path, w)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
