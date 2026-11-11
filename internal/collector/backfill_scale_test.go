package collector

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Messung des Bestandslaufs ctx db backfill-session-git an einer großen Datenbank
// (REQ-648). Läuft nur, wenn GHOSTTREE_BACKFILL_SCALE_SESSIONS gesetzt ist:
//
//	GHOSTTREE_BACKFILL_SCALE_SESSIONS=3000 GHOSTTREE_BACKFILL_SCALE_DIR=/var/tmp/bf \
//	  go test -p 1 -run TestBackfillScale -v -timeout 3h ./internal/collector/
//
// Die Datenbank wird erzeugt, wenn die Datei fehlt, und bleibt in DIR liegen (der
// nächste Lauf nimmt sie wieder). Ohne DIR liegt sie im temporären Verzeichnis des Tests.
//
// Der Bestand ist dem von Prod nachgebaut, soweit KN-4459 ihn misst: je Session im Mittel
// etwa dreihundert Zeilen mit Werkzeugergebnissen von einigen hundert Bytes bis wenigen
// Kilobytes, 13 % der Sessions mit Commit-Aufrufen (zu 88 % mit -q bei Claude Code, bei
// Codex mit Ausgabe), dazu Aufträge an Sub-Agenten, die "git commit" nur nennen; PRs
// zu etwa zwei Dritteln der leisen Commits und Rauschen, das in fremde Fenster fällt.

type scaleGen struct {
	rng        *rand.Rand
	prNumber   int64
	prCommits  [][4]string // sha, pr, committed_at, author
	prs        [][3]any    // number, head, state
	gitCalls   int
	quietCalls int
}

func (g *scaleGen) hex(n int) string {
	const digits = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		b[i] = digits[g.rng.Intn(16)]
	}
	return string(b)
}

var scaleWords = strings.Fields("the quick function returns value from handler when request context cancelled error wrapped " +
	"sqlite transaction commit session chunk raw line parser harness collector upload batch index query plan table column " +
	"migration schema writer reader lock busy timeout checkpoint wal journal vacuum backup restore archive project branch")

