package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func metaTranscript(word, title string) []store.Chunk {
	user, _ := json.Marshal(map[string]any{"type": "user", "timestamp": "2026-10-01T10:00:00Z", "message": map[string]any{"role": "user", "content": "please look at " + word}})
	name, _ := json.Marshal(map[string]any{"type": "ai-title", "aiTitle": title})
	return []store.Chunk{
		{Seq: 0, Role: "user", Text: "please look at " + word, Raw: string(user)},
		{Seq: 1, Role: "other", Text: "", Raw: string(name)},
	}
}

func addMetaSession(t *testing.T, f *accessAPIFixture, key string, account int64, owner, machine, branch, cwd, title, visibility string) int64 {
	t.Helper()
	id, err := f.st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: key, AccountID: account, CWD: cwd,
		Scope: scope.Axes{Project: accProject, Machine: machine, Branch: branch}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.AppendChunks(id, metaTranscript("nebelkraehe", title)); err != nil {
		t.Fatal(err)
	}
	if visibility != store.VisPrivate {
		if err := f.st.SetSessionVisibility(id, f.st.Access(store.Principal{ID: fmt.Sprintf("person:%d", account), Label: owner}), visibility); err != nil {
			t.Fatal(err)
		}
	}
	f.id[key] = id
	return id
}

func TestSessionMetadataAPIFollowsTheViewerPerRole(t *testing.T) {
	f := accessAPI(t, true)
	addMetaSession(t, f, "m-mia", 3, "mia", "mia-box", "main", "/home/mia/p", "Mia private title", store.VisPrivate)
	addMetaSession(t, f, "g-rex", 4, "rex", "rex-box", "feat/x", "/home/rex/p", "Guest shared title", store.VisGuests)
	addMetaSession(t, f, "p-rex", 4, "rex", "rex-box", "feat/y", "/home/rex/q", "Rex private title", store.VisPrivate)

	list := "/api/sessions?project=" + accProject
	search := "/api/search?q=nebelkraehe&kind=sessions"
	for _, path := range []string{list, search} {
		// Guest: only the guest-shared session, without host details or owner.
		out := f.expect(t, "gus", 200, "GET", path, nil)
		if !strings.Contains(out, "Guest shared title") {
			t.Errorf("guest %s lacks the shared session: %s", path, out)
		}
		for _, leak := range []string{"rex-box", "mia-box", "feat/x", "/home/rex", "g-rex", "\"owner\"", "Mia private title", "Rex private title"} {
			if strings.Contains(out, leak) {
				t.Errorf("guest %s leaks %q: %s", path, leak, out)
			}
		}
		// Member: a foreign private session is metadata only.
		out = f.expect(t, "mia", 200, "GET", path, nil)
		if strings.Contains(out, "Rex private title") {
			t.Errorf("member %s sees the title of a foreign private session: %s", path, out)
		}
		if !strings.Contains(out, "Mia private title") || !strings.Contains(out, "Guest shared title") {
			t.Errorf("member %s lacks what she may read: %s", path, out)
		}
		// Owner reads everything.
		out = f.expect(t, "robin", 200, "GET", path, nil)
		if !strings.Contains(out, "Rex private title") || !strings.Contains(out, "rex-box") {
			t.Errorf("owner %s: %s", path, out)
		}
	}
	// The member still sees that the private session exists (metadata).
	var ss []store.Session
	if err := json.Unmarshal([]byte(f.expect(t, "mia", 200, "GET", list, nil)), &ss); err != nil {
		t.Fatal(err)
	}
	foreign := 0
	for _, s := range ss {
		if s.ID == f.id["p-rex"] {
			foreign++
			if s.Title != "" || s.Messages != 0 || s.PublicID != "" {
				t.Errorf("foreign private row carries content: %+v", s)
			}
		}
	}
	if foreign != 1 {
		t.Errorf("member lost the metadata row of a foreign session: %d", foreign)
	}
}

func TestSessionMetadataFiltersDoNotProbeHiddenHostDetails(t *testing.T) {
	f := accessAPI(t, true)
	addMetaSession(t, f, "g-rex", 4, "rex", "rex-box", "feat/x", "/home/rex/p", "Guest shared title", store.VisGuests)
	for _, path := range []string{
		"/api/sessions?project=" + accProject + "&machine=rex-box",
		"/api/sessions?project=" + accProject + "&branch=feat/x",
		"/api/search?q=nebelkraehe&kind=sessions&machine=rex-box",
		"/api/search?q=nebelkraehe&kind=sessions&branch=feat/x",
	} {
		if out := f.expect(t, "gus", 200, "GET", path, nil); strings.Contains(out, "Guest shared title") {
			t.Errorf("guest probes a hidden host detail through %s: %s", path, out)
		}
		if out := f.expect(t, "mia", 200, "GET", path, nil); !strings.Contains(out, "Guest shared title") {
			t.Errorf("member lost the filter %s: %s", path, out)
		}
	}
}

func TestLegacyShareRouteFollowsTheShareAction(t *testing.T) {
	f := accessAPI(t, true)
	mia := f.id["s-mia"]
	share := map[string]bool{"shared": true}
	f.expect(t, "lena", 403, "PUT", idPath("/api/sessions/%d/share", mia), share)
	// The project owner may share any session, like in the browser.
	f.expect(t, "robin", 200, "PUT", idPath("/api/sessions/%d/share", mia), share)
	f.expect(t, "rex", 200, "GET", idPath("/api/sessions/%d", mia), nil)
	f.expect(t, "robin", 200, "PUT", idPath("/api/sessions/%d/share", mia), map[string]bool{"shared": false})
	f.expect(t, "rex", 404, "GET", idPath("/api/sessions/%d", mia), nil)
}

func TestSessionUploadsHaveABodyLimit(t *testing.T) {
	f := accessAPI(t, true)
	old := maxChunkBody
	maxChunkBody = 4 << 10
	defer func() { maxChunkBody = old }()
	id := f.id["s-mia"]
	small := map[string]any{"chunks": []store.Chunk{{Seq: 10, Role: "user", Text: "ok", Raw: "{}"}}}
	f.expect(t, "mia", 204, "POST", idPath("/api/sessions/%d/chunks", id), small)
	big := map[string]any{"chunks": []store.Chunk{{Seq: 11, Role: "user", Text: "x", Raw: strings.Repeat("x", 8<<10)}}}
	f.expect(t, "mia", 413, "POST", idPath("/api/sessions/%d/chunks", id), big)
	f.expect(t, "mia", 413, "POST", "/api/sessions", map[string]any{"harness": "claude-code", "external_id": "e", "cwd": strings.Repeat("c", 80<<10)})
}
