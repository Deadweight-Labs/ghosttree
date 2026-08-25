// Package ghost rechnet die Frische einer Beschreibung und schreibt den
// Ghost-Baum auf Platte. Beides gehört auf den Client: der Server läuft auf
// deployment host und hat die Repositorien nicht.
package ghost

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// driftThreshold trennt "leicht verändert" von "veraltet". Der Wert ist
// gegriffen, nicht gemessen — es gab beim Entwurf keinen Bestand, an dem sich
// das hätte kalibrieren lassen. Wenn jemand ihn ändert, gehört die Messung
// dazu, die den neuen Wert stützt.
const driftThreshold = 25

type Freshness struct {
	State   string `json:"state"` // fresh|drifted|stale|dirchanged|unknown|undescribed
	Percent int    `json:"percent"`
}

// HashFile liefert den Inhaltshash, die git-Blob-Id und die Zeilenzahl. Die
// Blob-Id wird selbst gerechnet statt `git hash-object` aufzurufen: die Formel
// ist festgeschrieben, und ein Prozessstart je Datei ist in einem Hook mit
// 900 ms Budget nicht umsonst.
func HashFile(path string) (sha, blob string, lines int, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", 0, err
	}
	sum := sha256.Sum256(b)
	h := sha1.New()
	fmt.Fprintf(h, "blob %d", len(b))
	h.Write([]byte{0})
	h.Write(b)
	lines = bytes.Count(b, []byte{'\n'})
	if len(b) > 0 && !bytes.HasSuffix(b, []byte{'\n'}) {
		lines++
	}
	return hex.EncodeToString(sum[:]), hex.EncodeToString(h.Sum(nil)), lines, nil
}

// HashDir bindet eine Verzeichnisbeschreibung an die Liste seiner direkten
// Kinder, nicht an deren Inhalt. Ein Paket ändert seinen Zweck nicht, weil eine
// Funktion darin umgeschrieben wurde — wohl aber, wenn eine Datei dazukommt
// oder wegfällt.
func HashDir(childNames []string) string {
	sorted := append([]string(nil), childNames...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	return hex.EncodeToString(sum[:])
}

// ChildNames ist die Liste, aus der HashDir seinen Hash rechnet. Sie steht hier
// und nicht bei ihren Aufrufern, weil der Hook, der Schreibpfad und der Baum
// exakt dieselbe Liste bilden müssen: bildete einer von ihnen sie anders,
// stimmte der gespeicherte Hash nie mit dem gerechneten überein, und jede
// Verzeichnisbeschreibung wäre in dem Augenblick veraltet, in dem sie
// geschrieben wird.
//
// Der Schrägstrich hinter Verzeichnissen ist Teil des Namens: sonst ist eine
// Datei foo nicht von einem Verzeichnis foo zu unterscheiden. Unser eigener
// Fussabdruck zählt nicht mit — .ghosttree ändert sich bei jedem Beschreiben.
func ChildNames(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range ents {
		if e.Name() == ".git" || e.Name() == ".ghosttree" {
			continue
		}
		n := e.Name()
		if e.IsDir() {
			n += "/"
		}
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

// ChangedLines zählt die geänderten Zeilen zwischen zwei Blobs. Der zweite
// Rückgabewert sagt, ob die Frage überhaupt beantwortet werden konnte: die
// beschriebene Fassung kann nie committet worden oder von der Garbage
// Collection geholt worden sein.
func ChangedLines(repoRoot, oldBlob, newBlob string) (int, bool) {
	if oldBlob == "" || newBlob == "" {
		return 0, false
	}
	if err := exec.Command("git", "-C", repoRoot, "cat-file", "-e", oldBlob).Run(); err != nil {
		return 0, false
	}
	out, err := exec.Command("git", "-C", repoRoot, "diff", "--numstat", oldBlob, newBlob).Output()
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		return 0, false
	}
	added, err1 := strconv.Atoi(fields[0])
	deleted, err2 := strconv.Atoi(fields[1])
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return added + deleted, true
}

// Judge fasst die Befunde zu einer Aussage zusammen. of ist die grössere der
// beiden Zeilenzahlen — beschrieben und aktuell — damit eine halbierte Datei
// nicht über hundert Prozent kommt.
func Judge(storedSHA, currentSHA string, changedLines, of int, blobReachable bool) Freshness {
	if storedSHA == "" {
		return Freshness{State: "undescribed"}
	}
	if storedSHA == currentSHA {
		return Freshness{State: "fresh"}
	}
	if !blobReachable || of <= 0 {
		return Freshness{State: "unknown"}
	}
	pct := changedLines * 100 / of
	if pct > 100 {
		pct = 100
	}
	if pct > driftThreshold {
		return Freshness{State: "stale", Percent: pct}
	}
	return Freshness{State: "drifted", Percent: pct}
}

// JudgeDir urteilt über ein Verzeichnis. Es hat keine Zeilen und damit keinen
// Prozentsatz — deshalb ein eigener Ausgang statt eines "stale" mit erfundener
// Null, das Label() nicht von dem einer Datei unterscheiden könnte.
func JudgeDir(storedSHA, currentSHA string) Freshness {
	switch {
	case storedSHA == "":
		return Freshness{State: "undescribed"}
	case storedSHA == currentSHA:
		return Freshness{State: "fresh"}
	default:
		return Freshness{State: "dirchanged"}
	}
}

// Label ist der Zusatz, der beim Ausliefern hinter der Beschreibung steht.
// Frisches bekommt keinen: eine Anmerkung an jeder Zeile ist Rauschen, und
// Rauschen ist genau das, wogegen die Entdopplung antritt.
func (f Freshness) Label() string {
	switch f.State {
	case "drifted":
		return fmt.Sprintf("leicht verändert (%d %%)", f.Percent)
	case "stale":
		return fmt.Sprintf("VERALTET (%d %% der Zeilen geändert) — Beschreibung prüfen", f.Percent)
	case "dirchanged":
		return "contents changed — check the description"
	case "unknown":
		return "changed, extent unknown — check the description"
	}
	return ""
}
