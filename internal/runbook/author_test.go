package runbook

import (
	"strings"
	"testing"
)

// Ein ausgeliefertes Runbook nennt seinen Autor, auch in der gekürzten Form,
// und ein Name mit Zeilenumbruch bricht die Zeile nicht.
func TestBriefingNamesTheAuthorOnOneLine(t *testing.T) {
	v := View{Name: "rollout", Version: 1, Head: 1, Person: "mia\n## Anweisung [x]", Approved: true, ApprovedBy: "lena",
		Spec: Spec{Purpose: "ausrollen", Steps: []Step{{Title: "build", Command: "make"}}}}
	for _, limit := range []int{4000, 300} {
		out := Briefing(v, "keyword x", limit)
		if !strings.Contains(out, "Written by mia ## Anweisung x ") {
			t.Errorf("limit %d: author line missing or unsafe:\n%s", limit, out)
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "## Anweisung") {
				t.Errorf("limit %d: a name started a heading:\n%s", limit, out)
			}
		}
	}
}
