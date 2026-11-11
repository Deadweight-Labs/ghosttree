// Command hookbench measures what the two hook routes of the server cost on an
// archive of the size of a production one (PAIN-76): GET /api/context/relevant
// (every prompt) and GET /api/context/bootstrap (every session start).
//
//	hookbench gen -db /tmp/hookbench.db            # about 4,500 entries, 3 million chunks
//	hookbench run -db /tmp/hookbench.db -n 300     # p50/p95 per route and per phase
//
// The database is synthetic and deterministic: a Zipf vocabulary filled into
// sessions, knowledge entries with evidence and project ties, requests with
// criteria, a work package and open tasks. It holds nothing of a real archive.
// gen takes minutes for the full size; run reads the file and writes only what
// the hooks write themselves (delivery memory).
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	_ "modernc.org/sqlite"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const (
	vocabularySize = 150000
	benchProject   = "github.com/example/hookbench"
	benchBranch    = "feat/hook-latency"
	benchMachine   = "benchbox"
)

var projects = []string{
	benchProject, "github.com/example/api", "github.com/example/web",
	"github.com/example/infra", "github.com/example/docs", "github.com/example/tools",
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "gen":
		err = generate(os.Args[2:])
	case "run":
		err = runBench(os.Args[2:], os.Stdout)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: hookbench gen|run -db PATH [flags]")
	os.Exit(2)
}

func vocabulary() []string {
	words := make([]string, vocabularySize)
	for i := range words {
		words[i] = "w" + strconv.FormatInt(int64(i)+100000, 36)
	}
	return words
}

// zipfText draws words the way a language is spoken: a few very common, a long
// tail of rare ones.
type zipfText struct {
	words []string
	z     *rand.Zipf
	rng   *rand.Rand
}

func newZipfText(seed int64) *zipfText {
	rng := rand.New(rand.NewSource(seed))
	return &zipfText{words: vocabulary(), z: rand.NewZipf(rng, 1.07, 1, vocabularySize-1), rng: rng}
}

func (t *zipfText) sentence(n int) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(t.words[t.z.Uint64()])
	}
	return sb.String()
}

