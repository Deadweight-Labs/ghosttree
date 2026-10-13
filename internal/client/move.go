package client

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// WritableProjects nennt die Projekte, in die dieses Konto Wissen und Aufträge
// schreiben darf. Die Zielangabe beim Schreiben löst sich gegen diese Liste auf.
func (c *Client) WritableProjects() ([]string, error) {
	var out []string
	err := c.do("GET", "/api/projects/writable", nil, nil, &out)
	return out, err
}

// ResolveProject macht aus einer Projektangabe (Remote oder eindeutiger
// Kurzname) die Remote eines beschreibbaren Projekts. Ein Projekt, das es nicht
// gibt, und eines ohne Schreibrecht ergeben denselben Fehler.
func (c *Client) ResolveProject(input string) (string, error) {
	list, err := c.WritableProjects()
	if err != nil {
		return "", err
	}
	return scope.ResolveProject(input, list)
}

// WriteTarget entscheidet, in welches Projekt ein Schreibvorgang geht. Ein
// genanntes Projekt gilt und muss eines sein, in das das Konto schreiben darf;
// ohne Angabe gilt das aktuelle. Mit keinem von beiden gibt es keinen stillen
// Ausweg: der Fehler nennt die beschreibbaren Projekte, damit der nächste
// Aufruf eines nennen kann. "Gibt es nicht" und "darf ich nicht" ergeben
// denselben Fehler.
func (c *Client) WriteTarget(explicit, current string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		list, err := c.WritableProjects()
		if err != nil {
			return "", err
		}
		project, err := scope.ResolveProject(explicit, list)
		if err != nil {
			var ambiguous *scope.ErrProjectAmbiguous
			if errors.As(err, &ambiguous) {
				return "", err
			}
			return "", fmt.Errorf("no project %q among the projects you can write to%s", explicit, writableList(list))
		}
		return project, nil
	}
	if current != "" {
		return current, nil
	}
	list, _ := c.WritableProjects()
	return "", fmt.Errorf("no project given and this is not inside one: name the project (project / --project)%s. In a folder without git: ctx project link <name>", writableList(list))
}

func writableList(list []string) string {
	if len(list) == 0 {
		return ""
	}
	return " (projects you can write to: " + strings.Join(list, ", ") + ")"
}

// MoveKnowledge legt einen Eintrag in ein Projekt, auf eine Maschine oder
// global; genau eines davon.
func (c *Client) MoveKnowledge(id int64, project, machine string, global bool) (store.Knowledge, error) {
	var out store.Knowledge
	err := c.do("POST", "/api/knowledge/"+strconv.FormatInt(id, 10)+"/move", nil,
		map[string]any{"project": project, "machine": machine, "global": global}, &out)
	return out, err
}

// RetireKnowledge zieht einen Eintrag zurück, dessen beschriebener Zustand
// nicht mehr existiert. Die Begründung ist Pflicht.
func (c *Client) RetireKnowledge(id int64, reason string) (store.Knowledge, error) {
	var out store.Knowledge
	err := c.do("POST", "/api/knowledge/"+strconv.FormatInt(id, 10)+"/retire", nil,
		map[string]any{"reason": reason}, &out)
	return out, err
}
