package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/ghost"
	"github.com/Deadweight-Labs/ghosttree/internal/hookstate"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type sessionStartOutput struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// cmdHook always exits 0 with a well-formed payload: a dead ghosttree server
// must never block a session from starting, nor stand between a person pressing
// enter and the model reading what they typed.
func cmdHook(args []string, stdout io.Writer) int {
	return cmdHookWith(os.Stdin, args, stdout)
}

// cmdHookWith takes the harness payload as a reader so the hooks can be tested
// without a process boundary.
func cmdHookWith(stdin io.Reader, args []string, stdout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stdout, hookUsage)
		return 2
	}
	eventArg := args[0]
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	harness := fs.String("harness", "", "calling harness")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(stdout, hookUsage)
		return 2
	}
	var out sessionStartOutput
	switch eventArg {
	case "session-start":
		out.HookSpecificOutput.HookEventName = "SessionStart"
		out.HookSpecificOutput.AdditionalContext = bootstrapContext(stdin)
	case "user-prompt-submit":
		out.HookSpecificOutput.HookEventName = "UserPromptSubmit"
		out.HookSpecificOutput.AdditionalContext = relevantContext(stdin)
	case "pre-tool-use":
		out.HookSpecificOutput.HookEventName = "PreToolUse"
		out.HookSpecificOutput.AdditionalContext = ghostContext(stdin)
	default:
		fmt.Fprintln(stdout, hookUsage)
		return 2
	}
	if (*harness == "claude" || *harness == "codex") && os.Getenv("GHOSTTREE_HOOK_SYNTHETIC") != "1" {
		_ = hookstate.Record(*harness, out.HookSpecificOutput.HookEventName)
	}
	json.NewEncoder(stdout).Encode(out)
	return 0
}

const hookUsage = `usage: ctx hook session-start [--harness claude|codex]
       ctx hook user-prompt-submit [--harness claude|codex]
       ctx hook pre-tool-use [--harness claude|codex]`

// relevanceTimeout is short because this hook sits between the keystroke and
// the answer. Knowledge that arrives late is worse than knowledge that does not
// arrive: the first costs the person their flow, the second costs a search they
// can still make.
const relevanceTimeout = 900 * time.Millisecond

// relevantContext answers a single prompt with whatever the archive gives a
// reason to say about it, which is usually nothing.
func relevantContext(stdin io.Reader) string {
	var in struct {
		Prompt string `json:"prompt"`
		CWD    string `json:"cwd"`
	}
	json.NewDecoder(stdin).Decode(&in)
	if strings.TrimSpace(in.Prompt) == "" {
		return ""
	}
	cfg, err := config.Load()
	if err != nil {
		return ""
	}
	cwd := in.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	gitCtx := collector.ResolveGitContext(cwd)
	md, err := client.NewWithTimeout(cfg, relevanceTimeout).Relevant(in.Prompt,
		scope.Axes{Project: gitCtx.Project, Branch: gitCtx.Branch, Lineage: gitCtx.Lineage, Machine: cfg.Machine}, 0)
	if err != nil {
		return ""
	}
	return md
}

func bootstrapContext(stdin io.Reader) string {
	var in struct {
		CWD       string `json:"cwd"`
		SessionID string `json:"session_id"`
	}
	json.NewDecoder(stdin).Decode(&in)
	cfg, err := config.Load()
	if err != nil {
		return ""
	}
	cwd := in.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	gitCtx := collector.ResolveGitContext(cwd)
	md, err := client.New(cfg).Bootstrap(
		scope.Axes{Project: gitCtx.Project, Branch: gitCtx.Branch, Lineage: gitCtx.Lineage, Machine: cfg.Machine},
		activation.Context{RepoPath: gitCtx.RepoPath}, 0, in.SessionID)
	if err != nil {
		return ""
	}
	// Einmal je Session, damit ein Agent den Baum aktuell vorfindet, auch wenn
	// die letzte Beschreibung auf einer anderen Maschine entstand. Fehler
	// geschluckt: der Bootstrap darf an einer Projektion nicht scheitern.
	if home, err := os.UserHomeDir(); err == nil {
		_ = WriteTree(client.New(cfg), gitCtx.Project, gitCtx.Root, home)
	}
	// Derselbe Gedanke für Wissen, Dokumente und den Ledger: wer nachsehen will,
	// soll nicht erst ein Werkzeug aufrufen müssen — und auf einer Harness ohne
	// MCP und ohne Hooks ist das der einzige Kanal, der überhaupt trägt.
	_ = WriteMirror(client.New(cfg),
		scope.Axes{Project: gitCtx.Project, Branch: gitCtx.Branch, Lineage: gitCtx.Lineage, Machine: cfg.Machine},
		gitCtx.Root)
	return md
}

