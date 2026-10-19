package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/installer"
	"github.com/Deadweight-Labs/ghosttree/internal/projectlink"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func projectCreate(c *client.Client, name, org string, stdout io.Writer) int {
	name = strings.ToLower(strings.TrimSpace(name))
	p, err := c.CreateProject(name, org)
	if err != nil {
		return orgFail(stdout, "create project", err)
	}
	fmt.Fprintf(stdout, "%s belongs to %s; link a folder to it with: ctx project link %s\n", p.Remote, p.Org, p.Name)
	return 0
}

// projectLink ordnet den aktuellen Ordner einem benannten Projekt zu. Die
// Zuordnung steht nur in der lokalen Registry dieser Maschine
// (projectlink.Path()); nichts im Ordner bestimmt sie, damit ein Repo, das man
// auscheckt, sich nicht selbst einem Projekt zuordnen kann.
func projectLink(c *client.Client, name string, stdout io.Writer) int {
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stdout, "link: %v\n", err)
		return 1
	}
	if why := projectlink.Refusal(dir); why != "" {
		fmt.Fprintln(stdout, why)
		return 1
	}
	list, err := c.WritableProjects()
	if err != nil {
		return orgFail(stdout, "link", err)
	}
	id, err := scope.ResolveProject(name, list)
	var amb *scope.ErrProjectAmbiguous
	switch {
	case errors.As(err, &amb):
		fmt.Fprintln(stdout, err)
		return 1
	case err != nil:
		// Ein Projekt, das es nicht gibt, und eines ohne Schreibrecht ergeben
		// dieselbe Antwort.
		fmt.Fprintf(stdout, "cannot write to project %q: it does not exist or you have no role in it\n", name)
		return 1
	}
	if !isNamedProject(c, id) {
		fmt.Fprintf(stdout, "%s is a git repository project and needs no link: its folders are recognised by their origin remote\n", id)
		return 2
	}
	if here, err := projectlink.Canonical(dir); err == nil {
		if existing := linkedHere(here); existing == id {
			fmt.Fprintf(stdout, "%s is already linked to %s\n", here, id)
			return 0
		} else if existing != "" {
			fmt.Fprintf(stdout, "%s is linked to %s already; run ctx project unlink first\n", here, existing)
			return 1
		}
	}
	if repo, _ := collector.GitInfo(dir); repo != "" {
		fmt.Fprintf(stdout, "note: this folder is inside the git repository %s; below the link it belongs to %s instead\n", repo, id)
	}
	real, err := projectlink.Set(dir, id)
	if err != nil {
		fmt.Fprintf(stdout, "link: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s now belongs to %s (recorded on this machine only)\n", real, id)
	return 0
}

// isNamedProject fragt den Server: benannt ist, was dort als benanntes Projekt
// angelegt wurde, nicht jede ID der Form "a/b" (auch gitserver/repo ist eine).
func isNamedProject(c *client.Client, id string) bool {
	projects, err := c.ListProjects("")
	if err != nil {
		return false
	}
	for _, p := range projects {
		if p.Remote == id {
			return p.Named
		}
	}
	return false
}

// linkedHere liefert das Projekt, das genau für diesen Ordner eingetragen ist.
func linkedHere(real string) string {
	for _, e := range projectlink.List() {
		if e.Path == real {
			return e.Project
		}
	}
	return ""
}

func projectUnlink(missing bool, stdout io.Writer) int {
	if missing {
		n, err := projectlink.Prune()
		if err != nil {
			fmt.Fprintf(stdout, "unlink: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "removed %d links to folders that no longer exist\n", n)
		return 0
	}
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stdout, "unlink: %v\n", err)
		return 1
	}
	ok, err := projectlink.Remove(dir)
	if err != nil {
		fmt.Fprintf(stdout, "unlink: %v\n", err)
		return 1
	}
	if !ok {
		if above, project := projectlink.Find(dir); above != "" {
			fmt.Fprintf(stdout, "not linked here: this folder belongs to %s through %s; run ctx project unlink there\n", project, above)
		} else {
			fmt.Fprintln(stdout, "not linked: this folder is not linked to a project")
		}
		return 1
	}
	fmt.Fprintf(stdout, "%s is no longer linked to a project\n", dir)
	return 0
}

// projectLinks listet die Zuordnungen dieser Maschine.
func projectLinks(stdout io.Writer) int {
	entries := projectlink.List()
	if len(entries) == 0 {
		fmt.Fprintln(stdout, "no folders are linked to a project; link one with: ctx project link <name>")
		return 0
	}
	for _, e := range entries {
		note := ""
		if _, err := os.Stat(e.Path); err != nil {
			note = "\t(folder is gone; ctx project unlink --missing removes it)"
		}
		fmt.Fprintf(stdout, "%s\t%s%s\n", e.Path, e.Project, note)
	}
	return 0
}

// linkChecks meldet Zuordnungen, deren Ordner es nicht mehr gibt. Ohne
// Zuordnungen schweigt der Doctor.
func linkChecks() []installer.Check {
	entries := projectlink.List()
	if len(entries) == 0 {
		return nil
	}
	var gone []string
	for _, e := range entries {
		if _, err := os.Stat(e.Path); err != nil {
			gone = append(gone, e.Path)
		}
	}
	if len(gone) == 0 {
		return []installer.Check{{Name: "project links", OK: true, Detail: fmt.Sprintf("%d folders linked (%s)", len(entries), projectlink.Path())}}
	}
	return []installer.Check{{
		Name:   "project links",
		Detail: fmt.Sprintf("%d of %d linked folders no longer exist: %s", len(gone), len(entries), strings.Join(gone, ", ")),
		Fix:    "run 'ctx project unlink --missing'",
	}}
}
