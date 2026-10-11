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
	// Mitglieder kennen die Nummern der Sessions, sehen aber, was an einer
	// privaten Session eines anderen Kontos hängt (Zitat, Übergabe, Beleg,
	// Aktivität), ebenso wenig wie ihr Transkript.
	numbers := a.SeesSessionNumbers(project)
	readable := map[int64]string{}
	known := map[int64]bool{}
	lookup := func(id int64) (string, bool) {
		if pub, ok := readable[id]; ok {
			return pub, known[id]
		}
		pub, ok := "", false
		if sess, err := a.st.SessionByID(id); err == nil {
			if a.CanSeeTranscript(sess) && (sess.PublicID != "" || numbers) {
				pub, ok = sess.PublicID, true
			}
		} else {
			// Eine Session, die es nicht mehr gibt, hält nichts zurück.
			ok = numbers
		}
		readable[id], known[id] = pub, ok
		return pub, ok
	}
	if numbers {
		d.Request.SessionRef = a.visibleSessionRef(d.Request.SessionRef, lookup)
	} else {
		d.Request.SessionRef = ""
	}
	// Wer an dem Auftrag arbeiten darf (decideGlobal: jedes Konto an globalen
	// Aufträgen), braucht die echten Arbeits-Ids, um Arbeit abzuschließen; neu
	// vergebene träfen fremde Zeilen. Gefiltert wird auch dann, nur diese Ids
	// bleiben (Aktivität und Beleg werden immer neu nummeriert).
	keepIDs := numbers || a.Decide(project, ResRequest, ActWork, Object{Own: a.IsAuthor(d.Request.Person)}).Allowed
	// Alles, was an einer Session hängt, bleibt nur, wenn sie lesbar ist, und
	// wird sonst ganz weggelassen: ein Platzhalter ließe sich mitzählen. Die
	// laufenden Nummern von Arbeit, Beleg und Aktivität werden neu vergeben,
	// denn Lücken darin zählten die verborgenen Einträge mit.
	work := []requestdomain.Work{}
	for _, w := range d.Work {
		if pub, ok := lookup(w.SessionID); ok {
			w.SessionPublicID = pub
			if !numbers {
				w.SessionID = 0
			}
			if !keepIDs {
				w.ID = int64(len(work) + 1)
			}
			work = append(work, w)
		}
	}
	d.Work = work
	if d.Sightings != nil {
		sightings := []requestdomain.Sighting{}
		for _, s := range d.Sightings {
			if pub, ok := lookup(s.SessionID); ok {
				s.SessionPublicID = pub
				if !numbers {
					s.SessionID = 0
				}
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
			if !ok || (!numbers && e.Kind == "session" && !sessionRefPattern.MatchString(e.Ref)) {
				continue
			}
			if !numbers {
				// Keine Route nimmt eine Beleg-Id an: immer neu vergeben.
				evidenceNo++
				e.ID = evidenceNo
				e.Ref = ref
			}
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
			if sid != 0 || !numbers {
				if _, ok := lookup(sid); !ok {
					continue
				}
			}
			if !numbers {
				// "session:<n> role:<r>" nennt die Nummer; die Rolle allein bleibt.
				if rest, ok := strings.CutPrefix(act.Data, "session:"); ok && (act.Kind == "work.started" || act.Kind == "work.resumed") {
					_, role, _ := strings.Cut(rest, " ")
					act.Data = role
				}
			}
		case "request.done", "request.migrated":
			ref, ok := rewriteSessionRef(act.Data, lookup)
			if !ok {
				continue
			}
			if !numbers {
				act.Data = ref
			}
		}
		if !numbers {
			// Auch eine Aktivitäts-Id braucht kein Schreibzugriff: immer neu vergeben.
			act.ID = int64(len(activity) + 1)
			act.SessionID = 0
		}
		activity = append(activity, act)
	}
	d.Activity = activity
	return d
}

