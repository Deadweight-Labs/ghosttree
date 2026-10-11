// Package garden is the knowledge gardener: it checks what ghosttree knows
// against the code and proposes upkeep. It proposes, a person decides.
package garden

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Entry is the part of a knowledge entry the gardener looks at.
type Entry struct {
	ID         int64
	Type       string
	Title      string
	Body       string
	ObservedAt string
	CreatedAt  string
}

func (e Entry) Since() string {
	if e.ObservedAt != "" {
		return e.ObservedAt
	}
	return e.CreatedAt
}

// CodeContext is what the model gets to see besides the entry. Everything in
// it comes from the checkout by deterministic search, so a citation in the
// verdict can be checked against Text afterwards.
type CodeContext struct {
	Files   []string
	Symbols []string
	Commits int
	Text    string
}

// HasRefs says whether the entry names anything that can be looked up. An entry
// that names nothing cannot be judged against code, and asking a model anyway
// invites an invented answer.
func (c CodeContext) HasRefs() bool { return len(c.Files)+len(c.Symbols) > 0 }

const (
	maxFiles          = 6
	maxSymbols        = 8
	maxHitLinesPerSym = 24
	maxLogPerQuery    = 12
	maxSnippetFileLns = 30
)

var (
	pathRe   = regexp.MustCompile(`[A-Za-z0-9_][A-Za-z0-9_./-]*\.(?:go|ts|tsx|js|svelte|sql|json|ya?ml|toml|sh|css|html|py|md)\b|(?:[A-Za-z0-9_-]+/){1,}[A-Za-z0-9_.-]*`)
	tickRe   = regexp.MustCompile("`([^`\n]{3,80})`")
	camelRe  = regexp.MustCompile(`\b[A-Z][a-z0-9]+(?:[A-Z][A-Za-z0-9]*)+\b`)
	flagRe   = regexp.MustCompile(`--[a-z][a-z0-9-]{3,}`)
	identRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]{3,}$`)
	hashRe   = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	stopword = map[string]bool{"GitHub": true, "JavaScript": true, "TypeScript": true, "SQLite": true, "PostgreSQL": true, "ChatGPT": true}
)

// Repo wraps a checkout. The tracked file list is read once.
type Repo struct {
	Root  string
	files []string
}

func OpenRepo(root string) (*Repo, error) {
	r := &Repo{Root: root}
	out, err := r.git("ls-files")
	if err != nil {
		return nil, fmt.Errorf("%s is not a git checkout: %w", root, err)
	}
	for _, l := range strings.Split(out, "\n") {
		if l != "" && !strings.HasPrefix(l, ".ghosttree/") {
			r.files = append(r.files, l)
		}
	}
	return r, nil
}

func (r *Repo) git(args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", r.Root}, args...)...)
	out, err := cmd.Output()
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 && len(out) == 0 {
		return "", nil // grep without a match
	}
	return string(out), err
}

// Refs extracts tracked file paths and symbol names an entry mentions.
func (r *Repo) Refs(e Entry) (files, symbols []string) {
	text := e.Title + "\n" + e.Body
	seenF, seenS := map[string]bool{}, map[string]bool{}
	addFile := func(f string) {
		if !seenF[f] && len(files) < maxFiles {
			seenF[f] = true
			files = append(files, f)
		}
	}
	for _, tok := range pathRe.FindAllString(text, -1) {
		tok = strings.Trim(tok, "./-")
		if tok == "" {
			continue
		}
		for _, f := range r.resolve(tok) {
			addFile(f)
		}
	}
	addSym := func(s string) {
		s = strings.TrimSuffix(strings.TrimSpace(s), "()")
		if s == "" || seenS[s] || len(symbols) >= maxSymbols || stopword[s] || hashRe.MatchString(s) {
			return
		}
		seenS[s] = true
		symbols = append(symbols, s)
	}
	for _, m := range tickRe.FindAllStringSubmatch(text, -1) {
		tok := strings.TrimSuffix(m[1], "()")
		if strings.Contains(tok, "/") || strings.Contains(tok, " ") {
			for _, f := range r.resolve(strings.Trim(tok, "./")) {
				addFile(f)
			}
			continue
		}
		if r.isFileName(tok) {
			for _, f := range r.resolve(tok) {
				addFile(f)
			}
			continue
		}
		if identRe.MatchString(tok) {
			addSym(tok)
		}
	}
	for _, s := range camelRe.FindAllString(text, -1) {
		addSym(s)
	}
	for _, s := range flagRe.FindAllString(text, -1) {
		addSym(s)
	}
	return files, symbols
}

func (r *Repo) isFileName(tok string) bool {
	return path.Ext(tok) != "" && !strings.ContainsAny(tok, " ()")
}

// resolve maps a mentioned path onto tracked files: an exact path, a path
// suffix, a directory prefix, or a unique base name.
func (r *Repo) resolve(tok string) []string {
	var out []string
	var base []string
	for _, f := range r.files {
		switch {
		case f == tok || strings.HasSuffix(f, "/"+tok):
			out = append(out, f)
		case strings.Contains(tok, "/") && !strings.Contains(path.Base(tok), ".") && strings.HasPrefix(f, strings.TrimSuffix(tok, "/")+"/"):
			if len(out) < 3 {
				out = append(out, f)
			}
		case !strings.Contains(tok, "/") && path.Base(f) == tok:
			base = append(base, f)
		}
	}
	if len(out) == 0 && len(base) > 0 && len(base) <= 3 {
		out = base
	}
	sort.Strings(out)
	return out
}

// Gather builds the code context for one entry: where the symbols occur now,
// the head of the named files, and the commits touching them since the entry
// was observed. maxChars caps the rendered text.
func (r *Repo) Gather(e Entry, maxChars int) CodeContext {
	files, symbols := r.Refs(e)
	cc := CodeContext{Files: files, Symbols: symbols}
	var b strings.Builder
	touched := append([]string{}, files...)
	seenTouched := map[string]bool{}
	for _, f := range touched {
		seenTouched[f] = true
	}

	for _, f := range files {
		fmt.Fprintf(&b, "## Datei %s (Anfang)\n", f)
		b.WriteString(r.head(f, maxSnippetFileLns))
		b.WriteString("\n")
	}
	for _, s := range symbols {
		out, _ := r.git("grep", "-n", "-I", "-F", "-w", "-C", "1", "-e", s, "--", ".", ":(exclude).ghosttree", ":(exclude)*.md")
		if strings.TrimSpace(out) == "" {
			fmt.Fprintf(&b, "## Symbol %s: KEIN Treffer im aktuellen Checkout\n\n", s)
			continue
		}
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if len(lines) > maxHitLinesPerSym {
			lines = lines[:maxHitLinesPerSym]
		}
		fmt.Fprintf(&b, "## Symbol %s: Treffer (Datei:Zeile)\n%s\n\n", s, strings.Join(lines, "\n"))
		for _, l := range lines {
			if i := strings.IndexAny(l, ":"); i > 0 {
				f := l[:i]
				if !seenTouched[f] && len(touched) < maxFiles+4 && r.tracked(f) {
					seenTouched[f] = true
					touched = append(touched, f)
				}
			}
		}
	}

	since := e.Since()
	commits := map[string]bool{}
	var log []string
	addLog := func(out string) {
		for _, l := range strings.Split(out, "\n") {
			if l != "" && !commits[l] {
				commits[l] = true
				log = append(log, l)
			}
		}
	}
	args := []string{"log", "--date=short", "--format=%h %ad %s", "-n", fmt.Sprint(maxLogPerQuery)}
	if since != "" {
		args = append(args, "--since="+since)
	}
	if len(touched) > 0 {
		out, _ := r.git(append(append([]string{}, args...), append([]string{"--"}, touched...)...)...)
		addLog(out)
	}
	for i, s := range symbols {
		if i >= 3 {
			break
		}
		out, _ := r.git(append(append([]string{}, args...), "-S"+s, "-n", "5")...)
		addLog(out)
	}
	if len(log) == 0 {
		fmt.Fprintf(&b, "## Commits seit %s an diesen Pfaden/Symbolen: keine\n", since)
	} else {
		fmt.Fprintf(&b, "## Commits seit %s (Hash Datum Betreff)\n%s\n", since, strings.Join(log, "\n"))
	}
	cc.Commits = len(log)
	cc.Text = b.String()
	if maxChars > 0 && len(cc.Text) > maxChars {
		cc.Text = cc.Text[:maxChars] + "\n[Kontext gekürzt]\n"
	}
	return cc
}

func (r *Repo) tracked(f string) bool {
	for _, t := range r.files {
		if t == f {
			return true
		}
	}
	return false
}

func (r *Repo) head(f string, n int) string {
	raw, err := os.ReadFile(filepath.Join(r.Root, f))
	if err != nil {
		return ""
	}
	lines := strings.Split(string(raw), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	var b strings.Builder
	for i, l := range lines {
		if len(l) > 200 {
			l = l[:200]
		}
		fmt.Fprintf(&b, "%s:%d:%s\n", f, i+1, l)
	}
	return b.String()
}
