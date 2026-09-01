package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/agentbench"
)

func main() {
	if err := runCommand(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("agentbench", flag.ContinueOnError)
	flags.SetOutput(stderr)
	campaignPath := flags.String("campaign", "", "campaign definition (JSON)")
	taskDir := flags.String("tasks", "tasks", "directory holding task files")
	armList := flags.String("arms", "claude-native,ghosttree", "comma-separated arms")
	outDir := flags.String("out", "", "directory for transcripts, JSONL and the report")
	repoSource := flags.String("repo", "", "repository checkout on the campaign commit")
	ghostTree := flags.String("ghost-tree", "", "pinned .ghosttree mirror for the ghosttree arm")
	binary := flags.String("agent-binary", "claude", "agent CLI to invoke")
	runtimeName := flags.String("runtime", "docker", "docker or local; local is not a valid campaign")
	image := flags.String("image", "agentbench:dev", "container image for the docker runtime")
	network := flags.String("network", "", "docker network; ignored when the network is sealed")
	seal := flags.Bool("seal-network", true, "run inside an internal network whose only way out is the allowlisted proxy")
	proxyImage := flags.String("proxy-image", "agentbench-proxy:dev", "container image for the sealing proxy")
	domains := flags.String("allowed-domains", strings.Join(agentbench.DefaultAllowedDomains, ","),
		"comma-separated hosts the sealed network may reach")
	allowOpenNetwork := flags.Bool("allow-open-network", false,
		"accept runs that can reach the open internet; never right for a campaign")
	modelURL := flags.String("model-url", "https://api.anthropic.com/",
		"URL the probe expects to reach; must match the endpoint the agent talks to")
	dryRun := flags.Bool("dry-run", false, "validate and print the plan without running agents")
	regrade := flags.String("regrade", "",
		"score an existing run directory again from its raw transcripts instead of running agents")
	resume := flags.Bool("resume", false,
		"continue an interrupted campaign in --out: keep what is recorded, recover what only exists as a transcript, run the rest")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if *campaignPath == "" {
		return fmt.Errorf("--campaign is required")
	}

	campaign, err := loadCampaign(*campaignPath)
	if err != nil {
		return err
	}
	if campaign.Arms, err = parseArms(*armList); err != nil {
		return err
	}
	tasks, err := agentbench.LoadTasks(*taskDir)
	if err != nil {
		return err
	}
	if err := campaign.Validate(tasks); err != nil {
		return err
	}
	if campaign.Repetitions <= 0 {
		return fmt.Errorf("campaign %q: repetitions must be positive", campaign.Name)
	}

	if *regrade != "" {
		return regradeRun(campaign, tasks, *regrade, *outDir, stdout)
	}

	planned := len(tasks) * len(campaign.Arms) * campaign.Repetitions
	fmt.Fprintf(stdout, "campaign %q: %d tasks x %d arms x %d repetitions = %d runs\n",
		campaign.Name, len(tasks), len(campaign.Arms), campaign.Repetitions, planned)
	if *dryRun {
		return nil
	}

	if *outDir == "" {
		return fmt.Errorf("--out is required for a real run")
	}
	if *repoSource == "" {
		return fmt.Errorf("--repo is required for a real run")
	}
	// Der Zugangsdaten-Test steht vor allem anderen: fehlt er, endet jeder
	// einzelne Lauf in "Not logged in", und das faellt sonst erst nach
	// Stunden auf.
	credential, err := agentbench.RequireModelCredential(agentbench.EnvLookup)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "model credential: %s (value never logged, never on a command line)\n", credential)
	if !*resume {
		if err := requireEmptyOutDir(*outDir); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}

	ctx := context.Background()
	runtime, isolation, err := selectRuntime(ctx, runtimeOptions{
		name: *runtimeName, image: *image, network: *network,
		seal: *seal, proxyImage: *proxyImage, domains: splitList(*domains),
	})
	if err != nil {
		return err
	}
	probeSpec := agentbench.ProbeSpec{ModelURL: *modelURL}
	if !isolation.Sealed && !*allowOpenNetwork {
		return fmt.Errorf("the runs would reach the open internet; seal the network or pass --allow-open-network " +
			"and accept that the report calls the numbers unsealed")
	}
	if slices.Contains(campaign.Arms, agentbench.ArmGhosttree) && *ghostTree == "" {
		return fmt.Errorf("--ghost-tree is required when the ghosttree arm runs; without it that arm measures an empty tree")
	}
	agents := &armAgents{
		campaign: campaign, repoSource: *repoSource, binary: *binary,
		outDir: *outDir, runtime: runtime, ghostTree: *ghostTree,
		rebuild:     *resume,
		openNetwork: *allowOpenNetwork,
		prepared:    map[agentbench.ArmName]agentbench.Agent{},
		workspaces:  map[agentbench.ArmName]agentbench.Workspace{},
	}
	// Alle Arme werden vor dem ersten Lauf gebaut und geprueft: ein
	// Leakage-Befund im dritten Arm soll die Kampagne stoppen, bevor Tokens
	// in den ersten beiden verbrannt sind.
	for _, arm := range campaign.Arms {
		if _, err := agents.For(arm); err != nil {
			return err
		}
		findings, err := agentbench.CheckRuntimeLeakage(ctx, runtime,
			agents.workspaces[arm], arm, allowedFor(arm, *allowOpenNetwork), probeSpec)
		if err != nil {
			return fmt.Errorf("runtime probe for arm %q: %w", arm, err)
		}
		if len(findings) > 0 {
			return fmt.Errorf("runtime probe failed for arm %q: %+v", arm, findings)
		}
		fmt.Fprintf(stdout, "arm %s: workspace prepared, probe clean\n", arm)
	}

	if *resume {
		// Eine fortgesetzte Kampagne muss dieselben Fragen stellen. Sonst
		// stuenden im selben Journal Antworten auf zwei verschiedene
		// Aufgabensaetze, und keine Zeile sagte, welche zu welchem gehoert.
		if err := requireSameTaskSet(tasks, *outDir); err != nil {
			return err
		}
	}
	// Der Aufgabensatz gehoert zum Lauf, nicht zum Verzeichnis, aus dem er
	// geladen wurde. Ohne diese Kopie rechnet eine spaetere Nachbewertung
	// gegen die inzwischen geaenderten Aufgaben — dieselben Transkripte,
	// andere Zahlen, und nichts sagt, dass sich etwas verschoben hat.
	if err := writeTaskSet(tasks, *outDir); err != nil {
		return err
	}
	journal, err := agentbench.OpenRunJournal(filepath.Join(*outDir, "runs.jsonl"))
	if err != nil {
		return err
	}
	if *resume {
		recovered, err := recoverOrphans(campaign, tasks, journal, filepath.Join(*outDir, "raw"))
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "resuming: %d runs already recorded, %d recovered from transcripts, %d to go\n",
			len(journal.Records())-recovered, recovered, planned-len(journal.Records()))
	}

	_, runErr := agentbench.Run(ctx, campaign, tasks, agents, journal)
	if err := journal.Close(); err != nil {
		return err
	}
	// Ausgewertet wird, was im Journal steht, nicht was dieser Aufruf
	// erzeugt hat: bei einer fortgesetzten Kampagne ist das zweite nur das
	// letzte Stueck.
	all := journal.Records()

	report := agentbench.BuildReport(campaign, all)
	report.Isolation = isolation
	report.Effects = contrastsAgainstGhosttree(all, campaign)
	// Auch ein abgebrochener Lauf bekommt seine Ausgabe: die wenigen
	// Datensaetze sagen, woran es lag, und ohne sie muesste man den Abbruch
	// nachstellen, um ihn zu verstehen.
	if err := writeOutputs(report, *outDir); err != nil {
		return err
	}
	return runErr
}

