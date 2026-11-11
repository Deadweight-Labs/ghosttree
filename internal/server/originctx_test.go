package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const originProject = "github.com/dw/origins"

type originEnv struct {
	st    *store.Store
	srv   *httptest.Server
	token string
}

func newOriginEnv(t *testing.T) originEnv {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	token, err := st.AddPerson("robin")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)
	return originEnv{st: st, srv: srv, token: token}
}

func (e originEnv) activity(t *testing.T, session string, account int64, paths map[string]bool) {
	t.Helper()
	if _, err := e.st.UpsertSession(store.Session{Harness: "claude", ExternalID: session, CWD: "/work/repo",
		Scope: scope.Axes{Project: originProject}, AccountID: account}); err != nil {
		t.Fatal(err)
	}
	var ev []store.PathActivity
	for p, w := range paths {
		ev = append(ev, store.PathActivity{Project: originProject, SessionExternalID: session, Checkout: "/work/repo",
			Tool: "Edit", Path: p, Writes: w, Quality: store.ActivityIntent,
			At: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339), AccountID: account})
	}
	if err := e.st.RecordPathActivity(ev); err != nil {
		t.Fatal(err)
	}
}

func (e originEnv) client(session string) *client.Client {
	c := client.New(config.Config{ServerURL: e.srv.URL, Token: e.token})
	c.SetOriginSession(session)
	return c
}