func generate(args []string) error {
	fs := flag.NewFlagSet("gen", flag.ContinueOnError)
	dbPath := fs.String("db", "", "new database file")
	chunks := fs.Int("chunks", 3000000, "session chunks")
	sessions := fs.Int("sessions", 1600, "sessions")
	entries := fs.Int("knowledge", 4500, "knowledge entries")
	requests := fs.Int("requests", 650, "requests")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dbPath == "" {
		return fmt.Errorf("-db is required")
	}
	if _, err := os.Stat(*dbPath); err == nil {
		return fmt.Errorf("%s exists", *dbPath)
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	started := time.Now()
	token, err := st.AddPerson("bench")
	if err != nil {
		return err
	}
	if _, err := st.AddPerson("friend"); err != nil {
		return err
	}
	if err := os.WriteFile(*dbPath+".token", []byte(token), 0o600); err != nil {
		return err
	}
	text := newZipfText(1)

	ids, err := generateSessions(st, text, *sessions, *chunks)
	if err != nil {
		return err
	}
	fmt.Printf("sessions and chunks: %s\n", time.Since(started).Round(time.Second))
	// Rows the store has no call for are written on a connection of their own; the
	// store's writer is idle between batches.
	db, err := sql.Open("sqlite", "file:"+*dbPath+"?_pragma=busy_timeout(30000)&_pragma=synchronous(OFF)")
	if err != nil {
		return err
	}
	defer db.Close()
	if err := generateKnowledge(db, text, ids, *entries); err != nil {
		return err
	}
	fmt.Printf("knowledge: %s\n", time.Since(started).Round(time.Second))
	if err := generateRequests(st, db, text, *requests); err != nil {
		return err
	}
	if err := generateDeliveries(db, text, ids); err != nil {
		return err
	}
	fmt.Printf("done: %s\n", time.Since(started).Round(time.Second))
	return nil
}

// generateSessions writes the transcripts the way the collector does: a session
// is written in batches while it runs, a few sessions at a time, so the chunks of
// one session sit close together in the index.
func generateSessions(st *store.Store, text *zipfText, count, chunks int) ([]int64, error) {
	ids := make([]int64, count)
	sizes := make([]int, count)
	total := 0
	for i := range sizes {
		sizes[i] = int(math.Exp(text.rng.NormFloat64()*1.1 + 6.5)) // median about 650
		total += sizes[i]
	}
	scale := float64(chunks) / float64(total)
	day := 24 * time.Hour
	for i := range ids {
		sizes[i] = max(4, int(float64(sizes[i])*scale))
		harness := "claude-code"
		if i%5 == 0 {
			harness = "codex"
		}
		account := int64(1)
		if i%17 == 0 {
			account = 2
		}
		started := time.Now().Add(-time.Duration(count-i) * day / 20)
		var err error
		ids[i], err = st.UpsertSession(store.Session{
			Harness: harness, ExternalID: fmt.Sprintf("bench-%05d", i), AccountID: account,
			Scope:     scope.Axes{Project: projects[i%len(projects)], Branch: "feat/b" + strconv.Itoa(i%40), Machine: benchMachine},
			StartedAt: started.UTC().Format(time.RFC3339),
		})
		if err != nil {
			return nil, err
		}
	}
	written := 0
	seq := make([]int, count)
	const live, batch = 3, 400
	for base := 0; base < count; base += live {
		for {
			progressed := false
			for k := base; k < min(base+live, count); k++ {
				if seq[k] >= sizes[k] {
					continue
				}
				n := min(batch, sizes[k]-seq[k])
				list := make([]store.Chunk, n)
				for j := range list {
					length := 6 + text.rng.Intn(40)
					role := "assistant"
					if text.rng.Intn(4) == 0 {
						role = "user"
					}
					body := text.sentence(length)
					list[j] = store.Chunk{Seq: seq[k] + j, Role: role, Text: body, Raw: rawLine(role, body)}
				}
				if err := st.AppendChunks(ids[k], list); err != nil {
					return nil, err
				}
				seq[k] += n
				written += n
				progressed = true
			}
			if !progressed {
				break
			}
		}
		if (base/live)%40 == 0 {
			fmt.Printf("  %d/%d chunks\n", written, chunks)
		}
	}
	return ids, nil
}

func rawLine(role, body string) string {
	var v any
	if role == "user" {
		v = map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": body}}
	} else {
		v = map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": body}}}}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func generateKnowledge(db *sql.DB, text *zipfText, sessions []int64, count int) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	at := time.Now().UTC().Format(time.RFC3339)
	types := []string{"pitfall", "pitfall", "pitfall", "decision", "decision", "note", "note", "plan"}
	for i := 0; i < count; i++ {
		typ := types[i%len(types)]
		if i%60 == 0 {
			typ = "instruction"
		}
		project, machine := projects[text.rng.Intn(len(projects))], ""
		switch r := text.rng.Intn(100); {
		case r < 22:
			project, machine = "", benchMachine // machine knowledge
		case r < 34:
			project = "" // global
		case r < 40:
			machine = benchMachine
		}
		confidence := []string{"trusted", "trusted", "verified", "staged"}[text.rng.Intn(4)]
		origin := []string{"agent", "distilled", "distilled", "human"}[text.rng.Intn(4)]
		status := "active"
		if text.rng.Intn(10) == 0 {
			status = "superseded"
		}
		title := text.sentence(5 + text.rng.Intn(7))
		body := text.sentence(40 + text.rng.Intn(180))
		res, err := tx.Exec(`INSERT INTO knowledge(type,title,body,project,branch,machine,confidence,status,origin,person,harness,
			session_ref,last_modified_by,last_used_at,hit_count,search_hits,observed_at,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			typ, title, body, project, "", machine, confidence, status, origin, "bench", "claude-code",
			"session:"+strconv.FormatInt(sessions[text.rng.Intn(len(sessions))], 10), "bench", at, text.rng.Intn(40), text.rng.Intn(10), at, at, at)
		if err != nil {
			return err
		}
		kid, _ := res.LastInsertId()
		// Evidence of distilled entries: three to twelve quotes from sessions.
		if origin == "distilled" {
			for e := 0; e < 3+text.rng.Intn(10); e++ {
				if _, err := tx.Exec(`INSERT OR IGNORE INTO knowledge_evidence(knowledge_id,session_id,chunk_seq,quote) VALUES(?,?,?,?)`,
					kid, sessions[text.rng.Intn(len(sessions))], text.rng.Intn(300), text.sentence(12)); err != nil {
					return err
				}
			}
		}
	}
	return tx.Commit()
}

func generateRequests(st *store.Store, db *sql.DB, text *zipfText, count int) error {
	var open []requestdomain.PackageMember
	for i := 0; i < count; i++ {
		project := projects[i%len(projects)]
		criteria := make([]string, 2+text.rng.Intn(5))
		for c := range criteria {
			criteria[c] = text.sentence(10 + text.rng.Intn(12))
		}
		d, err := st.CreateRequest(requestdomain.CreateInput{
			Request: requestdomain.Request{
				Type: "feature", Title: text.sentence(6 + text.rng.Intn(8)), Description: text.sentence(60 + text.rng.Intn(120)),
				Scope: scope.Axes{Project: project}, Origin: "agent", Person: "bench",
			},
			Criteria: criteria,
		})
		if err != nil {
			return err
		}
		if i%12 != 0 { // about one in twelve stays open: 53 of 650
			if _, err := db.Exec(`UPDATE requests SET state='done' WHERE id=?`, d.Request.ID); err != nil {
				return err
			}
			continue
		}
		if project == benchProject {
			open = append(open, requestdomain.PackageMember{RequestID: d.Request.ID, Role: requestdomain.RolePflicht})
		}
	}
	if len(open) > 0 {
		if _, err := st.CreatePackage(requestdomain.PackageInput{Project: benchProject, Branch: benchBranch, Name: "hook latency", Members: open, Person: "bench"}); err != nil {
			return err
		}
	}
	// Tasks that wait for a person, on the requests of the project.
	rows, err := db.Query(`SELECT id FROM requests WHERE project=? LIMIT 40`, benchProject)
	if err != nil {
		return err
	}
	var rid []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		rid = append(rid, id)
	}
	rows.Close()
	at := time.Now().UTC().Format(time.RFC3339)
	for i, id := range rid {
		state := []string{"open", "open", "done", "confirmed"}[i%4]
		if _, err := db.Exec(`INSERT INTO human_tasks(project,request_id,kind,title,description,state,assignee_account,created_by_account,created_at,updated_at)
			VALUES(?,?,'action',?,?,?,1,1,?,?)`, benchProject, id, text.sentence(8), text.sentence(30), state, at, at); err != nil {
			return err
		}
	}
	return nil
}

// generateDeliveries fills the delivery memory and the delivery log the way a
// month of hooks leaves them behind: the memory ages out after a week, the log
// is kept.
func generateDeliveries(db *sql.DB, text *zipfText, sessions []int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	for i := 0; i < 60000; i++ {
		at := now.Add(-time.Duration(text.rng.Intn(6*24*60)) * time.Minute).Format(time.RFC3339)
		if _, err := tx.Exec(`INSERT OR IGNORE INTO context_deliveries(account_id,session_key,knowledge_id,term,via,delivered_at) VALUES('person:1',?,?,'', 'bootstrap',?)`,
			fmt.Sprintf("bench-%05d", text.rng.Intn(len(sessions))), 1+text.rng.Intn(4000), at); err != nil {
			return err
		}
	}
	for i := 0; i < 120000; i++ {
		at := now.Add(-time.Duration(text.rng.Intn(30*24*60)) * time.Minute).Format(time.RFC3339)
		if _, err := tx.Exec(`INSERT INTO context_delivery_log(account_id,session_key,project,harness,via,kind,object_id,chars,delivered_at) VALUES(1,?,?,'claude','bootstrap','knowledge',?,300,?)`,
			fmt.Sprintf("bench-%05d", text.rng.Intn(len(sessions))), projects[i%len(projects)], 1+text.rng.Intn(4000), at); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// runBench serves the database like the server does (writer plus a pool of read
// connections), calls the two hook routes and reports the latency the client saw
// and, from the http_request log lines, the time of every phase.
func runBench(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	dbPath := fs.String("db", "", "database made by gen")
	n := fs.Int("n", 300, "calls per route and scenario")
	searches := fs.Int("searches", 0, "searches running at the same time during the measurement")
	collectors := fs.Int("collectors", 0, "collector uploads running at the same time (chunk batches)")
	judge := fs.Bool("judge", false, "also run the usage job in a loop")
	conns := fs.Int("read-connections", 3, "read connections, as in production")
	only := fs.String("route", "", "relevant or bootstrap (default both)")
	jsonOut := fs.String("json", "", "write the result as JSON to this file")
	cpuProfile := fs.String("cpuprofile", "", "write a CPU profile of the measured calls to this file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	tokenBytes, err := os.ReadFile(*dbPath + ".token")
	if err != nil {
		return err
	}
	token := strings.TrimSpace(string(tokenBytes))
	cfg := store.DefaultWriterConfig()
	cfg.ReadConnections = *conns
	st, err := store.OpenRuntime(*dbPath, cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	st.SetAccessMode(store.AccessMode{Enforce: true})
	logs := &logSink{}
	srv := httptest.NewServer(server.New(st, server.WithLogger(slog.New(slog.NewJSONHandler(logs, nil)))))
	defer srv.Close()
	client := &http.Client{Timeout: time.Minute, Transport: &http.Transport{MaxIdleConnsPerHost: 64}}
	get := func(path string, q url.Values) (time.Duration, int) {
		req, _ := http.NewRequest("GET", srv.URL+path+"?"+q.Encode(), nil)
		req.Header.Set("Authorization", "Bearer "+token)
		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			return 0, 0
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return time.Since(start), resp.StatusCode
	}

	text := newZipfText(99)
	prompt := func(i int) url.Values {
		var q string
		switch i % 10 {
		case 0: // a short reply
			q = "ja bitte mach das " + text.sentence(2)
		case 1: // a codename nobody has written down
			q = "was ist qzxjkvwp " + text.sentence(6)
		case 2: // asks about earlier work
			q = "was hatten wir damals zu " + text.sentence(5) + " gesagt"
		default:
			q = text.sentence(8 + text.rng.Intn(18))
		}
		return url.Values{"q": {q}, "session": {"hb-" + strconv.Itoa(i)}, "project": {benchProject}, "branch": {benchBranch}, "machine": {benchMachine}}
	}
	bootstrap := func(i int) url.Values {
		q := url.Values{"project": {benchProject}, "branch": {benchBranch}, "machine": {benchMachine}, "session": {"hs-" + strconv.Itoa(i)}}
		if i%4 == 3 {
			q.Set("profile", "lean")
		}
		return q
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	common := text.sentence(2)
	for i := 0; i < *searches; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			kinds := []string{"all", "sessions", "knowledge"}
			for {
				select {
				case <-stop:
					return
				default:
				}
				get("/api/search", url.Values{"q": {common}, "kind": {kinds[i%len(kinds)]}, "limit": {"20"}})
			}
		}(i)
	}
	var uploaded atomic.Int64
	for i := 0; i < *collectors; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id, err := st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: fmt.Sprintf("live-%d-%d", os.Getpid(), i), AccountID: 1, StartedAt: time.Now().UTC().Format(time.RFC3339)})
			if err != nil {
				return
			}
			rng := newZipfText(int64(i) + 7)
			for seq := 0; ; seq += 200 {
				select {
				case <-stop:
					return
				default:
				}
				list := make([]store.Chunk, 200)
				for j := range list {
					body := rng.sentence(30)
					list[j] = store.Chunk{Seq: seq + j, Role: "assistant", Text: body, Raw: rawLine("assistant", body)}
				}
				if st.AppendChunks(id, list) == nil {
					uploaded.Add(200)
				}
				time.Sleep(100 * time.Millisecond)
			}
		}(i)
	}
	if *judge {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := st.JudgeDeliveries(context.Background(), store.JudgeOptions{}); err != nil {
					time.Sleep(time.Second)
				}
				time.Sleep(200 * time.Millisecond)
			}
		}()
	}
	if *searches+*collectors > 0 || *judge {
		time.Sleep(500 * time.Millisecond)
	}

	if *cpuProfile != "" {
		f, err := os.Create(*cpuProfile)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			return err
		}
		defer pprof.StopCPUProfile()
	}
	result := map[string]any{}
	for _, route := range []struct {
		name, path string
		make       func(int) url.Values
	}{{"relevant", "/api/context/relevant", prompt}, {"bootstrap", "/api/context/bootstrap", bootstrap}} {
		if *only != "" && *only != route.name {
			continue
		}
		logs.reset()
		poolBefore := st.RuntimeStats().Reader
		cpuBefore := cpuTime()
		lat := make([]time.Duration, 0, *n)
		for i := 0; i < *n; i++ {
			d, status := get(route.path, route.make(i))
			if status != 200 {
				return fmt.Errorf("%s: status %d", route.name, status)
			}
			lat = append(lat, d)
		}
		fmt.Fprintf(out, "\n%s  n=%d searches=%d collectors=%d judge=%v\n", route.name, *n, *searches, *collectors, *judge)
		fmt.Fprintf(out, "  client   %s\n", summary(lat))
		cpuPerCall := (cpuTime() - cpuBefore) / time.Duration(*n)
		fmt.Fprintf(out, "  cpu per call (whole process, client and server): %.2f ms\n", ms(cpuPerCall))
		poolAfter := st.RuntimeStats().Reader
		waits := poolAfter.WaitCount - poolBefore.WaitCount
		waited := poolAfter.WaitDuration - poolBefore.WaitDuration
		fmt.Fprintf(out, "  read pool (all callers): %d of its connections waited for, %.1f ms in total\n", waits, ms(waited))
		phases := logs.phases(route.path)
		names := make([]string, 0, len(phases))
		for name := range phases {
			names = append(names, name)
		}
		slices.SortFunc(names, func(a, b string) int { return -cmpFloat(mean(phases[a]), mean(phases[b])) })
		res := map[string]any{"client": summaryMap(lat)}
		pm := map[string]any{}
		for _, name := range names {
			v := phases[name]
			fmt.Fprintf(out, "  %-18s p50 %7.2f  p95 %7.2f  max %7.2f  mean %7.2f ms (n=%d)\n", name, pct(v, 50), pct(v, 95), pct(v, 100), mean(v), len(v))
			pm[name] = map[string]float64{"p50": pct(v, 50), "p95": pct(v, 95), "max": pct(v, 100), "mean": mean(v)}
		}
		res["phases"] = pm
		result[route.name] = res
	}
	close(stop)
	wg.Wait()
	if *collectors > 0 {
		fmt.Fprintf(out, "\ncollector chunks written during the run: %d\n", uploaded.Load())
	}
	if *jsonOut != "" {
		b, _ := json.MarshalIndent(result, "", "  ")
		return os.WriteFile(*jsonOut, b, 0o644)
	}
	return nil
}

// cpuTime is the processor time the process has used so far. Unlike the wall
// clock it does not grow when other programs share the machine.
func cpuTime() time.Duration {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func summary(lat []time.Duration) string {
	s := summaryMap(lat)
	return fmt.Sprintf("p50 %7.2f  p95 %7.2f  p99 %7.2f  max %7.2f ms", s["p50"], s["p95"], s["p99"], s["max"])
}

func summaryMap(lat []time.Duration) map[string]float64 {
	v := make([]float64, len(lat))
	for i, d := range lat {
		v[i] = ms(d)
	}
	return map[string]float64{"p50": pct(v, 50), "p95": pct(v, 95), "p99": pct(v, 99), "max": pct(v, 100), "mean": mean(v)}
}

func pct(v []float64, p int) float64 {
	if len(v) == 0 {
		return 0
	}
	s := slices.Clone(v)
	slices.Sort(s)
	i := (len(s)*p + 99) / 100
	return s[max(0, min(len(s)-1, i-1))]
}

func mean(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	t := 0.0
	for _, x := range v {
		t += x
	}
	return t / float64(len(v))
}

// logSink keeps the http_request lines of the server.
type logSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logSink) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logSink) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Reset()
}

// phases returns, per phase, the milliseconds of every call of the route. A call
// that did not reach a phase counts as zero for it, so the figures are those of
// a call and not of the calls that happened to run the phase.
func (l *logSink) phases(route string) map[string][]float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	var lines []map[string]float64
	names := map[string]bool{}
	for _, line := range bytes.Split(l.buf.Bytes(), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var ev struct {
			Event  string             `json:"event"`
			Route  string             `json:"route"`
			Millis float64            `json:"duration_ms"`
			Phases map[string]float64 `json:"phases"`
		}
		if json.Unmarshal(line, &ev) != nil || ev.Event != "http_request" || ev.Route != route {
			continue
		}
		ev.Phases["total"] = ev.Millis
		lines = append(lines, ev.Phases)
		for k := range ev.Phases {
			names[k] = true
		}
	}
	out := map[string][]float64{}
	for k := range names {
		for _, p := range lines {
			out[k] = append(out[k], p[k])
		}
	}
	return out
}
