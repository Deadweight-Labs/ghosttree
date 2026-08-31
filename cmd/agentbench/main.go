package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	binary := flags.String("agent-binary", "claude", "agent CLI to invoke")
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

	agent, err := prepareAgent(campaign, *repoSource, *binary, *outDir)
	if err != nil {
		return err
	}
	records, err := agentbench.Run(context.Background(), campaign, tasks, agent)
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

// prepareAgent builds one workspace per campaign and refuses to run when the
// leakage check finds anything the arm is not entitled to. A finding blocks
// the campaign rather than annotating it.
func prepareAgent(campaign agentbench.Campaign, repoSource, binary, outDir string) (agentbench.Agent, error) {
	arm := campaign.Arms[0]
	workspace, err := agentbench.PrepareWorkspace(filepath.Join(outDir, "workspace"), arm,
		agentbench.WorkspaceSpec{RepoSource: repoSource})
	if err != nil {
		return nil, err
	}
	allowed := agentbench.AllowedSurface{
		GhostTree:  arm == agentbench.ArmGhosttree,
		ClaudeMD:   arm != agentbench.ArmBare,
		AutoMemory: arm == agentbench.ArmClaudeNative,
		MemoryDir:  arm == agentbench.ArmClaudeMem || arm == agentbench.ArmAgentMemory,
	}
	if findings := agentbench.CheckLeakage(workspace, arm, allowed); len(findings) > 0 {
		return nil, fmt.Errorf("leakage check failed for arm %q: %+v", arm, findings)
	}
	return agentbench.NewClaudeCodeAgent(binary, workspace, filepath.Join(outDir, "raw")), nil
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
