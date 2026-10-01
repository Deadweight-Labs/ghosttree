package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestCoordPageCarriesPreRenderEventCursorAndVisibleLiveStatus(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/y")
	materializeWebRoom(t, st, room)
	if _, err := st.AppendCoordMessage(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "fixture:" + room, ClientID: "cursor", Body: "ready"}); err != nil {
		t.Fatal(err)
	}
	page := coordPageBody(t, client, srv.URL+"/ui/coord?room="+room)
	if !strings.Contains(page, `data-coord-event-cursor="`) || !strings.Contains(page, `data-coord-live-status`) || !strings.Contains(page, `data-coord-action-status`) || !strings.Contains(page, "Live-Verbindung wird aufgebaut") {
		t.Fatalf("coord live bootstrap missing: %s", page)
	}
}

func TestCoordEventStructureRetainsProgressiveSurfaceHooks(t *testing.T) {
	template := string(mustReadEmbedded(t, "templates/coord.html"))
	for _, hook := range []string{
		`id="coord-rooms"`,
		`class="coord-conversation"`,
		`id="coord-context"`,
		`class="coord-messages"`,
		`data-coord-sidebar-dynamic`,
		`data-coord-read-actions`,
		`data-coord-standing-list`,
		`data-coord-thread-list`,
		`coord-thread-paging`,
		`class="coord-thread-messages"`,
		`data-coord-draft-key=`,
		`data-coord-focus-key=`,
		`data-coord-drawer-target=`,
		`data-coord-live-status`,
	} {
		if !strings.Contains(template, hook) {
			t.Errorf("progressive coordination hook missing %q", hook)
		}
	}
}

func TestCoordProgressiveClientRefetchesCanonicalVisibleSurfaces(t *testing.T) {
	source := string(mustReadEmbedded(t, "static/app.js"))
	for _, want := range []string{
		"new EventSource", "coord.changed", "resync", "DOMParser", "location.href",
		"URLSearchParams", "around", "thread_around", "data-coord-live-status",
		"replaceChildren", "getBoundingClientRect", "container.scrollTop", "messageScrollTop", "contextScrollTop",
		"captureFocus", "restoreFocus", "[data-coord-sidebar-dynamic]", "[data-coord-read-actions]",
		"raw.lastEventId", "raw.lastEventId === lastEventID",
		"captureDrafts", "restoreDrafts", "coordDraftKey", "container.open", "control.checked",
		".coord-thread-paging",
		"session-ended", "source.close()", "visibilitychange",
		`new URL(response.url).pathname === "/ui/login"`,
	} {
		if !strings.Contains(source, want) {
			t.Errorf("progressive coordination client missing %q", want)
		}
	}
	for _, unsafe := range []string{`queueSurface(".coord-conversation")`, `queueSurface("#coord-context")`, `queueSurface("#coord-rooms")`, `".coord-conversation-head",`} {
		if strings.Contains(source, unsafe) {
			t.Errorf("progressive refresh replaces draft-bearing surface via %q", unsafe)
		}
	}
}

func TestCoordProgressiveClientOnlyInterceptsSafeCoordinationNavigationAndForms(t *testing.T) {
	source := string(mustReadEmbedded(t, "static/app.js"))
	for _, want := range []string{
		"history.pushState", "popstate", "event.submitter", "new FormData(form)",
		"new URLSearchParams()", "coordFormBody",
		"response.redirected", "response.url", "AbortController", "aria-busy",
		"data-coord-action-status", "Aktion nicht automatisch erneut gesendet",
		`location.assign(target.href)`, `redirect: "follow"`,
	} {
		if !strings.Contains(source, want) {
			t.Errorf("progressive navigation client missing %q", want)
		}
	}
	for _, path := range []string{
		"/ui/coord/send", "/ui/coord/thread/post", "/ui/coord/read", "/ui/coord/unread",
		"/ui/coord/attention/action", "/ui/coord/standing/create", "/ui/coord/standing/end",
		"/ui/coord/direct/start", "/ui/coord/group/create", "/ui/coord/group/update",
		"/ui/coord/group/leave", "/ui/coord/thread/create", "/ui/coord/thread/state",
	} {
		if !strings.Contains(source, `"`+path+`"`) {
			t.Errorf("progressive form allowlist missing %q", path)
		}
	}
	if strings.Contains(source, `"/ui/logout"`) {
		t.Fatal("progressive coordination client must not intercept logout")
	}
}

