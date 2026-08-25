package store

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// RegressionStates sind die vier Antworten auf "womit ist das abgesichert".
//
// Der leere Zustand ist einer davon und trägt die eigentliche Unterscheidung:
// "" heisst, niemand hat den Eintrag darauf angesehen. "not_applicable" heisst,
// jemand hat ihn angesehen und entschieden, dass es hier nichts zu testen gibt
// — ein Pitfall über eine Werkzeugeigenschaft ist kein Regressionskandidat.
// Ohne diesen vierten Zustand sähe eine bewusste Entscheidung aus wie eine
// offene Aufgabe, und das beträfe die Mehrheit der Einträge.
var RegressionStates = []string{"", "covered", "uncovered", "not_applicable"}

// SetRegressionCover trägt ein, womit ein Eintrag abgesichert ist. Der Testname
// gehört zu "covered" und wird sonst verworfen: ein Name neben "uncovered" wäre
// ein Widerspruch, den später niemand auflösen kann.
func (s *Store) SetRegressionCover(id int64, state, test string) error {
	state = strings.TrimSpace(state)
	if !validRegressionState(state) {
		return fmt.Errorf("unknown regression state %q, want one of %q", state, RegressionStates)
	}
	test = strings.TrimSpace(test)
	if state == "covered" && test == "" {
		return fmt.Errorf(`regression state "covered" needs the test that does the covering`)
	}
	if state != "covered" {
		test = ""
	}
	// Bewusst nicht über UpdateKnowledge: das archiviert vor jeder Änderung die
	// bisherige Fassung. Womit ein Eintrag abgesichert ist, ist eine Aussage
	// ÜBER den Text und nicht der Text — eine neue Fassung anzulegen, weil
	// jemand einen Testnamen nachträgt, füllte die Historie mit Rauschen und
	// machte die echten Umformulierungen darin unauffindbar.
	res, err := s.db.Exec(`UPDATE knowledge SET regression_state=?, regression_test=?, updated_at=?
		WHERE id=?`, state, test, now(), id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("knowledge %d not found", id)
	}
	return nil
}

func validRegressionState(state string) bool {
	return slices.Contains(RegressionStates, state)
}

// RegressionGaps liefert die Einträge, bei denen ein Fehler behoben wurde und
// kein Test seine Wiederkehr bemerkt — der Befund, den sonst niemand erhebt.
//
// Die zweite Rückgabe zählt, was noch niemand beurteilt hat, und ist kein
// Beiwerk: ohne sie liest sich eine kurze Lückenliste als Entwarnung, während
// der Bestand in Wahrheit grösstenteils unangesehen ist.
func (s *Store) RegressionGaps(ax scope.Axes) ([]Knowledge, int, error) {
	where, args := ax.UnionWhere()
	rows, err := s.db.Query(`SELECT id,type,title,body,project,branch,machine,confidence,status,origin,
		person,confirmed_by,last_modified_by,harness,session_ref,observed_at,
		regression_state,regression_test,created_at,updated_at
		FROM knowledge WHERE regression_state='uncovered' AND status='active' AND `+where+
		` ORDER BY observed_at DESC, id DESC`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var gaps []Knowledge
	for rows.Next() {
		var k Knowledge
		if err := rows.Scan(&k.ID, &k.Type, &k.Title, &k.Body, &k.Scope.Project, &k.Scope.Branch, &k.Scope.Machine,
			&k.Confidence, &k.Status, &k.Origin, &k.Person, &k.ConfirmedBy, &k.LastModifiedBy, &k.Harness,
			&k.SessionRef, &k.ObservedAt, &k.RegressionState, &k.RegressionTest, &k.CreatedAt, &k.UpdatedAt); err != nil {
			return nil, 0, err
		}
		gaps = append(gaps, k)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	// Nur Pitfalls: eine Entscheidung oder Notiz hat keine Regressionsfrage, und
	// sie ungefragt mitzuzählen liesse die Lücke grösser aussehen, als sie ist.
	var unreviewed int
	err = s.db.QueryRow(`SELECT COUNT(*) FROM knowledge
		WHERE type='pitfall' AND regression_state='' AND status='active' AND `+where, args...).Scan(&unreviewed)
	return gaps, unreviewed, err
}
