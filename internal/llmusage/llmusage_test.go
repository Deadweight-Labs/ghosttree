package llmusage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestTokensCountCacheAtTheirOwnFigures(t *testing.T) {
	u := Usage{InputTokens: 10, OutputTokens: 5, CacheReadTokens: 100, CacheWriteTokens: 20}
	if u.Tokens() != 135 {
		t.Fatalf("tokens = %d, want 135", u.Tokens())
	}
}

func TestCheckNamesTheLimitThatWasReached(t *testing.T) {
	l := Limits{Day: Limit{Calls: 3}, Month: Limit{USD: 5, Tokens: 1000}}
	if r := l.Check(Usage{Calls: 2}, Usage{Calls: 2}); r != nil {
		t.Fatalf("nothing reached, got %v", r)
	}
	r := l.Check(Usage{Calls: 3}, Usage{})
	if r == nil || r.Period != "day" || r.Figure != "calls" {
		t.Fatalf("got %v", r)
	}
	r = l.Check(Usage{}, Usage{CostMicroUSD: MicroUSD(5)})
	if r == nil || r.Period != "month" || r.Figure != "usd" || !strings.Contains(r.Error(), "monthly") {
		t.Fatalf("got %v", r)
	}
	r = l.Check(Usage{}, Usage{InputTokens: 600, CacheReadTokens: 400})
	if r == nil || r.Figure != "tokens" {
		t.Fatalf("cache tokens must count: %v", r)
	}
	if (Limits{}).Check(Usage{Calls: 1 << 30, CostMicroUSD: 1 << 40}, Usage{Calls: 1 << 30}) != nil {
		t.Fatal("zero limits are unlimited")
	}
}

func TestDefaultsKeepKurzErklaertDailyCapsAndLeaveTheGardenerUnlimited(t *testing.T) {
	e, err := Resolve(JobExplain, "", env(nil))
	if err != nil || e.Day.Calls != 200 || e.Day.Tokens != 400000 || e.Month.Any() {
		t.Fatalf("explain defaults: %+v %v", e, err)
	}
	g, err := Resolve(JobGarden, "", env(nil))
	if err != nil || g.Day.Any() || g.Month.Any() {
		t.Fatalf("garden defaults: %+v %v", g, err)
	}
}

func TestEnvOverridesFileAndEmptyOrZeroMeansUnlimited(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "llm.json")
	os.WriteFile(p, []byte(`{"limits":{"explain":{"daily":{"calls":50},"monthly":{"usd":4.5}},"garden":{"monthly":{"tokens":9000}}}}`), 0o600)
	e, err := Resolve(JobExplain, p, env(nil))
	if err != nil || e.Day.Calls != 50 || e.Day.Tokens != 400000 || e.Month.USD != 4.5 {
		t.Fatalf("file layer: %+v %v", e, err)
	}
	e, err = Resolve(JobExplain, p, env(map[string]string{
		EnvName("explain", "daily", "calls"):  "",
		EnvName("explain", "daily", "tokens"): "0",
		EnvName("explain", "monthly", "usd"):  "12.5",
	}))
	if err != nil || e.Day.Calls != 0 || e.Day.Tokens != 0 || e.Month.USD != 12.5 {
		t.Fatalf("env layer: %+v %v", e, err)
	}
	g, _ := Resolve(JobGarden, p, env(nil))
	if g.Month.Tokens != 9000 {
		t.Fatalf("garden file: %+v", g)
	}
}

func TestBadEnvIsAnErrorNotASilentDefault(t *testing.T) {
	for _, v := range []string{"abc", "-1", "NaN", "1.5"} {
		name := EnvName("garden", "daily", "calls")
		if _, err := Resolve(JobGarden, "", env(map[string]string{name: v})); err == nil {
			t.Errorf("%q accepted", v)
		}
	}
}
