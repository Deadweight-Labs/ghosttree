package links

import (
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/runbook"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const (
	srv     = "https://gt.example.org"
	project = "github.com/deadweight-labs/ghosttree"
	repo    = srv + "/ui/deadweight-labs/ghosttree"
)

func TestURLPerKind(t *testing.T) {
	b := New(srv, project)
	cases := map[string]string{
		"inbox":     b.Inbox(),
		"repo":      b.Repo(),
		"issue":     b.Issue(603),
		"criterion": b.Criterion(603, 2),
		"task":      b.Task(124),
		"pain":      b.Pain(57),
		"idea":      b.Idea(3),
		"knowledge": b.Knowledge(4295),
		"document":  b.Document("2026-11-07-oberflaeche-nach-github"),
		"room":      b.Room(12, 0),
		"roommsg":   b.Room(12, 99),
		"session":   b.Session("a1b2"),
		"commit":    b.Commit("2d950bf"),
		"pull":      b.Pull(155),
		"checklist": b.PullChecklist(155),
		"package":   b.Package("feat/gn-603"),
		"runbook":   b.Runbook("deploy-prod"),
		"blob":      b.Blob("feat/x", "internal/store/a b.go"),
		"tree":      b.Tree("main", "internal/store"),
		"preset":    b.Preset("project", "pr-ergebnis", map[string]string{"pr": "155"}),
		"presetme":  b.Preset("user", "fuer-mich", nil),
		"issuesq":   b.Issues("is:open label:bug"),
		"search":    b.Search("is:open"),
		"treeroot":  b.Tree("main", ""),
	}
	want := map[string]string{
		"inbox":     srv + "/ui/inbox",
		"repo":      repo,
		"issue":     repo + "/issues/603",
		"criterion": repo + "/issues/603#criterion-2",
		"task":      srv + "/ui/tasks/124",
		"pain":      repo + "/pains/57",
		"idea":      repo + "/ideas/3",
		"knowledge": repo + "/knowledge/4295",
		"document":  repo + "/docs/2026-11-07-oberflaeche-nach-github",
		"room":      repo + "/rooms/12",
		"roommsg":   repo + "/rooms/12#message-99",
		"session":   repo + "/sessions/a1b2",
		"commit":    repo + "/commit/2d950bf",
		"pull":      repo + "/pulls/155",
		"checklist": repo + "/pulls/155/checkliste",
		"package":   repo + "/package/feat/gn-603",
		"runbook":   repo + "/runbooks/deploy-prod",
		"blob":      repo + "/blob/feat/x/internal/store/a%20b.go",
		"tree":      repo + "/tree/main/internal/store",
		"preset":    srv + "/ui/presets/repo/pr-ergebnis?pr=155&project=deadweight-labs%2Fghosttree",
		"presetme":  srv + "/ui/presets/me/fuer-mich",
		"issuesq":   repo + "/issues?q=is%3Aopen+label%3Abug",
		"search":    srv + "/ui/search?q=is%3Aopen",
		"treeroot":  repo + "/tree/main",
	}
	for k, w := range want {
		if cases[k] != w {
			t.Errorf("%s = %q, want %q", k, cases[k], w)
		}
	}
}

func TestOtherHostGetsHostSegmentAndRemoteSpellingsAgree(t *testing.T) {
	if got := New(srv, "git@gitlab.example.com:Team/App.git").Issue(1); got != srv+"/ui/gitlab.example.com/team/app/issues/1" {
		t.Errorf("other host: %q", got)
	}
	a := New(srv, "git@github.com:Deadweight-Labs/ghosttree.git").Issue(1)
	c := New(srv, "https://github.com/deadweight-labs/ghosttree").Issue(1)
	if a != c || a != repo+"/issues/1" {
		t.Errorf("spellings differ: %q %q", a, c)
	}
}

