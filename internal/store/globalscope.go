package store

import "encoding/json"

// Objekte ohne Projekt und ohne Maschine ("global") gehören ihrem Autorenkonto.
// Der Autor ist die Konto-Id aus knowledge_authors und request_authors, gesetzt
// beim Schreiben oder einmalig bei der Rückfüllung (authors.go). Zur Laufzeit
// wird kein Name aufgelöst: wer sich umbenennt, übernimmt nichts. Ein Eintrag
// ohne Besitzer (Id 0 oder keine Zeile) gehört dem Instanz-Owner.

func (a *ProjectAccess) loadOwners() {
	a.ownersOnce.Do(func() { a.ownerIDs, a.ownerDefault = a.st.machineDeliveryIndex() })
}

// entryAuthor: das Konto, dem ein Wissenseintrag gehört (0 ohne Besitzer).
func (a *ProjectAccess) entryAuthor(id int64) int64 {
	if id == 0 {
		return 0
	}
	if v, ok := a.entryAuthors[id]; ok {
		return v
	}
	v := a.st.knowledgeAuthorID(id)
	if a.entryAuthors == nil {
		a.entryAuthors = map[int64]int64{}
	}
	a.entryAuthors[id] = v
	return v
}

// WarmKnowledgeAuthors liest die Autoren der Einträge in einer Abfrage vor. Die
// Rechteprüfung fragt sie sonst je Eintrag, und ein Bootstrap prüft einige
// tausend (PAIN-76). Was die Abfrage nicht findet, gilt als ohne Besitzer, wie
// bei der Einzelabfrage.
func (a *ProjectAccess) WarmKnowledgeAuthors(ks []Knowledge) {
	if len(ks) == 0 {
		return
	}
	ids := make([]int64, 0, len(ks))
	for _, k := range ks {
		if _, known := a.entryAuthors[k.ID]; !known && k.ID != 0 {
			ids = append(ids, k.ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	list, err := json.Marshal(ids)
	if err != nil {
		return
	}
	rows, err := a.st.authorRows(string(list))
	if err != nil {
		return
	}
	defer rows.Close()
	if a.entryAuthors == nil {
		a.entryAuthors = make(map[int64]int64, len(ids))
	}
	for _, id := range ids {
		a.entryAuthors[id] = 0
	}
	for rows.Next() {
		var id, account int64
		if rows.Scan(&id, &account) == nil {
			a.entryAuthors[id] = account
		}
	}
}

// requestAuthor: das Konto, dem ein Auftrag gehört (0 ohne Besitzer).
func (a *ProjectAccess) requestAuthor(id int64) int64 {
	if id == 0 {
		return 0
	}
	if v, ok := a.requestAuthors[id]; ok {
		return v
	}
	v := a.st.requestAuthorID(id)
	if a.requestAuthors == nil {
		a.requestAuthors = map[int64]int64{}
	}
	a.requestAuthors[id] = v
	return v
}

// IsInstanceOwner: das Konto ist der Besitzer von Altbestand.
func (a *ProjectAccess) IsInstanceOwner() bool {
	if !a.valid {
		return false
	}
	a.loadOwners()
	return a.ownerDefault != 0 && a.ownerDefault == a.account
}

// ownsGlobal: das globale Objekt gehört dem Konto dieser Anfrage.
func (a *ProjectAccess) ownsGlobal(obj Object) bool {
	if !a.valid {
		return false
	}
	var author int64
	switch {
	case obj.EntryID != 0:
		author = a.entryAuthor(obj.EntryID)
	case obj.RequestID != 0:
		author = a.requestAuthor(obj.RequestID)
	}
	if author > 0 {
		return author == a.account
	}
	return a.IsInstanceOwner()
}

// ownsKnowledge: der Eintrag wurde von diesem Konto geschrieben.
func (a *ProjectAccess) ownsKnowledge(k Knowledge) bool {
	if !a.valid || k.ID == 0 {
		return false
	}
	author := a.entryAuthor(k.ID)
	return author > 0 && author == a.account
}

// ownsRequest: der Auftrag wurde von diesem Konto geschrieben.
// OwnsRequest: der Auftrag wurde von diesem Konto geschrieben.
func (a *ProjectAccess) OwnsRequest(id int64) bool { return a.ownsRequest(id) }

func (a *ProjectAccess) ownsRequest(id int64) bool {
	if !a.valid || id == 0 {
		return false
	}
	author := a.requestAuthor(id)
	return author > 0 && author == a.account
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
	w.GlobalAccount, w.GlobalOwner = a.account, a.IsInstanceOwner()
	return w
}

// ownAuthorSQL: Bedingung auf die Spalte id (mit alias), die Einträge des
// Kontos aus der Autor-Tabelle trifft; der Instanz-Owner bekommt zusätzlich die
// ohne Besitzer (Id 0 oder keine Zeile).
func ownAuthorSQL(alias, table, idCol string, account int64, owner bool) (string, []any) {
	id := "id"
	if alias != "" {
		id = alias + ".id"
	}
	own := id + ` IN (SELECT ` + idCol + ` FROM ` + table + ` WHERE account_id=?)`
	if owner {
		own += ` OR ` + id + ` NOT IN (SELECT ` + idCol + ` FROM ` + table + `) OR ` + id + ` IN (SELECT ` + idCol + ` FROM ` + table + ` WHERE account_id=0)`
	}
	return `(` + own + `)`, []any{account}
}

func ownKnowledgeSQL(alias string, account int64, owner bool) (string, []any) {
	return ownAuthorSQL(alias, "knowledge_authors", "knowledge_id", account, owner)
}

func ownRequestSQL(alias string, account int64, owner bool) (string, []any) {
	return ownAuthorSQL(alias, "request_authors", "request_id", account, owner)
}

// globalKnowledgeSQL: Bedingung für Abfragen über Wissen, die Einträge ohne
// Projekt und ohne Maschine anderer Autoren ausschließt (alias der Tabelle
// knowledge oder leer).
func globalKnowledgeSQL(alias string, account int64, owner bool) (string, []any) {
	col := func(c string) string {
		if alias == "" {
			return c
		}
		return alias + "." + c
	}
	own, args := ownKnowledgeSQL(alias, account, owner)
	return `(` + col("project") + `!='' OR ` + col("machine") + `!='' OR ` + own + `)`, args
}

// isInstanceOwner: der Betrachter ist der Besitzer von Altbestand.
func (v SessionViewer) isInstanceOwner() bool { return v.Owner != 0 && v.Account == v.Owner }

// withGlobalScope hängt an eine Wissensabfrage die Bedingung, die globale
// Einträge anderer Autoren ausschließt, wenn der Betrachter unter Durchsetzung
// fragt. Ohne Betrachter oder ohne Durchsetzung bleibt die Abfrage, wie sie ist.
// So verdrängt ein fremder globaler Treffer weder einen sichtbaren aus dem Limit
// noch geht er in die Nutzungszähler ein.
func withGlobalScope(where string, args []any, alias string, viewer []SessionViewer) (string, []any) {
	if len(viewer) == 0 || !viewer[0].Enforced {
		return where, args
	}
	cond, cargs := globalKnowledgeSQL(alias, viewer[0].Account, viewer[0].isInstanceOwner())
	return `(` + where + `) AND ` + cond, append(args, cargs...)
}
