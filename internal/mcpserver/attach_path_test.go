package mcpserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// An agent that a prompt steered must not make the client send a key or any other
// file that is not an image: the path of an attachment is checked before the
// file is read.
func TestAttachByPathReadsOnlyImages(t *testing.T) {
	c, st := newTestClient(t)
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}})
	home := t.TempDir()
	t.Setenv("HOME", home)
	if text, failed := callTool(t, session, "pain", map[string]any{"action": "record", "title": "Button bricht um"}); failed {
		t.Fatalf("%s", text)
	}
	put := func(rel string, b []byte) string {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	key := put(".ssh/id_ed25519", []byte("-----BEGIN OPENSSH "+"PRIVATE KEY-----\n"))
	fake := put("pics/key.png", []byte("-----BEGIN OPENSSH "+"PRIVATE KEY-----\n"))
	hidden := put(".config/shot.png", testPNGBytes(t))
	link := filepath.Join(home, "pics", "link.png")
	if err := os.Symlink(key, link); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]string{"ssh key": key, "key with an image name": fake, "hidden folder": hidden, "link to a key": link} {
		if text, failed := callTool(t, session, "pain", map[string]any{"action": "attach", "id": 1, "path": p}); !failed {
			t.Errorf("%s accepted: %s", name, text)
		}
	}
	// Not an image as data_base64 is refused before it is sent.
	if text, failed := callTool(t, session, "pain", map[string]any{"action": "attach", "id": 1, "data_base64": "c2VjcmV0"}); !failed || !strings.Contains(text, "not a PNG") {
		t.Errorf("data_base64 that is no image: %v %s", failed, text)
	}
	ok := put("pics/ok.png", testPNGBytes(t))
	if text, failed := callTool(t, session, "pain", map[string]any{"action": "attach", "id": 1, "path": ok}); failed {
		t.Fatalf("an image was refused: %s", text)
	}
	var n int
	st.DB().QueryRow(`SELECT COUNT(*) FROM attachments`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d attachments, want 1", n)
	}
}