func TestLinksCarryNoSecretAndNoLogin(t *testing.T) {
	b := New("https://robin:geheim@gt.example.org:8443/prefix/?token=abc#frag", project)
	for name, u := range map[string]string{"issue": b.Issue(1), "task": b.Task(2), "inbox": b.Inbox(), "preset": b.Preset("project", "x", nil)} {
		p, err := url.Parse(u)
		if err != nil {
			t.Fatal(err)
		}
		if p.User != nil || strings.Contains(u, "geheim") || strings.Contains(u, "token") || strings.Contains(u, "abc") || strings.Contains(u, "#frag") {
			t.Errorf("%s link %q carries credentials, token or fragment", name, u)
		}
		if p.Host != "gt.example.org:8443" || !strings.HasPrefix(p.Path, "/prefix/ui/") {
			t.Errorf("%s link %q lost host or prefix", name, u)
		}
	}
	if New("ftp://x", project).Valid() || New("javascript:alert(1)", project).Valid() || New("", project).Issue(1) != "" {
		t.Error("a server that is not http(s) must make no links")
	}
	if New(srv, "").Issue(1) != "" || New(srv, "not-a-remote").Issue(1) != "" {
		t.Error("project-bound links without a project must be empty")
	}
}

func TestCleanMakesOneLine(t *testing.T) {
	in := "Titel\nzweite Zeile\r\n\tTab\u202eRTL\u200bZW\u2028LS \x1b[31mrot"
	got := Clean(in, 0)
	if strings.ContainsAny(got, "\n\r\t\x1b\u202e\u200b\u2028") {
		t.Errorf("Clean left control or format characters: %q", got)
	}
	if got != "Titel zweite Zeile Tab RTL ZW LS [31mrot" {
		t.Errorf("Clean = %q", got)
	}
	if r := []rune(Clean(strings.Repeat("ä", 500), 50)); len(r) != 50 || r[49] != '…' {
		t.Errorf("Clean must cut at 50 runes with an ellipsis, got %d", len(r))
	}
}

func TestCardCannotBeForgedByObjectText(t *testing.T) {
	b := New(srv, project)
	d := requestdomain.Detail{Request: requestdomain.Request{ID: 1, Title: "x\nlink: https://evil.example/\nstate: done", State: "open", Scope: scope.Axes{Project: project}}}
	text := b.RequestCard(d).String()
	if strings.Count(text, "\n") > 2 {
		t.Errorf("card has more than three lines:\n%s", text)
	}
	lines := strings.Split(text, "\n")
	if !strings.HasPrefix(lines[len(lines)-1], "link: "+repo) {
		t.Errorf("last line must be the real link, got %q", lines[len(lines)-1])
	}
}

