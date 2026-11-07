package store

import (
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

const originProject = "github.com/dw/origins"

func originStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.AddPerson("robin"); err != nil {
		t.Fatal(err)
	}
	return st
}

func recordActivity(t *testing.T, st *Store, session, at string, paths map[string]bool) {
	t.Helper()
	var ev []PathActivity
	for p, writes := range paths {
		ev = append(ev, PathActivity{Project: originProject, SessionExternalID: session, Checkout: "/work/repo",
			Tool: "Edit", Path: p, Writes: writes, Quality: ActivityIntent, At: at})
	}
	if err := st.RecordPathActivity(ev); err != nil {
		t.Fatal(err)
	}
}

func originAreas(c *originctx.Context) string {
	if c == nil {
		return "<nil>"
	}
	var out []string
	for _, a := range c.Areas {
		out = append(out, a.Path)
	}
	return strings.Join(out, ",")
}

// Kriterium 1: der Kontext entsteht aus path_activity der Session, ohne dass der
// Schreiber ihn angibt: Zeitpunkt, Session, Pfade mit Gewicht.
func TestOriginForFreezesTheSessionPathsWithWeights(t *testing.T) {
	st := originStore(t)
	at := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	recordActivity(t, st, "sess-1", at, map[string]bool{
		"/work/repo/internal/store/a.go": true, "/work/repo/internal/web/b.go": false, "/etc/hosts": false,
	})
	recordActivity(t, st, "sess-2", at, map[string]bool{"/work/repo/other/x.go": true})

	c, err := st.OriginFor("sess-1", AccountIDOf("person:1"), []string{"cmd/ctx/main.go"})
	if err != nil {
		t.Fatal(err)
	}
	if c == nil || c.SessionRef != "sess-1" || c.CapturedAt == "" {
		t.Fatalf("context = %+v", c)
	}
	if originAreas(c) != "cmd/ctx,internal/store,internal/web" {
		t.Fatalf("areas = %s (named 10 > write 3 > read 1, /etc ignored, other session absent)", originAreas(c))
	}
	kinds := map[string]string{}
	for _, p := range c.Paths {
		kinds[p.Path] = p.Kind
	}
	if kinds["cmd/ctx/main.go"] != "named" || kinds["internal/store/a.go"] != "write" || kinds["internal/web/b.go"] != "read" {
		t.Fatalf("kinds = %v", kinds)
	}

	id, err := st.InsertKnowledge(Knowledge{Type: "note", Title: "t", Body: "b", Scope: scope.Axes{Project: originProject}, Person: "robin", OriginContext: c})
	if err != nil {
		t.Fatal(err)
	}
	k, err := st.KnowledgeByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if k.OriginContext == nil || originAreas(k.OriginContext) != originAreas(c) || len(k.OriginContext.Paths) != 3 || k.OriginContext.SessionRef != "sess-1" {
		t.Fatalf("stored context = %+v", k.OriginContext)
	}
}

