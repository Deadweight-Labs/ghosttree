package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

var csrfValueRE = regexp.MustCompile(`name="csrf_token" value="[0-9a-f]+"`)

// stripCSRF nimmt das sitzungseigene Token heraus; sonst ist keine Seite
// zweier Aufrufe gleich.
func stripCSRF(page string) string {
	return csrfValueRE.ReplaceAllString(page, `name="csrf_token" value="-"`)
}

func ovRequest(t *testing.T, st *store.Store, project, title string, criteria int, met int) {
	t.Helper()
	names := make([]string, criteria)
	for i := range names {
		names[i] = "criterion"
	}
	detail, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: title, Scope: scope.Axes{Project: project}, Person: "alice"}, Criteria: names})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < met; i++ {
		if err := st.SetCriterionState(detail.Criteria[i].ID, "met", requestdomain.Evidence{Kind: "test", Ref: "go test", Person: "alice"}); err != nil {
			t.Fatal(err)
		}
	}
}

func ovKnowledge(t *testing.T, st *store.Store, project, title, confidence string) {
	t.Helper()
	if _, err := st.InsertKnowledge(store.Knowledge{Type: "note", Title: title, Body: "body", Scope: scope.Axes{Project: project}, Person: "alice", Confidence: confidence}); err != nil {
		t.Fatal(err)
	}
}

// ovAgent meldet einen Agenten im Projektraum an.
func ovAgent(t *testing.T, st *store.Store, project, id, name, principal string) {
	t.Helper()
	if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: id, PrincipalID: principal, Person: name, Provider: "claude", DisplayName: name, RoomKey: store.RoomKeyForProject(project)}); err != nil {
		t.Fatal(err)
	}
}

// ovEnv ist die Shell-Umgebung mit durchgesetztem Zugriff und je einem sichtbaren
// und einem verborgenen Datensatz. Das Projekt "hidden" hat nur alice.
func ovEnv(t *testing.T) shellEnv {
	t.Helper()
	e := shellWebAll(t)
	e.St.SetAccessMode(store.AccessMode{Enforce: true})
	return e
}

func fillVisible(t *testing.T, st *store.Store) {
	ovRequest(t, st, shellProject, "Visible request", 2, 1)
	ovKnowledge(t, st, shellProject, "Visible lesson", "trusted")
	ovKnowledge(t, st, shellProject, "Visible proposal", "staged")
	ovAgent(t, st, shellProject, "claude:visible", "visible-agent", "person:2")
	ovAgent(t, st, shellProject, "claude:visible-owner", "owner-agent", "person:1")
}

func fillHidden(t *testing.T, st *store.Store) {
	ovRequest(t, st, shellHiddenProject, "SECRET-REQ", 3, 0)
	ovKnowledge(t, st, shellHiddenProject, "SECRET-LESSON", "trusted")
	ovKnowledge(t, st, shellHiddenProject, "SECRET-PROPOSAL", "staged")
	ovAgent(t, st, shellHiddenProject, "claude:hidden", "SECRET-AGENT", "person:1")
	if _, err := st.Device().Start("8.8.8.8", "SECRET-MACHINE", "8.8.8.8"); err != nil {
		t.Fatal(err)
	}
}

func fetchBody(t *testing.T, c *http.Client, target string) string {
	t.Helper()
	_, page := fetchPage(t, c, target)
	return page
}

