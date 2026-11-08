package store

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
)

// Ein Pain motiviert eine Lösungsidee (REQ-606): die Relation steht in pain_get,
// in der Idee, in der Suche und in den Karten, und nur für den, der beide lesen darf.
func TestAPainMotivatesAnIdeaAndBothSidesShowIt(t *testing.T) {
	w := newPainWorld(t)
	p := w.newPain(t, "mia")
	i := w.workshopIdea(t, "Schreiben in einem Zug")
	d, err := w.pa["mia"].AddPainLink(p.Pain.ID, pain.Target{IdeaID: i.ID}, pain.Actor{Person: "mia"})
	if err != nil || len(d.Motivates) != 1 || d.Motivates[0].Kind != "idea" || d.Motivates[0].IdeaID != i.ID || d.Motivates[0].Title != "Schreiben in einem Zug" {
		t.Fatalf("link: %+v %v", d, err)
	}
	if len(d.Pain.Ideas) != 1 || d.Pain.Ideas[0].ID != i.ID {
		t.Fatalf("pain detail does not name the idea: %+v", d.Pain.Ideas)
	}
	// Am Pain sichtbar, an der Idee sichtbar, in beiden Suchen.
	view, _ := w.pa["gus"].PainDetailView(p.Pain.ID)
	if len(view.Motivates) != 1 {
		t.Fatalf("a guest sees the link: %+v", view.Motivates)
	}
	got, _ := w.pa["gus"].IdeaView(i.ID)
	if len(got.Pains) != 1 || got.Pains[0].ID != p.Pain.ID {
		t.Fatalf("idea does not name the pain: %+v", got.Pains)
	}
	ideas, _ := w.pa["gus"].SearchIdeas(idea.Filter{Project: roleProject})
	if len(ideas) != 1 || len(ideas[0].Pains) != 1 {
		t.Fatalf("idea search: %+v", ideas)
	}
	pains, _ := w.pa["gus"].SearchPains(pain.Filter{Project: roleProject})
	if len(pains) != 1 || len(pains[0].Ideas) != 1 || pains[0].Ideas[0].ID != i.ID {
		t.Fatalf("pain search: %+v", pains)
	}
	// Ein Fremder sieht nichts davon, und die Antwort auf ein fremdes Ziel gleicht der auf ein fehlendes.
	if _, err := w.pa["nora"].PainDetailView(p.Pain.ID); err == nil {
		t.Fatal("a stranger reads the pain")
	}
	_, errMissing := w.pa["mia"].AddPainLink(p.Pain.ID, pain.Target{IdeaID: 999}, pain.Actor{Person: "mia"})
	other := w.workshopIdea(t, "Andere")
	if _, err := w.st.DB().Exec(`UPDATE ideas SET project='github.com/dw/elsewhere' WHERE id=?`, other.ID); err != nil {
		t.Fatal(err)
	}
	_, errForeign := w.pa["mia"].AddPainLink(p.Pain.ID, pain.Target{IdeaID: other.ID}, pain.Actor{Person: "mia"})
	if ruleCode(errMissing) != "link_target_not_found" || ruleCode(errForeign) != "link_target_not_found" || errMissing.Error() != errForeign.Error() {
		t.Fatalf("missing: %v / other project: %v", errMissing, errForeign)
	}
	if _, err := w.pa["mia"].AddPainLink(p.Pain.ID, pain.Target{IdeaID: i.ID, RequestID: 1}, pain.Actor{Person: "mia"}); ruleCode(err) != "link_target_required" {
		t.Fatalf("two targets: %v", err)
	}
	// Doppelt verknüpfen legt keinen zweiten Eintrag an.
	if d, _ = w.pa["mia"].AddPainLink(p.Pain.ID, pain.Target{IdeaID: i.ID}, pain.Actor{Person: "mia"}); len(d.Motivates) != 1 {
		t.Fatalf("duplicate link: %+v", d.Motivates)
	}
	// Verlauf nennt die Idee; Entfernen verlangt einen Grund.
	if len(d.Events) < 2 || d.Events[len(d.Events)-1].Data != "idea:1" {
		t.Fatalf("history: %+v", d.Events)
	}
	if err := w.pa["mia"].RemovePainLink(d.Motivates[0].ID, "", pain.Actor{Person: "mia"}); err == nil {
		t.Fatal("removing without a reason")
	}
	if err := w.pa["mia"].RemovePainLink(d.Motivates[0].ID, "doch keine Lösung", pain.Actor{Person: "mia"}); err != nil {
		t.Fatal(err)
	}
	if after, _ := w.pa["mia"].IdeaView(i.ID); len(after.Pains) != 0 {
		t.Fatalf("the idea still names the pain: %+v", after.Pains)
	}
}

