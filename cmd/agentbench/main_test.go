package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/agentbench"
)

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestParseArmsRejectsAnUnknownName(t *testing.T) {
	if _, err := parseArms("telepathy"); err == nil {
		t.Fatal("expected an unknown arm to be rejected")
	}
}

func TestParseArmsKeepsTheGivenOrder(t *testing.T) {
	arms, err := parseArms("ghosttree, claude-native ,oracle")
	if err != nil {
		t.Fatal(err)
	}
	want := []agentbench.ArmName{agentbench.ArmGhosttree, agentbench.ArmClaudeNative, agentbench.ArmOracle}
	for i, arm := range want {
		if arms[i] != arm {
			t.Fatalf("want %v, got %v", want, arms)
		}
	}
}

func TestParseArmsRejectsAnEmptyList(t *testing.T) {
	if _, err := parseArms("  ,  "); err == nil {
		t.Fatal("a campaign without arms must be rejected")
	}
}

// writeCampaign builds a minimal but valid campaign plus one task on disk.
func writeCampaign(t *testing.T) (campaignPath, taskDir, outDir string) {
	t.Helper()
	root := t.TempDir()
	campaignPath = filepath.Join(root, "campaign.json")
	taskDir = filepath.Join(root, "tasks")
	outDir = filepath.Join(root, "out")
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}

	campaign := agentbench.Campaign{
		Name: "pilot", Repo: "NurProxy", RepoCommit: "a1b2c3d",
		Repetitions: 1, Seed: 1,
		Agent: agentbench.AgentConfig{CLI: "claude-code", ModelID: "test-model"},
	}
	campaign.KnowledgeCutoff = mustTime(t, "2026-08-31T14:00:00Z")
	raw, err := json.Marshal(campaign)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(campaignPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	task := `{"id":"t1","repo":"NurProxy","commit":"a1b2c3d","prompt":"p",
	  "category":"localization","exposure":{"answer_fact_seen":true},
	  "facts":[{"id":"tolerance","type":"integer","weight":1,"expected_int":60}]}`
	if err := os.WriteFile(filepath.Join(taskDir, "t1.json"), []byte(task), 0o644); err != nil {
		t.Fatal(err)
	}
	return campaignPath, taskDir, outDir
}

func TestRunCommandRejectsATaskOnAnotherCommit(t *testing.T) {
	campaignPath, taskDir, outDir := writeCampaign(t)
	bad := `{"id":"t2","repo":"NurProxy","commit":"deadbeef","prompt":"p",
	  "category":"localization","facts":[{"id":"x","type":"string","weight":1,"accepted":["y"]}]}`
	if err := os.WriteFile(filepath.Join(taskDir, "t2.json"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	err := runCommand([]string{
		"--campaign", campaignPath, "--tasks", taskDir, "--out", outDir,
		"--arms", "ghosttree", "--dry-run",
	}, &out, &errOut)

	if err == nil || !strings.Contains(err.Error(), "deadbeef") {
		t.Fatalf("a task pinned to another commit must stop the campaign, got %v", err)
	}
}

func TestRunCommandDryRunReportsThePlannedRuns(t *testing.T) {
	campaignPath, taskDir, outDir := writeCampaign(t)

	var out, errOut bytes.Buffer
	if err := runCommand([]string{
		"--campaign", campaignPath, "--tasks", taskDir, "--out", outDir,
		"--arms", "ghosttree,claude-native", "--dry-run",
	}, &out, &errOut); err != nil {
		t.Fatalf("dry run: %v (%s)", err, errOut.String())
	}
	if !strings.Contains(out.String(), "2") {
		t.Fatalf("dry run must state the planned run count:\n%s", out.String())
	}
}

func TestRunCommandRequiresACampaign(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := runCommand([]string{"--tasks", t.TempDir()}, &out, &errOut); err == nil {
		t.Fatal("a run without a campaign file must be refused")
	}
}

// TestArmAgentsRebuildsTheWorkspacePerRun guards the property the design
// claims: every run starts from the same state. The first version cached one
// workspace per arm, so a file an agent wrote during run 3 was context for run
// 4 — which correlates repetitions and favours whichever arm the block order
// puts first. It never happened in more than two hundred runs; nothing
// prevented it either.
func TestArmAgentsRebuildsTheWorkspacePerRun(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	agents := &armAgents{
		repoSource: source, outDir: out, binary: "claude",
		runtime:    agentbench.LocalRuntime{},
		workspaces: map[agentbench.ArmName]agentbench.Workspace{},
	}

	if _, err := agents.For(agentbench.ArmBare); err != nil {
		t.Fatal(err)
	}
	// Ein Agent hinterlaesst etwas im Arbeitsbereich.
	debris := filepath.Join(agents.workspaces[agentbench.ArmBare].Repo, "scratch.txt")
	if err := os.WriteFile(debris, []byte("vom vorigen Lauf"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := agents.For(agentbench.ArmBare); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(debris); !os.IsNotExist(err) {
		t.Fatal("the next run must not see what the previous one left behind")
	}
	if _, err := os.Stat(filepath.Join(agents.workspaces[agentbench.ArmBare].Repo, "main.go")); err != nil {
		t.Fatalf("the repository itself must be there again: %v", err)
	}
}
