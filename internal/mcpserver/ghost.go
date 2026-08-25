package mcpserver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/ghost"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type DescribeInput struct {
	Path        string `json:"path" jsonschema:"repository-relative path of the file or directory this describes; \".\" for the repository root"`
	Description string `json:"description" jsonschema:"what this file or directory does — purpose, invariants, what went wrong here before, what it hangs together with. No length limit; this is the text that would otherwise become a header comment"`
}

// normalizeGhostPath nimmt entgegen, was ein Agent tippt, und lässt nur zu, was
// im Repo liegt. Dieselbe Prüfung wie activation.normalizeRel, plus das
// Verbot, in den Ghost-Baum selbst zu schreiben.
func normalizeGhostPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" || p == "." {
		return "", nil
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, `\`) {
		return "", fmt.Errorf("path must be repository-relative and slash-normalized: %q", p)
	}
	clean := path.Clean(p)
	if clean == "." {
		return "", nil
	}
	for _, part := range strings.Split(clean, "/") {
		if part == ".." {
			return "", fmt.Errorf("path escapes the repository: %q", p)
		}
	}
	if clean == ".ghosttree" || strings.HasPrefix(clean, ".ghosttree/") {
		return "", fmt.Errorf("%q is the ghost tree itself, not something to describe", p)
	}
	return clean, nil
}

func (s *Server) handleDescribe(ctx context.Context, _ *mcp.CallToolRequest, in DescribeInput) (*mcp.CallToolResult, any, error) {
	if strings.TrimSpace(in.Description) == "" {
		return nil, nil, fmt.Errorf("description is required — this is the text that would otherwise become a comment")
	}
	rel, err := normalizeGhostPath(in.Path)
	if err != nil {
		return nil, nil, err
	}
	if s.repoRoot == "" {
		return nil, nil, fmt.Errorf("this session is not inside a repository, so there is no path to attach a description to")
	}
	full := filepath.Join(s.repoRoot, filepath.FromSlash(rel))
	info, err := os.Stat(full)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot describe %q: %w", in.Path, err)
	}
	if !info.IsDir() {
		if err := trackedInRepo(s.repoRoot, rel); err != nil {
			return nil, nil, err
		}
	}

	g := store.GhostFile{Project: s.ctxAxes.Project, Path: rel, Description: in.Description, Harness: "mcp"}
	if info.IsDir() {
		names, err := ghost.ChildNames(full)
		if err != nil {
			return nil, nil, err
		}
		g.Kind, g.ContentSHA = "dir", ghost.HashDir(names)
	} else {
		if binary, err := looksBinary(full); err != nil {
			return nil, nil, err
		} else if binary {
			return nil, nil, fmt.Errorf("%q is a binary file; a description would have nothing to describe", in.Path)
		}
		sha, blob, lines, err := ghost.HashFile(full)
		if err != nil {
			return nil, nil, err
		}
		g.Kind, g.ContentSHA, g.GitBlob, g.LineCount = "file", sha, blob, lines
	}

	if _, err := s.client.PutGhost(g); err != nil {
		return nil, nil, err
	}
	msg := fmt.Sprintf("beschrieben: %s [%s]", rel, g.Kind)
	if hint := undescribedAncestor(s, rel); hint != "" {
		msg += "\n" + hint
	}
	if s.afterWrite != nil {
		s.afterWrite()
	}
	return textResult(msg), nil, nil
}

// trackedInRepo weist ab, was `git ls-files` nicht kennt.
//
// Der Baum wird aus genau dieser Liste gebaut. Eine Beschreibung für etwas, das
// dort nicht vorkommt, landet in der Datenbank, wird vom Hook ausgeliefert und
// ist im Baum nirgends zu finden — jemand hätte sie geschrieben und nie
// wiedergefunden. Beim Schreiben nein zu sagen ist ehrlicher als still zu
// verlieren.
//
// Verzeichnisse sind ausgenommen: git führt keine Verzeichnisse, und der Baum
// leitet sie aus den Pfaden ihrer Dateien ab.
func trackedInRepo(repoRoot, rel string) error {
	if rel == "" {
		return nil
	}
	err := exec.Command("git", "-C", repoRoot, "ls-files", "--error-unmatch", "--", rel).Run()
	if err == nil {
		return nil
	}
	return fmt.Errorf("%q ist nicht versioniert — der Ghost-Baum hat die Form von `git ls-files`, "+
		"eine Beschreibung dafür wäre nirgends wiederzufinden. Beschreibe den Pfad, sobald er im Repo ist", rel)
}

// firstMentionOf sagt, ob dieser Pfad in dieser Sitzung schon genannt wurde,
// und merkt ihn dabei vor.
//
// Der Zustand liegt im Prozess und nicht in der Datenbank, weil der MCP-Server
// die Sitzung IST: er startet mit ihr und endet mit ihr. Der PreToolUse-Hook
// braucht dafür ghost_deliveries, weil dort je Werkzeugaufruf ein eigener,
// kurzlebiger Prozess läuft — hier wäre dieselbe Runde über den Server nur
// Netzverkehr für eine Frage, die dieser Prozess selbst beantworten kann.
//
// Unter dem Schloss, weil das go-sdk Werkzeugaufrufe asynchron behandelt
// (jsonrpc2.Async in server.go): zwei handleDescribe können gleichzeitig
// laufen, und eine ungeschützte Map darunter ist kein theoretisches Rennen,
// sondern ein "concurrent map writes"-Absturz mitten in einer Sitzung.
func (s *Server) firstMentionOf(path string) bool {
	s.mentionedMu.Lock()
	defer s.mentionedMu.Unlock()
	if s.mentioned == nil {
		s.mentioned = map[string]bool{}
	}
	if s.mentioned[path] {
		return false
	}
	s.mentioned[path] = true
	return true
}

// undescribedAncestor stupst den Baum von unten nach oben voll. Wer gerade eine
// Datei beschrieben hat, weiss meistens auch, was ihr Verzeichnis tut.
//
// Jeder Pfad wird höchstens einmal je Sitzung genannt. Ohne das antwortete beim
// Beschreiben von fünfzehn Dateien am Stück jede einzelne mit demselben Satz —
// genau das Rauschen, gegen das die Auslieferung mit ihrer Einmal-je-Sitzung-
// Regel antritt.
func undescribedAncestor(s *Server, rel string) string {
	tree, err := s.client.GhostTree(s.ctxAxes.Project, "")
	if err != nil {
		return ""
	}
	have := map[string]bool{}
	for _, g := range tree {
		have[g.Path] = true
	}
	parents := store.ParentPaths(rel)
	for i := len(parents) - 1; i >= 0; i-- {
		if !have[parents[i]] {
			if !s.firstMentionOf(parents[i]) {
				return ""
			}
			name := parents[i]
			if name == "" {
				name = "die Repo-Wurzel"
			}
			return fmt.Sprintf("hinweis: %s hat noch keine Beschreibung", name)
		}
	}
	return ""
}

// looksBinary prüft die ersten 8 KB auf ein Nullbyte — dasselbe grobe Kriterium,
// das git benutzt. Gut genug, um Bilder und Binärdateien abzuweisen.
func looksBinary(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	buf := make([]byte, 8000)
	n, _ := f.Read(buf)
	for _, b := range buf[:n] {
		if b == 0 {
			return true, nil
		}
	}
	return false, nil
}

type HistoryInput struct {
	Path  string `json:"path" jsonschema:"repository-relative path whose earlier descriptions to read; \".\" for the repository root"`
	Limit int    `json:"limit,omitempty" jsonschema:"how many versions to return, newest first; omit for all"`
}

func (s *Server) handleFileHistory(ctx context.Context, _ *mcp.CallToolRequest, in HistoryInput) (*mcp.CallToolResult, any, error) {
	rel, err := normalizeGhostPath(in.Path)
	if err != nil {
		return nil, nil, err
	}
	versions, err := s.client.GhostHistory(s.ctxAxes.Project, rel, in.Limit)
	if err != nil {
		return nil, nil, err
	}
	name := rel
	if name == "" {
		name = "(Repo-Wurzel)"
	}
	if len(versions) == 0 {
		return textResult(fmt.Sprintf("%s: keine früheren Fassungen — die aktuelle Beschreibung ist die erste.", name)), nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## Frühere Fassungen von %s\n\n", name)
	// Voller Text je Fassung, nicht geplättet: wer hier fragt, will genau das
	// lesen, was einmal dastand. Die Trefferlisten sind der Ort für Einzeiler.
	for _, v := range versions {
		fmt.Fprintf(&b, "### %s bis %s", shortDate(v.DescribedAt), shortDate(v.ReplacedAt))
		if v.Person != "" {
			fmt.Fprintf(&b, ", von %s", v.Person)
		}
		if v.Reason != "" && v.Reason != "ersetzt" {
			fmt.Fprintf(&b, " [%s]", v.Reason)
		}
		if v.LineCount > 0 {
			fmt.Fprintf(&b, " — beschriebener Stand: %d Zeilen", v.LineCount)
		}
		b.WriteString("\n")
		b.WriteString(strings.TrimRight(v.Description, "\n"))
		b.WriteString("\n\n")
	}
	return textResult(b.String()), nil, nil
}

func shortDate(ts string) string {
	if len(ts) >= 10 {
		return ts[:10]
	}
	return ts
}

// renderGhostHit ist eine Zeile je Treffer, wie bei den übrigen Trefferarten.
// oneLine plättet den Body: zwanzig mehrzeilige Beschreibungen in einer
// Trefferliste sind keine Liste mehr.
func renderGhostHit(g store.GhostFile) string {
	body := oneLine(g.Description)
	if len([]rune(body)) > snippetChars {
		body = string([]rune(body)[:snippetChars]) + "…"
	}
	name := g.Path
	if name == "" {
		name = "(Repo-Wurzel)"
	}
	return fmt.Sprintf("- %s [%s] — %s\n", name, g.Kind, body)
}