func TestOverviewEveryListComesFromTheViewersVisibleSet(t *testing.T) {
	e := ovEnv(t)
	fillVisible(t, e.St)
	fillHidden(t, e.St)
	secrets := []string{"SECRET", shellHiddenProject, "claude:hidden", "0/3"}
	if strings.Contains(fetchBody(t, e.Owner, e.Base+"/ui/overview"), "SECRET-MACHINE") {
		t.Error("the owner overview names a machine that only started a login")
	}

	_, owner := fetchPage(t, e.Owner, e.Base+"/ui/overview")
	for _, want := range []string{"Visible request", "SECRET-REQ", "SECRET-LESSON", "SECRET-AGENT", "SECRET-PROPOSAL", "1/2", "0/3"} {
		if !strings.Contains(owner, want) {
			t.Errorf("owner overview lacks %q", want)
		}
	}
	_, scoped := fetchPage(t, e.Owner, e.Base+"/ui/overview?project="+url.QueryEscape(shellProject))
	if strings.Contains(scoped, "SECRET-REQ") || strings.Contains(scoped, "SECRET-AGENT") || strings.Contains(scoped, "SECRET-LESSON") || !strings.Contains(scoped, "Visible request") {
		t.Error("the project selector does not narrow the owner overview")
	}

	// Agenten erscheinen über dieselbe Zugangsprüfung wie die Räume: nur in
	// Räumen, in denen der Betrachter selbst einen Agenten hat.
	_, anna := fetchPage(t, e.Member, e.Base+"/ui/overview")
	if !strings.Contains(anna, "visible-agent") {
		t.Error("a member does not see the agents of the room they are in")
	}
	for name, c := range map[string]*http.Client{"lead": e.Lead, "reviewer": e.Reviewer} {
		if strings.Contains(fetchBody(t, c, e.Base+"/ui/overview"), "visible-agent") {
			t.Errorf("%s sees agents of a room they have no agent in", name)
		}
	}
	for name, c := range map[string]*http.Client{"member": e.Member, "lead": e.Lead, "reviewer": e.Reviewer} {
		_, page := fetchPage(t, c, e.Base+"/ui/overview")
		for _, want := range []string{"Visible request", "1/2", "Visible lesson"} {
			if !strings.Contains(page, want) {
				t.Errorf("%s overview lacks %q", name, want)
			}
		}
		for _, leak := range append(secrets, "wants to connect") {
			if strings.Contains(page, leak) {
				t.Errorf("%s overview leaks %q", name, leak)
			}
		}
	}
	_, member := fetchPage(t, e.Member, e.Base+"/ui/overview")
	if strings.Contains(member, "Visible proposal") {
		t.Error("a plain member is offered the review queue")
	}
	for name, c := range map[string]*http.Client{"reviewer": e.Reviewer, "lead": e.Lead} {
		_, page := fetchPage(t, c, e.Base+"/ui/overview")
		if !strings.Contains(page, "Visible proposal") || !strings.Contains(page, `href="/ui/review"`) {
			t.Errorf("%s does not get the review candidate", name)
		}
	}

	_, guest := fetchPage(t, e.Guest, e.Base+"/ui/overview")
	for _, want := range []string{"Visible request", "1/2", "Visible lesson"} {
		if !strings.Contains(guest, want) {
			t.Errorf("guest overview lacks %q", want)
		}
	}
	for _, leak := range append(secrets, "visible-agent", "Visible proposal", "ov-agent", "Agents", "Next", "wants to connect", "Connect") {
		if strings.Contains(guest, leak) {
			t.Errorf("guest overview shows %q", leak)
		}
	}
}

func TestGuestOverviewIsIdenticalWithAndWithoutHiddenData(t *testing.T) {
	e := ovEnv(t)
	paths := []string{"/ui/overview", "/ui/overview?project=" + url.QueryEscape(shellProject), "/ui/overview?project=" + url.QueryEscape(shellHiddenProject), "/ui/overview?project=nope", "/ui/overview?connect=1"}
	snapshot := func() map[string]string {
		out := map[string]string{}
		for _, p := range paths {
			status, page := fetchPage(t, e.Guest, e.Base+p)
			if status != http.StatusOK {
				t.Fatalf("GET %s = %d", p, status)
			}
			out[p] = stripCSRF(page)
		}
		return out
	}
	empty := snapshot()
	if !strings.Contains(empty["/ui/overview"], "Nothing here yet.") {
		t.Fatal("a guest without visible content gets no neutral line")
	}
	// Verborgene Daten aller Arten, auch Geräte-Abläufe und Anliegen.
	fillHidden(t, e.St)
	ovKnowledge(t, e.St, shellHiddenProject, "SECRET-OLD", "verified")
	materializeWebRoomFor(t, e.St, store.RoomKeyForProject(shellHiddenProject), "person:1", "alice")
	if _, err := e.St.AppendCoordMessage(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: store.RoomKeyForProject(shellHiddenProject),
		SenderExternalID: "alice", AuthorPrincipalID: "person:1", AuthorKind: store.AuthorHuman, ClientID: "g", Body: "SECRET-ASK", Intent: store.IntentQuestion, Mentions: []string{"person:3"}}); err != nil {
		t.Fatal(err)
	}
	for p, page := range snapshot() {
		if page != empty[p] {
			t.Errorf("guest page %s differs once hidden data exists", p)
		}
	}
	// Dasselbe, sobald es etwas Sichtbares gibt: nur das Sichtbare ändert die Seite.
	fillVisible(t, e.St)
	withVisible := snapshot()
	if withVisible["/ui/overview"] == empty["/ui/overview"] || !strings.Contains(withVisible["/ui/overview"], "Visible request") {
		t.Error("visible content does not change the guest page")
	}
	if withVisible["/ui/overview?project=nope"] != withVisible["/ui/overview?project="+url.QueryEscape(shellHiddenProject)] {
		t.Error("a hidden project name is distinguishable from an unknown one")
	}
}

