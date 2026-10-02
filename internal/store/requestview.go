package store

import (
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
	work := []requestdomain.Work{}
	for _, w := range d.Work {
		if pub, ok := lookup(w.SessionID); ok {
			w.SessionID, w.SessionPublicID = 0, pub
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
	activity := make([]requestdomain.Activity, len(d.Activity))
	copy(activity, d.Activity)
	for i := range activity {
		// "session:<n> role:<r>" nennt die Nummer; die Rolle allein bleibt.
		if activity[i].Kind == "work.started" || activity[i].Kind == "work.resumed" {
			if rest, ok := strings.CutPrefix(activity[i].Data, "session:"); ok {
				_, role, _ := strings.Cut(rest, " ")
				activity[i].Data = role
			}
		}
	}
	d.Activity = activity
	return d
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
	return h
}
