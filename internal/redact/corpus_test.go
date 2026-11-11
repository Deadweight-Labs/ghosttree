package redact

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// knownCorpusHits are entries of the local mirror that the scan refuses and
// that are still fine, by the start of their file name:
//
//   - 3149: an App Store Connect key ID after the words "API-Key"
//   - 3515: postgres://user:pw@127.0.0.1 of a throw-away test database
//   - PAIN-22: a pain that quotes false alarms of this very scan, with the
//     backticks of the quotation nested in each other
//
// Both of the first two are what the rule is for, seen from outside; the writer
// rewords them. The list is here so that a change to the scan that adds one
// more is noticed.
var knownCorpusHits = []string{"3149-", "3515-", "PAIN-22-"}

// TestMirrorTextsAreNotRefused measures what the write-time check (PAIN-17)
// does to real texts: the local mirror .ghosttree/ holds the knowledge,
// requests, pains and threads of this repository, and a text it would refuse
// is a false alarm for the next agent who writes something alike. The mirror
// is not in the repository; without it the test has nothing to measure and is
// skipped.
func TestMirrorTextsAreNotRefused(t *testing.T) {
	root := filepath.Join("..", "..", ".ghosttree")
	if _, err := os.Stat(filepath.Join(root, "knowledge")); err != nil {
		t.Skip("no local mirror under .ghosttree/")
	}
	for _, kind := range []string{"knowledge", "requests", "pains", "threads"} {
		files, refused := 0, 0
		err := filepath.WalkDir(filepath.Join(root, kind), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") || filepath.Base(p) == "INDEX.md" {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			files++
			matches := FindSecrets(string(b))
			if len(matches) == 0 {
				return nil
			}
			refused++
			name := filepath.Base(p)
			for _, known := range knownCorpusHits {
				if strings.HasPrefix(name, known) {
					return nil
				}
			}
			// Never the text: the file, the place and the kind.
			t.Errorf("%s would be refused at line %d, column %d (%s)", name, matches[0].Line, matches[0].Column, matches[0].Label)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d texts, %d refused", kind, files, refused)
	}
}
