package store

import (
	"regexp"
	"strconv"
	"strings"
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

// CanReleaseKnowledge: der Betrachter besitzt die private Herkunfts-Session des
// gehaltenen Eintrags und darf die Haltung aufheben.
func (a *ProjectAccess) CanReleaseKnowledge(k Knowledge) bool {
	ref := knowledgeHeldRef(k)
	if ref == "" {
		return false
	}
	_, owner := a.heldOrigin(ref)
	return owner && a.Allow(k.Scope.Project, ResKnowledge, ActRelease, Object{OriginOwner: true})
}

var writtenSessionRef = regexp.MustCompile(`^session:([A-Za-z0-9_-]+)`)

// CheckWrittenSessionRef lässt beim Schreiben von Wissen nur einen Verweis auf
// eine Session des schreibenden Kontos zu. Ein Verweis auf eine fremde oder
// unbekannte Session ließe einen Eintrag als Fund aus ihr erscheinen.
func (a *ProjectAccess) CheckWrittenSessionRef(ref string) error {
	if !a.st.AccessEnforced() {
		return nil
	}
	m := writtenSessionRef.FindStringSubmatch(ref)
	if m == nil {
		return nil
	}
	var sess Session
	var err error
	if id, perr := strconv.ParseInt(m[1], 10, 64); perr == nil {
		sess, err = a.st.SessionByID(id)
	} else {
		sess, err = a.st.SessionByPublicID(m[1])
	}
	if err != nil || !a.OwnsSession(sess) {
		return ErrAccessForbidden
	}
	return nil
}

// requestHeldTx: der Auftrag "REQ-n" ist ein distillierter, nicht übernommener
// aus der privaten Session eines anderen Kontos. Ein Thread darf ihn nicht
// verlinken; die Antwort ist dieselbe wie für eine unbekannte Nummer.
func (a CoordAccess) requestHeldTx(tx rowQuerier, reqID string) bool {
	if !a.Store.AccessEnforced() {
		return false
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(reqID, "REQ-"), 10, 64)
	if err != nil {
		return false
	}
	var origin, person, ref string
	if tx.QueryRow(`SELECT origin, person, session_ref FROM requests WHERE id=?`, n).Scan(&origin, &person, &ref) != nil {
		return false
	}
	if origin != "distilled" || person != "" {
		return false
	}
	m := originSessionPattern.FindStringSubmatch(ref)
	if m == nil {
		return false
	}
	sid, _ := strconv.ParseInt(m[1], 10, 64)
	var account int64
	var shared int
	var visibility string
	if tx.QueryRow(`SELECT account_id, shared, visibility FROM sessions WHERE id=?`, sid).Scan(&account, &shared, &visibility) != nil {
		return false
	}
	if shared != 0 || (visibility != "" && visibility != VisPrivate) {
		return false
	}
	viewer, ok := accountNumericID(a.Principal.ID)
	return !ok || effectiveOwner(account, instanceOwnerID(tx)) != viewer
}

// releaseHeldFindingsTx hebt die Haltung der Funde auf, die ein Konto in einem
// Projekt hinterlässt, wenn es dort keine Rolle mehr hat: niemand könnte sie
// sonst je freigeben. Wissen geht als gestagt in die Queue der Prüfer, ein
// Auftrag wird auf den Namen des Kontos gestellt. Zitate aus der privaten
// Session bleiben gefiltert.
func releaseHeldFindingsTx(tx execQueryer, account int64, remote string) error {
	owner := instanceOwnerID(tx)
	const fromOwned = `EXISTS (SELECT 1 FROM sessions hs WHERE (session_ref='session:'||hs.id OR session_ref LIKE 'session:'||hs.id||'#%')
		AND (CASE WHEN hs.account_id=0 THEN ? ELSE hs.account_id END)=?)`
	if _, err := tx.Exec(`UPDATE knowledge SET confidence='staged' WHERE project=? AND origin='distilled' AND confidence='quarantined' AND `+fromOwned,
		remote, owner, account); err != nil {
		return err
	}
	_, err := tx.Exec(`UPDATE requests SET person=(SELECT name FROM persons WHERE id=?) WHERE project=? AND origin='distilled' AND person='' AND `+fromOwned,
		account, remote, owner, account)
	return err
}
