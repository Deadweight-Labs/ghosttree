package main

import (
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
	if err := requireEmptyOutDir(*outDir); err != nil {
		return err
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
		prepared:   map[agentbench.ArmName]agentbench.Agent{},
		workspaces: map[agentbench.ArmName]agentbench.Workspace{},
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

	records, runErr := agentbench.Run(ctx, campaign, tasks, agents)

	report := agentbench.BuildReport(campaign, records)
	report.Isolation = isolation
	if len(campaign.Arms) >= 2 {
		report.Effects = append(report.Effects, agentbench.PairedBootstrap(
			records, agentbench.ArmGhosttree, agentbench.ArmClaudeNative, campaign.Seed, 10000))
	}
	// Auch ein abgebrochener Lauf bekommt seine Ausgabe: die wenigen
	// Datensaetze sagen, woran es lag, und ohne sie muesste man den Abbruch
	// nachstellen, um ihn zu verstehen.
	if err := writeOutputs(report, *outDir); err != nil {
		return err
	}
	return runErr
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
	prepared   map[agentbench.ArmName]agentbench.Agent
	workspaces map[agentbench.ArmName]agentbench.Workspace
}

func (a *armAgents) For(arm agentbench.ArmName) (agentbench.Agent, error) {
	if agent, ok := a.prepared[arm]; ok {
		return agent, nil
	}
	spec := agentbench.WorkspaceSpec{RepoSource: a.repoSource}
	if arm == agentbench.ArmGhosttree {
		spec.GhostTreeSource = a.ghostTree
	}
	workspace, err := agentbench.PrepareWorkspace(
		filepath.Join(a.outDir, "workspaces", string(arm)), arm, spec)
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
