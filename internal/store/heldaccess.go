package store

import (
	"regexp"
	"strconv"
)

// Vom Distiller abgeleitetes, noch nicht übernommenes Wissen und solche Requests
// gehören dem Besitzer der Session, aus der sie stammen, solange diese privat
// ist (REQ-438 D). Bestätigt oder übernimmt er sie, sind sie normales
// Projektwissen. Ausdrückliche Ablage eines Agenten (origin agent/human) ist
// davon nicht berührt.

var originSessionPattern = regexp.MustCompile(`^session:(\d+)`)

// heldOrigin prüft einen session_ref: held sagt, dass der Betrachter den
// Eintrag nicht sehen darf, owner, dass er der Besitzer der privaten
// Herkunfts-Session ist. Ohne Durchsetzung gilt nichts davon; eine unbekannte
// oder geteilte Herkunfts-Session hält nichts zurück.
func (a *ProjectAccess) heldOrigin(ref string) (held, owner bool) {
	if !a.st.AccessEnforced() {
		return false, false
	}
	m := originSessionPattern.FindStringSubmatch(ref)
	if m == nil {
		return false, false
	}
	id, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return false, false
	}
	sess, err := a.st.SessionByID(id)
	if err != nil || sess.Shared || sess.Visibility == VisGuests {
		return false, false
	}
	if a.OwnsSession(sess) {
		return false, true
	}
	return true, false
}

// knowledgeHeld: nur gestagtes, distilliertes Wissen wird zurückgehalten.
func knowledgeHeldRef(k Knowledge) string {
	if k.Origin == "distilled" && k.Confidence == "quarantined" {
		return k.SessionRef
	}
	return ""
}

// RequestObject baut das Objekt für die Prüfung eines Requests. visible ist
// falsch, wenn der Request aus der privaten Session eines anderen Kontos
// stammt und noch niemand ihn übernommen hat.
func (a *ProjectAccess) RequestObject(ref ObjectRef) (obj Object, visible bool) {
	obj = Object{Own: a.IsAuthor(ref.Person)}
	if ref.HeldBy == "" {
		return obj, true
	}
	held, owner := a.heldOrigin(ref.HeldBy)
	obj.OriginOwner = owner
	return obj, !held
}

// RequestHeldOwner: der Request ist noch nicht übernommen und gehört dem
// Betrachter als Besitzer der privaten Herkunfts-Session.
func (a *ProjectAccess) RequestHeldOwner(ref ObjectRef) bool {
	if ref.HeldBy == "" {
		return false
	}
	_, owner := a.heldOrigin(ref.HeldBy)
	return owner
}

// ClaimDistilledRequest macht einen distillierten Auftrag zu normalem
// Projektwissen: Wer als Besitzer der Herkunfts-Session an ihm arbeitet, wird
// sein Autor, und er steht danach allen Mitgliedern offen. id ist die Id der
// Art kind (Auftrag, Kriterium, Arbeit oder Relation).
func (s *Store) ClaimDistilledRequest(id int64, kind RequestRefKind, person string) error {
	if s.writer != nil {
		return queueWrite(s, []any{id, string(kind), person}, func(d *Store, p []any) error {
			return d.ClaimDistilledRequest(p[0].(int64), RequestRefKind(p[1].(string)), p[2].(string))
		})
	}
	if person == "" {
		return nil
	}
	var resolve string
	switch kind {
	case RefRequest:
		resolve = `?`
	case RefCriterion:
		resolve = `(SELECT request_id FROM request_criteria WHERE id=?)`
	case RefWork:
		resolve = `(SELECT request_id FROM request_work WHERE id=?)`
	case RefRelation:
		resolve = `(SELECT request_id FROM request_relations WHERE id=?)`
	default:
		return nil
	}
	_, err := s.db.Exec(`UPDATE requests SET person=? WHERE origin='distilled' AND person='' AND id=`+resolve, person, id)
	return err
}
