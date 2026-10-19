package garden

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/llm"
)

func TestPartitionKeepsTopicsTogetherAndBalances(t *testing.T) {
	repo := &Repo{files: []string{"internal/store/a.go", "internal/store/b.go", "internal/web/x.go", "cmd/ctx/main.go", "README.md"}}
	var es []Entry
	for i := int64(1); i <= 4; i++ {
		es = append(es, Entry{ID: i, Title: "store", Body: "siehe internal/store/a.go"})
	}
	for i := int64(5); i <= 6; i++ {
		es = append(es, Entry{ID: i, Title: "web", Body: "siehe internal/web/x.go"})
	}
	es = append(es, Entry{ID: 7, Title: "ohne", Body: "nichts Konkretes"})

	pkgs := Partition(repo, es, 4, 4)
	if len(pkgs) != 2 {
		t.Fatalf("7 entries at 4 per session need 2 packages, got %d", len(pkgs))
	}
	for _, p := range pkgs {
		if len(p.Entries) > 4 || len(p.Entries) == 0 {
			t.Errorf("package %q has %d entries", p.Name, len(p.Entries))
		}
	}
	// The four store entries are one topic and fit one package.
	where := map[int64]int{}
	for i, p := range pkgs {
		for _, e := range p.Entries {
			where[e.ID] = i
		}
	}
	for id := int64(2); id <= 4; id++ {
		if where[id] != where[1] {
			t.Errorf("store entries split: #%d in %d, #1 in %d", id, where[id], where[1])
		}
	}
	if len(where) != 7 {
		t.Errorf("every entry must be in exactly one package, got %d", len(where))
	}
}

func TestPartitionNeedsNoMorePackagesThanEntries(t *testing.T) {
	repo := &Repo{files: []string{"a/b/c.go"}}
	pkgs := Partition(repo, []Entry{{ID: 1, Body: "a/b/c.go"}, {ID: 2, Body: "a/b/c.go"}}, 4, 30)
	if len(pkgs) != 1 || len(pkgs[0].Entries) != 2 {
		t.Fatalf("%+v", pkgs)
	}
	if Partition(repo, nil, 4, 30) != nil {
		t.Error("no entries, no packages")
	}
}

func TestPartitionPutsReflessEntryWithItsTopicByWords(t *testing.T) {
	repo := &Repo{files: []string{"internal/llm/x.go", "internal/web/y.go"}}
	es := []Entry{
		{ID: 1, Title: "proxy tunnel", Body: "internal/llm/x.go tunnel loopback proxy hostname resolves"},
		{ID: 2, Title: "web", Body: "internal/web/y.go"},
		{ID: 3, Title: "tunnel", Body: "tunnel loopback proxy hostname"},
	}
	pkgs := Partition(repo, es, 2, 2)
	for _, p := range pkgs {
		ids := map[int64]bool{}
		for _, e := range p.Entries {
			ids[e.ID] = true
		}
		if ids[3] && !ids[1] {
			t.Errorf("entry 3 shares its words with entry 1 but is in another package: %+v", pkgs)
		}
	}
}

