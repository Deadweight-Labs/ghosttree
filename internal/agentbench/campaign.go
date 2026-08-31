package agentbench

import (
	"fmt"
	"time"
)

type ArmName string

const (
	ArmBare         ArmName = "bare"
	ArmClaudeMD     ArmName = "claudemd"
	ArmClaudeNative ArmName = "claude-native"
	ArmClaudeMem    ArmName = "claude-mem"
	ArmAgentMemory  ArmName = "agentmemory"
	ArmGhosttree    ArmName = "ghosttree"
	ArmOracle       ArmName = "oracle"
)

type MemoryBuild struct {
	SourceCorpusSHA256 string    `json:"source_corpus_sha256"`
	SourceSessionCount int       `json:"source_session_count"`
	SourceCutoff       time.Time `json:"source_cutoff"`
	IngestionOrder     string    `json:"ingestion_order"`
	ToolVersion        string    `json:"tool_version"`
	ToolCommit         string    `json:"tool_commit"`
	ConfigSHA256       string    `json:"config_sha256"`
	SummarizerModel    string    `json:"summarizer_model,omitempty"`
	DatabaseSHA256     string    `json:"database_sha256"`
	SnapshotName       string    `json:"snapshot_name,omitempty"`
	BuildFailures      int       `json:"build_failures"`
}

type AgentConfig struct {
	CLI             string `json:"cli"`
	CLIVersion      string `json:"cli_version"`
	ModelID         string `json:"model_id"`
	ReasoningEffort string `json:"reasoning_effort"`
	PermissionMode  string `json:"permission_mode"`
	MaxTurns        int    `json:"max_turns"`
	MaxInputTokens  int    `json:"max_input_tokens"`
	MaxOutputTokens int    `json:"max_output_tokens"`
	TimeoutSeconds  int    `json:"timeout_seconds"`
}

type Campaign struct {
	Name            string                  `json:"name"`
	Repo            string                  `json:"repo"`
	RepoCommit      string                  `json:"repo_commit"`
	KnowledgeCutoff time.Time               `json:"knowledge_cutoff"`
	TaskSetSHA256   string                  `json:"task_set_sha256"`
	Arms            []ArmName               `json:"arms"`
	Repetitions     int                     `json:"repetitions"`
	Seed            uint64                  `json:"seed"`
	Builds          map[ArmName]MemoryBuild `json:"builds"`
	Agent           AgentConfig             `json:"agent"`
}

func (c Campaign) Validate(tasks []Task) error {
	if c.RepoCommit == "" {
		return fmt.Errorf("campaign %q: repo_commit is required", c.Name)
	}
	if c.KnowledgeCutoff.IsZero() {
		return fmt.Errorf("campaign %q: knowledge_cutoff is required", c.Name)
	}
	for arm, build := range c.Builds {
		if build.SourceCutoff.After(c.KnowledgeCutoff) {
			return fmt.Errorf("arm %q: source cutoff %s is after the campaign cutoff %s (future leakage)",
				arm, build.SourceCutoff.Format(time.RFC3339), c.KnowledgeCutoff.Format(time.RFC3339))
		}
	}
	for _, task := range tasks {
		if task.Commit != c.RepoCommit {
			return fmt.Errorf("task %q is pinned to %q but the campaign runs %q",
				task.ID, task.Commit, c.RepoCommit)
		}
	}
	return nil
}
