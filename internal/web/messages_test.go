package web

import (
	"io/fs"
	"os"
	"regexp"
	"strings"
	"testing"
)

// p1Templates sind die Vorlagen, deren Texte vollständig im Katalog liegen.
var p1Templates = []string{"templates/layout.html", "templates/login.html", "templates/overview.html", "templates/sessions.html", "templates/coord.html", "templates/knowledge.html", "templates/requests.html", "templates/orgs.html", "templates/device.html", "templates/profile.html"}

var (
	scriptRE   = regexp.MustCompile(`(?s)<script>.*?</script>`)
	actionRE   = regexp.MustCompile(`(?s)\{\{.*?\}\}`)
	catalogRE  = regexp.MustCompile(`\{\{\s*t\s+"([^"]+)"`)
	textNodeRE = regexp.MustCompile(`>([^<>]+)<`)
	attrRE     = regexp.MustCompile(`\s(?:title|aria-label|placeholder|alt|value|label)="([^"]*)"`)
)

func TestP1TemplatesCarryNoTextOutsideTheCatalog(t *testing.T) {
	for _, name := range p1Templates {
		raw, err := fs.ReadFile(files, name)
		if err != nil {
			t.Fatal(err)
		}
		src := string(raw)
		// Aktionen durch einen Marker ersetzen: was übrig bleibt, ist Literal.
		stripped := scriptRE.ReplaceAllString(actionRE.ReplaceAllString(src, "\x00"), "")
		for _, m := range textNodeRE.FindAllStringSubmatch(stripped, -1) {
			if txt := strings.Trim(m[1], " \t\r\n\x00"); txt != "" {
				t.Errorf("%s: literal text %q outside the catalog", name, txt)
			}
		}
		for _, m := range attrRE.FindAllStringSubmatch(stripped, -1) {
			if v := strings.Trim(m[1], "\x00"); v != "" && !isTechnicalValue(v) {
				t.Errorf("%s: literal attribute text %q outside the catalog", name, v)
			}
		}
	}
}

// isTechnicalValue erlaubt Werte, die kein Text für Menschen sind.
func isTechnicalValue(v string) bool {
	switch v {
	case "message", "directive", "request", "question", "approval", "blocker", "handoff", "open", "resolved", "deferred", "withdraw", "met", "waived":
		return true
	}
	return strings.HasPrefix(v, "/") || strings.HasPrefix(v, "1") || strings.HasPrefix(v, "REQ-") || v == "on" || v == "member" || v == "owner" || v == "guest" || v == "none" || v == "7" || v == "approve" || v == "deny" || v == "member\x00owner"
}

func TestCatalogKeysUsedByTemplatesExistAndEveryKeyIsUsed(t *testing.T) {
	used := map[string]bool{}
	err := fs.WalkDir(files, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := fs.ReadFile(files, path)
		if err != nil {
			return err
		}
		for _, m := range catalogRE.FindAllStringSubmatch(string(raw), -1) {
			used[m[1]] = true
			if _, ok := messages[m[1]]; !ok {
				t.Errorf("%s uses unknown message key %q", path, m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Schlüssel, die Handler oder Daten (Navigation, Rollen) verwenden.
	for _, key := range goMessageKeys(t) {
		used[key] = true
		if _, ok := messages[key]; !ok {
			t.Errorf("code uses unknown message key %q", key)
		}
	}
	for key := range messages {
		if !used[key] {
			t.Errorf("message key %q is not used", key)
		}
	}
}

func TestCatalogTextsAreEnglishAndNotEmpty(t *testing.T) {
	for key, text := range messages {
		if strings.TrimSpace(text) == "" {
			t.Errorf("message %q is empty", key)
		}
		if strings.Count(text, "%s")+strings.Count(text, "%d") > 1 {
			t.Errorf("message %q has more than one verb; use numbered arguments", key)
		}
	}
}

func TestMsgFormatsArgumentsAndNeverReturnsEmpty(t *testing.T) {
	if got := msg("login.provider", "ZITADEL"); got != "Continue with ZITADEL" {
		t.Fatalf("msg returned %q", got)
	}
	if got := msg("no.such.key"); got != "no.such.key" {
		t.Fatalf("an unknown key must surface as itself, got %q", got)
	}
}

var goKeyRE = regexp.MustCompile(`"((?:shell|nav|role|login|auth|overview|ov|setup|age|agent|machine|sessions|coord|knowledge|requests|adm|time|join|pair|profile)\.[a-z0-9_.]+)"`)

// goMessageKeys sammelt die Schlüssel, die im Go-Code vorkommen (Navigation,
// Rollen, Fehlertexte der Handler).
func goMessageKeys(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var keys []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "messages.go" {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range goKeyRE.FindAllStringSubmatch(string(raw), -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				keys = append(keys, m[1])
			}
		}
	}
	return keys
}
