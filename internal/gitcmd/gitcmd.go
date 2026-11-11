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
	// Message ist die Commit-Nachricht, die der Befehl selbst trägt (-m, mehrere -m,
	// --message, "$(cat <<'EOF' … EOF)", -F - mit Heredoc); leer, wenn der Befehl
	// keine trägt (--amend --no-edit, -F datei, -C) oder der Text erst die Shell
	// auflöst ($VAR, $(date), Backticks). Der Text bleibt im Speicher: der Collector
	// bildet daraus Schlüssel (gitmsg) und meldet nur die.
	Message string
	// MessageAdded: git hängt der Nachricht Zeilen an (--signoff, --trailer); nur
	// die Titelzeile ist dann die des Commits.
	MessageAdded bool
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
	cmds := splitShell(command)
	for idx, cmd := range cmds {
		words, kinds := cmd.words, cmd.kinds
		// drop entfernt die ersten n Wörter samt ihrer Art.
		drop := func(n int) { words, kinds = words[n:], kinds[n:] }
		for len(words) > 0 {
			w := words[0]
			switch {
			case isAssignment(w), w == "!", w == "{", w == "then", w == "do", w == "else", w == "elif",
				w == "if", w == "while", w == "until",
				w == "sudo", w == "command", w == "builtin", w == "exec", w == "time", w == "nohup", w == "nice":
				drop(1)
				continue
			case w == "env":
				drop(len(words) - len(skipEnv(words[1:])))
				continue
			case w == "timeout":
				drop(len(words) - len(skipTimeout(words[1:])))
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
			// Der Text für -F - kommt aus dem Heredoc des Befehls, oder aus dem eines
			// "cat <<EOF" davor in derselben Pipe.
			stdin := cmd.docs
			if len(stdin) == 0 && idx > 0 && len(cmds[idx-1].words) > 0 && base(cmds[idx-1].words[0]) == "cat" {
				stdin = cmds[idx-1].docs
			}
			if c, ok := parseGit(words[1:], kinds[1:], dir, stdin); ok {
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

func parseGit(args []string, kinds []wordKind, dir string, stdin []heredocBody) (Call, bool) {
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
	sub, rest, restKinds := args[i], args[i+1:], kinds[i+1:]
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
	if sub == "commit" || sub == "merge" {
		c.Message, c.MessageAdded = commitMessage(sub, rest, restKinds, stdin)
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

// shellCmd ist ein einfacher Befehl: seine Wörter, ob die Shell sie noch verändert
// (wordKind) und die Rümpfe der Heredocs, die ihm gehören.
type shellCmd struct {
	words []string
	kinds []wordKind
	docs  []heredocBody
}

// splitCommands zerlegt einen Shell-Text in einfache Befehle (Wortlisten).
func splitCommands(s string) [][]string {
	cmds := splitShell(s)
	out := make([][]string, len(cmds))
	for i, c := range cmds {
		out[i] = c.words
	}
	return out
}

// splitShell zerlegt einen Shell-Text in einfache Befehle.
// Es ist KEIN Shell-Parser: Anführungszeichen, Escapes, Kommentare, Heredocs und
// die Trenner && || ; | & ( ) und Backticks reichen, um Aufrufe von Text zu
// unterscheiden. Was darüber hinausgeht (Variablen, Substitution in Wörtern),
// bleibt Text; wordKind merkt sich, welche Wörter das betrifft, damit eine
// Commit-Nachricht nur dann gelesen wird, wenn sie wörtlich dasteht.
func splitShell(s string) []shellCmd {
	var (
		cmds     []shellCmd
		cur      shellCmd
		word     strings.Builder
		kind     wordKind
		inWord   bool
		heredocs []heredoc
	)
	endWord := func() {
		if inWord {
			cur.words = append(cur.words, word.String())
			cur.kinds = append(cur.kinds, kind)
			word.Reset()
			kind = 0
			inWord = false
		}
	}
	endCmd := func() {
		endWord()
		if len(cur.words) > 0 {
			cmds = append(cmds, cur)
			cur = shellCmd{}
		}
	}
	// heredocOperator liest "<<[-] DELIM" ab rs[i] (auf dem ersten <) und merkt es für
	// das Zeilenende; es liefert die Position des letzten gelesenen Zeichens.
	heredocOperator := func(rs []rune, i int) int {
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
		quoted := false
		for j < len(rs) && !strings.ContainsRune(" \t\n;&|()<>", rs[j]) {
			if strings.ContainsRune("'\"\\", rs[j]) {
				quoted = true
				j++
				continue
			}
			delim.WriteRune(rs[j])
			j++
		}
		if delim.Len() > 0 {
			// Dem Befehl, der gerade entsteht: er bekommt den Index, den endCmd ihm gibt.
			heredocs = append(heredocs, heredoc{delim: delim.String(), strip: strip, quoted: quoted, owner: len(cmds)})
		}
		return j - 1
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
					kind |= kindSubst
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
				if rs[j] == '`' || (rs[j] == '$' && j+1 < len(rs) && hasExpansion(string(rs[j:j+2]))) {
					kind |= kindExpands
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
				end, bodies := readHeredocs(rs, i+1, heredocs)
				for n, d := range heredocs {
					if d.owner < len(cmds) {
						cmds[d.owner].docs = append(cmds[d.owner].docs, bodies[n])
					}
				}
				i = end - 1
				heredocs = nil
			}
		case c == ' ' || c == '\t' || c == '\r':
			endWord()
		case c == '<' && i+1 < len(rs) && rs[i+1] == '<' && !(i+2 < len(rs) && rs[i+2] == '<'):
			endWord()
			i = heredocOperator(rs, i)
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
			if c == '$' && i+1 < len(rs) && hasExpansion(string(rs[i:i+2])) {
				kind |= kindExpands
			}
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
	// quoted: die Kennung stand in Anführungszeichen, der Rumpf ist wörtlich.
	quoted bool
	// owner ist der Index des Befehls, dessen Standardeingabe der Rumpf ist.
	owner int
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
	end, _ := readHeredocs(rs, from, docs)
	return end
}

// readHeredocs liest die Rümpfe wie skipHeredocs und gibt sie zusätzlich zurück, je
// einen je Heredoc in der Reihenfolge ihrer Operatoren (ohne die Abschlusszeile).
func readHeredocs(rs []rune, from int, docs []heredoc) (int, []heredocBody) {
	pos := from
	bodies := make([]heredocBody, 0, len(docs))
	for _, d := range docs {
		var lines []string
		for pos < len(rs) {
			end := pos
			for end < len(rs) && rs[end] != '\n' {
				end++
			}
			raw := string(rs[pos:end])
			line := raw
			if d.strip {
				line = strings.TrimLeft(line, "\t")
			}
			pos = end + 1
			if strings.TrimRight(line, "\r") == d.delim {
				break
			}
			lines = append(lines, strings.TrimRight(line, "\r"))
		}
		bodies = append(bodies, heredocBody{text: strings.Join(lines, "\n"), literal: d.quoted})
	}
	if pos > len(rs) {
		pos = len(rs)
	}
	return pos, bodies
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

// maxPushRefs begrenzt, wie viele Refs ein Push meldet (git push --all).
const maxPushRefs = 8

// PushRef ist eine Zeile der Ausgabe von git push: was auf der Gegenseite mit einem
// Ref geschah.
type PushRef struct {
	// Src ist das lokale Ref, Dst das auf der Gegenseite (ohne refs/heads/).
	Src, Dst string
	// Old und New sind die SHA-Präfixe des Bereichs "Old..New", den der Push dem Ref
	// hinzugefügt hat; leer bei [new branch], abgelehnt und gelöscht. Bei einem
	// Force-Push ("Old...New") ist Old der verdrängte Stand.
	Old, New string
	// NewBranch: das Ref gab es auf der Gegenseite nicht; der Push brachte alle Commits.
	NewBranch bool
	// NewTag: ein Tag oder anderes Ref, kein Branch.
	NewTag bool
	// Forced: der Bereich ersetzt Commits (forced update).
	Forced bool
	// Rejected: die Gegenseite hat das Ref nicht angenommen; es ist nichts gepusht.
	Rejected bool
	// Deleted und UpToDate: das Ref wurde gelöscht bzw. war schon aktuell.
	Deleted, UpToDate bool
}

// Pushed sagt, ob der Push dieses Ref auf der Gegenseite verändert hat und es ein Branch ist.
func (r PushRef) Pushed() bool {
	return !r.Rejected && !r.Deleted && !r.UpToDate && !r.NewTag && r.Dst != ""
}

var pushLineRe = regexp.MustCompile(`^[ \t]*([ +\-*!=])?[ \t]+(\[[^\]\n]+\]|[0-9a-f]{7,64}\.\.\.?[0-9a-f]{7,64})[ \t]+(\S+)(?:[ \t]+->[ \t]+(\S+))?(?:[ \t]+\(([^)\n]*)\))?[ \t]*$`)

// PushRefs liest die Ref-Zeilen aus der Ausgabe von git push, in der Reihenfolge der
// Ausgabe:
//
//	  abc1234..def5678  feat/x -> feat/x
//	* [new branch]      feat/y -> feat/y
//	+ abc1234...def5678 feat/z -> feat/z (forced update)
//	! [rejected]        main -> main (non-fast-forward)
//
// Ein Push mit -q gibt nichts aus; dann ist die Liste leer.
func PushRefs(output string) []PushRef {
	var out []PushRef
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		m := pushLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		flag, summary, from, to := m[1], m[2], m[3], m[4]
		r := PushRef{Src: from, Dst: strings.TrimPrefix(to, "refs/heads/")}
		if to == "" {
			// "- [deleted]  name": nur ein Name.
			r.Dst = strings.TrimPrefix(from, "refs/heads/")
		}
		switch {
		case strings.HasPrefix(summary, "["):
			switch strings.Trim(summary, "[]") {
			case "new branch":
				r.NewBranch = true
			case "new tag", "new reference":
				r.NewTag = true
			case "rejected", "remote rejected", "remote failure":
				r.Rejected = true
			case "deleted":
				r.Deleted = true
			case "up to date":
				r.UpToDate = true
			default:
				continue
			}
		default:
			if flag == "-" || flag == "!" || flag == "=" || flag == "*" {
				continue
			}
			old, new, _ := strings.Cut(strings.Replace(summary, "...", "..", 1), "..")
			r.Old, r.New = old, new
			r.Forced = flag == "+" || strings.Contains(summary, "...")
		}
		out = append(out, r)
		if len(out) >= maxPushRefs {
			break
		}
	}
	return out
}

// PushedBranch nennt den ersten Branch, den der Push auf der Gegenseite
// verändert hat ("   abc..def  feat/x -> feat/x"); leer, wenn die Ausgabe keine
// Zeile dieser Art enthält (Everything up-to-date, Fehler, gekürzt).
func PushedBranch(output string) string {
	for _, r := range PushRefs(output) {
		if r.Pushed() {
			return r.Dst
		}
	}
	return ""
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