// Die Relation folgt den Rechten der Idee: wer sie nicht lesen darf, erfährt nichts über sie.
func TestAPainLinkToAnIdeaFollowsTheRightsOfTheIdea(t *testing.T) {
	w := newPainWorld(t)
	p := w.newPain(t, "mia")
	i := w.workshopIdea(t, "Verborgen")
	if _, err := w.pa["mia"].AddPainLink(p.Pain.ID, pain.Target{IdeaID: i.ID}, pain.Actor{Person: "mia"}); err != nil {
		t.Fatal(err)
	}
	// Idee in ein Projekt legen, das gus (Gast) nur am Pain lesen kann, gibt es nicht
	// einfach; der Weg ist die Berechtigung der Idee selbst: nora sieht nichts, gus alles.
	for who, wantLinks := range map[string]int{"mia": 1, "gus": 1} {
		d, err := w.pa[who].PainDetailView(p.Pain.ID)
		if err != nil || len(d.Motivates) != wantLinks {
			t.Errorf("%s: %+v %v", who, d.Motivates, err)
		}
	}
	if list := w.pa["nora"].PainsFor("idea", i.ID); len(list) != 0 {
		t.Fatalf("a stranger lists the pains of an idea: %+v", list)
	}
}

func TestEntityLinksLearnTheIdeaTargetWithoutLosingRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Auf den alten Stand zurück: Tabelle ohne das Ziel idea, mit einer Zeile.
	for _, q := range []string{
		`DROP TABLE entity_links`,
		`CREATE TABLE entity_links(
  id INTEGER PRIMARY KEY, project TEXT NOT NULL,
  src_kind TEXT NOT NULL CHECK(src_kind IN ('pain')),
  src_id INTEGER NOT NULL REFERENCES pains(id) ON DELETE RESTRICT,
  kind TEXT NOT NULL CHECK(kind IN ('motivates')),
  dst_kind TEXT NOT NULL CHECK(dst_kind IN ('request','knowledge','external')),
  dst_id INTEGER NOT NULL DEFAULT 0, dst_ref TEXT NOT NULL DEFAULT '',
  account_id INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL,
  CHECK((dst_kind='external' AND dst_ref != '' AND dst_id=0) OR (dst_kind!='external' AND dst_id>0 AND dst_ref='')),
  UNIQUE(src_kind, src_id, kind, dst_kind, dst_id, dst_ref))`,
		`INSERT INTO pains(id,project,title,state,created_at,updated_at) VALUES(1,'github.com/x/y','Qual','active','t','t')`,
		`INSERT INTO entity_links(id,project,src_kind,src_id,kind,dst_kind,dst_id,dst_ref,account_id,created_at) VALUES(7,'github.com/x/y','pain',1,'motivates','external',0,'https://x/1',0,'t')`,
	} {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := st.DB().Exec(`INSERT INTO entity_links(project,src_kind,src_id,kind,dst_kind,dst_id,dst_ref,created_at) VALUES('p','pain',1,'motivates','idea',1,'','t')`); err == nil {
		t.Fatal("the old table accepted an idea target")
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var ref string
	var id int64
	if err := st.DB().QueryRow(`SELECT id, dst_ref FROM entity_links`).Scan(&id, &ref); err != nil || id != 7 || ref != "https://x/1" {
		t.Fatalf("the old row is gone: %d %q %v", id, ref, err)
	}
	if _, err := st.DB().Exec(`INSERT INTO entity_links(project,src_kind,src_id,kind,dst_kind,dst_id,dst_ref,created_at) VALUES('github.com/x/y','pain',1,'motivates','idea',1,'','t')`); err != nil {
		t.Fatalf("the migrated table refuses an idea target: %v", err)
	}
	// Ein zweites Öffnen ändert nichts mehr.
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	st.DB().QueryRow(`SELECT COUNT(*) FROM entity_links`).Scan(&n)
	if n != 2 || !strings.Contains(tableSQL(t, st, "entity_links"), "'idea'") {
		t.Fatalf("rows %d", n)
	}
	st.Close()
}

func tableSQL(t *testing.T, st *Store, name string) string {
	t.Helper()
	var s string
	if err := st.DB().QueryRow(`SELECT sql FROM sqlite_master WHERE name=?`, name).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}