// Der ganze Weg (Kriterien 1 und 3): der Client schickt nur die Session, der
// Server friert die Bereiche aus path_activity ein, die Suche filtert danach.
func TestWritesFreezeTheOriginContextFromTheSessionActivity(t *testing.T) {
	e := newOriginEnv(t)
	e.activity(t, "sess-1", 0, map[string]bool{"/work/repo/internal/store/a.go": true, "/work/repo/internal/web/b.go": false})
	c := e.client("sess-1")
	ax := scope.Axes{Project: originProject}

	k, err := c.Remember(store.Knowledge{Type: "note", Title: "aus dem Store", Body: "gemeinsames stichwort store", Scope: ax}, ax)
	if err != nil {
		t.Fatal(err)
	}
	if k.OriginContext == nil || k.OriginContext.SessionRef != "sess-1" || k.OriginContext.Main() != "internal/store" || len(k.OriginContext.Areas) != 2 {
		t.Fatalf("origin context = %+v", k.OriginContext)
	}
	// Ohne Session am Client: kein Kontext, der Schreibzugriff gelingt.
	plain := client.New(config.Config{ServerURL: e.srv.URL, Token: e.token})
	k2, err := plain.Remember(store.Knowledge{Type: "note", Title: "ohne Session", Body: "gemeinsames stichwort ohne", Scope: ax}, ax)
	if err != nil || k2.OriginContext != nil {
		t.Fatalf("a client without a session got a context: %+v (%v)", k2.OriginContext, err)
	}

	titles := func(f client.AnchorFilter) string {
		t.Helper()
		res, err := c.SearchAnchored("stichwort", "knowledge", ax, 20, true, f)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, k := range res.Knowledge {
			out = append(out, k.Title)
		}
		return strings.Join(out, ",")
	}
	if got := titles(client.AnchorFilter{Origin: client.OriginFilter{Since: "14d", Areas: []string{"internal/store"}}}); got != "aus dem Store" {
		t.Errorf("store, last 14 days: %q", got)
	}
	if got := titles(client.AnchorFilter{Origin: client.OriginFilter{Areas: []string{"internal/store", "docs"}}}); got != "aus dem Store" {
		t.Errorf("alternatives: %q", got)
	}
	if got := titles(client.AnchorFilter{Origin: client.OriginFilter{Areas: []string{"cmd"}}}); got != "" {
		t.Errorf("other area: %q", got)
	}
	if got := titles(client.AnchorFilter{Origin: client.OriginFilter{Until: "2020-01-01"}}); got != "" {
		t.Errorf("until in the past: %q", got)
	}
	if got := titles(client.AnchorFilter{Origin: client.OriginFilter{Since: time.Now().UTC().Format("2006-01-02")}}); !strings.Contains(got, "ohne Session") || !strings.Contains(got, "aus dem Store") {
		t.Errorf("since today (creation time stands in without a context): %q", got)
	}

	// Beschreibung: der Pfad der Beschreibung zählt als ausdrücklich genannt, auch ohne Session.
	if _, err := plain.PutGhost(store.GhostFile{Project: originProject, Path: "cmd/ctx/main.go", Description: "beschreibt die datei", ContentSHA: "x"}); err != nil {
		t.Fatal(err)
	}
	gs, err := plain.SearchGhostsAnchored("datei", originProject, 10, client.AnchorFilter{Origin: client.OriginFilter{Areas: []string{"cmd/ctx"}}})
	if err != nil || len(gs) != 1 || gs[0].OriginContext == nil || gs[0].OriginContext.Paths[0].Kind != "named" {
		t.Fatalf("ghost search by area: %+v (%v)", gs, err)
	}
	if gs, err = plain.SearchGhostsAnchored("datei", originProject, 10, client.AnchorFilter{Origin: client.OriginFilter{Areas: []string{"internal"}}}); err != nil || len(gs) != 0 {
		t.Fatalf("ghost search by another area: %+v (%v)", gs, err)
	}

	// Request: das Anlegen trägt den Kontext, die Suche zeigt ihn und filtert danach.
	d, err := c.CreateRequest(requestdomain.CreateInput{
		Request:  requestdomain.Request{Type: "feature", Title: "aus dem Store", Scope: ax, Origin: "agent"},
		Criteria: []string{"eins"},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.SearchAnchored("Store", "requests", scope.Axes{Project: originProject}, 10, false,
		client.AnchorFilter{Origin: client.OriginFilter{Areas: []string{"internal/store"}}})
	if err != nil || len(res.Requests) != 1 || res.Requests[0].Request.ID != d.Request.ID || res.Requests[0].OriginContext.Main() != "internal/store" {
		t.Fatalf("request search by area: %+v (%v)", res.Requests, err)
	}
	res, err = c.SearchAnchored("Store", "requests", scope.Axes{Project: originProject}, 10, false,
		client.AnchorFilter{Origin: client.OriginFilter{Areas: []string{"cmd"}}})
	if err != nil || len(res.Requests) != 0 {
		t.Fatalf("request search by another area: %+v (%v)", res.Requests, err)
	}
}

// Eine Session, die dem Schreiber nicht gehört, ergibt keinen Kontext: er wäre
// aus fremder Aktivität gebaut. Ein Feld origin_context im Body zählt nie.
func TestOriginContextIsNeverTakenFromAForeignSessionOrTheBody(t *testing.T) {
	e := newOriginEnv(t)
	if _, err := e.st.AddPerson("lena"); err != nil {
		t.Fatal(err)
	}
	e.activity(t, "sess-lena", 2, map[string]bool{"/work/repo/internal/secret/a.go": true})
	c := e.client("sess-lena")
	ax := scope.Axes{Project: originProject}
	k, err := c.Remember(store.Knowledge{Type: "note", Title: "t", Body: "x", Scope: ax}, ax)
	if err != nil || k.OriginContext != nil {
		t.Fatalf("a foreign session produced a context: %+v (%v)", k.OriginContext, err)
	}

	body := `{"type":"note","title":"t2","body":"x","scope":{"project":"` + originProject + `"},` +
		`"origin_context":{"session_ref":"sess-x","areas":[{"path":"gefaelscht","weight":99}]}}`
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/knowledge", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+e.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var n int
	if err := e.st.DB().QueryRow(`SELECT COUNT(*) FROM origin_contexts`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("origin rows = %d (%v): nothing may be stored from a body or a foreign session", n, err)
	}
}

func TestAnOriginFilterThatIsNotUsableIsRejected(t *testing.T) {
	e := newOriginEnv(t)
	paths := []string{"/api/search?q=x&kind=knowledge&project=" + originProject + "&", "/api/ghosts/search?q=x&project=" + originProject + "&", "/api/requests?project=" + originProject + "&"}
	for _, path := range paths {
		req, _ := http.NewRequest("GET", e.srv.URL+path+"since=7d&until=2099-01-01&area=internal/store", nil)
		req.Header.Set("Authorization", "Bearer "+e.token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: a usable filter answered %d", path, resp.StatusCode)
		}
	}
	for _, q := range []string{"since=gestern", "until=7x", "since=2026-11-05&until=2026-11-01", "area=/etc", "area=../x"} {
		for _, path := range paths {
			req, _ := http.NewRequest("GET", e.srv.URL+path+q, nil)
			req.Header.Set("Authorization", "Bearer "+e.token)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("%s%s: status %d, want 400: a filter that is ignored answers a different question", path, q, resp.StatusCode)
			}
		}
	}
}

// Rechtetest, live: Konto B schreibt mit der session_ref von Konto A (im Body
// von Wissen, Beschreibung und Auftrag). Der Eintrag bekommt keine Pfade von A,
// und die fremde Verweisung bleibt nicht am Eintrag stehen.
func TestForeignSessionRefInTheBodyGivesNoPathsAndIsDropped(t *testing.T) {
	e := newOriginEnv(t) // Token gehört robin, Konto 1
	if _, err := e.st.AddPerson("lena"); err != nil {
		t.Fatal(err)
	}
	e.activity(t, "sess-lena", 2, map[string]bool{"/work/repo/internal/secret/a.go": true})
	c := e.client("")
	ax := scope.Axes{Project: originProject}

	k, err := c.Remember(store.Knowledge{Type: "note", Title: "fremd", Body: "gemeinsames stichwort", Scope: ax, SessionRef: "sess-lena"}, ax)
	if err != nil {
		t.Fatal(err)
	}
	if k.OriginContext != nil || k.SessionRef != "" {
		t.Fatalf("knowledge kept the foreign session: context=%+v ref=%q", k.OriginContext, k.SessionRef)
	}
	if _, err := c.PutGhost(store.GhostFile{Project: originProject, Path: "internal/secret/a.go", Description: "d", ContentSHA: "x", SessionRef: "sess-lena"}); err != nil {
		t.Fatal(err)
	}
	gs, err := c.SearchGhostsAnchored("a.go", originProject, 10, client.AnchorFilter{})
	if err != nil || len(gs) != 1 {
		t.Fatalf("ghost = %+v (%v)", gs, err)
	}
	if gs[0].SessionRef != "" {
		t.Fatalf("the ghost kept the foreign session_ref %q", gs[0].SessionRef)
	}
	for _, p := range gs[0].OriginContext.Paths {
		if strings.Contains(p.Path, "secret") && p.Kind != "named" {
			t.Fatalf("the ghost took a path from Lena's session: %+v", p)
		}
	}
	if len(gs[0].OriginContext.Paths) != 1 || gs[0].OriginContext.SessionRef != "" {
		t.Fatalf("only the named path of the description may remain: %+v", gs[0].OriginContext)
	}
	d, err := c.CreateRequest(requestdomain.CreateInput{
		Request:  requestdomain.Request{Type: "feature", Title: "fremd", Scope: ax, Origin: "agent", SessionRef: "sess-lena"},
		Criteria: []string{"eins"},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.SearchAnchored("fremd", "requests", ax, 10, false, client.AnchorFilter{Origin: client.OriginFilter{Areas: []string{"internal/secret"}}})
	if err != nil || len(res.Requests) != 0 {
		t.Fatalf("area search finds the request of a foreign session: %+v (%v)", res.Requests, err)
	}
	_ = d
}

// Der Eigentümer der Session bekommt seinen Kontext auch über die session_ref
// im Body, ohne Header (so schreiben ältere Clients).
func TestOwnSessionRefInTheBodyGivesTheContext(t *testing.T) {
	e := newOriginEnv(t)
	e.activity(t, "sess-own", 0, map[string]bool{"/work/repo/internal/store/a.go": true})
	c := e.client("")
	ax := scope.Axes{Project: originProject}
	k, err := c.Remember(store.Knowledge{Type: "note", Title: "eigen", Body: "x", Scope: ax, SessionRef: "sess-own"}, ax)
	if err != nil || k.OriginContext == nil || k.OriginContext.Main() != "internal/store" || k.SessionRef != "sess-own" {
		t.Fatalf("own session by body: %+v ref=%q (%v)", k.OriginContext, k.SessionRef, err)
	}
}
