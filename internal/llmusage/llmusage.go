// Package llmusage is what the model-calling jobs (the gardener, "Kurz
// erklärt") share: one record of what a job used, and one set of limits that
// is read the same way for every job. A limit of 0 means unlimited.
package llmusage

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Jobs that call a model and are metered.
const (
	JobGarden  = "garden"
	JobExplain = "explain"
)

// Jobs lists the metered jobs in display order.
var Jobs = []string{JobGarden, JobExplain}

// Usage is what a job used in one period. Token counts are the provider's own
// figures; the cache columns are what the provider reports as read from and
// written to its prompt cache, which are billed at their own prices and are
// not part of InputTokens.
type Usage struct {
	Day              string `json:"day"`
	Calls            int    `json:"calls"`
	Failures         int    `json:"failures"`
	InputTokens      int    `json:"input_tokens"`
	OutputTokens     int    `json:"output_tokens"`
	CacheReadTokens  int    `json:"cache_read_tokens"`
	CacheWriteTokens int    `json:"cache_write_tokens"`
	// CostMicroUSD is the cost in millionths of a dollar, summed per call at
	// the prices in force when the call was made.
	CostMicroUSD int64 `json:"cost_micro_usd"`
}

// Tokens is every token the model processed: input, cache reads and writes, output.
func (u Usage) Tokens() int {
	return u.InputTokens + u.OutputTokens + u.CacheReadTokens + u.CacheWriteTokens
}

// CostUSD is the cost in dollars.
func (u Usage) CostUSD() float64 { return float64(u.CostMicroUSD) / 1e6 }

// Add returns the sum of two usages (Day is kept from u).
func (u Usage) Add(o Usage) Usage {
	u.Calls += o.Calls
	u.Failures += o.Failures
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	u.CacheReadTokens += o.CacheReadTokens
	u.CacheWriteTokens += o.CacheWriteTokens
	u.CostMicroUSD += o.CostMicroUSD
	return u
}

// MicroUSD converts dollars to the stored unit.
func MicroUSD(usd float64) int64 { return int64(usd*1e6 + 0.5) }

// Limit caps one period. A zero field is no cap on that figure.
type Limit struct {
	USD    float64 `json:"usd"`
	Calls  int     `json:"calls"`
	Tokens int     `json:"tokens"`
}

// Any reports whether at least one figure is capped.
func (l Limit) Any() bool { return l.USD > 0 || l.Calls > 0 || l.Tokens > 0 }

// Limits are the caps of one job, per day and per month (UTC).
type Limits struct {
	Day   Limit `json:"daily"`
	Month Limit `json:"monthly"`
}

// Reached names the cap that is used up: which period and which figure.
type Reached struct {
	Period string // "day" or "month"
	Figure string // "usd", "calls" or "tokens"
	Limit  float64
	Used   float64
	// Cause is set when the usage could not be read at all; the run then stops
	// rather than guess that nothing was used.
	Cause error
}

func (r *Reached) Error() string {
	if r.Cause != nil {
		return "usage could not be read: " + r.Cause.Error()
	}
	period := map[string]string{"day": "daily", "month": "monthly"}[r.Period]
	if r.Figure == "usd" {
		return fmt.Sprintf("%s limit reached: $%.2f of $%.2f", period, r.Used, r.Limit)
	}
	return fmt.Sprintf("%s %s limit reached: %.0f of %.0f", period, r.Figure, r.Used, r.Limit)
}

// Check says which cap is used up, or nil. It is tested before a call, so a
// run can end over a cap by the cost of one call, not more.
func (l Limits) Check(day, month Usage) *Reached {
	for _, p := range []struct {
		name string
		lim  Limit
		use  Usage
	}{{"day", l.Day, day}, {"month", l.Month, month}} {
		if p.lim.USD > 0 && p.use.CostUSD() >= p.lim.USD {
			return &Reached{Period: p.name, Figure: "usd", Limit: p.lim.USD, Used: p.use.CostUSD()}
		}
		if p.lim.Calls > 0 && p.use.Calls >= p.lim.Calls {
			return &Reached{Period: p.name, Figure: "calls", Limit: float64(p.lim.Calls), Used: float64(p.use.Calls)}
		}
		if p.lim.Tokens > 0 && p.use.Tokens() >= p.lim.Tokens {
			return &Reached{Period: p.name, Figure: "tokens", Limit: float64(p.lim.Tokens), Used: float64(p.use.Tokens())}
		}
	}
	return nil
}