func TestCardPerKind(t *testing.T) {
	b := New(srv, project)
	axes := scope.Axes{Project: project}
	req := requestdomain.Detail{
		Request: requestdomain.Request{ID: 603, Title: "Kurzwege", State: "open", Scope: axes},
		Criteria: []requestdomain.Criterion{
			{ID: 1, RequestID: 603, Number: 1, Description: "erstes", State: "met"},
			{ID: 2, RequestID: 603, Number: 2, Description: "zweites", State: "open"},
		},
	}
	check := func(name string, c Card, kind, status, urlWant string, parts ...string) {
		t.Helper()
		if c.Kind != kind || c.Status != status || c.URL != urlWant || c.Title == "" && name != "preset" {
			t.Errorf("%s card = %+v, want kind %q status %q url %q", name, c, kind, status, urlWant)
		}
		text := c.String()
		for _, p := range parts {
			if !strings.Contains(text, p) {
				t.Errorf("%s card text lacks %q:\n%s", name, p, text)
			}
		}
		if !strings.HasSuffix(text, "link: "+urlWant) {
			t.Errorf("%s card must end with its link:\n%s", name, text)
		}
	}
	check("request", b.RequestCard(req), "Issue", "open", repo+"/issues/603", "REQ-603", "1 of 2 criteria open", "with: agent")
	req.Tasks = []requestdomain.TaskRef{{ID: 9, State: "open"}}
	check("request waiting", b.RequestCard(req), "Issue", "open", repo+"/issues/603", "with: human")
	cc, ok := b.CriterionCard(req, 2)
	if !ok {
		t.Fatal("criterion 2 not found")
	}
	check("criterion", cc, "Criterion", "open", repo+"/issues/603#criterion-2", "AC-603.2")
	task := humantask.Task{ID: 124, Project: project, Kind: "action", Title: "GitHub verbinden", State: "open", Assignee: "robin",
		Steps: []humantask.Step{{Text: "Einstellungen öffnen", URL: "https://github.com/settings"}}}
	check("task", b.TaskCard(task), "Task", "open", srv+"/ui/tasks/124", "TASK-124", "1. Einstellungen öffnen", "with: robin")
	task.State = humantask.Done
	task.CheckHint = "Eintrag da"
	check("task done", b.TaskCard(task), "Task", "done", srv+"/ui/tasks/124", "agent checks (Eintrag da) and confirms")
	check("pain", b.PainCard(pain.Pain{ID: 57, Project: project, Title: "Qual", State: "active"}), "Pain", "active", repo+"/pains/57", "PAIN-57")
	check("idea", b.IdeaCard(idea.Idea{ID: 3, Project: project, Title: "Idee", State: "open"}), "Idea", "open", repo+"/ideas/3", "IDEA-3", "with: human")
	check("knowledge", b.KnowledgeCard(store.Knowledge{ID: 4295, Title: "Wissen", Type: "decision", Status: "active", Scope: axes, Person: "robin"}), "Knowledge", "decision/active", repo+"/knowledge/4295", "#4295")
	check("document", b.DocumentCard(store.Document{Slug: "plan", Title: "Plan", Project: project, Status: "active", HeadRevision: 3}), "Document", "active r3", repo+"/docs/plan", "plan")
	check("room", b.ThreadCard(store.Thread{ID: 12, Project: project, Title: "Frage", State: "open"}, 99), "Room", "open", repo+"/rooms/12#message-99", "thread 12")
	check("session", b.SessionCard(store.Session{ID: 5, PublicID: "ab12", Title: "Sitzung", Harness: "claude-code", Scope: axes}), "Session", "claude-code", repo+"/sessions/ab12", "ab12")
	pkg := requestdomain.PackageDetail{Package: requestdomain.Package{ID: 1, Project: project, Branch: "feat/x", Name: "Paket", State: "open"},
		Members: []requestdomain.PackageMemberView{{PackageMember: requestdomain.PackageMember{Role: requestdomain.RolePflicht}, State: "open"}}}
	check("package", b.PackageCard(pkg), "Checklist", "open", repo+"/package/feat/x", "PKG-1", "1 required issue(s) still open")
	pkg.PR = 155
	check("package pr", b.PackageCard(pkg), "Checklist", "open", repo+"/pulls/155/checkliste")
	check("decision", b.DecisionCard(idea.Decision{ID: 9, Project: project, Subject: "idea", SubjectID: 3, Question: "Welches Format", State: "open", Way: "human"}), "Decision", "open", repo+"/decisions/9", "DEC-9", "with: human", "a person decides")
	check("runbook", b.RunbookCard(runbook.Summary{Name: "deploy", Project: project, Purpose: "ausrollen", Head: 2, ApprovedVersion: 1}), "Runbook", "head v2, approved v1", repo+"/runbooks/deploy", "a human approves")
	check("preset", b.PresetCard(store.SearchPreset{Name: "pr-ergebnis", Scope: "project", Description: "was wurde erledigt"}, map[string]string{"pr": "155"}), "Search", "project",
		srv+"/ui/presets/repo/pr-ergebnis?pr=155&project=deadweight-labs%2Fghosttree", "preset:pr-ergebnis")
}

func TestParseRef(t *testing.T) {
	cases := []struct {
		in   string
		want Ref
	}{
		{"REQ-596", Ref{Kind: KindRequest, ID: 596}},
		{"req-596", Ref{Kind: KindRequest, ID: 596}},
		{"TASK-124", Ref{Kind: KindTask, ID: 124}},
		{"PAIN-57", Ref{Kind: KindPain, ID: 57}},
		{"IDEA-3", Ref{Kind: KindIdea, ID: 3}},
		{"PKG-4", Ref{Kind: KindPackage, ID: 4}},
		{"DEC-9", Ref{Kind: KindDecision, ID: 9}},
		{"#4295", Ref{Kind: KindKnowledge, ID: 4295}},
		{"REQ-603#2", Ref{Kind: KindCriterion, ID: 603, Num: 2}},
		{"AC-603.2", Ref{Kind: KindCriterion, ID: 603, Num: 2}},
		{"pr:155", Ref{Kind: KindPull, ID: 155}},
		{"pr:#155", Ref{Kind: KindPull, ID: 155}},
		{"commit:2D950BF", Ref{Kind: KindCommit, Text: "2d950bf"}},
		{"room:12", Ref{Kind: KindRoom, ID: 12}},
		{"session:ab12", Ref{Kind: KindSession, Text: "ab12"}},
		{"doc:plan", Ref{Kind: KindDocument, Text: "plan"}},
		{"runbook:deploy-prod", Ref{Kind: KindRunbook, Text: "deploy-prod"}},
		{"branch:feat/gn-603", Ref{Kind: KindBranch, Text: "feat/gn-603"}},
		{"inbox", Ref{Kind: KindInbox}},
		{"internal/store/humantask.go", Ref{Kind: KindPath, Text: "internal/store/humantask.go"}},
		{"is:open label:bug", Ref{Kind: KindQuery, Text: "is:open label:bug"}},
		{"is:pain", Ref{Kind: KindQuery, Text: "is:pain"}},
	}
	for _, c := range cases {
		got, err := ParseRef(c.in)
		if err != nil || got.Kind != c.want.Kind || got.ID != c.want.ID || got.Num != c.want.Num || got.Text != c.want.Text {
			t.Errorf("ParseRef(%q) = %+v, %v; want %+v", c.in, got, err, c.want)
		}
	}
	p, err := ParseRef("preset:pr-ergebnis 155")
	if err != nil || p.Kind != KindPreset || p.Text != "pr-ergebnis" || len(p.Args) != 1 || p.Args[0] != "155" {
		t.Errorf("preset: %+v %v", p, err)
	}
	for _, bad := range []string{"", "   ", "banane", "pr:", "pr:x", "commit:xyz", "#abc", "REQ-", "room:0", "preset:", "path:"} {
		if got, err := ParseRef(bad); err == nil {
			t.Errorf("ParseRef(%q) = %+v, want an error", bad, got)
		}
	}
}

