package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// Ideen-Werkstatt (REQ-611): Texte mit Fassungen, Konzept, Phasen, der einmalige
// Hinweis „ausarbeiten?“. Rechte wie bei der Idee selbst (ResIdea): schreiben ab
// member, nach der Freigabe nur noch der Owner der Idee oder ein Maintainer.

// ideaFor lädt eine Idee für eine Aktion. Eine Idee, die es nicht gibt, und eine
// in einem verborgenen Projekt antworten gleich.
func (a *ProjectAccess) ideaFor(id int64, act Action) (idea.Idea, error) {
	i, err := a.st.IdeaByID(id)
	if err != nil {
		a.Filtered()
		return idea.Idea{}, ideaNotFound(id)
	}
	if err := a.CheckIdea(i, act); err != nil {
		if err == ErrAccessNotFound {
			return idea.Idea{}, ideaNotFound(id)
		}
		return idea.Idea{}, err
	}
	return i, nil
}

func ideaClosed(i idea.Idea) error {
	return requestdomain.NewRuleError("idea_closed", fmt.Sprintf("%s is %s", i.HumanID(), i.Phase), "reopen it first, or record a new idea", nil)
}

// attachIdeaWorkshopMeta füllt Zahl der offenen Entscheidungen und das
// Konzept-Kennzeichen für eine Liste von Ideen. ids sind die Argumente für IN.
func (s *Store) attachIdeaWorkshopMeta(list []idea.Idea, idx map[int64]int, args []any) error {
	rows, err := s.db.Query(`SELECT subject_id, COUNT(*) FROM open_decisions WHERE subject_kind='idea' AND state='open' AND subject_id IN (`+placeholders(len(args))+`) GROUP BY subject_id`, args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			rows.Close()
			return err
		}
		list[idx[id]].OpenDecisions = n
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = s.db.Query(`SELECT DISTINCT idea_id FROM idea_texts WHERE part='concept' AND idea_id IN (`+placeholders(len(args))+`)`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		list[idx[id]].HasConcept = true
	}
	return rows.Err()
}

// ---- Texte mit Fassungen

func scanText(row interface{ Scan(...any) error }, withBody bool) (idea.Text, error) {
	var t idea.Text
	var agent int
	var body string
	err := row.Scan(&t.Part, &t.Revision, &body, &t.Digest, &t.Source, &t.Message, &t.Person, &agent, &t.CreatedAt)
	t.ByAgent = agent != 0
	t.Bytes = len(body)
	if withBody {
		t.Body = body
	}
	return t, err
}

const ideaTextCols = `part,revision,body,digest,source,message,person,by_agent,created_at`

