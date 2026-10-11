package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

const markerHeader = "# ghosttree project; created by ctx project link, remove with ctx project unlink\n"

// linkRefusal sagt, warum dieser Ordner nie verlinkt wird: das Home-Verzeichnis
// und "/" würden jede Sitzung der Maschine einem Projekt zuordnen.
func linkRefusal(dir string) string {
	real := dir
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		real = r
	}
	if real == string(filepath.Separator) {
		return "refusing to link /: every session on this machine would belong to the project"
	}
	if home, err := os.UserHomeDir(); err == nil {
		if h, err := filepath.EvalSymlinks(home); err == nil {
			home = h
		}
		if filepath.Clean(home) == real {
			return "refusing to link your home directory: every session on this machine would belong to the project; link a folder below it"
		}
	}
	return ""
}

func projectCreate(c *client.Client, name, org string, stdout io.Writer) int {
	name = strings.ToLower(strings.TrimSpace(name))
	p, err := c.CreateProject(name, org)
	if err != nil {
		return orgFail(stdout, "create project", err)
	}
	fmt.Fprintf(stdout, "%s belongs to %s; link a folder to it with: ctx project link %s\n", p.Remote, p.Org, p.Remote)
	return 0
}

func projectLink(c *client.Client, name string, stdout io.Writer) int {
	name = strings.ToLower(strings.TrimSpace(name))
	if !scope.IsNamedProject(name) {
		fmt.Fprintf(stdout, "%q is not a project name (a short name such as infra); git repositories need no link\n", name)
		return 2
	}
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stdout, "link: %v\n", err)
		return 1
	}
	if why := linkRefusal(dir); why != "" {
		fmt.Fprintln(stdout, why)
		return 1
	}
	marker := filepath.Join(dir, collector.ProjectMarker)
	if existing, err := collector.ReadProjectMarker(marker); err == nil {
		if existing == name {
			fmt.Fprintf(stdout, "%s is already linked to %s\n", dir, name)
			return 0
		}
		fmt.Fprintf(stdout, "%s is linked to %s already; run ctx project unlink first\n", dir, existing)
		return 1
	}
	list, err := c.WritableProjects()
	if err != nil {
		return orgFail(stdout, "link", err)
	}
	if !slices.Contains(list, name) {
		// Ein Projekt, das es nicht gibt, und eines ohne Schreibrecht ergeben
		// dieselbe Antwort.
		fmt.Fprintf(stdout, "cannot write to project %q: it does not exist or you have no role in it\n", name)
		return 1
	}
	if repo, _ := collector.GitInfo(dir); repo != "" {
		fmt.Fprintf(stdout, "note: this folder is inside the git repository %s; below the link it belongs to %s instead\n", repo, name)
	}
	if err := os.WriteFile(marker, []byte(markerHeader+name+"\n"), 0o644); err != nil {
		fmt.Fprintf(stdout, "link: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s now belongs to %s (marker %s)\n", dir, name, collector.ProjectMarker)
	return 0
}

func projectUnlink(stdout io.Writer) int {
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stdout, "unlink: %v\n", err)
		return 1
	}
	marker := filepath.Join(dir, collector.ProjectMarker)
	if _, err := os.Lstat(marker); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if above, project := collector.FindProjectLink(dir); above != "" {
				fmt.Fprintf(stdout, "not linked here: this folder belongs to %s through %s; run ctx project unlink there\n", project, above)
			} else {
				fmt.Fprintln(stdout, "not linked: there is no "+collector.ProjectMarker+" in this folder")
			}
			return 1
		}
		fmt.Fprintf(stdout, "unlink: %v\n", err)
		return 1
	}
	if err := os.Remove(marker); err != nil {
		fmt.Fprintf(stdout, "unlink: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s is no longer linked to a project\n", dir)
	return 0
}
