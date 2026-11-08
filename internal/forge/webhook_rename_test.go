package forge

import (
	"errors"
	"net/http"
	"testing"
)

const renamedBody = `{"action":"renamed","installation":{"id":5},"changes":{"repository":{"name":{"from":"Before"}}},"repository":{"full_name":"Deadweight-Labs/After","owner":{"login":"Deadweight-Labs"}}}`

// Eine Umbenennung geht als Umbenennung an den Empfänger, und zwar vor dem
// Abgleich der Repo-Liste: sonst legte der Abgleich unter dem neuen Namen ein
// leeres Projekt an, bevor das bestehende umziehen kann.
func TestRenamedEventReachesTheRenameHookBeforeTheListSync(t *testing.T) {
	f := newFakeApp(t)
	var order []string
	h := f.app().WebhookHandlerWithRenames(
		func(i int64, a, r []string) error { order = append(order, "sync"); return nil }, nil,
		func(i int64, from, to string) error {
			if i != 5 || from != "github.com/deadweight-labs/before" || to != "github.com/deadweight-labs/after" {
				t.Errorf("rename %d %q -> %q", i, from, to)
			}
			order = append(order, "rename")
			return nil
		}, nil)
	if rec := deliver(t, h, "repository", "whsec", renamedBody); rec.Code != http.StatusNoContent {
		t.Fatalf("status %d", rec.Code)
	}
	if len(order) != 2 || order[0] != "rename" || order[1] != "sync" {
		t.Fatalf("order %v: rename first, then the list sync", order)
	}
}

func TestOnlyRenamedEventsReachTheRenameHook(t *testing.T) {
	f := newFakeApp(t)
	h := f.app().WebhookHandlerWithRenames(func(int64, []string, []string) error { return nil }, nil,
		func(int64, string, string) error { t.Error("rename hook must not run"); return nil }, nil)
	for event, body := range map[string]string{
		"repository":                `{"action":"created","installation":{"id":5},"repository":{"full_name":"Deadweight-Labs/new","owner":{"login":"Deadweight-Labs"}}}`,
		"installation_repositories": `{"action":"added","installation":{"id":6},"repositories_added":[{"full_name":"A/one"}]}`,
		"push":                      renamedBody,
	} {
		if rec := deliver(t, h, event, "whsec", body); rec.Code != http.StatusNoContent {
			t.Fatalf("%s: %d", event, rec.Code)
		}
	}
	// Ohne Name des alten Repos gibt es nichts umzubenennen.
	noFrom := `{"action":"renamed","installation":{"id":5},"repository":{"full_name":"Deadweight-Labs/After","owner":{"login":"Deadweight-Labs"}}}`
	if rec := deliver(t, h, "repository", "whsec", noFrom); rec.Code != http.StatusNoContent {
		t.Fatalf("no old name: %d", rec.Code)
	}
}

func TestRenameHookFailureIsAnErrorWithoutLeakingIt(t *testing.T) {
	f := newFakeApp(t)
	var logged error
	h := f.app().WebhookHandlerWithRenames(func(int64, []string, []string) error { t.Error("sync must not run after a failed rename"); return nil }, nil,
		func(int64, string, string) error { return errors.New("db down") }, func(err error) { logged = err })
	rec := deliver(t, h, "repository", "whsec", renamedBody)
	if rec.Code != http.StatusInternalServerError || logged == nil || rec.Body.Len() > 40 {
		t.Fatalf("status %d logged %v body %q", rec.Code, logged, rec.Body.String())
	}
}

func TestWebhookWithoutRenameHookKeepsAddAndRemove(t *testing.T) {
	f := newFakeApp(t)
	var calls []syncCall
	h := f.app().WebhookHandlerWithRenames(func(i int64, a, r []string) error { calls = append(calls, syncCall{i, a, r}); return nil }, nil, nil, nil)
	if rec := deliver(t, h, "repository", "whsec", renamedBody); rec.Code != http.StatusNoContent || len(calls) != 1 ||
		len(calls[0].add) != 1 || len(calls[0].remove) != 1 {
		t.Fatalf("calls %+v", calls)
	}
}