func TestMemberCannotProbeHiddenProjectsThroughTheSelector(t *testing.T) {
	e := ovEnv(t)
	fillVisible(t, e.St)
	fillHidden(t, e.St)
	_, hidden := fetchPage(t, e.Member, e.Base+"/ui/overview?project="+url.QueryEscape(shellHiddenProject))
	_, unknown := fetchPage(t, e.Member, e.Base+"/ui/overview?project=github.com/x/none")
	if stripCSRF(hidden) != stripCSRF(unknown) {
		t.Error("a hidden project answers differently from an unknown one")
	}
	if strings.Contains(hidden, "SECRET") {
		t.Error("a hidden project's content reaches a member")
	}
}

func TestDeviceApprovalIsACodeFieldWithoutMachineIPOrAge(t *testing.T) {
	e := ovEnv(t)
	start, err := e.St.Device().Start("203.0.113.4", "mainex", "203.0.113.4")
	if err != nil {
		t.Fatal(err)
	}
	_, owner := fetchPage(t, e.Owner, e.Base+"/ui/overview")
	for _, want := range []string{`action="/ui/device"`, `name="user_code"`, `name="csrf_token"`} {
		if !strings.Contains(owner, want) {
			t.Errorf("the setup lacks %q", want)
		}
	}
	for _, code := range []string{start.UserCode, store.FormatUserCode(start.UserCode)} {
		if strings.Contains(owner, code) {
			t.Error("the page shows the user code")
		}
	}
	for name, c := range map[string]*http.Client{"owner": e.Owner, "member": e.Member, "lead": e.Lead, "guest": e.Guest} {
		_, page := fetchPage(t, c, e.Base+"/ui/overview?connect=1")
		if strings.Contains(page, "mainex") || strings.Contains(page, "203.0.113.4") {
			t.Errorf("%s sees a pending device", name)
		}
	}
	// Die Aktion ist die bestehende Route: CSRF und interaktive Sitzung.
	form := url.Values{"user_code": {start.UserCode}}
	resp := sameOriginPostForm(t, e.Owner, e.Base+"/ui/device", form)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST /ui/device without CSRF = %d, want 403", resp.StatusCode)
	}
	form.Set("csrf_token", renderedCSRFToken(t, e.Owner, e.Base+"/ui/overview"))
	resp = sameOriginPostForm(t, e.Owner, e.Base+"/ui/device", form)
	page := body(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, "mainex") || !strings.Contains(page, `value="approve"`) {
		t.Errorf("POST /ui/device with the code = %d, no confirmation: %s", resp.StatusCode, page)
	}
	form.Set("user_code", "BCDFGHJK")
	resp = sameOriginPostForm(t, e.Owner, e.Base+"/ui/device", form)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a wrong code = %d, want 400", resp.StatusCode)
	}
}

