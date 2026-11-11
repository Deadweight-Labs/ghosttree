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
	for _, want := range []string{"Set up ghosttree", ">Install</button>", `action="/join/pair"`, `name="confirm_account"`, "Waiting for your machine", `http-equiv="refresh"`} {
		if !strings.Contains(page, want) {
			t.Errorf("setup lacks %q", want)
		}
	}
	if regexp.MustCompile(`name="confirm_account"[^>]*\bchecked\b`).MatchString(page) {
		t.Error("the confirmation box comes ticked: the protective question is gone")
	}
	// The box is required, so the browser says what is missing; no permanent sentence.
	if !regexp.MustCompile(`name="confirm_account"[^>]*\brequired\b`).MatchString(page) || strings.Contains(page, "Confirm first that this is your account.") {
		t.Error("the box is not required, or a permanent hint stands next to the button")
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
	if !strings.Contains(page, "mainex</b> is connected") || !strings.Contains(page, `http-equiv="refresh"`) {
		t.Errorf("setup does not confirm the connection: %s", page)
	}

	ovAgent(t, e.St, shellProject, "claude:mainex", "claude@mainex", "person:1")
	status, _ := fetchPage(t, e.Owner, e.Base+"/ui/overview")
	if status != http.StatusFound {
		t.Errorf("the inbox does not take over once an agent exists: %d", status)
	}
}

func TestGettingStartedIsForOwnersAndAConnectedMachineDoesNotTrapThem(t *testing.T) {
	e := shellWebAll(t)
	for name, c := range map[string]*http.Client{"member": e.Member, "lead": e.Lead, "guest": e.Guest} {
		status, page := fetchPage(t, c, e.Base+"/ui/overview")
		if status != http.StatusFound || strings.Contains(page, "Set up ghosttree") || strings.Contains(page, "ctx login") || strings.Contains(page, "install command") {
			t.Errorf("%s gets Getting started (status %d)", name, status)
		}
	}
	_, forced := fetchPage(t, e.Member, e.Base+"/ui/overview?connect=1")
	if !strings.Contains(forced, ">Install</button>") || strings.Contains(forced, `http-equiv="refresh"`) {
		t.Error("the connect view is missing for a member or reloads by itself")
	}
	_, guestForced := fetchPage(t, e.Guest, e.Base+"/ui/overview?connect=1")
	if strings.Contains(guestForced, "ctx login") || strings.Contains(guestForced, "install command") {
		t.Error("a guest reaches the connect view")
	}
	// Eine alte Verbindung ohne Agent hält den Owner nicht in Getting started.
	if _, _, err := e.St.CreateDeviceToken("person:1", "oldbox"); err != nil {
		t.Fatal(err)
	}
	_, owner := fetchPage(t, e.Owner, e.Base+"/ui/overview")
	if !strings.Contains(owner, "oldbox</b> is connected") {
		t.Fatalf("a fresh connection is not confirmed")
	}
	defer func(prev func() time.Time) { overviewNow = prev }(overviewNow)
	overviewNow = func() time.Time { return time.Now().Add(time.Hour) }
	status, owner := fetchPage(t, e.Owner, e.Base+"/ui/overview")
	if status != http.StatusFound || strings.Contains(owner, "Set up ghosttree") {
		t.Errorf("an old connection without an agent should not hold the owner in Getting started: %d", status)
	}
	_, agents := fetchPage(t, e.Owner, e.Base+"/ui/agents")
	if !strings.Contains(agents, "Start Claude or Codex in a project") || !strings.Contains(agents, "oldbox") || !strings.Contains(agents, "Last seen 1 h ago") {
		t.Errorf("the agents page should show the machine and the next step: %s", agents)
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
		if got := shortAge(langEN, now, now.Add(-age)); got != want {
			t.Errorf("shortAge(%s) = %q, want %q", age, got, want)
		}
	}
}

