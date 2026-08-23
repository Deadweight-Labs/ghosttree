// Package migrate discovers and distills repository-local agent artifacts.
package migrate

import (
	"io/fs"
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

func Scan(repo string) ([]Artifact, error) {
	repo, err := filepath.Abs(repo)
	if err != nil {
		return nil, err
	}
	rules := map[string]bool{"CLAUDE.md": true, "AGENTS.md": true, "GEMINI.md": true, ".cursorrules": true, ".windsurfrules": true, "CONVENTIONS.md": true}
	var out []Artifact
	err = filepath.WalkDir(repo, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(repo, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			excluded := map[string]bool{".git": true, "vendor": true, "node_modules": true, "deps": true, "_build": true}
			if rel != "." && excluded[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		kind := ""
		if rules[filepath.Base(rel)] {
			kind = "rules"
		}
		lower := strings.ToLower(filepath.Base(rel))
		underDocs := strings.HasPrefix(rel, "docs/") || strings.HasPrefix(rel, ".superpowers/")
		if underDocs && (strings.Contains(lower, "spec") || strings.Contains(rel, "/specs/")) {
			kind = "spec"
		}
		if underDocs && (strings.Contains(lower, "plan") || strings.Contains(rel, "/plans/")) {
			kind = "plan"
		}
		if kind == "" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		a := Artifact{Path: path, Rel: rel, Kind: kind, Size: info.Size()}
		if kind == "rules" {
			parent := filepath.ToSlash(filepath.Dir(rel))
			if parent != "." {
				a.Activation.Paths = []string{parent + "/**"}
			}
		}
		out = append(out, a)
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out, err
}