func TestCoordProgressiveTargetClassificationRunsInNode(t *testing.T) {
	source := string(mustReadEmbedded(t, "static/app.js"))
	start := strings.Index(source, "  const progressiveFormPaths =")
	end := strings.Index(source, "  const liveStatus =")
	if start < 0 || end <= start {
		t.Fatal("progressive target helpers are not independently testable")
	}
	program := source[start:end] + `
const origin = "https://ghosttree.test";
const anchor = (href, extra = {}) => ({href, target:"", download:false, ...extra});
if (coordPageTarget(anchor(origin + "/ui/coord?room=project%3Ap"), origin)?.pathname !== "/ui/coord") throw new Error("coord room link rejected");
if (coordPageTarget(anchor(origin + "/ui/coord?room=project%3Ap#message-4"), origin)?.hash !== "#message-4") throw new Error("coord hash lost");
if (coordPageTarget(anchor(origin + "/ui/requests/4"), origin) !== null) throw new Error("unrelated link intercepted");
if (coordPageTarget(anchor("https://elsewhere.test/ui/coord"), origin) !== null) throw new Error("cross-origin link intercepted");
if (coordPageTarget(anchor(origin + "/ui/coord", {target:"_blank"}), origin) !== null) throw new Error("targeted link intercepted");
if (!coordFormTarget({method:"post", action:origin + "/ui/coord/send"}, origin)) throw new Error("composer rejected");
if (!coordFormTarget({method:"POST", action:origin + "/ui/coord/thread/state"}, origin)) throw new Error("thread state rejected");
if (coordFormTarget({method:"post", action:origin + "/ui/logout"}, origin) !== null) throw new Error("logout intercepted");
if (coordFormTarget({method:"get", action:origin + "/ui/coord"}, origin) !== null) throw new Error("GET form intercepted");
if (coordFormTarget({method:"post", action:"https://elsewhere.test/ui/coord/send"}, origin) !== null) throw new Error("cross-origin form intercepted");
const NativeFormData = FormData;
FormData = class {
  constructor() { this.items = [["csrf_token", "token"], ["mentions", "person:a"], ["mentions", "person:b"]]; }
  append(name, value) { this.items.push([name, value]); }
  entries() { return this.items.values(); }
};
const encoded = coordFormBody({}, {name:"action", value:"approve"});
FormData = NativeFormData;
if (!(encoded instanceof URLSearchParams)) throw new Error("POST body is not urlencoded");
if (encoded.get("csrf_token") !== "token") throw new Error("csrf token was lost");
if (encoded.getAll("mentions").join(",") !== "person:a,person:b") throw new Error("repeated values were lost");
if (encoded.get("action") !== "approve") throw new Error("submitter was lost");
`
	command := exec.Command("node", "-e", program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("progressive target classification failed: %v\n%s", err, output)
	}
}

func TestCoordProgressiveURLEncodedPOSTPassesCSRFAndKeepsCanonicalPRG(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/progressive-post")
	materializeWebRoom(t, st, room)
	form := url.Values{
		"csrf_token": {renderedCSRFToken(t, client, srv.URL+"/ui/coord")},
		"room":       {room},
		"sequence":   {"0"},
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/ui/coord/read", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=UTF-8")
	req.Header.Set("Origin", srv.URL)
	req.Header.Set("X-Coord-Progressive", "1")
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("progressive POST status=%d, want 303", res.StatusCode)
	}
	want := "/ui/coord?room=" + url.QueryEscape(room)
	if res.Header.Get("Location") != want {
		t.Fatalf("progressive POST location=%q, want %q", res.Header.Get("Location"), want)
	}
}

