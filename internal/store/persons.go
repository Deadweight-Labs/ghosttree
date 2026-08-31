package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

type Principal struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

func (s *Store) AddPerson(name string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	_, err := s.db.Exec(`INSERT INTO persons(name, token_hash, created_at) VALUES(?,?,?)`,
		name, hex.EncodeToString(sum[:]), now())
	if err != nil {
		return "", err
	}
	return token, nil
}

func (s *Store) Authenticate(token string) (string, bool) {
	principal, ok := s.AuthenticatePrincipal(token)
	return principal.Label, ok
}

func (s *Store) AuthenticatePrincipal(token string) (Principal, bool) {
	sum := sha256.Sum256([]byte(token))
	var id int64
	var name string
	err := s.db.QueryRow(`SELECT id, name FROM persons WHERE token_hash = ?`,
		hex.EncodeToString(sum[:])).Scan(&id, &name)
	if err != nil {
		return Principal{}, false
	}
	return Principal{ID: "person:" + strconv.FormatInt(id, 10), Label: name}, true
}

func (s *Store) TouchMachine(hostname string) {
	s.db.Exec(`INSERT INTO machines(hostname, first_seen, last_seen) VALUES(?,?,?)
	           ON CONFLICT(hostname) DO UPDATE SET last_seen = excluded.last_seen`,
		hostname, now(), now())
}
