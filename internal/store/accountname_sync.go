package store

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
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
	if want == "" || want == current {
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
	if _, err := tx.Exec(`UPDATE persons SET name=?, name_source=? WHERE id=?`, name, nameSourceIdP, id); err != nil {
		return "", err
	}
	return name, nil
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
	tx, err := s.db.Begin()
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback()
	if _, err := accountState(tx, id); err != nil {
		return Account{}, err
	}
	if taken, err := accountNameTakenByOtherTx(tx, name, id); err != nil {
		return Account{}, err
	} else if taken {
		return Account{}, ErrAccountNameTaken
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
