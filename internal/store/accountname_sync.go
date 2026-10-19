package store

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Herkunft eines Anzeigenamens (persons.name_source).
const (
	nameSourceIdP  = "idp"
	nameSourceUser = "user"
)

// isFallbackAccountName erkennt die Platzhalter, die ein Konto ohne Namen vom
// Anbieter bekam ("admin", "user", "user-2"). Nur solche und vom Anbieter
// stammende Namen folgen später dem Anbieter; ein Name, den ein Admin gewählt
// hat, bleibt stehen.
func isFallbackAccountName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || name == "admin" || name == "user" {
		return true
	}
	if rest, ok := strings.CutPrefix(name, "user-"); ok {
		_, err := strconv.Atoi(rest)
		return err == nil
	}
	return false
}

// syncIdPNameTx übernimmt den Namen, den der Anbieter beim Login meldet, wenn
// er sich vom gespeicherten unterscheidet und die Person ihn nicht selbst
// gesetzt hat. Gibt den neuen Namen zurück, sonst "".
func syncIdPNameTx(tx *sql.Tx, id int64, current, offered string) (string, error) {
	want := NormalizeAccountName(offered)
	if want == "" || want == current || MixedScriptName(want) {
		return "", nil
	}
	var source string
	if err := tx.QueryRow(`SELECT name_source FROM persons WHERE id=?`, id).Scan(&source); err != nil {
		return "", err
	}
	if !(source == nameSourceIdP || (source == "" && isFallbackAccountName(current))) {
		return "", nil
	}
	name, err := inviteName(tx, want, id)
	if err != nil {
		return "", err
	}
	if name == current {
		return "", nil
	}
	if err := recordNameChangeTx(tx, id, current, name, nameSourceIdP); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`UPDATE persons SET name=?, name_source=? WHERE id=?`, name, nameSourceIdP, id); err != nil {
		return "", err
	}
	return name, nil
}

// Grenzen für selbst gewählte Namen: höchstens maxNameChanges je Fenster.
const (
	maxNameChanges   = 3
	nameChangeWindow = 24 * time.Hour
)

// recordNameChangeTx schreibt jeden Namenswechsel in das Audit-Protokoll
// person_name_history (nur anhängen). Der alte Name bleibt für andere Konten
// gesperrt; Platzhalter ("user", "admin") zeichnen niemanden aus und
// reservieren nichts.
func recordNameChangeTx(tx *sql.Tx, id int64, old, new, source string) error {
	key := ""
	if !isFallbackAccountName(old) {
		key = accountNameKey(old)
	}
	_, err := tx.Exec(`INSERT INTO person_name_history(person_id, old_name, old_key, new_name, source, created_at) VALUES(?,?,?,?,?,?)`,
		id, old, key, new, source, now())
	return err
}

// accountIsGuestOnlyTx: das Konto hat eine Gast-Rolle und nirgends mehr, ist
// weder Instanz-Admin noch Org-Owner.
func accountIsGuestOnlyTx(tx *sql.Tx, id int64) (bool, error) {
	var guest bool
	err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM project_members WHERE account_id=? AND role='guest')
		AND NOT EXISTS(SELECT 1 FROM project_members WHERE account_id=? AND role<>'guest')
		AND NOT EXISTS(SELECT 1 FROM org_members WHERE account_id=? AND role='owner')
		AND NOT EXISTS(SELECT 1 FROM persons WHERE id=? AND is_admin=1)`, id, id, id, id).Scan(&guest)
	return guest, err
}

// SetOwnName ändert den Anzeigenamen eines Kontos auf Wunsch der Person. Der
// Name bleibt danach, auch wenn der Anbieter einen anderen meldet. Er wird
// wie jeder Kontoname bereinigt und muss mindestens einen Buchstaben oder eine
// Ziffer enthalten, höchstens MaxDisplayNameRunes Zeichen lang sein und frei
// sein (ohne Groß-/Kleinschreibung und ähnliche Formen).
func (s *Store) SetOwnName(accountID, name string) (Account, error) {
	if s.writer != nil {
		return queueValue(s, []any{accountID, name}, func(d *Store, p []any) (Account, error) {
			return d.SetOwnName(p[0].(string), p[1].(string))
		})
	}
	id, ok := accountNumericID(accountID)
	if !ok {
		return Account{}, errAccountNotFound
	}
	if len([]rune(strings.TrimSpace(name))) > MaxDisplayNameRunes {
		return Account{}, fmt.Errorf("%w: a name has at most %d characters", ErrInvalidInput, MaxDisplayNameRunes)
	}
	name = NormalizeAccountName(name)
	if !strings.ContainsFunc(name, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
		return Account{}, fmt.Errorf("%w: a name needs a letter or digit", ErrInvalidInput)
	}
	if MixedScriptName(name) {
		return Account{}, fmt.Errorf("%w: a name uses one script (no mixing of, say, Latin and Cyrillic letters)", ErrInvalidInput)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback()
	cur, err := accountState(tx, id)
	if err != nil {
		return Account{}, err
	}
	if guest, err := accountIsGuestOnlyTx(tx, id); err != nil {
		return Account{}, err
	} else if guest {
		return Account{}, ErrNameNotAllowed
	}
	if taken, err := accountNameTakenByOtherTx(tx, name, id); err != nil {
		return Account{}, err
	} else if taken {
		return Account{}, ErrAccountNameTaken
	}
	if name != cur.Name {
		var recent int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM person_name_history WHERE person_id=? AND source=? AND created_at>?`,
			id, nameSourceUser, time.Now().UTC().Add(-nameChangeWindow).Format(time.RFC3339)).Scan(&recent); err != nil {
			return Account{}, err
		}
		if recent >= maxNameChanges {
			return Account{}, ErrNameRateLimited
		}
		if err := recordNameChangeTx(tx, id, cur.Name, name, nameSourceUser); err != nil {
			return Account{}, err
		}
	}
	if _, err := tx.Exec(`UPDATE persons SET name=?, name_source=? WHERE id=?`, name, nameSourceUser, id); err != nil {
		return Account{}, err
	}
	a, err := accountState(tx, id)
	if err != nil {
		return Account{}, err
	}
	return a, tx.Commit()
}

// NameLocked sagt, ob das Konto seinen Namen nicht selbst ändern darf (Gast).
func (s *Store) NameLocked(accountID string) bool {
	if s.reader != nil {
		return s.reader.NameLocked(accountID)
	}
	id, ok := accountNumericID(accountID)
	if !ok {
		return false
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false
	}
	defer tx.Rollback()
	locked, err := accountIsGuestOnlyTx(tx, id)
	return err == nil && locked
}
