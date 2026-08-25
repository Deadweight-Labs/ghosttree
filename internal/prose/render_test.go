package prose

import (
	"strings"
	"testing"
)

func satzfolge(prefix string, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(prefix)
		b.WriteString(" Satz Nummer ")
		b.WriteByte(byte('a' + i))
		b.WriteString(".\n")
	}
	return b.String()
}

// Unveränderte Sätze sind der Grund, warum die Historie heute unlesbar ist:
// neunzig Prozent des Textes stehen zweimal da und tragen nichts bei. Sie
// werden zu einer Zeile mit ihrer Anzahl.
func TestUnchangedRunsCollapseToOneLine(t *testing.T) {
	alt := "Eins. Zwei. Drei. Vier. Alt."
	neu := "Eins. Zwei. Drei. Vier. Neu."

	out := Render(Diff(alt, neu))

	if strings.Contains(out, "Zwei.") {
		t.Errorf("unveränderte Sätze dürfen nicht im Volltext stehen:\n%s", out)
	}
	if !strings.Contains(out, "4 Sätze unverändert") {
		t.Errorf("die Zahl der unveränderten Sätze fehlt:\n%s", out)
	}
	if !strings.Contains(out, "- Alt.") || !strings.Contains(out, "+ Neu.") {
		t.Errorf("die eigentliche Änderung fehlt:\n%s", out)
	}
}

// Kriterium 4 von REQ-180: eine vollständig neu geschriebene Fassung hat kaum
// etwas gemeinsam. Würde der Diff dann beide Fassungen ganz ausgeben, wäre er
// länger als das Dump, das er ersetzt.
func TestACompleteRewriteIsCappedAndSaysHowMuchIsHidden(t *testing.T) {
	out := Render(Diff(satzfolge("Alt", 20), satzfolge("Neu", 20)))

	if lines := strings.Count(out, "\n"); lines > 16 {
		t.Errorf("ein Neuschrieb muss gekappt werden, got %d Zeilen:\n%s", lines, out)
	}
	if !strings.Contains(out, "weitere") {
		t.Errorf("die verschwiegenen Sätze müssen gezählt werden:\n%s", out)
	}
}

// Der Kopf nennt die Größenordnung, bevor der Leser einen einzigen Satz liest.
// Auch bei gekapptem Diff bleibt so sichtbar, wie groß die Änderung war.
func TestSummaryNamesTheScaleOfTheChange(t *testing.T) {
	got := Summary(Diff("Eins. Zwei. Drei. Alt.", "Eins. Zwei. Drei. Neu. Dazu."))
	if !strings.Contains(got, "1 Satz weg") || !strings.Contains(got, "2 neu") || !strings.Contains(got, "3 unverändert") {
		t.Fatalf("Zusammenfassung nennt die Größenordnung nicht: %q", got)
	}
}

// Ein Umzug oder ein identisches Neuschreiben ändert nichts. Der Aufrufer muss
// das erkennen können, statt einen leeren Diff auszugeben.
func TestUnchangedIsRecognisable(t *testing.T) {
	if !Unchanged(Diff("Ein Satz. Noch einer.", "Ein Satz. Noch einer.")) {
		t.Error("gleicher Text muss als unverändert gelten")
	}
	if Unchanged(Diff("Ein Satz.", "Ein anderer Satz.")) {
		t.Error("ein geänderter Satz ist keine Unveränderung")
	}
}
