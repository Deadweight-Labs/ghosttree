package store

import "strings"

// Objekte ohne Projekt und ohne Maschine ("global") gehören ihrem Autorenkonto.
// Der Autor eines Wissenseintrags ist die Konto-Id aus knowledge_authors; bei
// Altbestand ohne diese Zeile und bei Aufträgen entscheidet der Name, wie in
// machineAuthorDelivers (ohne Groß-/Kleinschreibung, frühere Namen füllen
// Lücken). Ein Name, den kein Konto trägt, und ein leerer Autor gehören dem
// Instanz-Owner; ein Name, der auf zwei Konten passt, niemandem.

func (a *ProjectAccess) loadOwners() {
	a.ownersOnce.Do(func() { a.ownerIDs, a.ownerDefault, a.authorIDs = a.st.machineDeliveryIndex() })
}

// authorByName löst ein Autor-Label auf eine Konto-Id auf, -1 bei Mehrdeutigkeit.
func (a *ProjectAccess) authorByName(person string) int64 {
	a.loadOwners()
	name := strings.ToLower(strings.TrimSpace(person))
	if name == "" {
		return a.ownerDefault
	}
	id, known := a.authorIDs[name]
	if !known {
		return a.ownerDefault
	}
	return id
}

// globalAuthor: das Konto, dem ein globales Objekt gehört.
func (a *ProjectAccess) globalAuthor(obj Object) int64 {
	if obj.EntryID != 0 {
		if id, ok := a.entryAuthors[obj.EntryID]; ok {
			if id != 0 {
				return id
			}
		} else {
			id := a.st.knowledgeAuthorID(obj.EntryID)
			if a.entryAuthors == nil {
				a.entryAuthors = map[int64]int64{}
			}
			a.entryAuthors[obj.EntryID] = id
			if id != 0 {
				return id
			}
		}
	}
	return a.authorByName(obj.Person)
}

// ownsGlobal: das globale Objekt gehört dem Konto dieser Anfrage.
func (a *ProjectAccess) ownsGlobal(obj Object) bool {
	if !a.valid {
		return false
	}
	author := a.globalAuthor(obj)
	return author > 0 && author == a.account
}

// ownGlobalPersons nennt die Autor-Labels (genau wie gespeichert), die dieses
// Konto als seine auflöst, für die Abfragen, die globale Einträge schon in SQL
// begrenzen.
func (a *ProjectAccess) ownGlobalPersons() []string {
	if !a.valid {
		return nil
	}
	var out []string
	for _, p := range a.st.globalPersons() {
		if id := a.authorByName(p); id > 0 && id == a.account {
			out = append(out, p)
		}
	}
	return out
}

// globalPersons: die verschiedenen Autor-Labels aller Objekte ohne Projekt.
func (s *Store) globalPersons() []string {
	if s.reader != nil {
		return s.reader.globalPersons()
	}
	rows, err := s.db.Query(`SELECT person FROM requests WHERE project=''
		UNION SELECT person FROM knowledge WHERE project=''`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			out = append(out, p)
		}
	}
	return out
}

// RestrictKnowledgeWindow begrenzt eine Wissensabfrage der Startseite auf das,
// was der Aufrufer lesen darf. Nur unter Durchsetzung; globale Einträge bleiben
// darin die eigenen.
func (a *ProjectAccess) RestrictKnowledgeWindow(w KnowledgeWindow) KnowledgeWindow {
	a.Filtered()
	if !a.st.AccessEnforced() {
		return w
	}
	w.Restrict, w.Projects = true, a.Projects()
	w.UnclaimedAuthor, w.UnclaimedAll = a.principal.Label, a.IsAdmin()
	w.GlobalAccount, w.GlobalPersons = a.account, a.ownGlobalPersons()
	return w
}
