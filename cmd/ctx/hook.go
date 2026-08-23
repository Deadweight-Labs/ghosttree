package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

type sessionStartOutput struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// cmdHook always exits 0 with a well-formed payload: a dead ghosttree server
// must never block a session from starting.
func cmdHook(args []string, stdout io.Writer) int {
	if len(args) == 0 || args[0] != "session-start" {
		fmt.Fprintln(stdout, "usage: ctx hook session-start")
		return 2
	}
	var out sessionStartOutput
	out.HookSpecificOutput.HookEventName = "SessionStart"
	out.HookSpecificOutput.AdditionalContext = bootstrapContext(os.Stdin)
	json.NewEncoder(stdout).Encode(out)
	return 0
}

func bootstrapContext(stdin io.Reader) string {
	var in struct {
		CWD       string `json:"cwd"`
		SessionID string `json:"session_id"`
	}
	json.NewDecoder(stdin).Decode(&in)
	cfg, err := config.Load()
	if err != nil {
		return ""
	}
	cwd := in.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	gitCtx := collector.ResolveGitContext(cwd)
	md, err := client.New(cfg).Bootstrap(
		scope.Axes{Project: gitCtx.Project, Branch: gitCtx.Branch, Machine: cfg.Machine},
		activation.Context{RepoPath: gitCtx.RepoPath}, 0)
	if err != nil {
		return ""
	}
	return md
}
