package store

import (
	"errors"
	"strings"
)

// A person picks the language of the web interface in the profile (REQ-715). The
// choice sits on the account, not in a browser: it holds on every device after
// the next sign-in. "" is no choice yet, then the server setting and the
// browser decide.

// Interface languages a person can choose.
const (
	LanguageEN = "en"
	LanguageDE = "de"
)

// ErrInvalidLanguage is returned for a language the interface does not have.
var ErrInvalidLanguage = errors.New("language must be en or de")

// ValidLanguage reports whether s is a language of the interface.
func ValidLanguage(s string) bool { return s == LanguageEN || s == LanguageDE }

// AccountLanguage is the language an account chose, or "" when it has not chosen
// one (or the account is unknown).
func (s *Store) AccountLanguage(accountID string) string {
	if s.reader != nil {
		return s.reader.AccountLanguage(accountID)
	}
	id, ok := accountNumericID(accountID)
	if !ok {
		return ""
	}
	var language string
	if err := s.db.QueryRow(`SELECT language FROM persons WHERE id=?`, id).Scan(&language); err != nil {
		return ""
	}
	return language
}

// SetAccountLanguage saves the interface language of an account. "" takes the
// choice back (server setting and browser decide again); anything but "", en and
// de is refused.
func (s *Store) SetAccountLanguage(accountID, language string) error {
	if s.writer != nil {
		return queueWrite(s, []any{accountID, language}, func(d *Store, p []any) error {
			return d.SetAccountLanguage(p[0].(string), p[1].(string))
		})
	}
	language = strings.ToLower(strings.TrimSpace(language))
	if language != "" && !ValidLanguage(language) {
		return ErrInvalidLanguage
	}
	id, ok := accountNumericID(accountID)
	if !ok {
		return errAccountNotFound
	}
	res, err := s.db.Exec(`UPDATE persons SET language=? WHERE id=?`, language, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errAccountNotFound
	}
	return nil
}
