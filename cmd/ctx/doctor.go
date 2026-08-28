package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	harness := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		harness, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stdout)
	fix := fs.Bool("fix", false, "re-run the installers for whatever is broken")
	var only repeatedStrings
	fs.Var(&only, "only", "check only this component (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		return 2
	}
	harnesses, selections, shared, err := resolveDoctorScope(harness, only)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 2
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stdout, "cannot determine home directory: %v\n", err)
		return 1
	}

	if *fix {
		for _, name := range harnesses {
			if _, err := installer.InstallSelected(name, home, selections[name]); err != nil {
				fmt.Fprintf(stdout, "fix %s: %v\n", name, err)
			}
		}
		fmt.Fprint(stdout, "re-ran installers\n\n")
	}

	var checks []installer.Check
	for _, name := range harnesses {
		checks = append(checks, installer.VerifySelected(name, home, selections[name])...)
	}
	checks = append(checks, selectedSharedChecks(shared, home)...)

	if printDoctorChecks(stdout, checks) {
		return 0
	}
	return 1
}

var sharedDoctorOrder = []string{"binary", "client", "server", "collector", "tree"}

func resolveDoctorScope(harness string, only []string) ([]string, map[string]installer.ComponentSet, map[string]bool, error) {
	harnesses := []string{"claude", "codex", "opencode"}
	if harness != "" {
		if len(installer.SupportedComponents(harness)) == 0 {
			return nil, nil, nil, fmt.Errorf("unknown harness %q", harness)
		}
		harnesses = []string{harness}
	}
	shared := map[string]bool{}
	var harnessOnly []string
	for _, value := range only {
		if slices.Contains(sharedDoctorOrder, value) {
			shared[value] = true
			continue
		}
		if !slices.Contains([]string{"mcp", "hooks", "rules", "skills"}, value) {
			return nil, nil, nil, fmt.Errorf("unknown component %q", value)
		}
		harnessOnly = append(harnessOnly, value)
	}
	selections := make(map[string]installer.ComponentSet)
	if len(only) == 0 {
		for _, name := range harnesses {
			selected, _ := installer.ResolveComponents(name, nil)
			selections[name] = selected
		}
		for _, name := range sharedDoctorOrder {
			shared[name] = true
		}
		return harnesses, selections, shared, nil
	}
	for _, name := range harnesses {
		selected := installer.ComponentSet{}
		supported := installer.SupportedComponents(name)
		for _, raw := range harnessOnly {
			component := installer.Component(raw)
			if slices.Contains(supported, component) {
				selected[component] = true
			} else if harness != "" {
				_, err := installer.ResolveComponents(name, []string{raw})
				return nil, nil, nil, err
			}
		}
		if len(selected) != 0 {
			selections[name] = selected
		}
	}
	if slices.Contains(harnessOnly, "mcp") {
		shared["binary"], shared["client"], shared["server"] = true, true, true
	}
	if slices.Contains(harnessOnly, "hooks") {
		shared["binary"] = true
	}
	selectedHarnesses := harnesses[:0]
	for _, name := range harnesses {
		if len(selections[name]) != 0 {
			selectedHarnesses = append(selectedHarnesses, name)
		}
	}
	return selectedHarnesses, selections, shared, nil
}

func selectedSharedChecks(selected map[string]bool, home string) []installer.Check {
	var checks []installer.Check
	for _, component := range sharedDoctorOrder {
		if !selected[component] {
			continue
		}
		switch component {
		case "binary":
			checks = append(checks, binaryCheck())
		case "client":
			cfgChecks := configChecks()
			if len(cfgChecks) > 0 {
				checks = append(checks, cfgChecks[0])
			}
		case "server":
			cfgChecks := configChecks()
			if len(cfgChecks) > 1 {
				checks = append(checks, cfgChecks[1])
			} else {
				checks = append(checks, installer.Check{Name: "server reachable", Detail: "not checked without client config", Fix: "run 'ctx setup --server <url> --token <token>'"})
			}
		case "collector":
			checks = append(checks, collectorChecks(home)...)
		case "tree":
			cwd, err := os.Getwd()
			if err == nil {
				gitCtx := collector.ResolveGitContext(cwd)
				if gitCtx.Root != "" {
					checks = append(checks, checkedInDocumentCheck(gitCtx.Root))
				}
			}
			checks = append(checks, ghostChecks()...)
		}
	}
	return checks
}

