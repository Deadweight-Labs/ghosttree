package minisign

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testKey(t *testing.T) (PublicKey, func(msg []byte, comment string) []byte) {
	t.Helper()
	pk, priv, err := GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pk, func(msg []byte, comment string) []byte { return Sign(pk, priv, msg, comment) }
}

func TestSignedFileVerifiesAndReturnsTheTrustedComment(t *testing.T) {
	pk, sign := testKey(t)
	msg := []byte("abc  ghosttree_0.1.0_linux_amd64.tar.gz\n")
	got, err := Verify(pk, msg, sign(msg, "ghosttree v0.1.0"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "ghosttree v0.1.0" {
		t.Fatalf("trusted comment = %q", got)
	}
}

func TestVerifyRefusesWhatWasNotSignedByThisKey(t *testing.T) {
	pk, sign := testKey(t)
	other, signOther := testKey(t)
	msg := []byte("checksums")
	sig := sign(msg, "ghosttree v0.1.0")

	cases := map[string]struct {
		key PublicKey
		msg []byte
		sig []byte
	}{
		"changed file":      {pk, []byte("checksumS"), sig},
		"another key":       {other, msg, sig},
		"signed by another": {pk, msg, signOther(msg, "ghosttree v0.1.0")},
		"changed comment": {pk, msg, []byte(strings.Replace(string(sig),
			"trusted comment: ghosttree v0.1.0", "trusted comment: ghosttree v9.9.9", 1))},
		"empty":   {pk, msg, nil},
		"garbage": {pk, msg, []byte("untrusted comment: x\n!!!\ntrusted comment: y\n!!!\n")},
	}
	for name, c := range cases {
		if _, err := Verify(c.key, c.msg, c.sig); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPublicKeyRoundTripsThroughFileAndBareLine(t *testing.T) {
	pk, _ := testKey(t)
	for name, text := range map[string]string{"file": pk.File(), "bare": pk.String() + "\n"} {
		got, err := ParsePublicKey(text)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.ID != pk.ID || !got.Key.Equal(pk.Key) {
			t.Errorf("%s: key changed", name)
		}
	}
	for _, bad := range []string{"", "not base64!", "untrusted comment: x\n"} {
		if _, err := ParsePublicKey(bad); err == nil {
			t.Errorf("%q: accepted", bad)
		}
	}
}

// testdata/ was written by the minisign 0.11 binary (minisign -G -W, then
// minisign -S -t "ghosttree v0.1.0"): the format here is the one the release
// workflow produces, not only what Sign writes.
func TestSignatureFromTheMinisignBinaryVerifies(t *testing.T) {
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	pk, err := ParsePublicKey(string(read("minisign-0.11.pub")))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Verify(pk, read("message.txt"), read("message.txt.minisig"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "ghosttree v0.1.0" {
		t.Fatalf("trusted comment = %q", got)
	}
	if _, err := Verify(pk, []byte("hello!\n"), read("message.txt.minisig")); err == nil {
		t.Fatal("a changed file was accepted")
	}
	if pk.File() != string(read("minisign-0.11.pub")) {
		t.Errorf("File() = %q, minisign wrote %q", pk.File(), read("minisign-0.11.pub"))
	}
}
