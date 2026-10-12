package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/installer"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// collabEnv is the acceptance run of 2026-10-11 in one process: a host and a
// friend, two accounts on one server with the access rules on, the friend's
// Claude session registered in the project room, and the hooks exactly as the
// installer registers them for Claude Code.
type collabEnv struct {
	host, friend *client.Client
	friendSelf   string
	room         string
	repo         string
	hooks        map[string][]string // event -> argv after "ctx" as registered
}

const collabSession = "sess-friend-claude"

func newCollabEnv(t *testing.T) *collabEnv {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is needed to resolve the project of the working directory")
	}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	hostToken, _ := st.AddPerson("host")
	friendToken, _ := st.AddPerson("friend")
	st.SetAccessMode(store.AccessMode{Enforce: true})
	srv := httptest.NewServer(server.New(st))
	t.Cleanup(srv.Close)

	const project = "github.com/dw/collab"
	org, err := st.CreateOrg("person:1", "Collab", "collab")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureProject("person:1", project); err != nil {
		t.Fatal(err)
	}
	code, _, err := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProjectRole("person:1", project, "person:2", store.RoleMember, false, store.RoleViaAPI); err != nil {
		t.Fatal(err)
	}

	// The friend's machine: config, state and a checkout of the project.
	home := t.TempDir()
	cfgDir := filepath.Join(home, "ghosttree")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{ServerURL: srv.URL, Token: friendToken, Machine: "friendbox"}
	raw, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv(agentIDEnv, "")
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "https://" + project + ".git"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}

	e := &collabEnv{
		host:   client.New(config.Config{ServerURL: srv.URL, Token: hostToken, Machine: "hostbox"}),
		friend: client.New(cfg), friendSelf: collabSession,
		room: store.RoomKeyForProject(project), repo: repo, hooks: map[string][]string{},
	}
	// The host's agent writes, the friend's session is what ctx mcp registers
	// under the harness session id.
	if _, err := e.host.RegisterCoordAgent(store.CoordAgent{ExternalID: "sess-host", Provider: "claude-code", RoomKey: e.room, DisplayName: "sess-host"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.friend.RegisterCoordAgent(store.CoordAgent{ExternalID: collabSession, Provider: "claude-code", RoomKey: e.room, DisplayName: collabSession}); err != nil {
		t.Fatal(err)
	}
	e.hooks = installedClaudeHooks(t)
	return e
}

// installedClaudeHooks installs the Claude hooks into a scratch home and returns
// the arguments of the registered UserPromptSubmit and PostToolUse commands, so
// the test runs what Claude Code would run and not a name it made up.
func installedClaudeHooks(t *testing.T) map[string][]string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if _, err := installer.InstallClaude(home); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, event := range []string{"UserPromptSubmit", "PostToolUse"} {
		for _, entry := range settings.Hooks[event] {
			for _, h := range entry.Hooks {
				if _, rest, ok := strings.Cut(h.Command, " hook "); ok {
					out[event] = strings.Fields(rest)
				}
			}
		}
		if len(out[event]) == 0 {
			t.Fatalf("the installer registers no %s hook: %s", event, raw)
		}
	}
	return out
}

// run is one hook call as Claude Code makes it: JSON on stdin, JSON on stdout.
func (e *collabEnv) run(t *testing.T, event string, payload map[string]any) string {
	t.Helper()
	raw, _ := json.Marshal(payload)
	var out bytes.Buffer
	if code := cmdHookWith(bytes.NewReader(raw), e.hooks[event], &out); code != 0 {
		t.Fatalf("%s exit %d: %s", event, code, out.String())
	}
	var got struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("%s output is not JSON: %v: %s", event, err, out.String())
	}
	if got.HookSpecificOutput.HookEventName != event {
		t.Fatalf("%s answered as %q", event, got.HookSpecificOutput.HookEventName)
	}
	return got.HookSpecificOutput.AdditionalContext
}

func (e *collabEnv) prompt(session string) map[string]any {
	return map[string]any{"prompt": "carry on", "cwd": e.repo, "session_id": session}
}

