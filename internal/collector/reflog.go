package collector

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Die SHA eines Commits aus dem Reflog (IDEA-31, P2, Quelle observed).
//
// Claude Code committet zu 88 % mit -q; die Zeile "[branch abc1234] Titel" steht dann
// nicht im Transkript (KN-4459). Verlässlich ist, DASS und WANN committet wurde. Nach
// einem erfolgreichen Commit-Aufruf liest der Collector deshalb das Reflog von HEAD
// des Arbeitsverzeichnisses im Zeitfenster des Aufrufs: jeder Eintrag, der einen
// Commit erzeugt hat, trägt dessen volle SHA.
//
// Grenzen, damit das billig und harmlos bleibt:
//   - Es läuft nur nach einem erfolgreichen Aufruf, der einen Commit erzeugen kann
//     (gitcmd), und höchstens drei git-Prozesse je Aufruf: log -g, dann ein log mit
//     Patch für alle gefundenen Commits zusammen, dann patch-id. Ohne Treffer im
//     Fenster bleibt es bei einem.
//   - Es liest nur im Verzeichnis, in dem das Werkzeug lief, oder in einem, das
//     darunter liegt oder zum selben Repo gehört (Worktrees). Ein "git -C /anderswo"
//     oder "cd /anderswo" in ein fremdes Repo wird nicht gelesen. Die Prüfung liest
//     nur Dateien (.git, commondir), sie startet keinen Prozess.
//   - Kein Netzzugriff: nur Befehle, die das lokale Repo lesen, ohne Hooks, ohne
//     Pager und ohne die Index-Sperre. Weder fetch noch Remote-Helfer.
//   - Je Transkript höchstens observeBurst Beobachtungen in observeWindow, und nur für
//     Aufrufe der letzten reflogMaxAge: ein Collector, der zum ersten Mal ein altes
//     Transkript liest, startet keine Welle von Prozessen.
//
// Fehlt das Repo, das Reflog oder git, bleibt es beim Vorgang mit Zeitfenster: der
// Server kann ihn später selbst auflösen (matched).
const (
	reflogProcessTimeout = 10 * time.Second
	// reflogMaxEntries: so viele Einträge liest log -g höchstens (neueste zuerst).
	reflogMaxEntries = 200
	// reflogMaxCommits: so viele Commits meldet ein Aufruf höchstens (ein Rebase über
	// Hunderte Commits ist keiner, den eine Session in einem Aufruf erzeugt).
	reflogMaxCommits = 50
	// reflogPatchBytes: größer darf die Ausgabe des Patches nicht sein; darüber
	// bleibt die Patch-ID leer, der Commit wird trotzdem gemeldet.
	reflogPatchBytes = 8 << 20
	// reflogSlack: die Transkriptzeit hat Millisekunden, das Reflog Sekunden.
	reflogSlack  = 2 * time.Second
	reflogMaxAge = 30 * 24 * time.Hour

	observeBurst  = 300
	observeWindow = 10 * time.Minute
)

// gitRunner startet git mit Argumenten in dir und liefert die Ausgabe. Als Wert, damit
// ein Test die Prozesse zählen kann.
type gitRunner func(ctx context.Context, dir string, stdin []byte, limit int, args ...string) ([]byte, error)

var errGitOutputLimit = errors.New("git output over its limit")

// gitRepoVars sind Umgebungsvariablen, die ein Repo bestimmen: der Collector läuft
// womöglich in einer Umgebung, die eines davon gesetzt hat, und würde dann nicht das
// Repo des Aufrufs lesen.
var gitRepoVars = map[string]bool{
	"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_COMMON_DIR": true, "GIT_INDEX_FILE": true,
	"GIT_OBJECT_DIRECTORY": true, "GIT_ALTERNATE_OBJECT_DIRECTORIES": true, "GIT_NAMESPACE": true,
	"GIT_PREFIX": true,
}

func gitEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); gitRepoVars[k] {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
}

// limitedBuffer hält höchstens limit Bytes und bricht den Prozess ab, wenn mehr
// kommt.
type limitedBuffer struct {
	buf     bytes.Buffer
	limit   int
	onLimit func()
	over    bool
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if l.over {
		return len(p), nil
	}
	if l.buf.Len()+len(p) > l.limit {
		l.over = true
		if l.onLimit != nil {
			l.onLimit()
		}
		return len(p), nil
	}
	return l.buf.Write(p)
}

