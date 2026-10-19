package web

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestOwnerWithoutOwnAgentOnALiveInstanceSeesTheOverview(t *testing.T) {
	e := ovEnv(t)
	ovAgent(t, e.St, shellProject, "claude:member", "member-agent", "person:2")
	ovRequest(t, e.St, shellProject, "Live request", 2, 1)
	for name, url := range map[string]string{"owner": e.Base + "/ui/overview"} {
		_, page := fetchPage(t, e.Owner, url)
		if strings.Contains(page, "Connect your first agent") || strings.Contains(page, `http-equiv="refresh"`) {
			t.Errorf("%s is held in Getting started: %s", name, page)
		}
		if !strings.Contains(page, "Live request") || !strings.Contains(page, "member-agent") || !strings.Contains(page, "Connect another agent") {
			t.Errorf("%s lacks requests, the project's agent or the connect action", name)
		}
	}
}

func TestOwnerWithOnlyKnowledgeSeesTheOverview(t *testing.T) {
	e := ovEnv(t)
	ovKnowledge(t, e.St, shellProject, "Old lesson", "trusted")
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/overview")
	if strings.Contains(page, "Connect your first agent") || strings.Contains(page, `http-equiv="refresh"`) {
		t.Errorf("knowledge does not end Getting started: %s", page)
	}
}

func TestGuestOverviewStaysWithoutSetupOnALiveInstance(t *testing.T) {
	e := ovEnv(t)
	ovRequest(t, e.St, shellProject, "Live request", 1, 0)
	_, page := fetchPage(t, e.Guest, e.Base+"/ui/overview")
	if strings.Contains(page, "ctx login") || strings.Contains(page, `http-equiv="refresh"`) {
		t.Errorf("guest sees setup: %s", page)
	}
}

func TestEveryUIPageIsNoStore(t *testing.T) {
	e := ovEnv(t)
	for _, p := range []string{"/ui/overview", "/ui/coord", "/ui/knowledge", "/ui/requests"} {
		resp, err := e.Owner.Get(e.Base + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s Cache-Control = %q", p, got)
		}
	}
}

func TestKnowledgeKeepReadsAtMostTenPages(t *testing.T) {
	e := ovEnv(t)
	for i := 0; i < 1050; i++ {
		ovKnowledge(t, e.St, shellProject, "bulk", "trusted")
	}
	a := &app{store: e.St}
	calls := 0
	out, err := a.knowledgeKeep(store.KnowledgeWindow{}, 5, func(store.Knowledge) bool { calls++; return false })
	if err != nil || len(out) != 0 {
		t.Fatalf("out=%d err=%v", len(out), err)
	}
	if calls > 1000 {
		t.Errorf("read %d rows, cap is 10 pages of 100", calls)
	}
}

func TestProjectLabelsAddTheOwnerOnlyOnCollision(t *testing.T) {
	got := projectLabels([]string{"github.com/x/shell", "github.com/y/shell", "github.com/x/solo"})
	want := map[string]string{"github.com/x/shell": "x/shell", "github.com/y/shell": "y/shell", "github.com/x/solo": "solo"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("label(%s) = %q want %q", k, got[k], v)
		}
	}
}
