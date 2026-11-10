package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/runbook"
)

// The tool hook runs before every tool call, and nearly none of them matches a
// runbook trigger. Asking the server each time cost one request per Bash call.
// So the hook keeps the command and path triggers of the project for a few
// minutes, matches them with the server's own rule (runbook.Spec.Match) and
// asks the server only for a call that one of them matches. The server still
// decides what is delivered, and delivers a runbook once per session.
//
// A runbook approved in the meantime reaches the tool hook when the copy is
// older than runbookTriggerTTL; the prompt hook always asks the server.
const (
	runbookTriggerTTL = 5 * time.Minute
	// A server without the route is asked as before, before every call, and
	// not asked for the route again for this long.
	runbookTriggerRouteRetry = time.Hour
)

type runbookTriggerCache struct {
	FetchedAt   int64              `json:"fetched_at"`
	Unsupported bool               `json:"unsupported,omitempty"`
	Sets        []runbook.Triggers `json:"sets"`
}

// hookStateDir is where hooks keep what they remember between calls.
func hookStateDir() (string, error) {
	root := os.Getenv("XDG_STATE_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(home, ".local", "state")
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("state directory must be absolute")
	}
	return filepath.Join(root, "ghosttree"), nil
}

func runbookTriggerCachePath(cfg config.Config, project string) (string, error) {
	dir, err := hookStateDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(cfg.ServerURL + "\x00" + project + "\x00" + cfg.Machine))
	return filepath.Join(dir, "runbook-triggers", hex.EncodeToString(sum[:12])+".json"), nil
}

func loadRunbookTriggerCache(path string) (runbookTriggerCache, bool) {
	var c runbookTriggerCache
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &c) != nil {
		return runbookTriggerCache{}, false
	}
	return c, true
}

// saveRunbookTriggerCache replaces the file in one step: hooks of parallel tool
// calls write it at the same time and none may read half of it.
func saveRunbookTriggerCache(path string, c runbookTriggerCache) {
	raw, err := json.Marshal(c)
	if err != nil || os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	tmp := path + "." + strconv.Itoa(os.Getpid())
	if os.WriteFile(tmp, raw, 0o600) != nil {
		return
	}
	if os.Rename(tmp, path) != nil {
		_ = os.Remove(tmp)
	}
}

// runbookTriggerSets returns the command and path triggers that apply here.
// known is false when they cannot be told; the caller then asks the server
// about the call itself, as every call did before.
func runbookTriggerSets(cfg config.Config, project string) (sets []runbook.Triggers, known bool) {
	path, pathErr := runbookTriggerCachePath(cfg, project)
	if pathErr == nil {
		if c, ok := loadRunbookTriggerCache(path); ok {
			age := time.Since(time.Unix(c.FetchedAt, 0))
			if c.Unsupported && age >= 0 && age < runbookTriggerRouteRetry {
				return nil, false
			}
			if !c.Unsupported && age >= 0 && age < runbookTriggerTTL {
				return c.Sets, true
			}
		}
	}
	sets, err := client.NewWithTimeout(cfg, relevanceTimeout).RunbookTriggerSets(project, cfg.Machine)
	if err == nil {
		if pathErr == nil {
			saveRunbookTriggerCache(path, runbookTriggerCache{FetchedAt: time.Now().Unix(), Sets: sets})
		}
		return sets, true
	}
	if client.IsRouteMissing(err) {
		if pathErr == nil {
			saveRunbookTriggerCache(path, runbookTriggerCache{FetchedAt: time.Now().Unix(), Unsupported: true})
		}
		return nil, false
	}
	// A server that did not answer, or is broken, would not answer the
	// question about this call either: a second wait buys nothing. Any other
	// answer is not understood, and the call is asked about as before.
	var status *client.StatusError
	var transport *url.Error
	if errors.As(err, &transport) || (errors.As(err, &status) && status.Status >= 500) {
		return nil, true
	}
	return nil, false
}

// toolMayTriggerRunbook says whether the server has to be asked about this
// tool call at all.
func toolMayTriggerRunbook(cfg config.Config, gitCtx collector.GitContext, t runbookTrigger) bool {
	if t.command == "" && len(t.paths) == 0 {
		return false
	}
	sets, known := runbookTriggerSets(cfg, gitCtx.Project)
	if !known {
		return true
	}
	return anyTriggerMatches(sets, runbook.Input{Command: t.command, RepoPath: gitCtx.RepoPath, Paths: t.paths})
}

// anyTriggerMatches applies the rule the server applies to each runbook.
func anyTriggerMatches(sets []runbook.Triggers, in runbook.Input) bool {
	for _, s := range sets {
		if _, ok := (runbook.Spec{Triggers: s}).Match(in); ok {
			return true
		}
	}
	return false
}
