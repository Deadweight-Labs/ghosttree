package collector

import (
	"encoding/json"
	"path"
	"strings"
)

// PathTouch ist ein beobachteter Werkzeugaufruf mit Dateibezug.
//
// Warum das eine eigene Struktur ist und nicht weiter im Volltext lebt: der
// Archivtext trägt den Werkzeugnamen und ein auf 600 Zeichen gekürztes
// Argument. Der Name ist damit auswertbar, der PFAD nicht — und ein
// abgeschnittenes Argument verliert ihn stillschweigend. Die Frage "wer hat in
// den letzten dreißig Minuten an dieser Datei gearbeitet" ist damit grep-bar
// und nicht abfragbar, was etwas anderes ist.
type PathTouch struct {
	Tool string
	Path string
	// Quality sagt, wie belastbar die Aussage ist. Die vier Werte stammen aus
	// v1 §6 und sind dort wörtlich benannt; v2 hat den Gedanken nur als Prosa
	// (siehe Wissenseintrag #2077).
	Quality string
}

// Die vier Aussagequalitäten. Sie zu vermischen ist der Fehler, vor dem beide
// Fassungen der Spec warnen: ein gestarteter Werkzeugaufruf ist eine ABSICHT,
// kein Ergebnis, und ein Dateisystem-Watcher sieht eine Änderung, ohne sie
// jemandem zuordnen zu können.
//
// Wo Daten fehlen, lautet die Aussage "unbekannt" — nicht "hat nichts
// verändert". Der Unterschied entscheidet, ob eine Konfliktwarnung
// vertrauenswürdig ist oder nur beruhigend klingt.
const (
	QualityIntent     = "intent"
	QualityReported   = "reported_success"
	QualityObserved   = "observed_change"
	QualityUnattached = "unattributed"
)

// pathFields nennt je Werkzeug die Eingabefelder, die einen Pfad tragen.
//
// Bewusst eine Liste bekannter Werkzeuge statt einer Heuristik über alle
// Felder: "was nach einem Pfad aussieht" fängt Suchmuster, URLs und
// Shell-Schnipsel mit ein, und eine Konfliktwarnung auf einer geratenen Datei
// ist schlimmer als keine. Ein unbekanntes Werkzeug liefert lieber nichts.
var pathFields = map[string][]string{
	"Read":         {"file_path"},
	"Write":        {"file_path"},
	"Edit":         {"file_path"},
	"NotebookEdit": {"notebook_path"},
	"Glob":         {"path"},
	"Grep":         {"path"},
}

// writesPath sagt, welche Werkzeuge eine Datei VERÄNDERN. Lesen und Schreiben
// am selben Pfad sind verschiedene Risiken: zwei Leser stören einander nie.
var writesPath = map[string]bool{
	"Write":        true,
	"Edit":         true,
	"NotebookEdit": true,
}

// ToolPathTouches zieht die Pfadbezüge aus den tool_use-Blöcken einer
// Assistenten-Runde.
//
// Der Aufruf ist die beobachtete ABSICHT — an dieser Stelle im Transkript
// steht, dass ein Werkzeug mit diesem Pfad gerufen wurde, nicht dass es
// funktioniert hat. Das Ergebnis kommt im nächsten Block und wird getrennt
// verbucht.
func ToolPathTouches(raw json.RawMessage) []PathTouch {
	var blocks []struct {
		Type  string          `json:"type"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	var out []PathTouch
	for _, b := range blocks {
		if b.Type != "tool_use" || b.Name == "" {
			continue
		}
		fields, known := pathFields[b.Name]
		if !known {
			continue
		}
		var input map[string]any
		if json.Unmarshal(b.Input, &input) != nil {
			continue
		}
		for _, f := range fields {
			v, ok := input[f].(string)
			if !ok {
				continue
			}
			if p := NormalizeTouchPath(v); p != "" {
				out = append(out, PathTouch{Tool: b.Name, Path: p, Quality: QualityIntent})
			}
		}
	}
	return out
}

// TouchWrites sagt, ob dieses Werkzeug den Pfad verändert hätte.
func TouchWrites(tool string) bool { return writesPath[tool] }

// NormalizeTouchPath bringt einen Pfad auf eine vergleichbare Form. Ohne das
// wären "internal/store/x.go" und "./internal/store/x.go" zwei verschiedene
// Dateien, und eine Überschneidungswarnung bliebe aus, obwohl beide Agenten
// dieselbe Zeile anfassen.
//
// Absolute Pfade bleiben absolut: sie tragen den Checkout in sich, und genau
// den braucht die Unterscheidung zwischen zwei Worktrees desselben Repos.
func NormalizeTouchPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	// Ein Suchmuster ist kein Pfad. Glob und Grep tragen beides im selben
	// Feld, und ein Muster als Datei zu verbuchen erzeugt Warnungen über
	// Dateien, die es nicht gibt.
	if strings.ContainsAny(p, "*?[") {
		return ""
	}
	clean := path.Clean(p)
	// Die Wurzel ist kein Arbeitsgegenstand. Ein Lauf gegen echte
	// Transkripte am 2026-09-14 lieferte "/" als Treffer; eine Warnung
	// "jemand hat / angefasst" ist nur Rauschen und macht die nächste echte
	// Warnung unglaubwürdiger.
	if clean == "/" || clean == "." {
		return ""
	}
	return clean
}