func TestOriginForWithoutSessionIsEmptyNotGuessed(t *testing.T) {
	st := originStore(t)
	c, err := st.OriginFor("", 1, nil)
	if err != nil || c != nil {
		t.Fatalf("context = %+v (%v), want nil", c, err)
	}
	// Aktivität eines fremden Kontos unter derselben Kennung zählt nicht.
	recordActivity(t, st, "sess-x", time.Now().UTC().Format(time.RFC3339), map[string]bool{"/work/repo/a/b.go": true})
	c, err = st.OriginFor("sess-x", 99, nil)
	if err != nil || c == nil || len(c.Areas) != 0 {
		t.Fatalf("a foreign account must see no paths: %+v (%v)", c, err)
	}
	id, err := st.InsertKnowledge(Knowledge{Type: "note", Title: "ohne", Body: "b", Person: "robin"})
	if err != nil {
		t.Fatal(err)
	}
	if k, _ := st.KnowledgeByID(id); k.OriginContext != nil {
		t.Fatalf("context = %+v, want none", k.OriginContext)
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM origin_contexts`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows = %d (%v): an entry without context must cost no row", n, err)
	}
}

// Kriterium 2: Die Aufräumung löscht path_activity, der Kontext bleibt.
func TestOriginContextSurvivesPathActivityRetention(t *testing.T) {
	st := originStore(t)
	at := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	recordActivity(t, st, "sess-r", at, map[string]bool{"/work/repo/internal/store/a.go": true})
	c, err := st.OriginFor("sess-r", AccountIDOf("person:1"), nil)
	if err != nil || c == nil || originAreas(c) != "internal/store" {
		t.Fatalf("context = %+v (%v)", c, err)
	}
	kid, _ := st.InsertKnowledge(Knowledge{Type: "note", Title: "k", Body: "b", Person: "robin", OriginContext: c})
	gid, _ := st.PutGhostFile(GhostFile{Project: originProject, Path: "internal/store/a.go", Kind: "file", Description: "d", OriginContext: c})

	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	res, err := st.ApplyCoordRetention(future, future)
	if err != nil {
		t.Fatal(err)
	}
	if res.ActivityDeleted == 0 {
		t.Fatalf("retention deleted no activity: %+v", res)
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM path_activity`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("path_activity rows = %d (%v), want 0", n, err)
	}
	k, _ := st.KnowledgeByID(kid)
	if k.OriginContext == nil || originAreas(k.OriginContext) != "internal/store" || len(k.OriginContext.Paths) != 1 {
		t.Fatalf("knowledge context after retention = %+v", k.OriginContext)
	}
	gs, _ := st.GhostFilesUnder(originProject, "internal")
	if len(gs) != 1 || gs[0].ID != gid || gs[0].OriginContext == nil || originAreas(gs[0].OriginContext) != "internal/store" {
		t.Fatalf("ghost context after retention = %+v", gs)
	}
	if _, err := st.db.Exec(`DELETE FROM origin_areas`); err == nil {
		t.Fatal("origin areas must be immutable")
	}
}