func execGit(ctx context.Context, dir string, stdin []byte, limit int, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, reflogProcessTimeout)
	defer cancel()
	full := append([]string{"--no-pager", "--no-optional-locks", "-c", "core.fsmonitor=false", "-c", "core.quotepath=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out := &limitedBuffer{limit: limit, onLimit: cancel}
	cmd.Stdout = out
	err := cmd.Run()
	if out.over {
		return nil, errGitOutputLimit
	}
	if err != nil {
		return nil, err
	}
	return out.buf.Bytes(), nil
}

// reflogReader liest das Reflog eines Arbeitsverzeichnisses.
type reflogReader struct {
	run gitRunner
}

func newReflogReader() *reflogReader { return &reflogReader{run: execGit} }

// reflogOps erkennt die Einträge, die einen Commit erzeugt haben. Nicht dazu gehören
// der Anfang und das Ende eines Rebase, ein Checkout, ein Reset und ein
// Vorspulen: dort bewegt sich HEAD auf einen Commit, der schon da war.
var reflogRebaseRe = regexp.MustCompile(`^rebase(?: -i)? \((pick|reword|edit|squash|fixup|continue)\)`)

func reflogOp(msg string) (string, bool) {
	switch {
	case strings.HasPrefix(msg, "commit (amend)"):
		return "amend", true
	case strings.HasPrefix(msg, "commit"):
		return "commit", true
	case strings.HasPrefix(msg, "cherry-pick"):
		if strings.Contains(msg, "(abort)") {
			return "", false
		}
		return "cherry-pick", true
	case strings.HasPrefix(msg, "revert"):
		if strings.Contains(msg, "(abort)") {
			return "", false
		}
		return "revert", true
	case reflogRebaseRe.MatchString(msg):
		return "rebase", true
	case strings.HasPrefix(msg, "merge ") && !strings.Contains(msg, "Fast-forward"):
		return "merge", true
	case strings.HasPrefix(msg, "am:") || strings.HasPrefix(msg, "am "):
		if strings.HasPrefix(msg, "am (") {
			return "", false
		}
		return "am", true
	}
	return "", false
}

type reflogEntry struct {
	sha, op string
	// committed ist die Committer-Zeit des Commits, at die des Reflog-Eintrags.
	committed, at int64
}

// observe liest die Commits, die in dir zwischen from und to entstanden. ok ist
// false, wenn sich nichts lesen ließ (kein Repo, kein git, kein Reflog).
func (r *reflogReader) observe(ctx context.Context, dir string, from, to time.Time) (out []store.ObservedCommit, ok bool) {
	lo := from.Add(-reflogSlack).Unix()
	hi := to.Add(reflogSlack).Unix() + 1
	raw, err := r.run(ctx, dir, nil, 1<<20, "log", "-g", "--date=unix", "-n", strconv.Itoa(reflogMaxEntries),
		"--format=%H%x1f%ct%x1f%gd%x1f%gs", "HEAD", "--")
	if err != nil {
		return nil, false
	}
	var found []reflogEntry
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		f := strings.SplitN(sc.Text(), "\x1f", 4)
		if len(f) != 4 {
			continue
		}
		at, ok := reflogSelectorTime(f[2])
		if !ok {
			continue
		}
		if at > hi {
			continue
		}
		if at < lo {
			// Neueste zuerst: ab hier liegt alles vor dem Fenster.
			break
		}
		op, ok := reflogOp(f[3])
		if !ok {
			continue
		}
		ct, _ := strconv.ParseInt(f[1], 10, 64)
		found = append(found, reflogEntry{sha: f[0], op: op, committed: ct, at: at})
	}
	if len(found) == 0 {
		return nil, true
	}
	// Die ältesten zuerst, ohne Dubletten, und höchstens reflogMaxCommits.
	seen := map[string]bool{}
	var ordered []reflogEntry
	for i := len(found) - 1; i >= 0; i-- {
		if e := found[i]; !seen[e.sha] {
			seen[e.sha] = true
			ordered = append(ordered, e)
		}
	}
	if len(ordered) > reflogMaxCommits {
		ordered = ordered[len(ordered)-reflogMaxCommits:]
	}
	patches := r.patchIDs(ctx, dir, ordered)
	for _, e := range ordered {
		c := store.ObservedCommit{SHA: e.sha, Op: e.op, PatchID: patches[e.sha]}
		if e.committed > 0 {
			c.CommittedAt = time.Unix(e.committed, 0).UTC().Format(time.RFC3339)
		}
		out = append(out, c)
	}
	return out, true
}

// reflogSelectorTime liest die Zeit aus "HEAD@{1699999999}" (log -g --date=unix).
func reflogSelectorTime(sel string) (int64, bool) {
	i := strings.LastIndex(sel, "@{")
	if i < 0 || !strings.HasSuffix(sel, "}") {
		return 0, false
	}
	n, err := strconv.ParseInt(sel[i+2:len(sel)-1], 10, 64)
	return n, err == nil && n > 0
}