func TestOwnerOnAnEmptyInstanceSeesGettingStartedThatSwitches(t *testing.T) {
	e := shellWebAll(t)
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/overview")
	for _, want := range []string{"Connect your first agent", "ctx login --server http", "data-copy", "Waiting for your machine", `http-equiv="refresh"`} {
		if !strings.Contains(page, want) {
			t.Errorf("setup lacks %q", want)
		}
	}
	if strings.Contains(page, "Agents</h2>") || strings.Contains(page, "ov-cols") {
		t.Error("setup still shows the normal overview")
	}

	start, err := e.St.Device().Start("203.0.113.9", "mainex", "203.0.113.9")
	if err != nil {
		t.Fatal(err)
	}
	_, page = fetchPage(t, e.Owner, e.Base+"/ui/overview")
	if strings.Contains(page, "mainex") || strings.Contains(page, "203.0.113.9") || !strings.Contains(page, `name="user_code"`) {
		t.Error("setup names the waiting device or lacks the code field")
	}
	if strings.Contains(page, start.UserCode) {
		t.Error("the user code is shown")
	}
	if err := e.St.Device().Decide(start.UserCode, "person:1", true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.St.CreateDeviceToken("person:1", "mainex"); err != nil {
		t.Fatal(err)
	}
	_, page = fetchPage(t, e.Owner, e.Base+"/ui/overview")
	if !strings.Contains(page, "mainex is connected") || !strings.Contains(page, `http-equiv="refresh"`) {
		t.Errorf("setup does not confirm the connection: %s", page)
	}

	ovAgent(t, e.St, shellProject, "claude:mainex", "claude@mainex", "person:1")
	_, page = fetchPage(t, e.Owner, e.Base+"/ui/overview")
	if strings.Contains(page, "Connect your first agent") || strings.Contains(page, `http-equiv="refresh"`) || !strings.Contains(page, "claude@mainex") {
		t.Errorf("the overview does not take over once an agent exists: %s", page)
	}
}

func TestGettingStartedIsForOwnersAndAConnectedMachineDoesNotTrapThem(t *testing.T) {
	e := shellWebAll(t)
	for name, c := range map[string]*http.Client{"member": e.Member, "lead": e.Lead, "guest": e.Guest} {
		_, page := fetchPage(t, c, e.Base+"/ui/overview")
		if strings.Contains(page, "Connect your first agent") || strings.Contains(page, "ctx login") {
			t.Errorf("%s gets Getting started", name)
		}
	}
	_, member := fetchPage(t, e.Member, e.Base+"/ui/overview")
	if !strings.Contains(member, "No agents yet.") || !strings.Contains(member, "Connect an agent") {
		t.Error("a member without agents lacks the empty line and its action")
	}
	_, forced := fetchPage(t, e.Member, e.Base+"/ui/overview?connect=1")
	if !strings.Contains(forced, "ctx login --server") || strings.Contains(forced, `http-equiv="refresh"`) {
		t.Error("the connect view is missing for a member or reloads by itself")
	}
	_, guestForced := fetchPage(t, e.Guest, e.Base+"/ui/overview?connect=1")
	if strings.Contains(guestForced, "ctx login") {
		t.Error("a guest reaches the connect view")
	}
	// Eine alte Verbindung ohne Agent hält den Owner nicht in Getting started.
	if _, _, err := e.St.CreateDeviceToken("person:1", "oldbox"); err != nil {
		t.Fatal(err)
	}
	_, owner := fetchPage(t, e.Owner, e.Base+"/ui/overview")
	if !strings.Contains(owner, "oldbox is connected") {
		t.Fatalf("a fresh connection is not confirmed")
	}
	defer func(prev func() time.Time) { overviewNow = prev }(overviewNow)
	overviewNow = func() time.Time { return time.Now().Add(time.Hour) }
	_, owner = fetchPage(t, e.Owner, e.Base+"/ui/overview")
	if strings.Contains(owner, "Connect your first agent") || !strings.Contains(owner, "No agents yet.") {
		t.Errorf("an old connection without an agent keeps the owner in Getting started: %s", owner)
	}
}

func TestOverviewEmptySectionsAreOneLineAndAnAction(t *testing.T) {
	e := shellWebAll(t)
	ovAgent(t, e.St, shellProject, "claude:a", "agent-a", "person:2")
	ovAgent(t, e.St, shellProject, "claude:o", "agent-o", "person:1")
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/overview")
	for _, want := range []string{"No open requests.", "Nothing learned this week."} {
		if !strings.Contains(page, want) {
			t.Errorf("empty section lacks %q", want)
		}
	}
	if strings.Contains(page, "ov-next") || strings.Contains(page, ">Next<") {
		t.Error("an empty Next section is shown")
	}
	_, member := fetchPage(t, e.Member, e.Base+"/ui/overview")
	if !strings.Contains(member, "Connect another agent") {
		t.Error("the agent list lacks its action")
	}
}

func TestOverviewHasNoCountersPillsOrBadges(t *testing.T) {
	e := ovEnv(t)
	fillVisible(t, e.St)
	for name, c := range map[string]*http.Client{"owner": e.Owner, "member": e.Member, "guest": e.Guest} {
		_, page := fetchPage(t, c, e.Base+"/ui/overview")
		for _, bad := range []string{"badge", "pill", "clay-card", "chip"} {
			if strings.Contains(page, bad) {
				t.Errorf("%s overview carries %q", name, bad)
			}
		}
	}
}

func TestAgentStateFollowsTheLeaseWindows(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		age  time.Duration
		want string
	}{
		{0, "active"}, {7 * time.Minute, "active"}, {8 * time.Minute, "active"}, {9 * time.Minute, "idle"},
		{23 * time.Hour, "idle"}, {25 * time.Hour, "offline"}, {72 * time.Hour, "offline"},
	} {
		if got := agentState(now, now.Add(-tc.age)); got != tc.want {
			t.Errorf("age %s = %s, want %s", tc.age, got, tc.want)
		}
	}
	if agentState(now, time.Time{}) != "offline" {
		t.Error("no signal must read as offline")
	}
	for age, want := range map[time.Duration]string{10 * time.Second: "now", 5 * time.Minute: "5m", 3 * time.Hour: "3h", 72 * time.Hour: "3d"} {
		if got := shortAge(now, now.Add(-age)); got != want {
			t.Errorf("shortAge(%s) = %q, want %q", age, got, want)
		}
	}
}