func TestOriginColumnExistsOnTheEightRevisionTablesAndMigrationIsIdempotent(t *testing.T) {
	st := originStore(t)
	for i := 0; i < 2; i++ {
		if err := ensureOriginContexts(st.db); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range anchoredTables {
		if _, err := st.db.Exec(`SELECT origin_id FROM ` + table + ` LIMIT 1`); err != nil {
			t.Errorf("%s.origin_id: %v", table, err)
		}
	}
}

func originOfRow(t *testing.T, st *Store, table, where string, args ...any) *int64 {
	t.Helper()
	var id *int64
	if err := st.db.QueryRow(`SELECT origin_id FROM `+table+` WHERE `+where, args...).Scan(&id); err != nil {
		t.Fatalf("%s: %v", table, err)
	}
	return id
}

// Die Archivpfade kopieren die Spalte mit, sonst ginge der Kontext still verloren.
func TestArchivingCopiesTheOriginIntoTheHistory(t *testing.T) {
	st := originStore(t)
	ctxA := &originctx.Context{SessionRef: "s", CapturedAt: "2026-11-01T10:00:00Z", Areas: []originctx.Area{{Path: "a", Weight: 3}}}
	ctxB := &originctx.Context{SessionRef: "s", CapturedAt: "2026-11-02T10:00:00Z", Areas: []originctx.Area{{Path: "b", Weight: 3}}}

	kid, _ := st.InsertKnowledge(Knowledge{Type: "note", Title: "alt", Body: "eins", Person: "robin", OriginContext: ctxA})
	before := *originOfRow(t, st, "knowledge", "id=?", kid)
	// Eine inhaltliche Bearbeitung ohne eigenen Kontext (Web) hebt die alte
	// Fassung samt Kontext auf, die neue erbt keinen.
	if err := st.UpdateKnowledgeBy(kid, map[string]string{"body": "zwei"}, "lena"); err != nil {
		t.Fatal(err)
	}
	if got := originOfRow(t, st, "knowledge", "id=?", kid); got != nil {
		t.Fatalf("edited revision inherited origin %d", *got)
	}
	if got := originOfRow(t, st, "knowledge_versions", "knowledge_id=?", kid); got == nil || *got != before {
		t.Fatalf("history origin = %v, want %d", got, before)
	}

	g := GhostFile{Project: originProject, Path: "a/x.go", Kind: "file", Description: "eins", OriginContext: ctxA}
	if _, err := st.PutGhostFile(g); err != nil {
		t.Fatal(err)
	}
	gBefore := *originOfRow(t, st, "ghost_files", "path=?", "a/x.go")
	g.Description, g.OriginContext = "zwei", ctxB
	if _, err := st.PutGhostFile(g); err != nil {
		t.Fatal(err)
	}
	if got := originOfRow(t, st, "ghost_file_versions", "path=?", "a/x.go"); got == nil || *got != gBefore {
		t.Fatalf("replaced ghost version origin = %v, want %d", got, gBefore)
	}
	if got := originOfRow(t, st, "ghost_files", "path=?", "a/x.go"); got == nil || *got == gBefore {
		t.Fatalf("head ghost must carry the new origin, got %v", got)
	}
	headBefore := *originOfRow(t, st, "ghost_files", "path=?", "a/x.go")
	if err := st.MoveGhostFile(originProject, "a/x.go", "a/y.go"); err != nil {
		t.Fatal(err)
	}
	if got := originOfRow(t, st, "ghost_files", "path=?", "a/y.go"); got == nil || *got != headBefore {
		t.Fatalf("moved ghost lost its origin: %v", got)
	}
	var moved int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM ghost_file_versions WHERE reason='verschoben' AND origin_id=?`, headBefore).Scan(&moved); err != nil || moved != 1 {
		t.Fatalf("the move version lost the origin (%d, %v)", moved, err)
	}
}

func TestEveryWritePathStoresTheOrigin(t *testing.T) {
	st := originStore(t)
	c := &originctx.Context{SessionRef: "s", CapturedAt: "2026-11-01T10:00:00Z", Areas: []originctx.Area{{Path: "a", Weight: 3}}}

	d, err := st.CreateRequest(requestdomain.CreateInput{
		Request:  requestdomain.Request{Type: "feature", Title: "R", Scope: scope.Axes{Project: originProject}, Person: "robin"},
		Criteria: []string{"eins"}, Origin: c})
	if err != nil {
		t.Fatal(err)
	}
	if originOfRow(t, st, "request_activity", "request_id=? AND kind='request.created'", d.Request.ID) == nil {
		t.Fatal("request.created has no origin")
	}
	if err := st.SetCriterionState(d.Criteria[0].ID, "met", requestdomain.Evidence{Kind: "test", Ref: "T", Person: "robin", Origin: c}); err != nil {
		t.Fatal(err)
	}
	if originOfRow(t, st, "request_evidence", "request_id=?", d.Request.ID) == nil ||
		originOfRow(t, st, "request_activity", "request_id=? AND kind='criterion.met'", d.Request.ID) == nil {
		t.Fatal("criterion evidence or activity has no origin")
	}
	if err := st.CompleteRequest(d.Request.ID, requestdomain.Evidence{Kind: "test", Ref: "alles", Person: "robin", Origin: c}); err != nil {
		t.Fatal(err)
	}
	if originOfRow(t, st, "request_activity", "request_id=? AND kind='request.done'", d.Request.ID) == nil {
		t.Fatal("request.done has no origin")
	}

	pa := st.Access(Principal{ID: "person:1", Label: "robin"})
	pd, err := pa.CreatePain(CreatePainInput{Project: originProject, Title: "Schmerz", AuthorID: 1,
		Actor: pain.Actor{Person: "robin", Origin: c}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pa.SetPainState(pd.Pain.ID, pain.Mitigated, "Rest", pain.Evidence{Kind: "commit", Ref: "abc"}, "",
		pain.Actor{Person: "robin", Origin: c}); err != nil {
		t.Fatal(err)
	}
	var withOrigin int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM pain_events WHERE pain_id=? AND origin_id IS NOT NULL`, pd.Pain.ID).Scan(&withOrigin); err != nil || withOrigin != 2 {
		t.Fatalf("pain events with origin = %d (%v), want 2", withOrigin, err)
	}

	room := RoomKeyForProject(originProject)
	if _, err := st.AppendCoordMessage(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room,
		SenderExternalID: "sess-a", ClientID: "c-1", Body: "mit", OriginContext: c}); err != nil {
		t.Fatal(err)
	}
	if originOfRow(t, st, "coord_messages", "client_id='c-1'") == nil {
		t.Fatal("room message has no origin")
	}
}

