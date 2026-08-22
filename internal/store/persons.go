package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

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
	sum := sha256.Sum256([]byte(token))
	var name string
	err := s.db.QueryRow(`SELECT name FROM persons WHERE token_hash = ?`,
		hex.EncodeToString(sum[:])).Scan(&name)
	return name, err == nil
}

func (s *Store) TouchMachine(hostname string) {
	s.db.Exec(`INSERT INTO machines(hostname, first_seen, last_seen) VALUES(?,?,?)
	           ON CONFLICT(hostname) DO UPDATE SET last_seen = excluded.last_seen`,
		hostname, now(), now())
}
