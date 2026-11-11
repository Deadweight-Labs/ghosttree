// Package gitcmd erkennt in einem Shell-Befehl, den ein Agent ausgeführt hat,
// die Git-Vorgänge, die für die Zuordnung einer Session zu Commits und PRs
// zählen: ein Commit (und was sonst Commits erzeugt), ein Push, gh pr create
// und gh pr merge. Dazu liest es aus der Ausgabe, was sie verrät.
//
// Das Paket arbeitet nur auf Text und kennt weder Transkripte noch Git.
// Erkannt wird ein AUFRUF, nicht ein Wort: "git commit" in einem Auftrag an
// einen Sub-Agenten, in einem echo, in einem Heredoc oder in einem Python-Skript
// ist kein Commit. Der Befehl muss am Anfang eines einfachen Befehls stehen
// (hinter &&, ||, ;, |, einer Zeile, einer Klammer oder einem Umschlag wie
// "bash -lc"). Im Zweifel wird nichts erkannt: ein fehlender Treffer kostet eine
// Verbindung, ein falscher legt einer Session einen fremden Commit zu.
package gitcmd

import (
	"regexp"
	"strconv"
	"strings"
)

// Arten eines Git-Vorgangs.
const (
	KindCommit   = "commit"
	KindPush     = "push"
	KindPRCreate = "pr_create"
	KindPRMerge  = "pr_merge"
)

// Ops sind die Befehle, die einen Commit erzeugen können.
const (
	OpCommit     = "commit"
	OpAmend      = "amend"
	OpMerge      = "merge"
	OpRebase     = "rebase"
	OpCherryPick = "cherry-pick"
	OpAm         = "am"
	OpRevert     = "revert"
)

// Call ist ein erkannter Vorgang in einem Befehl.
type Call struct {
	Kind string
	// Op nennt bei KindCommit, welcher Befehl den Commit erzeugt.
	Op string
	// Dir ist das Verzeichnis, das der Befehl selbst nennt (cd oder git -C);
	// leer, wenn der Aufruf im Arbeitsverzeichnis des Werkzeugs läuft.
	Dir string
	// Quiet: der Commit lief mit -q und gibt deshalb keine SHA aus.
	Quiet bool
	// PR ist bei gh pr merge die im Befehl genannte Nummer (0 = nicht genannt).
	PR int64
	// Repo ist bei gh das Repo, das -R/--repo nennt ("owner/name" oder
	// "host/owner/name"); leer heißt: das Repo des Arbeitsverzeichnisses.
	Repo string
}

// maxDepth begrenzt das Auspacken von "bash -c '…'".
const maxDepth = 3

// Parse liefert die Vorgänge in der Reihenfolge, in der sie im Befehl stehen.
func Parse(command string) []Call {
	return parseDepth(command, 0, "")
}

func parseDepth(command string, depth int, dir string) []Call {
	if depth > maxDepth || strings.TrimSpace(command) == "" {
		return nil
	}
	var out []Call
	for _, cmd := range splitCommands(command) {
		words := cmd
		for len(words) > 0 {
			w := words[0]
			switch {
			case isAssignment(w), w == "!", w == "{", w == "then", w == "do", w == "else", w == "elif",
				w == "if", w == "while", w == "until",
				w == "sudo", w == "command", w == "builtin", w == "exec", w == "time", w == "nohup", w == "nice":
				words = words[1:]
				continue
			case w == "env":
				words = skipEnv(words[1:])
				continue
			case w == "timeout":
				words = skipTimeout(words[1:])
				continue
			}
			break
		}
		if len(words) == 0 {
			continue
		}
		switch base(words[0]) {
		case "cd", "pushd":
			if len(words) > 1 && words[1] != "-" && !strings.HasPrefix(words[1], "-") {
				dir = joinDir(dir, words[1])
			}
		case "bash", "sh", "zsh", "dash", "ksh":
			if script, ok := shellScript(words[1:]); ok {
				out = append(out, parseDepth(script, depth+1, dir)...)
			}
		case "git":
			if c, ok := parseGit(words[1:], dir); ok {
				out = append(out, c)
			}
		case "gh":
			if c, ok := parseGH(words[1:], dir); ok {
				out = append(out, c)
			}
		}
	}
	return out
}

func base(w string) string {
	if i := strings.LastIndexByte(w, '/'); i >= 0 {
		return w[i+1:]
	}
	return w
}

var assignRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

func isAssignment(w string) bool { return assignRe.MatchString(w) }

func skipEnv(words []string) []string {
	for len(words) > 0 && (isAssignment(words[0]) || strings.HasPrefix(words[0], "-")) {
		words = words[1:]
	}
	return words
}

func skipTimeout(words []string) []string {
	for len(words) > 0 && strings.HasPrefix(words[0], "-") {
		words = words[1:]
	}
	if len(words) > 0 {
		words = words[1:]
	}
	return words
}

// joinDir hängt ein relatives Verzeichnis an das bisherige. Ein ~ oder eine
// Variable bleibt, wie sie ist: das Verzeichnis dient nur dazu, ein Repo zu
// finden, und der Aufrufer weiß, was er mit einem unauflösbaren Pfad tut.
func joinDir(cur, next string) string {
	if next == "" {
		return cur
	}
	if strings.HasPrefix(next, "/") || strings.HasPrefix(next, "~") || strings.HasPrefix(next, "$") || cur == "" {
		return next
	}
	return strings.TrimRight(cur, "/") + "/" + next
}

// shellScript sucht hinter den Schaltern von "bash -lc" das Skript.
func shellScript(args []string) (string, bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" || a == "--" {
			return "", false
		}
		if strings.HasPrefix(a, "--") {
			continue
		}
		if strings.Contains(a, "c") {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", false
		}
	}
	return "", false
}

// --------------------------------------------------------------------- git

// commitOps sind die Unterbefehle, die einen Commit erzeugen können, und die
// Schalter, mit denen sie es gerade nicht tun.
var commitOps = map[string]struct {
	op   string
	skip []string
}{
	"commit":      {OpCommit, []string{"--dry-run", "-h", "--help"}},
	"merge":       {OpMerge, []string{"--abort", "--quit", "--no-commit", "--squash", "--ff-only", "-h", "--help"}},
	"rebase":      {OpRebase, []string{"--abort", "--quit", "--show-current-patch", "--edit-todo", "-h", "--help"}},
	"cherry-pick": {OpCherryPick, []string{"--abort", "--quit", "-n", "--no-commit", "-h", "--help"}},
	"am":          {OpAm, []string{"--abort", "--quit", "--show-current-patch", "-h", "--help"}},
	"revert":      {OpRevert, []string{"--abort", "--quit", "-n", "--no-commit", "-h", "--help"}},
}

// gitValueOpts sind globale Schalter von git, die den nächsten Wert verbrauchen.
var gitValueOpts = map[string]bool{
	"-c": true, "--git-dir": true, "--work-tree": true, "--namespace": true,
	"--super-prefix": true, "--config-env": true,
}

func parseGit(args []string, dir string) (Call, bool) {
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "-C":
			if i+1 < len(args) {
				dir = joinDir(dir, args[i+1])
			}
			i += 2
			continue
		case strings.HasPrefix(a, "--work-tree="):
			dir = strings.TrimPrefix(a, "--work-tree=")
			i++
			continue
		case a == "--work-tree":
			if i+1 < len(args) {
				dir = args[i+1]
			}
			i += 2
			continue
		case gitValueOpts[a]:
			i += 2
			continue
		case strings.HasPrefix(a, "-"):
			i++
			continue
		}
		break
	}
	if i >= len(args) {
		return Call{}, false
	}
	sub, rest := args[i], args[i+1:]
	switch sub {
	case "push":
		if has(rest, "--dry-run", "-n", "-h", "--help") {
			return Call{}, false
		}
		return Call{Kind: KindPush, Dir: dir}, true
	}
	spec, ok := commitOps[sub]
	if !ok || has(rest, spec.skip...) {
		return Call{}, false
	}
	c := Call{Kind: KindCommit, Op: spec.op, Dir: dir}
	if sub == "commit" {
		if has(rest, "--amend") {
			c.Op = OpAmend
		}
		c.Quiet = quietFlag(rest)
	}
	return c, true
}

func has(args []string, opts ...string) bool {
	for _, a := range args {
		for _, o := range opts {
			if a == o {
				return true
			}
		}
	}
	return false
}