func originTitles(t *testing.T, st *Store, f originctx.Filter) string {
	t.Helper()
	ks, err := st.SearchKnowledgeWith("suchbegriff", KnowledgeSearch{Axes: scope.Axes{Project: originProject}, Union: true, Origin: f, NoRecord: true})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, k := range ks {
		out = append(out, k.Title)
	}
	return strings.Join(sortedStrings(out), ",")
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// Kriterium 3: "Wissen aus internal/store, letzte 14 Tage" liefert nur passende
// Einträge.
func TestKnowledgeSearchFiltersByTimeAndArea(t *testing.T) {
	st := originStore(t)
	day := func(daysAgo int) string {
		return time.Now().UTC().Add(-time.Duration(daysAgo) * 24 * time.Hour).Format(time.RFC3339)
	}
	add := func(title string, at string, areas ...string) {
		var c *originctx.Context
		if len(areas) > 0 {
			c = &originctx.Context{SessionRef: "s", CapturedAt: at}
			for _, a := range areas {
				c.Areas = append(c.Areas, originctx.Area{Path: a, Weight: 3})
			}
		}
		if _, err := st.InsertKnowledge(Knowledge{Type: "note", Title: title, Body: "gemeinsamer suchbegriff " + title,
			Scope: scope.Axes{Project: originProject}, Person: "robin", OriginContext: c}); err != nil {
			t.Fatal(err)
		}
	}
	add("store-neu", day(3), "internal/store")
	add("store-alt", day(30), "internal/store")
	add("web-neu", day(2), "internal/web")
	add("store-und-web", day(1), "internal/web", "internal/store")
	add("storex-neu", day(1), "internal/storex")
	add("ohne-kontext", day(0))

	resolve := func(s originctx.Spec) originctx.Filter {
		t.Helper()
		f, err := s.Resolve(time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	if got := originTitles(t, st, originctx.Filter{}); got != "ohne-kontext,store-alt,store-neu,store-und-web,storex-neu,web-neu" {
		t.Fatalf("no filter: %q", got)
	}
	if got := originTitles(t, st, resolve(originctx.Spec{Since: "14d", Areas: []string{"internal/store"}})); got != "store-neu,store-und-web" {
		t.Fatalf("store, last 14 days: %q (internal/storex must not match, an old entry must not match)", got)
	}
	if got := originTitles(t, st, resolve(originctx.Spec{Areas: []string{"internal/store", "internal/web"}})); got != "store-alt,store-neu,store-und-web,web-neu" {
		t.Fatalf("two areas are alternatives: %q", got)
	}
	// Ein Filter unterhalb des Bereichs trifft den Bereich, der Datei umfasst.
	if got := originTitles(t, st, resolve(originctx.Spec{Since: "14d", Areas: []string{"internal/web/handlers.go"}})); got != "store-und-web,web-neu" {
		t.Fatalf("file below an area: %q", got)
	}
	// Ohne Kontext zählt der Zeitpunkt des Eintrags (heute), ein Bereich trifft nicht.
	if got := originTitles(t, st, resolve(originctx.Spec{Since: "1d"})); !strings.Contains(got, "ohne-kontext") {
		t.Fatalf("an entry without context counts by creation time: %q", got)
	}
	if got := originTitles(t, st, resolve(originctx.Spec{Areas: []string{"internal"}})); strings.Contains(got, "ohne-kontext") || !strings.Contains(got, "storex-neu") {
		t.Fatalf("area internal: %q", got)
	}
	until := time.Now().UTC().Add(-10 * 24 * time.Hour).Format("2006-01-02")
	if got := originTitles(t, st, resolve(originctx.Spec{Until: until})); got != "store-alt" {
		t.Fatalf("until %s: %q", until, got)
	}
}

func TestGhostAndRequestSearchFilterByOrigin(t *testing.T) {
	st := originStore(t)
	old := &originctx.Context{SessionRef: "s", CapturedAt: "2026-01-05T10:00:00Z", Areas: []originctx.Area{{Path: "internal/web", Weight: 3}}}
	recent := &originctx.Context{SessionRef: "s", CapturedAt: time.Now().UTC().Format(time.RFC3339), Areas: []originctx.Area{{Path: "internal/store", Weight: 3}}}
	for path, c := range map[string]*originctx.Context{"internal/store/a.go": recent, "internal/web/b.go": old} {
		if _, err := st.PutGhostFile(GhostFile{Project: originProject, Path: path, Kind: "file", Description: "suchbegriff datei", OriginContext: c}); err != nil {
			t.Fatal(err)
		}
	}
	f, err := originctx.Spec{Since: "7d", Areas: []string{"internal/store"}}.Resolve(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	gs, err := st.SearchGhostFilesWith("suchbegriff", GhostSearch{Project: originProject, Origin: f})
	if err != nil || len(gs) != 1 || gs[0].Path != "internal/store/a.go" || gs[0].OriginContext.Main() != "internal/store" {
		t.Fatalf("ghost search = %+v (%v)", gs, err)
	}
	if gs, _ := st.SearchGhostFilesWith("suchbegriff", GhostSearch{Project: originProject}); len(gs) != 2 {
		t.Fatalf("no filter finds both, got %d", len(gs))
	}

	for title, c := range map[string]*originctx.Context{"Neuer": recent, "Alter": old} {
		if _, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: title,
			Scope: scope.Axes{Project: originProject}, Person: "robin"}, Criteria: []string{"x"}, Origin: c}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := st.SearchRequestsWith(requestdomain.SearchFilter{Scope: scope.Axes{Project: originProject}, Origin: f}, SearchAuto)
	if err != nil || len(page.Results) != 1 || page.Results[0].Request.Title != "Neuer" || page.Results[0].OriginContext.Main() != "internal/store" {
		t.Fatalf("request search = %+v (%v)", page.Results, err)
	}
}

// Kriterium 4: nachtragen, wo Session und Pfaddaten da sind, sonst leer lassen.
func TestBackfillFillsWhatItCanAndLeavesTheRestEmpty(t *testing.T) {
	st := originStore(t)
	if _, err := st.UpsertSession(Session{Harness: "claude", ExternalID: "sess-b", CWD: "/work/repo", Scope: scope.Axes{Project: originProject}}); err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	after := time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339)
	recordActivity(t, st, "sess-b", before, map[string]bool{"/work/repo/internal/store/a.go": true})
	recordActivity(t, st, "sess-b", after, map[string]bool{"/work/repo/internal/web/later.go": true})

	known, _ := st.InsertKnowledge(Knowledge{Type: "note", Title: "mit Session", Body: "b", Person: "robin", SessionRef: "sess-b"})
	unknown, _ := st.InsertKnowledge(Knowledge{Type: "note", Title: "fremde Session", Body: "b", Person: "robin", SessionRef: "sess-unbekannt"})
	plain, _ := st.InsertKnowledge(Knowledge{Type: "note", Title: "ohne Session", Body: "b", Person: "robin"})
	edited, _ := st.InsertKnowledge(Knowledge{Type: "note", Title: "bearbeitet", Body: "b", Person: "robin", SessionRef: "sess-b"})
	if err := st.UpdateKnowledgeBy(edited, map[string]string{"body": "neu"}, "lena"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutGhostFile(GhostFile{Project: originProject, Path: "internal/store/a.go", Kind: "file", Description: "d", SessionRef: "sess-b"}); err != nil {
		t.Fatal(err)
	}
	dry, err := st.BackfillOrigins(true)
	if err != nil {
		t.Fatal(err)
	}
	if !dry.DryRun || dry.Knowledge.Filled != 1 || dry.Knowledge.Empty != 1 || dry.Knowledge.Candidates != 2 || dry.Ghosts.Filled != 1 {
		t.Fatalf("dry run = %+v", dry)
	}
	if originOfRow(t, st, "knowledge", "id=?", known) != nil {
		t.Fatal("a dry run must not write")
	}

	rep, err := st.BackfillOrigins(false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Knowledge.Filled != 1 || rep.Ghosts.Filled != 1 {
		t.Fatalf("report = %+v", rep)
	}
	k, _ := st.KnowledgeByID(known)
	if k.OriginContext == nil || !k.OriginContext.Backfilled || originAreas(k.OriginContext) != "internal/store" {
		t.Fatalf("backfilled context = %+v (activity after the entry must not count)", k.OriginContext)
	}
	if k.OriginContext.CapturedAt != k.CreatedAt {
		t.Fatalf("captured_at = %s, want the entry time %s", k.OriginContext.CapturedAt, k.CreatedAt)
	}
	for name, id := range map[string]int64{"unknown session": unknown, "no session": plain, "edited": edited} {
		if originOfRow(t, st, "knowledge", "id=?", id) != nil {
			t.Fatalf("%s must stay empty, not guessed", name)
		}
	}

	// Idempotent: ein zweiter Lauf findet nichts mehr zu füllen und ändert nichts.
	var rows int
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM origin_contexts`).Scan(&rows)
	again, err := st.BackfillOrigins(false)
	if err != nil || again.Knowledge.Filled != 0 || again.Ghosts.Filled != 0 {
		t.Fatalf("second run = %+v (%v)", again, err)
	}
	var rows2 int
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM origin_contexts`).Scan(&rows2)
	if rows != rows2 {
		t.Fatalf("second run wrote %d context rows", rows2-rows)
	}
}

func TestBackfillCoversRequestsAndPainsBySessionId(t *testing.T) {
	st := originStore(t)
	sid, err := st.UpsertSession(Session{Harness: "claude", ExternalID: "sess-c", CWD: "/work/repo", Scope: scope.Axes{Project: originProject}})
	if err != nil {
		t.Fatal(err)
	}
	recordActivity(t, st, "sess-c", time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), map[string]bool{"/work/repo/cmd/ctx/x.go": true})
	d, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "R",
		Scope: scope.Axes{Project: originProject}, Person: "robin", SessionRef: "sess-c"}, Criteria: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	pa := st.Access(Principal{ID: "person:1", Label: "robin"})
	pd, err := pa.CreatePain(CreatePainInput{Project: originProject, Title: "P", AuthorID: 1, Actor: pain.Actor{Person: "robin", SessionID: sid}})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := st.BackfillOrigins(false)
	if err != nil || rep.Requests.Filled != 1 || rep.Pains.Filled != 1 {
		t.Fatalf("report = %+v (%v)", rep, err)
	}
	if originOfRow(t, st, "request_activity", "request_id=? AND kind='request.created'", d.Request.ID) == nil ||
		originOfRow(t, st, "pain_events", "pain_id=? AND kind='created'", pd.Pain.ID) == nil {
		t.Fatal("request or pain stayed empty")
	}
}

func TestPainEventsStayAppendOnlyExceptTheOriginNachtrag(t *testing.T) {
	st := originStore(t)
	pa := st.Access(Principal{ID: "person:1", Label: "robin"})
	d, err := pa.CreatePain(CreatePainInput{Project: originProject, Title: "P", AuthorID: 1, Actor: pain.Actor{Person: "robin"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`UPDATE pain_events SET note='x'`, `UPDATE pain_events SET created_at='2000-01-01T00:00:00Z'`, `DELETE FROM pain_events`} {
		if _, err := st.db.Exec(q); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Errorf("%s: want append-only error, got %v", q, err)
		}
	}
	res, err := st.db.Exec(`INSERT INTO origin_contexts(session_ref,captured_at) VALUES('s','2026-11-01T10:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	oid, _ := res.LastInsertId()
	if _, err := st.db.Exec(`UPDATE pain_events SET origin_id=? WHERE pain_id=?`, oid, d.Pain.ID); err != nil {
		t.Fatalf("the origin may be set once: %v", err)
	}
	if _, err := st.db.Exec(`UPDATE pain_events SET origin_id=NULL WHERE pain_id=?`, d.Pain.ID); err == nil {
		t.Fatal("a set origin may not be changed")
	}
	// Die Migration lässt den engeren Trigger beim nächsten Start in Ruhe.
	if err := ensureOriginContexts(st.db); err != nil {
		t.Fatal(err)
	}
}