func checkedInDocumentCheck(repoRoot string) installer.Check {
	var found []string
	docsRoot := filepath.Join(repoRoot, "docs")
	_ = filepath.WalkDir(docsRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		wrapped := "/" + strings.ToLower(rel) + "/"
		if strings.HasSuffix(strings.ToLower(rel), ".md") &&
			(strings.Contains(wrapped, "/specs/") || strings.Contains(wrapped, "/plans/")) {
			found = append(found, rel)
		}
		return nil
	})
	if len(found) == 0 {
		return installer.Check{Name: "document worktree", OK: true, Detail: "no specs or plans checked into docs/"}
	}
	sort.Strings(found)
	shown := found
	if len(shown) > 5 {
		shown = shown[:5]
	}
	return installer.Check{
		Name:   "document worktree",
		Detail: fmt.Sprintf("%d checked-in specs or plans: %s", len(found), strings.Join(shown, ", ")),
		Fix:    "publish each file with 'ctx doc import <file> --kind <kind> --clean'",
	}
}

func printDoctorChecks(stdout io.Writer, checks []installer.Check) bool {
	width := 0
	for _, c := range checks {
		if len(c.Name) > width {
			width = len(c.Name)
		}
	}
	ok := true
	for _, c := range checks {
		status := "OK"
		if c.Unverified {
			status = "UNVERIFIED"
		} else if !c.OK {
			status, ok = "FAIL", false
		}
		fmt.Fprintf(stdout, "%-*s  %s  %s\n", width, c.Name, status, c.Detail)
		if !c.OK && c.Fix != "" {
			fmt.Fprintf(stdout, "%-*s        fix: %s\n", width, "", c.Fix)
		}
	}
	return ok
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

func collectorChecks(home string) []installer.Check {
	var checks []installer.Check
	roots := collector.DefaultRoots(home)
	for _, harness := range []string{"claude-code", "codex"} {
		var root string
		for path, owner := range roots {
			if owner == harness {
				root = path
				break
			}
		}
		check := installer.Check{Name: "collector " + harness + " root", Detail: root}
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			check.OK = true
		} else if os.IsNotExist(err) {
			check.Unverified = true
			check.Detail += " (no transcript directory yet)"
		} else if err != nil {
			check.Detail += ": " + err.Error()
		}
		checks = append(checks, check)
	}

	statePath := collector.DefaultStatePath()
	stateCheck := installer.Check{Name: "collector state", Detail: statePath}
	if _, err := os.Stat(statePath); os.IsNotExist(err) {
		stateCheck.Unverified = true
		stateCheck.Detail += " (not created yet)"
	} else if err != nil {
		stateCheck.Detail += ": " + err.Error()
	} else if state, err := collector.LoadState(statePath); err != nil {
		stateCheck.Detail += " (invalid: " + err.Error() + ")"
	} else {
		stateCheck.OK = true
		stateCheck.Detail = fmt.Sprintf("%s (%d transcripts)", statePath, len(state.Files))
	}
	checks = append(checks, stateCheck)

	active := installer.Check{Name: "collector active", Detail: "ghosttree-watch.service", Fix: "enable and start deploy/ghosttree-watch.service or run 'ctx watch'"}
	if pid, running := watchProcess(); running {
		active.OK = true
		active.Detail = fmt.Sprintf("watch process pid %d", pid)
		return append(checks, active)
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		active.Unverified = true
		active.Detail = "systemd user manager unavailable and no watch pid observed"
		return append(checks, active)
	}
	cmd := exec.Command("systemctl", "--user", "show", "ghosttree-watch.service", "--property=LoadState", "--property=ActiveState", "--value")
	raw, err := cmd.Output()
	if err != nil {
		active.Unverified = true
		active.Detail = "systemd user state unavailable and no watch pid observed"
		return append(checks, active)
	}
	values := strings.Fields(string(raw))
	if slices.Contains(values, "active") {
		active.OK = true
		active.Detail = "ghosttree-watch.service active"
	} else if slices.Contains(values, "not-found") {
		active.Unverified = true
		active.Detail = "ghosttree-watch.service not installed and no watch pid observed"
	} else {
		active.Detail = "ghosttree-watch.service " + strings.Join(values, "/")
	}
	return append(checks, active)
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
// Der Grund ist der Preis, den niemand sieht: der PreToolUse-Hook liefert beim
// Anfassen einer Datei deren Beschreibung samt der ihrer Vorfahren ungefragt
// aus. Es gibt dafür keine Obergrenze und keine Kürzung.
//
// Der Nenner ist dabei die SITZUNG und nicht der einzelne Zugriff: jeder
// betrachtete Pfad landet in ghost_deliveries und wird in derselben Sitzung
// nicht zweimal geliefert. Gemessen am 2026-08-26 gegen die Produktionsdaten
// bei 20 % Abdeckung: je Sitzung 18.054 Zeichen im Schnitt und 39.752 im
// schlechtesten Fall, je Auslieferung 1.128 im Schnitt. Volle Abdeckung
// verdoppelt das mindestens (REQ-198).
//
// Diese Zeile macht die Grössenordnung sichtbar, bevor jemand sie am
// Kontextfenster bemerkt.
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