func (e *collabEnv) tool(session string) map[string]any {
	return map[string]any{"tool_name": "Bash", "cwd": e.repo, "session_id": session}
}

var collabMsg int

func (e *collabEnv) say(t *testing.T, body string) {
	t.Helper()
	collabMsg++
	if _, err := e.host.SendCoordMessage(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: e.room,
		SenderExternalID: "sess-host", ClientID: fmt.Sprintf("collab-%d-%d", collabMsg, time.Now().UnixNano()), Body: body}); err != nil {
		t.Fatal(err)
	}
}

func (e *collabEnv) expire(t *testing.T) {
	t.Helper()
	path, err := coordStateFile(e.friendSelf)
	if err != nil {
		t.Fatal(err)
	}
	st := loadCoordState(path)
	st.LastPoll = time.Now().Add(-time.Hour).Unix()
	saveCoordState(path, st)
}

// A mention of the friend's running Claude session by another account arrives in
// the running agent through both hooks, with the sender and authority the server
// computed (acceptance finding 3, REQ-438).
func TestMentionByAnotherAccountReachesTheRunningClaudeThroughBothHooks(t *testing.T) {
	e := newCollabEnv(t)

	// UserPromptSubmit: the next prompt carries it.
	e.say(t, "@claude-friend please run the migration check")
	got := e.run(t, "UserPromptSubmit", e.prompt(e.friendSelf))
	for _, want := range []string{"new messages for you", "please run the migration check", "sess-host", "authority=", "project room github.com/dw/collab"} {
		if !strings.Contains(got, want) {
			t.Errorf("UserPromptSubmit context lacks %q:\n%s", want, got)
		}
	}
	if again := e.run(t, "UserPromptSubmit", e.prompt(e.friendSelf)); strings.Contains(again, "migration check") {
		t.Errorf("delivered twice:\n%s", again)
	}

	// PostToolUse: an agent that works without a new prompt gets it on a tool call.
	if first := e.run(t, "PostToolUse", e.tool(e.friendSelf)); first != "" {
		t.Fatalf("nothing to deliver yet: %q", first)
	}
	e.say(t, "@claude-friend and then the lint run")
	e.expire(t)
	got = e.run(t, "PostToolUse", e.tool(e.friendSelf))
	if !strings.Contains(got, "and then the lint run") || !strings.Contains(got, "sess-host") {
		t.Errorf("PostToolUse context:\n%s", got)
	}

	// coord_inbox does not repeat what the hooks handed over, and marks the
	// message as addressed to the agent.
	inbox, err := e.friend.CoordInbox(store.DestinationRoom, e.room, e.friendSelf, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range inbox {
		if strings.Contains(m.Body, "migration check") && m.ToYou != store.ToYouMention {
			t.Errorf("the inbox does not mark %q as addressed to the agent: %+v", m.Body, m)
		}
	}
}

// The case of the report: a session the hook does not know stays silent, and so
// does a message that names the person instead of the agent. Both are intended.
// The room delivers to the participant that was meant, and a hook call speaks
// for the session id it is given; an MCP process that could not learn the
// harness session id registers as "derived:<host>:<pid>", which no hook
// payload ever carries.
func TestHookStaysSilentForAnUnregisteredSessionAndForAPersonMention(t *testing.T) {
	e := newCollabEnv(t)
	e.say(t, "@claude-friend for the registered session")
	if got := e.run(t, "UserPromptSubmit", e.prompt("some-other-session")); got != "" {
		t.Errorf("a session that registered nowhere got the message:\n%s", got)
	}
	e.say(t, "@friend this one names the person")
	got := e.run(t, "UserPromptSubmit", e.prompt(e.friendSelf))
	if !strings.Contains(got, "for the registered session") {
		t.Errorf("the agent mention is missing:\n%s", got)
	}
	if strings.Contains(got, "names the person") {
		t.Errorf("a mention of the person was handed to the agent:\n%s", got)
	}
}
