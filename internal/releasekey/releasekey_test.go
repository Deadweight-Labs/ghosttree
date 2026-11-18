package releasekey

import (
	"os"
	"strings"
	"testing"
)

// The key ctx embeds and the key install.sh carries are one key: scripts/release-key.sh
// writes both, and this keeps a hand edit from separating them.
func TestEmbeddedKeyIsTheKeyOfTheInstaller(t *testing.T) {
	deploy, err := os.ReadFile("../../deploy/downloads/ghosttree/minisign.pub")
	if err != nil {
		t.Fatal(err)
	}
	if string(deploy) != embedded {
		t.Fatalf("internal/releasekey/minisign.pub differs from deploy/downloads/ghosttree/minisign.pub; run scripts/release-key.sh")
	}
	script, err := os.ReadFile("../../deploy/downloads/ghosttree/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Split(strings.TrimSpace(embedded), "\n")[1]
	if !strings.Contains(string(script), "pubkey='"+line+"'") {
		t.Fatalf("install.sh does not carry the embedded key %q", line)
	}
}

func TestPublicSaysWhenThereIsNoKeyYetAndParsesARealOne(t *testing.T) {
	_, err := Public()
	if strings.Contains(embedded, "__GHOSTTREE") {
		if err != ErrNoKey {
			t.Fatalf("placeholder key: %v", err)
		}
	} else if err != nil {
		t.Fatalf("the embedded key does not parse: %v", err)
	}
	defer func(old string) { override = old }(override)
	override = "RWRO2MKMhNKzIw7tQgFomtGlgJwXklN+sy5/5922LpGTCnebY7G9Zt+e"
	if k, err := Public(); err != nil || k.ID == [8]byte{} {
		t.Fatalf("override: %v", err)
	}
	override = "not a key"
	if _, err := Public(); err == nil {
		t.Fatal("a broken override was accepted")
	}
}
