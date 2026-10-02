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
	Account    string `json:"account,omitempty"` // Kontoname, nur in ListAllTokens
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
	if err := allowDeviceTokenKind(db); err != nil {
		return err
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

// allowDeviceTokenKind erweitert die CHECK-Bedingung von api_tokens um die Art
// 'device'. SQLite kann eine CHECK-Bedingung nicht ändern; die Tabelle wird
// deshalb einmal neu aufgebaut, wenn ihre Definition die Art noch nicht kennt.
// Auf neuen Datenbanken ist das ein Lesezugriff auf sqlite_master.
func allowDeviceTokenKind(db *sql.DB) error {
	var ddl string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='api_tokens'`).Scan(&ddl); err != nil {
		return err
	}
	if strings.Contains(ddl, "'device'") {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Ein zweiter Prozess kann den Umbau inzwischen erledigt haben.
	if err := tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='api_tokens'`).Scan(&ddl); err != nil {
		return err
	}
	if strings.Contains(ddl, "'device'") {
		return nil
	}
	for _, stmt := range []string{
		`CREATE TABLE api_tokens_new(
  id INTEGER PRIMARY KEY,
  account_id INTEGER NOT NULL REFERENCES persons(id) ON DELETE RESTRICT,
  token_hash TEXT NOT NULL UNIQUE, label TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL CHECK(kind IN ('cli','legacy','device')),
  machine TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL, last_used_at TEXT NOT NULL DEFAULT '',
  expires_at TEXT NOT NULL DEFAULT '', revoked_at TEXT NOT NULL DEFAULT '')`,
		`INSERT INTO api_tokens_new SELECT id, account_id, token_hash, label, kind, machine, created_at, last_used_at, expires_at, revoked_at FROM api_tokens`,
		`DROP TABLE api_tokens`,
		`ALTER TABLE api_tokens_new RENAME TO api_tokens`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return err
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
	name = NormalizeAccountName(name)
	if name == "" {
		return Account{}, fmt.Errorf("account name is required")
	}
	flag := 0
	if admin {
		flag = 1
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback()
	if taken, err := accountNameTakenTx(tx, name); err != nil {
		return Account{}, err
	} else if taken {
		return Account{}, ErrAccountNameTaken
	}
	if _, err := tx.Exec(`INSERT INTO persons(name, token_hash, created_at, email, is_admin) VALUES(?,?,?,?,?)`,
		name, "", now(), strings.TrimSpace(email), flag); err != nil {
		return Account{}, err
	}
	if err := tx.Commit(); err != nil {
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
	if m := canonicalMachine(spec.Machine); m != "" {
		if err := s.ClaimMachine(m, a.ID); err != nil {
			return "", TokenInfo{}, err
		}
		spec.Machine = m
	}
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
	if p.TokenKind == WebSessionKind && p.TokenID == 0 {
		return s.AccountActive(p.ID)
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

// Code-Arten in account_codes. Es werden nur Hashes gespeichert; der
// Klartext existiert genau einmal, bei der Ausgabe.
const (
	CodeBootstrap = "bootstrap"
	CodeClaim     = "claim"
	CodeLogin     = "login"
	// CodeInvitation steht nicht in account_codes, sondern in invitations;
	// CodeKindFor meldet sie mit.
	CodeInvitation = "invitation"

	BootstrapCodeTTL = 24 * time.Hour
	ClaimCodeTTL     = 30 * time.Minute
	LoginLinkTTL     = 10 * time.Minute
)

var (
	// ErrCodeInvalid deckt unbekannt, abgelaufen, schon benutzt und falsche Art
	// ab. Der Aufrufer soll Gründe nicht unterscheiden können.
	ErrCodeInvalid = errors.New("code is invalid, expired or already used")
	// ErrNoAccountForIdentity: Die Identität ist nirgends verbunden und es kam
	// weder ein Claim- noch ein Bootstrap-Code mit (Registrierung nur per
	// Einladung).
	ErrNoAccountForIdentity = errors.New("no account is linked to this identity")
	ErrAccountDisabled      = errors.New("account is disabled")
	ErrAccountHasIdentity   = errors.New("account already has an identity")
)

// IdentityLogin ist das Ergebnis einer erfolgreichen IdP-Anmeldung, wie der
// Store es braucht. Code ist optional: ein Claim- oder Bootstrap-Code.
type IdentityLogin struct {
	Issuer, Subject string
	Email, Name     string
	Code            string
}

// LoginOutcome sagt, was LoginIdentity getan hat.
type LoginOutcome string

const (
	LoginExisting     LoginOutcome = "existing"
	LoginClaimed      LoginOutcome = "claimed"
	LoginBootstrapped LoginOutcome = "bootstrapped"
	// LoginInvited: neues Konto aus einer Einladung, mit Org-Mitgliedschaft.
	LoginInvited LoginOutcome = "invited"
	// LoginJoined: bekannte Identität, die über eine Einladung einer weiteren
	// Organisation beigetreten ist.
	LoginJoined LoginOutcome = "joined"
)

func newCode() (plain, hash string, err error) { return newToken() }

// CreateAccountCode gibt einen Claim- oder Login-Code für ein bestehendes Konto
// aus. Ein Claim-Code entsteht nur für ein aktives Konto ohne Identität, damit
// er nie ein schon verbundenes Konto umhängen kann.
func (s *Store) CreateAccountCode(kind, account string) (string, time.Duration, error) {
	if s.writer != nil {
		type result struct {
			code string
			ttl  time.Duration
		}
		r, err := queueValue(s, []any{kind, account}, func(d *Store, p []any) (result, error) {
			c, t, err := d.CreateAccountCode(p[0].(string), p[1].(string))
			return result{c, t}, err
		})
		return r.code, r.ttl, err
	}
	var ttl time.Duration
	switch kind {
	case CodeClaim:
		ttl = ClaimCodeTTL
	case CodeLogin:
		ttl = LoginLinkTTL
	default:
		return "", 0, fmt.Errorf("unsupported code kind %q", kind)
	}
	a, err := s.AccountByName(account)
	if err != nil {
		return "", 0, err
	}
	if a.State != "active" {
		return "", 0, fmt.Errorf("account %q is %s", a.Name, a.State)
	}
	id, _ := parsePersonPrincipalID(a.ID)
	if kind == CodeClaim {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM account_identities WHERE account_id=?`, id).Scan(&n); err != nil {
			return "", 0, err
		}
		if n > 0 {
			return "", 0, ErrAccountHasIdentity
		}
	}
	code, hash, err := newCode()
	if err != nil {
		return "", 0, err
	}
	if _, err := s.db.Exec(`INSERT INTO account_codes(code_hash, kind, account_id, expires_at) VALUES(?,?,?,?)`,
		hash, kind, id, time.Now().UTC().Add(ttl).Format(time.RFC3339)); err != nil {
		return "", 0, err
	}
	return code, ttl, nil
}

// EnsureBootstrapCode legt auf einer leeren Instanz (keine Person) einen
// frischen Bootstrap-Code an und verwirft ältere unbenutzte. Sonst gibt es
// keinen: ein bestehendes Konto wird per Claim-Code übernommen.
func (s *Store) EnsureBootstrapCode() (string, bool, error) {
	if s.writer != nil {
		type result struct {
			code string
			ok   bool
		}
		r, err := queueValue(s, nil, func(d *Store, _ []any) (result, error) {
			c, ok, err := d.EnsureBootstrapCode()
			return result{c, ok}, err
		})
		return r.code, r.ok, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	var persons int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM persons`).Scan(&persons); err != nil {
		return "", false, err
	}
	if persons > 0 {
		return "", false, nil
	}
	code, hash, err := newCode()
	if err != nil {
		return "", false, err
	}
	if _, err := tx.Exec(`DELETE FROM account_codes WHERE kind='bootstrap' AND used_at=''`); err != nil {
		return "", false, err
	}
	if _, err := tx.Exec(`INSERT INTO account_codes(code_hash, kind, expires_at) VALUES(?,?,?)`,
		hash, CodeBootstrap, time.Now().UTC().Add(BootstrapCodeTTL).Format(time.RFC3339)); err != nil {
		return "", false, err
	}
	return code, true, tx.Commit()
}

// consumeCode verbraucht einen Code atomar: ein einziges UPDATE mit allen
// Bedingungen, damit zwei gleichzeitige Einlösungen nicht beide gewinnen.
func consumeCode(tx *sql.Tx, code, kind string) (accountID int64, err error) {
	hash := hashToken(code)
	res, err := tx.Exec(`UPDATE account_codes SET used_at=? WHERE code_hash=? AND kind=? AND used_at='' AND expires_at>?`,
		now(), hash, kind, now())
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return 0, ErrCodeInvalid
	}
	err = tx.QueryRow(`SELECT account_id FROM account_codes WHERE code_hash=?`, hash).Scan(&accountID)
	return accountID, err
}

func codeKind(tx *sql.Tx, code string) (string, error) {
	var kind string
	err := tx.QueryRow(`SELECT kind FROM account_codes WHERE code_hash=?`, hashToken(code)).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrCodeInvalid
	}
	return kind, err
}

func accountState(tx *sql.Tx, id int64) (Account, error) {
	return scanAccount(tx.QueryRow(accountSelect+` WHERE id=?`, id))
}

// createBootstrapAccount legt innerhalb der Transaktion das erste Konto an.
func createBootstrapAccount(tx *sql.Tx, name, email string) (int64, error) {
	var persons int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM persons`).Scan(&persons); err != nil {
		return 0, err
	}
	if persons > 0 {
		return 0, ErrCodeInvalid
	}
	name = NormalizeAccountName(name)
	if name == "" {
		name = "admin"
	}
	res, err := tx.Exec(`INSERT INTO persons(name, token_hash, created_at, email, is_admin) VALUES(?,?,?,?,1)`,
		name, "", now(), strings.TrimSpace(email))
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	// Eine leere Instanz bekommt mit ihrem ersten Konto die Default-Organisation.
	if _, err := createOrgTx(tx, "Default", "default", id); err != nil {
		return 0, err
	}
	return id, nil
}

// LoginIdentity löst eine IdP-Identität zu einem Konto auf. Bekannte Identität:
// Anmeldung. Unbekannte: nur mit gültigem Claim-Code (verbindet ein
// bestehendes Konto ohne Identität) oder Bootstrap-Code (erstes Konto einer
// leeren Instanz, Admin). Alles andere ist ErrNoAccountForIdentity; es gibt
// keine offene Registrierung. Alles läuft in einer Transaktion, ein
// gescheiterter Versuch verbraucht den Code nicht.
func (s *Store) LoginIdentity(in IdentityLogin) (Account, LoginOutcome, error) {
	if s.writer != nil {
		type result struct {
			a Account
			o LoginOutcome
		}
		r, err := queueValue(s, []any{in}, func(d *Store, p []any) (result, error) {
			a, o, err := d.LoginIdentity(p[0].(IdentityLogin))
			return result{a, o}, err
		})
		return r.a, r.o, err
	}
	key := in.Issuer + "\x00" + in.Subject
	if in.Code != "" && s.attemptLimiter().blocked(key) {
		return Account{}, "", ErrTooManyAttempts
	}
	a, o, err := s.loginIdentity(in)
	if in.Code != "" {
		s.attemptLimiter().note(key, err)
	}
	return a, o, err
}

func (s *Store) loginIdentity(in IdentityLogin) (Account, LoginOutcome, error) {
	if in.Issuer == "" || in.Subject == "" {
		return Account{}, "", fmt.Errorf("identity needs issuer and subject")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Account{}, "", err
	}
	defer tx.Rollback()
	var accountID int64
	err = tx.QueryRow(`SELECT account_id FROM account_identities WHERE issuer=? AND subject=?`, in.Issuer, in.Subject).Scan(&accountID)
	switch {
	case err == nil:
		a, err := accountState(tx, accountID)
		if err != nil {
			return Account{}, "", err
		}
		if a.State != "active" {
			return Account{}, "", ErrAccountDisabled
		}
		// Ein Einladungs-Code nimmt auch ein bekanntes Konto in eine weitere
		// Organisation auf. Andere Codes ignoriert eine bekannte Identität wie
		// bisher.
		if in.Code != "" && invitationExists(tx, in.Code) {
			switch _, err := acceptInvitationTx(tx, in.Code, accountID, in.Email); {
			case errors.Is(err, ErrAlreadyMember):
				return a, LoginExisting, nil
			case err != nil:
				return Account{}, "", err
			}
			return a, LoginJoined, tx.Commit()
		}
		return a, LoginExisting, nil
	case !errors.Is(err, sql.ErrNoRows):
		return Account{}, "", err
	}
	if in.Code == "" {
		return Account{}, "", ErrNoAccountForIdentity
	}
	kind, err := codeKind(tx, in.Code)
	if errors.Is(err, ErrCodeInvalid) && invitationExists(tx, in.Code) {
		kind, err = "invitation", nil
	}
	if err != nil {
		return Account{}, "", err
	}
	var outcome LoginOutcome
	switch kind {
	case "invitation":
		// Registrierung nur per Einladung: Konto und Mitgliedschaft entstehen
		// gemeinsam oder gar nicht. in.Email ist nur gesetzt, wenn der IdP sie
		// als verifiziert gemeldet hat.
		if accountID, err = createInvitedAccountTx(tx, in.Name, in.Email, in.Code); err != nil {
			return Account{}, "", err
		}
		outcome = LoginInvited
	case CodeClaim:
		if accountID, err = consumeCode(tx, in.Code, CodeClaim); err != nil {
			return Account{}, "", err
		}
		a, err := accountState(tx, accountID)
		if err != nil {
			return Account{}, "", err
		}
		if a.State != "active" {
			return Account{}, "", ErrAccountDisabled
		}
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM account_identities WHERE account_id=?`, accountID).Scan(&n); err != nil {
			return Account{}, "", err
		}
		if n > 0 {
			return Account{}, "", ErrAccountHasIdentity
		}
		if a.Email == "" && in.Email != "" {
			if _, err := tx.Exec(`UPDATE persons SET email=? WHERE id=?`, strings.TrimSpace(in.Email), accountID); err != nil {
				return Account{}, "", err
			}
		}
		outcome = LoginClaimed
	case CodeBootstrap:
		if _, err := consumeCode(tx, in.Code, CodeBootstrap); err != nil {
			return Account{}, "", err
		}
		if accountID, err = createBootstrapAccount(tx, in.Name, in.Email); err != nil {
			return Account{}, "", err
		}
		outcome = LoginBootstrapped
	default:
		// Ein Login-Link bindet keine Identität.
		return Account{}, "", ErrCodeInvalid
	}
	if _, err := tx.Exec(`INSERT INTO account_identities(account_id, issuer, subject, created_at) VALUES(?,?,?,?)`,
		accountID, in.Issuer, in.Subject, now()); err != nil {
		return Account{}, "", err
	}
	a, err := accountState(tx, accountID)
	if err != nil {
		return Account{}, "", err
	}
	return a, outcome, tx.Commit()
}

// BootstrapLocal erstellt das erste Konto ohne IdP: Bootstrap-Code plus ein
// frei gewählter Name. Das Konto wird Admin.
func (s *Store) BootstrapLocal(code, name string) (Account, error) {
	if s.writer != nil {
		return queueValue(s, []any{code, name}, func(d *Store, p []any) (Account, error) {
			return d.BootstrapLocal(p[0].(string), p[1].(string))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback()
	if _, err := consumeCode(tx, code, CodeBootstrap); err != nil {
		return Account{}, err
	}
	id, err := createBootstrapAccount(tx, name, "")
	if err != nil {
		return Account{}, err
	}
	a, err := accountState(tx, id)
	if err != nil {
		return Account{}, err
	}
	return a, tx.Commit()
}

// RedeemLoginLink löst einen Einmal-Login-Link ein und gibt das Konto zurück.
func (s *Store) RedeemLoginLink(code string) (Account, error) {
	if s.writer != nil {
		return queueValue(s, []any{code}, func(d *Store, p []any) (Account, error) {
			return d.RedeemLoginLink(p[0].(string))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback()
	id, err := consumeCode(tx, code, CodeLogin)
	if err != nil {
		return Account{}, err
	}
	a, err := accountState(tx, id)
	if err != nil {
		return Account{}, err
	}
	if a.State != "active" {
		return Account{}, ErrAccountDisabled
	}
	return a, tx.Commit()
}

// CodeKindFor sagt, welche Art ein noch gültiger Code hat, ohne ihn zu
// verbrauchen. Die Web-Seite entscheidet damit, ob sie einen Namen braucht.
func (s *Store) CodeKindFor(code string) string {
	if s.reader != nil {
		return s.reader.CodeKindFor(code)
	}
	var kind string
	err := s.db.QueryRow(`SELECT kind FROM account_codes WHERE code_hash=? AND used_at='' AND expires_at>?`,
		hashToken(code), now()).Scan(&kind)
	if err != nil {
		var one int
		if s.db.QueryRow(`SELECT 1 FROM invitations WHERE code_hash=? AND accepted_at='' AND revoked_at='' AND expires_at>?`,
			hashToken(code), now()).Scan(&one) == nil {
			return CodeInvitation
		}
		return ""
	}
	return kind
}

// HasIdentities sagt, ob irgendein Konto schon eine IdP-Identität hat. Solange
// nicht, bleibt der Token-Login der Weboberfläche der einzige Weg in eine
// bestehende Instanz.
func (s *Store) HasIdentities() bool {
	if s.reader != nil {
		return s.reader.HasIdentities()
	}
	var n int
	return s.db.QueryRow(`SELECT COUNT(*) FROM account_identities`).Scan(&n) == nil && n > 0
}

// AccountActive prüft für kontobasierte Websitzungen (ohne Token), ob das
// Konto noch existiert und aktiv ist.
func (s *Store) AccountActive(principalID string) bool {
	if s.reader != nil {
		return s.reader.AccountActive(principalID)
	}
	a, err := s.AccountByPrincipalID(principalID)
	return err == nil && a.State == "active"
}

// TokenByID liefert ein Token ohne Geheimnis.
func (s *Store) TokenByID(id int64) (TokenInfo, error) {
	if s.reader != nil {
		return s.reader.TokenByID(id)
	}
	return s.tokenByID(id)
}

// ListAllTokens liefert die Tokens aller Konten samt Kontoname, neueste zuerst.
// Nur für Administratoren gedacht; der Aufrufer entscheidet.
func (s *Store) ListAllTokens() ([]TokenInfo, error) {
	if s.reader != nil {
		return s.reader.ListAllTokens()
	}
	rows, err := s.db.Query(`SELECT t.id, t.account_id, t.label, t.kind, t.machine, t.created_at, t.last_used_at,
		t.expires_at, t.revoked_at, p.name FROM api_tokens t JOIN persons p ON p.id = t.account_id ORDER BY t.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TokenInfo
	for rows.Next() {
		var t TokenInfo
		var account int64
		if err := rows.Scan(&t.ID, &account, &t.Label, &t.Kind, &t.Machine, &t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt, &t.RevokedAt, &t.Account); err != nil {
			return nil, err
		}
		t.AccountID = "person:" + strconv.FormatInt(account, 10)
		out = append(out, t)
	}
	return out, rows.Err()
}

// CreateDeviceToken stellt das Token eines Geräte-Logins aus: gebunden an die
// Maschine, Art 'device'. Frühere, noch gültige Geräte-Tokens desselben Kontos
// für dieselbe Maschine werden im selben Schritt widerrufen (ein Token pro
// Maschine). Manuelle und Legacy-Tokens bleiben unberührt: Skripte können sie
// noch brauchen.
func (s *Store) CreateDeviceToken(accountID, machine string) (string, TokenInfo, error) {
	if s.writer != nil {
		type result struct {
			token string
			info  TokenInfo
		}
		r, err := queueValue(s, []any{accountID, machine}, func(d *Store, p []any) (result, error) {
			t, i, err := d.CreateDeviceToken(p[0].(string), p[1].(string))
			return result{t, i}, err
		})
		return r.token, r.info, err
	}
	id, err := parsePersonPrincipalID(accountID)
	if err != nil {
		return "", TokenInfo{}, err
	}
	machine = canonicalMachine(machine)
	if machine == "" {
		return "", TokenInfo{}, fmt.Errorf("device token needs a machine")
	}
	token, hash, err := newToken()
	if err != nil {
		return "", TokenInfo{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", TokenInfo{}, err
	}
	defer tx.Rollback()
	var state string
	if err := tx.QueryRow(`SELECT state FROM persons WHERE id=?`, id).Scan(&state); err != nil {
		return "", TokenInfo{}, errAccountNotFound
	}
	if state != "active" {
		return "", TokenInfo{}, ErrAccountDisabled
	}
	if err := claimMachineTx(tx, machine, id); err != nil {
		return "", TokenInfo{}, err
	}
	at := now()
	if _, err := tx.Exec(`UPDATE api_tokens SET revoked_at=? WHERE account_id=? AND kind='device' AND machine=? AND revoked_at=''`,
		at, id, machine); err != nil {
		return "", TokenInfo{}, err
	}
	res, err := tx.Exec(`INSERT INTO api_tokens(account_id, token_hash, label, kind, machine, created_at)
		VALUES(?,?,?,?,?,?)`, id, hash, "ctx login on "+machine, "device", machine, at)
	if err != nil {
		return "", TokenInfo{}, err
	}
	if err := tx.Commit(); err != nil {
		return "", TokenInfo{}, err
	}
	tokenID, _ := res.LastInsertId()
	info, err := s.tokenByID(tokenID)
	return token, info, err
}
