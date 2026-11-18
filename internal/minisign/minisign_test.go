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

// The attack the trusted comment's own signature stops: a genuine, signed list
// of an older release, with only the comment line edited to name the release
// the victim asked for. The signature over the file still verifies, so only
// the second signature (over signature plus comment) can tell.
func TestRewrittenTrustedCommentIsRefused(t *testing.T) {
	pk, sign := testKey(t)
	msg := []byte("abc  ghosttree_0.0.9_linux_amd64.tar.gz\n")
	genuine := sign(msg, "ghosttree v0.0.9")
	if got, err := Verify(pk, msg, genuine); err != nil || got != "ghosttree v0.0.9" {
		t.Fatalf("the unedited signature must verify: %q, %v", got, err)
	}
	rewritten := strings.Replace(string(genuine), "trusted comment: ghosttree v0.0.9", "trusted comment: ghosttree v0.1.0", 1)
	if rewritten == string(genuine) {
		t.Fatal("the comment was not rewritten")
	}
	if got, err := Verify(pk, msg, []byte(rewritten)); err == nil {
		t.Fatalf("a rewritten trusted comment was accepted as %q", got)
	}
	// The comment of one signature does not transfer to another file either.
	other := sign([]byte("another list"), "ghosttree v0.1.0")
	lines := strings.Split(string(other), "\n")
	mixed := strings.Join([]string{lines[0], strings.Split(string(genuine), "\n")[1], lines[2], lines[3], ""}, "\n")
	if _, err := Verify(pk, msg, []byte(mixed)); err == nil {
		t.Fatal("the comment and its signature of another file were accepted")
	}
}
