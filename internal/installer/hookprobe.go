package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/hookstate"
)

var hookCommandProbe = runHookCommandProbe

const hookActivityMaxAge = 30 * 24 * time.Hour

func hookRuntimeChecks(h Harness, home string) []Check {
	var checks []Check
	for _, channel := range h.Channels {
		event, command, _, ok := h.hookCommandFor(channel)
		if !ok {
			continue
		}
		name := h.Name + " " + string(channel)
		runnable := Check{Name: name + " runnable", Component: ComponentHooks, Detail: command, Fix: "run 'ctx install " + h.Name + " --only hooks'"}
		if err := hookCommandProbe(command, event); err != nil {
			runnable.Detail = err.Error()
		} else {
			runnable.OK = true
		}
		checks = append(checks, runnable)

		active := Check{Name: name + " active", Component: ComponentHooks, Detail: hookstate.DefaultPath(), Fix: "start a fresh " + h.Name + " session and trigger the event"}
		receipt, found, err := hookstate.Latest(h.Name, event)
		now := time.Now().UTC()
		var hookModified time.Time
		if info, statErr := os.Stat(h.HooksPath(home)); statErr == nil {
			hookModified = info.ModTime()
		}
		switch {
		case err != nil:
			active.Detail += ": " + err.Error()
		case !found:
			active.Unverified = true
			active.Detail += " (no real invocation observed)"
		case receipt.SeenAt.IsZero() || now.Sub(receipt.SeenAt) > hookActivityMaxAge:
			active.Unverified = true
			active.Detail += " (stale real invocation receipt)"
		case receipt.SeenAt.After(now.Add(5 * time.Minute)):
			active.Unverified = true
			active.Detail += " (receipt timestamp is in the future)"
		case !hookModified.IsZero() && receipt.SeenAt.Before(hookModified):
			active.Unverified = true
			active.Detail += " (receipt predates the installed hook definition)"
		default:
			active.OK = true
			active.Detail = receipt.SeenAt.Local().Format(time.RFC3339)
		}
		checks = append(checks, active)
	}
	return checks
}

func runHookCommandProbe(command, event string) error {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return fmt.Errorf("empty hook command")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, fields[0], fields[1:]...)
	cmd.Env = append(os.Environ(), "GHOSTTREE_HOOK_SYNTHETIC=1")
	cmd.Stdin = strings.NewReader(`{"cwd":"/tmp","prompt":"","tool_input":{}}`)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("timed out running %s", command)
		}
		return fmt.Errorf("%s failed: %v: %s", command, err, bounded(stderr.String(), 512))
	}
	var output struct {
		HookSpecificOutput struct {
			HookEventName string `json:"hookEventName"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		return fmt.Errorf("%s returned invalid JSON: %v", command, err)
	}
	if output.HookSpecificOutput.HookEventName != event {
		return fmt.Errorf("%s returned event %q, want %q", command, output.HookSpecificOutput.HookEventName, event)
	}
	return nil
}

func bounded(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
