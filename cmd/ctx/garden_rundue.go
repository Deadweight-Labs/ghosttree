package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/llmusage"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const gardenRunDueUsage = `usage: ctx garden run-due --repos-dir <dir> --state-file <file> [--out-dir <dir>] [-- <check-knowledge flags>]

Asks the server which organizations and people want their gardener now (their
settings in the web interface: how often, which repositories, budget) and runs
check-knowledge once per repository, with the budget of the run and of the
month from those settings. Each run is reported back, with its cost, and shows
on the Gardener tab. Run it from a timer (every hour is enough); nothing is
due most of the time and then nothing happens.

  --repos-dir   one checkout per repository: <dir>/<owner>/<name>; a repository
                without one is reported as failed and skipped
  --state-file  the gardener's memory (as for check-knowledge); required, it
                is how the cost of each run is measured
Each namespace has its own month: before every repository run-due checks what
is left of that namespace's monthly budget (what its runs reported this month
and what this call spent so far) and gives the run at most that much.
  --out-dir     write each run's report to <dir>/<date>-<owner>-<name>.md
Flags after -- go to check-knowledge unchanged (for example --mode agent
--propose --sessions 4 --claude ... --claude-dir ...). --project, --repo,
--max-usd and --monthly-usd come from the server and may not be given there.`

// gardenUSDEpsilon is less than any budget anybody sets: a rest below it is
// spent.
const gardenUSDEpsilon = 0.005

// gardenDueClient is what run-due needs of the server.
type gardenDueClient interface {
	GardenDue() ([]store.GardenJob, error)
	StartGardenRun(orgID int64, project string) (int64, error)
	FinishGardenRun(id int64, state string, costUSD float64, note string) error
}

// gardenSpend measures what the gardener spent this month so far. An error
// (an unreadable state file) stops run-due: a cost it cannot measure would
// count as nothing and let the budget run over.
type gardenSpend func() (int64, error)

func cmdGardenRunDue(args []string, stdout io.Writer) int {
	own, pass := args, []string(nil)
	for i, a := range args {
		if a == "--" {
			own, pass = args[:i], args[i+1:]
			break
		}
	}
	for _, p := range pass {
		name := strings.TrimLeft(strings.SplitN(p, "=", 2)[0], "-")
		switch name {
		case "project", "repo", "max-usd", "monthly-usd", "state-file", "out":
			fmt.Fprintf(stdout, "--%s comes from run-due, not from the flags after --\n", name)
			return 2
		}
	}
	fs := flag.NewFlagSet("garden run-due", flag.ContinueOnError)
	fs.SetOutput(stdout)
	reposDir := fs.String("repos-dir", "", "folder with one checkout per repository")
	stateFile := fs.String("state-file", "", "the gardener's memory, as for check-knowledge")
	outDir := fs.String("out-dir", "", "folder for the reports")
	if err := fs.Parse(own); err != nil || fs.NArg() != 0 || *reposDir == "" || *stateFile == "" {
		fmt.Fprintln(stdout, gardenRunDueUsage)
		return 2
	}
	sc, err := config.Load()
	if err != nil {
		fmt.Fprintf(stdout, "no config (%s) - run 'ctx setup'\n", config.Path())
		return 1
	}
	path := *stateFile
	spend := func() (int64, error) {
		fst, err := openFileState(path)
		if err != nil {
			return 0, err
		}
		u, err := fst.LLMUsageMonth(llmusage.JobGarden, time.Now().UTC().Format("2006-01"))
		return u.CostMicroUSD, err
	}
	run := func(extra []string) int {
		full := append([]string{"check-knowledge"}, extra...)
		full = append(full, "--state-file", path)
		return cmdGarden(append(full, pass...), stdout)
	}
	return runGardenDue(client.New(sc), *reposDir, *outDir, spend, run, stdout)
}

// runGardenDue is run-due without flags and config, so a test can drive it.
func runGardenDue(c gardenDueClient, reposDir, outDir string, spend gardenSpend, run func([]string) int, stdout io.Writer) int {
	if _, err := spend(); err != nil {
		fmt.Fprintf(stdout, "read the state file: %v\n", err)
		return 1
	}
	jobs, err := c.GardenDue()
	if err != nil {
		fmt.Fprintf(stdout, "ask the server what is due: %v\n", err)
		return 1
	}
	if len(jobs) == 0 {
		fmt.Fprintln(stdout, "nothing is due")
		return 0
	}
	failed := 0
	for _, job := range jobs {
		// What is left of this namespace's month; the state file counts the
		// gardener's spending over all namespaces, so it never stands in for it.
		left := job.BudgetMon - job.SpentMon
		for _, project := range job.Projects {
			budget := job.BudgetRun
			if job.BudgetMon > 0 && left < budget {
				budget = left
			}
			if budget < gardenUSDEpsilon {
				fmt.Fprintf(stdout, "%s: budget of %s spent, skipped\n", project, job.Slug)
				continue
			}
			id, err := c.StartGardenRun(job.OrgID, project)
			if err != nil {
				fmt.Fprintf(stdout, "%s: %v\n", project, err)
				failed++
				continue
			}
			dir, ok := gardenCheckout(reposDir, project)
			if !ok {
				_ = c.FinishGardenRun(id, "failed", 0, "no checkout under --repos-dir")
				fmt.Fprintf(stdout, "%s: no checkout under %s, skipped\n", project, reposDir)
				failed++
				continue
			}
			args := []string{"--project", project, "--repo", dir,
				"--max-usd", strconv.FormatFloat(budget, 'f', 2, 64)}
			if outDir != "" {
				args = append(args, "--out", filepath.Join(outDir, time.Now().UTC().Format("2006-01-02")+"-"+strings.ReplaceAll(strings.TrimPrefix(project, "github.com/"), "/", "-")+".md"))
			}
			before, err := spend()
			if err != nil {
				_ = c.FinishGardenRun(id, "failed", 0, "state file unreadable")
				fmt.Fprintf(stdout, "read the state file: %v\n", err)
				return 1
			}
			code := run(args)
			after, err := spend()
			if err != nil {
				_ = c.FinishGardenRun(id, "failed", 0, "state file unreadable after the run; cost unknown")
				fmt.Fprintf(stdout, "read the state file: %v\n", err)
				return 1
			}
			cost := float64(after-before) / 1e6
			left -= cost
			state, note := "done", ""
			if code != 0 {
				state, note = "failed", "check-knowledge exited with "+strconv.Itoa(code)
				failed++
			}
			if cost >= budget-gardenUSDEpsilon {
				state = "budget"
			}
			if err := c.FinishGardenRun(id, state, cost, note); err != nil {
				fmt.Fprintf(stdout, "%s: report the run: %v\n", project, err)
			}
		}
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// gardenCheckout is the checkout of a repository under the folder:
// <dir>/<owner>/<name> for a github.com remote, <dir>/<path> otherwise.
func gardenCheckout(dir, project string) (string, bool) {
	rel := strings.TrimPrefix(project, "github.com/")
	if rel == "" || strings.Contains(rel, "..") || filepath.IsAbs(rel) {
		return "", false
	}
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if fi, err := os.Stat(full); err != nil || !fi.IsDir() {
		return "", false
	}
	return full, true
}
