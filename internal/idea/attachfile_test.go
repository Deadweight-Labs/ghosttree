package idea

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadImageFileStrictOnlyReadsImages(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	good := pngBytes(t, 2, 2)
	write := func(rel string, b []byte) string {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	key := write(".ssh/id_ed25519", []byte("-----BEGIN OPENSSH "+"PRIVATE KEY-----\nAAAA\n"))
	keyAsPng := write("shots/key.png", []byte("-----BEGIN OPENSSH "+"PRIVATE KEY-----\nAAAA\n"))
	okPng := write("shots/ok.png", good)
	hiddenPng := write(".cache/shot.png", good)
	linkToKey := filepath.Join(home, "shots", "link.png")
	if err := os.Symlink(key, linkToKey); err != nil {
		t.Fatal(err)
	}
	linkToPng := filepath.Join(home, "shots", "link-ok.png")
	if err := os.Symlink(okPng, linkToPng); err != nil {
		t.Fatal(err)
	}
	renamed := write("shots/photo.txt", good)

	for name, p := range map[string]string{
		"ssh key": key, "key with image name": keyAsPng, "hidden folder": hiddenPng,
		"link to a key": linkToKey, "no image extension": renamed, "missing": filepath.Join(home, "none.png"),
		"directory": filepath.Join(home, "shots"), "empty": "", "nul": "a\x00.png",
	} {
		if _, err := ReadImageFileStrict(p); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	for name, p := range map[string]string{"plain": okPng, "link to an image": linkToPng} {
		if b, err := ReadImageFileStrict(p); err != nil || len(b) != len(good) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Outside the home directory a dot folder is no reason to refuse.
	other := filepath.Join(t.TempDir(), ".tmp", "shot.png")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, good, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadImageFileStrict(other); err != nil {
		t.Errorf("outside the home directory: %v", err)
	}
}

func TestReadImageFileChecksTheContent(t *testing.T) {
	dir := t.TempDir()
	notImage := filepath.Join(dir, "secret")
	if err := os.WriteFile(notImage, []byte("password=hunter2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadImageFile(notImage); err == nil || !strings.Contains(err.Error(), "not a PNG") {
		t.Fatalf("%v", err)
	}
	if _, err := ReadImageFile(dir); err == nil {
		t.Fatal("a directory is no image")
	}
	big := filepath.Join(dir, "big.png")
	if err := os.WriteFile(big, append(pngBytes(t, 2, 2), make([]byte, MaxAttachmentBytes)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadImageFile(big); err == nil {
		t.Fatal("a file over the limit")
	}
}
