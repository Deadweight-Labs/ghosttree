package store

import (
	"context"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/pain"
)

// oldPainsDDL is the definition of the pains table before REQ-616: four states, no reason.
const oldPainsDDL = `CREATE TABLE pains_old(
  id INTEGER PRIMARY KEY,
  project TEXT NOT NULL CHECK(project != ''),
  title TEXT NOT NULL CHECK(trim(title) != ''),
  body TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT 'active' CHECK(state IN ('active','mitigated','resolved','obsolete')),
  remaining TEXT NOT NULL DEFAULT '',
  person TEXT NOT NULL DEFAULT '', session_ref TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  CHECK(state != 'mitigated' OR trim(remaining) != ''))`

// downgradePains rebuilds the pains table the way an older database has it, with
// its rows, so the migration has something to carry over.
func downgradePains(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	conn, err := st.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
	for _, q := range []string{
		oldPainsDDL,
		`INSERT INTO pains_old SELECT id,project,title,body,state,remaining,person,session_ref,created_at,updated_at FROM pains`,
		`DROP TABLE pains`,
		`ALTER TABLE pains_old RENAME TO pains`,
		`CREATE INDEX pains_project_state ON pains(project, state)`,
	} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			t.Fatalf("%.50s: %v", q, err)
		}
	}
}

