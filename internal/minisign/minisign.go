// Package minisign verifies and writes minisign signatures
// (https://jedisct1.github.io/minisign/), the format that signs the checksum
// list of every Ghosttree release.
//
// Verification is the part the product needs: ctx server update checks the
// release it is about to install, and the tests of install.sh produce
// signatures with a throwaway key. Signing exists so those tests need no
// minisign binary; the release itself is signed by minisign in the workflow.
//
// Only the format that minisign writes by default is accepted: a prehashed
// signature ("ED": Ed25519 over the BLAKE2b-512 digest of the file) or the
// legacy one ("Ed": Ed25519 over the file), both with the trusted comment
// covered by the global signature. Anything else is an error.
package minisign

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/blake2b"
)

const (
	algLegacy    = "Ed"
	algPrehashed = "ED"
	trustedLabel = "trusted comment: "
	keyIDSize    = 8
)

// PublicKey is a minisign public key: the key itself and the 8-byte id that
// signatures carry, so a signature made with another key is named as such.
type PublicKey struct {
	ID  [keyIDSize]byte
	Key ed25519.PublicKey
}

// ParsePublicKey reads a public key from the file minisign -G writes (an
// "untrusted comment:" line, then the base64 line) or from the bare base64
// line alone, the form install.sh carries.
func ParsePublicKey(text string) (PublicKey, error) {
	line := ""
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "untrusted comment:") {
			continue
		}
		if line != "" {
			return PublicKey{}, errors.New("minisign: public key has more than one key line")
		}
		line = l
	}
	raw, err := base64.StdEncoding.DecodeString(line)
	if err != nil {
		return PublicKey{}, fmt.Errorf("minisign: public key is not base64: %w", err)
	}
	if len(raw) != 2+keyIDSize+ed25519.PublicKeySize || string(raw[:2]) != algLegacy {
		return PublicKey{}, errors.New("minisign: not an Ed25519 minisign public key")
	}
	var pk PublicKey
	copy(pk.ID[:], raw[2:2+keyIDSize])
	pk.Key = ed25519.PublicKey(append([]byte(nil), raw[2+keyIDSize:]...))
	return pk, nil
}

// String is the bare base64 line of the key, as install.sh carries it.
func (pk PublicKey) String() string {
	raw := append([]byte(algLegacy), pk.ID[:]...)
	return base64.StdEncoding.EncodeToString(append(raw, pk.Key...))
}

// File is the text of a public key file.
func (pk PublicKey) File() string {
	return fmt.Sprintf("untrusted comment: minisign public key %X\n%s\n", reverse(pk.ID[:]), pk)
}

func reverse(b []byte) []byte {
	out := make([]byte, len(b))
	for i := range b {
		out[len(b)-1-i] = b[i]
	}
	return out
}

// Verify checks that sigFile, the contents of a .minisig file, is a valid
// signature of msg by pk, including the trusted comment. It returns the
// trusted comment, which only the key holder can have written.
func Verify(pk PublicKey, msg, sigFile []byte) (trusted string, err error) {
	lines := strings.Split(strings.ReplaceAll(string(sigFile), "\r\n", "\n"), "\n")
	if len(lines) < 4 || !strings.HasPrefix(lines[0], "untrusted comment:") || !strings.HasPrefix(lines[2], trustedLabel) {
		return "", errors.New("minisign: not a minisign signature file")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[1]))
	if err != nil || len(raw) != 2+keyIDSize+ed25519.SignatureSize {
		return "", errors.New("minisign: malformed signature line")
	}
	global, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[3]))
	if err != nil || len(global) != ed25519.SignatureSize {
		return "", errors.New("minisign: malformed global signature line")
	}
	if !bytes.Equal(raw[2:2+keyIDSize], pk.ID[:]) {
		return "", errors.New("minisign: signature was made with another key")
	}
	sig := raw[2+keyIDSize:]
	signed := msg
	switch string(raw[:2]) {
	case algLegacy:
	case algPrehashed:
		sum := blake2b.Sum512(msg)
		signed = sum[:]
	default:
		return "", errors.New("minisign: unknown signature algorithm")
	}
	if !ed25519.Verify(pk.Key, signed, sig) {
		return "", errors.New("minisign: signature does not match the file")
	}
	trusted = strings.TrimPrefix(lines[2], trustedLabel)
	if !ed25519.Verify(pk.Key, append(append([]byte(nil), sig...), trusted...), global) {
		return "", errors.New("minisign: trusted comment does not match the signature")
	}
	return trusted, nil
}

// GenerateKey makes a key pair without a password. The release key is made
// with minisign itself; this is for tests.
func GenerateKey(rand io.Reader) (PublicKey, ed25519.PrivateKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand)
	if err != nil {
		return PublicKey{}, nil, err
	}
	var pk PublicKey
	if _, err := io.ReadFull(rand, pk.ID[:]); err != nil {
		return PublicKey{}, nil, err
	}
	pk.Key = pub
	return pk, priv, nil
}

// Sign writes the contents of a .minisig file for msg the way minisign -S
// does: prehashed, with trustedComment under the global signature. It is used
// by tests; the release is signed by the minisign binary.
func Sign(pk PublicKey, priv ed25519.PrivateKey, msg []byte, trustedComment string) []byte {
	sum := blake2b.Sum512(msg)
	sig := ed25519.Sign(priv, sum[:])
	global := ed25519.Sign(priv, append(append([]byte(nil), sig...), trustedComment...))
	raw := append(append([]byte(algPrehashed), pk.ID[:]...), sig...)
	var b strings.Builder
	b.WriteString("untrusted comment: signature from minisign secret key\n")
	b.WriteString(base64.StdEncoding.EncodeToString(raw) + "\n")
	b.WriteString(trustedLabel + trustedComment + "\n")
	b.WriteString(base64.StdEncoding.EncodeToString(global) + "\n")
	return []byte(b.String())
}
