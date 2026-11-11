package web

import (
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestEveryUIPageIsNoStore(t *testing.T) {
	e := ovEnv(t)
	for _, p := range []string{"/ui/inbox", "/ui/overview?connect=1", "/ui/coord", "/ui/knowledge"} {
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
