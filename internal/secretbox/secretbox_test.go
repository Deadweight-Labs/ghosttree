package secretbox

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testBox(t *testing.T, fill byte) *Box {
	t.Helper()
	b, err := New(bytes.Repeat([]byte{fill}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSealOpensOnlyWithTheSameKeyAndPurpose(t *testing.T) {
	b := testBox(t, 1)
	sealed, err := b.Seal("sk-test-1234567890", "ai-connection")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("sk-test")) || bytes.Contains(sealed, []byte("7890")) {
		t.Fatal("the sealed value carries the key in the clear")
	}
	if got, err := b.Open(sealed, "ai-connection"); err != nil || got != "sk-test-1234567890" {
		t.Fatalf("open: %q %v", got, err)
	}
	if _, err := b.Open(sealed, "other"); !errors.Is(err, ErrSealed) {
		t.Fatalf("other purpose: %v", err)
	}
	if _, err := testBox(t, 2).Open(sealed, "ai-connection"); !errors.Is(err, ErrSealed) {
		t.Fatalf("other key: %v", err)
	}
	again, _ := b.Seal("sk-test-1234567890", "ai-connection")
	if bytes.Equal(again, sealed) {
		t.Fatal("two seals of the same key are identical (nonce reused)")
	}
	var none *Box
	if _, err := none.Seal("x", "p"); !errors.Is(err, ErrNoKey) {
		t.Fatalf("no key: %v", err)
	}
}

func TestLoadReadsTheCredentialOrTheNamedFile(t *testing.T) {
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv(EnvKeyFile, "")
	if b, err := Load(); b != nil || err != nil {
		t.Fatalf("nothing configured: %v %v", b, err)
	}
	dir := t.TempDir()
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	file := filepath.Join(dir, "secret-key")
	if err := os.WriteFile(file, []byte(key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvKeyFile, file)
	b, err := Load()
	if err != nil || b == nil {
		t.Fatalf("file: %v %v", b, err)
	}
	cred := t.TempDir()
	if err := os.WriteFile(filepath.Join(cred, CredentialName), []byte(strings.Repeat("ab", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", cred)
	c, err := Load()
	if err != nil || c == nil {
		t.Fatalf("credential: %v %v", c, err)
	}
	sealed, _ := c.Seal("x", "p")
	if _, err := b.Open(sealed, "p"); err == nil {
		t.Fatal("the credential wins over the file")
	}
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv(EnvKeyFile, filepath.Join(dir, "missing"))
	if _, err := Load(); err == nil {
		t.Fatal("a named file that is missing is an error")
	}
	if err := os.WriteFile(file, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvKeyFile, file)
	if _, err := Load(); err == nil {
		t.Fatal("a key of the wrong length is an error")
	}
}

func TestLast4ShowsNoMoreThanFourCharacters(t *testing.T) {
	if got := Last4("sk-ant-api03-abcdefWXYZ"); got != "WXYZ" {
		t.Fatalf("last4: %q", got)
	}
	if got := Last4("short"); got != "" {
		t.Fatalf("a short key shows nothing: %q", got)
	}
}
