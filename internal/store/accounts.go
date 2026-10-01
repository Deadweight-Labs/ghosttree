package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Account ist ein Login-Konto. Es bleibt die persons-Zeile; die Principal-ID
// "person:<id>" ändert sich nicht, damit nichts Gespeichertes umgeschrieben
// werden muss.
type Account struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Email     string `json:"email,omitempty"`
	Admin     bool   `json:"admin"`
	State     string `json:"state"`
	CreatedAt string `json:"created_at"`
}

// TokenInfo beschreibt ein Token ohne sein Geheimnis; der Hash verlässt den
// Store nicht.
type TokenInfo struct {
	ID         int64  `json:"id"`
	AccountID  string `json:"account_id"`
	Label      string `json:"label"`
	Kind       string `json:"kind"`
	Machine    string `json:"machine,omitempty"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"`
	RevokedAt  string `json:"revoked_at,omitempty"`
}

// TokenSpec sind die Angaben für ein neues Token. ExpiresIn 0 heißt: läuft
// nicht ab.
type TokenSpec struct {
	Label     string
	Machine   string
	ExpiresIn time.Duration
}

var errAccountNotFound = errors.New("account not found")

// migrateAccounts ergänzt persons additiv und überführt jedes vorhandene
// persons.token_hash als Legacy-Token nach api_tokens. Der Token-Teil läuft bei
// jedem Öffnen: token_hash ist UNIQUE, INSERT OR IGNORE macht ihn wiederholbar
// und fängt Personen auf, die ein zurückgerolltes Binary angelegt hat. Ein
// widerrufenes Token bleibt als Zeile stehen und lebt deshalb nicht wieder auf.
// Der Admin-Teil ist einmalig, sonst würde ein entzogenes Admin-Recht beim
// nächsten Start zurückkehren.
func migrateAccounts(db *sql.DB) error {
	for _, c := range []struct{ name, ddl string }{
		{"email", `TEXT NOT NULL DEFAULT ''`},
		{"is_admin", `INTEGER NOT NULL DEFAULT 0`},
		{"state", `TEXT NOT NULL DEFAULT 'active' CHECK(state IN ('active','disabled'))`},
		{"default_org_id", `INTEGER NOT NULL DEFAULT 0`},
	} {
		if err := ensurePersonsColumn(db, c.name, c.ddl); err != nil {
			return err
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT OR IGNORE INTO api_tokens(account_id, token_hash, label, kind, created_at)
		SELECT id, token_hash, 'legacy', 'legacy', created_at FROM persons WHERE token_hash <> ''`); err != nil {
		return err
	}
	var done int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM account_migrations WHERE version=1`).Scan(&done); err != nil {
		return err
	}
	if done == 0 {
		// Ohne Person gibt es keinen Admin zu bestimmen; der Marker bleibt dann
		// aus, und die erste später angelegte Person wird beim nächsten Öffnen
		// Admin. Ein späteres Entziehen bleibt wirksam, weil der Marker danach
		// gesetzt ist.
		res, err := tx.Exec(`UPDATE persons SET is_admin=1 WHERE id=(SELECT MIN(id) FROM persons)`)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			if _, err := tx.Exec(`INSERT INTO account_migrations(version, migrated_at) VALUES(1,?)`, now()); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func ensurePersonsColumn(db *sql.DB, name, ddl string) error {
	rows, err := db.Query(`PRAGMA table_info(persons)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var column, typ string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &column, &typ, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if column == name {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	return addPersonsColumn(db, name, ddl)
}

// addPersonsColumn wertet "duplicate column" als Erfolg: öffnen zwei Prozesse
// gleichzeitig, gewinnt einer das ALTER, und der andere hat sein Ziel ebenfalls
// erreicht.
func addPersonsColumn(db *sql.DB, name, ddl string) error {
	_, err := db.Exec(`ALTER TABLE persons ADD COLUMN ` + name + ` ` + ddl)
	if err != nil && strings.Contains(err.Error(), "duplicate column") {
		return nil
	}
	return err
}

// AddAccount legt ein Konto ohne Token an. Tokens gibt CreateToken aus.
func (s *Store) AddAccount(name, email string, admin bool) (Account, error) {
	if s.writer != nil {
		return queueValue(s, []any{name, email, admin}, func(d *Store, p []any) (Account, error) {
			return d.AddAccount(p[0].(string), p[1].(string), p[2].(bool))
		})
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Account{}, fmt.Errorf("account name is required")
	}
	flag := 0
	if admin {
		flag = 1
	}
	if _, err := s.db.Exec(`INSERT INTO persons(name, token_hash, created_at, email, is_admin) VALUES(?,?,?,?,?)`,
		name, "", now(), strings.TrimSpace(email), flag); err != nil {
		return Account{}, err
	}
	return s.AccountByName(name)
}

func (s *Store) AccountByName(name string) (Account, error) {
	if s.reader != nil {
		return s.reader.AccountByName(name)
	}
	return scanAccount(s.db.QueryRow(accountSelect+` WHERE name=?`, strings.TrimSpace(name)))
}

// AccountByPrincipalID löst "person:<id>" auf.
func (s *Store) AccountByPrincipalID(principalID string) (Account, error) {
	if s.reader != nil {
		return s.reader.AccountByPrincipalID(principalID)
	}
	id, err := parsePersonPrincipalID(principalID)
	if err != nil {
		return Account{}, err
	}
	return scanAccount(s.db.QueryRow(accountSelect+` WHERE id=?`, id))
}

func (s *Store) ListAccounts() ([]Account, error) {
	if s.reader != nil {
		return s.reader.ListAccounts()
	}
	rows, err := s.db.Query(accountSelect + ` ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

const accountSelect = `SELECT id, name, email, is_admin, state, created_at FROM persons`

func scanAccount(r rowScanner) (Account, error) {
	var a Account
	var id int64
	var admin int
	if err := r.Scan(&id, &a.Name, &a.Email, &admin, &a.State, &a.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Account{}, errAccountNotFound
		}
		return Account{}, err
	}
	a.ID = "person:" + strconv.FormatInt(id, 10)
	a.Admin = admin == 1
	return a, nil
}

// CreateToken stellt ein cli-Token für ein Konto aus und gibt es genau einmal
// im Klartext zurück; gespeichert wird nur der Hash.
func (s *Store) CreateToken(account string, spec TokenSpec) (string, TokenInfo, error) {
	if s.writer != nil {
		type result struct {
			token string
			info  TokenInfo
		}
		r, err := queueValue(s, []any{account, spec}, func(d *Store, p []any) (result, error) {
			t, i, err := d.CreateToken(p[0].(string), p[1].(TokenSpec))
			return result{t, i}, err
		})
		return r.token, r.info, err
	}
	a, err := s.AccountByName(account)
	if err != nil {
		return "", TokenInfo{}, err
	}
	if a.State != "active" {
		return "", TokenInfo{}, fmt.Errorf("account %q is %s", a.Name, a.State)
	}
	if spec.ExpiresIn < 0 {
		return "", TokenInfo{}, fmt.Errorf("token lifetime must be positive")
	}
	token, hash, err := newToken()
	if err != nil {
		return "", TokenInfo{}, err
	}
	id, _ := parsePersonPrincipalID(a.ID)
	expires := ""
	if spec.ExpiresIn > 0 {
		expires = time.Now().UTC().Add(spec.ExpiresIn).Format(time.RFC3339)
	}
	res, err := s.db.Exec(`INSERT INTO api_tokens(account_id, token_hash, label, kind, machine, created_at, expires_at)
		VALUES(?,?,?,?,?,?,?)`, id, hash, spec.Label, "cli", spec.Machine, now(), expires)
	if err != nil {
		return "", TokenInfo{}, err
	}
	tokenID, _ := res.LastInsertId()
	info, err := s.tokenByID(tokenID)
	return token, info, err
}

func (s *Store) tokenByID(id int64) (TokenInfo, error) {
	return scanToken(s.db.QueryRow(tokenSelect+` WHERE id=?`, id))
}

const tokenSelect = `SELECT id, account_id, label, kind, machine, created_at, last_used_at, expires_at, revoked_at FROM api_tokens`

func scanToken(r rowScanner) (TokenInfo, error) {
	var t TokenInfo
	var account int64
	if err := r.Scan(&t.ID, &account, &t.Label, &t.Kind, &t.Machine, &t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt, &t.RevokedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TokenInfo{}, fmt.Errorf("token not found")
		}
		return TokenInfo{}, err
	}
	t.AccountID = "person:" + strconv.FormatInt(account, 10)
	return t, nil
}

// ListTokens liefert die Tokens eines Kontos, auch widerrufene.
func (s *Store) ListTokens(account string) ([]TokenInfo, error) {
	if s.reader != nil {
		return s.reader.ListTokens(account)
	}
	a, err := s.AccountByName(account)
	if err != nil {
		return nil, err
	}
	id, _ := parsePersonPrincipalID(a.ID)
	rows, err := s.db.Query(tokenSelect+` WHERE account_id=? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TokenInfo
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeToken widerruft ein Token. Wiederholt aufgerufen bleibt der erste
// Widerrufszeitpunkt stehen.
func (s *Store) RevokeToken(id int64) error {
	if s.writer != nil {
		return queueWrite(s, []any{id}, func(d *Store, p []any) error { return d.RevokeToken(p[0].(int64)) })
	}
	if _, err := s.tokenByID(id); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE api_tokens SET revoked_at=? WHERE id=? AND revoked_at=''`, now(), id)
	return err
}

// PrincipalValid sagt, ob das Token und das Konto des Principals noch gelten.
// Langlebige Sitzungen (Web) prüfen damit bei jeder Anfrage nach, statt dem
// Login-Zeitpunkt zu glauben. Nur Lesen.
func (s *Store) PrincipalValid(p Principal) bool {
	if s.reader != nil {
		return s.reader.PrincipalValid(p)
	}
	account, err := parsePersonPrincipalID(p.ID)
	if err != nil || p.TokenID == 0 {
		return false
	}
	var one int
	err = s.db.QueryRow(`SELECT 1 FROM api_tokens t JOIN persons p ON p.id = t.account_id
		WHERE t.id = ? AND t.account_id = ? AND t.revoked_at = ''
		  AND (t.expires_at = '' OR t.expires_at > ?) AND p.state = 'active'`,
		p.TokenID, account, now()).Scan(&one)
	return err == nil
}