type fakeAPI struct {
	reqs  map[int64]requestdomain.Detail
	tasks map[int64]humantask.Task
}

func (f fakeAPI) GetRequest(id int64) (requestdomain.Detail, error) {
	if d, ok := f.reqs[id]; ok {
		return d, nil
	}
	return requestdomain.Detail{}, errors.New("forbidden")
}
func (f fakeAPI) GetHumanTask(id int64) (humantask.Task, error) {
	if d, ok := f.tasks[id]; ok {
		return d, nil
	}
	return humantask.Task{}, errors.New("404")
}
func (fakeAPI) GetPain(id int64) (pain.Detail, error) {
	return pain.Detail{Pain: pain.Pain{ID: id, Project: project, Title: "P", State: "active"}}, nil
}
func (fakeAPI) GetIdea(id int64) (idea.Idea, error) {
	return idea.Idea{ID: id, Project: project, Title: "I", State: "open"}, nil
}
func (fakeAPI) GetDecision(id int64) (idea.Decision, error) {
	return idea.Decision{ID: id, Project: project, Subject: "idea", SubjectID: 1, Question: "Q", State: "open", Way: "human"}, nil
}
func (fakeAPI) KnowledgeByID(id int64) (store.Knowledge, error) {
	return store.Knowledge{ID: id, Title: "K", Type: "note", Status: "active"}, nil
}
func (fakeAPI) Documents(string, string, bool) ([]store.Document, error) {
	return []store.Document{{Slug: "plan", Title: "Plan", Project: project, Status: "active", HeadRevision: 1}}, nil
}
func (fakeAPI) ThreadByID(id int64, _ string) (store.Thread, error) {
	return store.Thread{ID: id, Project: project, Title: "T", State: "open"}, nil
}
func (fakeAPI) ListRunbooks(scope.Axes) ([]runbook.Summary, error) {
	return []runbook.Summary{{Name: "deploy", Project: project, Purpose: "p", Head: 1, ApprovedVersion: 1}}, nil
}
func (fakeAPI) ListPresets(string) ([]store.SearchPreset, error) {
	return []store.SearchPreset{{Name: "pr-ergebnis", Scope: "project", Args: []string{"pr"}}}, nil
}
func (fakeAPI) GetPackage(id int64, _ bool) (requestdomain.PackageDetail, error) {
	return requestdomain.PackageDetail{Package: requestdomain.Package{ID: id, Project: project, Branch: "b", Name: "N", State: "open"}}, nil
}
func (fakeAPI) Sessions(scope.Axes, int) ([]store.Session, error) {
	return []store.Session{{ID: 5, PublicID: "ab12", Title: "S", Harness: "codex", Scope: scope.Axes{Project: project}}}, nil
}

