package store

import (
	"database/sql"
	"strings"
)

// Autorschaft hängt an der Konto-Id, nie am Namen. Wissen (knowledge_authors)
// und Aufträge (request_authors) tragen je eine Zeile mit dem schreibenden
// Konto; account_id 0 heißt "ohne Besitzer" (ein Name, der beim Schreiben oder
// bei der Rückfüllung auf zwei Konten passte): nur der Instanz-Owner sieht so
// einen Eintrag. Ein Konto, das sich in "Claude" oder "admin" umbenennt,
// übernimmt damit nichts. Den Namen löst allein der Schreibvorgang auf (aus dem
// Token des Schreibenden) und die einmalige Rückfüllung für Altbestand.

const authorsBackfillKey = "authors_backfilled"

// nameIndex: Kontoname in Kleinschreibung -> Konto-Id, -1 bei Mehrdeutigkeit. Der
// heutige Name eines Kontos gilt zuerst, frühere Namen (person_name_history)
// füllen nur Lücken.
func nameIndex(tx *sql.Tx) map[string]int64 {
	load := func(query string) map[string]int64 {
		into := map[string]int64{}
		rows, err := tx.Query(query)
		if err != nil {
			return into
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var name string
			if rows.Scan(&id, &name) != nil {
				continue
			}
			key := strings.ToLower(strings.TrimSpace(name))
			if prev, ok := into[key]; ok && prev != id {
				into[key] = -1
			} else {
				into[key] = id
			}
		}
		return into
	}
	names := load(`SELECT id, name FROM persons`)
	for key, id := range load(`SELECT person_id, old_name FROM person_name_history`) {
		if _, ok := names[key]; !ok {
			names[key] = id
		}
	}
	return names
}

// resolveAuthorTx bestimmt das Konto, dem ein Eintrag gehört, der keines
// mitbringt: bei einem Destillat ohne Autor der Besitzer der Herkunfts-Session,
// sonst das Konto, das den Namen trägt (mehrdeutig: 0, ohne Besitzer); ein leerer
// oder unbekannter Name (das Label "claude" aus der Zeit vor Konten) gehört dem
// Instanz-Owner.
func resolveAuthorTx(tx *sql.Tx, names map[string]int64, owner int64, person, origin, sessionRef string) int64 {
	name := strings.ToLower(strings.TrimSpace(person))
	if origin == "distilled" && name == "" {
		if m := originSessionPattern.FindStringSubmatch(sessionRef); m != nil {
			var account int64
			if tx.QueryRow(`SELECT account_id FROM sessions WHERE id=?`, m[1]).Scan(&account) == nil {
				return effectiveOwner(account, owner)
			}
		}
	}
	if name == "" {
		return owner
	}
	switch id, known := names[name]; {
	case !known:
		return owner
	case id == -1:
		return 0
	default:
		return id
	}
}

// authorForWriteTx ist resolveAuthorTx für einen einzelnen Schreibvorgang.
func authorForWriteTx(tx *sql.Tx, given int64, person, origin, sessionRef string) int64 {
	if given != 0 {
		return given
	}
	return resolveAuthorTx(tx, nameIndex(tx), instanceOwnerID(tx), person, origin, sessionRef)
}

func recordRequestAuthorTx(tx txExec, requestID, accountID int64) error {
	_, err := tx.Exec(`INSERT OR REPLACE INTO request_authors(request_id, account_id) VALUES(?,?)`, requestID, accountID)
	return err
}

// requestAuthorID: das Konto, dem der Auftrag gehört; 0 ohne Zeile oder ohne
// Besitzer.
func (s *Store) requestAuthorID(id int64) int64 {
	if id == 0 {
		return 0
	}
	if s.reader != nil {
		return s.reader.requestAuthorID(id)
	}
	var acct int64
	_ = s.db.QueryRow(`SELECT account_id FROM request_authors WHERE request_id=?`, id).Scan(&acct)
	return acct
}

// instanceOwner: der Besitzer von Altbestand und von Einträgen ohne Besitzer.
func (s *Store) instanceOwner() int64 {
	if s.reader != nil {
		return s.reader.instanceOwner()
	}
	return instanceOwnerID(s.db)
}

// backfillAuthors trägt einmalig die Konto-Id für Einträge nach, die keine Zeile
// haben (Altbestand vor knowledge_authors und request_authors). Danach gilt nur
// noch die Id. Die Marke macht den Lauf einmalig, jeder Eintrag mit Zeile bleibt
// unberührt, ein zweiter Lauf täte also ohnehin nichts.
func backfillAuthors(db *sql.DB) error {
	var done string
	if err := db.QueryRow(`SELECT value FROM org_state WHERE key=?`, authorsBackfillKey).Scan(&done); err == nil {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	names, owner := nameIndex(tx), instanceOwnerID(tx)
	type entry struct {
		id                         int64
		person, origin, sessionRef string
	}
	collect := func(query string) ([]entry, error) {
		rows, err := tx.Query(query)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []entry
		for rows.Next() {
			var e entry
			if err := rows.Scan(&e.id, &e.person, &e.origin, &e.sessionRef); err != nil {
				return nil, err
			}
			out = append(out, e)
		}
		return out, rows.Err()
	}
	knowledge, err := collect(`SELECT id, person, origin, session_ref FROM knowledge WHERE id NOT IN (SELECT knowledge_id FROM knowledge_authors)`)
	if err != nil {
		return err
	}
	for _, e := range knowledge {
		if err := recordAuthorTx(tx, e.id, resolveAuthorTx(tx, names, owner, e.person, e.origin, e.sessionRef)); err != nil {
			return err
		}
	}
	requests, err := collect(`SELECT id, person, origin, session_ref FROM requests WHERE id NOT IN (SELECT request_id FROM request_authors)`)
	if err != nil {
		return err
	}
	for _, e := range requests {
		if err := recordRequestAuthorTx(tx, e.id, resolveAuthorTx(tx, names, owner, e.person, e.origin, e.sessionRef)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO org_state(key, value) VALUES(?, '1')`, authorsBackfillKey); err != nil {
		return err
	}
	return tx.Commit()
}
