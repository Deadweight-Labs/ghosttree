package scope

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// namedProject ist die Gestalt der ID eines benannten Projekts (ohne Git): ein
// kleingeschriebener Slug. Er enthält weder Punkt noch "/", und genau daran
// trennt er sich von jeder normalisierten Remote, die immer Host/Pfad trägt.
var namedProject = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$`)

// IsNamedProject sagt, ob id die ID eines benannten Projekts sein kann.
func IsNamedProject(id string) bool { return namedProject.MatchString(id) }

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
	return fmt.Sprintf("%q matches several projects (%s); give the full remote", e.Name, strings.Join(e.Matches, ", "))
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
	for _, p := range writable {
		if NormalizeRemote(p) == want {
			return NormalizeRemote(p), nil
		}
	}
	var matches []string
	for _, p := range writable {
		n := NormalizeRemote(p)
		if strings.HasSuffix(n, "/"+want) {
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