// recoverOrphans takes the transcripts of runs whose record never reached the
// journal and turns them back into records. This is the case a crash leaves
// behind: the agent writes its transcript per run, so the evidence is complete
// while the ledger is empty. Without this the resumed campaign would pay for
// those runs a second time.
func recoverOrphans(campaign agentbench.Campaign, tasks []agentbench.Task,
	journal *agentbench.RunJournal, rawDir string) (int, error) {
	recovered, err := agentbench.RecoverFromTranscripts(campaign, tasks, rawDir)
	if err != nil {
		return 0, err
	}
	var count int
	for _, record := range recovered {
		if journal.Done(record.TaskID, record.Arm, record.Repetition) {
			continue
		}
		if err := journal.Emit(record); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// regradeRun re-scores a finished campaign. The transcripts are the evidence
// and stay untouched; only the judgement changes. Without this a wrong list of
// accepted spellings would cost a whole campaign to fix — and the corrected
// numbers would carry fresh sampling noise, so they could not be compared with
// the ones they replace.
func regradeRun(campaign agentbench.Campaign, tasks []agentbench.Task, runDir, outDir string, stdout io.Writer) error {
	tasks, err := taskSetFor(runDir, tasks, stdout)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(runDir, "runs.jsonl"))
	// Ohne Journal wird aus den Transkripten gebaut. Ein abgestuerzter Lauf
	// hat genau diese Form: die Evidenz vollstaendig, die Urteile weg.
	if os.IsNotExist(err) {
		records, err := agentbench.RecoverFromTranscripts(campaign, tasks, filepath.Join(runDir, "raw"))
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "no runs.jsonl in %s; recovered %d runs from the transcripts\n", runDir, len(records))
		return writeRegraded(campaign, records, runDir, outDir, stdout)
	}
	if err != nil {
		return err
	}
	var records []agentbench.RunRecord
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record agentbench.RunRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return fmt.Errorf("%s/runs.jsonl: %w", runDir, err)
		}
		records = append(records, record)
	}

	regraded, err := agentbench.Regrade(records, tasks)
	if err != nil {
		return err
	}
	return writeRegraded(campaign, regraded, runDir, outDir, stdout)
}