// writingTools sind die Werkzeuge, bei denen der Agent ohnehin gerade den Urge
// zum erklärenden Kommentar hat. Nur dort wird nach einer Beschreibung gefragt;
// wer bloss liest, wird nicht behelligt.
var writingTools = map[string]bool{"Edit": true, "Write": true, "NotebookEdit": true, "apply_patch": true}

const (
	maxMentionedTokens = 256
	maxPathWords       = 8
	maxGhostTargets    = 32
)

// ghostContext beantwortet einen Werkzeugaufruf mit dem, was über diesen Pfad
// bekannt ist. Wie der Prompt-Hook: kurzer Timeout, Schweigen im Zweifel, und
// niemals ein Fehler nach aussen.
func ghostContext(stdin io.Reader) string {
	var in struct {
		SessionID string          `json:"session_id"`
		CWD       string          `json:"cwd"`
		ToolName  string          `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
	}
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		return ""
	}
	cfg, err := config.Load()
	if err != nil {
		return ""
	}
	cwd := in.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	gitCtx := collector.ResolveGitContext(cwd)
	if gitCtx.Project == "" || gitCtx.Root == "" {
		return ""
	}
	targets := toolPaths(in.ToolInput, cwd, gitCtx.Root)
	if len(targets) == 0 {
		return ""
	}

	contexts := make([]string, len(targets))
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			contexts[i] = ghostContextForPath(cfg, gitCtx, in.SessionID, in.ToolName, target)
		}()
	}
	wg.Wait()
	return strings.Join(contexts, "\n")
}

func toolPaths(raw json.RawMessage, cwd, root string) []string {
	var direct struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
	}
	if err := json.Unmarshal(raw, &direct); err == nil {
		if direct.FilePath != "" {
			if rel, ok := repositoryRelativePath(direct.FilePath, cwd, root); ok {
				if !safeExistingPath(rel, root, false) {
					if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
						return nil
					}
				}
				return []string{rel}
			}
			return nil
		}
		if direct.NotebookPath != "" {
			if rel, ok := repositoryRelativePath(direct.NotebookPath, cwd, root); ok {
				if !safeExistingPath(rel, root, false) {
					if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
						return nil
					}
				}
				return []string{rel}
			}
			return nil
		}
	}

	var command struct {
		Command string `json:"command"`
	}
	var source string
	if err := json.Unmarshal(raw, &command); err == nil && command.Command != "" {
		source = command.Command
	} else if err := json.Unmarshal(raw, &source); err != nil {
		return nil
	}
	seen := map[string]bool{}
	var paths []string
	remainingTokens := maxMentionedTokens
	for _, scope := range codexExecScopes(source, cwd, root) {
		words := sourceWords(scope.source)
		if len(words) > remainingTokens {
			words = words[:remainingTokens]
		}
		remainingTokens -= len(words)
		phrases := pathPhrases(words)
		for _, candidate := range phrases {
			rel, ok := repositoryRelativePath(candidate, scope.base, root)
			if !ok || rel == ".ghosttree" || strings.HasPrefix(rel, ".ghosttree/") || seen[rel] {
				continue
			}
			if !safeExistingPath(rel, root, true) {
				continue
			}
			seen[rel] = true
			paths = append(paths, rel)
			if len(paths) == maxGhostTargets {
				return paths
			}
		}
		if remainingTokens == 0 {
			break
		}
	}
	return paths
}

type execScope struct {
	source string
	base   string
}

func codexExecScopes(source, cwd, root string) []execScope {
	var bases []string
	seen := map[string]bool{}
	for _, value := range keyedStringValues(source, "workdir") {
		base := value
		if !filepath.IsAbs(base) {
			base = filepath.Join(cwd, base)
		}
		base = filepath.Clean(base)
		if seen[base] || !canonicalPathInside(base, root) {
			continue
		}
		if info, err := os.Stat(base); err == nil && info.IsDir() {
			seen[base] = true
			bases = append(bases, base)
		}
	}
	if strings.Count(source, "exec_command") <= 1 && len(bases) <= 1 {
		base := cwd
		if len(bases) == 1 {
			base = bases[0]
		}
		return []execScope{{source: source, base: base}}
	}

	var scopes []execScope
	for start := strings.Index(source, "exec_command"); start >= 0; {
		rest := source[start+len("exec_command"):]
		next := strings.Index(rest, "exec_command")
		end := len(source)
		if next >= 0 {
			end = start + len("exec_command") + next
		}
		segment := source[start:end]
		commands := keyedStringValues(segment, "cmd")
		workdirs := keyedStringValues(segment, "workdir")
		if len(commands) > 0 {
			base := cwd
			if len(workdirs) > 0 {
				base = workdirs[0]
				if !filepath.IsAbs(base) {
					base = filepath.Join(cwd, base)
				}
				base = filepath.Clean(base)
			}
			if canonicalPathInside(base, root) {
				scopes = append(scopes, execScope{source: commands[0], base: base})
			}
		}
		if next < 0 {
			break
		}
		start = end
	}
	return scopes
}

func keyedStringValues(source, key string) []string {
	var values []string
	for offset := 0; offset < len(source); {
		rel := strings.Index(source[offset:], key)
		if rel < 0 {
			break
		}
		start := offset + rel
		endKey := start + len(key)
		if start > 0 && (source[start-1] == '_' || unicode.IsLetter(rune(source[start-1]))) {
			offset = endKey
			continue
		}
		pos := endKey
		for pos < len(source) && unicode.IsSpace(rune(source[pos])) {
			pos++
		}
		if pos >= len(source) || source[pos] != ':' {
			offset = endKey
			continue
		}
		pos++
		for pos < len(source) && unicode.IsSpace(rune(source[pos])) {
			pos++
		}
		value, end, ok := sourceStringAt(source, pos)
		if ok {
			values = append(values, value)
			offset = end
			continue
		}
		offset = endKey
	}
	return values
}

func sourceStringAt(source string, start int) (string, int, bool) {
	if start >= len(source) || source[start] != '\'' && source[start] != '"' && source[start] != '`' {
		return "", start, false
	}
	quote := source[start]
	var b strings.Builder
	escaped := false
	for i := start + 1; i < len(source); i++ {
		c := source[i]
		if escaped {
			b.WriteByte(c)
			escaped = false
			continue
		}
		if c == '\\' {
			escaped = true
			continue
		}
		if c == quote {
			return b.String(), i + 1, true
		}
		b.WriteByte(c)
	}
	return "", start, false
}