// quietFlag liest -q, --quiet und Schalterbündel wie -qam; hinter dem ersten
// Schalter mit Wert (m, F, c, C, t) beginnt der Wert, kein Schalter mehr.
func quietFlag(args []string) bool {
	for _, a := range args {
		if a == "--quiet" {
			return true
		}
		if len(a) < 2 || a[0] != '-' || a[1] == '-' {
			continue
		}
		for _, r := range a[1:] {
			if strings.ContainsRune("mFcCt", r) {
				break
			}
			if r == 'q' {
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------------- gh

// ghValueOpts verbrauchen den nächsten Wert; ohne sie hielte parseGH den Wert
// für die Nummer des PRs.
var ghValueOpts = map[string]bool{
	"-R": true, "--repo": true, "-b": true, "--body": true, "-F": true, "--body-file": true,
	"-t": true, "--subject": true, "--match-head-commit": true, "-A": true, "--author-email": true,
	"--title": true, "--base": true, "-B": true, "--head": true, "-H": true, "--assignee": true,
	"--label": true, "-l": true, "--milestone": true, "-m": true, "--reviewer": true,
	"--project": true, "-p": true, "-a": true,
}

var prURLRe = regexp.MustCompile(`/pull/(\d+)`)

func parseGH(args []string, dir string) (Call, bool) {
	var pos []string
	var repo string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			if (a == "-h" || a == "--help") || a == "--dry-run" {
				return Call{}, false
			}
			switch {
			case (a == "-R" || a == "--repo") && i+1 < len(args):
				repo = args[i+1]
			case strings.HasPrefix(a, "--repo="):
				repo = strings.TrimPrefix(a, "--repo=")
			}
			if ghValueOpts[a] {
				i++
			}
			continue
		}
		pos = append(pos, a)
	}
	if len(pos) < 2 || pos[0] != "pr" {
		return Call{}, false
	}
	switch pos[1] {
	case "create":
		return Call{Kind: KindPRCreate, Dir: dir, Repo: repo}, true
	case "merge":
		c := Call{Kind: KindPRMerge, Dir: dir, Repo: repo}
		if len(pos) > 2 {
			sel := strings.TrimPrefix(pos[2], "#")
			if n, err := strconv.ParseInt(sel, 10, 64); err == nil && n > 0 {
				c.PR = n
			} else if m := prURLRe.FindStringSubmatch(sel); m != nil {
				c.PR, _ = strconv.ParseInt(m[1], 10, 64)
			}
		}
		return c, true
	}
	return Call{}, false
}

// ------------------------------------------------------------ Zerlegen

// splitCommands zerlegt einen Shell-Text in einfache Befehle (Wortlisten).
// Es ist KEIN Shell-Parser: Anführungszeichen, Escapes, Kommentare, Heredocs und
// die Trenner && || ; | & ( ) und Backticks reichen, um Aufrufe von Text zu
// unterscheiden. Was darüber hinausgeht (Variablen, Substitution in Wörtern),
// bleibt Text.
func splitCommands(s string) [][]string {
	var (
		cmds     [][]string
		cur      []string
		word     strings.Builder
		inWord   bool
		heredocs []heredoc
	)
	endWord := func() {
		if inWord {
			cur = append(cur, word.String())
			word.Reset()
			inWord = false
		}
	}
	endCmd := func() {
		endWord()
		if len(cur) > 0 {
			cmds = append(cmds, cur)
			cur = nil
		}
	}
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == '\\':
			if i+1 < len(rs) {
				if rs[i+1] == '\n' {
					i++
					continue
				}
				word.WriteRune(rs[i+1])
				inWord = true
				i++
			}
		case c == '\'':
			inWord = true
			j := i + 1
			for j < len(rs) && rs[j] != '\'' {
				word.WriteRune(rs[j])
				j++
			}
			i = j
		case c == '"':
			inWord = true
			j := i + 1
			for j < len(rs) && rs[j] != '"' {
				if rs[j] == '$' && j+1 < len(rs) && rs[j+1] == '(' {
					// "$(cat <<'EOF' … EOF)": die Ersetzung ist Text des Wortes, soweit
					// sie reicht. Ihr Ende liegt hinter Anführungszeichen und
					// Heredoc-Rümpfen, nicht beim nächsten ".
					end := skipSubstitution(rs, j+2)
					word.WriteString(string(rs[j:end]))
					j = end
					continue
				}
				if rs[j] == '\\' && j+1 < len(rs) && strings.ContainsRune("\"\\$`\n", rs[j+1]) {
					if rs[j+1] != '\n' {
						word.WriteRune(rs[j+1])
					}
					j += 2
					continue
				}
				word.WriteRune(rs[j])
				j++
			}
			i = j
		case c == '#' && !inWord:
			for i < len(rs) && rs[i] != '\n' {
				i++
			}
			i--
		case c == '\n':
			endCmd()
			if len(heredocs) > 0 {
				i = skipHeredocs(rs, i+1, heredocs) - 1
				heredocs = nil
			}
		case c == ' ' || c == '\t' || c == '\r':
			endWord()
		case c == '<' && i+1 < len(rs) && rs[i+1] == '<' && !(i+2 < len(rs) && rs[i+2] == '<'):
			endWord()
			j := i + 2
			strip := false
			if j < len(rs) && rs[j] == '-' {
				strip = true
				j++
			}
			for j < len(rs) && (rs[j] == ' ' || rs[j] == '\t') {
				j++
			}
			var delim strings.Builder
			for j < len(rs) && !strings.ContainsRune(" \t\n;&|()<>", rs[j]) {
				if strings.ContainsRune("'\"\\", rs[j]) {
					j++
					continue
				}
				delim.WriteRune(rs[j])
				j++
			}
			if delim.Len() > 0 {
				heredocs = append(heredocs, heredoc{delim: delim.String(), strip: strip})
			}
			i = j - 1
		case c == '<' && i+1 < len(rs) && rs[i+1] == '<':
			// Here-String <<<: das Wort dahinter ist Text.
			word.WriteString("<<<")
			inWord = true
			i += 2
		case c == '&' && ((i > 0 && (rs[i-1] == '>' || rs[i-1] == '<')) || (i+1 < len(rs) && rs[i+1] == '>')):
			word.WriteRune(c)
			inWord = true
		case c == '&' || c == '|' || c == ';' || c == '(' || c == ')' || c == '`':
			endCmd()
			if (c == '&' || c == '|') && i+1 < len(rs) && rs[i+1] == c {
				i++
			}
		case c == '$' && i+1 < len(rs) && rs[i+1] == '(':
			endCmd()
			i++
		default:
			word.WriteRune(c)
			inWord = true
		}
	}
	endCmd()
	return cmds
}

