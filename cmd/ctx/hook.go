package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

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
// must never block a session from starting, nor stand between a person pressing
// enter and the model reading what they typed.
func cmdHook(args []string, stdout io.Writer) int {
	return cmdHookWith(os.Stdin, args, stdout)
}

// cmdHookWith takes the harness payload as a reader so the hooks can be tested
// without a process boundary.
func cmdHookWith(stdin io.Reader, args []string, stdout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stdout, hookUsage)
		return 2
	}
	var out sessionStartOutput
	switch args[0] {
	case "session-start":
		out.HookSpecificOutput.HookEventName = "SessionStart"
		out.HookSpecificOutput.AdditionalContext = bootstrapContext(stdin)
	case "user-prompt-submit":
		out.HookSpecificOutput.HookEventName = "UserPromptSubmit"
		out.HookSpecificOutput.AdditionalContext = relevantContext(stdin)
	default:
		fmt.Fprintln(stdout, hookUsage)
		return 2
	}
	json.NewEncoder(stdout).Encode(out)
	return 0
}

const hookUsage = `usage: ctx hook session-start
       ctx hook user-prompt-submit`

// relevanceTimeout is short because this hook sits between the keystroke and
// the answer. Knowledge that arrives late is worse than knowledge that does not
// arrive: the first costs the person their flow, the second costs a search they
// can still make.
const relevanceTimeout = 900 * time.Millisecond

// relevantContext answers a single prompt with whatever the archive gives a
// reason to say about it, which is usually nothing.
func relevantContext(stdin io.Reader) string {
	var in struct {
		Prompt string `json:"prompt"`
		CWD    string `json:"cwd"`
	}
	json.NewDecoder(stdin).Decode(&in)
	if strings.TrimSpace(in.Prompt) == "" {
		return ""
	}
	cfg, err := config.Load()
	if err != nil {
		return ""
	}
	cwd := in.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	gitCtx := collector.ResolveGitContext(cwd)
	md, err := client.NewWithTimeout(cfg, relevanceTimeout).Relevant(in.Prompt,
		scope.Axes{Project: gitCtx.Project, Branch: gitCtx.Branch, Lineage: gitCtx.Lineage, Machine: cfg.Machine}, 0)
	if err != nil {
		return ""
	}
	return md
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
		scope.Axes{Project: gitCtx.Project, Branch: gitCtx.Branch, Lineage: gitCtx.Lineage, Machine: cfg.Machine},
		activation.Context{RepoPath: gitCtx.RepoPath}, 0)
	if err != nil {
		return ""
	}
	return md
}