func TestCoordMessagePromotionHasStableDraftIdentity(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/y")
	materializeWebRoom(t, st, room)
	messageID, err := st.AppendCoordMessage(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "fixture:" + room, ClientID: "draft", Body: "promote"})
	if err != nil {
		t.Fatal(err)
	}
	page := coordPageBody(t, client, srv.URL+"/ui/coord?room="+room)
	if !strings.Contains(page, `data-coord-draft-key="promote-`+strconv.FormatInt(messageID, 10)+`"`) {
		t.Fatalf("promotion draft key missing: %s", page)
	}
}

func TestCoordFocusIdentityPrefersStableOperationKey(t *testing.T) {
	source := string(mustReadEmbedded(t, "static/app.js"))
	start := strings.Index(source, "const focusIdentity =")
	end := strings.Index(source, "  const captureFocus =")
	if start < 0 || end <= start {
		t.Fatal("focus identity helpers are not independently testable")
	}
	program := source[start:end] + `
const oldButton = {dataset:{coordFocusKey:"room-read-project:p"}, tagName:"BUTTON", id:"", getAttribute:()=>"", textContent:"Bis hier gelesen"};
const replacement = {dataset:{coordFocusKey:"room-read-project:p"}, tagName:"BUTTON", id:"", getAttribute:()=>"", textContent:"Localized replacement"};
const wrongOperation = {dataset:{coordFocusKey:"room-unread-project:p"}, tagName:"BUTTON", id:"", getAttribute:()=>"", textContent:"Bis hier gelesen"};
const oldUnkeyed = {dataset:{}, tagName:"BUTTON", id:"", getAttribute:()=>"", textContent:"Stand ändern"};
const replacementUnkeyed = {dataset:{}, tagName:"BUTTON", id:"", getAttribute:()=>"", textContent:"Stand ändern"};
const state = focusIdentity(oldButton);
if (!sameFocusIdentity(state, replacement)) throw new Error("stable key did not survive replacement");
if (sameFocusIdentity(state, wrongOperation)) throw new Error("fallback matched a different keyed operation");
if (!sameFocusIdentity(focusIdentity(oldUnkeyed), replacementUnkeyed)) throw new Error("unkeyed operation focus was lost");
`
	command := exec.Command("node", "-e", program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("focus identity behavior failed: %v\n%s", err, output)
	}
}