// Defaults are the limits of a job nobody configured. "Kurz erklärt" keeps its
// original daily caps; the gardener has none beyond the per-run --max-usd.
func Defaults(job string) Limits {
	if job == JobExplain {
		return Limits{Day: Limit{Calls: 200, Tokens: 400000}}
	}
	return Limits{}
}

// EnvName is the variable for one figure, for example
// GHOSTTREE_GARDEN_MONTHLY_USD or GHOSTTREE_EXPLAIN_DAILY_CALLS.
func EnvName(job, period, figure string) string {
	return "GHOSTTREE_" + strings.ToUpper(job) + "_" + strings.ToUpper(period) + "_" + strings.ToUpper(figure)
}

type fileLimits struct {
	Limits map[string]Limits `json:"limits"`
}

// Resolve returns the limits of a job. Order, last wins: the defaults, the
// "limits" entry of llm.json (path, may be empty), then the environment. A
// variable that is set to an empty string or 0 means unlimited; one that is not
// set leaves the lower layer alone. Flags are applied by the caller afterwards.
func Resolve(job, llmJSON string, lookup func(string) (string, bool)) (Limits, error) {
	out := Defaults(job)
	if llmJSON != "" {
		raw, err := os.ReadFile(llmJSON)
		if err == nil {
			var f fileLimits
			if err := json.Unmarshal(raw, &f); err != nil {
				return out, fmt.Errorf("read %s: %w", llmJSON, err)
			}
			// A job present in the file replaces the defaults figure by
			// figure only where it names the figure.
			if l, ok := f.Limits[job]; ok {
				var generic map[string]map[string]map[string]map[string]json.RawMessage
				_ = json.Unmarshal(raw, &generic)
				out = mergeFile(out, l, generic["limits"][job])
			}
		} else if !os.IsNotExist(err) {
			return out, fmt.Errorf("read %s: %w", llmJSON, err)
		}
	}
	if lookup == nil {
		lookup = os.LookupEnv
	}
	for _, p := range []struct {
		period string
		lim    *Limit
	}{{"daily", &out.Day}, {"monthly", &out.Month}} {
		if err := envFigure(lookup, job, p.period, "usd", func(v float64) { p.lim.USD = v }); err != nil {
			return out, err
		}
		if err := envFigure(lookup, job, p.period, "calls", func(v float64) { p.lim.Calls = int(v) }); err != nil {
			return out, err
		}
		if err := envFigure(lookup, job, p.period, "tokens", func(v float64) { p.lim.Tokens = int(v) }); err != nil {
			return out, err
		}
	}
	return out, nil
}

// mergeFile applies the figures the file names for one job. raw holds the
// period objects as written, so an absent figure keeps the default and a
// written 0 means unlimited.
func mergeFile(base, file Limits, raw map[string]map[string]json.RawMessage) Limits {
	apply := func(dst *Limit, src Limit, period string) {
		p := raw[period]
		if _, ok := p["usd"]; ok {
			dst.USD = src.USD
		}
		if _, ok := p["calls"]; ok {
			dst.Calls = src.Calls
		}
		if _, ok := p["tokens"]; ok {
			dst.Tokens = src.Tokens
		}
	}
	apply(&base.Day, file.Day, "daily")
	apply(&base.Month, file.Month, "monthly")
	return base
}

func envFigure(lookup func(string) (string, bool), job, period, figure string, set func(float64)) error {
	name := EnvName(job, period, figure)
	v, ok := lookup(name)
	if !ok {
		return nil
	}
	v = strings.TrimSpace(v)
	if v == "" {
		set(0)
		return nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 || f != f || f > 1e12 {
		return fmt.Errorf("%s=%q is not a number of zero or more", name, v)
	}
	if figure != "usd" && f != float64(int64(f)) {
		return fmt.Errorf("%s=%q must be a whole number", name, v)
	}
	set(f)
	return nil
}

// Text renders one cap for people: "unlimited" or the figure.
func (l Limit) Text() string {
	var parts []string
	if l.USD > 0 {
		parts = append(parts, fmt.Sprintf("$%.2f", l.USD))
	}
	if l.Calls > 0 {
		parts = append(parts, fmt.Sprintf("%d calls", l.Calls))
	}
	if l.Tokens > 0 {
		parts = append(parts, fmt.Sprintf("%d tokens", l.Tokens))
	}
	if len(parts) == 0 {
		return "unlimited"
	}
	return strings.Join(parts, ", ")
}