func (g *scaleGen) text(min, max int) string {
	n := min + g.rng.Intn(max-min+1)
	var b strings.Builder
	for b.Len() < n {
		b.WriteString(scaleWords[g.rng.Intn(len(scaleWords))])
		b.WriteByte(' ')
	}
	return b.String()
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func cl(ts time.Time) string { return ts.UTC().Format("2006-01-02T15:04:05.000Z") }

func (g *scaleGen) claudeLines(ext, cwd, branch string, start time.Time, n int, gitAt map[int]string) []string {
	var lines []string
	ts := start
	step := func() time.Time {
		ts = ts.Add(time.Duration(200+g.rng.Intn(20000)) * time.Millisecond)
		return ts
	}
	head := func(typ string) string {
		return `{"type":"` + typ + `","timestamp":"` + cl(step()) + `","cwd":"` + cwd + `","sessionId":"` + ext + `","gitBranch":"` + branch + `","version":"2.1.319",`
	}
	lines = append(lines, head("user")+`"message":{"role":"user","content":`+jsonString(g.text(60, 400))+`}}`)
	for len(lines) < n {
		id := "toolu_" + g.hex(20)
		pos := len(lines)
		var cmd, out string
		tool := "Bash"
		input := ""
		if c, ok := gitAt[pos]; ok {
			cmd = c
			out = "(Bash completed with no output)"
			if strings.HasPrefix(c, "git push") {
				out = "To github.com:example-org/example-repo.git\n   " + g.hex(7) + ".." + g.hex(7) + "  " + branch + " -> " + branch
			}
			if strings.HasPrefix(c, "gh pr create") {
				out = "https://github.com/example-org/example-repo/pull/" + strconv.FormatInt(g.prNumber, 10)
			}
			input = `{"command":` + jsonString(cmd) + `}`
		} else {
			switch g.rng.Intn(6) {
			case 0:
				tool, input = "Read", `{"file_path":"/home/user/Projects/example-repo/internal/`+g.hex(6)+`.go"}`
			case 1:
				tool, input = "Edit", `{"file_path":"/home/user/Projects/example-repo/internal/`+g.hex(6)+`.go","old_string":`+jsonString(g.text(30, 200))+`,"new_string":`+jsonString(g.text(30, 200))+`}`
			case 2:
				cmd = "go test ./internal/" + g.hex(4) + "/..."
				input = `{"command":` + jsonString(cmd) + `}`
			case 3:
				cmd = "git status --short"
				input = `{"command":` + jsonString(cmd) + `}`
			case 4:
				tool, input = "Grep", `{"pattern":"`+g.hex(5)+`","path":"internal"}`
			default:
				cmd = "ls -la internal/" + g.hex(3)
				input = `{"command":` + jsonString(cmd) + `}`
			}
			out = g.text(100, 3500)
		}
		lines = append(lines, head("assistant")+`"message":{"role":"assistant","content":[{"type":"text","text":`+jsonString(g.text(40, 600))+
			`},{"type":"tool_use","id":"`+id+`","name":"`+tool+`","input":`+input+`}]}}`)
		var resTS string
		if _, ok := gitAt[pos]; ok {
			// Die Ergebniszeile liegt um Millisekunden nach dem Aufruf: das Fenster ist kurz.
			resTS = cl(ts.Add(110 * time.Millisecond))
		} else {
			resTS = cl(step())
		}
		lines = append(lines, `{"type":"user","timestamp":"`+resTS+`","cwd":"`+cwd+`","sessionId":"`+ext+`","message":{"role":"user","content":[{"tool_use_id":"`+id+
			`","type":"tool_result","content":`+jsonString(out)+`,"is_error":false}]}}`)
	}
	return lines
}

// session erzeugt die Zeilen einer Session und merkt sich, welche PR-Commits dazu gehören.
func (g *scaleGen) session(ext, branch string, harness string, start time.Time, n int, withGit bool) []string {
	cwd := "/home/user/Projects/example-repo"
	gitAt := map[int]string{}
	var commitTimes []int
	if withGit {
		calls := 2 + g.rng.Intn(7)
		for i := 0; i < calls && i*2+4 < n; i++ {
			pos := 4 + (n-6)*i/calls
			pos -= pos % 2
			pos++ // Aufrufzeilen stehen an ungeraden Positionen
			switch {
			case i == calls-1 && g.rng.Intn(5) == 0:
				gitAt[pos] = "gh pr create --fill"
			case i%3 == 2:
				gitAt[pos] = "git push origin " + branch
			default:
				q := "-qm"
				if g.rng.Intn(100) >= 88 {
					q = "-m"
				}
				gitAt[pos] = "git add -A && git commit " + q + ` "fix: ` + g.hex(6) + `"`
				commitTimes = append(commitTimes, pos)
			}
		}
	}
	lines := g.claudeLines(ext, cwd, branch, start, n, gitAt)
	if harness == "codex" {
		lines = g.codexLines(start, n, branch, gitAt)
	}
	// Aus den Aufrufzeilen die Zeiten der Commits lesen und PR-Commits dazu erzeugen.
	g.noteCommits(lines, harness, branch, commitTimes, withGit)
	if g.rng.Intn(10) == 0 {
		// Ein Auftrag an einen Sub-Agenten, der den Befehl nur nennt.
		lines = append(lines, `{"type":"assistant","timestamp":"`+cl(start.Add(time.Hour))+`","cwd":"`+cwd+`","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_task","name":"Task","input":{"prompt":"run git commit -m done and git push when green"}}]}}`)
	}
	return lines
}

func (g *scaleGen) codexLines(start time.Time, n int, branch string, gitAt map[int]string) []string {
	ts := start
	step := func() time.Time {
		ts = ts.Add(time.Duration(200+g.rng.Intn(20000)) * time.Millisecond)
		return ts
	}
	lines := []string{`{"timestamp":"` + cl(start) + `","type":"session_meta","payload":{"id":"` + g.hex(32) + `","cwd":"/home/user/Projects/example-repo"}}`}
	for len(lines) < n {
		pos := len(lines)
		call := "call_" + g.hex(24)
		cmd := "ls -la " + g.hex(4)
		out := g.text(100, 3000)
		if c, ok := gitAt[pos]; ok {
			cmd = c
			out = ""
			if strings.Contains(c, "git commit") {
				out = "[" + branch + " " + g.hex(7) + "] fix\n 1 file changed"
			}
		}
		args := `{"cmd":` + jsonString(cmd) + `}`
		lines = append(lines, `{"timestamp":"`+cl(step())+`","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":`+jsonString(args)+`,"call_id":"`+call+`"}}`)
		lines = append(lines, `{"timestamp":"`+cl(ts.Add(120*time.Millisecond))+`","type":"response_item","payload":{"type":"function_call_output","call_id":"`+call+`","output":`+jsonString("Chunk ID: "+g.hex(6)+"\nWall time: 0.1 seconds\nProcess exited with code 0\nOutput:\n"+out)+`}}`)
	}
	return lines
}

// noteCommits liest aus den fertigen Zeilen die Commit-Aufrufe und legt zu rund zwei
// Dritteln einen PR-Commit an, dessen Zeit in das Fenster des Aufrufs fällt.
func (g *scaleGen) noteCommits(lines []string, harness, branch string, _ []int, withGit bool) {
	if !withGit {
		return
	}
	g.prNumber++
	pr := g.prNumber
	g.prs = append(g.prs, [3]any{pr, branch, "merged"})
	for i, l := range lines {
		if !strings.Contains(l, "git commit") || !strings.Contains(l, `"tool_use"`) && !strings.Contains(l, `"function_call"`) {
			continue
		}
		if strings.Contains(l, `"name":"Task"`) {
			continue
		}
		g.gitCalls++
		if strings.Contains(l, " -qm ") {
			g.quietCalls++
		}
		var ts struct {
			Timestamp string `json:"timestamp"`
		}
		_ = json.Unmarshal([]byte(l), &ts)
		started, err := time.Parse(time.RFC3339Nano, ts.Timestamp)
		if err != nil || g.rng.Intn(3) == 0 {
			continue
		}
		_ = i
		g.prCommits = append(g.prCommits, [4]string{g.hex(40), strconv.FormatInt(pr, 10), started.Add(300 * time.Millisecond).Format(time.RFC3339), ""})
	}
}

func envInt(name string) int {
	n, _ := strconv.Atoi(os.Getenv(name))
	return n
}

func TestBackfillScale(t *testing.T) {
	sessions := envInt("GHOSTTREE_BACKFILL_SCALE_SESSIONS")
	if sessions == 0 {
		t.Skip("set GHOSTTREE_BACKFILL_SCALE_SESSIONS to run the measurement")
	}
	dir := os.Getenv("GHOSTTREE_BACKFILL_SCALE_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "scale.db")
	if _, err := os.Stat(path); err != nil {
		buildScaleDB(t, path, sessions)
	}
	if fi, err := os.Stat(path); err == nil {
		t.Logf("database: %.1f MiB", float64(fi.Size())/(1<<20))
	}
	run := func(name string, opts store.BackfillGitOptions) store.BackfillGitReport {
		opts.Scan = func(h, e string) store.GitLineScanner { return NewHistoryScan(h, e) }
		var worst time.Duration
		var waits []time.Duration
		var mu sync.Mutex
		stop := make(chan struct{})
		var wg sync.WaitGroup
		probe, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(30000)")
		if err != nil {
			t.Fatal(err)
		}
		defer probe.Close()
		// Ein Schreiber daneben, wie der Server: alle 5 ms ein kleiner Schreibzugriff. Seine
		// Wartezeit ist die, die der Lauf dem Server zumutet.
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				case <-time.After(5 * time.Millisecond):
				}
				began := time.Now()
				if _, err := probe.Exec(`INSERT INTO index_state(key,val) VALUES('scale_probe',1) ON CONFLICT(key) DO UPDATE SET val=val+1`); err != nil {
					t.Errorf("probe: %v", err)
					return
				}
				d := time.Since(began)
				mu.Lock()
				waits = append(waits, d)
				if d > worst {
					worst = d
				}
				mu.Unlock()
			}
		}()
		rep, err := store.BackfillSessionGit(context.Background(), path, opts)
		close(stop)
		wg.Wait()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		mu.Lock()
		defer mu.Unlock()
		p99 := time.Duration(0)
		if len(waits) > 0 {
			s := append([]time.Duration(nil), waits...)
			for i := 1; i < len(s); i++ {
				for j := i; j > 0 && s[j] < s[j-1]; j-- {
					s[j], s[j-1] = s[j-1], s[j]
				}
			}
			p99 = s[int(math.Min(float64(len(s)-1), float64(len(s))*0.99))]
		}
		t.Logf("%s: %d sessions, %d lines, found %v; recorded %d, reported %d, matched %d; took %s (read %s, match %s, count %s)",
			name, rep.Sessions, rep.Chunks, rep.Found, rep.Recorded, rep.ReportedAdded, rep.CommitsMatched,
			rep.Duration.Round(time.Millisecond), rep.ScanTook.Round(time.Millisecond), rep.MatchTook.Round(time.Millisecond), rep.CoverageTook.Round(time.Millisecond))
		t.Logf("%s: %.0f lines/s; %d write transactions, lock p50 %s p95 %s max %s, holding the lock %s in total",
			name, float64(rep.Chunks)/rep.ScanTook.Seconds(), rep.Batches, rep.LockP50, rep.LockP95, rep.LockMax, rep.Write.Round(time.Millisecond))
		t.Logf("%s: a writer beside it (5 ms interval, %d writes) waited at most %s, p99 %s", name, len(waits), worst, p99)
		t.Logf("%s: coverage %+v", name, rep.Coverage)
		return rep
	}
	dry := run("dry run", store.BackfillGitOptions{DryRun: true})
	real := run("real run", store.BackfillGitOptions{})
	run("second run", store.BackfillGitOptions{})
	if dry.Recorded != real.Recorded || dry.CommitsMatched != real.CommitsMatched || dry.Coverage != real.Coverage {
		t.Errorf("the dry run predicts the run:\n dry  %+v\n real %+v", dry, real)
	}
	if real.LockMax > 50*time.Millisecond {
		t.Errorf("a transaction held the write lock for %s, the aim is below 50 ms", real.LockMax)
	}
}