func TestTheRootAndTheBrandLeadToTheInbox(t *testing.T) {
	srv, _, token := testWeb(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	c := login(t, srv, token)
	c.CheckRedirect = client.CheckRedirect
	resp, err := c.Get(srv.URL + "/ui/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/inbox" {
		t.Errorf("GET /ui/ = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if !strings.Contains(brandHref(t, c, srv.URL), `href="/ui/inbox"`) {
		t.Error("the brand link does not lead to the inbox")
	}
}

func brandHref(t *testing.T, c *http.Client, base string) string {
	t.Helper()
	_, page := fetchPage(t, c, base+"/ui/inbox")
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
	_, page := fetchPage(t, e.Member, e.Base+"/ui/inbox")
	head := page[:strings.Index(page, "</head>")]
	if strings.Contains(head, "<script>") || !regexp.MustCompile(`<script src="/static/shell\.js\?v=[0-9a-f]+"></script>`).MatchString(head) {
		t.Error("the js class script is not a synchronous external script in the head")
	}
	js := body(t, mustGet(t, e.Base+"/static/shell.js"))
	if !strings.Contains(js, `classList.add("js")`) {
		t.Error("shell.js does not set the js class")
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
	if !strings.Contains(page, "Set up ghosttree") {
		t.Errorf("the first account of a fresh instance does not get Getting started: %s", page)
	}
	if _, err := st.AddPerson("bob"); err != nil {
		t.Fatal(err)
	}
	_, page = fetchPage(t, loginInteractive(t, srv, st, "bob"), srv.URL+"/ui/overview")
	if strings.Contains(page, "Set up ghosttree") {
		t.Error("a later account gets Getting started")
	}
}

func TestOwnerWithoutAMachineSeesTheSameGuidedInstallCommandAsAnInvitedPerson(t *testing.T) {
	e := shellWebAll(t)
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/overview")
	if strings.Contains(page, "ctx login") || strings.Contains(page, "curl -fsSL") {
		t.Fatalf("the owner sees a command before asking for one:\n%s", page)
	}
	// Asking creates the pairing code, and the command stands on the overview.
	form := url.Values{"confirm_account": {"alice"}, "csrf_token": {renderedCSRFToken(t, e.Owner, e.Base+"/ui/overview")}}
	resp := sameOriginPostForm(t, e.Owner, e.Base+"/join/pair", form)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /join/pair = %d", resp.StatusCode)
	}
	_, page = fetchPage(t, e.Owner, e.Base+"/ui/overview")
	for _, want := range []string{"curl -fsSL", "/install.sh</span> | sh -s -- --pair ", "data-copy-from", "Waiting for your machine"} {
		if !strings.Contains(page, want) {
			t.Errorf("overview lacks %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, "ctx login --server") {
		t.Error("the bare login command is still the first thing the owner sees")
	}
	pairCode := e.St.Join().View("person:1").Pair
	_, pairPage := fetchPage(t, e.Owner, e.Base+"/join/pair")
	if !strings.Contains(pairPage, pairCode) {
		t.Error("the pairing page shows another code than the overview")
	}
	// Once the machine reported itself, approval happens on the pairing page.
	if _, err := e.St.Join().Claim(store.JoinClaimRequest{Addr: "203.0.113.9", Pair: pairCode, Machine: "mainex"}); err != nil {
		t.Fatal(err)
	}
	_, page = fetchPage(t, e.Owner, e.Base+"/ui/overview")
	if !strings.Contains(page, `href="/join/pair"`) || strings.Contains(page, "mainex") {
		t.Errorf("overview after the claim: %s", page)
	}
}

func TestOverviewWithAPairingCodeIsNeverCached(t *testing.T) {
	e := shellWebAll(t)
	form := url.Values{"confirm_account": {"alice"}, "csrf_token": {renderedCSRFToken(t, e.Owner, e.Base+"/ui/overview")}}
	resp := sameOriginPostForm(t, e.Owner, e.Base+"/join/pair", form)
	resp.Body.Close()
	code := e.St.Join().View("person:1").Pair
	for _, path := range []string{"/ui/overview", "/join/pair"} {
		resp, err := e.Owner.Get(e.Base + path)
		if err != nil {
			t.Fatal(err)
		}
		page := body(t, resp)
		if !strings.Contains(page, code) {
			t.Fatalf("%s does not show the pairing code", path)
		}
		if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
			t.Errorf("%s shows a pairing code with Cache-Control %q", path, cc)
		}
	}
}
