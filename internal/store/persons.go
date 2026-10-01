package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// Principal ist, wer eine Anfrage stellt. ID und Label sind das Konto;
// TokenID, TokenKind und Machine sagen, mit welchem Token — damit spätere
// Schreibpfade Besitz aus dem Token ableiten können statt aus dem Request.
type Principal struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	TokenID   int64  `json:"token_id,omitempty"`
	TokenKind string `json:"token_kind,omitempty"`
	Machine   string `json:"machine,omitempty"`
}

// WebSessionKind kennzeichnet einen Principal einer Websitzung, die aus einem
// Login (OIDC oder Code) entstand und kein Token hat. Er entsteht nur
// serverseitig, nie aus einer Anfrage.
const WebSessionKind = "web"

func newToken() (token, hash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	token = hex.EncodeToString(raw)
	return token, hashToken(token), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// AddPerson legt ein Konto mit einem Legacy-Token an. persons.token_hash wird
// weiter mitgeschrieben, damit ein Rollback auf das alte Binary die Tokens
// behält.
func (s *Store) AddPerson(name string) (string, error) {
	if s.writer != nil {
		return queueValue(s, []any{name}, func(d *Store, p []any) (string, error) { return d.AddPerson(p[0].(string)) })
	}
	token, hash, err := newToken()
	if err != nil {
		return "", err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	at := now()
	res, err := tx.Exec(`INSERT INTO persons(name, token_hash, created_at) VALUES(?,?,?)`, name, hash, at)
	if err != nil {
		return "", err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(`INSERT INTO api_tokens(account_id, token_hash, label, kind, created_at) VALUES(?,?,?,?,?)`,
		id, hash, "legacy", "legacy", at); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Store) Authenticate(token string) (string, bool) {
	if s.reader != nil {
		return s.reader.Authenticate(token)
	}
	principal, ok := s.AuthenticatePrincipal(token)
	return principal.Label, ok
}

// AuthenticatePrincipal löst ein Token über api_tokens zu seinem Konto auf.
// Widerrufene, abgelaufene und an deaktivierte Konten gebundene Tokens
// scheitern wie unbekannte.
func (s *Store) AuthenticatePrincipal(token string) (Principal, bool) {
	if s.reader != nil {
		return s.reader.AuthenticatePrincipal(token)
	}
	var p Principal
	var accountID int64
	err := s.db.QueryRow(`SELECT t.id, t.kind, t.machine, p.id, p.name
		FROM api_tokens t JOIN persons p ON p.id = t.account_id
		WHERE t.token_hash = ? AND t.revoked_at = ''
		  AND (t.expires_at = '' OR t.expires_at > ?) AND p.state = 'active'`,
		hashToken(token), now()).Scan(&p.TokenID, &p.TokenKind, &p.Machine, &accountID, &p.Label)
	if err != nil {
		return Principal{}, false
	}
	p.ID = "person:" + strconv.FormatInt(accountID, 10)
	return p, true
}

func (s *Store) TouchMachine(hostname string) {
	if s.bookkeeper != nil {
		s.bookkeeper.coalesceMachine(hostname)
		return
	}
	s.db.Exec(`INSERT INTO machines(hostname, first_seen, last_seen) VALUES(?,?,?)
	           ON CONFLICT(hostname) DO UPDATE SET last_seen = excluded.last_seen`,
		hostname, now(), now())
}