func sourceWords(source string) []string {
	return strings.FieldsFunc(source, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("._/-", r)
	})
}

func pathPhrases(words []string) []string {
	var phrases []string
	for end := range words {
		for count := 1; count <= maxPathWords && count <= end+1; count++ {
			phrases = append(phrases, strings.Join(words[end-count+1:end+1], " "))
		}
	}
	return phrases
}

func safeExistingPath(rel, root string, regular bool) bool {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Stat(abs)
	if err != nil || regular && !info.Mode().IsRegular() {
		return false
	}
	return canonicalPathInside(abs, root)
}

func canonicalPathInside(path, root string) bool {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(realRoot, realPath)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func repositoryRelativePath(target, cwd, root string) (string, bool) {
	abs := target
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(cwd, target)
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func ghostContextForPath(cfg config.Config, gitCtx collector.GitContext, sessionID, toolName, target string) string {
	abs := target
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(gitCtx.Root, target)
	}
	rel, err := filepath.Rel(gitCtx.Root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		// Ausserhalb des Repos: nicht unser Baum.
		return ""
	}
	rel = filepath.ToSlash(rel)
	if rel == ".ghosttree" || strings.HasPrefix(rel, ".ghosttree/") {
		return ""
	}

	// Gehasht wird für die Frische. Der Blob reiste hier einmal zum Server mit,
	// damit eine verschobene Datei ihre Beschreibung behält — das ist entfallen,
	// weil sich Verschiebung und Kopie ohne die Dateiliste nicht unterscheiden
	// lassen und eine Kopie deshalb die Beschreibung des Originals stahl
	// (REQ-179). Erkannt wird der Umzug jetzt beim Baumschreiben.
	sha, blob, lines, _ := ghost.HashFile(abs)

	sessionKey := "claude:" + sessionID
	entries, err := client.NewWithTimeout(cfg, relevanceTimeout).
		GhostsForPath(gitCtx.Project, rel, sessionKey)
	if err != nil {
		return ""
	}

	// Auch die Vorfahren werden bewertet, nicht nur der angefasste Pfad. Sie
	// kosten je Ebene einen Verzeichnisdurchlauf — bei drei bis vier Ebenen
	// weit unter dem Zeitbudget dieses Hooks. Ungeprüft ausgeliefert waren sie
	// die gefährlichste Auskunft des ganzen Systems: eine
	// Verzeichnisbeschreibung kommt bei JEDER Datei darunter mit, und eine, die
	// nicht mehr stimmt, las sich wie eine, die stimmt.
	fresh := map[string]ghost.Freshness{}
	describedTarget := false
	for _, g := range entries {
		if g.Path == rel {
			describedTarget = true
		}
		if g.Kind == "dir" {
			names, err := ghost.ChildNames(filepath.Join(gitCtx.Root, filepath.FromSlash(g.Path)))
			if err != nil {
				continue
			}
			fresh[g.Path] = ghost.JudgeDir(g.ContentSHA, ghost.HashDir(names))
			continue
		}
		if g.Path != rel {
			continue
		}
		changed, reachable := ghost.ChangedLines(gitCtx.Root, g.GitBlob, blob)
		of := lines
		if g.LineCount > of {
			of = g.LineCount
		}
		fresh[g.Path] = ghost.Judge(g.ContentSHA, sha, changed, of, reachable)
	}

	// Ein Pfad fehlt in der Auslieferung aus ZWEI Gründen: es gibt nichts, oder
	// es wurde in dieser Sitzung schon gesagt. Die beiden zu verwechseln ist
	// derselbe Fehlschluss wie bei null Suchtreffern (#732) — und er ist hier
	// teurer: beim zweiten Ändern derselben Datei behauptete der Hook, sie habe
	// keine Beschreibung, und forderte auf, eine zu schreiben. Die hätte die
	// vorhandene ersetzt, denn PutGhostFile ist ein Upsert ohne Historie.
	//
	// Deshalb wird beim Schreiben nachgesehen, statt aus dem Schweigen zu
	// schliessen. Eine zweite Runde, aber nur bei Schreibwerkzeugen und nur,
	// wenn der Pfad nicht ohnehin schon in der Auslieferung stand.
	if writingTools[toolName] && !describedTarget {
		if g, ok := describedNow(cfg, gitCtx.Project, rel); ok {
			describedTarget = true
			changed, reachable := ghost.ChangedLines(gitCtx.Root, g.GitBlob, blob)
			of := lines
			if g.LineCount > of {
				of = g.LineCount
			}
			fresh[rel] = ghost.Judge(g.ContentSHA, sha, changed, of, reachable)
		}
	}
	// Die Zahl der Vorfassungen, nicht ihr Text. Nur wenn der Pfad selbst
	// ausgeliefert wird — sonst hinge der Hinweis an einer Beschreibung, die
	// gar nicht dasteht.
	priorVersions := 0
	if describedTarget && len(entries) > 0 {
		if n, err := client.NewWithTimeout(cfg, relevanceTimeout).
			GhostHistoryCount(gitCtx.Project, rel); err == nil {
			priorVersions = n
		}
	}
	return renderGhostDelivery(entries, fresh, writingTools[toolName], describedTarget, rel, priorVersions)
}

func pluralVersions(n int) string {
	if n == 1 {
		return "1 earlier version"
	}
	return fmt.Sprintf("%d earlier versions", n)
}

// describedNow fragt gezielt nach einem einzelnen Pfad, ohne die
// Einmal-je-Sitzung-Buchführung zu berühren. Schweigt bei jedem Fehler: die
// Auskunft ist eine Zugabe, sie darf einen Werkzeugaufruf nicht aufhalten.
func describedNow(cfg config.Config, project, rel string) (store.GhostFile, bool) {
	got, err := client.NewWithTimeout(cfg, relevanceTimeout).GhostTree(project, rel)
	if err != nil {
		return store.GhostFile{}, false
	}
	for _, g := range got {
		if g.Path == rel {
			return g, true
		}
	}
	return store.GhostFile{}, false
}

func renderGhostDelivery(entries []store.GhostFile, fresh map[string]ghost.Freshness, writing, describedTarget bool, target string, priorVersions int) string {
	var b strings.Builder
	if len(entries) > 0 {
		b.WriteString("## What is known about this path\n\n")
		for _, g := range entries {
			name := g.Path
			if name == "" {
				name = "(Repo-Wurzel)"
			}
			fmt.Fprintf(&b, "### %s", name)
			if label := fresh[g.Path].Label(); label != "" {
				fmt.Fprintf(&b, " — %s", label)
			}
			b.WriteString("\n")
			b.WriteString(strings.TrimRight(g.Description, "\n"))
			b.WriteString("\n\n")
			// Nur die Zahl, nicht der Text. Wer die Vorfassungen braucht, holt
			// sie gezielt; sie ungefragt mitzuliefern wäre genau das
			// Kontextrauschen, gegen das die Entdopplung antritt.
			if g.Path == target && priorVersions > 0 {
				fmt.Fprintf(&b, "(%s — `context_file_history` if the current description no longer fits)\n\n",
					pluralVersions(priorVersions))
			}
		}
	}
	if writing {
		switch {
		case !describedTarget:
			fmt.Fprintf(&b, "This path has no description. If your change would produce an explanatory comment, write it to %s with `context_describe_file` instead.\n", target)
		case fresh[target].State == "stale" || fresh[target].State == "unknown":
			fmt.Fprintf(&b, "The description of %s is out of date. Update it after your change with `context_describe_file`.\n", target)
		}
	}
	return b.String()
}