// patchIDs bildet die Patch-ID der Commits mit zwei Prozessen für alle zusammen:
// log mit Patch (ein Commit je Eintrag, "commit <sha>" davor), dann patch-id --stable.
// Eine Patch-ID, die sich nicht bilden lässt (Merge-Commit ohne Diff, zu große Ausgabe,
// Fehler), fehlt in der Antwort; die Commits werden trotzdem gemeldet.
func (r *reflogReader) patchIDs(ctx context.Context, dir string, entries []reflogEntry) map[string]string {
	var in bytes.Buffer
	for _, e := range entries {
		in.WriteString(e.sha)
		in.WriteByte('\n')
	}
	patch, err := r.run(ctx, dir, in.Bytes(), reflogPatchBytes, "log", "--no-walk=unsorted", "--stdin", "-p",
		"--no-color", "--no-ext-diff", "--no-textconv", "--no-renames", "--format=commit %H")
	if err != nil || len(patch) == 0 {
		return nil
	}
	ids, err := r.run(ctx, dir, patch, 1<<20, "patch-id", "--stable")
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(ids), "\n") {
		id, sha, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok && id != "" && sha != "" {
			out[strings.TrimSpace(sha)] = id
		}
	}
	return out
}

// ----------------------------------------------------- welches Verzeichnis

// repoOf sucht von dir aufwärts das Repo (.git als Verzeichnis oder als Datei eines
// Worktrees) und nennt sein gemeinsames Verzeichnis. Es liest nur Dateien. Mehrere
// Worktrees eines Repos haben dasselbe gemeinsame Verzeichnis.
func repoOf(dir string) (commonDir string, ok bool) {
	for i := 0; i < 64; i++ {
		dotgit := filepath.Join(dir, ".git")
		fi, err := os.Lstat(dotgit)
		if err == nil {
			if fi.IsDir() {
				return dotgit, true
			}
			b, err := os.ReadFile(dotgit)
			if err != nil || len(b) > 4096 {
				return "", false
			}
			target, found := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
			if !found {
				return "", false
			}
			gitdir := strings.TrimSpace(target)
			if !filepath.IsAbs(gitdir) {
				gitdir = filepath.Join(dir, gitdir)
			}
			gitdir = filepath.Clean(gitdir)
			common := gitdir
			if c, err := os.ReadFile(filepath.Join(gitdir, "commondir")); err == nil && len(c) < 4096 {
				common = strings.TrimSpace(string(c))
				if !filepath.IsAbs(common) {
					common = filepath.Join(gitdir, common)
				}
			}
			if real, err := filepath.EvalSymlinks(common); err == nil {
				return real, true
			}
			return filepath.Clean(common), true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
	return "", false
}

// ownRepoDir bestimmt das Verzeichnis, in dem ein Aufruf lief, und sagt, ob der
// Collector es lesen darf: base ist das Arbeitsverzeichnis des Werkzeugs, callDir das,
// was der Befehl selbst nennt (cd, git -C; leer ohne). Gelesen wird nur, wenn das Ziel
// unter base liegt oder zum selben Repo wie base gehört.
func ownRepoDir(base, callDir string) (string, bool) {
	if base == "" || !filepath.IsAbs(base) {
		return "", false
	}
	target := base
	if callDir != "" {
		if strings.ContainsAny(callDir, "$`~*?[") {
			return "", false
		}
		if filepath.IsAbs(callDir) {
			target = callDir
		} else {
			target = filepath.Join(base, callDir)
		}
	}
	rb, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", false
	}
	rt, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", false
	}
	if fi, err := os.Stat(rt); err != nil || !fi.IsDir() {
		return "", false
	}
	if rel, err := filepath.Rel(rb, rt); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return rt, true
	}
	cb, ok1 := repoOf(rb)
	ct, ok2 := repoOf(rt)
	if ok1 && ok2 && cb == ct {
		return rt, true
	}
	return "", false
}

// ------------------------------------------------------------ Begrenzung

// observer begrenzt die Beobachtungen eines Transkripts.
type observer struct {
	reader *reflogReader
	now    func() time.Time

	windowStart time.Time
	used        int
}

func newObserver() *observer { return &observer{reader: newReflogReader(), now: time.Now} }

// allow zählt eine Beobachtung und sagt, ob sie noch erlaubt ist.
func (o *observer) allow() bool {
	now := o.now()
	if o.windowStart.IsZero() || now.Sub(o.windowStart) > observeWindow {
		o.windowStart, o.used = now, 0
	}
	if o.used >= observeBurst {
		return false
	}
	o.used++
	return true
}

// observeCall liest die Commits eines Commit-Aufrufs: base ist das Arbeitsverzeichnis
// des Werkzeugs, callDir das Verzeichnis, das der Befehl nennt.
func (o *observer) observeCall(base, callDir, started, ended string) []store.ObservedCommit {
	from, err := time.Parse(time.RFC3339Nano, started)
	if err != nil {
		return nil
	}
	to := from
	if t, err := time.Parse(time.RFC3339Nano, ended); err == nil && t.After(from) {
		to = t
	}
	if o.now().Sub(to) > reflogMaxAge {
		return nil
	}
	dir, ok := ownRepoDir(base, callDir)
	if !ok || !o.allow() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*reflogProcessTimeout)
	defer cancel()
	commits, _ := o.reader.observe(ctx, dir, from, to)
	return commits
}
