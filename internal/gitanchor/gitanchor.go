// Package gitanchor beschreibt den Git-Stand, an dem ein Eintrag geschrieben
// wurde (REQ-597): HEAD-Commit, Branch, Merge-Base zum Default-Branch und ob
// das Arbeitsverzeichnis unsauber war. Das Paket kennt nur Form, Kurzform und
// Transport; ermittelt wird der Anker im Client (collector.ResolveAnchor),
// gespeichert vom Server (store, Tabelle git_anchors).
package gitanchor

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// Header ist der HTTP-Header, mit dem der Client den Anker an einen
// schreibenden Aufruf hängt. Der Wert ist URL-sicheres Base64 eines
// JSON-Objekts: Branch-Namen dürfen alles enthalten, was ein Ref erlaubt, und
// ein Header-Wert soll davon nicht abhängen.
const Header = "X-Ghosttree-Git-Anchor"

// MinPrefix ist die kürzeste Commit-Angabe, die ein Filter annimmt. Kürzer
// träfe in einem großen Repo zu viele Commits und wäre ein Raten.
const MinPrefix = 7

// maxHeaderBytes begrenzt, was der Server von einem Header annimmt.
const maxHeaderBytes = 4096

// Anchor ist ein unveränderlicher Git-Stand. Leer heißt: nicht ermittelbar
// (kein Git, kein Repo, Fehler), nie geraten.
type Anchor struct {
	// Commit ist der volle HEAD-Hash.
	Commit string `json:"commit,omitempty"`
	// Branch ist der ausgecheckte Branch, leer bei losem HEAD.
	Branch string `json:"branch,omitempty"`
	// MergeBase ist der gemeinsame Vorfahr von HEAD und Default-Branch des
	// Remotes (siehe collector.DefaultBaseRef). Leer, wenn es keinen gibt.
	MergeBase string `json:"merge_base,omitempty"`
	// Dirty: das Arbeitsverzeichnis hatte nicht eingecheckte Änderungen an
	// getrackten Dateien. Nicht getrackte Dateien zählen nicht.
	Dirty bool `json:"dirty,omitempty"`
	// PR ist die PR-Nummer, falls bekannt. Der Client setzt sie nie; sie kommt
	// später serverseitig über die GitHub-App (REQ-596). Bis dahin leer.
	PR *int64 `json:"pr,omitempty"`
	// CapturedAt setzt der Server beim Speichern.
	CapturedAt string `json:"captured_at,omitempty"`
}

// IsZero sagt, dass es nichts zu speichern gibt. Der Commit ist der Kern: ohne
// ihn sind Branch und Merge-Base allein kein Anker, sondern nur ein Name.
func (a Anchor) IsZero() bool { return a.Commit == "" }

// Short ist der Commit in der üblichen Kurzform.
func (a Anchor) Short() string { return ShortCommit(a.Commit) }

// ShortCommit kürzt einen Hash auf zwölf Zeichen.
func ShortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

// ValidPrefix prüft die Commit-Angabe eines Filters: mindestens MinPrefix
// Hexzeichen, höchstens ein voller SHA-256-Hash.
func ValidPrefix(p string) bool {
	if len(p) < MinPrefix || len(p) > 64 {
		return false
	}
	for _, r := range p {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// Normalize bringt eine Commit-Angabe in die gespeicherte Form (klein, ohne
// Leerraum).
func Normalize(p string) string { return strings.ToLower(strings.TrimSpace(p)) }

// Encode macht aus dem Anker einen Header-Wert; leer für einen leeren Anker.
func Encode(a Anchor) string {
	if a.IsZero() {
		return ""
	}
	a.CapturedAt = ""
	a.PR = nil
	raw, err := json.Marshal(a)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// Decode liest einen Header-Wert. Alles Ungültige ergibt den leeren Anker:
// ein kaputter Header darf einen Schreibzugriff nie scheitern lassen. Die PR-
// Nummer wird nicht übernommen, sie ist nicht Sache des Clients.
func Decode(value string) Anchor {
	if value == "" || len(value) > maxHeaderBytes {
		return Anchor{}
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return Anchor{}
	}
	var a Anchor
	if json.Unmarshal(raw, &a) != nil {
		return Anchor{}
	}
	a.PR, a.CapturedAt = nil, ""
	a.Commit = Normalize(a.Commit)
	a.MergeBase = Normalize(a.MergeBase)
	if !ValidPrefix(a.Commit) || len(a.MergeBase) > 64 || len(a.Branch) > 512 {
		return Anchor{}
	}
	return a
}