func testEnv(t *testing.T) Env {
	t.Helper()
	return Env{Builder: New(srv, project), Project: project, Branch: "feat/gn-603", RepoRoot: t.TempDir(), API: fakeAPI{
		reqs:  map[int64]requestdomain.Detail{596: {Request: requestdomain.Request{ID: 596, Title: "R", State: "open", Scope: scope.Axes{Project: project}}}},
		tasks: map[int64]humantask.Task{124: {ID: 124, Project: project, Kind: "action", Title: "T", State: "open"}},
	}}
}

func TestResolveEveryShortFormHasOneCanonicalURL(t *testing.T) {
	e := testEnv(t)
	want := map[string]string{
		"REQ-596":                repo + "/issues/596",
		"REQ-596#1":              "",
		"TASK-124":               srv + "/ui/tasks/124",
		"PAIN-57":                repo + "/pains/57",
		"IDEA-3":                 repo + "/ideas/3",
		"DEC-9":                  repo + "/decisions/9",
		"#4295":                  repo + "/knowledge/4295",
		"PKG-1":                  repo + "/package/b",
		"room:7":                 repo + "/rooms/7",
		"session:ab12":           repo + "/sessions/ab12",
		"doc:plan":               repo + "/docs/plan",
		"runbook:deploy":         repo + "/runbooks/deploy",
		"preset:pr-ergebnis 155": srv + "/ui/presets/repo/pr-ergebnis?pr=155&project=deadweight-labs%2Fghosttree",
		"pr:155":                 repo + "/pulls/155",
		"commit:2d950bf":         repo + "/commit/2d950bf",
		"branch:feat/x":          repo + "/package/feat/x",
		"internal/store/a.go":    repo + "/blob/feat/gn-603/internal/store/a.go",
		"is:open label:bug":      repo + "/issues?q=is%3Aopen+label%3Abug",
		"inbox":                  srv + "/ui/inbox",
	}
	for in, u := range want {
		if in == "REQ-596#1" {
			continue // the fake request has no criteria; covered below
		}
		ref, err := ParseRef(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		c, err := e.Resolve(ref)
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if c.URL != u {
			t.Errorf("%s -> %q, want %q", in, c.URL, u)
		}
	}
}

func TestResolveFolderUsesTreeAndAbsolutePathsStayInTheRepo(t *testing.T) {
	e := testEnv(t)
	if err := osMkdir(e.RepoRoot + "/internal"); err != nil {
		t.Fatal(err)
	}
	c, err := e.Resolve(Ref{Kind: KindPath, Text: "internal"})
	if err != nil || c.URL != repo+"/tree/feat/gn-603/internal" {
		t.Errorf("folder: %+v %v", c, err)
	}
	c, err = e.Resolve(Ref{Kind: KindPath, Text: e.RepoRoot + "/internal/x.go"})
	if err != nil || c.URL != repo+"/blob/feat/gn-603/internal/x.go" {
		t.Errorf("absolute: %+v %v", c, err)
	}
	for _, out := range []string{"../etc/passwd", "/etc/passwd", "internal/../../x"} {
		if c, err := e.Resolve(Ref{Kind: KindPath, Text: out}); err == nil {
			t.Errorf("%q must be refused, got %+v", out, c)
		}
	}
}

// Rechte: ein Objekt, das der Aufrufer nicht lesen darf, ist unbekannt, ohne
// Karte und ohne Link, und die Meldung verrät nicht, ob es existiert.
func TestResolveUnreadableIsLikeUnknown(t *testing.T) {
	e := testEnv(t)
	for _, in := range []string{"REQ-999", "TASK-999", "AC-596.9"} {
		ref, _ := ParseRef(in)
		c, err := e.Resolve(ref)
		if err == nil || c.URL != "" || c.Title != "" {
			t.Errorf("%s: want error and empty card, got %+v %v", in, c, err)
		}
		if err != nil && (strings.Contains(err.Error(), "forbidden") || strings.Contains(err.Error(), "404")) {
			t.Errorf("%s: error leaks the reason: %v", in, err)
		}
	}
}

func TestResolvePresetRejectsTooManyValues(t *testing.T) {
	e := testEnv(t)
	ref, _ := ParseRef("preset:pr-ergebnis 1 2")
	if _, err := e.Resolve(ref); err == nil {
		t.Error("a preset with more values than slots must be refused")
	}
	ref, _ = ParseRef("preset:gibtsnicht")
	if _, err := e.Resolve(ref); err == nil {
		t.Error("unknown preset must be an error")
	}
}

func osMkdir(p string) error { return os.MkdirAll(p, 0o755) }
