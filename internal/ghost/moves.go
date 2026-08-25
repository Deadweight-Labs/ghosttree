package ghost

import (
	"path/filepath"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// DetectMoves findet Beschreibungen, deren Datei umgezogen ist, und nennt den
// neuen Pfad.
//
// WARUM DAS HIER STEHT UND NICHT BEIM AUSLIEFERN: Verschiebung und Kopie
// unterscheiden sich einzig darin, ob der ALTE Pfad noch existiert. Der Server
// kann das nicht wissen — er hat die Repositorien nicht. Die frühere Erkennung
// lief trotzdem dort, allein über den git-Blob, und hängte deshalb bei einer
// simplen Dateikopie die Beschreibung des noch existierenden Originals auf die
// Kopie um. Das Original stand danach unbeschrieben da, und niemand erfuhr es
// (REQ-179). Hier liegt die vollständige Dateiliste vor, und damit ist die
// Frage beantwortbar statt zu raten.
//
// WARUM ES TROTZDEM BILLIG IST: Solange keine Beschreibung verwaist ist — der
// Normalfall bei jedem Baumschreiben — wird keine einzige Datei gelesen. Erst
// wenn etwas fehlt, werden die unbeschriebenen Dateien gehasht, und auch dann
// nur sie.
//
// Es wird nie geraten. Genau ein Kandidat zählt als Umzug; zwei Dateien mit
// demselben Inhalt sind eine Verdopplung, und ein Ziel, das schon eine
// Beschreibung hat, wird nicht überschrieben.
func DetectMoves(repoRoot string, entries []Entry, described map[string]store.GhostFile) map[string]string {
	live := make(map[string]bool, len(entries))
	for _, e := range entries {
		live[e.Path] = true
	}

	// Verzeichnisse bleiben aussen vor: sie haben keinen Blob, und ein
	// Verzeichnis über Inhaltsgleichheit umzuhängen hiesse zu raten.
	var orphans []store.GhostFile
	for path, g := range described {
		if g.Kind == "dir" || g.GitBlob == "" || live[path] {
			continue
		}
		orphans = append(orphans, g)
	}
	if len(orphans) == 0 {
		return nil
	}

	// Nur unbeschriebene Dateien kommen als Ziel infrage — ein beschriebenes
	// Ziel zu überschreiben wäre derselbe Datenverlust in der anderen Richtung.
	byBlob := map[string][]string{}
	for _, e := range entries {
		if e.Kind != "file" {
			continue
		}
		if _, taken := described[e.Path]; taken {
			continue
		}
		_, blob, _, err := HashFile(filepath.Join(repoRoot, filepath.FromSlash(e.Path)))
		if err != nil {
			continue
		}
		byBlob[blob] = append(byBlob[blob], e.Path)
	}

	// Zwei Verwaiste mit demselben Blob sind untereinander nicht zu
	// unterscheiden. Welche von ihnen den Kandidaten bekäme, hinge an der
	// Reihenfolge, in der sie aus einer Map gefallen sind — also am Zufall.
	// Beide bleiben liegen; auch hier wird nicht geraten, nur eben in einer
	// Richtung, die man ohne diese Zeile nie bemerkt hätte.
	orphansPerBlob := map[string]int{}
	for _, g := range orphans {
		orphansPerBlob[g.GitBlob]++
	}

	moves := map[string]string{}
	for _, g := range orphans {
		cands := byBlob[g.GitBlob]
		if len(cands) != 1 || orphansPerBlob[g.GitBlob] != 1 {
			continue
		}
		moves[g.Path] = cands[0]
	}
	if len(moves) == 0 {
		return nil
	}
	return moves
}
