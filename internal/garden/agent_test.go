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
{"type":"assistant","message":{"id":"m2","usage":{"input_tokens":20,"output_tokens":7,"cache_read_input_tokens":1100,"cache_creation_input_tokens":0}}}
{"type":"result","subtype":"success","is_error":false,"num_turns":3,"result":"{\"verdicts\":[]}","total_cost_usd":0.5,"permission_denials":[{"tool_name":"Bash"}],"usage":{"input_tokens":30,"output_tokens":12,"cache_read_input_tokens":1200,"cache_creation_input_tokens":1000}}
`

func TestParseStreamTakesUsageIncludingCacheFromTheResult(t *testing.T) {
	r, err := ParseStream(strings.NewReader(streamFixture))
	if err != nil {
		t.Fatal(err)
	}
	want := llm.Usage{InputTokens: 30, OutputTokens: 12, CacheReadTokens: 1200, CacheWriteTokens: 1000}
	if r.Usage != want || r.Turns != 3 || r.Denials != 1 || r.ReportedUSD != 0.5 || r.Text != `{"verdicts":[]}` {
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

func TestClaudeArgsAreLockedDown(t *testing.T) {
	c := ClaudeConfig{Bin: "claude", Model: "claude-sonnet-5-5", RepoRoot: "/r", CtxBin: "/usr/bin/ctx", DenyRead: []string{"/srv/x/private"}}
	args, err := c.Args(SessionSpec{MaxTurns: 60, BudgetUSD: 1.5})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, "\x00")
	for _, bad := range []string{"bypassPermissions", "acceptEdits", "dangerously", "--allowedTools\x00Edit", "--allow-dangerously"} {
		if strings.Contains(joined, bad) {
			t.Errorf("args contain %q", bad)
		}
	}
	val := func(flag string) string {
		for i, a := range args {
			if a == flag && i+1 < len(args) {
				return args[i+1]
			}
		}
		t.Errorf("no %s", flag)
		return ""
	}
	if val("--permission-mode") != "dontAsk" || val("--max-turns") != "60" || val("--max-budget-usd") != "1.50" {
		t.Errorf("%v", args)
	}
	if val("--tools") != "Read,Grep,Glob,Bash" {
		t.Errorf("tool set: %s", val("--tools"))
	}
	allowed := val("--allowedTools")
	for _, w := range []string{"Bash(git log:*)", "Bash(git show:*)", "Bash(git blame:*)", "mcp__ghosttree__context_search", "mcp__ghosttree__context_get"} {
		if !strings.Contains(allowed, w) {
			t.Errorf("allowlist lacks %s", w)
		}
	}
	for _, w := range []string{"context_remember", "context_retire", "Edit", "Write", "WebFetch", "context_describe_file"} {
		if strings.Contains(allowed, w) {
			t.Errorf("allowlist contains %s", w)
		}
		if !strings.Contains(val("--disallowedTools"), w) {
			t.Errorf("denylist lacks %s", w)
		}
	}
	if !strings.Contains(val("--disallowedTools"), "Read(//srv/x/private/**)") {
		t.Errorf("deny-read missing: %s", val("--disallowedTools"))
	}
	if !contains(args, "--strict-mcp-config") || !strings.Contains(val("--mcp-config"), `"args":["mcp"]`) {
		t.Errorf("mcp: %v", args)
	}
	if val("--setting-sources") != "user" {
		t.Error("project settings of the checkout must not apply")
	}
	c.CtxBin = ""
	args, _ = c.Args(SessionSpec{MaxTurns: 20})
	if strings.Contains(strings.Join(args, " "), "ghosttree\":{") || contains(args, "--max-budget-usd") {
		t.Errorf("no MCP and no budget expected: %v", args)
	}
	if _, err := (ClaudeConfig{DenyRead: []string{"relative"}}).Args(SessionSpec{MaxTurns: 20}); err == nil {
		t.Error("relative deny path accepted")
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

func TestClaudeEnvironKeepsSecretsOut(t *testing.T) {
	c := ClaudeConfig{ConfigDir: "/c", BaseURL: "http://allgemein:18317", Token: "dwl-garden",
		ParentEnv: []string{"PATH=/bin", "HOME=/h", "GHOSTTREE_GARDEN_KEY=geheim", "ANTHROPIC_API_KEY=alt", "XDG_CONFIG_HOME=/x", "AWS_SECRET_ACCESS_KEY=s", "GIT_CONFIG_GLOBAL=/g"}}
	env := strings.Join(c.Environ(), "\n")
	for _, bad := range []string{"geheim", "ANTHROPIC_API_KEY", "AWS_", "alt"} {
		if strings.Contains(env, bad) {
			t.Errorf("environment leaks %q:\n%s", bad, env)
		}
	}
	for _, good := range []string{"PATH=/bin", "HOME=/h", "XDG_CONFIG_HOME=/x", "GIT_CONFIG_GLOBAL=/g", "CLAUDE_CONFIG_DIR=/c", "ANTHROPIC_BASE_URL=http://allgemein:18317", "ANTHROPIC_AUTH_TOKEN=dwl-garden", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1"} {
		if !strings.Contains(env, good) {
			t.Errorf("environment lacks %q", good)
		}
	}
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
		"{ pwd; echo \"CFG=$CLAUDE_CONFIG_DIR\"; echo \"KEY=$GHOSTTREE_GARDEN_KEY\"; for a in \"$@\"; do echo \"ARG=$a\"; done; echo PROMPT; cat; } > " + record + "\n" +
		"cat <<'EOF'\n" + stream + "EOF\n"
	bin = filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, record
}

func TestClaudeRunnerStartsInTheCheckoutWithThePromptOnStdin(t *testing.T) {
	t.Setenv("GHOSTTREE_GARDEN_KEY", "geheim")
	bin, record := fakeClaude(t, streamFixture)
	root := t.TempDir()
	r := ClaudeRunner{Config: ClaudeConfig{Bin: bin, RepoRoot: root, ConfigDir: "/cfg", Model: "m", ParentEnv: os.Environ(), Timeout: 30 * time.Second}}
	res, err := r.RunSession(context.Background(), SessionSpec{Prompt: "PRÜFE DAS", MaxTurns: 20})
	if err != nil || res.Turns != 3 {
		t.Fatalf("%+v %v", res, err)
	}
	raw, _ := os.ReadFile(record)
	got := string(raw)
	real, _ := filepath.EvalSymlinks(root)
	for _, want := range []string{real + "\n", "CFG=/cfg\n", "KEY=\n", "ARG=dontAsk\n", "ARG=--max-turns\n", "PROMPT\nPRÜFE DAS"} {
		if !strings.Contains(got, want) {
			t.Errorf("record lacks %q:\n%s", want, got)
		}
	}
}

func TestClaudeRunnerTimeoutKillsTheSessionAndSaysSo(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	os.WriteFile(bin, []byte("#!/bin/sh\nsleep 30\n"), 0o755)
	r := ClaudeRunner{Config: ClaudeConfig{Bin: bin, RepoRoot: dir, Timeout: 300 * time.Millisecond}}
	start := time.Now()
	_, err := r.RunSession(context.Background(), SessionSpec{MaxTurns: 20})
	if err == nil || !strings.Contains(err.Error(), "abgebrochen") || time.Since(start) > 10*time.Second {
		t.Fatalf("%v after %v", err, time.Since(start))
	}
}
