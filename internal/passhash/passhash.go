// Package passhash stores and checks passwords with argon2id.
//
// A hash is one string in the PHC format, $argon2id$v=19$m=…,t=…,p=…$salt$hash,
// so the cost can be raised later and old hashes still verify with their own
// parameters. Needs ReHash tells the caller when a stored hash is weaker than
// the current setting.
package passhash

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Parameters follow the RFC 9106 recommendation for memory-constrained
// settings (second choice): 64 MiB, 3 passes, 4 lanes. One check takes tens
// of milliseconds, which the rate limits of the sign-in routes keep from being
// a way to exhaust the server.
const (
	memoryKiB = 64 * 1024
	passes    = 3
	lanes     = 4
	saltLen   = 16
	keyLen    = 32
)

// Bounds of a password. The minimum is NIST's 8; the maximum keeps a hash from
// being a way to make the server hash megabytes.
const (
	MinLength = 8
	MaxLength = 256
)

var (
	ErrTooShort = errors.New("password is too short")
	ErrTooLong  = errors.New("password is too long")
	ErrFormat   = errors.New("not an argon2id hash")
)

// Check says whether a password is allowed to be set.
func Check(password string) error {
	n := utf8.RuneCountInString(password)
	switch {
	case n < MinLength:
		return ErrTooShort
	case len(password) > MaxLength:
		return ErrTooLong
	}
	return nil
}

// Hash returns the PHC string of a password.
func Hash(password string) (string, error) {
	if err := Check(password); err != nil {
		return "", err
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, passes, memoryKiB, lanes, keyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, memoryKiB, passes, lanes, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

type params struct {
	m, t uint32
	p    uint8
	salt []byte
	key  []byte
}

func parse(hash string) (params, error) {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return params{}, ErrFormat
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return params{}, ErrFormat
	}
	var p params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.m, &p.t, &p.p); err != nil {
		return params{}, ErrFormat
	}
	// A stored value is trusted, but a damaged row must not be a request for
	// gigabytes of memory.
	if p.m == 0 || p.m > 1<<20 || p.t == 0 || p.t > 16 || p.p == 0 {
		return params{}, ErrFormat
	}
	enc := base64.RawStdEncoding
	var err error
	if p.salt, err = enc.DecodeString(parts[4]); err != nil || len(p.salt) < 8 {
		return params{}, ErrFormat
	}
	if p.key, err = enc.DecodeString(parts[5]); err != nil || len(p.key) < 16 {
		return params{}, ErrFormat
	}
	return p, nil
}

// Verify compares a password with a stored hash in constant time.
func Verify(password, hash string) bool {
	if len(password) > MaxLength {
		return false
	}
	p, err := parse(hash)
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), p.salt, p.t, p.m, p.p, uint32(len(p.key)))
	return subtle.ConstantTimeCompare(got, p.key) == 1
}

// NeedsRehash says whether a stored hash was made with weaker parameters than
// Hash uses now; the caller then stores a new hash after a successful Verify.
func NeedsRehash(hash string) bool {
	p, err := parse(hash)
	if err != nil {
		return true
	}
	return p.m < memoryKiB || p.t < passes || p.p < lanes
}

// dummy is a real hash of a password nobody has; checking against it takes as
// long as a real check, so a sign-in with an unknown address costs the same
// time as one with a wrong password.
var dummy, _ = Hash("this password belongs to nobody at all")

// Burn spends the time of one check.
func Burn(password string) {
	if len(password) > MaxLength {
		password = password[:MaxLength]
	}
	Verify(password, dummy)
}
