package store

import (
	"database/sql"
	"regexp"
	"strconv"
	"strings"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// RequestDetailView formt ein Request-Detail für den Betrachter. Wer die
// laufenden Session-Nummern des Projekts nicht kennen darf (#2447), bekommt
// statt der Nummer die Adresse, und nur für Sessions mit lesbarem Transkript;
// alles andere (Rolle, Stand, Zusammenfassung, Zitat) bleibt weg, denn auch das
// verriete, dass es die Session gibt.
func (a *ProjectAccess) RequestDetailView(d requestdomain.Detail) requestdomain.Detail {
	project := d.Request.Scope.Project
	if a.SeesSessionNumbers(project) {
		work := make([]requestdomain.Work, len(d.Work))
		copy(work, d.Work)
		for i := range work {
			if sess, err := a.st.SessionByID(work[i].SessionID); err == nil && a.CanSeeTranscript(sess) {
				work[i].SessionPublicID = sess.PublicID
			}
		}
		d.Work = work
		return d
	}
	readable := map[int64]string{}
	lookup := func(id int64) (string, bool) {
		if pub, ok := readable[id]; ok {
			return pub, pub != ""
		}
		pub := ""
		if sess, err := a.st.SessionByID(id); err == nil && sess.PublicID != "" && a.CanSeeTranscript(sess) {
			pub = sess.PublicID
		}
		readable[id] = pub
		return pub, pub != ""
	}
	d.Request.SessionRef = ""
	// Alles, was an einer Session hängt, bleibt nur, wenn sie lesbar ist, und
	// wird sonst ganz weggelassen: ein Platzhalter ließe sich mitzählen. Die
	// laufenden Nummern von Arbeit, Beleg und Aktivität werden neu vergeben,
	// denn Lücken darin zählten die verborgenen Einträge mit.
	work := []requestdomain.Work{}
	for _, w := range d.Work {
		if pub, ok := lookup(w.SessionID); ok {
			w.ID, w.SessionID, w.SessionPublicID = int64(len(work)+1), 0, pub
			work = append(work, w)
		}
	}
	d.Work = work
	if d.Sightings != nil {
		sightings := []requestdomain.Sighting{}
		for _, s := range d.Sightings {
			if pub, ok := lookup(s.SessionID); ok {
				s.SessionID, s.SessionPublicID = 0, pub
				sightings = append(sightings, s)
			}
		}
		d.Sightings = sightings
	}
	criteria := make([]requestdomain.Criterion, len(d.Criteria))
	copy(criteria, d.Criteria)
	evidenceNo := int64(0)
	for i := range criteria {
		var kept []requestdomain.Evidence
		for _, e := range criteria[i].Evidence {
			ref, ok := rewriteSessionRef(e.Ref, lookup)
			if !ok {
				continue
			}
			evidenceNo++
			e.ID, e.Ref = evidenceNo, ref
			kept = append(kept, e)
		}
		criteria[i].Evidence = kept
	}
	d.Criteria = criteria
	activity := []requestdomain.Activity{}
	for _, act := range d.Activity {
		switch act.Kind {
		case "work.started", "work.resumed", "work.finished", "evidence.migrated":
			sid := act.SessionID
			if sid == 0 {
				if rest, ok := strings.CutPrefix(act.Data, "session:"); ok && act.Kind != "work.finished" && act.Kind != "evidence.migrated" {
					num, _, _ := strings.Cut(rest, " ")
					sid, _ = strconv.ParseInt(num, 10, 64)
				}
			}
			if _, ok := lookup(sid); !ok {
				continue
			}
			// "session:<n> role:<r>" nennt die Nummer; die Rolle allein bleibt.
			if rest, ok := strings.CutPrefix(act.Data, "session:"); ok && (act.Kind == "work.started" || act.Kind == "work.resumed") {
				_, role, _ := strings.Cut(rest, " ")
				act.Data = role
			}
		case "request.done", "request.migrated":
			ref, ok := rewriteSessionRef(act.Data, lookup)
			if !ok {
				continue
			}
			act.Data = ref
		}
		act.ID, act.SessionID = int64(len(activity)+1), 0
		activity = append(activity, act)
	}
	d.Activity = activity
	return d
}

var sessionRefPattern = regexp.MustCompile(`^session:(\d+)(#.*)?$`)

// rewriteSessionRef schreibt einen Verweis "session:<n>#..." auf die Adresse
// der Session um; ok ist false, wenn sie für den Betrachter nicht lesbar ist.
// Alles andere ist kein Session-Verweis und bleibt wie es ist.
func rewriteSessionRef(ref string, lookup func(int64) (string, bool)) (string, bool) {
	m := sessionRefPattern.FindStringSubmatch(ref)
	if m == nil {
		return ref, true
	}
	id, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return "", false
	}
	pub, ok := lookup(id)
	if !ok {
		return "", false
	}
	return "session:" + pub + m[2], true
}

// RequestHitView entfernt aus einem Treffer, was Sessions des Betrachters
// verrät: den Verweis des Auftrags und die Zahl der Sessions, in denen der
// Wunsch fiel (sie zählt auch unlesbare).
func (a *ProjectAccess) RequestHitView(h requestdomain.SearchHit) requestdomain.SearchHit {
	if a.SeesSessionNumbers(h.Request.Scope.Project) {
		return h
	}
	h.Request.SessionRef = ""
	h.Sightings = 0
	// Die Übergabe stammt aus der jüngsten Arbeit mit lesbarer Session; die
	// jüngste überhaupt könnte aus einer verborgenen kommen.
	h.LatestHandoff = ""
	if d, err := a.st.RequestByID(h.Request.ID); err == nil {
		for _, w := range d.Work { // jüngste zuerst
			if w.Summary == "" {
				continue
			}
			if sess, err := a.st.SessionByID(w.SessionID); err == nil && a.CanSeeTranscript(sess) {
				h.LatestHandoff = w.Summary
				break
			}
		}
	}
	return h
}

// ensureActivitySession ergänzt request_activity um die Session, auf die sich
// ein Eintrag bezieht, und trägt sie für Altbestand nach, soweit sie sich
// ableiten lässt. Was offen bleibt, zeigt kein eingeschränkter Betrachter.
func ensureActivitySession(db *sql.DB) error {
	if err := ensureColumn(db, "request_activity", "session_id", `INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	_, err := db.Exec(`UPDATE request_activity SET session_id=CAST(substr(data,9) AS INTEGER)
		WHERE session_id=0 AND kind IN ('work.started','work.resumed') AND data LIKE 'session:%';
		UPDATE request_activity SET session_id=COALESCE((SELECT w.session_id FROM request_work w
			WHERE w.request_id=request_activity.request_id AND w.ended_at=request_activity.created_at AND w.summary=request_activity.data LIMIT 1),0)
		WHERE session_id=0 AND kind='work.finished'`)
	return err
}
