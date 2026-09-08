// Package migrate discovers and distills repository-local agent artifacts.
package migrate

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
)

type Artifact struct {
	Path, Rel, Kind string
	Size            int64
	Activation      activation.Rule
}

type ScanExclusion struct {
	Rel, Reason string
}

type ScanResult struct {
	Artifacts []Artifact
	Skipped   []ScanExclusion
	Unscanned []ScanExclusion
}

// ShouldDistill separates current agent rules from dated historical material.
// Specs and plans are preserved verbatim as archived cold storage; turning
// their prose or checkboxes into current instructions would erase time.
func ShouldDistill(a Artifact) bool { return a.Kind == "rules" }

// namesTopic sagt, ob ein Dateiname das Wort wirklich nennt, statt es nur zu
// enthalten. "spec" steckt sonst auch in "perspective" und "plan" in
// "planning-tool" — beides ist am 2026-08-25 aufgeschlagen, und der zweite Fall
// wäre als Volltext eines fremden Erzeugnisses im Baum gelandet.
func namesTopic(name, word string) bool {
	for i := 0; i+len(word) <= len(name); i++ {
		if name[i:i+len(word)] != word {
			continue
		}
		if boundary(name, i-1) && boundary(name, i+len(word)) {
			return true
		}
	}
	return false
}

// boundary ist der Rand eines Wortes in einem Dateinamen: der Anfang, das Ende
// oder eines der üblichen Trennzeichen.
func boundary(name string, i int) bool {
	if i < 0 || i >= len(name) {
		return true
	}
	return strings.ContainsRune("-_. /", rune(name[i]))
}

func Scan(repo string) ([]Artifact, error) {
	report, err := ScanWithReport(repo)
	return report.Artifacts, err
}

func ScanWithReport(repo string) (ScanResult, error) {
	var out ScanResult
	repo, err := filepath.Abs(repo)
	if err != nil {
		return out, err
	}
	rules := map[string]bool{"CLAUDE.md": true, "AGENTS.md": true, "GEMINI.md": true, ".cursorrules": true, ".windsurfrules": true, "CONVENTIONS.md": true}
	err = filepath.WalkDir(repo, func(path string, d fs.DirEntry, walkErr error) error {
		rel, err := filepath.Rel(repo, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if walkErr != nil {
			// Was der Nutzer nicht lesen darf, geht die Migration nichts an —
			// ein Datenverzeichnis eines Containers etwa. Daran den ganzen
			// Repo-Lauf scheitern zu lassen, kostet die echten Artefakte
			// desselben Repos mit.
			if os.IsPermission(walkErr) {
				out.Unscanned = append(out.Unscanned, ScanExclusion{rel, "permission denied; contents not inspected"})
				return nil
			}
			return walkErr
		}
		if d.IsDir() {
			excluded := map[string]string{".git": "Git metadata", ".ghosttree": "managed ghosttree state", "vendor": "dependency directory", "node_modules": "dependency directory", "deps": "dependency directory", "_build": "build output"}
			if reason := excluded[d.Name()]; rel != "." && reason != "" {
				out.Unscanned = append(out.Unscanned, ScanExclusion{rel, reason + "; contents not inspected"})
				return fs.SkipDir
			}
			// Ein Verzeichnis mit eigenem .git ist ein anderer Checkout —
			// eingebetteter Arbeitsbaum, Submodul oder abgelegter Klon. Seine
			// Regeldateien gehören seinem Repo, nicht diesem; sie hier
			// mitzunehmen kostet eine zweite Destillation desselben Textes und
			// liesse `--clean` in einem fremden Arbeitsbaum aufräumen. Geprüft
			// wird mit Stat statt auf ein Verzeichnis, weil .git im Arbeitsbaum
			// eine Datei ist.
			if rel != "." {
				if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
					out.Unscanned = append(out.Unscanned, ScanExclusion{rel, "embedded repository or worktree; contents not inspected"})
					return fs.SkipDir
				} else if !os.IsNotExist(err) {
					if !os.IsPermission(err) {
						return err
					}
					out.Unscanned = append(out.Unscanned, ScanExclusion{rel, "permission denied checking repository boundary; contents not inspected"})
					return fs.SkipDir
				}
			}
			return nil
		}
		kind := ""
		if rules[filepath.Base(rel)] {
			kind = "rules"
		}
		lower := strings.ToLower(filepath.Base(rel))
		// Nur Markdown: ein Plan ist ein Text. Eine HTML-Datei aus einem
		// Brainstorm-Verzeichnis oder ein Screenshot ist ein Erzeugnis und
		// gehört nicht als Volltext in den Baum.
		underDocs := strings.HasSuffix(lower, ".md") &&
			(strings.HasPrefix(rel, "docs/") || strings.HasPrefix(rel, ".superpowers/"))
		if strings.HasSuffix(lower, ".md") && strings.HasPrefix(rel, "docs/") {
			kind = "other"
		}
		if underDocs && (namesTopic(lower, "spec") || strings.Contains(rel, "/specs/")) {
			kind = "spec"
		}
		if underDocs && (namesTopic(lower, "plan") || strings.Contains(rel, "/plans/")) {
			kind = "plan"
		}
		if kind == "" {
			if d.Type()&os.ModeSymlink != 0 {
				out.Unscanned = append(out.Unscanned, ScanExclusion{rel, "symbolic link; target not inspected"})
			} else if strings.HasSuffix(lower, ".md") {
				reason := "outside document roots and not a recognized agent rule filename"
				if strings.HasPrefix(rel, ".superpowers/") {
					reason = ".superpowers Markdown is selected only when classified as a spec or plan"
				}
				out.Skipped = append(out.Skipped, ScanExclusion{rel, reason})
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			out.Skipped = append(out.Skipped, ScanExclusion{rel, "symbolic link; target not inspected"})
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if os.IsPermission(err) {
				out.Unscanned = append(out.Unscanned, ScanExclusion{rel, "permission denied reading file metadata"})
				return nil
			}
			return err
		}
		if !info.Mode().IsRegular() {
			out.Skipped = append(out.Skipped, ScanExclusion{rel, "not a regular file; contents not inspected"})
			return nil
		}
		a := Artifact{Path: path, Rel: rel, Kind: kind, Size: info.Size()}
		if kind == "rules" {
			parent := filepath.ToSlash(filepath.Dir(rel))
			if parent != "." {
				a.Activation.Paths = []string{parent + "/**"}
			}
		}
		out.Artifacts = append(out.Artifacts, a)
		return nil
	})
	sort.Slice(out.Artifacts, func(i, j int) bool { return out.Artifacts[i].Rel < out.Artifacts[j].Rel })
	sort.Slice(out.Skipped, func(i, j int) bool { return out.Skipped[i].Rel < out.Skipped[j].Rel })
	sort.Slice(out.Unscanned, func(i, j int) bool { return out.Unscanned[i].Rel < out.Unscanned[j].Rel })
	return out, err
}
