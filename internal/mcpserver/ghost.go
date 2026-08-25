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
	Path string `json:"path" jsonschema:"repository-relative path of the file or directory this describes; \".\" for the repository root"`
	// omitempty ist hier keine Kosmetik. Ohne es steht das Feld als Pflichtfeld
	// im generierten Schema, und der nothing_to_say-Aufruf, den die
	// Werkzeugbeschreibung verspricht, würde vom SDK abgewiesen, bevor der
	// Handler ihn sieht — derselbe Fehler, der context_search einmal vier grüne
	// Tests und ein für Agenten unbenutzbares Werkzeug bescherte (#846). Dass
	// eins von beiden dasein muss, prüft deshalb der Handler.
	Description  string `json:"description,omitempty" jsonschema:"what this file or directory does — purpose, invariants, what went wrong here before, what it hangs together with. No length limit; this is the text that would otherwise become a header comment"`
	NothingToSay bool   `json:"nothing_to_say,omitempty" jsonschema:"set this instead of description when you have read the path and there is nothing to record that is not already in the code. It notes that the path was looked at, bound to the current contents, so later passes skip it until the file changes. Preferable to a description that restates the source: an empty entry costs nothing, a restatement costs trust in every other description"`
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
	hasText := strings.TrimSpace(in.Description) != ""
	switch {
	case !hasText && !in.NothingToSay:
		return nil, nil, fmt.Errorf("either description or nothing_to_say is required — " +
			"the description is the text that would otherwise become a comment")
	case hasText && in.NothingToSay:
		// Beides zugleich meint zwei verschiedene Dinge. Stillschweigend eins zu
		// wählen hiesse, entweder eine Beschreibung zu verwerfen oder einen
		// Review zu erfinden.
		return nil, nil, fmt.Errorf("description and nothing_to_say are mutually exclusive")
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

	if in.NothingToSay {
		// Nur für Dateien. Ein Verzeichnis hat keinen Blob, an den die
		// Entscheidung sich binden liesse, und "nichts zu sagen" ist dort auch
		// selten wahr: was ein Verzeichnis zusammenhält, steht in keiner seiner
		// Dateien.
		if info.IsDir() {
			return nil, nil, fmt.Errorf("nothing_to_say is for files; a directory has no blob to bind the decision to, " +
				"and what holds a directory together is in none of its files")
		}
		_, blob, _, err := ghost.HashFile(full)
		if err != nil {
			return nil, nil, err
		}
		if err := s.client.PutGhostReview(store.GhostReview{
			Project: s.ctxAxes.Project, Path: rel, GitBlob: blob}); err != nil {
			return nil, nil, err
		}
		if s.afterWrite != nil {
			s.afterWrite()
		}
		return textResult(fmt.Sprintf("vermerkt: %s — angesehen, nichts zu sagen", rel)), nil, nil
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
	Limit int    `json:"limit,omitempty" jsonschema:"how many earlier versions to consider, newest first; omit for all"`
	Full  bool   `json:"full,omitempty" jsonschema:"return each earlier version in full instead of what changed between them; costs far more context, so ask for it only when the wording of an old version is the point"`
}

func (s *Server) handleFileHistory(ctx context.Context, _ *mcp.CallToolRequest, in HistoryInput) (*mcp.CallToolResult, any, error) {
	rel, err := normalizeGhostPath(in.Path)
	if err != nil {
		return nil, nil, err
	}
	// Die Kette statt der blossen Historie: der Nachfolger der neuesten
	// abgelösten Fassung ist die Beschreibung, die heute gilt, und ohne sie
	// gäbe es bei genau einer Vorfassung nichts zu vergleichen.
	chain, err := s.client.GhostChain(s.ctxAxes.Project, rel, in.Limit)
	if err != nil {
		return nil, nil, err
	}
	return textResult(renderHistory(rel, chain, in.Full)), nil, nil
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