func buildScaleDB(t *testing.T, path string, sessions int) {
	t.Helper()
	began := time.Now()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	g := &scaleGen{rng: rand.New(rand.NewSource(20261111))}
	base := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	total := 0
	for i := 0; i < sessions; i++ {
		harness := "claude-code"
		if g.rng.Intn(5) == 0 {
			harness = "codex"
		}
		ext := fmt.Sprintf("%08x-0000-4000-8000-%012x", i, g.rng.Int63n(1<<40))
		branch := "feat/x" + strconv.Itoa(i%97)
		start := base.Add(time.Duration(i) * 11 * time.Minute)
		// Etwa dreihundert Zeilen im Mittel, lang nach rechts verteilt.
		n := int(math.Exp(5.3+g.rng.NormFloat64()*0.7)) &^ 1
		n = min(max(n, 20), 3000)
		lines := g.session(ext, branch, harness, start, n, g.rng.Intn(100) < 13)
		id, err := st.UpsertSession(store.Session{Harness: harness, ExternalID: ext, AccountID: 1,
			Scope: scope.Axes{Project: "github.com/example-org/example-repo", Branch: branch}, StartedAt: cl(start)})
		if err != nil {
			t.Fatal(err)
		}
		for off := 0; off < len(lines); off += 200 {
			var chunks []store.Chunk
			for seq := off; seq < min(off+200, len(lines)); seq++ {
				chunks = append(chunks, store.Chunk{Seq: seq, Raw: lines[seq]})
			}
			if err := st.AppendChunks(id, chunks); err != nil {
				t.Fatal(err)
			}
		}
		total += len(lines)
		if (i+1)%500 == 0 {
			t.Logf("generated %d sessions, %d lines, %s", i+1, total, time.Since(began).Round(time.Second))
		}
	}
	st.Close()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(30000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	project := "github.com/example-org/example-repo"
	for _, p := range g.prs {
		if _, err := tx.Exec(`INSERT INTO git_pull_requests(project,number,state,head_ref,opened_at,updated_at) VALUES(?,?,?,?,?,?)`,
			project, p[0], p[2], p[1], "2026-10-01T00:00:00Z", "2026-10-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	for i, c := range g.prCommits {
		if _, err := tx.Exec(`INSERT INTO git_pr_commits(project,pr,sha,author,committed_at,position,recorded_at) VALUES(?,?,?,?,?,?,?)`,
			project, c[1], c[0], c[3], c[2], i, "2026-10-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	// Rauschen: Commits ohne Session, quer über die Zeit, einige in fremde Fenster.
	noise := len(g.prCommits) * 2
	for i := 0; i < noise; i++ {
		at := base.Add(time.Duration(g.rng.Int63n(int64(time.Duration(sessions) * 11 * time.Minute))))
		if _, err := tx.Exec(`INSERT INTO git_pr_commits(project,pr,sha,author,committed_at,position,recorded_at) VALUES(?,?,?,?,?,?,?)`,
			project, 1+g.rng.Intn(len(g.prs)+1), g.hex(40), "", at.Format(time.RFC3339), 0, "2026-10-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	t.Logf("built %d sessions, %d lines, %d commit calls (%d quiet), %d PRs, %d PR commits (+%d noise) in %s",
		sessions, total, g.gitCalls, g.quietCalls, len(g.prs), len(g.prCommits), noise, time.Since(began).Round(time.Second))
}
