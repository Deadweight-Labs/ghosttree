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
	UpdatedAt  string
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
	// Lines and Hashes are what Text lets a verdict cite: file:line pairs on
	// its lines and the full-length commit hashes of its log.
	Lines  map[string]bool
	Hashes []string
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
	if ee, ok := err.(*exec.ExitError); ok {
		// Exit 1 without output is how git grep says "no match". Any other
		// failure is a failure to look, and must not read as "not found".
		if len(args) > 0 && args[0] == "grep" && ee.ExitCode() == 1 && len(out) == 0 && len(ee.Stderr) == 0 {
			return "", nil
		}
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(ee.Stderr)))
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

// Gather builds the code context for one entry: the commits touching the named
// paths and symbols since the entry was observed, where the symbols occur now,
// and the head of the named files. The sections are budgeted in that order, so
// a long grep never crowds the commit list out, and every cut is at a line
// boundary.
//
// A failing git command is an error, never an empty result: "no match" and
// "could not look" must not read the same, or a broken checkout produces a
// stale verdict for every entry.
func (r *Repo) Gather(e Entry, maxChars int) (CodeContext, error) {
	files, symbols := r.Refs(e)
	cc := CodeContext{Files: files, Symbols: symbols}
	touched := append([]string{}, files...)
	seenTouched := map[string]bool{}
	for _, f := range touched {
		seenTouched[f] = true
	}

	var fileSecs, symSecs []string
	for _, f := range files {
		head, err := r.head(f, maxSnippetFileLns)
		if err != nil {
			return cc, err
		}
		if head != "" {
			fileSecs = append(fileSecs, fmt.Sprintf("## Datei %s (Anfang)\n%s", f, head))
		}
	}
	for _, s := range symbols {
		out, err := r.git("grep", "-n", "-I", "-F", "-w", "-C", "1", "-e", s, "--", ".", ":(exclude).ghosttree", ":(exclude)*.md")
		if err != nil {
			return cc, fmt.Errorf("git grep %q: %w", s, err)
		}
		if strings.TrimSpace(out) == "" {
			symSecs = append(symSecs, fmt.Sprintf("## Symbol %s: KEIN Treffer im aktuellen Checkout\n", s))
			continue
		}
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if len(lines) > maxHitLinesPerSym {
			lines = lines[:maxHitLinesPerSym]
		}
		symSecs = append(symSecs, fmt.Sprintf("## Symbol %s: Treffer (Datei:Zeile)\n%s\n", s, strings.Join(lines, "\n")))
		for _, l := range lines {
			if m := hitRe.FindStringSubmatch(l); m != nil {
				if f := m[1]; !seenTouched[f] && len(touched) < maxFiles+4 && r.tracked(f) {
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
	base := []string{"log", "--date=short", "--abbrev=12", "--format=%h %ad %s", "-n", fmt.Sprint(maxLogPerQuery)}
	if since != "" {
		base = append(base, "--since="+since)
	}
	if len(touched) > 0 {
		out, err := r.git(append(append([]string{}, base...), append([]string{"--"}, touched...)...)...)
		if err != nil {
			return cc, fmt.Errorf("git log: %w", err)
		}
		addLog(out)
	}
	for i, s := range symbols {
		if i >= 3 {
			break
		}
		out, err := r.git(append(append([]string{}, base...), "-S"+s)...)
		if err != nil {
			return cc, fmt.Errorf("git log -S %q: %w", s, err)
		}
		addLog(out)
	}
	commitSec := fmt.Sprintf("## Commits seit %s an diesen Pfaden/Symbolen: keine\n", since)
	if len(log) > 0 {
		commitSec = fmt.Sprintf("## Commits seit %s (Hash Datum Betreff)\n%s\n", since, strings.Join(log, "\n"))
	}
	cc.Commits = len(log)

	if maxChars <= 0 {
		maxChars = 1 << 30
	}
	commitText := cutLines(commitSec, maxChars/3)
	rest := maxChars - len(commitText)
	symText := fitSections(symSecs, rest*2/3)
	fileText := fitSections(fileSecs, rest-len(symText))
	cc.Text = commitText + "\n" + symText + fileText
	cc.Lines, cc.Hashes = citable(cc.Text)
	return cc, nil
}

var (
	hitRe    = regexp.MustCompile(`^(.+?):\d+:`)
	colonRe  = regexp.MustCompile(`^(\S+?):(\d+):`)
	dashRe   = regexp.MustCompile(`^(\S+?)-(\d+)-`)
	hashLine = regexp.MustCompile(`^([0-9a-f]{12}) \d{4}-\d{2}-\d{2} `)
)

// citable lists what a verdict may cite: file:line pairs that appear on a line
// of the context, and the commit hashes of its log. Anything else is not
// evidence, however plausible.
func citable(text string) (lines map[string]bool, hashes []string) {
	lines = map[string]bool{}
	for _, l := range strings.Split(text, "\n") {
		if m := hashLine.FindStringSubmatch(l); m != nil {
			hashes = append(hashes, m[1])
			continue
		}
		if m := colonRe.FindStringSubmatch(l); m != nil {
			lines[m[1]+":"+m[2]] = true
		} else if m := dashRe.FindStringSubmatch(l); m != nil {
			lines[m[1]+":"+m[2]] = true
		}
	}
	return lines, hashes
}

// cutLines keeps whole lines up to max bytes and says so when it dropped any.
// Cutting at a newline also keeps a multi-byte character from being split.
func cutLines(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := strings.LastIndex(s[:max], "\n")
	if cut < 0 {
		cut = 0
	}
	return s[:cut] + "\n[gekürzt]\n"
}

// fitSections adds whole sections while they fit, truncates the one that does
// not at a line boundary, and notes how many were left out.
func fitSections(secs []string, budget int) string {
	var b strings.Builder
	for i, s := range secs {
		if b.Len()+len(s) <= budget {
			b.WriteString(s)
			b.WriteString("\n")
			continue
		}
		if room := budget - b.Len(); room > 200 {
			b.WriteString(cutLines(s, room))
			i++
		}
		if left := len(secs) - i; left > 0 {
			fmt.Fprintf(&b, "[%d weitere Abschnitte ausgelassen]\n", left)
		}
		break
	}
	return b.String()
}

func (r *Repo) tracked(f string) bool {
	for _, t := range r.files {
		if t == f {
			return true
		}
	}
	return false
}

// head returns the first n lines of a tracked regular file. A symlink is
// skipped: it can point outside the checkout, and what it points at is not
// the repository's content.
func (r *Repo) head(f string, n int) (string, error) {
	p := filepath.Join(r.Root, f)
	info, err := os.Lstat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil // tracked but deleted in the working tree
		}
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", nil
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(raw), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	var b strings.Builder
	for i, l := range lines {
		if len(l) > 200 {
			l = strings.ToValidUTF8(l[:200], "")
		}
		fmt.Fprintf(&b, "%s:%d:%s\n", f, i+1, l)
	}
	return b.String(), nil
}
