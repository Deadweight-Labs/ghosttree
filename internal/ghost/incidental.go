package ghost

import (
	"path"
	"strings"
)

// incidentalDirs sind Verzeichnisse, deren Inhalt niemand einzeln beschreibt.
// Geprüft wird auf ganze Pfadsegmente, nicht auf Zeichenketten: sonst nimmt
// "vendor" das Paket internal/vendoring mit, und eine echte Quelldatei
// verschwindet aus dem Baum, ohne dass es jemandem auffällt.
var incidentalDirs = map[string]bool{
	"testdata":     true,
	"vendor":       true,
	"node_modules": true,
}

// incidentalSuffixes sind Endungen, die eine Datei als beiläufig ausweisen.
// Ebenfalls streng: "_test.go" und nicht "test", sonst trifft es
// internal/testing.go; "_gen.go" und nicht "gen", sonst trifft es
// docs/generated-tokens.md.
var incidentalSuffixes = []string{"_test.go", "_gen.go", ".pb.go", ".min.js", ".min.css", ".lock"}

// incidentalNames sind einzelne Dateien, die zwar versioniert sind, aber nichts
// enthalten, was ein Mensch beschreiben würde.
var incidentalNames = map[string]bool{
	"go.sum":            true,
	"package-lock.json": true,
	"yarn.lock":         true,
	"Cargo.lock":        true,
}

// IsIncidental sagt, ob ein Pfad im Baum eine eigene Ghost-Datei verdient oder
// ob er in die Sammelzeile seines Verzeichnisses gehört.
//
// Die Musterliste ist GEGRIFFEN, NICHT GEMESSEN — sie kommt aus dem Anblick
// dieses einen Repos, in dem `ls internal/store/` 42 Einträge zeigte, von denen
// 20 auf _test.go endeten und nie beschrieben werden würden. Wer sie erweitert,
// prüfe die Gegenprobe in incidental_test.go: ein zu weites Muster versteckt
// echte Quelldateien, und ein versteckter Ast liest sich wie ein leerer.
//
// Beiläufig heisst nicht unsichtbar. Diese Pfade verschwinden nicht aus dem
// Baum, sie bekommen nur keine eigene Datei — ihr Verzeichnis nennt sie in
// einer Zeile.
func IsIncidental(p string) bool {
	if p == "" {
		return false
	}
	name := path.Base(p)
	if incidentalNames[name] {
		return true
	}
	for _, s := range incidentalSuffixes {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	for _, seg := range strings.Split(path.Dir(p), "/") {
		if incidentalDirs[seg] {
			return true
		}
	}
	return false
}
