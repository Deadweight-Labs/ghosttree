package store

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// correctable is what a correction may touch: the text of the request and how
// it is filed. Deliberately not state — that moves only through complete and
// drop, which demand evidence, and a text edit must not become a way around
// them. Not scope either: a request that turns out to belong to another
// repository is a different entry, not a corrected one.
var correctable = map[string]bool{"title": true, "description": true, "type": true, "priority": true}

// UpdateRequest corrects what a request says. The ledger's whole value is that
// it can be believed, and a description carries the parts that can turn out to
// be wrong: measurements, evidence, reasoning. Until this existed, that text was
// the only thing in the system with no way back — knowledge can be patched or
// deprecated, a criterion can go from met to open, a request can be dropped.
//
// A reason is required. A silent edit would leave a later reader with a text
// that reads as if it had always said this, and no way to tell that a claim was
// withdrawn. The reason and the changed field names go into the activity list,
// which already records creation, criteria and completion.
//
// A finished request may be corrected too. Being done does not make a wrong
// claim right, and that case — a completed entry whose justification collapsed —
// is exactly the one this exists for.
func (s *Store) UpdateRequest(id int64, patch map[string]string, person, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return requestdomain.NewRuleError("correction_reason_required",
			"a correction has to say why", "pass the reason the earlier text was wrong", nil)
	}
	var fields []string
	for col := range patch {
		if !correctable[col] {
			return requestdomain.NewRuleError("field_not_correctable",
				fmt.Sprintf("field %q cannot be corrected", col),
				"correct title, description, type or priority; state moves through complete or drop", nil)
		}
		fields = append(fields, col)
	}
	if len(fields) == 0 {
		return requestdomain.NewRuleError("correction_empty", "nothing to correct", "name at least one field", nil)
	}
	// Sorted so the activity entry reads the same for the same correction,
	// rather than in Go's map order.
	sort.Strings(fields)
	if typ, ok := patch["type"]; ok {
		if err := requestdomain.ValidateType(typ); err != nil {
			return err
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int64
	if err := tx.QueryRow(`SELECT id FROM requests WHERE id=?`, id).Scan(&exists); err != nil {
		return err
	}
	sets := make([]string, 0, len(fields)+1)
	args := make([]any, 0, len(fields)+2)
	for _, col := range fields {
		sets = append(sets, col+"=?")
		args = append(args, patch[col])
	}
	ts := now()
	sets = append(sets, "updated_at=?")
	args = append(args, ts, id)
	if _, err := tx.Exec(`UPDATE requests SET `+strings.Join(sets, ",")+` WHERE id=?`, args...); err != nil {
		return err
	}
	// The search projection is a copy, and a copy that is not pulled along keeps
	// answering with the withdrawn wording. Correcting REQ-165 by hand missed
	// this at first, which is half of why this function exists.
	if _, err := tx.Exec(`UPDATE search_documents SET title=r.title,body=r.description
		FROM requests r WHERE search_documents.kind='request' AND search_documents.domain_id=r.id AND r.id=?`, id); err != nil {
		return err
	}
	data := fmt.Sprintf("%s — %s", strings.Join(fields, ","), reason)
	if _, err := tx.Exec(`INSERT INTO request_activity(request_id,kind,person,data,created_at) VALUES(?,'request.corrected',?,?,?)`, id, person, data, ts); err != nil {
		return err
	}
	return tx.Commit()
}

// RemoveRequestRelation takes an edge back out of the graph.
//
// It deletes rather than marking the relation withdrawn, which is the opposite
// of how knowledge is treated. A superseded entry is kept because it records how
// an understanding changed, and reading it teaches something. A relation carries
// no content: it asserts that two entries stand in a structure. An assertion
// that was never true — a blocks edge set the wrong way round — teaches nothing
// when kept, and it does harm while it is there, because whoever reads the order
// of work out of the ledger works the entries the wrong way round.
//
// Nothing is lost by that. The activity list already records every relation that
// was added, and now also every one withdrawn, with the reason. The history of
// what was claimed survives; only the false edge is gone.
func (s *Store) RemoveRequestRelation(relationID int64, person, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return requestdomain.NewRuleError("removal_reason_required",
			"removing a relation has to say why", "pass the reason the edge was wrong", nil)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var requestID int64
	var kind, externalRef string
	var other, knowledge sql.NullInt64
	err = tx.QueryRow(`SELECT request_id,kind,other_request_id,knowledge_id,external_ref FROM request_relations WHERE id=?`, relationID).
		Scan(&requestID, &kind, &other, &knowledge, &externalRef)
	if err == sql.ErrNoRows {
		return requestdomain.NewRuleError("relation_not_found",
			fmt.Sprintf("no relation with id %d", relationID), "read the request to see which relations it has", nil)
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM request_relations WHERE id=?`, relationID); err != nil {
		return err
	}
	data := fmt.Sprintf("%s %s — %s", kind, relationTarget(other, knowledge, externalRef), reason)
	if _, err := tx.Exec(`INSERT INTO request_activity(request_id,kind,person,data,created_at) VALUES(?,'relation.removed',?,?,?)`, requestID, person, data, now()); err != nil {
		return err
	}
	return tx.Commit()
}

// relationTarget names what the withdrawn edge pointed at, so the log entry
// stands on its own once the row itself is gone.
func relationTarget(other, knowledge sql.NullInt64, externalRef string) string {
	switch {
	case other.Valid:
		return fmt.Sprintf("REQ-%d", other.Int64)
	case knowledge.Valid:
		return fmt.Sprintf("knowledge #%d", knowledge.Int64)
	default:
		return externalRef
	}
}