type heredoc struct {
	delim string
	strip bool
}

// skipSubstitution liest eine Befehlsersetzung "$( … )" (from steht hinter dem "$(")
// und liefert die Position hinter der schließenden Klammer; nicht geschlossen frisst
// sie den Rest. Anführungszeichen, Escapes, geschachtelte Klammern und Heredocs
// werden gelesen, so dass ein ungerades ' oder " in einer Commit-Nachricht das Ende
// nicht verschiebt.
func skipSubstitution(rs []rune, from int) int {
	depth := 1
	var docs []heredoc
	for i := from; i < len(rs); i++ {
		switch c := rs[i]; {
		case c == '\\':
			i++
		case c == '\'':
			i++
			for i < len(rs) && rs[i] != '\'' {
				i++
			}
		case c == '"':
			i = skipDoubleQuoted(rs, i+1) - 1
		case c == '`':
			i++
			for i < len(rs) && rs[i] != '`' {
				if rs[i] == '\\' {
					i++
				}
				i++
			}
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return i + 1
			}
		case c == '\n':
			if len(docs) > 0 {
				i = skipHeredocs(rs, i+1, docs) - 1
				docs = nil
			}
		case c == '<' && i+1 < len(rs) && rs[i+1] == '<' && !(i+2 < len(rs) && rs[i+2] == '<'):
			j := i + 2
			strip := false
			if j < len(rs) && rs[j] == '-' {
				strip = true
				j++
			}
			for j < len(rs) && (rs[j] == ' ' || rs[j] == '\t') {
				j++
			}
			var delim strings.Builder
			for j < len(rs) && !strings.ContainsRune(" \t\n;&|()<>", rs[j]) {
				if !strings.ContainsRune("'\"\\", rs[j]) {
					delim.WriteRune(rs[j])
				}
				j++
			}
			if delim.Len() > 0 {
				docs = append(docs, heredoc{delim: delim.String(), strip: strip})
			}
			i = j - 1
		case c == '<' && i+1 < len(rs) && rs[i+1] == '<':
			i += 2
		}
	}
	return len(rs)
}