func writeRegraded(campaign agentbench.Campaign, records []agentbench.RunRecord,
	runDir, outDir string, stdout io.Writer) error {
	report := agentbench.BuildReport(campaign, records)
	report.Effects = contrastsAgainstGhosttree(records, campaign)
	if outDir == "" {
		outDir = runDir
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "regraded %d runs from %s into %s\n", len(records), runDir, outDir)
	return writeOutputs(report, outDir)
}

// contrastsAgainstGhosttree pairs the treatment against every control arm the
// campaign actually ran. A fixed contrast against claude-native produced an
// empty table whenever that arm was not part of the run — the report looked
// complete and said nothing.
func contrastsAgainstGhosttree(records []agentbench.RunRecord, campaign agentbench.Campaign) []agentbench.PairedEffect {
	var effects []agentbench.PairedEffect
	for _, arm := range campaign.Arms {
		if arm == agentbench.ArmGhosttree {
			continue
		}
		for _, metric := range agentbench.DefaultMetrics {
			effect := agentbench.PairedBootstrapMetric(
				records, agentbench.ArmGhosttree, arm, metric, campaign.Seed, 10000)
			if effect.Tasks > 0 {
				effects = append(effects, effect)
			}
		}
	}
	return effects
}

type runtimeOptions struct {
	name, image, network, proxyImage string
	seal                             bool
	domains                          []string
}

func selectRuntime(ctx context.Context, opts runtimeOptions) (agentbench.Runtime, agentbench.Isolation, error) {
	switch opts.name {
	case "docker":
		if opts.image == "" {
			return nil, agentbench.Isolation{}, fmt.Errorf("--image is required for the docker runtime")
		}
		runtime := agentbench.DockerRuntime{
			Image: opts.image, Network: opts.network,
			PassEnv: append(append([]string{}, agentbench.ModelCredentialVars...), agentbench.ModelConfigVars...),
		}
		isolation := agentbench.Isolation{Runtime: "docker", Image: opts.image, Network: opts.network}
		if opts.seal {
			sealed, err := agentbench.EnsureSealedNetwork(ctx, agentbench.NetworkSpec{
				ProxyImage: opts.proxyImage, Allowed: opts.domains,
			})
			if err != nil {
				return nil, agentbench.Isolation{}, err
			}
			runtime.Network, runtime.ProxyURL = sealed.Name, sealed.ProxyURL
			isolation.Network, isolation.AllowedDomains, isolation.Sealed = sealed.Name, sealed.Allowed, true
		}
		return runtime, isolation, nil
	case "local":
		// Auf der Wirtsmaschine erbt jeder Arm die global installierten
		// Agent-Regeln und Werkzeuge. Fuer eine Kampagne ist das keine
		// gueltige Messung, nur zum Nachsehen eines einzelnen Laufs.
		return agentbench.LocalRuntime{}, agentbench.Isolation{Runtime: "local"}, nil
	default:
		return nil, agentbench.Isolation{}, fmt.Errorf("unknown runtime %q, want docker or local", opts.name)
	}
}

