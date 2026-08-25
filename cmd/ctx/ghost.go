package main

import (
	"path/filepath"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/ghost"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// WriteTree schreibt den Ghost-Baum neu. Vollständig, nicht abgeglichen: der
// ganze Baum dieses Repos sind rund 47 KB, und ein kompletter Durchlauf ist
// schneller als die Entscheidung, ob er nötig ist.
//
// Fehler sind hier nie fatal. Der Baum ist eine Projektion; ihn nicht schreiben
// zu können, darf weder eine Session noch einen Werkzeugaufruf aufhalten.
//
// Ohne Projekt oder ohne Repo-Wurzel gibt es nichts zu schreiben und nichts zu
// fragen: `git ls-files` liefe im Arbeitsverzeichnis des Prozesses, und der
// Server bekäme den Ast des Projekts "" — beim Session-Start ausserhalb eines
// Repos ist das der Normalfall, nicht der Ausnahmefall.
func WriteTree(c *client.Client, project, repoRoot, home string) error {
	if project == "" || repoRoot == "" {
		return nil
	}
	entries, err := ghost.RepoEntries(repoRoot)
	if err != nil {
		return err
	}
	stored, err := c.GhostTree(project, "")
	if err != nil {
		return err
	}
	described := make(map[string]store.GhostFile, len(stored))
	for _, g := range stored {
		described[g.Path] = g
	}

	// Hier und nicht beim Ausliefern: Verschiebung und Kopie unterscheiden sich
	// daran, ob der alte Pfad noch existiert, und diese Antwort steht nur dort,
	// wo die Dateiliste liegt — auf dieser Seite. Solange nichts verwaist ist,
	// kostet der Aufruf nichts.
	for from, to := range ghost.DetectMoves(repoRoot, entries, described) {
		if err := c.MoveGhost(project, from, to); err != nil {
			continue // ein misslungener Umzug darf den Baum nicht aufhalten
		}
		g := described[from]
		g.Path = to
		described[to] = g
		delete(described, from)
	}

	if err := ghost.EnsureExcluded(repoRoot); err != nil {
		return err
	}
	return ghost.Materialize(ghost.TreeRoot(repoRoot, project, home),
		ghost.BuildDocs(entries, described, treeFreshness(repoRoot, described),
			reviewedEmpty(c, project, repoRoot)))
}

// reviewedEmpty holt die Pfade, die jemand angesehen und absichtlich nicht
// beschrieben hat, und behält nur die, deren Datei sich seitdem nicht geändert
// hat. Der Blob-Vergleich braucht die echten Dateien und kann deshalb nur hier
// stattfinden.
//
// Ein Fehler ist wie überall in diesem Pfad kein Grund, den Baum ausfallen zu
// lassen: ein Baum ohne die vierte Gruppe ist unvollständig, ein fehlender Baum
// ist unbrauchbar.
func reviewedEmpty(c *client.Client, project, repoRoot string) map[string]bool {
	reviews, err := c.GhostReviews(project, "")
	if err != nil {
		return nil
	}
	return ghost.ReviewedEmpty(repoRoot, reviews)
}

// treeFreshness rechnet den Zustand jeder Beschreibung — und nur der
// beschriebenen Pfade. Über alle Einträge zu laufen hiesse, bei jedem
// Beschreiben jede Datei des Repos zu lesen; so sind es in diesem Repo 21
// statt 234, und der Aufwand wächst mit dem, was jemand tatsächlich
// beschrieben hat.
//
// Ein Pfad, dessen Zustand sich nicht ermitteln lässt, taucht in der Karte
// nicht auf und bekommt damit keine Anmerkung. Das ist die richtige Richtung
// zu irren: eine fehlende Warnung ist ein verpasster Hinweis, eine falsche
// Warnung erzieht dazu, Warnungen zu übergehen.
func treeFreshness(repoRoot string, described map[string]store.GhostFile) map[string]ghost.Freshness {
	fresh := make(map[string]ghost.Freshness, len(described))
	for p, g := range described {
		full := filepath.Join(repoRoot, filepath.FromSlash(p))
		if g.Kind == "dir" {
			names, err := ghost.ChildNames(full)
			if err != nil {
				continue
			}
			fresh[p] = ghost.JudgeDir(g.ContentSHA, ghost.HashDir(names))
			continue
		}
		sha, blob, lines, err := ghost.HashFile(full)
		if err != nil {
			continue
		}
		changed, reachable := ghost.ChangedLines(repoRoot, g.GitBlob, blob)
		of := lines
		if g.LineCount > of {
			of = g.LineCount
		}
		fresh[p] = ghost.Judge(g.ContentSHA, sha, changed, of, reachable)
	}
	return fresh
}