// REQ-616 Kriterium 1: Schema und Modell kennen accepted, die Migration trägt
// Zeilen und alles, was auf sie zeigt, unverändert hinüber und ist wiederholbar.
func TestPainsMigrationAddsAcceptedAndIsRepeatable(t *testing.T) {
	w := newPainWorld(t)
	first := w.newPain(t, "mia")
	second := w.newPain(t, "mia")
	if _, err := w.pa["robin"].SetPainState(second.Pain.ID, pain.Resolved, "", pain.Evidence{Kind: "url", Ref: "https://x.example/1"}, "", pain.Actor{Person: "robin"}); err != nil {
		t.Fatal(err)
	}
	downgradePains(t, w.st)
	// Der alte Stand kennt accepted nicht.
	if _, err := w.st.db.Exec(`UPDATE pains SET state='accepted' WHERE id=?`, first.Pain.ID); err == nil {
		t.Fatal("the old table accepted the new state")
	}
	if err := allowPainAccepted(w.st.db); err != nil {
		t.Fatalf("migration: %v", err)
	}
	// Zeilen, Nummern, Zustände, Ereignisse und Autoren sind noch da.
	var n int
	var state string
	if err := w.st.db.QueryRow(`SELECT COUNT(*) FROM pains`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("pains after the rebuild: %d %v", n, err)
	}
	if err := w.st.db.QueryRow(`SELECT state FROM pains WHERE id=?`, second.Pain.ID).Scan(&state); err != nil || state != "resolved" {
		t.Fatalf("state of the second pain: %q %v", state, err)
	}
	var events, authors int
	w.st.db.QueryRow(`SELECT COUNT(*) FROM pain_events WHERE pain_id IN (?,?)`, first.Pain.ID, second.Pain.ID).Scan(&events)
	w.st.db.QueryRow(`SELECT COUNT(*) FROM pain_authors`).Scan(&authors)
	if events != 3 || authors != 2 {
		t.Fatalf("events %d (want 3), authors %d (want 2)", events, authors)
	}
	if rows, err := w.st.db.Query(`PRAGMA foreign_key_check`); err != nil {
		t.Fatal(err)
	} else {
		if rows.Next() {
			t.Error("a row points at a pain that is gone")
		}
		rows.Close()
	}
	// Foreign keys are back on for the connections of the pool.
	var fk int
	if err := w.st.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d %v", fk, err)
	}
	// CHECK: accepted needs its reason.
	if _, err := w.st.db.Exec(`UPDATE pains SET state='accepted' WHERE id=?`, first.Pain.ID); err == nil {
		t.Error("accepted without a reason was stored")
	}
	if _, err := w.st.db.Exec(`UPDATE pains SET state='accepted', accepted_reason='so bleibt es' WHERE id=?`, first.Pain.ID); err != nil {
		t.Errorf("accepted with a reason: %v", err)
	}
	// Wiederholbar: ein zweiter Lauf ändert nichts und kann nicht scheitern.
	var before string
	w.st.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='pains'`).Scan(&before)
	if err := allowPainAccepted(w.st.db); err != nil {
		t.Fatalf("second run: %v", err)
	}
	var after string
	w.st.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='pains'`).Scan(&after)
	if before != after || !strings.Contains(after, "'accepted'") {
		t.Errorf("the second run changed the table:\n%s\n%s", before, after)
	}
	if err := w.st.db.QueryRow(`SELECT COUNT(*) FROM pains`).Scan(&n); err != nil || n != 2 {
		t.Errorf("rows after the second run: %d %v", n, err)
	}
	// Eine frische Datenbank hat die neue Definition von Anfang an.
	fresh := newPainWorld(t)
	var ddl string
	fresh.st.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='pains'`).Scan(&ddl)
	if !strings.Contains(ddl, "'accepted'") || !strings.Contains(ddl, "accepted_reason") {
		t.Errorf("a new database lacks accepted:\n%s", ddl)
	}
}

// REQ-616 Kriterium 2 und 3 im Store: Begründung, Rückweg, Reihenfolge, geschlossen.
func TestAcceptedPainNeedsAReasonKeepsItAndIsClosed(t *testing.T) {
	w := newPainWorld(t)
	robin := w.pa["robin"]
	actor := pain.Actor{Person: "robin"}
	p := w.newPain(t, "mia").Pain

	if _, err := robin.SetPainState(p.ID, pain.Accepted, "", pain.Evidence{}, "  ", actor); ruleCode(err) != "accepted_reason_required" {
		t.Fatalf("accepting without a reason: %v", err)
	}
	if d, _ := w.st.PainByID(p.ID); d.State != pain.Active {
		t.Fatalf("a refused change moved the pain to %s", d.State)
	}
	d, err := robin.SetPainState(p.ID, pain.Accepted, "", pain.Evidence{}, "Aufwand steht in keinem Verhältnis", actor)
	if err != nil {
		t.Fatal(err)
	}
	if d.Pain.State != pain.Accepted || d.Pain.AcceptedReason != "Aufwand steht in keinem Verhältnis" {
		t.Fatalf("pain = %+v", d.Pain)
	}
	if last := d.Events[len(d.Events)-1]; last.ToState != pain.Accepted || last.Note != "Aufwand steht in keinem Verhältnis" {
		t.Fatalf("the history lacks the reason: %+v", last)
	}

	// accepted zählt als geschlossen: nicht in der Standardliste, in "accepted" und "all".
	ids := func(state string) []int64 {
		list, err := robin.SearchPains(pain.Filter{Project: roleProject, State: state})
		if err != nil {
			t.Fatal(err)
		}
		var out []int64
		for _, x := range list {
			out = append(out, x.ID)
		}
		return out
	}
	if got := ids(""); len(got) != 0 {
		t.Errorf("the open list shows an accepted pain: %v", got)
	}
	if got := ids("accepted"); len(got) != 1 || got[0] != p.ID {
		t.Errorf("state accepted: %v", got)
	}
	// Reihenfolge: active, mitigated, accepted, resolved, obsolete.
	mk := func(state string) int64 {
		x := w.newPain(t, "mia").Pain
		var err error
		switch state {
		case pain.Active:
		case pain.Mitigated:
			_, err = robin.SetPainState(x.ID, pain.Mitigated, "Rest", pain.Evidence{Kind: "url", Ref: "https://x.example/m"}, "", actor)
		case pain.Resolved:
			_, err = robin.SetPainState(x.ID, pain.Resolved, "", pain.Evidence{Kind: "url", Ref: "https://x.example/r"}, "", actor)
		case pain.Obsolete:
			_, err = robin.SetPainState(x.ID, pain.Obsolete, "", pain.Evidence{}, "weg", actor)
		}
		if err != nil {
			t.Fatal(err)
		}
		return x.ID
	}
	obsolete, resolved, mitigated, active := mk(pain.Obsolete), mk(pain.Resolved), mk(pain.Mitigated), mk(pain.Active)
	got := ids("all")
	want := []int64{active, mitigated, p.ID, resolved, obsolete}
	if len(got) != len(want) {
		t.Fatalf("all = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order of all = %v, want active, mitigated, accepted, resolved, obsolete = %v", got, want)
		}
	}

	// Rückweg: nach active nur mit Notiz; der Grund bleibt nicht stehen.
	if _, err := robin.SetPainState(p.ID, pain.Active, "", pain.Evidence{}, "", actor); ruleCode(err) != "note_required" {
		t.Fatalf("back to active without a note: %v", err)
	}
	d, err = robin.SetPainState(p.ID, pain.Active, "", pain.Evidence{}, "Es stört doch", actor)
	if err != nil {
		t.Fatal(err)
	}
	if d.Pain.State != pain.Active || d.Pain.AcceptedReason != "" {
		t.Fatalf("after the way back: %+v", d.Pain)
	}
}