// visibleSessionRef lässt einen Verweis auf eine unlesbare Session weg.
func (a *ProjectAccess) visibleSessionRef(ref string, lookup func(int64) (string, bool)) string {
	if _, ok := rewriteSessionRef(ref, lookup); !ok {
		return ""
	}
	return ref
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
	numbers := a.SeesSessionNumbers(h.Request.Scope.Project)
	if !numbers {
		h.Request.SessionRef = ""
	} else if _, ok := rewriteSessionRef(h.Request.SessionRef, func(id int64) (string, bool) {
		sess, err := a.st.SessionByID(id)
		return "", err != nil || a.CanSeeTranscript(sess)
	}); !ok {
		h.Request.SessionRef = ""
	}
	// Die Zahl zählt nur Sessions mit lesbarem Transkript.
	if h.Sightings > 0 {
		h.Sightings = a.readableSightings(h.Request.ID)
	}
	// Die Übergabe stammt aus der jüngsten Arbeit mit lesbarer Session; die
	// jüngste überhaupt könnte aus einer verborgenen kommen.
	h.LatestHandoff = ""
	if sids, sums, err := a.st.RequestHandoffs(h.Request.ID); err == nil {
		for i, sid := range sids { // jüngste zuerst
			if sess, err := a.st.SessionByID(sid); err == nil && a.CanSeeTranscript(sess) {
				h.LatestHandoff = sums[i]
				break
			}
		}
	}
	return h
}

// readableSightings zählt die Sessions mit lesbarem Transkript, in denen der
// Wunsch fiel.
func (a *ProjectAccess) readableSightings(requestID int64) int {
	ids, err := a.st.RequestSightingSessions(requestID)
	if err != nil {
		return 0
	}
	n := 0
	for _, id := range ids {
		if sess, err := a.st.SessionByID(id); err == nil && a.CanSeeTranscript(sess) {
			n++
		}
	}
	return n
}

// ensureActivitySession ergänzt request_activity um die Session, auf die sich
// ein Eintrag bezieht, und trägt sie für Altbestand nach, soweit sie sich
// eindeutig ableiten lässt. Was sich nicht zuordnen lässt, bekommt -1 (keine
// Session lesbar, wird nicht bei jedem Start neu versucht) und zeigt kein
// eingeschränkter Betrachter.
func ensureActivitySession(db *sql.DB) error {
	if err := ensureColumn(db, "request_activity", "session_id", `INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	_, err := db.Exec(`UPDATE request_activity SET session_id=CAST(substr(data,9) AS INTEGER)
		WHERE session_id=0 AND kind IN ('work.started','work.resumed') AND data LIKE 'session:%';
		UPDATE request_activity SET session_id=(SELECT CASE WHEN count(*)=1 THEN min(w.session_id) ELSE -1 END FROM request_work w
			WHERE w.request_id=request_activity.request_id AND w.ended_at=request_activity.created_at AND w.summary=request_activity.data)
		WHERE session_id=0 AND kind='work.finished'`)
	return err
}

// RequestHandoffs liefert Session und Übergabe jeder Arbeit mit Zusammenfassung,
// jüngste zuerst, ohne den ganzen Auftrag zu laden.
func (s *Store) RequestHandoffs(requestID int64) ([]int64, []string, error) {
	if s.reader != nil {
		return s.reader.RequestHandoffs(requestID)
	}
	rows, err := s.db.Query(`SELECT session_id, summary FROM request_work WHERE request_id=? AND summary!='' ORDER BY id DESC`, requestID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var sids []int64
	var sums []string
	for rows.Next() {
		var sid int64
		var sum string
		if err := rows.Scan(&sid, &sum); err != nil {
			return nil, nil, err
		}
		sids, sums = append(sids, sid), append(sums, sum)
	}
	return sids, sums, rows.Err()
}

// RequestSightingSessions nennt die Sessions, in denen ein Wunsch fiel.
func (s *Store) RequestSightingSessions(requestID int64) ([]int64, error) {
	if s.reader != nil {
		return s.reader.RequestSightingSessions(requestID)
	}
	rows, err := s.db.Query(`SELECT DISTINCT session_id FROM request_sightings WHERE request_id=?`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
