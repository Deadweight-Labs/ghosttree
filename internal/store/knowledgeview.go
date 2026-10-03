package store

import "strconv"

// KnowledgeView formt einen Wissenseintrag für den Betrachter. Wer die laufenden
// Session-Nummern des Projekts nicht kennen darf (#2447, #2482), bekommt den
// Verweis "session:<n>#<seq>" aus der Destillation nur als Adresse der Session
// und nur, wenn er ihr Transkript lesen darf; sonst fehlt der Verweis, denn auch
// seine Anwesenheit verriete eine verborgene Session.
func (a *ProjectAccess) KnowledgeView(k Knowledge) Knowledge {
	if a.seesKnowledgeNumbers(k) {
		return k
	}
	if ref, ok := rewriteSessionRef(k.SessionRef, a.readableSessionLookup()); ok {
		k.SessionRef = ref
	} else {
		k.SessionRef = ""
	}
	return k
}

// seesKnowledgeNumbers: globales Wissen hat kein Projekt, das über die Nummer
// entschiede; dann zählt das Projekt der Session, auf die der Eintrag verweist.
// Eine Session, die es nicht gibt, schaltet nichts frei.
func (a *ProjectAccess) seesKnowledgeNumbers(k Knowledge) bool {
	if k.Scope.Project != "" {
		return a.SeesSessionNumbers(k.Scope.Project)
	}
	if !a.st.AccessEnforced() || a.IsAdmin() {
		return true
	}
	m := sessionRefPattern.FindStringSubmatch(k.SessionRef)
	if m == nil {
		return false
	}
	id, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return false
	}
	sess, err := a.st.SessionByID(id)
	return err == nil && a.SeesSessionNumbers(sess.Scope.Project)
}

// DropWrittenSessionNumber verwirft beim Anlegen von Wissen eine numerische
// Angabe session:<n>, wenn der Schreibende die Nummern des Projekts nicht
// kennen darf. Die Antwort enthielte sie sonst umgeschrieben oder gar nicht,
// je nachdem, ob es die Session gibt und ob er sie lesen darf (#2447, #2482).
func (a *ProjectAccess) DropWrittenSessionNumber(k *Knowledge) {
	if a.st.AccessEnforced() && !a.IsAdmin() && !a.SeesSessionNumbers(k.Scope.Project) && sessionRefPattern.MatchString(k.SessionRef) {
		k.SessionRef = ""
	}
}

// KnowledgeViews wendet KnowledgeView auf eine Liste an.
func (a *ProjectAccess) KnowledgeViews(ks []Knowledge) []Knowledge {
	out := make([]Knowledge, len(ks))
	for i, k := range ks {
		out[i] = a.KnowledgeView(k)
	}
	return out
}

// ReadableEvidence behält von den Belegen eines Eintrags die, deren Session der
// Betrachter lesen darf, und zählt deren Sessions. Mitglieder sehen alles; für
// alle anderen bliebe sonst das Zitat aus einem verborgenen Transkript stehen,
// und die Zahl der Sessions zählte die verborgenen mit.
func (a *ProjectAccess) ReadableEvidence(project string, ev []Evidence, sessions int) ([]Evidence, int) {
	if a.SeesSessionNumbers(project) {
		return ev, sessions
	}
	lookup := a.readableSessionLookup()
	kept := []Evidence{}
	seen := map[int64]bool{}
	for _, e := range ev {
		if _, ok := lookup(e.SessionID); ok {
			kept = append(kept, e)
			seen[e.SessionID] = true
		}
	}
	return kept, len(seen)
}

// EvidenceView ist ReadableEvidence für die API: statt der Nummer steht die
// Adresse der Session.
func (a *ProjectAccess) EvidenceView(project string, ev []Evidence, sessions int) ([]Evidence, int) {
	if a.SeesSessionNumbers(project) {
		return ev, sessions
	}
	lookup := a.readableSessionLookup()
	kept, n := a.ReadableEvidence(project, ev, sessions)
	for i := range kept {
		pub, _ := lookup(kept[i].SessionID)
		kept[i].SessionID, kept[i].SessionPublicID = 0, pub
	}
	return kept, n
}

// MigrationEvidenceView lässt den Herkunftsnachweis einer Migration weg, wenn
// seine Quelle eine Session ist, die der Betrachter nicht lesen darf.
func (a *ProjectAccess) MigrationEvidenceView(project string, m *MigrationEvidence) *MigrationEvidence {
	if m == nil || a.SeesSessionNumbers(project) {
		return m
	}
	ref, ok := rewriteSessionRef(m.Source, a.readableSessionLookup())
	if !ok {
		return nil
	}
	c := *m
	c.Source = ref
	return &c
}

func (a *ProjectAccess) readableSessionLookup() func(int64) (string, bool) {
	cache := map[int64]string{}
	return func(id int64) (string, bool) {
		pub, done := cache[id]
		if !done {
			if sess, err := a.st.SessionByID(id); err == nil && sess.PublicID != "" && a.CanSeeTranscript(sess) {
				pub = sess.PublicID
			}
			cache[id] = pub
		}
		return pub, pub != ""
	}
}
