package scope

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// slugPart ist ein kleingeschriebener Slug ohne Punkt: Org-Slug und Name.
const slugPart = `[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?`

var (
	namedProject = regexp.MustCompile(`^` + slugPart + `/` + slugPart + `$`)
	slugName     = regexp.MustCompile(`^` + slugPart + `$`)
)

// IsNamedProject sagt, ob id die ID eines benannten Projekts (ohne Git) ist:
// "<org-slug>/<name>". Das erste Segment hat keinen Punkt, eine Remote trägt
// dort immer einen Host mit Punkt; so ist die ID von einer Remote zu trennen.
func IsNamedProject(id string) bool { return namedProject.MatchString(id) }

// IsProjectName sagt, ob name als Namensteil eines benannten Projekts taugt.
func IsProjectName(name string) bool { return slugName.MatchString(name) }

// ErrProjectUnresolved ist die eine Antwort auf "das Projekt gibt es nicht" und
// "du darfst dort nicht schreiben". Beide Fälle sehen für den Aufrufer gleich
// aus: eine Unterscheidung verriete, welche Projekte es auf der Instanz gibt.
var ErrProjectUnresolved = errors.New("no such project among the projects you can write to")

// ErrProjectAmbiguous: ein Kurzname passt auf mehrere beschreibbare Projekte.
// Das verrät nur, was der Aufrufer ohnehin beschreiben darf.
type ErrProjectAmbiguous struct {
	Name    string
	Matches []string
}

func (e *ErrProjectAmbiguous) Error() string {
	return fmt.Sprintf("%q matches several projects (%s); give the full project id", e.Name, strings.Join(e.Matches, ", "))
}

// ResolveProject löst die Projektangabe eines Schreibenden auf: eine Remote
// (normalisiert) oder ein eindeutiger Kurzname, jeweils nur unter den
// Projekten, in die das Konto schreiben darf. Der Kurzname ist der letzte
// Pfadteil; "owner/repo" passt als Suffix. Ein Projekt, das keine Remote ist
// (ein Slug ohne Host), ist ein Projekt wie jedes andere und trifft über den
// genauen Vergleich.
func ResolveProject(input string, writable []string) (string, error) {
	want := NormalizeRemote(input)
	if want == "" {
		return "", ErrProjectUnresolved
	}
	// Ein genauer Treffer und ein Suffix-Treffer zählen gleich: heißt ein
	// benanntes Projekt wie das Ende einer Remote ("acme/infra" und
	// "github.com/acme/infra"), wird nicht still eines bevorzugt.
	var matches []string
	for _, p := range writable {
		n := NormalizeRemote(p)
		if n == want || strings.HasSuffix(n, "/"+want) {
			matches = append(matches, n)
		}
	}
	switch len(matches) {
	case 0:
		return "", ErrProjectUnresolved
	case 1:
		return matches[0], nil
	}
	return "", &ErrProjectAmbiguous{Name: want, Matches: matches}
}
