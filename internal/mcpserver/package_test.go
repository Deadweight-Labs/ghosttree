package mcpserver

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const pkgProject = "github.com/x/y"

func pkgServer(t *testing.T, branch string, lineage ...string) (*Server, *store.Store) {
	t.Helper()
	c, st := newTestClient(t)
	s := &Server{client: c, ctxAxes: scope.Axes{Project: pkgProject, Branch: "main", Machine: "workstation-a"}}
	s.SetLive(func() Live { return Live{Branch: branch, Lineage: lineage, CWD: "/work/y"} })
	return s, st
}

func pkgRequests(t *testing.T, st *store.Store, titles ...string) []requestdomain.Detail {
	t.Helper()
	var out []requestdomain.Detail
	for _, title := range titles {
		d, err := st.CreateRequest(requestdomain.CreateInput{
			Request:  requestdomain.Request{Type: "feature", Title: title, Scope: scope.Axes{Project: pkgProject}, Person: "alice"},
			Criteria: []string{title + ": erstes", title + ": zweites"},
		})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}

// Anlegen, Erweitern, Abfragen nach Branch (mit Erbe) und Prüfen: der Weg, den
// ein Agent über die Werkzeuge geht.
func TestPackageToolsCreateExtendAndReview(t *testing.T) {
	ctx := context.Background()
	s, st := pkgServer(t, "feat/pkg", "main")
	reqs := pkgRequests(t, st, "Kern", "Zusatz")

	// Ohne Paket: eine Antwort, kein Fehler.
	res, out, err := s.handlePackageGet(ctx, nil, PackageGetInput{})
	if err != nil || out.Found || !strings.Contains(text(t, res), "No open work package") {
		t.Fatalf("no package: %v %+v %s", err, out, text(t, res))
	}

	// Der Branch kommt aus dem Arbeitsverzeichnis jetzt, nicht aus dem Start (main).
	_, created, err := s.handlePackageUpdate(ctx, nil, PackageUpdateInput{Action: "create", Name: "Das Paket",
		Requests: []PackageRequestRef{{RequestID: reqs[0].Request.ID}}})
	if err != nil || created.Branch != "feat/pkg" || created.PackageID == 0 || len(created.Members) != 1 {
		t.Fatalf("create = %+v %v", created, err)
	}
	// "add" ohne package_id meint das Paket des aktuellen Branches.
	_, added, err := s.handlePackageUpdate(ctx, nil, PackageUpdateInput{Action: "add", Reason: "gehört dazu",
		Requests: []PackageRequestRef{{RequestID: reqs[1].Request.ID, Role: "optional"}}})
	if err != nil || len(added.Members) != 2 {
		t.Fatalf("add = %+v %v", added, err)
	}
	if _, _, err := s.handlePackageUpdate(ctx, nil, PackageUpdateInput{Action: "remove", RequestID: reqs[1].Request.ID}); err == nil || !strings.Contains(err.Error(), "removal_reason_required") && !strings.Contains(err.Error(), "why") {
		t.Fatalf("remove without a reason: %v", err)
	}
	if _, _, err := s.handlePackageUpdate(ctx, nil, PackageUpdateInput{Action: "bogus"}); err == nil {
		t.Fatal("an unknown action was accepted")
	}

	res, got, err := s.handlePackageGet(ctx, nil, PackageGetInput{})
	if err != nil || !got.Found {
		t.Fatalf("get = %+v %v", got, err)
	}
	for _, want := range []string{"PKG-1", "feat/pkg", "pflicht", "optional", "Kern", "2 open, 0 met"} {
		if !strings.Contains(text(t, res), want) {
			t.Errorf("package_get misses %q:\n%s", want, text(t, res))
		}
	}

	// Ein abgezweigter Branch sieht dasselbe Paket, als geerbt gekennzeichnet.
	child := &Server{client: s.client, ctxAxes: s.ctxAxes}
	child.SetLive(func() Live { return Live{Branch: "feat/pkg-child", Lineage: []string{"main", "feat/pkg"}} })
	res, got, err = child.handlePackageGet(ctx, nil, PackageGetInput{})
	if err != nil || !got.Found || got.Detail.InheritedFrom != "feat/pkg" || !strings.Contains(text(t, res), "inherited") {
		t.Fatalf("child = %+v %v\n%s", got, err, text(t, res))
	}

	// Prüfen: der Nachweis trägt seinen Git-Anker, Offenes bestimmt das Urteil.
	anchor := &gitanchor.Anchor{Commit: "0123456789abcdef0123456789abcdef01234567", Branch: "feat/pkg", Dirty: true}
	d0 := reqs[0]
	if err := st.SetCriterionState(d0.Criteria[0].ID, "met", requestdomain.Evidence{Kind: "test", Ref: "TestKern", Person: "alice", GitAnchor: anchor}); err != nil {
		t.Fatal(err)
	}
	res, _, err = s.handlePackageReview(ctx, nil, PackageGetInput{})
	if err != nil {
		t.Fatal(err)
	}
	review := text(t, res)
	for _, want := range []string{
		"NOT READY: 1 mandatory criteria open", // nur Pflicht zählt, das optionale Zusatz-Request nicht
		"evidence test: TestKern (@ 0123456789 on feat/pkg, uncommitted changes)",
		"AC-" + itoa(d0.Request.ID) + ".1 [met]", "AC-" + itoa(d0.Request.ID) + ".2 [open]",
		"## optional requests", "criterion_reopen",
	} {
		if !strings.Contains(review, want) {
			t.Errorf("review misses %q:\n%s", want, review)
		}
	}
	if err := st.SetCriterionState(d0.Criteria[1].ID, "waived", requestdomain.Evidence{Kind: "decision", Ref: "kommt später", Person: "alice"}); err != nil {
		t.Fatal(err)
	}
	res, _, _ = s.handlePackageReview(ctx, nil, PackageGetInput{})
	review = text(t, res)
	if !strings.Contains(review, "READY: every mandatory criterion") || !strings.Contains(review, "no git anchor") {
		t.Errorf("review after the last criterion:\n%s", review)
	}

	// Wiederöffnen: Begründung ist Pflicht, danach ist das Urteil wieder offen,
	// und die alten Nachweise stehen als vor dem Wiederöffnen entstanden da.
	reopen := RequestProgressInput{RequestID: d0.Request.ID, Action: "criterion_reopen", CriterionID: d0.Criteria[0].ID}
	if _, _, err := s.handleRequestProgress(ctx, nil, reopen); err == nil {
		t.Fatal("a criterion was reopened without a reason")
	}
	reopen.Reason = "TestKern prüft den Rückgabewert nicht"
	if _, progressed, err := s.handleRequestProgress(ctx, nil, reopen); err != nil || progressed.Criterion == nil || progressed.Criterion.State != "open" {
		t.Fatalf("reopen = %+v %v", progressed, err)
	}
	res, _, _ = s.handlePackageReview(ctx, nil, PackageGetInput{})
	review = text(t, res)
	if !strings.Contains(review, "NOT READY: 1 mandatory criteria open") || !strings.Contains(review, "[from before it was reopened]") {
		t.Errorf("review after reopening:\n%s", review)
	}

	// Schließen nimmt das Paket aus der Abfrage nach Branch.
	if _, _, err := s.handlePackageUpdate(ctx, nil, PackageUpdateInput{Action: "set", State: "closed", PR: 12}); err != nil {
		t.Fatal(err)
	}
	if _, out, err := s.handlePackageGet(ctx, nil, PackageGetInput{}); err != nil || out.Found {
		t.Fatalf("a closed package is still found: %+v %v", out, err)
	}
	// Per PR ist es auffindbar? Geschlossene Pakete werden nur über die Nummer geladen.
	if _, out, err := s.handlePackageGet(ctx, nil, PackageGetInput{PackageID: created.PackageID}); err != nil || !out.Found || out.Detail.PR != 12 {
		t.Fatalf("by id = %+v %v", out, err)
	}
}

func TestPackageToolsImportRelationsWithDryRun(t *testing.T) {
	ctx := context.Background()
	s, st := pkgServer(t, "feat/imp")
	reqs := pkgRequests(t, st, "Eins", "Zwei")
	for _, d := range reqs {
		if _, err := st.AddRequestRelation(d.Request.ID, requestdomain.Relation{Kind: "external", ExternalRef: "branch:" + pkgProject + "@feat/imp"}, "alice"); err != nil {
			t.Fatal(err)
		}
	}
	_, dry, err := s.handlePackageUpdate(ctx, nil, PackageUpdateInput{Action: "import_relations", DryRun: true})
	if err != nil || dry.Import == nil || !dry.Import.DryRun || dry.Import.Added != 2 {
		t.Fatalf("dry = %+v %v", dry.Import, err)
	}
	if _, out, _ := s.handlePackageGet(ctx, nil, PackageGetInput{}); out.Found {
		t.Fatal("the dry run created a package")
	}
	_, done, err := s.handlePackageUpdate(ctx, nil, PackageUpdateInput{Action: "import_relations"})
	if err != nil || done.Import.Added != 2 || len(done.Import.PackagesCreated) != 1 {
		t.Fatalf("done = %+v %v", done.Import, err)
	}
	if _, out, _ := s.handlePackageGet(ctx, nil, PackageGetInput{}); !out.Found || len(out.Detail.Members) != 2 {
		t.Fatalf("package after the import = %+v", out)
	}
}

func TestPackageToolsAreDiscoverable(t *testing.T) {
	c, _ := newTestClient(t)
	ghosttree := &Server{client: c, ctxAxes: scope.Axes{Project: pkgProject}}
	protocolServer := mcp.NewServer(&mcp.Implementation{Name: "ghosttree-test", Version: "test"}, nil)
	ghosttree.Register(protocolServer)
	protocolClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	serverSession, err := protocolServer.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	clientSession, err := protocolClient.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	listed, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"package_get", "package_update", "package_review"}
	for _, tool := range listed.Tools {
		if slices.Contains(want, tool.Name) {
			if tool.InputSchema == nil || tool.OutputSchema == nil || tool.Annotations == nil {
				t.Errorf("tool %s missing schema or annotations", tool.Name)
			}
			want = slices.DeleteFunc(want, func(n string) bool { return n == tool.Name })
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing tools: %v", want)
	}
}

// PAIN-57: der Agent legt mitten in der Sitzung einen Branch an. Die Session-Zeile
// trägt den Branch vom Start (der Sammler liest ihn einmal), der MCP-Prozess ebenso.
// request_start_work ohne Session-Nummer muss die eigene Session trotzdem finden.
func TestStartWorkFindsTheOwnSessionAfterABranchSwitch(t *testing.T) {
	ctx := context.Background()
	mk := func(s *Server, st *store.Store, ext, cwd, branch string) {
		t.Helper()
		if _, err := st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: ext, CWD: cwd, Scope: scope.Axes{Project: pkgProject, Branch: branch, Machine: "workstation-a"}}); err != nil {
			t.Fatal(err)
		}
	}
	start := func(t *testing.T, s *Server, st *store.Store, id int64) (int64, error) {
		t.Helper()
		_, out, err := s.handleRequestStartWork(ctx, nil, RequestStartWorkInput{RequestID: id})
		if err != nil {
			return 0, err
		}
		return out.Work.SessionID, nil
	}
	sessionID := func(t *testing.T, st *store.Store, ext string) int64 {
		t.Helper()
		sessions, err := st.ListSessions(scope.Axes{Project: pkgProject}, 20)
		if err != nil {
			t.Fatal(err)
		}
		for _, sess := range sessions {
			if sess.ExternalID == ext {
				return sess.ID
			}
		}
		t.Fatalf("session %s not found", ext)
		return 0
	}

	t.Run("by the harness session id", func(t *testing.T) {
		s, st := pkgServer(t, "feat/new")
		s.ctxAxes.Branch = "feat/new" // der MCP-Prozess startete erst nach dem Wechsel
		req := pkgRequests(t, st, "r")[0].Request.ID
		mk(s, st, "mine", "/elsewhere", "main")
		mk(s, st, "newer-neighbour", "/work/y", "feat/new") // neuer und passt auf Branch und Pfad
		s.sessionRef = "mine"
		got, err := start(t, s, st, req)
		if err != nil || got != sessionID(t, st, "mine") {
			t.Fatalf("session %d, %v; want the one named by the harness id", got, err)
		}
	})
	t.Run("by the working directory when the branch no longer matches", func(t *testing.T) {
		s, st := pkgServer(t, "feat/new")
		s.ctxAxes.Branch = "feat/new" // der MCP-Prozess startete erst nach dem Wechsel
		req := pkgRequests(t, st, "r")[0].Request.ID
		mk(s, st, "mine", "/work/y", "main") // Branch vom Start
		mk(s, st, "neighbour", "/work/other", "feat/new")
		got, err := start(t, s, st, req)
		if err != nil || got != sessionID(t, st, "mine") {
			t.Fatalf("session %d, %v; want the one in this working directory", got, err)
		}
	})
	t.Run("by the old branch rule without a git state", func(t *testing.T) {
		c, st := newTestClient(t)
		s := &Server{client: c, ctxAxes: scope.Axes{Project: pkgProject, Branch: "main", Machine: "workstation-a"}}
		req := pkgRequests(t, st, "r")[0].Request.ID
		mk(s, st, "elsewhere", "/x", "feat/other")
		mk(s, st, "mine", "/y", "main")
		if got, err := start(t, s, st, req); err != nil || got != sessionID(t, st, "mine") {
			t.Fatalf("session %d, %v", got, err)
		}
	})
	t.Run("no match is an error that says what was compared", func(t *testing.T) {
		s, st := pkgServer(t, "feat/new")
		req := pkgRequests(t, st, "r")[0].Request.ID
		mk(s, st, "stranger", "/work/other", "feat/other")
		if _, err := start(t, s, st, req); err == nil || !strings.Contains(err.Error(), "working directory") {
			t.Fatalf("error = %v", err)
		}
	})
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
