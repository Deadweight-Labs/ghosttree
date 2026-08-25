package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/ghost"
	"github.com/Deadweight-Labs/ghosttree/internal/installer"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
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
	checks = append(checks, ghostChecks()...)

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

// ghostChecks meldet Beschreibungen, deren Datei es nicht mehr gibt. Sie
// bleiben in der Datenbank — gelöscht wird hier nichts, weil eine verschobene
// Datei beim nächsten Anfassen über ihren Blob wiedergefunden wird und die
// Beschreibung dann noch da sein muss.
//
// Kein Repo, keine Konfiguration oder kein erreichbarer Server heisst
// schweigen: der Doctor prüft die Verdrahtung, und ein abwesender Server ist
// bereits eine eigene Zeile weiter oben.
func ghostChecks() []installer.Check {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	gitCtx := collector.ResolveGitContext(cwd)
	if gitCtx.Project == "" || gitCtx.Root == "" {
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		return nil
	}
	c := client.New(cfg)
	if err := c.Health(); err != nil {
		return nil
	}
	entries, err := ghost.RepoEntries(gitCtx.Root)
	if err != nil {
		return nil
	}
	var files []string
	for _, e := range entries {
		if e.Kind == "file" {
			files = append(files, e.Path)
		}
	}
	stored, err := c.GhostTree(gitCtx.Project, "")
	if err != nil {
		return nil
	}
	live := map[string]bool{}
	for _, p := range files {
		live[p] = true
		for _, parent := range store.ParentPaths(p) {
			live[parent] = true
		}
	}
	var orphans []string
	for _, g := range stored {
		if !live[g.Path] {
			orphans = append(orphans, g.Path)
		}
	}
	if len(orphans) == 0 {
		return []installer.Check{{
			Name: "ghost tree", OK: true,
			Detail: fmt.Sprintf("%d von %d Pfaden beschrieben, %s", len(stored), len(entries), describedBulk(stored)),
		}}
	}
	sort.Strings(orphans)
	shown := orphans
	if len(shown) > 5 {
		shown = shown[:5]
	}
	return []installer.Check{{
		Name:   "ghost tree",
		Detail: fmt.Sprintf("%d Beschreibungen ohne Datei: %s", len(orphans), strings.Join(shown, ", ")),
		Fix:    "die Pfade prüfen — verschoben, umbenannt oder wirklich weg. Sie bleiben in der Datenbank und verschwinden beim nächsten Neuschreiben aus dem Baum",
	}}
}

// describedBulk nennt die Textmenge, die hinter den Beschreibungen steckt.
//
// Der Grund ist der Preis, den niemand sieht: der PreToolUse-Hook liefert bei
// jedem Anfassen einer Datei deren Beschreibung samt der ihrer Vorfahren
// ungefragt aus. Es gibt dafür keine Obergrenze und keine Kürzung — bei neun
// Prozent Abdeckung ist das kein Problem, und genau deshalb fällt es erst auf,
// wenn es eines ist. Diese Zeile macht die Grössenordnung sichtbar, bevor
// jemand sie am Kontextfenster bemerkt.
func describedBulk(stored []store.GhostFile) string {
	total, largest := 0, 0
	for _, g := range stored {
		n := len(g.Description)
		total += n
		if n > largest {
			largest = n
		}
	}
	return fmt.Sprintf("%s Beschreibungstext, größte %s", humanBytes(total), humanBytes(largest))
}

func humanBytes(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f KB", float64(n)/1024)
}
