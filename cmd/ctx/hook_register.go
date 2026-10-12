package main

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const registerHookDeadline = 1500 * time.Millisecond

func registerStateFile(self string) (string, error) {
	path, err := coordStateFile(self)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(path, ".json") + ".register.json", nil
}

// registerHookSession announces the session in its project room, so that it
// shows up for everyone without having called a coord tool. It registers the
// same way ctx mcp does (same id, same fields), which makes the two calls the
// same idempotent write, and it refreshes last_seen at most once per
// coordHookInterval. force skips that interval (session start). Everything
// fails quietly: the hook must never cost the session anything.
func registerHookSession(raw []byte, harness string, force bool) {
	if harness != "claude" && harness != "codex" {
		return
	}
	var in struct {
		SessionID string `json:"session_id"`
		CWD       string `json:"cwd"`
		AgentID   string `json:"agent_id"`
	}
	if json.Unmarshal(raw, &in) != nil || in.AgentID != "" {
		// A subagent gets no card of its own; its calls carry an agent id.
		return
	}
	self := hookCoordSelf(in.SessionID)
	if !store.ValidExternalID(self) {
		return
	}
	path, err := registerStateFile(self)
	if err != nil {
		return
	}
	state := loadCoordState(path)
	now := time.Now()
	if !force && state.LastRegister != 0 && now.Sub(time.Unix(state.LastRegister, 0)) < coordHookInterval {
		return
	}
	// The attempt counts even when it fails or finds nothing to join (no
	// config, no repository), so that a dead server costs one short wait a
	// minute and a repo-less session does not run git on every tool call.
	state.LastRegister = now.Unix()
	saveCoordState(path, state)
	cfg, err := config.Load()
	if err != nil {
		return
	}
	cwd := in.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	git := collector.ResolveGitContext(cwd)
	room, branch := state.RegisterRoom, state.RegisterBranch
	switch {
	case room == "":
		// First join: the room of the first repository a hook saw the session in.
		if git.Project == "" {
			return
		}
		room, branch = store.RoomKeyForProject(git.Project), git.Branch
	case git.Project != "" && store.RoomKeyForProject(git.Project) == room:
		branch = git.Branch
	}
	// Once joined, the room stays: a session that moved into another repository
	// must not be announced there, because the server binds it to one project
	// room and would refuse its coord calls afterwards.
	session := agentSessionFromEnv()
	if session == "" && store.ValidExternalID(in.SessionID) {
		session = in.SessionID
	}
	if _, err := client.NewWithTimeout(cfg, registerHookDeadline).RegisterCoordAgent(store.CoordAgent{
		ExternalID: self, Provider: harness, RoomKey: room,
		DisplayName: self, Branch: branch, Role: agentRoleFromEnv(), SessionID: session,
	}); err == nil {
		state.RegisterRoom, state.RegisterBranch = room, branch
		saveCoordState(path, state)
	}
}