func head(t *testing.T, repo *Repo) string {
	t.Helper()
	h, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestValidateAnswerHoldsCitationsToTheCheckout(t *testing.T) {
	repo := newRepo(t)
	pkg := Package{Name: "p", Entries: []Entry{entry(1, "a"), entry(2, "b"), entry(3, "c"), entry(4, "d")}}
	text := fmt.Sprintf("Fertig.\n```json\n{\"verdicts\":["+
		`{"id":1,"verdict":"stale","reason":"weg","evidence":["internal/distill.go:3"]},`+
		`{"id":2,"verdict":"stale","reason":"erfunden","evidence":["internal/distill.go:99","internal/nope.go:1","deadbeefdeadbeef"]},`+
		`{"id":3,"verdict":"correction","reason":"x","evidence":["%s"],"correction":"neu"},`+
		`{"id":9,"verdict":"holds","reason":"fremd"},`+
		`{"id":1,"verdict":"holds","reason":"doppelt"}`+
		"]}\n```", head(t, repo))
	v, err := ValidateAnswer(repo, text, pkg, nil)
	if err != nil {
		t.Fatal(err)
	}
	kind := map[int64]string{}
	for _, x := range v.Verdicts {
		kind[x.Entry.ID] = x.Kind
	}
	want := map[int64]string{1: Stale, 2: Unclear, 3: Correction, 4: NotRun}
	for id, k := range want {
		if kind[id] != k {
			t.Errorf("#%d: %q, want %q", id, kind[id], k)
		}
	}
	if len(v.Verdicts) != 4 {
		t.Errorf("one verdict per package entry, got %d", len(v.Verdicts))
	}
	if len(v.Rejected) != 2 {
		t.Errorf("foreign and doubled verdict must be rejected: %v", v.Rejected)
	}
	for _, x := range v.Verdicts {
		if x.Entry.ID == 2 && len(x.Unverified) != 3 {
			t.Errorf("unverified citations not kept: %v", x.Unverified)
		}
	}
}

func TestValidateAnswerNotJSONIsAnError(t *testing.T) {
	repo := newRepo(t)
	for _, in := range []string{"", "ich bin fertig", `{"foo":1}`, "{kaputt}"} {
		if _, err := ValidateAnswer(repo, in, Package{Entries: []Entry{entry(1, "a")}}, nil); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestValidateRelationNeedsVerbatimQuotesFromEveryEntry(t *testing.T) {
	repo := newRepo(t)
	a := Entry{ID: 1, Title: "Eins", Body: "Der Dienst läuft   auf orax\nund nirgends sonst."}
	b := Entry{ID: 2, Title: "Zwei", Body: "Der Dienst läuft auf apps-vm."}
	known := map[int64]Entry{1: a, 2: b, 3: {ID: 3, Title: "Drei", Body: "unrelated text"}}
	pkg := Package{Entries: []Entry{a, b}}
	mk := func(rel string) string {
		return `{"verdicts":[],"relations":[` + rel + `]}`
	}
	good := mk(`{"kind":"contradiction","ids":[2,1],"reason":"Ort","quotes":[{"id":1,"text":"Dienst läuft auf orax"},{"id":2,"text":"der dienst läuft auf apps-vm"}]}`)
	v, err := ValidateAnswer(repo, good, pkg, known)
	if err != nil || len(v.Relations) != 1 || len(v.Rejected) != 0 {
		t.Fatalf("%+v %v", v, err)
	}
	if r := v.Relations[0]; r.IDs[0] != 1 || r.IDs[1] != 2 || r.Quotes[1] == "" {
		t.Errorf("%+v", r)
	}
	for name, rel := range map[string]string{
		"missing quote":   `{"kind":"duplicate","ids":[1,2],"reason":"x","quotes":[{"id":1,"text":"Dienst läuft auf orax"}]}`,
		"invented quote":  `{"kind":"duplicate","ids":[1,2],"reason":"x","quotes":[{"id":1,"text":"Dienst läuft auf orax"},{"id":2,"text":"nicht im Eintrag enthalten"}]}`,
		"short quote":     `{"kind":"duplicate","ids":[1,2],"reason":"x","quotes":[{"id":1,"text":"Dienst"},{"id":2,"text":"Dienst"}]}`,
		"unknown entry":   `{"kind":"duplicate","ids":[1,77],"reason":"x","quotes":[]}`,
		"one entry":       `{"kind":"duplicate","ids":[1,1],"reason":"x","quotes":[{"id":1,"text":"Dienst läuft auf orax"}]}`,
		"no reason":       `{"kind":"duplicate","ids":[1,2],"reason":"","quotes":[{"id":1,"text":"Dienst läuft auf orax"},{"id":2,"text":"Dienst läuft auf apps-vm"}]}`,
		"bad kind":        `{"kind":"related","ids":[1,2],"reason":"x","quotes":[]}`,
		"quote of 3 only": `{"kind":"duplicate","ids":[1,3],"reason":"x","quotes":[{"id":3,"text":"unrelated text"}]}`,
	} {
		v, err := ValidateAnswer(repo, mk(rel), pkg, known)
		if err != nil || len(v.Relations) != 0 || len(v.Rejected) != 1 {
			t.Errorf("%s: relations=%d rejected=%v err=%v", name, len(v.Relations), v.Rejected, err)
		}
	}
}

const streamFixture = `{"type":"system","subtype":"init","model":"claude-sonnet-5-5"}
{"type":"assistant","message":{"id":"m1","usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":1000}}}
{"type":"assistant","message":{"id":"m1","usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":1000}}}
{"type":"user","message":{"content":"x"}}
{"type":"assistant","message":{"id":"m2","content":[{"type":"tool_use","id":"t1","name":"Grep"},{"type":"tool_use","id":"t2","name":"mcp__ghosttree__context_search"}],"usage":{"input_tokens":20,"output_tokens":7,"cache_read_input_tokens":1100,"cache_creation_input_tokens":0}}}
{"type":"assistant","message":{"id":"m2","usage":{"input_tokens":20,"output_tokens":7,"cache_read_input_tokens":1100,"cache_creation_input_tokens":0}}}
{"type":"result","subtype":"success","is_error":false,"num_turns":3,"result":"{\"verdicts\":[]}","total_cost_usd":0.5,"permission_denials":[{"tool_name":"Bash"}],"usage":{"input_tokens":30,"output_tokens":12,"cache_read_input_tokens":1200,"cache_creation_input_tokens":1000}}
`

func TestParseStreamTakesUsageIncludingCacheFromTheResult(t *testing.T) {
	r, err := ParseStream(strings.NewReader(streamFixture))
	if err != nil {
		t.Fatal(err)
	}
	want := llm.Usage{InputTokens: 30, OutputTokens: 12, CacheReadTokens: 1200, CacheWriteTokens: 1000}
	if r.Tools["Grep"] != 1 || r.Tools["mcp__ghosttree__context_search"] != 1 {
		t.Errorf("tools: %v", r.Tools)
	}
	if len(r.DeniedCalls) != 1 || !strings.HasPrefix(r.DeniedCalls[0], "Bash") || r.Usage != want || r.Turns != 3 || r.Denials != 1 || r.ReportedUSD != 0.5 || r.Text != `{"verdicts":[]}` {
		t.Fatalf("%+v", r)
	}
}

func TestParseStreamKilledSessionStillCountsWhatItUsed(t *testing.T) {
	cut := streamFixture[:strings.Index(streamFixture, `{"type":"result"`)]
	r, err := ParseStream(strings.NewReader(cut))
	if err == nil {
		t.Fatal("a stream without result must be an error")
	}
	// m1 counts once, m2 once.
	want := llm.Usage{InputTokens: 30, OutputTokens: 12, CacheReadTokens: 1200, CacheWriteTokens: 1000}
	if r.Usage != want || r.Turns != 2 {
		t.Fatalf("%+v", r)
	}
}

func TestParseStreamErrorSubtypeIsAnErrorWithUsage(t *testing.T) {
	in := `{"type":"result","subtype":"error_max_turns","is_error":false,"num_turns":60,"usage":{"input_tokens":1,"output_tokens":2}}` + "\n"
	r, err := ParseStream(strings.NewReader(in))
	if err == nil || !strings.Contains(err.Error(), "error_max_turns") || r.Usage.OutputTokens != 2 {
		t.Fatalf("%+v %v", r, err)
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

type fakeRunner struct {
	answers []func(s SessionSpec) (SessionResult, error)
	specs   []SessionSpec
}

func (f *fakeRunner) RunSession(_ context.Context, s SessionSpec) (SessionResult, error) {
	f.specs = append(f.specs, s)
	i := len(f.specs) - 1
	if i >= len(f.answers) {
		i = len(f.answers) - 1
	}
	return f.answers[i](s)
}

func holdsAll(s SessionSpec) (SessionResult, error) {
	var vs []string
	for _, l := range strings.Split(s.Prompt, "\n") {
		var id int
		if _, err := fmt.Sscanf(l, "=== EINTRAG #%d", &id); err == nil {
			vs = append(vs, fmt.Sprintf(`{"id":%d,"verdict":"holds","reason":"ok"}`, id))
		}
	}
	return SessionResult{Text: `{"verdicts":[` + strings.Join(vs, ",") + `]}`, Turns: 4,
		Usage: llm.Usage{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 1000, CacheWriteTokens: 2000}}, nil
}

func agentEntries(n int) []Entry {
	var es []Entry
	for i := 1; i <= n; i++ {
		es = append(es, Entry{ID: int64(i), Type: "pitfall", Title: fmt.Sprint("e", i), Body: "internal/distill.go", ObservedAt: "2000-01-01T00:00:00Z"})
	}
	return es
}

func TestCheckAgentRunsOneSessionPerPackageAndMetersEverySession(t *testing.T) {
	repo := newRepo(t)
	fr := &fakeRunner{answers: []func(SessionSpec) (SessionResult, error){holdsAll}}
	var metered []SessionStat
	var judged int
	res := CheckAgent(context.Background(), repo, agentEntries(7), AgentOptions{
		Runner: fr, Project: "p", Pricing: price, MaxUSD: 5, Sessions: 2, PerSession: 4, Turns: 30,
		OnSession: func(s SessionStat, failed bool) {
			if failed {
				t.Error("no failure expected")
			}
			metered = append(metered, s)
		},
		OnJudged: func(Verdict) { judged++ },
	})
	if len(fr.specs) != 2 || len(metered) != 2 || judged != 7 {
		t.Fatalf("sessions=%d metered=%d judged=%d", len(fr.specs), len(metered), judged)
	}
	if len(res.Verdicts) != 7 || res.StoppedBy != "" || res.Deferred != 0 {
		t.Fatalf("%+v", res)
	}
	// 2 sessions at 100 in, 50 out, 1000 read, 2000 written.
	want := 2 * price.CostUsage(llm.Usage{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 1000, CacheWriteTokens: 2000})
	if d := res.Spent - want; d > 1e-9 || d < -1e-9 {
		t.Errorf("spent %f want %f", res.Spent, want)
	}
	for _, s := range fr.specs {
		if s.MaxTurns != 30 || s.BudgetUSD <= 0 || s.BudgetUSD > 5 {
			t.Errorf("spec %+v", s)
		}
	}
}

func TestCheckAgentLimitDefersTheRestAndDryRunCallsNothing(t *testing.T) {
	repo := newRepo(t)
	fr := &fakeRunner{answers: []func(SessionSpec) (SessionResult, error){holdsAll}}
	res := CheckAgent(context.Background(), repo, agentEntries(10), AgentOptions{
		Runner: fr, Pricing: price, MaxUSD: 5, Sessions: 4, PerSession: 30, Turns: 30, Limit: 3,
	})
	if len(res.Verdicts) != 3 || res.Deferred != 7 || len(fr.specs) != 1 {
		t.Fatalf("verdicts=%d deferred=%d sessions=%d", len(res.Verdicts), res.Deferred, len(fr.specs))
	}
	fr2 := &fakeRunner{answers: []func(SessionSpec) (SessionResult, error){holdsAll}}
	res = CheckAgent(context.Background(), repo, agentEntries(10), AgentOptions{
		Runner: fr2, Pricing: price, MaxUSD: 5, Sessions: 4, PerSession: 4, Turns: 30, DryRun: true,
	})
	if len(fr2.specs) != 0 || len(res.Packages) != 3 || !res.DryRun {
		t.Fatalf("dry run: sessions=%d packages=%d", len(fr2.specs), len(res.Packages))
	}
}

func TestCheckAgentFailedSessionIsNotRememberedAndTwoFailuresStop(t *testing.T) {
	repo := newRepo(t)
	boom := func(SessionSpec) (SessionResult, error) {
		return SessionResult{Usage: llm.Usage{InputTokens: 10, OutputTokens: 10}, Turns: 60}, errors.New("die Session endete mit error_max_turns")
	}
	fr := &fakeRunner{answers: []func(SessionSpec) (SessionResult, error){boom}}
	var failed, judged int
	res := CheckAgent(context.Background(), repo, agentEntries(12), AgentOptions{
		Runner: fr, Pricing: price, MaxUSD: 5, Sessions: 4, PerSession: 3, Turns: 30,
		OnSession: func(_ SessionStat, f bool) {
			if f {
				failed++
			}
		},
		OnJudged: func(Verdict) { judged++ },
	})
	if len(fr.specs) != 2 || failed != 2 || judged != 0 || res.StoppedBy != "errors" {
		t.Fatalf("sessions=%d failed=%d judged=%d stopped=%q", len(fr.specs), failed, judged, res.StoppedBy)
	}
	if res.Spent <= 0 {
		t.Error("what a failed session used still counts")
	}
	if len(res.Verdicts) != 12 {
		t.Errorf("every entry has a verdict line, got %d", len(res.Verdicts))
	}
}

func TestCheckAgentBudgetIsSplitAndStopsBeforeASessionWithNothingLeft(t *testing.T) {
	repo := newRepo(t)
	fr := &fakeRunner{answers: []func(SessionSpec) (SessionResult, error){holdsAll}}
	res := CheckAgent(context.Background(), repo, agentEntries(8), AgentOptions{
		Runner: fr, Pricing: price, MaxUSD: 0.005, Sessions: 2, PerSession: 4, Turns: 30,
	})
	if res.StoppedBy != "budget" || len(fr.specs) != 0 {
		t.Fatalf("stopped=%q sessions=%d", res.StoppedBy, len(fr.specs))
	}
}

func TestReportShowsSessionsAndRelations(t *testing.T) {
	repo := newRepo(t)
	a := Entry{ID: 1, Type: "pitfall", Title: "Eins", Body: "internal/distill.go läuft auf orax", ObservedAt: "2000-01-01T00:00:00Z"}
	b := Entry{ID: 2, Type: "pitfall", Title: "Zwei", Body: "internal/distill.go läuft auf apps-vm", ObservedAt: "2000-01-01T00:00:00Z"}
	fr := &fakeRunner{answers: []func(SessionSpec) (SessionResult, error){func(SessionSpec) (SessionResult, error) {
		return SessionResult{Turns: 5, Usage: llm.Usage{InputTokens: 1, OutputTokens: 2, CacheReadTokens: 3, CacheWriteTokens: 4}, Denials: 2,
			Text: `{"verdicts":[{"id":1,"verdict":"holds"},{"id":2,"verdict":"holds"}],"relations":[{"kind":"contradiction","ids":[1,2],"reason":"Ort","quotes":[{"id":1,"text":"läuft auf orax"},{"id":2,"text":"läuft auf apps-vm"}]}]}`}, nil
	}}}
	res := CheckAgent(context.Background(), repo, []Entry{a, b}, AgentOptions{
		Runner: fr, Pricing: price, MaxUSD: 5, Sessions: 1, PerSession: 5, Turns: 30, Known: map[int64]Entry{1: a, 2: b},
	})
	var sb strings.Builder
	WriteReport(&sb, "p", res, parseTime("2026-10-16T03:05:00Z"))
	out := sb.String()
	for _, want := range []string{"Agent-Lauf: 1 Sessions mit 5 Turns", "## Sessions", "2 Werkzeugaufrufe verweigert", "## Duplikate und Widersprüche", `"läuft auf orax"`, "Der Gärtner hat 2 Einträge geprüft, 0 Vorschläge"} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
}

// A stand-in for the claude binary: it records how it was started and answers
// with a stream, so the real process handling runs.
func fakeClaude(t *testing.T, stream string) (bin, record string) {
	t.Helper()
	dir := t.TempDir()
	record = filepath.Join(dir, "record")
	script := "#!/bin/sh\n" +
		"{ pwd; echo \"CFG=$CLAUDE_CONFIG_DIR\"; echo \"KEY=$GHOSTTREE_GARDEN_KEY\"; echo \"CFGLS=$(ls -A \"$CLAUDE_CONFIG_DIR\" | tr '\\n' ' ')\"; env | sed 's/^/ENV:/'; for a in \"$@\"; do echo \"ARG=$a\"; done; echo PROMPT; cat; } > " + record + "\n" +
		"cat <<'EOF'\n" + stream + "EOF\n"
	bin = filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, record
}

func TestClaudeRunnerTimeoutKillsTheSessionAndSaysSo(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	os.WriteFile(bin, []byte("#!/bin/sh\nsleep 30\n"), 0o755)
	r := ClaudeRunner{Config: ClaudeConfig{Bin: bin, RepoRoot: dir, ConfigRoot: t.TempDir(), Timeout: 300 * time.Millisecond}}
	start := time.Now()
	_, err := r.RunSession(context.Background(), SessionSpec{MaxTurns: 20})
	if err == nil || !strings.Contains(err.Error(), "abgebrochen") || time.Since(start) > 10*time.Second {
		t.Fatalf("%v after %v", err, time.Since(start))
	}
}

func TestCheckAgentDiscardsASessionThatChangedTheCheckout(t *testing.T) {
	repo := newRepo(t)
	vandal := func(s SessionSpec) (SessionResult, error) {
		if err := os.WriteFile(filepath.Join(repo.Root, "geschrieben.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return holdsAll(s)
	}
	fr := &fakeRunner{answers: []func(SessionSpec) (SessionResult, error){vandal}}
	var judged int
	res := CheckAgent(context.Background(), repo, agentEntries(8), AgentOptions{
		Runner: fr, Pricing: price, MaxUSD: 5, Sessions: 2, PerSession: 4, Turns: 30,
		OnJudged: func(Verdict) { judged++ },
	})
	if res.StoppedBy != "tamper" || judged != 0 || len(fr.specs) != 1 || len(res.Verdicts) != 8 {
		t.Fatalf("stopped=%q judged=%d sessions=%d verdicts=%d", res.StoppedBy, judged, len(fr.specs), len(res.Verdicts))
	}
	for _, v := range res.Verdicts {
		if v.Kind == Holds {
			t.Errorf("a verdict of the tampering session was kept: %+v", v)
		}
	}
	if res.Spent <= 0 {
		t.Error("the tampering session was still paid for")
	}
}

func lookedFrom(t *testing.T, stream string) *Looked {
	t.Helper()
	r, _ := ParseStream(strings.NewReader(stream))
	return r.Looked
}

func TestEvidenceMustHaveBeenSeenBySession(t *testing.T) {
	repo := newRepo(t)
	h := head(t, repo)
	stream := `{"type":"assistant","message":{"id":"a","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/work/internal/distill.go"}},{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"git log"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":"` + h[:12] + ` add distill"}]}}
`
	lk := lookedFrom(t, stream)
	if !lk.Has("internal/distill.go:3") || !lk.Has(h[:12]) || !lk.Has(h) {
		t.Error("read file and printed hash must count")
	}
	if lk.Has("go.mod:1") || lk.Has("deadbeef1234") {
		t.Error("what the session never saw must not count")
	}
	pkg := Package{Entries: []Entry{{ID: 1, Title: "a", Body: "internal/distill.go"}, {ID: 2, Title: "b", Body: "internal/distill.go"}}}
	text := `{"verdicts":[{"id":1,"verdict":"stale","reason":"x","evidence":["internal/distill.go:3"]},{"id":2,"verdict":"stale","reason":"x","evidence":["internal/distill.go:1"]}]}`
	v, err := validateAnswer(repo, text, pkg, nil, lk)
	if err != nil || v.Verdicts[0].Kind != Stale {
		t.Fatalf("%+v %v", v, err)
	}
	// Same file, never opened by a session that saw nothing.
	v, _ = validateAnswer(repo, text, pkg, nil, &Looked{Read: map[string]bool{}})
	if v.Verdicts[0].Kind != Unclear || v.Verdicts[1].Kind != Unclear || len(v.Verdicts[0].Unverified) != 1 {
		t.Errorf("unseen evidence must not hold a stale verdict: %+v", v.Verdicts)
	}
}

func TestParseStreamBudgetCutKeepsLastAnswerText(t *testing.T) {
	in := `{"type":"assistant","message":{"id":"a","content":[{"type":"text","text":"{\"verdicts\":[]}"}]}}
{"type":"result","subtype":"error_max_budget_usd","is_error":false,"num_turns":9}
`
	r, err := ParseStream(strings.NewReader(in))
	if err == nil || r.Text != `{"verdicts":[]}` {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestCheckAgentKeepsPartialAnswerOfSessionCutOffByALimit(t *testing.T) {
	repo := newRepo(t)
	cut := func(s SessionSpec) (SessionResult, error) {
		r, _ := holdsAll(s)
		r.Looked = &Looked{Read: map[string]bool{}}
		r.Tools = map[string]int{"Grep": 3}
		return r, errors.New("die Session endete mit error_max_budget_usd")
	}
	fr := &fakeRunner{answers: []func(SessionSpec) (SessionResult, error){cut}}
	var judged, failed int
	res := CheckAgent(context.Background(), repo, agentEntries(3), AgentOptions{
		Runner: fr, Pricing: price, MaxUSD: 5, Sessions: 1, PerSession: 3, Turns: 30,
		OnSession: func(_ SessionStat, f bool) {
			if f {
				failed++
			}
		},
		OnJudged: func(Verdict) { judged++ },
	})
	if judged != 3 || failed != 0 || len(res.Sessions) != 1 || !strings.Contains(res.Sessions[0].Err, "Teilergebnis") {
		t.Fatalf("judged=%d failed=%d %+v", judged, failed, res.Sessions)
	}
	// The cap is generous: twice the share, within what is left.
	if b := fr.specs[0].BudgetUSD; b != 5 {
		t.Errorf("budget %f", b)
	}
}

func TestCheckAgentSessionWithoutAnyToolCallJudgesNothing(t *testing.T) {
	repo := newRepo(t)
	lazy := func(s SessionSpec) (SessionResult, error) {
		r, _ := holdsAll(s)
		r.Looked = &Looked{Read: map[string]bool{}}
		return r, nil
	}
	fr := &fakeRunner{answers: []func(SessionSpec) (SessionResult, error){lazy}}
	var judged int
	res := CheckAgent(context.Background(), repo, agentEntries(3), AgentOptions{
		Runner: fr, Pricing: price, MaxUSD: 5, Sessions: 1, PerSession: 3, Turns: 30,
		OnJudged: func(Verdict) { judged++ },
	})
	if judged != 0 || res.Verdicts[0].Kind != Failed {
		t.Fatalf("judged=%d %+v", judged, res.Verdicts)
	}
}