// requireEmptyOutDir keeps two campaigns from sharing an output directory.
// Their JSONL and their report would land in the same files, and the mix
// would look like one campaign with contradictory numbers.
func requireEmptyOutDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return nil
	}
	return fmt.Errorf("--out %s is not empty; a campaign writes into a fresh directory "+
		"so its report and transcripts cannot be mixed with another run's", dir)
}

func splitList(list string) []string {
	var out []string
	for _, item := range strings.Split(list, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// armAgents builds one workspace per arm and refuses to hand out an agent
// whose arm sees anything it is not entitled to. A leakage finding stops the
// campaign rather than annotating it.
type armAgents struct {
	campaign   agentbench.Campaign
	repoSource string
	binary     string
	outDir     string
	runtime    agentbench.Runtime
	ghostTree  string
	// rebuild throws an existing workspace away instead of refusing it. A
	// resumed campaign starts its arms from the checkout again, which is
	// exactly the state the campaign began in — the alternative would be to
	// hand the resumed segment a workspace that earlier runs had already
	// walked through.
	rebuild bool
	// openNetwork wird fuer die Nachpruefung nach jedem Lauf gebraucht: die
	// erlaubte Oberflaeche eines Arms haengt daran.
	openNetwork bool
	prepared    map[agentbench.ArmName]agentbench.Agent
	workspaces  map[agentbench.ArmName]agentbench.Workspace
}

func (a *armAgents) For(arm agentbench.ArmName) (agentbench.Agent, error) {
	if agent, ok := a.prepared[arm]; ok {
		return agent, nil
	}
	spec := agentbench.WorkspaceSpec{RepoSource: a.repoSource}
	if arm == agentbench.ArmGhosttree {
		spec.GhostTreeSource = a.ghostTree
	}
	// Die CLAUDE.md des Projekts ist die Baseline aller erweiterten Arme —
	// die echte, nicht eine nachgebaute. PrepareWorkspace entfernt sie
	// zuerst und schreibt sie nur fuer berechtigte Arme zurueck, damit der
	// bare-Arm sie garantiert nicht sieht.
	if arm != agentbench.ArmBare {
		md, err := os.ReadFile(filepath.Join(a.repoSource, "CLAUDE.md"))
		if err == nil {
			spec.ClaudeMD = string(md)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		// Ein handgepflegtes .claude/wiki gehoert zur selben Baseline. Ohne
		// es zu geben, misst der Kontrollarm weniger, als das Projekt
		// tatsaechlich hat, und der Vorsprung des Behandlungsarms waere
		// teilweise nur der geloeschte Ordner.
		claudeDir := filepath.Join(a.repoSource, ".claude")
		if info, err := os.Stat(claudeDir); err == nil && info.IsDir() {
			spec.ClaudeDirSource = claudeDir
		} else if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	dir := filepath.Join(a.outDir, "workspaces", string(arm))
	if a.rebuild {
		if err := os.RemoveAll(dir); err != nil {
			return nil, err
		}
	}
	workspace, err := agentbench.PrepareWorkspace(dir, arm, spec)
	if err != nil {
		return nil, err
	}
	if findings := agentbench.CheckLeakage(workspace, arm, allowedFor(arm, false)); len(findings) > 0 {
		return nil, fmt.Errorf("leakage check failed for arm %q: %+v", arm, findings)
	}
	agent := agentbench.NewClaudeCodeAgent(a.binary, workspace,
		filepath.Join(a.outDir, "raw", string(arm)), a.runtime)
	a.prepared[arm] = agent
	a.workspaces[arm] = workspace
	return agent, nil
}

// AfterRun is called by the runner after every run. It asks the cheap question
// that a shared HOME makes necessary: did this run leave something behind that
// the arm's next run would read?
func (a *armAgents) AfterRun(arm agentbench.ArmName) error {
	ws, ok := a.workspaces[arm]
	if !ok {
		return nil
	}
	if findings := agentbench.CheckHomeDrift(ws, arm, allowedFor(arm, a.openNetwork)); len(findings) > 0 {
		return fmt.Errorf("arm %q is no longer the arm it is named after: %+v", arm, findings)
	}
	return nil
}

func allowedFor(arm agentbench.ArmName, openNetwork bool) agentbench.AllowedSurface {
	return agentbench.AllowedSurface{
		GhostTree:   arm == agentbench.ArmGhosttree,
		ClaudeMD:    arm != agentbench.ArmBare,
		AutoMemory:  arm == agentbench.ArmClaudeNative,
		MemoryDir:   arm == agentbench.ArmClaudeMem || arm == agentbench.ArmAgentMemory,
		OpenNetwork: openNetwork,
	}
}

func writeOutputs(report agentbench.Report, outDir string) error {
	jsonl, err := os.Create(filepath.Join(outDir, "runs.jsonl"))
	if err != nil {
		return err
	}
	defer jsonl.Close()
	if err := report.WriteJSONL(jsonl); err != nil {
		return err
	}

	markdown, err := os.Create(filepath.Join(outDir, "report.md"))
	if err != nil {
		return err
	}
	defer markdown.Close()
	return report.WriteMarkdown(markdown)
}

const taskSetFile = "tasks.json"

// writeTaskSet pins the questions a run was actually asked. A task file edited
// afterwards would otherwise re-score the same transcripts to different
// numbers, with nothing on disk saying that anything moved.
func writeTaskSet(tasks []agentbench.Task, outDir string) error {
	raw, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, taskSetFile), raw, 0o644)
}

// requireSameTaskSet refuses to continue a campaign with different questions.
// The comparison is over the marshalled definitions, so a changed accepted
// spelling counts as a change — it is one.
func requireSameTaskSet(tasks []agentbench.Task, outDir string) error {
	previous, err := os.ReadFile(filepath.Join(outDir, taskSetFile))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	current, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return err
	}
	if !bytes.Equal(bytes.TrimSpace(previous), bytes.TrimSpace(current)) {
		return fmt.Errorf(
			"the task set in %s/%s differs from --tasks; a resumed campaign must ask the same questions. "+
				"Start a new run directory, or regrade the old one against its own %s",
			outDir, taskSetFile, taskSetFile)
	}
	return nil
}

// taskSetFor prefers the copy a run left behind over whatever --tasks points at
// today. A regrade that silently used a newer question would be the quietest
// way imaginable to publish a wrong number.
func taskSetFor(runDir string, fallback []agentbench.Task, stdout io.Writer) ([]agentbench.Task, error) {
	raw, err := os.ReadFile(filepath.Join(runDir, taskSetFile))
	if os.IsNotExist(err) {
		return fallback, nil
	}
	if err != nil {
		return nil, err
	}
	var pinned []agentbench.Task
	if err := json.Unmarshal(raw, &pinned); err != nil {
		return nil, fmt.Errorf("%s/%s: %w", runDir, taskSetFile, err)
	}
	fmt.Fprintf(stdout, "using the %d tasks recorded in %s/%s, not --tasks\n",
		len(pinned), runDir, taskSetFile)
	return pinned, nil
}

func loadCampaign(path string) (agentbench.Campaign, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return agentbench.Campaign{}, err
	}
	var campaign agentbench.Campaign
	if err := json.Unmarshal(raw, &campaign); err != nil {
		return agentbench.Campaign{}, fmt.Errorf("%s: %w", path, err)
	}
	return campaign, nil
}

func parseArms(list string) ([]agentbench.ArmName, error) {
	known := map[string]agentbench.ArmName{
		"bare":          agentbench.ArmBare,
		"claudemd":      agentbench.ArmClaudeMD,
		"claude-native": agentbench.ArmClaudeNative,
		"claude-mem":    agentbench.ArmClaudeMem,
		"agentmemory":   agentbench.ArmAgentMemory,
		"ghosttree":     agentbench.ArmGhosttree,
		"oracle":        agentbench.ArmOracle,
	}
	var arms []agentbench.ArmName
	for _, name := range strings.Split(list, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		arm, ok := known[name]
		if !ok {
			return nil, fmt.Errorf("unknown arm %q", name)
		}
		arms = append(arms, arm)
	}
	if len(arms) == 0 {
		return nil, fmt.Errorf("at least one arm is required")
	}
	return arms, nil
}
