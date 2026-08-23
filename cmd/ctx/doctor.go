package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/installer"
)

// cmdDoctor inspects the wiring rather than the data: `ctx status` answers
// "is it running", doctor answers "would a fresh agent session actually reach
// ghosttree". Installing is idempotent but one-shot, and nothing else notices
// when a harness config drifts out from under it.
func cmdDoctor(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stdout)
	fix := fs.Bool("fix", false, "re-run the installers for whatever is broken")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stdout, "cannot determine home directory: %v\n", err)
		return 1
	}

	if *fix {
		for name, install := range map[string]func(string) ([]installer.Change, error){
			"claude": installer.InstallClaude,
			"codex":  installer.InstallCodex,
		} {
			if _, err := install(home); err != nil {
				fmt.Fprintf(stdout, "fix %s: %v\n", name, err)
			}
		}
		fmt.Fprint(stdout, "re-ran installers\n\n")
	}

	checks := append(installer.VerifyClaude(home), installer.VerifyCodex(home)...)
	checks = append(checks, binaryCheck())
	checks = append(checks, configChecks()...)

	width := 0
	for _, c := range checks {
		if len(c.Name) > width {
			width = len(c.Name)
		}
	}
	ok := true
	for _, c := range checks {
		status := "ok  "
		if !c.OK {
			status, ok = "FAIL", false
		}
		fmt.Fprintf(stdout, "%-*s  %s  %s\n", width, c.Name, status, c.Detail)
		if !c.OK && c.Fix != "" {
			fmt.Fprintf(stdout, "%-*s        fix: %s\n", width, "", c.Fix)
		}
	}
	if !ok {
		return 1
	}
	return 0
}

// binaryCheck catches the case where the harnesses are registered to run `ctx`
// but the binary is not on the PATH they will use.
func binaryCheck() installer.Check {
	path, err := exec.LookPath("ctx")
	if err != nil {
		return installer.Check{
			Name:   "ctx on PATH",
			Detail: "not found (harness configs invoke bare 'ctx')",
			Fix:    "install the binary into a directory on PATH, e.g. ~/.local/bin",
		}
	}
	return installer.Check{Name: "ctx on PATH", OK: true, Detail: path}
}

func configChecks() []installer.Check {
	cfg, err := config.Load()
	if err != nil {
		return []installer.Check{{
			Name:   "client config",
			Detail: config.Path() + " (missing)",
			Fix:    "run 'ctx setup --server <url> --token <token>'",
		}}
	}
	checks := []installer.Check{{
		Name: "client config", OK: true,
		Detail: fmt.Sprintf("%s (machine %s)", config.Path(), cfg.Machine),
	}}
	if err := client.New(cfg).Health(); err != nil {
		return append(checks, installer.Check{
			Name:   "server reachable",
			Detail: fmt.Sprintf("%s: %v", cfg.ServerURL, err),
			Fix:    "check the server is running and the private network is up",
		})
	}
	return append(checks, installer.Check{Name: "server reachable", OK: true, Detail: cfg.ServerURL})
}
