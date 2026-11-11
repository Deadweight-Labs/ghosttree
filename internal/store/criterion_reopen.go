package store

import (
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// ReopenCriterion setzt ein erfülltes oder verzichtetes Kriterium wieder auf
// offen. Das ist der Rückweg des Prüfmodus (REQ-601): ein Prüfer, der den
// Nachweis gegen den Code nicht bestätigen kann, hebt das Kriterium auf, statt
// es stehen zu lassen.
//
// Die Begründung ist Pflicht und steht im Activity-Log des Requests. Die alten
// Nachweise bleiben: sie sind die Geschichte des Kriteriums, und wer es erneut
// erfüllt, hängt einen neuen an. Ein abgeschlossener Request geht dabei wieder
// auf offen, denn "erledigt mit offenem Kriterium" darf es nicht geben (genau
// das verhindert CompleteRequest). Ein verworfener Request bleibt, wie er ist.
func (s *Store) ReopenCriterion(id int64, reason, person string, anchor *gitanchor.Anchor) error {
	if strings.TrimSpace(reason) == "" {
		return requestdomain.NewRuleError("reopen_reason_required", "reopening a criterion has to say why", "pass what the evidence does not show", nil)
	}
	if s.writer != nil {
		return queueWrite(s, []any{id, reason, person, anchor}, func(d *Store, p []any) error {
			return d.ReopenCriterion(p[0].(int64), p[1].(string), p[2].(string), p[3].(*gitanchor.Anchor))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var requestID int64
	var number int
	var requestState, criterionState string
	if err := tx.QueryRow(`SELECT c.request_id,c.number,r.state,c.state FROM request_criteria c JOIN requests r ON r.id=c.request_id WHERE c.id=?`, id).
		Scan(&requestID, &number, &requestState, &criterionState); err != nil {
		return err
	}
	if requestState == "dropped" {
		return terminalRequestError(requestID, requestState)
	}
	if criterionState == "open" {
		return requestdomain.NewRuleError("criterion_open", "acceptance criterion is already open", "nothing to reopen", nil)
	}
	ts := now()
	anchorID, originID, err := insertWriteMetaTx(tx, anchor, nil)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE request_criteria SET state='open',updated_at=? WHERE id=?`, ts, id); err != nil {
		return err
	}
	data := fmt.Sprintf("AC-%d.%d (was %s) — %s", requestID, number, criterionState, strings.TrimSpace(reason))
	if _, err := tx.Exec(`INSERT INTO request_activity(request_id,kind,person,data,created_at,anchor_id,origin_id) VALUES(?,'criterion.reopened',?,?,?,?,?)`, requestID, person, data, ts, anchorID, originID); err != nil {
		return err
	}
	if requestState == "done" {
		if _, err := tx.Exec(`UPDATE requests SET state='open',updated_at=? WHERE id=?`, ts, requestID); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO request_activity(request_id,kind,person,data,created_at,anchor_id,origin_id) VALUES(?,'request.reopened',?,?,?,?,?)`, requestID, person,
			fmt.Sprintf("AC-%d.%d reopened", requestID, number), ts, anchorID, originID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
