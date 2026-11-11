package store

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// The GitHub account of a person (REQ-599 criterion 14): the login, and its
// number when it is known. It is the source of the profile picture the web
// interface shows and no login. The login alone grants nothing; with the number
// (github_id > 0, only set from a login GitHub proved) it is a verified GitHub
// identity, and a write to github.com/<that login>/... makes the remote a
// project in the person's namespace (REQ-623). A number belongs to one account
// at a time: a unique index holds it, and linking it moves it.
//
// Who may set it: the account's owner after a proven GitHub login (the callback
// of the GitHub connection, REQ-596, calls SetAccountGitHub with the login GitHub
// itself reported), or an instance admin with database access (ctx account
// link-github). Never an organization member for somebody else. The owner can
// also take the link away again (ClearAccountGitHub). The store does not know who
// calls; the callers above are the only ones, and the writer inventory lists both
// methods as writes.

// ErrInvalidGitHubLogin is returned for a login that is not in GitHub's syntax.
var ErrInvalidGitHubLogin = errors.New("not a GitHub login: letters, digits and single hyphens, at most 39 characters")

// githubLoginSyntax is GitHub's own rule: letters, digits and hyphens, no
// hyphen at the start or the end and none twice in a row, 1 to 39 characters.
var githubLoginSyntax = regexp.MustCompile(`^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$`)

// ValidGitHubLogin reports whether login has the syntax of a GitHub login.
func ValidGitHubLogin(login string) bool {
	return len(login) >= 1 && len(login) <= 39 && githubLoginSyntax.MatchString(login)
}

// GitHubLink is the GitHub account of one person. ID is 0 when only the login
// is known.
type GitHubLink struct {
	Login    string
	ID       int64
	LinkedAt string
}

// SetAccountGitHub links an account to a GitHub login (and its number, 0 if not
// known). A second call replaces the first. An invalid login is refused.
func (s *Store) SetAccountGitHub(accountID, login string, githubID int64) error {
	if s.writer != nil {
		return queueWrite(s, []any{accountID, login, githubID}, func(d *Store, p []any) error {
			return d.SetAccountGitHub(p[0].(string), p[1].(string), p[2].(int64))
		})
	}
	id, ok := accountNumericID(accountID)
	if !ok {
		return errAccountNotFound
	}
	login = strings.TrimSpace(login)
	if !ValidGitHubLogin(login) {
		return ErrInvalidGitHubLogin
	}
	if githubID < 0 {
		return fmt.Errorf("%w: a GitHub number is positive", ErrInvalidInput)
	}
	var one int
	if err := s.db.QueryRow(`SELECT 1 FROM persons WHERE id=?`, id).Scan(&one); err != nil {
		return errAccountNotFound
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// The proven number moves here: whichever account held it before keeps
	// only its login, so no two accounts are verified as the same person. The
	// same for the login: GitHub gives a freed login to somebody else, and an
	// older link to it must not stay verified.
	if githubID > 0 {
		if _, err := tx.Exec(`UPDATE account_github SET github_id=0 WHERE (github_id=? OR lower(login)=lower(?)) AND account_id<>?`, githubID, login, id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO account_github(account_id, login, github_id, linked_at) VALUES(?,?,?,?)
		ON CONFLICT(account_id) DO UPDATE SET login=excluded.login, github_id=excluded.github_id, linked_at=excluded.linked_at`,
		id, login, githubID, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	// A verified login is the person's address; an unverified namespace of
	// that name yields (slug_claims.go).
	if githubID > 0 {
		if err := takeVerifiedLoginTx(tx, id, login); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ensureGitHubIDUnique makes a verified GitHub number, and a verified login,
// unique across accounts. Numbers or logins that several verified accounts
// hold already are ambiguous; all of them drop back to the login alone
// (github_id 0) before the indexes are built, so nobody stays verified by a
// link that is in doubt.
func ensureGitHubIDUnique(db *sql.DB) error {
	if _, err := db.Exec(`UPDATE account_github SET github_id=0 WHERE github_id IN (
		SELECT github_id FROM account_github WHERE github_id>0 GROUP BY github_id HAVING COUNT(*)>1)`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE account_github SET github_id=0 WHERE github_id>0 AND lower(login) IN (
		SELECT lower(login) FROM account_github WHERE github_id>0 GROUP BY lower(login) HAVING COUNT(*)>1)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS account_github_id ON account_github(github_id) WHERE github_id>0`); err != nil {
		return err
	}
	_, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS account_github_login ON account_github(lower(login)) WHERE github_id>0`)
	return err
}

// ClearAccountGitHub removes the GitHub link of an account, so its avatar is the
// initials again. Unlinking an account that has none is not an error.
func (s *Store) ClearAccountGitHub(accountID string) error {
	if s.writer != nil {
		return queueWrite(s, []any{accountID}, func(d *Store, p []any) error {
			return d.ClearAccountGitHub(p[0].(string))
		})
	}
	id, ok := accountNumericID(accountID)
	if !ok {
		return errAccountNotFound
	}
	_, err := s.db.Exec(`DELETE FROM account_github WHERE account_id=?`, id)
	return err
}

// AccountGitHub returns the GitHub account of a person, if one is linked.
func (s *Store) AccountGitHub(accountID string) (GitHubLink, bool) {
	if s.reader != nil {
		return s.reader.AccountGitHub(accountID)
	}
	id, ok := accountNumericID(accountID)
	if !ok {
		return GitHubLink{}, false
	}
	var l GitHubLink
	if err := s.db.QueryRow(`SELECT login, github_id, linked_at FROM account_github WHERE account_id=?`, id).Scan(&l.Login, &l.ID, &l.LinkedAt); err != nil {
		return GitHubLink{}, false
	}
	return l, true
}
