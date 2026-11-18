package web

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"strings"
	"testing"
)

// Every script under static/vendor is named in THIRD_PARTY_NOTICES.md with the
// SHA-256 of the file as it is checked in, so a replaced or edited third-party
// script cannot slip in without its entry (version, source, hash) changing too.
func TestVendoredScriptsMatchTheirEntryInTheNotices(t *testing.T) {
	notices, err := os.ReadFile("../../THIRD_PARTY_NOTICES.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(notices)
	entries, err := fs.ReadDir(files, "static/vendor")
	if err != nil {
		t.Fatal(err)
	}
	scripts := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".js") {
			continue
		}
		scripts++
		raw, err := fs.ReadFile(files, "static/vendor/"+e.Name())
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		if !strings.Contains(text, "internal/web/static/vendor/"+e.Name()) {
			t.Errorf("%s is not named in THIRD_PARTY_NOTICES.md", e.Name())
		}
		if !strings.Contains(text, "SHA-256: `"+hex.EncodeToString(sum[:])+"`") {
			t.Errorf("%s has the SHA-256 %x, which THIRD_PARTY_NOTICES.md does not list: the file was changed without its entry", e.Name(), sum)
		}
	}
	if scripts == 0 {
		t.Fatal("no vendored script found")
	}
	// The version in the entry is the version the script carries.
	mermaid, _ := fs.ReadFile(files, "static/vendor/mermaid.min.js")
	if !strings.Contains(text, "Version: 12.3.0") || !strings.Contains(string(mermaid), `"12.3.0"`) {
		t.Error("the Mermaid version in THIRD_PARTY_NOTICES.md is not the one in the script")
	}
}
