package agentbench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTranscriptManifestIsReadableBySha256sum(t *testing.T) {
	runDir := t.TempDir()
	armDir := filepath.Join(runDir, "raw", "ghosttree")
	if err := os.MkdirAll(armDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(armDir, "np-01--ghosttree--r1.jsonl"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	n, err := WriteTranscriptManifest(&out, runDir, filepath.Join(runDir, "raw"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 transcript, got %d", n)
	}
	// sha256sum(1) erwartet genau zwei Leerzeichen und einen relativen Pfad.
	// Ein eigenes Format braeuchte ein eigenes Pruefwerkzeug, und ein
	// Pruefwerkzeug, das niemand ausfuehrt, belegt nichts.
	want := "2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881" +
		"  raw/ghosttree/np-01--ghosttree--r1.jsonl\n"
	if out.String() != want {
		t.Fatalf("manifest line:\n got %q\nwant %q", out.String(), want)
	}
}

// Zwei Berechnungen desselben Laufs muessen dasselbe Manifest ergeben, sonst
// sieht ein Nachrechnen wie eine Aenderung aus.
func TestTranscriptManifestIsSortedAndStable(t *testing.T) {
	runDir := t.TempDir()
	raw := filepath.Join(runDir, "raw")
	for _, name := range []string{"ghosttree/z.jsonl", "bare/a.jsonl", "claudemd/m.jsonl"} {
		path := filepath.Join(raw, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var first, second strings.Builder
	if _, err := WriteTranscriptManifest(&first, runDir, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteTranscriptManifest(&second, runDir, raw); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Fatal("zwei Berechnungen desselben Laufs muessen dasselbe Manifest ergeben")
	}
	lines := strings.Split(strings.TrimSpace(first.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d", len(lines))
	}
	// Sortiert wird nach dem Pfad, nicht nach der Zeile — die faengt mit dem
	// Hash an.
	for i := 1; i < len(lines); i++ {
		previous, current := pathOf(t, lines[i-1]), pathOf(t, lines[i])
		if previous >= current {
			t.Fatalf("nach Pfad sortiert erwartet:\n%s", first.String())
		}
	}
}

func pathOf(t *testing.T, line string) string {
	t.Helper()
	_, path, found := strings.Cut(line, "  ")
	if !found {
		t.Fatalf("keine sha256sum-Zeile: %q", line)
	}
	return path
}

// Eine Nachbewertung, die auf ein Verzeichnis ohne raw/ zeigt, ist kein Fehler:
// sie hat schlicht nichts zu hashen.
func TestTranscriptManifestOfAnEmptyRunIsEmpty(t *testing.T) {
	runDir := t.TempDir()
	var out strings.Builder
	n, err := WriteTranscriptManifest(&out, runDir, filepath.Join(runDir, "raw"))
	if err != nil || n != 0 || out.Len() != 0 {
		t.Fatalf("want an empty manifest without error, got n=%d err=%v out=%q", n, err, out.String())
	}
}