// skipDoubleQuoted liefert die Position hinter dem schließenden " einer Zeichenkette
// (from steht hinter dem öffnenden); eine Ersetzung "$( … )" darin wird für sich
// gelesen.
func skipDoubleQuoted(rs []rune, from int) int {
	for i := from; i < len(rs); i++ {
		switch {
		case rs[i] == '\\':
			i++
		case rs[i] == '$' && i+1 < len(rs) && rs[i+1] == '(':
			i = skipSubstitution(rs, i+2) - 1
		case rs[i] == '"':
			return i + 1
		}
	}
	return len(rs)
}

// skipHeredocs überspringt die Rümpfe nach dem Zeilenende und liefert die
// Position hinter der letzten Abschlusszeile. Ein nicht geschlossenes Heredoc
// frisst den Rest: besser nichts erkennen als Text für Befehle halten.
func skipHeredocs(rs []rune, from int, docs []heredoc) int {
	pos := from
	for _, d := range docs {
		for pos < len(rs) {
			end := pos
			for end < len(rs) && rs[end] != '\n' {
				end++
			}
			line := string(rs[pos:end])
			if d.strip {
				line = strings.TrimLeft(line, "\t")
			}
			pos = end + 1
			if strings.TrimRight(line, "\r") == d.delim {
				break
			}
		}
	}
	if pos > len(rs) {
		pos = len(rs)
	}
	return pos
}

// ----------------------------------------------------------------- Ausgabe

var commitLineRe = regexp.MustCompile(`(?m)^\s*\[[^\]\n]*?[ ]([0-9a-f]{7,40})\][ ]`)

// maxReported begrenzt, wie viele SHAs ein Aufruf melden darf (ein Rebase über
// viele Commits).
const maxReported = 20

// CommitSHAs liest die Zeilen "[branch abc1234] Titel" aus der Ausgabe von git
// commit oder git cherry-pick. Claude Code ruft git commit meist mit -q auf; dann
// gibt es keine Zeile, und der Aufruf trägt nur Zeit und Ergebnis.
func CommitSHAs(output string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range commitLineRe.FindAllStringSubmatch(output, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
			if len(out) >= maxReported {
				break
			}
		}
	}
	return out
}

var pushRefRe = regexp.MustCompile(`(?m)^\s*(?:[0-9a-f]{7,40}\.\.\.?[0-9a-f]{7,40}|\*\s+\[new (?:branch|tag)\]|\+\s+[0-9a-f]{7,40}\.\.\.[0-9a-f]{7,40}\s+\(forced update\)|\+\s+[0-9a-f]{7,40}\.\.\.[0-9a-f]{7,40})\s+\S+\s+->\s+(\S+)`)

// PushedBranch nennt den ersten Branch, den der Push auf der Gegenseite
// verändert hat ("   abc..def  feat/x -> feat/x"); leer, wenn die Ausgabe keine
// Zeile dieser Art enthält (Everything up-to-date, Fehler, gekürzt).
func PushedBranch(output string) string {
	m := pushRefRe.FindStringSubmatch(output)
	if m == nil {
		return ""
	}
	return strings.TrimPrefix(m[1], "refs/heads/")
}

var prCreatedRe = regexp.MustCompile(`https?://([A-Za-z0-9.\-]+)/([A-Za-z0-9._\-]+)/([A-Za-z0-9._\-]+)/pull/(\d+)`)

// PullCreated liest die Adresse, die gh pr create ausgibt: Repo (host/owner/name)
// und Nummer. Bei mehreren Adressen gilt die letzte.
func PullCreated(output string) (remote string, n int64) {
	all := prCreatedRe.FindAllStringSubmatch(output, -1)
	if len(all) == 0 {
		return "", 0
	}
	m := all[len(all)-1]
	n, _ = strconv.ParseInt(m[4], 10, 64)
	return strings.ToLower(m[1] + "/" + m[2] + "/" + m[3]), n
}

var prMergedRe = regexp.MustCompile(`(?i)\bmerged pull request (?:[A-Za-z0-9._\-]+/[A-Za-z0-9._\-]+)?#(\d+)`)

// PullMerged liest "✓ Merged pull request #12" bzw. "Squashed and merged …"; 0,
// wenn gh nichts Gemergtes meldet (auch --auto, das erst später mergt).
func PullMerged(output string) int64 {
	m := prMergedRe.FindStringSubmatch(output)
	if m == nil {
		return 0
	}
	n, _ := strconv.ParseInt(m[1], 10, 64)
	return n
}