// ideaTexts liefert die Fassungen eines Teils, älteste zuerst, ohne Text.
func (s *Store) ideaTexts(ideaID int64, part string) ([]idea.Text, error) {
	if s.reader != nil {
		return s.reader.ideaTexts(ideaID, part)
	}
	rows, err := s.db.Query(`SELECT `+ideaTextCols+` FROM idea_texts WHERE idea_id=? AND part=? ORDER BY revision`, ideaID, part)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []idea.Text{}
	for rows.Next() {
		t, err := scanText(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ideaTextAt liefert eine Fassung mit Text; revision 0 heißt die jüngste.
// sql.ErrNoRows, wenn es sie nicht gibt.
func (s *Store) ideaTextAt(ideaID int64, part string, revision int) (idea.Text, error) {
	if s.reader != nil {
		return s.reader.ideaTextAt(ideaID, part, revision)
	}
	q := `SELECT ` + ideaTextCols + ` FROM idea_texts WHERE idea_id=? AND part=?`
	args := []any{ideaID, part}
	if revision > 0 {
		q += ` AND revision=?`
		args = append(args, revision)
	} else {
		q += ` ORDER BY revision DESC LIMIT 1`
	}
	return scanText(s.db.QueryRow(q, args...), true)
}

type ideaTextCommit struct {
	IdeaID  int64
	Part    string
	Body    string
	Source  string
	Message string
	Person  string
	ByAgent bool
	// Base ist die Fassung, auf der der Schreiber aufbaut (0 = egal).
	Base int
}

// commitIdeaText hängt eine Fassung an. Gleicht der Text dem der jüngsten
// Fassung, entsteht keine neue (Unchanged). Ein Konzept hebt eine Rohidee auf
// die Phase concept.
func (s *Store) commitIdeaText(c ideaTextCommit) (idea.Text, bool, error) {
	if s.writer != nil {
		type result struct {
			T         idea.Text
			Unchanged bool
		}
		r, err := queueValue(s, []any{c}, func(d *Store, p []any) (result, error) {
			t, u, err := d.commitIdeaText(p[0].(ideaTextCommit))
			return result{t, u}, err
		})
		return r.T, r.Unchanged, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return idea.Text{}, false, err
	}
	defer tx.Rollback()
	head := 0
	var headDigest string
	err = tx.QueryRow(`SELECT revision, digest FROM idea_texts WHERE idea_id=? AND part=? ORDER BY revision DESC LIMIT 1`, c.IdeaID, c.Part).Scan(&head, &headDigest)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return idea.Text{}, false, err
	}
	if c.Base > 0 && c.Base != head {
		return idea.Text{}, false, requestdomain.NewRuleError("text_changed", fmt.Sprintf("the %s is at revision %d, not %d", c.Part, head, c.Base), "read the current version and apply your change to it", nil)
	}
	digest := Digest(c.Body)
	if head > 0 && digest == headDigest {
		tx.Rollback()
		t, err := s.ideaTextAt(c.IdeaID, c.Part, head)
		return t, true, err
	}
	ts := now()
	if _, err := tx.Exec(`INSERT INTO idea_texts(idea_id,part,revision,body,digest,source,message,person,by_agent,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		c.IdeaID, c.Part, head+1, c.Body, digest, c.Source, c.Message, c.Person, boolInt(c.ByAgent), ts); err != nil {
		return idea.Text{}, false, err
	}
	if _, err := tx.Exec(`UPDATE ideas SET updated_at=? WHERE id=?`, ts, c.IdeaID); err != nil {
		return idea.Text{}, false, err
	}
	if c.Part == idea.PartConcept {
		if _, err := tx.Exec(`UPDATE ideas SET phase='concept' WHERE id=? AND phase='raw'`, c.IdeaID); err != nil {
			return idea.Text{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return idea.Text{}, false, err
	}
	t, err := s.ideaTextAt(c.IdeaID, c.Part, head+1)
	return t, false, err
}

// PutIdeaTextInput hängt Rohfassung oder Kurzfassung an.
type PutIdeaTextInput struct {
	// Part ist raw oder summary; das Konzept hat PutIdeaConcept.
	Part string
	Body string
	// Source nennt die Herkunft der Rohfassung („Sprachnachricht“, „Meeting
	// 2026-11-08“), eine Zeile.
	Source  string
	Message string
	Base    int
	Actor   idea.Actor
}

// writeActFor ist das Recht, das ein Text der Idee braucht: bis zur Freigabe
// genügt Mitarbeit, danach nur noch der Owner der Idee oder ein Maintainer.
func writeActFor(i idea.Idea) Action {
	switch i.Phase {
	case idea.PhaseApproved, idea.PhaseBuilding:
		return ActEdit
	}
	return ActWork
}

// PutIdeaText legt eine neue Fassung der Rohfassung oder der Kurzfassung an.
// Die Rohfassung bleibt unverändert und versioniert; mehrere Fassungen sind
// mehrere Quellen (ein Transkript, dann ein zweites Gespräch).
func (a *ProjectAccess) PutIdeaText(id int64, in PutIdeaTextInput) (idea.Text, bool, error) {
	if err := refuseSecrets("body", in.Body, "source", in.Source, "message", in.Message); err != nil {
		return idea.Text{}, false, err
	}
	i, err := a.ideaFor(id, ActWork)
	if err != nil {
		return idea.Text{}, false, err
	}
	if i.Phase == idea.PhaseDismissed || i.Phase == idea.PhaseDone {
		return idea.Text{}, false, ideaClosed(i)
	}
	if act := writeActFor(i); act != ActWork {
		if err := a.CheckIdea(i, act); err != nil {
			return idea.Text{}, false, err
		}
	}
	c := ideaTextCommit{IdeaID: id, Part: in.Part, Person: in.Actor.Person, ByAgent: in.Actor.Agent, Base: in.Base}
	if c.Message, err = idea.CleanOneLine(in.Message, "message", "the note", idea.MaxVersionNote, false); err != nil {
		return idea.Text{}, false, err
	}
	switch in.Part {
	case idea.PartRaw:
		if err := idea.CleanRaw(in.Body); err != nil {
			return idea.Text{}, false, err
		}
		c.Body = in.Body // unverändert
		if c.Source, err = idea.CleanOneLine(in.Source, "source", "the source", idea.MaxSourceLabel, false); err != nil {
			return idea.Text{}, false, err
		}
	case idea.PartSummary:
		if c.Body, err = idea.CleanSection(in.Body, "summary"); err != nil {
			return idea.Text{}, false, err
		}
		if c.Body == "" {
			return idea.Text{}, false, requestdomain.NewRuleError("summary_required", "the summary is empty", "write two or three sentences", nil)
		}
	default:
		return idea.Text{}, false, requestdomain.NewRuleError("invalid_part", fmt.Sprintf("unknown part %q", in.Part), "use raw or summary; the concept has its own call", nil)
	}
	return a.st.commitIdeaText(c)
}

// ConceptVersion ist eine Fassung des Konzepts.
type ConceptVersion struct {
	Revision  int          `json:"revision"`
	Concept   idea.Concept `json:"concept"`
	Digest    string       `json:"digest"`
	Message   string       `json:"message,omitempty"`
	Person    string       `json:"person,omitempty"`
	ByAgent   bool         `json:"by_agent,omitempty"`
	CreatedAt string       `json:"created_at"`
}

func conceptOf(t idea.Text) (ConceptVersion, error) {
	v := ConceptVersion{Revision: t.Revision, Digest: t.Digest, Message: t.Message, Person: t.Person, ByAgent: t.ByAgent, CreatedAt: t.CreatedAt}
	if err := json.Unmarshal([]byte(t.Body), &v.Concept); err != nil {
		return v, fmt.Errorf("concept of revision %d: %w", t.Revision, err)
	}
	if v.Concept.Requirements == nil {
		v.Concept.Requirements = []string{}
	}
	return v, nil
}

// ideaConcept liefert eine Fassung des Konzepts (0 = jüngste); false, wenn es
// noch keines gibt.
func (s *Store) ideaConcept(ideaID int64, revision int) (ConceptVersion, bool, error) {
	t, err := s.ideaTextAt(ideaID, idea.PartConcept, revision)
	if errors.Is(err, sql.ErrNoRows) {
		return ConceptVersion{}, false, nil
	}
	if err != nil {
		return ConceptVersion{}, false, err
	}
	v, err := conceptOf(t)
	return v, err == nil, err
}

// IdeaConcept liefert das Konzept (revision 0 = jüngste Fassung). Leserecht an
// der Idee genügt.
func (a *ProjectAccess) IdeaConcept(id int64, revision int) (ConceptVersion, bool, error) {
	if _, err := a.ideaFor(id, ActRead); err != nil {
		return ConceptVersion{}, false, err
	}
	return a.st.ideaConcept(id, revision)
}

// IdeaText liefert eine Fassung mit Text (revision 0 = jüngste).
func (a *ProjectAccess) IdeaText(id int64, part string, revision int) (idea.Text, error) {
	if _, err := a.ideaFor(id, ActRead); err != nil {
		return idea.Text{}, err
	}
	if part != idea.PartRaw && part != idea.PartSummary && part != idea.PartConcept {
		return idea.Text{}, requestdomain.NewRuleError("invalid_part", fmt.Sprintf("unknown part %q", part), "use raw, summary or concept", nil)
	}
	t, err := a.st.ideaTextAt(id, part, revision)
	if errors.Is(err, sql.ErrNoRows) {
		return idea.Text{}, requestdomain.NewRuleError("text_not_found", "no such version", "list the versions of the idea", nil)
	}
	return t, err
}

// IdeaTextVersions listet die Fassungen eines Teils ohne Text.
func (a *ProjectAccess) IdeaTextVersions(id int64, part string) ([]idea.Text, error) {
	if _, err := a.ideaFor(id, ActRead); err != nil {
		return nil, err
	}
	if part != idea.PartRaw && part != idea.PartSummary && part != idea.PartConcept {
		return nil, requestdomain.NewRuleError("invalid_part", fmt.Sprintf("unknown part %q", part), "use raw, summary or concept", nil)
	}
	return a.st.ideaTexts(id, part)
}

// PutIdeaConceptInput ändert das Konzept abschnittsweise.
type PutIdeaConceptInput struct {
	Patch   idea.ConceptPatch
	Message string
	// Base ist die Fassung, auf der der Schreiber aufbaut (0 = egal).
	Base  int
	Actor idea.Actor
}

// conceptFields lists the texts of a concept patch for the secret check, a
// section by its name and a requirement by its number.
func conceptFields(in PutIdeaConceptInput) []string {
	p := in.Patch
	var f []string
	for _, s := range []struct {
		name string
		text *string
	}{{"about", p.About}, {"what", p.What}, {"how", p.How}, {"not", p.Not}, {"questions", p.Questions}} {
		if s.text != nil {
			f = append(f, s.name, *s.text)
		}
	}
	if p.Requirements != nil {
		for i, r := range *p.Requirements {
			f = append(f, indexed("requirements", i), r)
		}
	}
	return append(f, "message", in.Message)
}

// PutIdeaConcept legt eine neue Fassung des Konzepts an: die jüngste mit dem
// Patch darüber. Abschnitte, die der Patch nicht nennt, bleiben stehen.
func (a *ProjectAccess) PutIdeaConcept(id int64, in PutIdeaConceptInput) (ConceptVersion, bool, error) {
	if err := refuseSecrets(conceptFields(in)...); err != nil {
		return ConceptVersion{}, false, err
	}
	i, err := a.ideaFor(id, ActWork)
	if err != nil {
		return ConceptVersion{}, false, err
	}
	if i.Phase == idea.PhaseDismissed || i.Phase == idea.PhaseDone {
		return ConceptVersion{}, false, ideaClosed(i)
	}
	if act := writeActFor(i); act != ActWork {
		if err := a.CheckIdea(i, act); err != nil {
			return ConceptVersion{}, false, err
		}
	}
	// Ab der Freigabe sind die Anforderungen die Abmachung. Ein Agent ändert sie
	// nicht: er könnte unbequeme streichen und die Idee dann für umgesetzt erklären.
	if in.Patch.Requirements != nil && in.Actor.Agent && (i.Phase == idea.PhaseApproved || i.Phase == idea.PhaseBuilding) {
		return ConceptVersion{}, false, humanOnly("changing the requirements of an approved idea")
	}
	msg, err := idea.CleanOneLine(in.Message, "message", "the note", idea.MaxVersionNote, false)
	if err != nil {
		return ConceptVersion{}, false, err
	}
	cur, _, err := a.st.ideaConcept(id, 0)
	if err != nil {
		return ConceptVersion{}, false, err
	}
	next, err := cur.Concept.Apply(in.Patch)
	if err != nil {
		return ConceptVersion{}, false, err
	}
	body, err := json.Marshal(next)
	if err != nil {
		return ConceptVersion{}, false, err
	}
	t, unchanged, err := a.st.commitIdeaText(ideaTextCommit{IdeaID: id, Part: idea.PartConcept, Body: string(body), Message: msg,
		Person: in.Actor.Person, ByAgent: in.Actor.Agent, Base: in.Base})
	if err != nil {
		return ConceptVersion{}, false, err
	}
	v, err := conceptOf(t)
	return v, unchanged, err
}

// ---- Phasen

// IdeaActorFor bildet den Schreiber der Werkstatt aus dem Principal. Mensch ist
// nur, wer aus einer interaktiven Websitzung handelt (OIDC, Login-Link oder Code,
// TokenKind web); jedes Bearer- oder Gerätetoken (API, MCP, CLI) gilt als Agent,
// auch mit eigener Session-ID, denn ein Token liegt in der Konfiguration von
// Rechnern, auf denen Agenten laufen. Die Weboberfläche ruft die Funktionen von
// ProjectAccess mit diesem Schreiber auf; nur so kann sie Ideen freigeben,
// menschliche Entscheidungen treffen oder kippen und als Person abstimmen.
func IdeaActorFor(p Principal, sessionRef string) idea.Actor {
	account, _ := AccountNumericID(p.ID)
	return idea.Actor{AccountID: account, Person: p.Label, SessionRef: sessionRef, Agent: p.TokenKind != WebSessionKind}
}

func humanOnly(what string) error {
	return requestdomain.NewRuleError("human_only", what+" needs a person in the web interface, a token (API, MCP, CLI) does not count as one",
		"propose it as a decision with way human (idea action=decision_open); a person decides it there", nil)
}

// SetIdeaPhase wechselt die Phase. Freigeben (approved) darf nur der Owner der
// Idee oder ein Maintainer (ActEdit), und nur ein Mensch aus einer Websitzung:
// das gilt auch für den Weg zurück aus der Freigabe, denn beides ist die
// Entscheidung „so wollen wir es“. Die übrigen Wechsel ändern den Stand der
// ganzen Idee und brauchen ActEdit. Die Regeln der Übergänge stehen in
// idea.ValidatePhase.
//
// Beim Freigeben nennt der Mensch die Fassung des Konzepts, die er gesehen hat
// (expectedRevision, Pflicht): schrieb inzwischen jemand eine neuere, lehnt die
// Freigabe in derselben Transaktion ab (concept_changed), und der Marker hält genau
// diese Fassung samt ihren Anforderungen. Bei anderen Wechseln zählt der Wert nicht.
func (a *ProjectAccess) SetIdeaPhase(id int64, to string, actor idea.Actor, expectedRevision int) (idea.Idea, error) {
	i, err := a.ideaFor(id, ActEdit)
	if err != nil {
		return idea.Idea{}, err
	}
	if (to == idea.PhaseApproved || to == idea.PhaseDone || i.Phase == idea.PhaseApproved) && actor.Agent {
		return idea.Idea{}, humanOnly("approving an idea, taking the approval back or marking it done,")
	}
	if i.Phase == idea.PhaseDismissed {
		return idea.Idea{}, ideaClosed(i)
	}
	open := false
	for _, rid := range i.Requests {
		if d, err := a.st.requestStateOf(rid); err == nil && d == "open" {
			open = true
		}
	}
	if err := idea.ValidatePhase(i.Phase, to, i.HasConcept, len(i.Requests) > 0, open); err != nil {
		return idea.Idea{}, err
	}
	if to == idea.PhaseDone {
		cov, err := a.st.requirementCoverage(id)
		if err != nil {
			return idea.Idea{}, err
		}
		if gaps := uncoveredOf(cov); len(gaps) > 0 {
			nos := make([]string, len(gaps))
			for n, g := range gaps {
				nos[n] = fmt.Sprintf("%d", g.No)
			}
			return idea.Idea{}, requestdomain.NewRuleError("requirements_uncovered", fmt.Sprintf("requirements %s have no met criterion and are not waived", strings.Join(nos, ", ")),
				"meet a criterion that came out of the requirement, or waive it with a reason (idea action=requirement_waive)", nil)
		}
	}
	if to == idea.PhaseApproved {
		// Der Freigabe-Marker entsteht mit dem Phasenwechsel, in einer Transaktion:
		// wer, wann, welche Fassung des Konzepts, welche Anforderungen.
		if expectedRevision < 1 {
			return idea.Idea{}, requestdomain.NewRuleError("revision_required", "approving names the version of the concept that was seen", "pass the revision shown with the concept", nil)
		}
		if err := a.st.commitIdeaApproval(id, i.Phase, expectedRevision, actor); err != nil {
			return idea.Idea{}, err
		}
		return a.IdeaView(id)
	}
	if err := a.st.commitIdeaPhase(id, i.Phase, to); err != nil {
		return idea.Idea{}, err
	}
	return a.IdeaView(id)
}

// ideaApproval ist der aktive Freigabe-Marker einer Idee.
type ideaApproval struct {
	Revision     int
	Requirements []string
	Person       string
	At           string
}

// activeApproval liefert den Marker der Freigabe, die noch gilt; false ohne.
func (s *Store) activeApproval(ideaID int64) (ideaApproval, bool, error) {
	if s.reader != nil {
		return s.reader.activeApproval(ideaID)
	}
	var ap ideaApproval
	var raw string
	err := s.db.QueryRow(`SELECT concept_revision, requirements, person, approved_at FROM idea_approvals WHERE idea_id=? AND revoked_at='' ORDER BY id DESC LIMIT 1`, ideaID).
		Scan(&ap.Revision, &raw, &ap.Person, &ap.At)
	if errors.Is(err, sql.ErrNoRows) {
		return ap, false, nil
	}
	if err != nil {
		return ap, false, err
	}
	if err := json.Unmarshal([]byte(raw), &ap.Requirements); err != nil {
		return ap, false, err
	}
	return ap, true, nil
}

func (s *Store) commitIdeaApproval(id int64, from string, revision int, actor idea.Actor) error {
	if s.writer != nil {
		return queueWrite(s, []any{id, from, revision, actor}, func(d *Store, p []any) error {
			return d.commitIdeaApproval(p[0].(int64), p[1].(string), p[2].(int), p[3].(idea.Actor))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Die neueste Fassung in derselben Transaktion: weicht sie von der gesehenen
	// ab, wird nichts freigegeben, was der Mensch nie gesehen hat.
	var head int
	var body string
	if err := tx.QueryRow(`SELECT revision, body FROM idea_texts WHERE idea_id=? AND part='concept' ORDER BY revision DESC LIMIT 1`, id).Scan(&head, &body); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return requestdomain.NewRuleError("concept_required", "an idea is approved with a concept", "write the concept first (idea action=concept)", nil)
		}
		return err
	}
	if head != revision {
		return requestdomain.NewRuleError("concept_changed", fmt.Sprintf("the concept is at version %d, you saw %d", head, revision), "read the current concept and approve that", nil)
	}
	var c idea.Concept
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		return err
	}
	if c.Requirements == nil {
		c.Requirements = []string{}
	}
	raw, err := json.Marshal(c.Requirements)
	if err != nil {
		return err
	}
	ts := now()
	res, err := tx.Exec(`UPDATE ideas SET phase='approved', updated_at=? WHERE id=? AND phase=? AND state<>'dismissed'`, ts, id, from)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return requestdomain.NewRuleError("idea_changed", "the idea changed in the meantime", "read it again", nil)
	}
	if _, err := tx.Exec(`UPDATE idea_approvals SET revoked_at=? WHERE idea_id=? AND revoked_at=''`, ts, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO idea_approvals(idea_id,concept_revision,requirements,person,account_id,approved_at) VALUES(?,?,?,?,?,?)`,
		id, revision, string(raw), actor.Person, actor.AccountID, ts); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) requestStateOf(id int64) (string, error) {
	if s.reader != nil {
		return s.reader.requestStateOf(id)
	}
	var st string
	err := s.db.QueryRow(`SELECT state FROM requests WHERE id=?`, id).Scan(&st)
	return st, err
}

func (s *Store) commitIdeaPhase(id int64, from, to string) error {
	if s.writer != nil {
		return queueWrite(s, []any{id, from, to}, func(d *Store, p []any) error {
			return d.commitIdeaPhase(p[0].(int64), p[1].(string), p[2].(string))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	res, err := tx.Exec(`UPDATE ideas SET phase=?, updated_at=? WHERE id=? AND phase=? AND state<>'dismissed'`, to, ts, id, from)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return requestdomain.NewRuleError("idea_changed", "the idea changed in the meantime", "read it again", nil)
	}
	// Wer die Freigabe zurücknimmt, widerruft ihren Marker.
	if from == idea.PhaseApproved && to != idea.PhaseBuilding {
		if _, err := tx.Exec(`UPDATE idea_approvals SET revoked_at=? WHERE idea_id=? AND revoked_at=''`, ts, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// markIdeaDiscussing hebt eine Idee in Phase discussion, wenn eine Diskussion
// an ihr beginnt (raw oder concept); alle anderen Phasen bleiben.
func (s *Store) markIdeaDiscussing(id int64) error {
	if s.writer != nil {
		return queueWrite(s, []any{id}, func(d *Store, p []any) error { return d.markIdeaDiscussing(p[0].(int64)) })
	}
	_, err := s.db.Exec(`UPDATE ideas SET phase='discussion', updated_at=? WHERE id=? AND phase IN ('raw','concept') AND state<>'dismissed'`, now(), id)
	return err
}

// StartIdeaDiscussion vermerkt, dass an der Idee diskutiert wird: Phase
// discussion, soweit sie noch raw oder concept ist. Die Diskussion selbst ist ein
// Thread, verknüpft mit kind=idea, id=IDEA-n (thread_open).
func (a *ProjectAccess) StartIdeaDiscussion(id int64) (idea.Idea, error) {
	if _, err := a.ideaFor(id, ActWork); err != nil {
		return idea.Idea{}, err
	}
	if err := a.st.markIdeaDiscussing(id); err != nil {
		return idea.Idea{}, err
	}
	return a.IdeaView(id)
}

// ---- Gesamtansicht

// IdeaWorkshop ist alles, was eine Seite der Idee braucht.
type IdeaWorkshop struct {
	Idea        idea.Idea                  `json:"idea"`
	Raw         []idea.Text                `json:"raw"`
	Summary     *idea.Text                 `json:"summary,omitempty"`
	Concept     *ConceptVersion            `json:"concept,omitempty"`
	ConceptRevs []idea.Text                `json:"concept_revisions"`
	Coverage    []idea.RequirementCoverage `json:"coverage"`
	Decisions   []idea.Decision            `json:"decisions"`
	Attachments []idea.AttachmentMeta      `json:"attachments"`
	// Affects sind die Features, die die Idee ändert, mit ihrer aktuellen Fassung
	// (Ist) neben dem Konzept (Soll), soweit der Betrachter sie lesen darf (REQ-612).
	Affects []IdeaFeature `json:"affects"`
}

// IdeaWorkshop liest die Idee mit Rohfassungen (ohne Text, eine Zeile je
// Fassung), Kurzfassung, Konzept, Entscheidungen und Anhängen, soweit der
// Betrachter sie lesen darf.
func (a *ProjectAccess) IdeaWorkshop(id int64) (IdeaWorkshop, error) {
	i, err := a.IdeaView(id)
	if err != nil {
		return IdeaWorkshop{}, err
	}
	w := IdeaWorkshop{Idea: i}
	if w.Raw, err = a.st.ideaTexts(id, idea.PartRaw); err != nil {
		return w, err
	}
	if t, err := a.st.ideaTextAt(id, idea.PartSummary, 0); err == nil {
		w.Summary = &t
	} else if !errors.Is(err, sql.ErrNoRows) {
		return w, err
	}
	if c, ok, err := a.st.ideaConcept(id, 0); err != nil {
		return w, err
	} else if ok {
		w.Concept = &c
	}
	if w.ConceptRevs, err = a.st.ideaTexts(id, idea.PartConcept); err != nil {
		return w, err
	}
	if w.Coverage, err = a.IdeaCoverage(id); err != nil {
		return w, err
	}
	if w.Decisions, err = a.DecisionsOf(idea.SubjectIdea, id); err != nil {
		return w, err
	}
	if w.Attachments, err = a.AttachmentsOf(idea.SubjectIdea, id); err != nil {
		return w, err
	}
	if w.Affects, err = a.IdeaAffects(id); err != nil {
		return w, err
	}
	return w, nil
}

// ---- Vorschlag „ausarbeiten?“

// ElaborationHint liefert höchstens einmal je Idee den Vorschlag, sie
// auszuarbeiten, und nur für eine Idee, die nach einem größeren Vorhaben aussieht
// und noch nichts angehängt hat. Er ist ein Vorschlag, nie eine Bedingung: die
// Idee ist längst abgelegt. Wer ihn bekommen hat, bekommt ihn nicht wieder.
func (a *ProjectAccess) ElaborationHint(i idea.Idea) string {
	if i.HasConcept || !idea.LooksLarge(i.Title, i.Body) || a.CheckIdea(i, ActWork) != nil {
		return ""
	}
	if raw, err := a.st.ideaTexts(i.ID, idea.PartRaw); err != nil || len(raw) > 0 {
		return ""
	}
	if ok, err := a.st.claimIdeaHint(i.ID); err != nil || !ok {
		return ""
	}
	return idea.ElaborationHintText
}

func (s *Store) claimIdeaHint(id int64) (bool, error) {
	if s.writer != nil {
		return queueValue(s, []any{id}, func(d *Store, p []any) (bool, error) { return d.claimIdeaHint(p[0].(int64)) })
	}
	res, err := s.db.Exec(`UPDATE ideas SET hint_shown=1 WHERE id=? AND hint_shown=0`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
