// Package secretbox seals the API keys of AI connections (REQ-694) before they
// go into the database. The server key never lives in the database: it comes
// from a systemd credential or a file named by the operator. Without it the
// server still runs, but no key can be stored in the database.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// EnvKeyFile names a file holding the server key.
	EnvKeyFile = "GHOSTTREE_SECRET_KEY_FILE"
	// CredentialName is the systemd credential the server key is read from
	// (LoadCredential=ghosttree-secret-key:/etc/ghosttree/secret-key).
	CredentialName = "ghosttree-secret-key"

	version   byte = 1
	keyLength      = 32
)

var (
	// ErrNoKey: the server has no key, so nothing can be sealed or opened.
	ErrNoKey = errors.New("the server has no secret key (GHOSTTREE_SECRET_KEY_FILE or the systemd credential ghosttree-secret-key)")
	// ErrSealed: the sealed value is damaged or was sealed with another key.
	ErrSealed = errors.New("sealed value cannot be opened with this server key")
)

// Box seals and opens short secrets with AES-256-GCM.
type Box struct{ aead cipher.AEAD }

// New makes a Box from a 32-byte key.
func New(key []byte) (*Box, error) {
	if len(key) != keyLength {
		return nil, fmt.Errorf("secret key must be %d bytes, got %d", keyLength, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Load reads the server key: the systemd credential first, then the file in
// GHOSTTREE_SECRET_KEY_FILE. It returns nil and no error when neither is
// configured; a configured key that cannot be read is an error, so that a typo
// does not silently switch the feature off.
func Load() (*Box, error) {
	path := ""
	if dir := os.Getenv("CREDENTIALS_DIRECTORY"); dir != "" {
		if p := filepath.Join(dir, CredentialName); fileExists(p) {
			path = p
		}
	}
	if path == "" {
		path = strings.TrimSpace(os.Getenv(EnvKeyFile))
	}
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read secret key: %w", err)
	}
	key, err := ParseKey(string(raw))
	if err != nil {
		return nil, fmt.Errorf("secret key in %s: %w", path, err)
	}
	return New(key)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// ParseKey reads a key written as base64 (standard or URL alphabet) or hex.
func ParseKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if b, err := hex.DecodeString(s); err == nil && len(b) == keyLength {
		return b, nil
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == keyLength {
			return b, nil
		}
	}
	return nil, fmt.Errorf("expected %d bytes as base64 or hex", keyLength)
}

// Seal encrypts plain. purpose is bound to the result: a value sealed for one
// purpose does not open for another.
func (b *Box) Seal(plain, purpose string) ([]byte, error) {
	if b == nil {
		return nil, ErrNoKey
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := append([]byte{version}, nonce...)
	return b.aead.Seal(out, nonce, []byte(plain), []byte(purpose)), nil
}

// Open decrypts a value made by Seal with the same purpose.
func (b *Box) Open(sealed []byte, purpose string) (string, error) {
	if b == nil {
		return "", ErrNoKey
	}
	n := b.aead.NonceSize()
	if len(sealed) < 1+n+b.aead.Overhead() || sealed[0] != version {
		return "", ErrSealed
	}
	plain, err := b.aead.Open(nil, sealed[1:1+n], sealed[1+n:], []byte(purpose))
	if err != nil {
		return "", ErrSealed
	}
	return string(plain), nil
}

// Last4 is all of a key that is ever shown again: its last four characters.
// A key of eight characters or fewer shows nothing, since four would be half.
func Last4(key string) string {
	key = strings.TrimSpace(key)
	r := []rune(key)
	if len(r) <= 8 {
		return ""
	}
	return string(r[len(r)-4:])
}
