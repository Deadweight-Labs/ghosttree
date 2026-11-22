package store

import (
	"time"
)

// gardenSessionTokenTTL is how long the token of one gardener session lives: as
// long as the longest run, not longer.
const gardenSessionTokenTTL = 4 * time.Hour

// CreateGardenSessionToken issues the token a gardener session uses to read
// ghosttree. It belongs to the gardener's account but is bound to one project and
// to reading: whatever the session is talked into (its prompt contains text from
// the repository and from merges), the server shows it nothing of another
// project or organization and takes no write from it. Only the gardener's
// account asks, and only for a project whose organization has the gardener
// switched on.
func (s *Store) CreateGardenSessionToken(accountPrincipal, project string) (string, error) {
	if s.writer != nil {
		return queueValue(s, []any{accountPrincipal, project}, func(d *Store, p []any) (string, error) {
			return d.CreateGardenSessionToken(p[0].(string), p[1].(string))
		})
	}
	if err := s.requireGardener(accountPrincipal); err != nil {
		return "", err
	}
	var orgID int64
	if s.db.QueryRow(`SELECT org_id FROM projects WHERE remote=?`, project).Scan(&orgID) != nil {
		return "", ErrProjectNotFound
	}
	if g, ok := gardenSettingsTx(s.db, orgID); !ok || !g.Enabled {
		return "", ErrGardenSettings
	}
	account, err := parsePersonPrincipalID(accountPrincipal)
	if err != nil {
		return "", err
	}
	token, hash, err := newAPIToken()
	if err != nil {
		return "", err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO api_tokens(account_id, token_hash, label, kind, machine, created_at, expires_at)
		VALUES(?,?,?,?,?,?,?)`, account, hash, "garden session", "cli", "", now(), time.Now().UTC().Add(gardenSessionTokenTTL).Format(time.RFC3339))
	if err != nil {
		return "", err
	}
	id, _ := res.LastInsertId()
	if _, err := tx.Exec(`INSERT INTO api_token_scopes(token_id, project, read_only) VALUES(?,?,1)`, id, project); err != nil {
		return "", err
	}
	return token, tx.Commit()
}
