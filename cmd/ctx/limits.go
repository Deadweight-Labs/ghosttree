package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/llmusage"
)

// llmConfigPath is the provider configuration file, the same one llm.LoadConfig
// reads; its "limits" entry sets the caps of every job.
func llmConfigPath() string {
	if p := os.Getenv("GHOSTTREE_LLM_CONFIG"); p != "" {
		return p
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "ghosttree", "llm.json")
	}
	return ""
}

// jobLimits are the caps of a job from llm.json and the environment, without
// flags. The server and the commands read them the same way, so they agree.
func jobLimits(job string) (llmusage.Limits, error) {
	return llmusage.Resolve(job, llmConfigPath(), os.LookupEnv)
}

var limitFigures = []struct{ period, figure string }{
	{"daily", "usd"}, {"daily", "calls"}, {"daily", "tokens"},
	{"monthly", "usd"}, {"monthly", "calls"}, {"monthly", "tokens"},
}

// addLimitFlags registers --<prefix>daily-usd ... --<prefix>monthly-tokens and
// returns the function that resolves the limits after parsing: defaults,
// llm.json and environment first, then the flags that were given. A flag set to
// 0 or to an empty value means unlimited.
func addLimitFlags(fs *flag.FlagSet, job, prefix string) func() (llmusage.Limits, error) {
	for _, f := range limitFigures {
		env := llmusage.EnvName(job, f.period, f.figure)
		fs.String(prefix+f.period+"-"+f.figure, "", f.period+" "+f.figure+" cap of "+job+", 0 = unlimited (env "+env+")")
	}
	return func() (llmusage.Limits, error) {
		lim, err := jobLimits(job)
		if err != nil {
			return lim, err
		}
		var ferr error
		fs.Visit(func(fl *flag.Flag) {
			for _, f := range limitFigures {
				if fl.Name != prefix+f.period+"-"+f.figure {
					continue
				}
				target := &lim.Day
				if f.period == "monthly" {
					target = &lim.Month
				}
				v := strings.TrimSpace(fl.Value.String())
				n := 0.0
				if v != "" {
					if n, err = strconv.ParseFloat(v, 64); err != nil || n < 0 || n != n || n > 1e12 || (f.figure != "usd" && n != float64(int64(n))) {
						ferr = fmt.Errorf("--%s: %q is not a number of zero or more", fl.Name, v)
						return
					}
				}
				switch f.figure {
				case "usd":
					target.USD = n
				case "calls":
					target.Calls = int(n)
				case "tokens":
					target.Tokens = int(n)
				}
			}
		})
		return lim, ferr
	}
}

// limitSummary renders the caps of one period for the command line.
func limitSummary(l llmusage.Limit) string { return l.Text() }