func TestCoordDraftIdentityExcludesServerContextAndSurvivesReplyShapeChanges(t *testing.T) {
	source := string(mustReadEmbedded(t, "static/app.js"))
	start := strings.Index(source, "  const draftControlSelector =")
	end := strings.Index(source, "  const applyCoordinator =")
	if start < 0 || end <= start {
		t.Fatal("draft identity helpers are not independently testable")
	}
	if !strings.Contains(source[start:end], `input:not([type='hidden'])`) {
		t.Fatal("draft controls must exclude hidden server-owned context")
	}
	program := source[start:end] + `
const control = (tag, type, name, value, text = "") => ({
  tagName:tag, type, value, textContent:text,
  getAttribute:(key) => key === "name" ? name : (key === "type" ? type : ""),
});
const before = [
  control("TEXTAREA", "", "body", "user draft"),
  control("SELECT", "", "intent", "question"),
  control("INPUT", "checkbox", "mentions", "person:a"),
  control("INPUT", "checkbox", "mentions", "person:b"),
];
const after = [
  control("TEXTAREA", "", "body", ""),
  control("SELECT", "", "intent", ""),
  control("INPUT", "checkbox", "mentions", "person:a"),
  control("INPUT", "checkbox", "mentions", "person:b"),
];
const beforeKeys = draftControlKeys(before);
const afterKeys = draftControlKeys(after);
if (JSON.stringify(beforeKeys) !== JSON.stringify(afterKeys)) throw new Error("reply shape changed visible draft identities");
if (new Set(beforeKeys).size !== beforeKeys.length) throw new Error("mention identities collided");
if (beforeKeys.some((key) => key.includes("user draft"))) throw new Error("mutable text leaked into identity");

let containers = [];
const document = {
  activeElement:null,
  querySelectorAll:() => containers,
};
const field = (tag, type, name, value, checked = false) => ({
  tagName:tag, type, value, checked, selectionStart:null, selectionEnd:null,
  getAttribute:(key) => key === "name" ? name : (key === "type" ? type : ""),
  focus() { document.activeElement = this; },
});
const container = (controls) => ({
  dataset:{coordDraftKey:"room-composer-project:p"}, tagName:"FORM",
  querySelectorAll:() => controls.filter((item) => item.type !== "hidden"),
  contains:(item) => controls.includes(item),
});
const oldControls = [
  field("INPUT", "hidden", "csrf_token", "old-csrf"),
  field("INPUT", "hidden", "room", "project:p"),
  field("INPUT", "hidden", "form_id", "old-form"),
  field("INPUT", "hidden", "reply_to", "41"),
  field("TEXTAREA", "", "body", "user draft"),
  field("SELECT", "", "intent", "question"),
  field("INPUT", "checkbox", "mentions", "person:a", true),
  field("INPUT", "checkbox", "mentions", "person:b", false),
];
containers = [container(oldControls)];
document.activeElement = oldControls[4];
const drafts = captureDrafts();
const newControls = [
  field("INPUT", "hidden", "csrf_token", "new-csrf"),
  field("INPUT", "hidden", "room", "project:p"),
  field("INPUT", "hidden", "form_id", "new-form"),
  field("INPUT", "hidden", "reply_to", "99"),
  field("TEXTAREA", "", "body", ""),
  field("SELECT", "", "intent", ""),
  field("INPUT", "checkbox", "mentions", "person:a", false),
  field("INPUT", "checkbox", "mentions", "person:b", true),
];
containers = [container(newControls)];
document.activeElement = null;
restoreDrafts(drafts);
if (newControls[0].value !== "new-csrf" || newControls[2].value !== "new-form") throw new Error("server token context was overwritten");
if (newControls[3].value !== "99") throw new Error("reply A overwrote server reply B");
if (newControls[4].value !== "user draft" || newControls[5].value !== "question") throw new Error("user text draft was lost");
if (!newControls[6].checked || newControls[7].checked) throw new Error("mention draft was not restored by value");

const noReplyControls = oldControls.filter((item) => item.getAttribute("name") !== "reply_to");
containers = [container(noReplyControls)];
const noReplyDraft = captureDrafts();
const replyControls = [...newControls];
replyControls[3] = field("INPUT", "hidden", "reply_to", "123");
containers = [container(replyControls)];
restoreDrafts(noReplyDraft);
if (replyControls[3].value !== "123" || replyControls[4].value !== "user draft") throw new Error("no-reply to reply shifted draft controls");
`
	command := exec.Command("node", "-e", program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("draft identity behavior failed: %v\n%s", err, output)
	}
}

func TestCoordApplyCoordinatorRejectsStaleNavigationPostAndRefreshResults(t *testing.T) {
	source := string(mustReadEmbedded(t, "static/app.js"))
	start := strings.Index(source, "  const createCoordApplyCoordinator =")
	end := strings.Index(source, "  const progressiveFormPaths =")
	if start < 0 || end <= start {
		t.Fatal("coord apply coordinator is not independently testable")
	}
	program := source[start:end] + `
const deferred = () => {
  let resolve;
  return {promise:new Promise((done) => { resolve = done; }), resolve};
};
const coordinator = createCoordApplyCoordinator();
let rendered = "initial";
let queuedRefresh = false;
let refreshDrains = 0;
coordinator.onIdle(() => {
  if (!queuedRefresh) return;
  const refresh = coordinator.begin("refresh");
  if (!refresh) throw new Error("queued refresh still blocked after idle");
  queuedRefresh = false;
  refreshDrains += 1;
  rendered = "event-change";
  coordinator.finish(refresh);
});
const run = async (kind, pending) => {
  const lease = coordinator.begin(kind);
  if (!lease) return;
  const value = await pending.promise;
  if (coordinator.current(lease)) rendered = value;
  coordinator.finish(lease);
};
(async () => {
  const a = deferred();
  const b = deferred();
  const runA = run("navigation", a);
  const runB = run("navigation", b);
  b.resolve("room-b"); await runB;
  a.resolve("room-a"); await runA;
  if (rendered !== "room-b") throw new Error("stale room A replaced room B");

  const post = deferred();
  const nav = deferred();
  const runPost = run("post", post);
  const runNav = run("navigation", nav);
  nav.resolve("new-navigation"); await runNav;
  post.resolve("old-post"); await runPost;
  if (rendered !== "new-navigation") throw new Error("stale POST replaced navigation");

  const blocking = coordinator.begin("navigation");
  queuedRefresh = true;
  if (coordinator.begin("refresh") !== null) throw new Error("refresh preempted navigation");
  rendered = "stale-navigation-snapshot";
  coordinator.finish(blocking);
  if (queuedRefresh || refreshDrains !== 1 || rendered !== "event-change") throw new Error("pending SSE change was lost after navigation");
})().catch((error) => { console.error(error); process.exit(1); });
`
	command := exec.Command("node", "-e", program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("coord apply ordering failed: %v\n%s", err, output)
	}
}

