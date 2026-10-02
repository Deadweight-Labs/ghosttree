package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// ---------------------------------------------------------------- Helfer

func jl(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func userLine(ts, text string) string {
	return jl(map[string]any{"type": "user", "timestamp": ts, "message": map[string]any{"role": "user", "content": text}})
}

func assistantLine(ts string, blocks ...map[string]any) string {
	return jl(map[string]any{"type": "assistant", "timestamp": ts, "message": map[string]any{"role": "assistant", "content": blocks}})
}

func textBlock(s string) map[string]any { return map[string]any{"type": "text", "text": s} }
func thinkBlock(s string) map[string]any {
	return map[string]any{"type": "thinking", "thinking": s}
}
func bashBlock(id, cmd string) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": cmd}}
}
func resultLine(ts, id, out string) string {
	return jl(map[string]any{"type": "user", "timestamp": ts, "message": map[string]any{"role": "user",
		"content": []map[string]any{{"type": "tool_result", "tool_use_id": id, "content": out}}}})
}

func chunksOf(lines ...string) []Chunk {
	out := make([]Chunk, len(lines))
	for i, l := range lines {
		out[i] = Chunk{Seq: i, Raw: l}
	}
	return out
}

// transcriptWith builds a session that mentions word in one message, one
// thinking block, one command and one tool output.
func transcriptWith(word string) []Chunk {
	return chunksOf(
		userLine("2026-10-01T10:00:00Z", "please look into "+word+" for me"),
		assistantLine("2026-10-01T10:00:01Z", thinkBlock("reasoning about "+word+" privately")),
		assistantLine("2026-10-01T10:00:02Z", textBlock("looking"), bashBlock("t1", "grep -rn "+word+" .")),
		resultLine("2026-10-01T10:00:03Z", "t1", "./file.go:3: "+word+" appears here"),
		assistantLine("2026-10-01T10:00:04Z", textBlock("done")),
	)
}

