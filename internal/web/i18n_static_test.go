package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// The static scripts get their words from the page: data attributes, a JSON block
// (data-coord-texts) or the language of <html lang>. A word written into a script
// is the same in both languages. This test finds the places where a script puts a
// literal into what a person reads.
var (
	jsBlockRE    = regexp.MustCompile(`(?s)/\*.*?\*/`)
	jsLineRE     = regexp.MustCompile(`(?m)^[ \t]*//.*$|(?m)[ \t]//[ \t].*$`)
	jsLiteral    = `(?:"((?:[^"\\\n]|\\.)*)"|'((?:[^'\\\n]|\\.)*)'|` + "`" + `((?:[^` + "`" + `\\]|\\.)*)` + "`" + `)`
	jsSinkRE     = regexp.MustCompile(`(?:\.(?:textContent|innerText|placeholder|title|alt|label|ariaLabel)\s*=\s*|\bsetAttribute\(\s*["'](?:title|aria-label|placeholder|alt|label)["']\s*,\s*|\bcreateTextNode\(\s*|\b(?:alert|confirm|prompt)\(\s*|\|\|\s*)` + jsLiteral)
	jsMarkupRE   = regexp.MustCompile(`>\s*([A-Za-zÄÖÜäöüß][^<>$]{2,}?)\s*<`)
	jsLettersRE  = regexp.MustCompile(`[A-Za-zÄÖÜäöüß]{2,}`)
	jsHumanWordE = regexp.MustCompile(`^[A-ZÄÖÜ][a-zäöüß]{2,}`)
)

// jsAllowed are literals a script may carry: there are none today. A word that is
// the same in both languages still goes through the catalog, so nobody has to
// judge whether it is.
var jsAllowed = map[string]bool{}

func TestStaticScriptsCarryNoVisibleTextOutsideTheCatalog(t *testing.T) {
	err := fs.WalkDir(files, "static", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".js") || strings.Contains(path, "/vendor/") || strings.HasSuffix(path, ".test.js") {
			return err
		}
		raw, err := fs.ReadFile(files, path)
		if err != nil {
			return err
		}
		src := jsLineRE.ReplaceAllString(jsBlockRE.ReplaceAllString(string(raw), ""), "")
		for _, m := range jsSinkRE.FindAllStringSubmatch(src, -1) {
			lit := m[1] + m[2] + m[3]
			if jsAllowed[lit] || strings.Contains(lit, "${") && !jsHumanWordE.MatchString(lit) {
				continue
			}
			if jsLettersRE.MatchString(lit) && (strings.Contains(strings.TrimSpace(lit), " ") || jsHumanWordE.MatchString(lit)) {
				t.Errorf("%s: %q is written into the page by the script; it belongs in the catalog (data attribute or text block)", path, lit)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// ui-md.js is the one script with words of its own: it picks them by <html lang>.
// Both tables must name the same things.
func TestMarkdownScriptHasBothLanguagesForEveryWord(t *testing.T) {
	raw, err := fs.ReadFile(files, "static/ui-md.js")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)var WORDS = DE\s*\?\s*\{(.*?)\}\s*:\s*\{(.*?)\};`).FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatal("ui-md.js has no WORDS table for DE and EN")
	}
	keys := func(s string) []string {
		var out []string
		for _, k := range regexp.MustCompile(`(\w+):\s*"`).FindAllStringSubmatch(s, -1) {
			out = append(out, k[1])
		}
		return out
	}
	if a, b := strings.Join(keys(m[1]), ","), strings.Join(keys(m[2]), ","); a != b {
		t.Errorf("ui-md.js words differ between German (%s) and English (%s)", a, b)
	}
}

// A German text that is the English one, word for word, was not translated. The
// short ones are names and terms (Branch, Commit, Token) that stay the same.
func TestGermanSentencesAreNotCopiesOfTheEnglishOnes(t *testing.T) {
	for key, en := range messages {
		de := messagesDE[key]
		if de != en {
			continue
		}
		if strings.HasPrefix(en, "ctx ") {
			continue // a command is typed the same way in both languages
		}
		if words := strings.Fields(strings.NewReplacer("%s", "", "%d", "", "·", "", "→", "", "…", "").Replace(en)); len(words) >= 3 && !strings.Contains(en, ":") && !strings.Contains(en, "%[") {
			t.Errorf("key %q has the English text in the German catalog: %q", key, en)
		}
	}
}

// Every key a template or the code uses exists in German as well; a page in German
// must never fall back to the English text.
func TestEveryKeyHasAGermanText(t *testing.T) {
	for key := range messages {
		if strings.TrimSpace(messagesDE[key]) == "" {
			t.Errorf("key %q has no German text", key)
		}
	}
}
