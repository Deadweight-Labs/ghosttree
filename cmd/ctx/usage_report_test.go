package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/deliverylog"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// REQ-636: ctx usage tools answers the question of the DEC-8 gate, per tool in
// how many sessions and how often; the denominator contains the sessions that
// called no tool at all.
func TestUsageToolsReportsSessionsAndShareSince(t *testing.T) {
	f := newLogFixture(t)
	const project = "github.com/test/repo"
	for _, ref := range []string{"u1", "u2", "u3", "u4"} {
		if _, err := f.st.UpsertSession(store.Session{Harness: "claude", ExternalID: ref, Scope: scope.Axes{Project: project}}); err != nil {
			t.Fatal(err)
		}
	}
	use := func(ref, tool, action string, n int) {
		t.Helper()
		if err := f.st.RecordToolUses(store.ToolUseBatch{Project: project, SessionRef: ref, Harness: "claude",
			Uses: []store.ToolUse{{Tool: tool, Action: action, Calls: n}}}, 1); err != nil {
			t.Fatal(err)
		}
	}
	use("u1", "request", "get", 3)
	use("u2", "request", "search", 2)
	use("u3", "request", "get", 1)
	use("u1", "runbook", "report", 1)

	var out bytes.Buffer
	if code := cmdUsage([]string{"tools", "--since", "2020-01-01", "--project", project}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	text := out.String()
	if !strings.Contains(text, "sessions in the window: 4, with at least one counted call: 3") {
		t.Errorf("denominators:\n%s", text)
	}
	var reqLine, runLine string
	for _, l := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(strings.TrimSpace(l), "request"):
			reqLine = l
		case strings.HasPrefix(strings.TrimSpace(l), "runbook"):
			runLine = l
		}
	}
	if f := strings.Fields(reqLine); len(f) < 5 || f[1] != "3" || f[2] != "75.0" || f[4] != "6" {
		t.Errorf("request line %q", reqLine)
	}
	if f := strings.Fields(runLine); len(f) < 5 || f[1] != "1" || f[2] != "25.0" || f[4] != "1" {
		t.Errorf("runbook line %q", runLine)
	}
	out.Reset()
	if code := cmdUsage([]string{"tools", "--since", "2020-01-01", "--project", project, "--actions"}, &out); code != 0 || !strings.Contains(out.String(), "search") {
		t.Errorf("--actions: %d\n%s", code, out.String())
	}
	out.Reset()
	if code := cmdUsage([]string{"tools", "--since", "2020-01-01", "--project", project, "--json"}, &out); code != 0 {
		t.Fatal(code)
	}
	var rep store.ToolReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil || rep.Sessions != 4 || len(rep.Tools) != 2 {
		t.Errorf("json %s %v", out.String(), err)
	}
	// A window that is not a day is a usage error, not "everything".
	out.Reset()
	if code := cmdUsage([]string{"tools", "--since", "last week"}, &out); code != 2 {
		t.Errorf("bad --since exits %d", code)
	}
	// The older flags of ctx usage still work.
	out.Reset()
	if code := cmdUsage([]string{}, &out); code != 2 {
		t.Errorf("ctx usage without arguments: %d", code)
	}
}

func TestUsageSignalsAndDeliveriesPrintTheirSums(t *testing.T) {
	f := newLogFixture(t)
	const project = "github.com/test/repo"
	hourAgo := time.Now().UTC().Add(-3 * time.Hour).Format(time.RFC3339)
	for i, prompts := range []int{1, 2, 9} {
		ref := []string{"v1", "v2", "v3"}[i]
		id, err := f.st.UpsertSession(store.Session{Harness: "claude", ExternalID: ref, Scope: scope.Axes{Project: project}})
		if err != nil {
			t.Fatal(err)
		}
		for j := range prompts {
			f.st.DB().Exec(`INSERT INTO chunk_index(chunk_id, session_id, seq, role, is_prompt) VALUES(?,?,?,?,1)`, id*100+int64(j), id, j, "user")
		}
		f.st.DB().Exec(`UPDATE sessions SET last_seen_at=? WHERE id=?`, hourAgo, id)
		entry := "cli"
		if prompts < 3 {
			entry = "sdk-cli"
		}
		if err := f.st.RecordContextLog(store.ContextLogBatch{Project: project, SessionRef: ref, Harness: "claude",
			Signals: []store.SessionSignal{{Name: "CLAUDE_CODE_ENTRYPOINT", Value: entry}},
			Events: []store.DeliveryEvent{{Call: "c" + ref, Parts: []deliverylog.Part{
				{Via: deliverylog.ViaBootstrap, Kind: deliverylog.KindKnowledge, ID: 1, Chars: 100},
				{Via: deliverylog.ViaBootstrap, Kind: deliverylog.KindFrame, Chars: 20}}}}}, 1); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if code := cmdUsage([]string{"signals", "--since", "2020-01-01", "--project", project}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	var sdk, cli string
	for _, l := range strings.Split(out.String(), "\n") {
		if strings.Contains(l, "sdk-cli") {
			sdk = l
		} else if strings.Contains(l, "ENTRYPOINT") && strings.Contains(l, " cli ") {
			cli = l
		}
	}
	if f := strings.Fields(sdk); len(f) < 7 || f[1] != "sdk-cli" || f[2] != "2" || f[3] != "2" || f[4] != "2" || f[5] != "100.0" {
		t.Errorf("sdk-cli line %q\n%s", sdk, out.String())
	}
	if f := strings.Fields(cli); len(f) < 7 || f[2] != "1" || f[4] != "0" || f[5] != "0.0" {
		t.Errorf("cli line %q", cli)
	}
	out.Reset()
	if code := cmdUsage([]string{"deliveries", "--since", "2020-01-01", "--project", project}, &out); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "3 sessions, 360 characters") || !strings.Contains(out.String(), "bootstrap") {
		t.Errorf("deliveries:\n%s", out.String())
	}
}
