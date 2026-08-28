package main

import (
	"os"
	"path/filepath"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/ghost"
	"github.com/Deadweight-Labs/ghosttree/internal/mirror"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// doneRequestsShown begrenzt, wie viel Erledigtes im Spiegel steht. Der Ledger
// wächst, das Verzeichnis soll es nicht: was fertig ist, braucht man selten,
// und der Index sagt, wie viele zurückgehalten wurden.
const doneRequestsShown = 25

// WriteMirror schreibt Wissen, Dokumente und den Auftragsspeicher als Dateien
// unter .ghosttree/. Die drei Verzeichnisse werden je für sich vollständig neu
// gebaut — der Ghost-Baum daneben bleibt unberührt, er hat seinen eigenen
// Durchlauf.
//
// Sitzungsprotokolle werden nicht einmal abgefragt: sie sind Hunderte Megabyte
// und haben trotz Schwärzung in einem Repo-Verzeichnis nichts zu suchen.
func WriteMirror(c *client.Client, ax scope.Axes, repoRoot string) error {
	project := ax.Project
	if project == "" || repoRoot == "" {
		return nil
	}
	if ax.Machine == "" {
		ax.Machine = c.Machine()
	}
	knowledge, err := c.Knowledge(ax)
	if err != nil {
		return err
	}
	documentHeaders, err := c.Documents(project, "", true)
	if err != nil {
		return err
	}
	documents := make([]mirror.PublishedDocument, 0, len(documentHeaders))
	for _, document := range documentHeaders {
		revision, err := c.DocumentRevision(document.ID, document.HeadRevision)
		if err != nil {
			return err
		}
		documents = append(documents, mirror.PublishedDocument{Document: document, Body: revision.Body})
	}
	open, _, err := requestPage(c, project, "open", 0)
	if err != nil {
		return err
	}
	done, doneTotal, err := requestPage(c, project, "done", doneRequestsShown)
	if err != nil {
		return err
	}

	// Wie voll der Baum daneben ist. Fehlschlagen darf das: eine Zahl weniger im
	// Index ist hinnehmbar, ein nicht geschriebener Spiegel nicht.
	described, paths := treeFill(c, project, repoRoot)

	docs := mirror.Build(mirror.Input{
		Project:       project,
		Machine:       c.Machine(),
		Knowledge:     knowledge,
		Documents:     documents,
		Requests:      append(open, done...),
		DoneShown:     len(done),
		DoneTotal:     doneTotal,
		TreeDescribed: described,
		TreePaths:     paths,
		At:            time.Now().UTC().Format(time.RFC3339),
	})
	return writeMirrorDocs(filepath.Join(repoRoot, ".ghosttree"), repoRoot, docs)
}

// treeFill zählt, zu wie vielen Pfaden des Repos eine Beschreibung vorliegt.
// Beide Zahlen sind 0, wenn eine davon nicht zu haben ist — eine halbe Angabe
// ("39 von 0") wäre schlechter als gar keine.
func treeFill(c *client.Client, project, repoRoot string) (described, paths int) {
	entries, err := ghost.RepoEntries(repoRoot)
	if err != nil {
		return 0, 0
	}
	stored, err := c.GhostTree(project, "")
	if err != nil {
		return 0, 0
	}
	known := make(map[string]bool, len(stored))
	for _, g := range stored {
		known[g.Path] = true
	}
	for _, e := range entries {
		paths++
		if known[e.Path] {
			described++
		}
	}
	return described, paths
}

// requestPage holt eine Zustandsseite des Ledgers. limit 0 heisst: alles, denn
// was offen ist, gehört vollständig in den Spiegel. Zurück kommt zusätzlich die
// Gesamtzahl, damit der Index sagen kann, was er zurückhält.
func requestPage(c *client.Client, project, state string, limit int) ([]requestdomain.SearchHit, int, error) {
	var out []requestdomain.SearchHit
	cursor := ""
	total := 0
	for {
		// FullDescription, weil der Spiegel das Dokument zeigt und nicht die
		// Trefferliste: eine Beschreibung, die nach 200 Zeichen mitten im Wort
		// endet, liest sich dort nicht als gekürzt, sondern als beschädigt.
		page, err := c.SearchRequests(requestdomain.SearchFilter{
			Scope: scope.Axes{Project: project}, State: state, Limit: 25, Cursor: cursor,
			FullDescription: true,
		})
		if err != nil {
			return nil, 0, err
		}
		for _, hit := range page.Results {
			total++
			if limit == 0 || len(out) < limit {
				out = append(out, hit)
			}
		}
		if page.NextCursor == "" {
			return out, total, nil
		}
		cursor = page.NextCursor
	}
}

// writeMirrorDocs verteilt die Dokumente auf ihre obersten Verzeichnisse und
// schreibt jedes davon vollständig neu. Ein Durchlauf über .ghosttree als
// Ganzes würde den Ghost-Baum mitlöschen, der dort ebenfalls liegt.
func writeMirrorDocs(root, repoRoot string, docs []mirror.Doc) error {
	if err := ghost.EnsureExcluded(repoRoot); err != nil {
		return err
	}
	byDir := map[string][]ghost.Doc{}
	for _, d := range docs {
		dir, rest := split(d.Path)
		byDir[dir] = append(byDir[dir], ghost.Doc{Path: rest, Body: d.Body})
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	for dir, group := range byDir {
		if dir == "" {
			for _, d := range group {
				if err := os.WriteFile(filepath.Join(root, d.Path), []byte(d.Body), 0o644); err != nil {
					return err
				}
			}
			continue
		}
		if err := ghost.Materialize(filepath.Join(root, dir), group); err != nil {
			return err
		}
	}
	return nil
}

// split trennt das oberste Verzeichnis vom Rest. Eine Datei direkt unter
// .ghosttree/ — INDEX.md — bekommt das leere Verzeichnis.
func split(path string) (string, string) {
	for i := 0; i < len(path); i++ {
		if path[i] == '/' {
			return path[:i], path[i+1:]
		}
	}
	return "", path
}
