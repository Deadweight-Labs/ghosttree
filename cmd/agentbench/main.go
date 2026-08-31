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
	network := flags.String("network", "", "docker network; empty uses the Docker default")
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
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}

	runtime, err := selectRuntime(*runtimeName, *image, *network)
	if err != nil {
		return err
	}
	if slices.Contains(campaign.Arms, agentbench.ArmGhosttree) && *ghostTree == "" {
		return fmt.Errorf("--ghost-tree is required when the ghosttree arm runs; without it that arm measures an empty tree")
	}
	agents := &armAgents{
		campaign: campaign, repoSource: *repoSource, binary: *binary,
		outDir: *outDir, runtime: runtime, ghostTree: *ghostTree,
		prepared: map[agentbench.ArmName]agentbench.Agent{},
	}
	// Alle Arme werden vor dem ersten Lauf gebaut und geprueft: ein
	// Leakage-Befund im dritten Arm soll die Kampagne stoppen, bevor Tokens
	// in den ersten beiden verbrannt sind.
	for _, arm := range campaign.Arms {
		if _, err := agents.For(arm); err != nil {
			return err
		}
	}

	records, err := agentbench.Run(context.Background(), campaign, tasks, agents)
	if err != nil {
		return err
	}

	report := agentbench.BuildReport(campaign, records)
	if len(campaign.Arms) >= 2 {
		report.Effects = append(report.Effects, agentbench.PairedBootstrap(
			records, agentbench.ArmGhosttree, agentbench.ArmClaudeNative, campaign.Seed, 10000))
	}
	return writeOutputs(report, *outDir)
}

func selectRuntime(name, image, network string) (agentbench.Runtime, error) {
	switch name {
	case "docker":
		if image == "" {
			return nil, fmt.Errorf("--image is required for the docker runtime")
		}
		return agentbench.DockerRuntime{
			Image: image, Network: network,
			PassEnv: []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"},
		}, nil
	case "local":
		// Auf der Wirtsmaschine erbt jeder Arm die global installierten
		// Agent-Regeln und Werkzeuge. Fuer eine Kampagne ist das keine
		// gueltige Messung, nur zum Nachsehen eines einzelnen Laufs.
		return agentbench.LocalRuntime{}, nil
	default:
		return nil, fmt.Errorf("unknown runtime %q, want docker or local", name)
	}
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
	if findings := agentbench.CheckLeakage(workspace, arm, allowedFor(arm)); len(findings) > 0 {
		return nil, fmt.Errorf("leakage check failed for arm %q: %+v", arm, findings)
	}
	agent := agentbench.NewClaudeCodeAgent(a.binary, workspace,
		filepath.Join(a.outDir, "raw", string(arm)), a.runtime)
	a.prepared[arm] = agent
	return agent, nil
}

func allowedFor(arm agentbench.ArmName) agentbench.AllowedSurface {
	return agentbench.AllowedSurface{
		GhostTree:  arm == agentbench.ArmGhosttree,
		ClaudeMD:   arm != agentbench.ArmBare,
		AutoMemory: arm == agentbench.ArmClaudeNative,
		MemoryDir:  arm == agentbench.ArmClaudeMem || arm == agentbench.ArmAgentMemory,
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