func TestCoordThreadInvalidationAlsoRefreshesVisibleRoomMessages(t *testing.T) {
	source := string(mustReadEmbedded(t, "static/app.js"))
	start := strings.Index(source, "  const roomSurfaces =")
	end := strings.Index(source, "  const scheduleRefreshDrain =")
	if start < 0 || end <= start {
		t.Fatal("coord event surface mapping is not independently testable")
	}
	program := source[start:end] + `
const selectors = coordEventSurfaces(
  {object_kind:"thread", object_id:"77", kind:"create"},
  "project:visible", "",
);
if (!selectors.includes(".coord-messages")) throw new Error("thread promotion left visible room messages stale");
if (!selectors.includes("[data-coord-thread-list]")) throw new Error("thread list was not refreshed");
if (!selectors.includes("[data-coord-sidebar-dynamic]")) throw new Error("authorized sidebar summary was not refreshed");
if (selectors.includes("#coord-rooms") || selectors.includes("#coord-context")) throw new Error("mapping requested broad private surfaces");
`
	command := exec.Command("node", "-e", program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("coord thread invalidation mapping failed: %v\n%s", err, output)
	}
}

func TestCoordAttentionInvalidationRefreshesDedicatedDetailSurface(t *testing.T) {
	source := string(mustReadEmbedded(t, "static/app.js"))
	start := strings.Index(source, "  const roomSurfaces =")
	end := strings.Index(source, "  const scheduleRefreshDrain =")
	if start < 0 || end <= start {
		t.Fatal("coord event surface mapping is not independently testable")
	}
	program := source[start:end] + `
const selectors = coordEventSurfaces(
  {object_kind:"attention", object_id:"41", kind:"attention"},
  "project:visible", "",
);
if (!selectors.includes("[data-coord-attention-details]")) throw new Error("attention detail appearance/disappearance was not refreshed");
if (!selectors.includes("[data-coord-sidebar-dynamic]")) throw new Error("attention summary was not refreshed");
`
	command := exec.Command("node", "-e", program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("coord attention invalidation mapping failed: %v\n%s", err, output)
	}
}

