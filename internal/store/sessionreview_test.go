package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

const roleProjectB = "github.com/dw/q"

// gus is guest in roleProject and member in roleProjectB.
func newMixedRoleFixture(t *testing.T) *Store {
	t.Helper()
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	if _, err := st.EnsureProject("person:1", roleProjectB); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProjectRole("person:1", roleProjectB, "person:5", RoleMember, false, RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestMixedRoleFacetsAndMachineFilterUseTheViewedMachine(t *testing.T) {
	st := newMixedRoleFixture(t)
	addSession(t, st, "x-guest", 4, roleProject, "boxa", VisGuests, transcriptWith("zebra"))
	addSession(t, st, "y-private", 3, roleProjectB, "boxb", "", transcriptWith("zebra"))
	gus := viewer(st, "person:5", "gus")

	page, err := st.BrowseSessions(gus, SessionFilter{}, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	var machines []string
	for _, o := range page.Facets.Machines {
		machines = append(machines, o.Value)
	}
	if strings.Join(machines, ",") != "boxb" {
		t.Errorf("machine facet = %v, want only the machine of the project where the viewer is a member", machines)
	}
	for _, c := range []struct {
		machine string
		rows    int
	}{{"boxa", 0}, {"boxb", 1}} {
		p, err := st.BrowseSessions(gus, SessionFilter{Machine: c.machine}, "", 50)
		if err != nil || len(p.Rows) != c.rows {
			t.Errorf("browse machine=%s: rows=%d err=%v, want %d", c.machine, len(p.Rows), err, c.rows)
		}
	}
	s, err := st.SearchTranscripts(gus, SearchQuery{Q: "zebra", Filter: SessionFilter{Machine: "boxa"}, Limit: 10})
	if err != nil || len(s.Groups) != 0 || s.Sessions != 0 || s.Matches != 0 {
		t.Errorf("search machine=boxa as a guest of that project: %+v err=%v", s, err)
	}
	if s, _ = st.SearchTranscripts(gus, SearchQuery{Q: "zebra", Limit: 10}); len(s.Facets.Machines) != 0 {
		t.Errorf("search machine facet leaks %v", s.Facets.Machines)
	}
}

func TestMetaViewHidesWhatTheViewerMayNotRead(t *testing.T) {
	f := newVisFixture(t)
	mia := viewer(f.st, "person:3", "mia")
	got := mia.MetaView(f.a)
	if got.Title != "" || got.Messages != 0 || got.PublicID != "" {
		t.Errorf("member meta view of a foreign private session: %+v", got)
	}
	if own := mia.MetaView(f.b); own.Title == "" || own.PublicID == "" {
		t.Errorf("owner lost the content of her session: %+v", own)
	}
	gus := viewer(f.st, "person:5", "gus")
	g := gus.MetaView(f.d)
	if g.Title == "" || g.Scope.Machine != "" || g.Scope.Branch != "" || g.CWD != "" || g.ExternalID != "" || g.Owner != "" {
		t.Errorf("guest meta view of a guest-shared session: %+v", g)
	}
}

func TestCursorsCarryNeitherInternalIDsNorScores(t *testing.T) {
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	for i := 0; i < 8; i++ {
		addSession(t, st, "hidden-"+strconv.Itoa(i), 3, roleProject, "m", "", transcriptWith("zebra"))
	}
	var visible []Session
	for i := 0; i < 3; i++ {
		visible = append(visible, addSession(t, st, "vis-"+strconv.Itoa(i), 4, roleProject, "box", VisGuests, transcriptWith("zebra")))
	}
	gus := viewer(st, "person:5", "gus")
	b, err := st.BrowseSessions(gus, SessionFilter{}, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	cursors := map[string]string{"browse": b.Next}
	for _, sort := range []string{"best", "newest"} {
		p, err := st.SearchTranscripts(gus, SearchQuery{Q: "zebra", Sort: sort, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		cursors["search "+sort] = p.Next
	}
	for name, c := range cursors {
		if c == "" {
			t.Fatalf("%s: no cursor", name)
		}
		if strings.Contains(c, ".") {
			t.Errorf("%s cursor carries a score: %q", name, c)
		}
		for _, field := range strings.Split(c, "|") {
			for _, s := range visible {
				if field == strconv.FormatInt(s.ID, 10) {
					t.Errorf("%s cursor carries the internal id %d: %q", name, s.ID, c)
				}
			}
		}
	}
}

func TestIndexSessionRunsOneWriterJobPerBatch(t *testing.T) {
	st, err := OpenRuntime(filepath.Join(t.TempDir(), "rt.db"), DefaultWriterConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.AddPerson("robin"); err != nil {
		t.Fatal(err)
	}
	s := addSession(t, st, "s", 1, "", "m", "", nil)
	legacyChunks(t, st, s.ID, numbered(700))
	before := st.RuntimeStats().Writer.Admitted
	if err := st.IndexSession(s.ID); err != nil {
		t.Fatal(err)
	}
	if jobs := st.RuntimeStats().Writer.Admitted - before; jobs < 3 {
		t.Errorf("IndexSession took %d writer jobs for 700 chunks, want one per batch of 300", jobs)
	}
	var n int
	_ = st.DB().QueryRow(`SELECT count(*) FROM chunk_index WHERE session_id=?`, s.ID).Scan(&n)
	if n != 700 {
		t.Errorf("indexed %d of 700", n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	legacyChunks(t, st, s.ID, []Chunk{{Seq: 700, Raw: userLine("2026-10-01T10:00:00Z", "late")}})
	if err := st.IndexSessionContext(ctx, s.ID); err == nil {
		t.Error("a cancelled request must stop indexing")
	}
}

func TestBackfillRestartsForChunksBeyondTheBound(t *testing.T) {
	st := orgStore(t, "robin")
	s := addSession(t, st, "old", 1, "", "m", "", nil)
	legacyChunks(t, st, s.ID, transcriptWith("narwhal"))
	resetBackfill(t, st)
	if err := st.RunIndexBackfill(context.Background(), BackfillOptions{Batch: 2}); err != nil {
		t.Fatal(err)
	}
	if !st.IndexProgress().Done {
		t.Fatal("first run not done")
	}
	// An older binary ran for a while and wrote chunks without an index row.
	legacyChunks(t, st, s.ID, []Chunk{{Seq: 100, Raw: userLine("2026-10-01T11:00:00Z", "written by a rolled back binary narwhal")}})
	if err := st.startIndexBackfill(); err != nil {
		t.Fatal(err)
	}
	if st.IndexProgress().Done {
		t.Fatal("chunks beyond the bound are not noticed at start")
	}
	if err := st.RunIndexBackfill(context.Background(), BackfillOptions{Batch: 2}); err != nil {
		t.Fatal(err)
	}
	var total, indexed int
	_ = st.db.QueryRow(`SELECT count(*) FROM session_chunks`).Scan(&total)
	_ = st.db.QueryRow(`SELECT count(*) FROM chunk_index`).Scan(&indexed)
	if total != indexed {
		t.Errorf("indexed %d of %d chunks", indexed, total)
	}
	// A second start finds nothing to redo.
	if err := st.startIndexBackfill(); err != nil || !st.IndexProgress().Done {
		t.Errorf("restart without new chunks: done=%v err=%v", st.IndexProgress().Done, err)
	}
}

func TestBackfillStepStaysWithinItsTimeBudget(t *testing.T) {
	st := orgStore(t, "robin")
	s := addSession(t, st, "old", 1, "", "m", "", nil)
	legacyChunks(t, st, s.ID, numbered(120))
	resetBackfill(t, st)
	old := backfillStepBudget
	backfillStepBudget = time.Nanosecond
	defer func() { backfillStepBudget = old }()
	done, err := st.IndexBackfillStep(300)
	if err != nil || done {
		t.Fatalf("a step with no time left finished the whole stock: done=%v err=%v", done, err)
	}
	var n int
	_ = st.db.QueryRow(`SELECT count(*) FROM chunk_index`).Scan(&n)
	if n == 0 || n >= 120 {
		t.Fatalf("step indexed %d chunks, want some but not all", n)
	}
	for i := 0; !done; i++ {
		if i > 500 {
			t.Fatal("backfill does not make progress")
		}
		if done, err = st.IndexBackfillStep(300); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.db.QueryRow(`SELECT count(*) FROM chunk_index`).Scan(&n)
	if n != 120 {
		t.Errorf("indexed %d of 120", n)
	}
	if got, _ := st.SessionByID(s.ID); got.Messages != 80 {
		t.Errorf("messages = %d, a chunk was counted twice or lost", got.Messages)
	}
}

func idsOfSessions(ss []Session) string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.ExternalID
	}
	return strings.Join(out, ",")
}

// The prefilter is the readable set as SQL: it must give the same sessions as
// the full per-row check, for every kind of viewer.
func TestTranscriptPrefilterEqualsTheRowByRowCheck(t *testing.T) {
	f := newVisFixture(t)
	addSession(t, f.st, "f-loose", 3, "", "laptop", "", transcriptWith("zebra"))
	addSession(t, f.st, "g-unclaimed", 4, "github.com/dw/unclaimed", "box", "", transcriptWith("zebra"))
	addSession(t, f.st, "h-unclaimed-robin", 1, "github.com/dw/unclaimed", "box", "", transcriptWith("zebra"))
	// Altbestand: kein Konto, shared=1 bei Stufe private, und Projekte in
	// nicht kanonischer Schreibweise (die Migration macht sie kanonisch).
	legacy := addSession(t, f.st, "i-legacy-shared", 1, roleProject, "box", "", transcriptWith("zebra"))
	if _, err := f.st.db.Exec(`UPDATE sessions SET account_id=0, shared=1, visibility='private' WHERE id=?`, legacy.ID); err != nil {
		t.Fatal(err)
	}
	for i, p := range []string{" HTTPS://" + roleProject + ".git/ ", "git@" + strings.Replace(roleProject, "/", ":", 1) + ".git"} {
		odd := addSession(t, f.st, fmt.Sprintf("j-odd-%d", i), 4, "", "box", VisGuests, transcriptWith("zebra"))
		if _, err := f.st.db.Exec(`UPDATE sessions SET project=? WHERE id=?`, p, odd.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.st.db.Exec(`DELETE FROM index_state WHERE key='sessions_project_canonical'`); err != nil {
		t.Fatal(err)
	}
	if err := ensureCanonicalSessionProjects(f.st.db); err != nil {
		t.Fatal(err)
	}
	var odd int
	if err := f.st.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE project != '' AND project != lower(trim(project)) OR project LIKE '%.git' OR project LIKE 'git@%'`).Scan(&odd); err != nil || odd != 0 {
		t.Fatalf("%d sessions keep a non canonical project (err %v)", odd, err)
	}
	for _, who := range []struct{ id, label string }{{"person:1", "robin"}, {"person:2", "lena"}, {"person:3", "mia"}, {"person:4", "rex"}, {"person:5", "gus"}, {"person:6", "nora"}} {
		pa := viewer(f.st, who.id, who.label)
		keep := func(s Session) bool { return pa.CanSeeTranscript(s) }
		want, err := f.st.ListSessionsVisible(scope.Axes{}, 100, "", keep, SessionPrefilter{})
		if err != nil {
			t.Fatal(err)
		}
		pre := pa.TranscriptPrefilter()
		viaFilter, err := f.st.ListSessionsVisible(scope.Axes{}, 100, "", nil, pre)
		if err != nil {
			t.Fatal(err)
		}
		if !pre.Exact {
			t.Errorf("%s: the prefilter must be exact under enforcement", who.label)
		}
		if idsOfSessions(want) != idsOfSessions(viaFilter) {
			t.Errorf("%s: row check %s, prefilter %s", who.label, idsOfSessions(want), idsOfSessions(viaFilter))
		}
	}
}

// Hidden sessions cost the guest nothing: the row check never sees them.
func TestGuestScanDoesNotTouchHiddenSessions(t *testing.T) {
	f := newVisFixture(t)
	for i := 0; i < 60; i++ {
		addSession(t, f.st, fmt.Sprintf("hidden-%d", i), 3, roleProject, "laptop", "", nil)
	}
	pa := viewer(f.st, "person:5", "gus")
	calls := 0
	_, err := f.st.SearchSessionsVisible("zebra", scope.Axes{}, "", 20, func(s Session) bool { calls++; return pa.CanSeeTranscript(s) }, pa.TranscriptPrefilter())
	if err != nil {
		t.Fatal(err)
	}
	if calls > 3 {
		t.Errorf("row check ran %d times, hidden sessions must not reach it", calls)
	}
}

// Ranking must not depend on sessions the viewer cannot read: bm25 uses the
// document frequency of the whole index, so a restricted viewer is ranked from
// the readable set only.
func userChunks(texts ...string) []Chunk {
	var out []Chunk
	for i, text := range texts {
		out = append(out, Chunk{Seq: i, Role: "user", Text: text,
			Raw: userLine(fmt.Sprintf("2026-10-01T10:00:%02dZ", i), text)})
	}
	return out
}

func TestRankingIgnoresHiddenSessions(t *testing.T) {
	orders := func(hidden string) (web, api string) {
		st := accessFixture(t)
		st.SetAccessMode(AccessMode{Enforce: true})
		addSession(t, st, "s1", 4, roleProject, "box", VisGuests, userChunks("alpha alpha alpha alpha beta"))
		addSession(t, st, "s2", 4, roleProject, "box", VisGuests, userChunks("alpha beta beta beta beta"))
		for i := 0; i < 40 && hidden != ""; i++ {
			addSession(t, st, fmt.Sprintf("p-%d", i), 3, roleProject, "m", "", userChunks("zzz "+hidden))
		}
		gus := viewer(st, "person:5", "gus")
		page, err := st.SearchTranscripts(gus, SearchQuery{Q: "alpha beta", Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range page.Groups {
			raw, _ := st.SessionByPublicID(g.Session.PublicID)
			web += raw.ExternalID + ","
		}
		hits, err := st.SearchSessionsVisible("alpha beta", scope.Axes{}, "", 50,
			func(s Session) bool { return gus.CanSeeTranscript(s) }, gus.TranscriptPrefilter())
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range hits {
			api += h.Session.ExternalID + ","
		}
		return
	}
	web0, api0 := orders("")
	if web0 == "" || api0 == "" {
		t.Fatalf("no hits: web=%q api=%q", web0, api0)
	}
	for _, hidden := range []string{"alpha", "beta"} {
		web, api := orders(hidden)
		if web != web0 || api != api0 {
			t.Errorf("40 hidden %q sessions changed the order: web %q -> %q, api %q -> %q", hidden, web0, web, api0, api)
		}
	}
}