func TestLoginAndTheRootLandOnTheOverview(t *testing.T) {
	srv, _, token := testWeb(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	c := login(t, srv, token)
	c.CheckRedirect = client.CheckRedirect
	resp, err := c.Get(srv.URL + "/ui/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/overview" {
		t.Errorf("GET /ui/ = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if !strings.Contains(brandHref(t, c, srv.URL), `href="/ui/overview"`) {
		t.Error("the brand link does not lead to the overview")
	}
}

func brandHref(t *testing.T, c *http.Client, base string) string {
	t.Helper()
	_, page := fetchPage(t, c, base+"/ui/requests")
	i := strings.Index(page, `class="shell-brand"`)
	if i < 0 {
		t.Fatal("no brand link")
	}
	return page[i : i+80]
}

func TestPastedTokenSessionGetsNoDeviceForm(t *testing.T) {
	srv, st, token := testWeb(t)
	if _, err := st.Device().Start("203.0.113.4", "mainex", "203.0.113.4"); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, login(t, srv, token), srv.URL+"/ui/overview")
	if strings.Contains(page, "mainex") || strings.Contains(page, `name="user_code"`) {
		t.Error("a read-only session is offered the device approval")
	}
}

func TestShellHeadSetsTheJSClassBeforeFirstPaint(t *testing.T) {
	e := shellWebAll(t)
	_, page := fetchPage(t, e.Member, e.Base+"/ui/requests")
	head := page[:strings.Index(page, "</head>")]
	if strings.Contains(head, "<script>") || !strings.Contains(head, `<script src="/static/shell.js"></script>`) {
		t.Error("the js class script is not a synchronous external script in the head")
	}
	js := body(t, mustGet(t, e.Base+"/static/shell.js"))
	if !strings.Contains(js, `classList.add("js")`) {
		t.Error("shell.js does not set the js class")
	}
	if strings.Index(head, "<script>") > strings.Index(head, "shell.css") {
		t.Log("script after stylesheet is fine as long as it sits in the head")
	}
}

func TestFontPathsAreVersionedSoImmutableCachingIsSafe(t *testing.T) {
	srv, _, _ := testWeb(t)
	get := func(path string) *http.Response {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	if resp := get("/static/fonts/gabarito-latin-wght-normal.woff2"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the unversioned font path still answers: %d", resp.StatusCode)
	}
	resp := get("/static/fonts/v5/gabarito-latin-wght-normal.woff2")
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Errorf("versioned font = %d %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	css := body(t, mustGet(t, srv.URL+"/static/tokens.css"))
	for _, m := range regexp.MustCompile(`url\("(/static/fonts/[^"]+)"\)`).FindAllStringSubmatch(css, -1) {
		if r := get(m[1]); r.StatusCode != http.StatusOK || !strings.Contains(m[1], "v5/") {
			t.Errorf("tokens.css font %s = %d", m[1], r.StatusCode)
		}
	}
}

func mustGet(t *testing.T, target string) *http.Response {
	t.Helper()
	resp, err := http.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestFreshInstanceFirstAccountGetsGettingStartedWithoutAnOrganization(t *testing.T) {
	srv, st, _ := testWeb(t)
	c := loginInteractive(t, srv, st, "alice")
	_, page := fetchPage(t, c, srv.URL+"/ui/overview")
	if !strings.Contains(page, "Connect your first agent") {
		t.Errorf("the first account of a fresh instance does not get Getting started: %s", page)
	}
	if _, err := st.AddPerson("bob"); err != nil {
		t.Fatal(err)
	}
	_, page = fetchPage(t, loginInteractive(t, srv, st, "bob"), srv.URL+"/ui/overview")
	if strings.Contains(page, "Connect your first agent") {
		t.Error("a later account gets Getting started")
	}
}
