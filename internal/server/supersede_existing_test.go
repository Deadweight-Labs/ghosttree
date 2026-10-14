package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func supersedeExistingBody(reason string, olds ...int64) map[string]any {
	return map[string]any{"supersedes": olds, "reason": reason, "via": "cli"}
}

func TestExistingEntrySupersedesOlderOnesAndTheDetailShowsWhy(t *testing.T) {
	f := accessAPI(t, true)
	old := f.id["k-mia"]
	var fresh store.Knowledge
	if err := json.Unmarshal([]byte(f.expect(t, "mia", 200, "POST", "/api/knowledge", newEntry(nil))), &fresh); err != nil {
		t.Fatal(err)
	}
	path := idPath("/api/knowledge/%d/supersede", fresh.ID)

	if code, out := f.call(t, "mia", "POST", path, supersedeExistingBody("", old)); code != 400 || !strings.Contains(out, "reason_required") {
		t.Errorf("without reason = %d %s", code, out)
	}
	if code, out := f.call(t, "mia", "POST", path, supersedeExistingBody("x")); code != 400 {
		t.Errorf("without olds = %d %s", code, out)
	}
	if code, out := f.call(t, "mia", "POST", path, supersedeExistingBody("x", fresh.ID)); code == 200 {
		t.Errorf("superseding itself = %d %s", code, out)
	}
	if code, out := f.call(t, "mia", "POST", path, supersedeExistingBody("x", f.id["k-robin-staged"])); code != 403 {
		t.Errorf("foreign old entry = %d %s", code, out)
	}
	if code, out := f.call(t, "gus", "POST", path, supersedeExistingBody("x", old)); code != 403 {
		t.Errorf("guest = %d %s", code, out)
	}
	if code, out := f.call(t, "robin", "POST", path, supersedeExistingBody("x", f.id["k-other"])); code != 400 || !strings.Contains(out, "scope_mismatch") {
		t.Errorf("other scope = %d %s", code, out)
	}
	if k, _ := f.st.KnowledgeByID(old); k.Status != "active" {
		t.Fatalf("a refused call changed the old entry: %+v", k)
	}
	// The replacement must be an entry the caller may edit.
	if code, out := f.call(t, "mia", "POST", idPath("/api/knowledge/%d/supersede", f.id["k-robin-staged"]), supersedeExistingBody("x", old)); code != 403 {
		t.Errorf("foreign replacement = %d %s", code, out)
	}

	f.expect(t, "mia", 200, "POST", path, supersedeExistingBody("the share was removed again", old))
	k, err := f.st.KnowledgeByID(old)
	if err != nil || k.Status != "superseded" || k.SupersededBy != fresh.ID {
		t.Fatalf("old entry = %+v, %v", k, err)
	}
	if code, out := f.call(t, "mia", "POST", path, supersedeExistingBody("again", old)); code != 409 {
		t.Errorf("second supersede = %d %s", code, out)
	}

	var detail store.Knowledge
	if err := json.Unmarshal([]byte(f.expect(t, "mia", 200, "GET", idPath("/api/knowledge/%d", old), nil)), &detail); err != nil {
		t.Fatal(err)
	}
	if c := detail.Closure; c == nil || c.Kind != "superseded" || c.Reason != "the share was removed again" || c.SupersededBy != fresh.ID {
		t.Errorf("detail closure = %+v", c)
	}
}

func TestRetiredEntryDetailShowsTheReason(t *testing.T) {
	f := accessAPI(t, true)
	id := f.id["k-mia"]
	f.expect(t, "mia", 200, "POST", idPath("/api/knowledge/%d/retire", id), map[string]any{"reason": "the tunnel was closed"})
	var detail store.Knowledge
	if err := json.Unmarshal([]byte(f.expect(t, "mia", 200, "GET", idPath("/api/knowledge/%d", id), nil)), &detail); err != nil {
		t.Fatal(err)
	}
	if c := detail.Closure; c == nil || c.Kind != "retired" || c.Reason != "the tunnel was closed" {
		t.Errorf("detail closure = %+v", c)
	}
	var live store.Knowledge
	if err := json.Unmarshal([]byte(f.expect(t, "robin", 200, "GET", idPath("/api/knowledge/%d", f.id["k-global"]), nil)), &live); err != nil || live.Closure != nil {
		t.Errorf("an entry in use has no closure: %+v (%v)", live.Closure, err)
	}
}