func addSession(t *testing.T, st *Store, ext string, account int64, project, machine, visibility string, chunks []Chunk) Session {
	t.Helper()
	id, err := st.UpsertSession(Session{Harness: "claude-code", ExternalID: ext, AccountID: account,
		Scope: scope.Axes{Project: project, Machine: machine, Branch: "main"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) > 0 {
		if err := st.AppendChunks(id, chunks); err != nil {
			t.Fatal(err)
		}
	}
	if visibility != "" && visibility != VisPrivate {
		if _, err := st.db.Exec(`UPDATE sessions SET visibility=?, shared=1 WHERE id=?`, visibility, id); err != nil {
			t.Fatal(err)
		}
	}
	sess, err := st.SessionByID(id)
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

func viewer(st *Store, id, label string) *ProjectAccess {
	return st.Access(Principal{ID: id, Label: label})
}

func searchIDs(t *testing.T, st *Store, pa *ProjectAccess, q string) (SearchPage, map[string]bool) {
	t.Helper()
	page, err := st.SearchTranscripts(pa, SearchQuery{Q: q, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, g := range page.Groups {
		raw, err := st.SessionByID(g.Session.ID)
		if err != nil {
			t.Fatal(err)
		}
		got[raw.ExternalID] = true
	}
	return page, got
}

func sameKeys(got map[string]bool, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for _, w := range want {
		if !got[w] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------- Index und Art

func TestIndexCoversMessagesThinkingCommandsAndOutput(t *testing.T) {
	st := orgStore(t, "robin")
	addSession(t, st, "s1", 1, "", "m1", "", append(transcriptWith("zebra"), Chunk{Seq: 5, Raw: userLine("2026-10-01T10:01:00Z", "unrelated")}))
	pa := viewer(st, "person:1", "robin")
	cases := []struct {
		kind, term string
		want       bool
	}{
		{"", "zebra", true},
		{"messages", "zebra", true},
		{"output", "appears", true},
		{"commands", "grep", true},
		{"thinking", "privately", true},
		// Denkblöcke gehören nicht zu "Everything".
		{"", "privately", false},
		{"messages", "privately", false},
		{"messages", "appears", false},
		{"output", "privately", false},
		{"commands", "appears", false},
		{"thinking", "appears", false},
	}
	for _, c := range cases {
		page, err := st.SearchTranscripts(pa, SearchQuery{Q: c.term, Kind: c.kind, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if got := page.Matches > 0; got != c.want {
			t.Errorf("kind=%q term=%q: matches=%d, want found=%v", c.kind, c.term, page.Matches, c.want)
		}
	}
}

func TestSearchHitsNameTheKindAndMarkTheMatch(t *testing.T) {
	st := orgStore(t, "robin")
	addSession(t, st, "s1", 1, "", "m1", "", transcriptWith("zebra"))
	pa := viewer(st, "person:1", "robin")
	page, err := st.SearchTranscripts(pa, SearchQuery{Q: "zebra", Limit: 10})
	if err != nil || len(page.Groups) != 1 {
		t.Fatalf("groups=%d err=%v", len(page.Groups), err)
	}
	kinds := map[string]SearchHit{}
	for _, h := range page.Groups[0].Hits {
		kinds[h.Kind] = h
	}
	if len(kinds) != 3 || kinds["user"].Seq != 0 || kinds["command"].Seq != 2 || kinds["output"].Seq != 3 {
		t.Fatalf("kinds = %+v", kinds)
	}
	hit := kinds["output"]
	var marked string
	for _, p := range hit.Parts {
		if p.Hit {
			marked += p.Text
		}
	}
	if !strings.EqualFold(marked, "zebra") {
		t.Errorf("marked = %q in %+v", marked, hit.Parts)
	}
	if page.Matches != 3 || page.Sessions != 1 || page.Groups[0].Total != 3 {
		t.Errorf("matches=%d sessions=%d total=%d", page.Matches, page.Sessions, page.Groups[0].Total)
	}
}

func TestGroupShowsThreeHitsAndCountsTheRest(t *testing.T) {
	st := orgStore(t, "robin")
	var lines []string
	for i := 0; i < 8; i++ {
		lines = append(lines, userLine("2026-10-01T10:00:00Z", fmt.Sprintf("kafka question %d", i)))
	}
	addSession(t, st, "s1", 1, "", "m1", "", chunksOf(lines...))
	page, _ := searchIDs(t, st, viewer(st, "person:1", "robin"), "kafka")
	g := page.Groups[0]
	if len(g.Hits) != 3 || g.Total != 8 || page.Matches != 8 {
		t.Fatalf("hits=%d total=%d matches=%d", len(g.Hits), g.Total, page.Matches)
	}
}

func TestTitleIsAITitleElseFirstUserMessage(t *testing.T) {
	st := orgStore(t, "robin")
	a := addSession(t, st, "a", 1, "", "m", "", chunksOf(
		userLine("2026-10-01T10:00:00Z", "first words of the very first prompt"),
		jl(map[string]any{"type": "ai-title", "aiTitle": "A proper title"})))
	b := addSession(t, st, "b", 1, "", "m", "", chunksOf(
		userLine("2026-10-01T10:00:00Z", "  fix the\n  login   page please  ")))
	if a.Title != "A proper title" {
		t.Errorf("ai title = %q", a.Title)
	}
	if b.Title != "fix the login page please" {
		t.Errorf("fallback title = %q", b.Title)
	}
	long := addSession(t, st, "c", 1, "", "m", "", chunksOf(userLine("2026-10-01T10:00:00Z", strings.Repeat("word ", 60))))
	if n := len([]rune(long.Title)); n > 81 {
		t.Errorf("title not cut: %d runes", n)
	}
}

func TestMessageCountComesFromTheIndex(t *testing.T) {
	st := orgStore(t, "robin")
	s := addSession(t, st, "s", 1, "", "m", "", transcriptWith("zebra"))
	// 1 prompt + 2 assistant texts; thinking, tool calls and results do not count.
	if s.Messages != 3 {
		t.Errorf("messages = %d, want 3", s.Messages)
	}
}

// ------------------------------------------------- Sichtbarkeit (#2447)

type visFixture struct {
	st *Store
	// ext -> owner
	a, b, c, d, e Session
}

// robin(1) Owner, lena(2) Lead, mia(3) Member, rex(4) Member, gus(5) Guest,
// nora(6) ohne Rolle. Jede Session enthält das Wort "zebra".
func newVisFixture(t *testing.T) visFixture {
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	f := visFixture{st: st}
	f.a = addSession(t, st, "a-robin-private", 1, roleProject, "mainex", "", transcriptWith("zebra"))
	f.b = addSession(t, st, "b-mia-private", 3, roleProject, "laptop", "", transcriptWith("zebra"))
	f.c = addSession(t, st, "c-rex-project", 4, roleProject, "box", VisProject, transcriptWith("zebra"))
	f.d = addSession(t, st, "d-rex-guests", 4, roleProject, "box", VisGuests, transcriptWith("zebra"))
	f.e = addSession(t, st, "e-rex-private", 4, roleProject, "box", "", transcriptWith("zebra"))
	return f
}

func TestSearchOnlySeesTheReadableSet(t *testing.T) {
	f := newVisFixture(t)
	for _, c := range []struct {
		who, label string
		want       []string
	}{
		{"person:1", "robin", []string{"a-robin-private", "b-mia-private", "c-rex-project", "d-rex-guests", "e-rex-private"}},
		{"person:2", "lena", []string{"a-robin-private", "b-mia-private", "c-rex-project", "d-rex-guests", "e-rex-private"}},
		{"person:3", "mia", []string{"b-mia-private", "c-rex-project", "d-rex-guests"}},
		{"person:4", "rex", []string{"c-rex-project", "d-rex-guests", "e-rex-private"}},
		{"person:5", "gus", []string{"d-rex-guests"}},
		{"person:6", "nora", nil},
	} {
		page, got := searchIDs(t, f.st, viewer(f.st, c.who, c.label), "zebra")
		if !sameKeys(got, c.want...) {
			t.Errorf("%s sees %v, want %v", c.label, got, c.want)
		}
		// Zähler stammen aus derselben Menge, nie aus dem Gesamtbestand.
		if page.Sessions != len(c.want) || page.Matches != 3*len(c.want) {
			t.Errorf("%s: sessions=%d matches=%d for %d readable sessions", c.label, page.Sessions, page.Matches, len(c.want))
		}
		total := 0
		for _, o := range page.Facets.Projects {
			total += o.Count
		}
		if total != len(c.want) {
			t.Errorf("%s: project facets add up to %d, want %d (%+v)", c.label, total, len(c.want), page.Facets.Projects)
		}
	}
}

func TestSnippetsNeverComeFromHiddenSessions(t *testing.T) {
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	addSession(t, st, "secret", 1, roleProject, "mainex", "", transcriptWith("quokka-hidden-word"))
	addSession(t, st, "open", 4, roleProject, "box", VisProject, transcriptWith("zebra"))
	page, err := st.SearchTranscripts(viewer(st, "person:3", "mia"), SearchQuery{Q: "quokka", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Matches != 0 || len(page.Groups) != 0 || page.Sessions != 0 {
		t.Fatalf("hidden content leaked into the result: %+v", page)
	}
	b, _ := json.Marshal(page)
	if strings.Contains(string(b), "quokka") && !strings.Contains(string(b), `"q"`) {
		t.Errorf("hidden word in the serialized page: %s", b)
	}
}

func TestBrowseShowsPrivateRowsToMembersButNeverTheirContent(t *testing.T) {
	f := newVisFixture(t)
	page, err := f.st.BrowseSessions(viewer(f.st, "person:3", "mia"), SessionFilter{}, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 5 {
		t.Fatalf("member sees %d rows, want all 5 as metadata", len(page.Rows))
	}
	for _, r := range page.Rows {
		readable := r.Session.ExternalID == "b-mia-private" || r.Session.ExternalID == "c-rex-project" || r.Session.ExternalID == "d-rex-guests"
		if r.Readable != readable {
			t.Errorf("%s readable=%v, want %v", r.Session.ExternalID, r.Readable, readable)
		}
		if !r.Readable && (r.Session.Title != "" || r.Session.Messages != 0 || r.Session.PublicID != "") {
			t.Errorf("unreadable row exposes content or an address: %+v", r.Session)
		}
		if r.Readable && r.Session.Title == "" {
			t.Errorf("readable row lacks a title")
		}
	}
}

func TestGuestListsOnlyGuestSharedSessionsWithoutMachineOrOwner(t *testing.T) {
	f := newVisFixture(t)
	page, err := f.st.BrowseSessions(viewer(f.st, "person:5", "gus"), SessionFilter{}, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 1 || page.Rows[0].Session.ID != f.d.ID || !page.Rows[0].Readable {
		t.Fatalf("guest rows = %+v", page.Rows)
	}
	s := page.Rows[0].Session
	if s.Scope.Machine != "" || s.Scope.Branch != "" || s.CWD != "" || s.Owner != "" || s.AccountID != 0 || s.ExternalID != "" {
		t.Errorf("guest sees machine, branch, path or owner: %+v", s)
	}
	if s.Harness == "" || s.Scope.Project == "" {
		t.Errorf("guest must keep platform and project: %+v", s)
	}
	if len(page.Facets.Machines) != 0 {
		t.Errorf("guest gets machine facets: %+v", page.Facets.Machines)
	}
}

func TestStrangerBrowsesNothing(t *testing.T) {
	f := newVisFixture(t)
	page, err := f.st.BrowseSessions(viewer(f.st, "person:6", "nora"), SessionFilter{}, "", 50)
	if err != nil || len(page.Rows) != 0 || page.Next != "" {
		t.Fatalf("stranger: rows=%d next=%q err=%v", len(page.Rows), page.Next, err)
	}
}

func TestGuestAlsoNeedsTheGuestLevelForHitsInsideASession(t *testing.T) {
	f := newVisFixture(t)
	pa := viewer(f.st, "person:5", "gus")
	if _, err := f.st.SessionHits(pa, f.c, "zebra", ""); err == nil {
		t.Error("guest got in-session hits of a members-only session")
	}
	hits, err := f.st.SessionHits(pa, f.d, "zebra", "")
	if err != nil || len(hits) != 3 {
		t.Errorf("guest hits in guest-shared session: %v %v", hits, err)
	}
}

func TestEnforcementOffKeepsEverythingVisible(t *testing.T) {
	st := accessFixture(t) // Log-Modus
	addSession(t, st, "a", 1, roleProject, "m", "", transcriptWith("zebra"))
	_, got := searchIDs(t, st, viewer(st, "person:5", "gus"), "zebra")
	if !sameKeys(got, "a") {
		t.Errorf("log mode must not hide: %v", got)
	}
}

// ------------------------------------------------ Freigabe und Matrix

func TestVisibilityLevelsOpenTheMatrixStepwise(t *testing.T) {
	member, guest := RoleInfo{Role: RoleMember}, RoleInfo{Role: RoleGuest}
	lead, owner := RoleInfo{Role: RoleLead}, RoleInfo{Role: RoleOwner}
	for _, c := range []struct {
		name string
		role RoleInfo
		res  Resource
		act  Action
		obj  Object
		want bool
	}{
		{"guest reads guest-shared transcript", guest, ResTranscript, ActRead, Object{Shared: true, Guests: true}, true},
		{"guest reads members-only transcript", guest, ResTranscript, ActRead, Object{Shared: true}, false},
		{"guest reads private transcript", guest, ResTranscript, ActRead, Object{}, false},
		{"guest sees guest-shared meta", guest, ResSessionMeta, ActRead, Object{Shared: true, Guests: true}, true},
		{"guest sees private meta", guest, ResSessionMeta, ActRead, Object{}, false},
		{"member reads project-shared", member, ResTranscript, ActRead, Object{Shared: true}, true},
		{"member reads private foreign", member, ResTranscript, ActRead, Object{}, false},
		{"owner of session shares", member, ResTranscript, ActShare, Object{Own: true}, true},
		{"foreign member cannot share", member, ResTranscript, ActShare, Object{}, false},
		{"lead cannot share foreign", lead, ResTranscript, ActShare, Object{}, false},
		{"project owner shares foreign", owner, ResTranscript, ActShare, Object{}, true},
	} {
		if got := MatrixAllows(c.role, c.res, c.act, c.obj); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSetSessionVisibilityIsForTheOwnerAndTheProjectOwner(t *testing.T) {
	f := newVisFixture(t)
	st := f.st
	// mia teilt ihre eigene Session mit Gästen.
	if err := st.SetSessionVisibility(f.b.ID, viewer(st, "person:3", "mia"), VisGuests); err != nil {
		t.Fatalf("owner: %v", err)
	}
	got, _ := st.SessionByID(f.b.ID)
	if got.Visibility != VisGuests || !got.Shared {
		t.Errorf("after share: %+v", got)
	}
	// Ein fremdes Mitglied, ein Lead und ein Gast dürfen es nicht.
	for _, who := range []struct{ id, label string }{{"person:4", "rex"}, {"person:2", "lena"}, {"person:5", "gus"}, {"person:6", "nora"}} {
		if err := st.SetSessionVisibility(f.a.ID, viewer(st, who.id, who.label), VisProject); err == nil {
			t.Errorf("%s changed a foreign session's visibility", who.label)
		}
	}
	// Der Projekt-Owner darf.
	if err := st.SetSessionVisibility(f.e.ID, viewer(st, "person:1", "robin"), VisProject); err != nil {
		t.Errorf("project owner: %v", err)
	}
	if err := st.SetSessionVisibility(f.b.ID, viewer(st, "person:3", "mia"), VisPrivate); err != nil {
		t.Fatal(err)
	}
	got, _ = st.SessionByID(f.b.ID)
	if got.Visibility != VisPrivate || got.Shared {
		t.Errorf("after unshare: %+v", got)
	}
	var events int
	var last string
	_ = st.db.QueryRow(`SELECT count(*), COALESCE(MAX(actor),'') FROM session_share_events WHERE session_id=?`, f.b.ID).Scan(&events, &last)
	if events != 2 || last != "person:3" {
		t.Errorf("audit rows = %d actor %q, want 2 by person:3", events, last)
	}
	if _, err := st.db.Exec(`DELETE FROM session_share_events`); err == nil {
		t.Error("audit log can be deleted")
	}
	if err := st.SetSessionVisibility(f.b.ID, viewer(st, "person:3", "mia"), "world"); err == nil {
		t.Error("unknown level accepted")
	}
}

func TestLegacySharedFlagMeansProjectLevel(t *testing.T) {
	st := orgStore(t, "robin")
	s := addSession(t, st, "s", 1, "", "m", "", nil)
	if _, err := st.db.Exec(`UPDATE sessions SET shared=1 WHERE id=?`, s.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := st.SessionByID(s.ID)
	if got.Visibility != VisProject || !got.Shared {
		t.Errorf("legacy shared row = %+v", got)
	}
	if err := st.SetSessionShared(s.ID, "person:1", false); err != nil {
		t.Fatal(err)
	}
	got, _ = st.SessionByID(s.ID)
	if got.Visibility != VisPrivate || got.Shared {
		t.Errorf("after SetSessionShared(false) = %+v", got)
	}
}

// -------------------------------------------------------- Zufalls-IDs

func TestPublicIDsAreRandomUniqueAndLookupWorks(t *testing.T) {
	st := orgStore(t, "robin")
	seen := map[string]bool{}
	var last Session
	for i := 0; i < 200; i++ {
		last = addSession(t, st, fmt.Sprintf("s%d", i), 1, "", "m", "", nil)
		if len(last.PublicID) < 10 || seen[last.PublicID] {
			t.Fatalf("public id %q short or duplicate", last.PublicID)
		}
		if strings.Trim(last.PublicID, "abcdefghijkmnpqrstuvwxyz23456789") != "" {
			t.Fatalf("public id %q has characters outside the alphabet", last.PublicID)
		}
		seen[last.PublicID] = true
	}
	got, err := st.SessionByPublicID(last.PublicID)
	if err != nil || got.ID != last.ID {
		t.Fatalf("lookup: %v %+v", err, got)
	}
	if _, err := st.SessionByPublicID("nonexistent1"); err == nil {
		t.Error("unknown id resolved")
	}
	if _, err := st.SessionByPublicID(""); err == nil {
		t.Error("empty id resolved")
	}
	// Eine zweite Upsert-Runde ändert die Adresse nicht.
	again := addSession(t, st, "s199", 1, "", "m", "", nil)
	if again.PublicID != last.PublicID {
		t.Errorf("address changed on upsert: %q -> %q", last.PublicID, again.PublicID)
	}
}

func TestMigrationGivesExistingSessionsAnAddress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := addSession(t, st, "old", 0, "", "m", "", nil)
	if _, err := st.db.Exec(`UPDATE sessions SET public_id='' WHERE id=?`, s.ID); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got, _ := st.SessionByID(s.ID)
	if got.PublicID == "" {
		t.Fatal("legacy session still has no address after reopening")
	}
}

// ------------------------------------------- Fenster, Treffer, Gliederung

func numbered(n int) []Chunk {
	var out []Chunk
	for i := 0; i < n; i++ {
		var raw string
		switch i % 3 {
		case 0:
			raw = userLine("2026-10-01T10:00:00Z", fmt.Sprintf("prompt %d about okapi", i))
		case 1:
			raw = assistantLine("2026-10-01T10:00:01Z", textBlock("answer"), bashBlock(fmt.Sprintf("t%d", i), "ls"))
		default:
			raw = resultLine("2026-10-01T10:00:02Z", fmt.Sprintf("t%d", i-1), "files")
		}
		out = append(out, Chunk{Seq: i, Raw: raw})
	}
	return out
}

func TestSessionWindowAroundASequence(t *testing.T) {
	st := orgStore(t, "robin")
	s := addSession(t, st, "long", 1, "", "m", "", numbered(1000))
	w, err := st.SessionWindow(s.ID, 0, 500, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Chunks) != 201 && len(w.Chunks) != 200 {
		t.Fatalf("window has %d chunks", len(w.Chunks))
	}
	if w.Chunks[0].Seq != 400 || w.Chunks[len(w.Chunks)-1].Seq < 598 {
		t.Errorf("window = %d..%d", w.Chunks[0].Seq, w.Chunks[len(w.Chunks)-1].Seq)
	}
	if !w.Earlier || !w.Later || w.Total != 1000 {
		t.Errorf("earlier=%v later=%v total=%d", w.Earlier, w.Later, w.Total)
	}
	first, _ := st.SessionWindow(s.ID, 0, 0, 200)
	if first.Earlier || !first.Later || first.Chunks[0].Seq != 0 || len(first.Chunks) != 200 {
		t.Errorf("first window = earlier=%v later=%v n=%d", first.Earlier, first.Later, len(first.Chunks))
	}
	last, _ := st.SessionWindow(s.ID, 900, 0, 200)
	if last.Later || len(last.Chunks) != 100 {
		t.Errorf("last window later=%v n=%d", last.Later, len(last.Chunks))
	}
	near, _ := st.SessionWindow(s.ID, 0, 10, 200)
	if near.Chunks[0].Seq != 0 || near.Earlier {
		t.Errorf("around near the start: first=%d earlier=%v", near.Chunks[0].Seq, near.Earlier)
	}
}

func TestSessionWindowTailAndNeighbours(t *testing.T) {
	st := orgStore(t, "robin")
	s := addSession(t, st, "long", 1, "", "m", "", numbered(1000))
	tail, err := st.SessionWindow(s.ID, -1, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	if tail.Chunks[0].Seq != 800 || tail.Chunks[len(tail.Chunks)-1].Seq != 999 || tail.Later || !tail.Earlier {
		t.Errorf("tail = %d..%d later=%v", tail.Chunks[0].Seq, tail.Chunks[len(tail.Chunks)-1].Seq, tail.Later)
	}
	if tail.EarlierFrom != 600 {
		t.Errorf("EarlierFrom = %d, want 600", tail.EarlierFrom)
	}
	mid, _ := st.SessionWindow(s.ID, 200, 0, 200)
	if mid.EarlierFrom != 0 || mid.LaterFrom != 400 {
		t.Errorf("mid neighbours = %d, %d", mid.EarlierFrom, mid.LaterFrom)
	}
	short := addSession(t, st, "short", 1, "", "m", "", numbered(5))
	all, _ := st.SessionWindow(short.ID, -1, 0, 200)
	if len(all.Chunks) != 5 || all.Earlier || all.Later {
		t.Errorf("short session window = %d earlier=%v later=%v", len(all.Chunks), all.Earlier, all.Later)
	}
}

func TestSessionHitsListEveryPositionInTheWholeSession(t *testing.T) {
	st := orgStore(t, "robin")
	s := addSession(t, st, "long", 1, "", "m", "", numbered(1000))
	pa := viewer(st, "person:1", "robin")
	hits, err := st.SessionHits(pa, s, "okapi", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 334 || hits[0] != 0 || hits[1] != 3 || hits[len(hits)-1] != 999 {
		t.Fatalf("hits n=%d first=%v", len(hits), hits[:2])
	}
	for i := 1; i < len(hits); i++ {
		if hits[i] <= hits[i-1] {
			t.Fatal("hits not ascending")
		}
	}
	none, _ := st.SessionHits(pa, s, "nothing-here", "")
	if len(none) != 0 {
		t.Errorf("unexpected hits %v", none)
	}
}

func TestSessionOutlineListsPromptsWithToolCalls(t *testing.T) {
	st := orgStore(t, "robin")
	s := addSession(t, st, "long", 1, "", "m", "", numbered(9))
	out, err := st.SessionOutline(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 || out[0].Seq != 0 || out[1].Seq != 3 || out[0].ToolCalls != 1 {
		t.Fatalf("outline = %+v", out)
	}
	if out[0].Text != "prompt 0 about okapi" {
		t.Errorf("prompt text = %q", out[0].Text)
	}
}

// ---------------------------------------------------- Cursor-Paginierung

func TestBrowseCursorPagesWithoutOverlap(t *testing.T) {
	st := orgStore(t, "robin")
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 120; i++ {
		s := addSession(t, st, fmt.Sprintf("s%03d", i), 1, "", "m", "", nil)
		ts := base.Add(time.Duration(i/2) * time.Minute).Format(time.RFC3339) // Paare mit gleichem Zeitstempel
		if _, err := st.db.Exec(`UPDATE sessions SET last_seen_at=? WHERE id=?`, ts, s.ID); err != nil {
			t.Fatal(err)
		}
	}
	pa := viewer(st, "person:1", "robin")
	seen := map[int64]bool{}
	cursor, pages := "", 0
	var prev string
	for {
		page, err := st.BrowseSessions(pa, SessionFilter{}, cursor, 50)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, r := range page.Rows {
			if seen[r.Session.ID] {
				t.Fatalf("session %d on two pages", r.Session.ID)
			}
			seen[r.Session.ID] = true
			if prev != "" && r.Session.LastSeenAt > prev {
				t.Fatal("not newest first")
			}
			prev = r.Session.LastSeenAt
		}
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
	if len(seen) != 120 || pages != 3 {
		t.Errorf("seen=%d pages=%d", len(seen), pages)
	}
	if _, err := st.BrowseSessions(pa, SessionFilter{}, "garbage!", 50); err != nil {
		t.Errorf("bad cursor must start over, got %v", err)
	}
}

func TestSearchCursorPagesBothSorts(t *testing.T) {
	st := orgStore(t, "robin")
	for i := 0; i < 7; i++ {
		s := addSession(t, st, fmt.Sprintf("s%d", i), 1, "", "m", "", chunksOf(userLine("2026-10-01T10:00:00Z", strings.Repeat("filler ", i)+"walrus")))
		if _, err := st.db.Exec(`UPDATE sessions SET last_seen_at=? WHERE id=?`, fmt.Sprintf("2026-10-0%dT00:00:00Z", i+1), s.ID); err != nil {
			t.Fatal(err)
		}
	}
	pa := viewer(st, "person:1", "robin")
	for _, sortBy := range []string{"best", "newest"} {
		seen := map[string]bool{}
		cursor := ""
		var order []string
		for pages := 0; pages < 10; pages++ {
			page, err := st.SearchTranscripts(pa, SearchQuery{Q: "walrus", Sort: sortBy, Cursor: cursor, Limit: 3})
			if err != nil {
				t.Fatal(err)
			}
			if page.Sessions != 7 || page.Matches != 7 {
				t.Fatalf("%s: totals sessions=%d matches=%d", sortBy, page.Sessions, page.Matches)
			}
			for _, g := range page.Groups {
				if seen[g.Session.ExternalID] {
					t.Fatalf("%s: %s twice", sortBy, g.Session.ExternalID)
				}
				seen[g.Session.ExternalID] = true
				order = append(order, g.Session.ExternalID)
			}
			if page.Next == "" {
				break
			}
			cursor = page.Next
		}
		if len(seen) != 7 {
			t.Errorf("%s: saw %d sessions", sortBy, len(seen))
		}
		if sortBy == "newest" && order[0] != "s6" {
			t.Errorf("newest first: %v", order)
		}
	}
}

func TestFacetsAndFiltersNarrowTheReadableSet(t *testing.T) {
	st := orgStore(t, "robin", "mia")
	addSession(t, st, "a", 1, "", "mainex", "", transcriptWith("zebra"))
	addSession(t, st, "b", 1, "", "laptop", "", transcriptWith("zebra"))
	if _, err := st.UpsertSession(Session{Harness: "codex", ExternalID: "c", AccountID: 2, Scope: scope.Axes{Machine: "mainex"}}); err != nil {
		t.Fatal(err)
	}
	pa := viewer(st, "person:1", "robin")
	page, err := st.BrowseSessions(pa, SessionFilter{Machine: "mainex"}, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 2 {
		t.Errorf("machine filter rows = %d", len(page.Rows))
	}
	counts := map[string]int{}
	for _, o := range page.Facets.Machines {
		counts[o.Value] = o.Count
	}
	// Die Maschinen-Facette zählt ohne den eigenen Filter.
	if counts["mainex"] != 2 || counts["laptop"] != 1 {
		t.Errorf("machine facets = %v", counts)
	}
	mine, _ := st.BrowseSessions(pa, SessionFilter{Mine: true}, "", 50)
	if len(mine.Rows) != 2 {
		t.Errorf("mine rows = %d", len(mine.Rows))
	}
	codex, _ := st.BrowseSessions(pa, SessionFilter{Harness: "codex"}, "", 50)
	if len(codex.Rows) != 1 {
		t.Errorf("harness rows = %d", len(codex.Rows))
	}
	res, _ := st.SearchTranscripts(pa, SearchQuery{Q: "zebra", Filter: SessionFilter{Machine: "laptop"}, Limit: 10})
	if res.Sessions != 1 || res.Groups[0].Session.ExternalID != "b" {
		t.Errorf("search with machine filter: %+v", res.Sessions)
	}
	since, _ := st.BrowseSessions(pa, SessionFilter{Since: time.Now().Add(time.Hour)}, "", 50)
	if len(since.Rows) != 0 {
		t.Errorf("since in the future still lists %d", len(since.Rows))
	}
}

// ------------------------------------------------- Nachindizieren (Migration)

// legacyChunks schreibt Chunks so, wie sie vor dem Index in der Datenbank
// standen: ohne Indexzeile, mit leerem text-Feld.
func legacyChunks(t *testing.T, st *Store, sessionID int64, chunks []Chunk) {
	t.Helper()
	for _, c := range chunks {
		if _, err := st.db.Exec(`INSERT INTO session_chunks(session_id, seq, role, text, raw) VALUES(?,?,?,?,?)`, sessionID, c.Seq, "", "", c.Raw); err != nil {
			t.Fatal(err)
		}
	}
}

func resetBackfill(t *testing.T, st *Store) {
	t.Helper()
	// Ein Zustand wie direkt nach dem Update eines alten Binarys.
	for _, q := range []string{`DELETE FROM chunk_index`, `INSERT INTO sess_fts(sess_fts) VALUES('delete-all')`, `DELETE FROM index_state`, `UPDATE sessions SET title='', msg_count=0`} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := st.startIndexBackfill(); err != nil {
		t.Fatal(err)
	}
}

func TestBackfillIndexesOldChunksInSteps(t *testing.T) {
	st := orgStore(t, "robin")
	s := addSession(t, st, "old", 1, "", "m", "", nil)
	legacyChunks(t, st, s.ID, transcriptWith("narwhal"))
	resetBackfill(t, st)
	pa := viewer(st, "person:1", "robin")
	if page, _ := st.SearchTranscripts(pa, SearchQuery{Q: "narwhal", Limit: 5}); page.Matches != 0 {
		t.Fatal("unindexed chunks are found")
	}
	pr := st.IndexProgress()
	if pr.Done || pr.Percent != 0 {
		t.Fatalf("progress before = %+v", pr)
	}
	done, err := st.IndexBackfillStep(2)
	if err != nil || done {
		t.Fatalf("first step done=%v err=%v", done, err)
	}
	if mid := st.IndexProgress(); mid.Done || mid.Percent <= 0 || mid.Percent >= 100 {
		t.Errorf("progress in the middle = %+v", mid)
	}
	for !done {
		if done, err = st.IndexBackfillStep(2); err != nil {
			t.Fatal(err)
		}
	}
	if pr := st.IndexProgress(); !pr.Done || pr.Percent != 100 {
		t.Errorf("progress after = %+v", pr)
	}
	page, _ := st.SearchTranscripts(pa, SearchQuery{Q: "narwhal", Limit: 5})
	if page.Matches != 3 {
		t.Errorf("after backfill matches = %d, want 3 (message, command, output)", page.Matches)
	}
	if th, _ := st.SearchTranscripts(pa, SearchQuery{Q: "privately", Kind: "thinking", Limit: 5}); th.Matches != 1 {
		t.Errorf("thinking block of old chunk not indexed: %d", th.Matches)
	}
	got, _ := st.SessionByID(s.ID)
	if got.Title == "" || got.Messages != 3 {
		t.Errorf("title=%q messages=%d after backfill", got.Title, got.Messages)
	}
}

func TestBackfillResumesAndNeverCountsAChunkTwice(t *testing.T) {
	st := orgStore(t, "robin")
	s := addSession(t, st, "old", 1, "", "m", "", nil)
	legacyChunks(t, st, s.ID, transcriptWith("narwhal"))
	resetBackfill(t, st)
	if _, err := st.IndexBackfillStep(2); err != nil {
		t.Fatal(err)
	}
	// Neue Chunks kommen während des Nachindizierens und werden sofort indiziert.
	if err := st.AppendChunks(s.ID, []Chunk{{Seq: 5, Raw: userLine("2026-10-01T11:00:00Z", "a later prompt")}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := st.RunIndexBackfill(ctx, BackfillOptions{Batch: 2}); err == nil {
		t.Error("cancelled run must report the cancellation")
	}
	if err := st.RunIndexBackfill(context.Background(), BackfillOptions{Batch: 2}); err != nil {
		t.Fatal(err)
	}
	// Auch ein erneuter Lauf (Neustart) ändert nichts mehr.
	if err := st.RunIndexBackfill(context.Background(), BackfillOptions{Batch: 2}); err != nil {
		t.Fatal(err)
	}
	got, _ := st.SessionByID(s.ID)
	if got.Messages != 4 {
		t.Errorf("messages = %d, want 4", got.Messages)
	}
	var n int
	_ = st.db.QueryRow(`SELECT count(*) FROM chunk_index`).Scan(&n)
	if n != 6 {
		t.Errorf("index rows = %d, want 6", n)
	}
}

func TestIndexSessionFillsGapsOnDemand(t *testing.T) {
	st := orgStore(t, "robin")
	s := addSession(t, st, "old", 1, "", "m", "", nil)
	legacyChunks(t, st, s.ID, transcriptWith("narwhal"))
	resetBackfill(t, st)
	if err := st.IndexSession(s.ID); err != nil {
		t.Fatal(err)
	}
	out, err := st.SessionOutline(s.ID)
	if err != nil || len(out) != 1 {
		t.Fatalf("outline after on-demand index: %v %v", out, err)
	}
	if err := st.IndexSession(s.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := st.SessionByID(s.ID)
	if got.Messages != 3 {
		t.Errorf("messages = %d", got.Messages)
	}
}

func TestBackfillOnAnEmptyDatabaseIsDone(t *testing.T) {
	st := orgStore(t, "robin")
	if pr := st.IndexProgress(); !pr.Done {
		t.Errorf("empty database progress = %+v", pr)
	}
}

// Messung auf einer Kopie mit vielen Chunks: der Start darf nicht blockieren,
// das Nachindizieren läuft in kleinen Schritten.
func TestMigrationOnManyChunksStartsFastAndIndexesInTheBackground(t *testing.T) {
	if testing.Short() {
		t.Skip("many chunks")
	}
	path := filepath.Join(t.TempDir(), "big.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddPerson("robin"); err != nil {
		t.Fatal(err)
	}
	const sessions, perSession = 40, 1500
	tx, _ := st.db.Begin()
	for i := 0; i < sessions; i++ {
		res, _ := tx.Exec(`INSERT INTO sessions(harness, external_id, started_at, last_seen_at) VALUES('claude-code', ?, '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`, fmt.Sprintf("big%d", i))
		sid, _ := res.LastInsertId()
		for j, c := range numbered(perSession) {
			if _, err := tx.Exec(`INSERT INTO session_chunks(session_id, seq, role, text, raw) VALUES(?,?,?,?,?)`, sid, j, "", "", c.Raw); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// Zustand eines Altbestands: kein Index, kein Zustand.
	for _, q := range []string{`DELETE FROM chunk_index`, `INSERT INTO sess_fts(sess_fts) VALUES('delete-all')`, `DELETE FROM index_state`} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()

	start := time.Now()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	open := time.Since(start)
	if open > 5*time.Second {
		t.Errorf("open blocked for %v", open)
	}
	start = time.Now()
	if err := st.RunIndexBackfill(context.Background(), BackfillOptions{}); err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	t.Logf("open %v, backfill of %d chunks %v (%.0f chunks/s)", open, sessions*perSession, took, float64(sessions*perSession)/took.Seconds())
	pa := viewer(st, "person:1", "robin")
	page, err := st.SearchTranscripts(pa, SearchQuery{Q: "okapi", Limit: 5})
	if err != nil || page.Sessions != sessions {
		t.Errorf("sessions with hits = %d (%v)", page.Sessions, err)
	}
	_ = os.Remove(path)
}

func TestIndexSurvivesTheWriterQueue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rt.db")
	st, err := OpenRuntime(path, DefaultWriterConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.AddPerson("robin"); err != nil {
		t.Fatal(err)
	}
	s := addSession(t, st, "s", 1, "", "m", "", transcriptWith("zebra"))
	if err := st.IndexSession(s.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.RunIndexBackfill(context.Background(), BackfillOptions{Batch: 3}); err != nil {
		t.Fatal(err)
	}
	pa := viewer(st, "person:1", "robin")
	page, err := st.SearchTranscripts(pa, SearchQuery{Q: "zebra", Limit: 5})
	if err != nil || page.Matches != 3 {
		t.Fatalf("matches=%d err=%v", page.Matches, err)
	}
	if err := st.SetSessionVisibility(s.ID, pa, VisProject); err != nil {
		t.Fatal(err)
	}
}

func TestCanShareAndSessionViewFollowTheMatrix(t *testing.T) {
	f := newVisFixture(t)
	st := f.st
	if !viewer(st, "person:4", "rex").CanShareSession(f.c) || !viewer(st, "person:1", "robin").CanShareSession(f.c) {
		t.Error("owner of the session and project owner must be able to share")
	}
	for _, who := range []struct{ id, label string }{{"person:3", "mia"}, {"person:2", "lena"}, {"person:5", "gus"}, {"person:6", "nora"}} {
		if viewer(st, who.id, who.label).CanShareSession(f.c) {
			t.Errorf("%s may share a foreign session", who.label)
		}
	}
	g := viewer(st, "person:5", "gus").SessionView(f.d)
	if g.Scope.Machine != "" || g.Owner != "" || g.Scope.Project == "" {
		t.Errorf("guest session view = %+v", g)
	}
	m := viewer(st, "person:3", "mia").SessionView(f.d)
	if m.Scope.Machine != "box" {
		t.Errorf("member loses the machine: %+v", m)
	}
}

func TestSessionLinksAreFilteredByTheViewer(t *testing.T) {
	f := newVisFixture(t)
	st := f.st
	pub := scope.Axes{Project: roleProject}
	k, err := st.InsertKnowledge(Knowledge{Type: "pitfall", Title: "trusted note", Body: "b", Scope: pub, Person: "robin", Confidence: "trusted"})
	if err != nil {
		t.Fatal(err)
	}
	staged, err := st.InsertKnowledge(Knowledge{Type: "note", Title: "staged note", Body: "b", Scope: pub, Person: "robin", Confidence: "staged"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{k, staged} {
		if err := st.AddEvidence(id, []Evidence{{SessionID: f.d.ID, ChunkSeq: 2, Quote: "q"}}); err != nil {
			t.Fatal(err)
		}
	}
	own, _ := st.SessionLinks(viewer(st, "person:3", "mia"), []int64{f.d.ID})
	if len(own[f.d.ID]) != 2 {
		t.Errorf("member sees %d links, want 2", len(own[f.d.ID]))
	}
	guest, _ := st.SessionLinks(viewer(st, "person:5", "gus"), []int64{f.d.ID})
	if len(guest[f.d.ID]) != 1 || guest[f.d.ID][0].Label != "trusted note" {
		t.Errorf("guest links = %+v, want only the trusted entry", guest[f.d.ID])
	}
}