func TestCoordProgressiveNavigationHandlesThreadDrawerPopstateAndRefreshRetry(t *testing.T) {
	source := string(mustReadEmbedded(t, "static/app.js"))
	applyStart := strings.Index(source, "  const applyCoordPage =")
	applyEnd := strings.Index(source, "  const fetchCoordPage =")
	if applyStart < 0 || applyEnd <= applyStart {
		t.Fatal("coord page apply function missing")
	}
	applySource := source[applyStart:applyEnd]
	for _, want := range []string{"drawerWasOpen", "openSelectedThread()"} {
		if !strings.Contains(applySource, want) {
			t.Errorf("progressive page apply missing %q", want)
		}
	}
	for _, want := range []string{
		"coordFailedPopstateTarget(result, location.href)",
		"refreshRetry.failure()", "refreshRetry.success()",
	} {
		if !strings.Contains(source, want) {
			t.Errorf("progressive recovery path missing %q", want)
		}
	}

	start := strings.Index(source, "  const coordPageLoadApplied =")
	end := strings.Index(source, "  const liveStatus =")
	if start < 0 || end <= start {
		t.Fatal("progressive load/retry helpers are not independently testable")
	}
	program := source[start:end] + `
const current = "https://ghosttree.test/ui/coord?room=project%3Ap";
if (coordFailedPopstateTarget(coordPageLoadApplied, current) !== "") throw new Error("applied popstate forced reload");
if (coordFailedPopstateTarget(coordPageLoadSuperseded, current) !== "") throw new Error("superseded popstate overrode newer navigation");
if (coordFailedPopstateTarget(coordPageLoadForbidden, current) !== "/ui/coord") throw new Error("forbidden popstate did not escape to safe root");
if (coordFailedPopstateTarget(coordPageLoadFailed, current) !== current) throw new Error("failed popstate left stale DOM under URL");

const scheduled = [];
const retry = createCoordRefreshRetry((delay) => scheduled.push(delay));
retry.failure();
if (scheduled.join(",") !== "250") throw new Error("first transient failure was not retried with backoff");
retry.success();
retry.failure();
if (scheduled.join(",") !== "250,250") throw new Error("successful recovery did not reset backoff");
retry.failure(); retry.failure(); retry.failure();
if (scheduled.length !== 4 || scheduled[2] !== 1000 || scheduled[3] !== 3000) throw new Error("retry was not bounded");
`
	command := exec.Command("node", "-e", program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("progressive navigation recovery behavior failed: %v\n%s", err, output)
	}
}

func TestCoordSSERequiresBrowserSessionAndRejectsBadCursor(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h := New(st)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/ui/coord/events", nil))
	if res.Code != http.StatusSeeOther || res.Header().Get("Location") != "/ui/login" {
		t.Fatalf("anonymous status=%d location=%q", res.Code, res.Header().Get("Location"))
	}

	srv, _, client := signedIn(t)
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/ui/coord/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Last-Event-ID", "not-a-number")
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	response, err := client.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	// Ein ungültiger Cursor ist kein Fehler mit Auskunft, sondern ein Resync.
	if response.StatusCode != http.StatusOK || !strings.Contains(string(data), "event: resync") {
		t.Fatalf("bad cursor status=%d body=%q", response.StatusCode, data)
	}
}

func TestCoordSSEReplaysAuthorizedInvalidationWithoutContent(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/y")
	materializeWebRoom(t, st, room)
	if _, err := st.AppendCoordMessage(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "fixture:" + room, ClientID: "sse-visible", Body: "must not enter SSE"}); err != nil {
		t.Fatal(err)
	}
	body, headers := readFiniteSSE(t, client, srv.URL+"/ui/coord/events?after=0", "")
	if headers.Get("Content-Type") != "text/event-stream" || !strings.Contains(body, "event: coord.changed") || !strings.Contains(body, `"object_id":"`+room+`"`) {
		t.Fatalf("headers=%v stream=%q", headers, body)
	}
	if strings.Contains(body, "must not enter SSE") {
		t.Fatalf("stream included canonical content: %q", body)
	}
}

func TestCoordSSEReplayFiltersRevokedPrivateMembership(t *testing.T) {
	srv, st, client := signedIn(t)
	shared := store.RoomKeyForProject("github.com/x/y")
	materializeWebRoom(t, st, shared)
	if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: "peer", PrincipalID: "person:2", Provider: "test", RoomKey: shared}); err != nil {
		t.Fatal(err)
	}
	access := st.CoordinationFor(store.Principal{ID: "person:1"}, "")
	group, err := access.CreateGroup(store.GroupInput{Label: "secret", Members: []string{"person:1", "person:2"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := access.Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: group.Key, ClientID: "secret", Body: "private"}); err != nil {
		t.Fatal(err)
	}
	if err := access.UpdateGroup(store.GroupUpdate{RoomKey: group.Key, Actor: "person:1", AddManagers: []string{"person:2"}}); err != nil {
		t.Fatal(err)
	}
	peerAccess := st.CoordinationFor(store.Principal{ID: "person:2"}, "")
	if err := peerAccess.UpdateGroup(store.GroupUpdate{RoomKey: group.Key, Actor: "person:2", Remove: []string{"person:1"}}); err != nil {
		t.Fatal(err)
	}
	body, _ := readFiniteSSE(t, client, srv.URL+"/ui/coord/events?after=0", "")
	if strings.Contains(body, group.Key) {
		t.Fatalf("revoked group leaked in stream: %q", body)
	}
	if !strings.Contains(body, "event: resync") || strings.Contains(body, `"object_kind":"principal"`) {
		t.Fatalf("revoked member did not receive opaque resync: %q", body)
	}
}

func TestCoordSSETooOldCursorEmitsResync(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/y")
	materializeWebRoom(t, st, room)
	first, _ := readFiniteSSE(t, client, srv.URL+"/ui/coord/events?after=0", "")
	oldToken := lastSSEID(t, first)
	for i := 0; i < 520; i++ {
		if _, err := st.AppendCoordMessage(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "fixture:" + room, ClientID: "sse-prune-" + strconv.Itoa(i), Body: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	body, _ := readFiniteSSE(t, client, srv.URL+"/ui/coord/events", oldToken)
	if !strings.Contains(body, "event: resync") {
		t.Fatalf("stream=%q", body)
	}
}

// lastSSEID liest die id: der letzten Meldung eines Streams.
func lastSSEID(t *testing.T, stream string) string {
	t.Helper()
	id := ""
	for _, line := range strings.Split(stream, "\n") {
		if strings.HasPrefix(line, "id: ") {
			id = strings.TrimPrefix(line, "id: ")
		}
	}
	if id == "" {
		t.Fatalf("no id in stream %q", stream)
	}
	return id
}

func TestCoordSSEStopsWhenRequestContextIsCancelled(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	a := &app{store: st, sessions: newSessions()}
	principal := store.Principal{ID: "person:1", Label: "robin"}
	sessionID, err := a.sessions.create(principal)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/ui/coord/events", nil).WithContext(ctx)
	req = req.WithContext(context.WithValue(req.Context(), personKey{}, principal))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: sessionID})
	done := make(chan struct{})
	go func() {
		a.coordEvents(httptest.NewRecorder(), req)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SSE handler ignored request cancellation")
	}
}

func TestCoordSSESignalsTerminalBrowserSessionRemoval(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	a := &app{store: st, sessions: newSessions()}
	principal := store.Principal{ID: "person:1", Label: "robin"}
	sessionID, err := a.sessions.create(principal)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/ui/coord/events", nil)
	req = req.WithContext(context.WithValue(req.Context(), personKey{}, principal))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: sessionID})
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		a.coordEvents(recorder, req)
		close(done)
	}()
	a.sessions.remove(sessionID)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SSE handler did not end after browser session removal")
	}
	if stream := recorder.Body.String(); !strings.Contains(stream, "event: session-ended") {
		t.Fatalf("stream=%q", stream)
	}
}

func readFiniteSSE(t *testing.T, client *http.Client, target, lastID string) (string, http.Header) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	return string(data), res.Header
}

func mustReadEmbedded(t *testing.T, name string) []byte {
	t.Helper()
	data, err := files.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCoordSSEEndsWhenTokenIsRevoked(t *testing.T) {
	old := sessionRecheckInterval
	sessionRecheckInterval = 100 * time.Millisecond
	t.Cleanup(func() { sessionRecheckInterval = old })
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	token, err := st.AddPerson("robin")
	if err != nil {
		t.Fatal(err)
	}
	principal, ok := st.AuthenticatePrincipal(token)
	if !ok {
		t.Fatal("token rejected")
	}
	a := &app{store: st, sessions: newSessions()}
	sessionID, err := a.sessions.create(principal)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/ui/coord/events", nil)
	req = req.WithContext(context.WithValue(req.Context(), personKey{}, principal))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: sessionID})
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		a.coordEvents(recorder, req)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("stream ended while the token was still valid")
	case <-time.After(400 * time.Millisecond):
	}
	if err := st.RevokeToken(principal.TokenID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream stayed open after the token was revoked")
	}
	if !strings.Contains(recorder.Body.String(), "event: session-ended") {
		t.Fatalf("missing session-ended: %q", recorder.Body.String())
	}
	if _, ok := a.sessions.get(sessionID); ok {
		t.Fatal("browser session survived")
	}
}
