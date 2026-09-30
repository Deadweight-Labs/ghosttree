package web

import (
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// signedIn baut eine angemeldete Oberfläche. Die Herkunft eines menschlichen
// Beitrags hängt an genau dieser Anmeldung, deshalb geht der Test durch den
// echten Login statt am Cookie vorbei.
func signedIn(t *testing.T) (*httptest.Server, *store.Store, *http.Client) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("robin")
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	res := sameOriginPostForm(t, client, srv.URL+"/ui/login", url.Values{"token": {token}})
	res.Body.Close()
	return srv, st, client
}

func materializeWebRoom(t *testing.T, st *store.Store, room string) {
	t.Helper()
	if _, err := st.RegisterCoordAgent(store.CoordAgent{
		ExternalID: "fixture:" + room, PrincipalID: "person:1", Person: "robin",
		Provider: "test", DisplayName: "fixture", RoomKey: room,
	}); err != nil {
		t.Fatal(err)
	}
}

func authenticatedPostForm(t *testing.T, client *http.Client, target string, form url.Values) *http.Response {
	t.Helper()
	form.Set("csrf_token", renderedCSRFToken(t, client, strings.Split(target, "/ui/")[0]+"/ui/coord"))
	req, err := http.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", u.Scheme+"://"+u.Host)
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func renderedCSRFToken(t *testing.T, client *http.Client, pageURL string) string {
	t.Helper()
	page, err := client.Get(pageURL)
	if err != nil {
		t.Fatal(err)
	}
	markup, err := io.ReadAll(page.Body)
	page.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	const prefix = `name="csrf_token" value="`
	start := strings.Index(string(markup), prefix)
	if start < 0 {
		t.Fatal("authenticated page did not render a CSRF token")
	}
	value := string(markup)[start+len(prefix):]
	end := strings.IndexByte(value, '"')
	if end < 0 {
		t.Fatal("rendered CSRF token was not terminated")
	}
	return value[:end]
}

func TestTaskThreadCanBeCreatedOpenedAndDiscussedWithoutJavaScript(t *testing.T) {
	srv, st, client := signedIn(t)
	project := "github.com/x/y"
	room := store.RoomKeyForProject(project)
	materializeWebRoom(t, st, room)
	anchor, err := st.AppendCoordMessage(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room,
		SenderExternalID: "fixture:" + room, ClientID: "thread-anchor", Body: "Release is blocked",
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{
		Type: "feature", Title: "Ship release", Scope: scope.Axes{Project: project},
	}})
	if err != nil {
		t.Fatal(err)
	}

	res := authenticatedPostForm(t, client, srv.URL+"/ui/coord/thread/create", url.Values{
		"room": {room}, "anchor_message_id": {strconv.FormatInt(anchor, 10)},
		"title": {"Investigate release"}, "question": {"What failed?"}, "request_id": {req.Request.HumanID()},
	})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("create status=%d body=%s", res.StatusCode, body(t, res))
	}
	location := res.Header.Get("Location")
	res.Body.Close()
	if !strings.Contains(location, "thread=") {
		t.Fatalf("create redirect=%q", location)
	}

	page := coordPageBody(t, client, srv.URL+location)
	for _, want := range []string{"Investigate release", "What failed?", req.Request.HumanID(), "Ship release", "open", "Release is blocked"} {
		if !strings.Contains(page, want) {
			t.Errorf("thread page missing %q", want)
		}
	}
	requestPage := coordPageBody(t, client, srv.URL+"/ui/requests/"+strconv.FormatInt(req.Request.ID, 10))
	if !strings.Contains(requestPage, "Investigate release") || !strings.Contains(requestPage, "thread=") {
		t.Fatalf("request page did not link its authorized discussion: %s", requestPage)
	}
	locationURL, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	threadIDText := locationURL.Query().Get("thread")
	threadID, err := strconv.ParseInt(threadIDText, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	res = authenticatedPostForm(t, client, srv.URL+"/ui/coord/thread/post", url.Values{
		"thread_id": {threadIDText}, "body": {"I can reproduce it"}, "form_id": {"thread-post"},
	})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("post status=%d body=%s", res.StatusCode, body(t, res))
	}
	res.Body.Close()
	posts, err := st.CoordMessagesSince(store.DestinationDiscussion, store.ThreadDestinationID(threadID), 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 1 || posts[0].Body != "I can reproduce it" {
		t.Fatalf("posts=%+v", posts)
	}

	res = authenticatedPostForm(t, client, srv.URL+"/ui/coord/thread/state", url.Values{
		"thread_id": {threadIDText}, "state": {store.ThreadResolved},
	})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("state status=%d body=%s", res.StatusCode, body(t, res))
	}
	res.Body.Close()
	stillOpen, err := st.RequestByID(req.Request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stillOpen.Request.State != "open" {
		t.Fatalf("thread state changed request to %q", stillOpen.Request.State)
	}
}

func TestCoordThreadPathFocusesItsAuthorizedHome(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/y")
	materializeWebRoom(t, st, room)
	anchor, err := st.AppendCoordMessage(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "fixture:" + room, ClientID: "focus-anchor", Body: "Focus me"})
	if err != nil {
		t.Fatal(err)
	}
	threadID, err := st.CoordinationFor(store.Principal{ID: "person:1", Label: "robin"}, "").PromoteRoomMessageToTaskThread(anchor, "Focused", "", "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := client.Get(srv.URL + "/ui/coord/thread/" + strconv.FormatInt(threadID, 10))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("status=%d body=%s", res.StatusCode, body(t, res))
	}
	if got := res.Header.Get("Location"); !strings.Contains(got, url.QueryEscape(room)) || !strings.Contains(got, "thread=") {
		t.Fatalf("location=%q", got)
	}
	res.Body.Close()
}

func TestRoomTaskThreadCanBeCreatedWithoutAnchor(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/y")
	materializeWebRoom(t, st, room)
	res := authenticatedPostForm(t, client, srv.URL+"/ui/coord/thread/create", url.Values{
		"room": {room}, "title": {"Room task"}, "question": {"What next?"},
	})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("status=%d body=%s", res.StatusCode, body(t, res))
	}
	location := res.Header.Get("Location")
	res.Body.Close()
	page := coordPageBody(t, client, srv.URL+location)
	if !strings.Contains(page, "Room task") || strings.Contains(page, "Zur Ankernachricht") {
		t.Fatalf("unexpected thread page: %s", page)
	}
}

// AC-4 von REQ-350: der Mensch schreibt aus der authentifizierten Oberfläche
// in einen Raum, und der Beitrag trägt seine Herkunft. Bis hierhin war die
// Operator-Oberfläche schreibfrei.
func TestAHumanWritesIntoARoomAndTheMessageCarriesThatOrigin(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/y")
	materializeWebRoom(t, st, room)
	res := authenticatedPostForm(t, client, srv.URL+"/ui/coord/send", url.Values{
		"room": {room}, "body": {"für diesen Release nur additive Änderungen"}, "standing": {"1"}})
	res.Body.Close()

	msgs, err := st.CoordMessagesSince(store.DestinationRoom, room, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("want one message, got %d", len(msgs))
	}
	if msgs[0].AuthorKind != store.AuthorHuman {
		t.Fatalf("a message from the signed-in UI must be human, got %q", msgs[0].AuthorKind)
	}
	if msgs[0].SenderExternalID != "person:1" {
		t.Fatalf("the sender must be the stable signed-in principal, got %q", msgs[0].SenderExternalID)
	}
	if msgs[0].Intent == store.IntentStanding {
		t.Fatal("normal composer accepted a forged standing flag")
	}
}

// Eine gezielte Vorgabe bleibt sichtbar und fällt nicht durch nachfolgende
// Nachrichten aus dem Blickfeld. Spec §A4 und §11 — sie endet mit ihrer
// Aufgabe oder durch eine ausdrückliche Geste, nicht durch Chatverkehr.
func TestAStandingInstructionSurvivesLaterTraffic(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/y")
	materializeWebRoom(t, st, room)
	if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: "sess-backend", PrincipalID: "person:2", Person: "peer", Provider: "test", DisplayName: "backend", RoomKey: room}); err != nil {
		t.Fatal(err)
	}

	res := authenticatedPostForm(t, client, srv.URL+"/ui/coord/standing/create", url.Values{
		"room": {room}, "body": {"keine Breaking Changes in diesem Release"},
		"confirm_scope": {"1"}, "mentions": {"sess-backend"}})
	res.Body.Close()

	// Fünfzig weitere Nachrichten.
	for i := 0; i < 50; i++ {
		if _, err := st.AppendCoordMessage(store.CoordMessage{
			DestinationKind: store.DestinationRoom, DestinationID: room,
			SenderExternalID: "sess-a", ClientID: store.FormatMessageID(int64(i)),
			Body: "Geplauder"}); err != nil {
			t.Fatal(err)
		}
	}

	standing, err := st.StandingInstructions(room)
	if err != nil {
		t.Fatal(err)
	}
	if len(standing) != 1 {
		t.Fatalf("the instruction must still hold after 50 messages, got %d", len(standing))
	}
	if standing[0].Person != "robin" {
		t.Errorf("an instruction without a named person is a rule without an author: %+v", standing[0])
	}
	if len(standing[0].Targets) != 1 || standing[0].Targets[0] != "sess-backend" {
		t.Errorf("targets must survive: %+v", standing[0].Targets)
	}
	page := coordPageBody(t, client, srv.URL+"/ui/coord?room="+url.QueryEscape(room))
	for _, want := range []string{"Adressaten:", "sess-backend", `<time datetime="`, "Gilt in"} {
		if !strings.Contains(page, want) {
			t.Errorf("standing card missing %q", want)
		}
	}

	// Und sie endet nur ausdrücklich.
	res = authenticatedPostForm(t, client, srv.URL+"/ui/coord/standing/end", url.Values{
		"room": {room}, "message_id": {standing[0].MessageID}})
	res.Body.Close()

	after, err := st.StandingInstructions(room)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Fatalf("the instruction should be ended, got %+v", after)
	}
}

// Ein Doppelklick oder Reload darf keine zweite Vorgabe erzeugen. Beim
// Menschen passiert das häufiger als beim Agenten, und die Folge wäre
// dieselbe Anweisung zweimal im Raum.
func TestResubmittingTheSameFormDoesNotDuplicate(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/y")
	materializeWebRoom(t, st, room)
	form := url.Values{"room": {room}, "body": {"einmal"}, "form_id": {"stabil-1"}}

	for i := 0; i < 2; i++ {
		res := authenticatedPostForm(t, client, srv.URL+"/ui/coord/send", form)
		res.Body.Close()
	}
	msgs, err := st.CoordMessagesSince(store.DestinationRoom, room, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("a resubmitted form created %d messages", len(msgs))
	}
}

// Ohne Anmeldung kein Beitrag. Sonst wäre die Herkunft eines menschlichen
// Beitrags eine Behauptung wie jede andere.
func TestWritingRequiresBeingSignedIn(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	res, err := client.PostForm(srv.URL+"/ui/coord/send", url.Values{
		"room": {"project:x"}, "body": {"ich bin angeblich Robin"}})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("an anonymous write must be redirected to login, got %d", res.StatusCode)
	}
	msgs, err := st.CoordMessagesSince(store.DestinationRoom, "project:x", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("an anonymous write stored %d messages", len(msgs))
	}
}

func TestBrowserCoordUsesStablePrincipalAndHidesUnknownPrivateRooms(t *testing.T) {
	srv, st, client := signedIn(t)
	project := store.RoomKeyForProject("github.com/x/y")
	if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: "sess-a", PrincipalID: "person:1", Person: "robin", Provider: "test", RoomKey: project}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: "sess-b", PrincipalID: "person:1", Person: "robin", Provider: "test", RoomKey: project}); err != nil {
		t.Fatal(err)
	}
	direct := store.RoomKeyForDirect([]string{"sess-a", "sess-b"})
	if err := st.EnsureCoordRoom(store.CoordRoom{Key: direct, Kind: store.RoomDirect, Members: []string{"sess-a", "sess-b"}}); err != nil {
		t.Fatal(err)
	}

	for _, room := range []string{store.RoomKeyForProject("github.com/x/missing"), direct} {
		res, err := client.Get(srv.URL + "/ui/coord?room=" + url.QueryEscape(room))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("room %q: want 404, got %d", room, res.StatusCode)
		}
	}
}

func TestCoordMutationRejectsMissingCSRF(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/y")
	for _, tc := range []struct {
		name  string
		token string
	}{
		{name: "missing"},
		{name: "wrong", token: "not-the-session-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{"room": {room}, "body": {"unsafe"}}
			if tc.token != "" {
				form.Set("csrf_token", tc.token)
			}
			res := sameOriginPostForm(t, client, srv.URL+"/ui/coord/send", form)
			defer res.Body.Close()
			if res.StatusCode != http.StatusForbidden {
				t.Fatalf("status=%d", res.StatusCode)
			}
		})
	}
	msgs, err := st.CoordMessagesSince(store.DestinationRoom, room, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("invalid CSRF stored %d messages", len(msgs))
	}
}

func TestCoordMutationRejectsTokenFromAnotherSession(t *testing.T) {
	srv, st, client := signedIn(t)
	otherToken, err := st.AddPerson("other")
	if err != nil {
		t.Fatal(err)
	}
	otherJar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	other := &http.Client{Jar: otherJar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	login := sameOriginPostForm(t, other, srv.URL+"/ui/login", url.Values{"token": {otherToken}})
	login.Body.Close()
	foreignToken := renderedCSRFToken(t, other, srv.URL+"/ui/coord")
	room := store.RoomKeyForProject("github.com/x/y")
	res := sameOriginPostForm(t, client, srv.URL+"/ui/coord/send", url.Values{
		"room": {room}, "body": {"unsafe"}, "csrf_token": {foreignToken},
	})
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d", res.StatusCode)
	}
	msgs, err := st.CoordMessagesSince(store.DestinationRoom, room, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("foreign CSRF token stored %d messages", len(msgs))
	}
}

func TestCSRFMiddlewareCoversLogoutAndStandingEnd(t *testing.T) {
	srv, _, client := signedIn(t)
	for _, path := range []string{"/ui/logout", "/ui/coord/standing/end"} {
		res := sameOriginPostForm(t, client, srv.URL+path, url.Values{})
		res.Body.Close()
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status=%d", path, res.StatusCode)
		}
	}
	res, err := client.Get(srv.URL + "/ui/coord")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("failed logout CSRF ended the session: status=%d", res.StatusCode)
	}
}

func TestCoordMutationRejectsInvalidOrigin(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/y")
	for _, origin := range []string{"", "https://evil.example"} {
		form := url.Values{
			"room":       {room},
			"body":       {"unsafe"},
			"csrf_token": {renderedCSRFToken(t, client, srv.URL+"/ui/coord")},
		}
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/ui/coord/send", strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusForbidden {
			t.Fatalf("origin %q: status=%d", origin, res.StatusCode)
		}
	}
	msgs, err := st.CoordMessagesSince(store.DestinationRoom, room, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("invalid origins stored %d messages", len(msgs))
	}
}

// Angemeldet zu sein ist NICHT dasselbe wie in einem Raum zu sein. Ein
// privates Gespräch zwischen zwei Agenten wird nicht dadurch lesbar, dass ein
// Mensch dessen Schlüssel in die URL schreibt. Spec §9 — eine URL ist keine
// Ausnahme von "private DMs bleiben privat".
//
// Aus einem automatischen Security-Review: der erste Entwurf des Composers
// prüfte gar keine Mitgliedschaft.
func TestBeingSignedInIsNotBeingInTheRoom(t *testing.T) {
	srv, st, client := signedIn(t)

	fremd := store.RoomKeyForDirect([]string{"sess-a", "sess-b"})
	if err := st.EnsureCoordRoom(store.CoordRoom{Key: fremd, Kind: store.RoomDirect,
		Members: []string{"sess-a", "sess-b"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendCoordMessage(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: fremd,
		SenderExternalID: "sess-a", ClientID: "c-1", Body: "vertraulich"}); err != nil {
		t.Fatal(err)
	}

	res, err := client.Get(srv.URL + "/ui/coord?room=" + fremd)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("reading a foreign private room: want 404, got %d", res.StatusCode)
	}

	// Und hineinschreiben geht auch nicht.
	post := authenticatedPostForm(t, client, srv.URL+"/ui/coord/send", url.Values{
		"room": {fremd}, "body": {"ich mische mich ein"}})
	defer post.Body.Close()
	if post.StatusCode != http.StatusNotFound {
		t.Fatalf("writing into a foreign private room: want 404, got %d", post.StatusCode)
	}

	msgs, err := st.CoordMessagesSince(store.DestinationRoom, fremd, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("the foreign room gained a message: %+v", msgs)
	}
}

// Der Projektraum bleibt für den angemeldeten Menschen offen: dort
// entscheidet der Perimeter, nicht eine Mitgliederliste.
func TestAProjectRoomStaysOpenToTheSignedInHuman(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/y")
	materializeWebRoom(t, st, room)
	res, err := client.Get(srv.URL + "/ui/coord?room=" + room)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("a project room must stay readable, got %d", res.StatusCode)
	}
}

func TestCoordRoomRendersLatestFiftyAndSequencePages(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/paging")
	materializeWebRoom(t, st, room)
	for i := 1; i <= 75; i++ {
		if _, err := st.AppendCoordMessage(store.CoordMessage{
			DestinationKind: store.DestinationRoom, DestinationID: room,
			SenderExternalID: "sess-other", ClientID: store.FormatMessageID(int64(i)), Body: "message",
		}); err != nil {
			t.Fatal(err)
		}
	}

	latest := coordPageBody(t, client, srv.URL+"/ui/coord?room="+url.QueryEscape(room))
	if !strings.Contains(latest, "#26 ·") || !strings.Contains(latest, "#75 ·") || strings.Contains(latest, "#25 ·") {
		t.Fatalf("latest page did not render 26..75")
	}
	before := coordPageBody(t, client, srv.URL+"/ui/coord?room="+url.QueryEscape(room)+"&before=26")
	if !strings.Contains(before, "#1 ·") || !strings.Contains(before, "#25 ·") || strings.Contains(before, "#26 ·") {
		t.Fatalf("before page did not render 1..25")
	}
	after := coordPageBody(t, client, srv.URL+"/ui/coord?room="+url.QueryEscape(room)+"&after=25")
	if !strings.Contains(after, "#26 ·") || !strings.Contains(after, "#75 ·") || strings.Contains(after, "#25 ·") {
		t.Fatalf("after page did not use an exclusive sequence boundary")
	}
	emptyAfter := coordPageBody(t, client, srv.URL+"/ui/coord?room="+url.QueryEscape(room)+"&after=75")
	if !strings.Contains(emptyAfter, `before=75`) {
		t.Fatalf("empty after page has no usable older navigation")
	}
	if strings.Contains(emptyAfter, "before=0") {
		t.Fatal("empty after page rendered an invalid before=0 link")
	}
	around := coordPageBody(t, client, srv.URL+"/ui/coord?room="+url.QueryEscape(room)+"&around=25")
	if !strings.Contains(around, "#25 ·") || !strings.Contains(around, `id="message-25"`) || strings.Contains(around, "#75 ·") {
		t.Fatalf("around page did not include its requested anchor")
	}
}

func TestCoordGETDoesNotMarkMessagesRead(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/read")
	materializeWebRoom(t, st, room)
	if _, err := st.AppendCoordMessage(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "other", ClientID: "one", Body: "one"}); err != nil {
		t.Fatal(err)
	}
	res, err := client.Get(srv.URL + "/ui/coord?room=" + url.QueryEscape(room))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	state, err := st.CoordReadState("person:1", store.DestinationRoom, room)
	if err != nil {
		t.Fatal(err)
	}
	if state.ReadThrough != 0 {
		t.Fatalf("GET changed read state: %+v", state)
	}
}

func TestCoordReadAndUnreadAreExplicitCSRFMutations(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/read")
	materializeWebRoom(t, st, room)
	for i := int64(1); i <= 3; i++ {
		if _, err := st.AppendCoordMessage(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "other", ClientID: store.FormatMessageID(i), Body: "message"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, change := range []struct{ path, sequence string }{{"read", "3"}, {"unread", "2"}} {
		res := authenticatedPostForm(t, client, srv.URL+"/ui/coord/"+change.path, url.Values{"room": {room}, "sequence": {change.sequence}})
		res.Body.Close()
		if res.StatusCode != http.StatusSeeOther {
			t.Fatalf("%s status=%d", change.path, res.StatusCode)
		}
	}
	state, err := st.CoordReadState("person:1", store.DestinationRoom, room)
	if err != nil {
		t.Fatal(err)
	}
	if state.ReadThrough != 3 || state.ManualUnreadFrom != 2 {
		t.Fatalf("state=%+v", state)
	}

	res := sameOriginPostForm(t, client, srv.URL+"/ui/coord/read", url.Values{"room": {room}, "sequence": {"3"}})
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("read without CSRF status=%d", res.StatusCode)
	}
}

func TestCoordReadRoutesHidePrivateRoomsAndRejectInvalidPaging(t *testing.T) {
	srv, st, client := signedIn(t)
	private := store.RoomKeyForDirect([]string{"sess-a", "sess-b"})
	if err := st.EnsureCoordRoom(store.CoordRoom{Key: private, Kind: store.RoomDirect, Members: []string{"sess-a", "sess-b"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendCoordMessage(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: private, SenderExternalID: "sess-a", ClientID: "secret", Body: "secret"}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"read", "unread"} {
		res := authenticatedPostForm(t, client, srv.URL+"/ui/coord/"+path, url.Values{"room": {private}, "sequence": {"1"}})
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("%s private status=%d", path, res.StatusCode)
		}
	}
	res, err := client.Get(srv.URL + "/ui/coord?room=" + url.QueryEscape(private) + "&before=0")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	// Authorization runs before parsing, so a private room remains 404 even
	// when its paging cursor is malformed.
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("private invalid paging status=%d", res.StatusCode)
	}

	room := store.RoomKeyForProject("github.com/x/valid")
	materializeWebRoom(t, st, room)
	res, err = client.Get(srv.URL + "/ui/coord?room=" + url.QueryEscape(room) + "&before=0")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid paging status=%d", res.StatusCode)
	}
}

func TestCoordReadValidationReturnsBadRequest(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/validation")
	materializeWebRoom(t, st, room)
	if _, err := st.AppendCoordMessage(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "other", ClientID: "one", Body: "one"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, sequence string }{
		{path: "read", sequence: "2"},
		{path: "unread", sequence: "2"},
	} {
		res := authenticatedPostForm(t, client, srv.URL+"/ui/coord/"+tc.path, url.Values{"room": {room}, "sequence": {tc.sequence}})
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s invalid sequence status=%d", tc.path, res.StatusCode)
		}
	}
	res, err := client.Get(srv.URL + "/ui/coord?room=" + url.QueryEscape(room) + "&after=2")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("future window status=%d", res.StatusCode)
	}
}

func TestCoordWorkspaceRendersAllRoomSectionsAndNewestWindow(t *testing.T) {
	srv, st, client := signedIn(t)
	project := store.RoomKeyForProject("github.com/x/workspace")
	machine := store.RoomKeyForMachine("mainex")
	materializeWebRoom(t, st, project)
	materializeWebRoom(t, st, machine)
	for i := 1; i <= 75; i++ {
		if _, err := st.AppendCoordMessage(store.CoordMessage{
			DestinationKind: store.DestinationRoom, DestinationID: project,
			SenderExternalID: "sess-other", ClientID: store.FormatMessageID(int64(i)), Body: "message",
		}); err != nil {
			t.Fatal(err)
		}
	}

	html := coordPageBody(t, client, srv.URL+"/ui/coord?room="+url.QueryEscape(project))
	for _, want := range []string{"Aufmerksamkeit", "Maschine", "Projekte", "Direkt &amp; Gruppen", "#75", "Teilnehmende", "Threads"} {
		if !strings.Contains(html, want) {
			t.Errorf("workspace missing %q", want)
		}
	}
	if strings.Contains(html, "#25") {
		t.Fatal("workspace rendered the oldest page instead of the newest window")
	}
	for _, want := range []string{`class="coord-shell"`, `class="coord-sidebar`, `class="coord-conversation`, `class="coord-context`} {
		if !strings.Contains(html, want) {
			t.Errorf("workspace landmark missing %q", want)
		}
	}
	if strings.Count(html, "<main") != 1 {
		t.Fatalf("workspace must have exactly one main landmark")
	}
	for _, want := range []string{`aria-label="Räume"`, `aria-label="Unterhaltung"`, `aria-label="Raumkontext"`, `aria-live="polite"`} {
		if !strings.Contains(html, want) {
			t.Errorf("accessible workspace contract missing %q", want)
		}
	}
	for _, want := range []string{`href="#coord-rooms"`, `href="#coord-context"`, `aria-controls="coord-rooms"`, `aria-controls="coord-context"`, `aria-expanded="false"`} {
		if !strings.Contains(html, want) {
			t.Errorf("drawer contract missing %q", want)
		}
	}
}

func TestCoordWorkspaceStructureUsesChatLandmarksAndOneAttentionRegion(t *testing.T) {
	templateBytes, err := files.ReadFile("templates/coord.html")
	if err != nil {
		t.Fatal(err)
	}
	template := string(templateBytes)
	for _, want := range []string{
		`class="coord-appbar"`,
		`class="coord-sidebar coord-room-rail"`,
		`class="coord-message coord-message-group{{if .MentionsViewer}} coord-message-mentioned{{end}} {{if .GroupStart}}`,
		`class="coord-message-actions coord-message-tools"`,
	} {
		if !strings.Contains(template, want) {
			t.Errorf("chat workspace structure missing %q", want)
		}
	}
	if got := strings.Count(template, `class="coord-attention-region"`); got != 1 {
		t.Fatalf("attention regions=%d, want exactly one compact region", got)
	}
	if strings.Contains(template, `class="coord-knowledge-slot"`) || strings.Contains(template, `<h2>Knowledge</h2>`) {
		t.Fatal("coordination template must not contain the future Knowledge placeholder")
	}
}

func TestCoordAttentionRegionRendersEachSignalledRoomOnceWithExplicitCounts(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/attention-union")
	materializeWebRoom(t, st, room)
	if _, err := st.AppendCoordMessage(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room,
		SenderExternalID: "reviewer", AuthorPrincipalID: "person:2", AuthorKind: store.AuthorHuman,
		ClientID: "all-signals", Body: "Bitte prüfen", Intent: store.IntentBlocker,
		Mentions: []string{"person:1"},
	}); err != nil {
		t.Fatal(err)
	}

	page := coordPageBody(t, client, srv.URL+"/ui/coord?room="+url.QueryEscape(room))
	start := strings.Index(page, `class="coord-attention-region"`)
	if start < 0 {
		t.Fatal("compact attention region missing")
	}
	end := strings.Index(page[start:], `</section>`)
	if end < 0 {
		t.Fatal("compact attention region is not closed")
	}
	region := page[start : start+end]
	roomURL := `/ui/coord?room=` + url.QueryEscape(room)
	if got := strings.Count(region, roomURL); got != 1 {
		t.Fatalf("signalled room occurrences=%d, want one; region=%s", got, region)
	}
	for _, label := range []string{"1 offen", "1 Erwähnung", "1 Ungelesen"} {
		if !strings.Contains(region, label) {
			t.Errorf("attention row missing explicit signal %q", label)
		}
	}
}

func TestCoordAttentionDetailsHookExistsWhenEmptyAndPopulated(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/attention-details")
	materializeWebRoom(t, st, room)
	empty := coordPageBody(t, client, srv.URL+"/ui/coord?room="+url.QueryEscape(room))
	if !strings.Contains(empty, `data-coord-attention-details`) {
		t.Fatal("empty default inspector must retain the attention replacement hook")
	}
	if _, err := st.AppendCoordMessage(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room,
		SenderExternalID: "reviewer", AuthorPrincipalID: "person:2", AuthorKind: store.AuthorHuman,
		ClientID: "attention-detail", Body: "Antwort nötig", Intent: store.IntentQuestion,
		Mentions: []string{"person:1"},
	}); err != nil {
		t.Fatal(err)
	}
	populated := coordPageBody(t, client, srv.URL+"/ui/coord?room="+url.QueryEscape(room))
	start := strings.Index(populated, `data-coord-attention-details`)
	if start < 0 {
		t.Fatal("populated default inspector lost the attention replacement hook")
	}
	end := strings.Index(populated[start:], `</div>`)
	if end < 0 || !strings.Contains(populated[start:start+end], "Antwort nötig") {
		t.Fatal("populated attention replacement hook must contain actionable detail")
	}
}

func TestCoordMessageStructureExposesGroupingAndHumanAgentAvatarsInBothFeeds(t *testing.T) {
	template := string(mustReadEmbedded(t, "templates/coord.html"))
	for _, want := range []string{
		`coord-message-group-start`, `coord-message-continuation`,
		`coord-message-avatar-human`, `coord-message-avatar-agent`,
		`data-author-kind="{{.AuthorKind}}"`, `<span class="coord-author-kind">Agent</span>`,
	} {
		if !strings.Contains(template, want) {
			t.Errorf("grouped message identity markup missing %q", want)
		}
	}
	if got := strings.Count(template, `{{template "coord-message-identity" .}}`); got != 2 {
		t.Fatalf("message identity render sites=%d, want room and thread feeds", got)
	}
}

func TestCoordComposerKeepsPrimaryInputVisibleAndAdvancedFieldsInNativeDisclosure(t *testing.T) {
	templateBytes, err := files.ReadFile("templates/coord.html")
	if err != nil {
		t.Fatal(err)
	}
	template := string(templateBytes)
	formStart := strings.Index(template, `<form class="coord-composer"`)
	if formStart < 0 {
		t.Fatal("room composer form missing")
	}
	formEnd := strings.Index(template[formStart:], `</form>`)
	if formEnd < 0 {
		t.Fatal("room composer form is not closed")
	}
	composer := template[formStart : formStart+formEnd]
	if !strings.Contains(composer, `class="coord-compose-primary"`) {
		t.Fatal("composer must expose a primary input/action row")
	}
	moreStart := strings.Index(composer, `<details class="coord-compose-more`)
	if moreStart < 0 {
		t.Fatal("advanced composer controls must use a native details disclosure")
	}
	moreEnd := strings.Index(composer[moreStart:], `</details>`)
	if moreEnd < 0 {
		t.Fatal("advanced composer disclosure is not closed")
	}
	advanced := composer[moreStart : moreStart+moreEnd]
	for _, field := range []string{`name="intent"`, `name="mentions"`, `{{template "coord-expiry-field"`} {
		if !strings.Contains(advanced, field) {
			t.Errorf("advanced composer disclosure missing %q", field)
		}
	}
}

func TestCoordThreadContextReplacesDefaultInspector(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/inspector-modes")
	materializeWebRoom(t, st, room)
	access := st.CoordinationFor(store.Principal{ID: "person:1", Label: "robin"}, "")
	threadID, err := access.CreateTaskThreadInRoom(room, "Focused inspector", "Only the task", "")
	if err != nil {
		t.Fatal(err)
	}

	defaultPage := coordPageBody(t, client, srv.URL+"/ui/coord?room="+url.QueryEscape(room))
	if !strings.Contains(defaultPage, `class="coord-context-default"`) || strings.Contains(defaultPage, `class="coord-context-thread`) {
		t.Fatal("room view must render only the default inspector mode")
	}
	threadPage := coordPageBody(t, client, srv.URL+coordThreadURL(room, threadID))
	if !strings.Contains(threadPage, `class="coord-context-thread coord-thread-detail"`) || strings.Contains(threadPage, `class="coord-context-default"`) {
		t.Fatal("selected thread must replace the default inspector mode")
	}
}

func TestCoordWelcomeKeepsRoomPickerReachableWhenEnhanced(t *testing.T) {
	srv, _, client := signedIn(t)
	html := coordPageBody(t, client, srv.URL+"/ui/coord")
	for _, want := range []string{`<h1 class="coord-visually-hidden">Coordination</h1>`, `aria-controls="coord-rooms"`, `data-coord-drawer-target="coord-rooms"`} {
		if !strings.Contains(html, want) {
			t.Errorf("welcome drawer contract missing %q", want)
		}
	}
}

func TestCoordResponsiveEnhancedShellDropsHiddenSidebarColumn(t *testing.T) {
	cssBytes, err := files.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(cssBytes)
	if !strings.Contains(css, `@media (max-width: 1100px)`) ||
		!strings.Contains(coordCSSRule(t, css, `.coord-enhanced .coord-shell`), `grid-template-columns: minmax(0, 1fr);`) {
		t.Fatal("enhanced tablet layout must give the hidden drawers no grid column")
	}
	hidden := coordCSSRule(t, css, `.coord-enhanced .coord-sidebar[hidden],`)
	if !strings.Contains(hidden, `display: none;`) {
		t.Fatal("enhanced drawers must honor the hidden attribute over their display rules")
	}
}

func TestCoordResponsiveDrawersExposeNamedCloseControls(t *testing.T) {
	srv, _, client := signedIn(t)
	html := coordPageBody(t, client, srv.URL+"/ui/coord")
	for _, want := range []string{
		`class="coord-drawer-head"`,
		`class="coord-drawer-close" data-coord-drawer-close aria-label="Räume schließen"`,
		`class="coord-drawer-close" data-coord-drawer-close aria-label="Kontext schließen"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("responsive drawer header missing %q", want)
		}
	}

	cssBytes, err := files.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(cssBytes)
	if !strings.Contains(coordCSSRule(t, css, `.coord-drawer-close`), `display: none;`) ||
		!strings.Contains(coordCSSRule(t, css, `.coord-enhanced .coord-drawer-head`), `display: flex;`) ||
		!strings.Contains(coordCSSRule(t, css, `.coord-enhanced .coord-drawer-close`), `display: inline-grid;`) {
		t.Fatal("drawer close controls must only become visible in the enhanced responsive layout")
	}
	jsBytes, err := files.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(jsBytes)
	for _, want := range []string{
		`const bindDrawerCloseControls = () =>`,
		`querySelectorAll("[data-coord-drawer-close]")`,
		`control.dataset.coordDrawerCloseBound`,
		`const reopenedPanel = document.getElementById(openPanel.id)`,
		`setOutsideInert(reopenedPanel)`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("replace-safe drawer close binding missing %q", want)
		}
	}
}

func TestCoordSelectedThreadOpensResponsiveContextOnInitialLoad(t *testing.T) {
	jsBytes, err := files.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(jsBytes)
	for _, want := range []string{
		`const openSelectedThread = () =>`,
		`document.querySelector(".coord-thread-detail")`,
		`button.dataset.coordDrawerTarget === "coord-context"`,
		"sync();\n  openSelectedThread();",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("initial selected-thread drawer behavior missing %q", want)
		}
	}
}

func TestCoordMessageActionsShareOneTouchSizedHierarchy(t *testing.T) {
	templateBytes, err := files.ReadFile("templates/coord.html")
	if err != nil {
		t.Fatal(err)
	}
	template := string(templateBytes)
	if got := strings.Count(template, `class="coord-message-actions coord-message-tools"`); got != 2 {
		t.Fatalf("message action rows=%d, want room and thread rows", got)
	}
	if !strings.Contains(template, `class="coord-message-action coord-message-action-primary coord-reply-action"`) ||
		!strings.Contains(template, `class="coord-message-action coord-message-action-secondary"`) {
		t.Fatal("message actions must distinguish reply as primary from thread actions")
	}

	cssBytes, err := files.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(cssBytes)
	action := coordCSSRule(t, css, `.coord-message-action {`)
	if !strings.Contains(action, `display: inline-flex;`) ||
		!strings.Contains(action, `min-height: 2.75rem;`) {
		t.Fatal("message actions must expose a 44px-equivalent touch target")
	}
}

func TestCoordTimestampsKeepMachineValueAndShowShortLocalValue(t *testing.T) {
	templateBytes, err := files.ReadFile("templates/coord.html")
	if err != nil {
		t.Fatal(err)
	}
	template := string(templateBytes)
	for _, want := range []string{
		`<time datetime="{{.Timestamp}}" title="{{.Timestamp}}">{{.DisplayTimestamp}}</time>`,
		`<time datetime="{{.CreatedAt}}" title="{{.CreatedAt}}">{{.DisplayTimestamp}}</time>`,
		`<time datetime="{{.LastSeen}}" title="{{.LastSeen}}">{{.DisplayTimestamp}}</time>`,
	} {
		if !strings.Contains(template, want) {
			t.Errorf("coord timestamp markup missing %q", want)
		}
	}
}

func TestCoordLiveBadgeAndMobileToolbarHaveStableCompactContracts(t *testing.T) {
	jsBytes, err := files.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(jsBytes), `setLive("live", "Live verbunden"`) {
		t.Fatal("connected live status must state what is connected")
	}

	cssBytes, err := files.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(cssBytes)
	// Desktop: the badge is a status row in the layout flow. Only the mobile
	// dot floats, and it never takes pointer events.
	live := coordCSSRule(t, css[strings.Index(css, `@media (max-width: 700px)`):], `.coord-live-status:not(.coord-nojs-status)`)
	for _, want := range []string{
		`right: max(.7rem, env(safe-area-inset-right))`,
		`bottom: max(.7rem, env(safe-area-inset-bottom))`,
		`pointer-events: none`,
	} {
		if !strings.Contains(live, want) {
			t.Errorf("stable responsive chrome missing %q", want)
		}
	}
	toolbarLinks := coordCSSRule(t, css, `.app-brand,`)
	if !strings.Contains(toolbarLinks, `min-height: 3rem;`) {
		t.Fatal("mobile toolbar links must retain 44px touch targets")
	}
}

func TestCoordMobilePolishKeepsConversationDenseAndStatusOutOfTheWay(t *testing.T) {
	templateBytes, err := files.ReadFile("templates/coord.html")
	if err != nil {
		t.Fatal(err)
	}
	template := string(templateBytes)
	for _, unwanted := range []string{
		`class="coord-kicker">Zusammenarbeit, die nachvollziehbar bleibt`,
		`Gültig bis (RFC3339)`,
	} {
		if strings.Contains(template, unwanted) {
			t.Errorf("coordination UI retains editorial or implementation-facing copy %q", unwanted)
		}
	}
	for _, want := range []string{
		`coord-nojs-status`,
		`type="datetime-local"`,
		`Zeit in {{.}} (Serverzeit)`,
		`Braucht dich <small>alle Räume</small>`,
		`{{.Attention}} offen`,
		`{{.Unread}} neu`,
	} {
		if !strings.Contains(template, want) {
			t.Errorf("coordination polish missing %q", want)
		}
	}

	cssBytes, err := files.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(cssBytes)
	for _, selector := range []string{
		`.coord-context > .coord-context-thread`,
		`.coord-mentions:not(:has(input))`,
		`.coord-live-status:not(.coord-nojs-status)`,
		`.coord-nojs-status`,
	} {
		if !strings.Contains(css, selector) {
			t.Errorf("coordination polish CSS missing %q", selector)
		}
	}
	if !strings.Contains(css, "padding: 0;\n    border: 0;\n    background: transparent;") {
		t.Fatal("mobile message actions must not render as repeated bordered cards")
	}
}

func TestCoordThreadListSeparatesTitleFromStatus(t *testing.T) {
	cssBytes, err := files.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(cssBytes)
	status := coordCSSRule(t, css, `.coord-threads li > small`)
	if !strings.Contains(status, `display: block;`) || !strings.Contains(status, `margin-top: .2rem;`) {
		t.Fatal("thread status must render on its own spaced line")
	}
}

func TestCoordVisualSystemSeparatesChromeConversationAndInspector(t *testing.T) {
	cssBytes, err := files.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(cssBytes)
	workspace := coordCSSRule(t, css, "\n.coord-workspace {")
	for _, want := range []string{
		`--coord-chrome: #171a19`,
		`--coord-conversation: #f4f0e7`,
		`--coord-inspector: #ebe7de`,
	} {
		if !strings.Contains(workspace, want) {
			t.Errorf("coord visual hierarchy missing %q", want)
		}
	}
	if shell := coordCSSRule(t, css, `.coord-shell`); !strings.Contains(shell, `grid-template-columns: 16rem minmax(0, 1fr) 22rem;`) {
		t.Fatalf("coord desktop columns lost their hierarchy: %s", shell)
	}
	if toggles := coordCSSRule(t, css, `.coord-mobile-actions a,`); !strings.Contains(toggles, `border-radius: 4px;`) {
		t.Fatalf("coord controls exceed the restrained radius contract: %s", toggles)
	}
	for _, forbidden := range []string{`linear-gradient(`, `radial-gradient(`, `backdrop-filter:`, `border-radius: 999px`} {
		if strings.Contains(css, forbidden) {
			t.Errorf("coord visual system contains prohibited slop treatment %q", forbidden)
		}
	}
}

func TestCoordMessageGroupsKeepIdentityAndRevealToolsWithoutCardChrome(t *testing.T) {
	css := string(mustReadEmbedded(t, "static/app.css"))
	for _, want := range []string{
		`.coord-message-group-start`,
		`grid-template-columns: 2rem minmax(0, 1fr)`,
		`.coord-message-continuation`,
		`.coord-message-avatar-agent`,
		`border-radius: var(--coord-radius)`,
		`.coord-message:focus-within .coord-message-tools`,
		`@media (hover: hover) and (pointer: fine)`,
		`@media (hover: none), (pointer: coarse)`,
	} {
		if !strings.Contains(css, want) {
			t.Errorf("message grouping/tool contract missing %q", want)
		}
	}
	if strings.Contains(css, `.coord-message {\n  border:`) || strings.Contains(css, `.coord-message{border:`) {
		t.Fatal("messages must not become individual bordered cards")
	}
}

func TestCoordResponsiveLayoutKeepsFeedScrollableAndComposerVisible(t *testing.T) {
	css := string(mustReadEmbedded(t, "static/app.css"))
	for _, media := range []string{`@media (max-width: 1100px)`, `@media (max-width: 700px)`} {
		if !strings.Contains(css, media) {
			t.Errorf("bounded responsive chat contract missing %q", media)
		}
	}
	mobile := css[strings.Index(css, `@media (max-width: 700px)`):]
	if workspace := coordCSSRule(t, mobile, `html.coord-enhanced .coord-workspace`); !strings.Contains(workspace, `height: calc(100dvh - 3rem);`) {
		t.Fatalf("enhanced mobile workspace is not viewport bound: %s", workspace)
	}
	if messages := coordCSSRule(t, mobile, `.coord-messages`); !strings.Contains(messages, `overflow: auto;`) {
		t.Fatalf("mobile message feed does not own scrolling: %s", messages)
	}
	if composer := coordCSSRule(t, mobile, `.coord-composer`); !strings.Contains(composer, `padding-bottom: max(.75rem, env(safe-area-inset-bottom));`) {
		t.Fatalf("mobile composer lost safe-area padding: %s", composer)
	}
	if hidden := coordCSSRule(t, css, `.coord-enhanced .coord-sidebar[hidden],`); !strings.Contains(hidden, `display: none;`) {
		t.Fatalf("enhanced hidden drawers still occupy layout: %s", hidden)
	}
}

func TestCoordNoJSMobileRestoresDocumentFlowInDOMOrder(t *testing.T) {
	css := string(mustReadEmbedded(t, "static/app.css"))
	if strings.Contains(css, "\nbody:has(> .coord-workspace) {") {
		t.Fatal("viewport locking must not apply before progressive enhancement is active")
	}
	if block := coordCSSRule(t, css, `html.coord-enhanced body:has(> .coord-workspace)`); !strings.Contains(block, `overflow: hidden;`) {
		t.Fatalf("enhanced body rule does not lock viewport: %s", block)
	}
	if block := coordCSSRule(t, css, `html:not(.coord-enhanced) body:has(> .coord-workspace)`); !strings.Contains(block, `overflow: auto;`) {
		t.Fatalf("no-JS body rule does not restore document scroll: %s", block)
	}
	workspace := coordCSSRule(t, css, `html:not(.coord-enhanced) .coord-workspace`)
	for _, want := range []string{`height: auto;`, `min-height: calc(100dvh - 3rem);`, `overflow: visible;`} {
		if !strings.Contains(workspace, want) {
			t.Errorf("no-JS workspace rule missing %q: %s", want, workspace)
		}
	}
	shell := coordCSSRule(t, css, `html:not(.coord-enhanced) .coord-shell`)
	for _, want := range []string{`display: flex;`, `flex-direction: column;`} {
		if !strings.Contains(shell, want) {
			t.Errorf("no-JS sequential shell missing %q: %s", want, shell)
		}
	}
}

func TestCoordNoJSMobilePutsConversationFirstAndHidesDeadControls(t *testing.T) {
	css := string(mustReadEmbedded(t, "static/app.css"))
	narrow := css[strings.Index(css, "@media (max-width: 1100px)"):]
	for _, want := range []string{
		`html:not(.coord-enhanced) .coord-conversation { order: 1; }`,
		`html:not(.coord-enhanced) .coord-sidebar { order: 2; }`,
		`html:not(.coord-enhanced) .coord-context { order: 3; }`,
	} {
		if !strings.Contains(narrow, want) {
			t.Errorf("no-JS narrow flow must lead with the conversation, missing %q", want)
		}
	}
	start := strings.Index(css, "html:not(.coord-enhanced) .coord-drawer-toggle")
	if start < 0 {
		t.Fatal("JS-only drawer close is not hidden without enhancement")
	}
	open := strings.Index(css[start:], "{")
	selectors := css[start : start+open]
	for _, want := range []string{".coord-drawer-toggle", ".coord-drawer-close", ".coord-backdrop"} {
		if !strings.Contains(selectors, "html:not(.coord-enhanced) "+want) {
			t.Errorf("JS-only control %s stays visible without enhancement", want)
		}
	}
	if block := coordCSSRule(t, css, "html:not(.coord-enhanced) .coord-drawer-toggle"); !strings.Contains(block, "display: none !important;") {
		t.Fatalf("dead controls must beat the mobile button display rule: %s", block)
	}
	if strings.Contains(string(mustReadEmbedded(t, "templates/coord.html")), "coord-skip-conversation") {
		t.Fatal("the conversation is first without JS, so the skip link is dead weight")
	}
}

func TestCoordRefreshLinkKeepsTheActiveRoom(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/refresh-link")
	materializeWebRoom(t, st, room)
	res, err := client.Get(srv.URL + "/ui/coord?room=" + url.QueryEscape(room))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	want := `href="/ui/coord?room=` + url.QueryEscape(room) + `" aria-label="Raumliste aktualisieren"`
	if !strings.Contains(strings.ToLower(string(body)), strings.ToLower(want)) {
		t.Fatalf("refresh link loses the room context, want %q", want)
	}
}

func TestCoordHiddenMessageToolsStayFocusableAndOutOfFlow(t *testing.T) {
	css := string(mustReadEmbedded(t, "static/app.css"))
	base := coordCSSRule(t, css, `.coord-message-tools`)
	if !strings.Contains(base, `position: absolute;`) {
		t.Fatalf("message tools remain in message flow: %s", base)
	}
	hoverMedia := css[strings.Index(css, `@media (hover: hover) and (pointer: fine)`):]
	hidden := coordCSSRule(t, hoverMedia, `.coord-message-tools`)
	for _, want := range []string{`opacity: 0;`, `pointer-events: none;`} {
		if !strings.Contains(hidden, want) {
			t.Errorf("pointer-hidden tools rule missing %q: %s", want, hidden)
		}
	}
	focused := coordCSSRule(t, hoverMedia, `.coord-message:hover .coord-message-tools,`)
	for _, want := range []string{`opacity: 1;`, `pointer-events: auto;`} {
		if !strings.Contains(focused, want) {
			t.Errorf("focused tools rule missing %q: %s", want, focused)
		}
	}
	if strings.Contains(css, `visibility: hidden`) {
		t.Fatal("hidden message tools must remain in the keyboard tab sequence")
	}
}

func TestCoordAgentIdentitySitsBesideAuthorAndContinuationKeepsAccessibleName(t *testing.T) {
	template := string(mustReadEmbedded(t, "templates/coord.html"))
	for _, want := range []string{
		`<strong class="{{if not .GroupStart}}coord-visually-hidden{{end}}">{{.Author}}</strong>`,
		`{{if and .GroupStart (eq .AuthorKind "agent")}}<span class="coord-author-kind">Agent</span>{{end}}`,
		`{{if eq .AuthorKind "agent"}}A{{else}}●{{end}}`,
	} {
		if !strings.Contains(template, want) {
			t.Errorf("restrained grouped identity missing %q", want)
		}
	}
	if strings.Contains(template, `>◆<`) {
		t.Fatal("agent avatar must not use the decorative AI-style diamond")
	}
}

func TestCoordContrastFocusAndCoarseTargetsAreExplicit(t *testing.T) {
	css := string(mustReadEmbedded(t, "static/app.css"))
	workspace := coordCSSRule(t, css, "\n.coord-workspace {")
	if !strings.Contains(workspace, `--coord-muted: #565d58`) {
		t.Fatalf("conversation/inspector muted color lost its contrast token: %s", workspace)
	}
	focus := coordCSSRule(t, css, `.coord-shell :focus-visible,`)
	if !strings.Contains(focus, `outline: 3px solid`) {
		t.Fatalf("workspace focus indicator is thinner than three pixels: %s", focus)
	}
	coarseMedia := css[strings.Index(css, `@media (hover: none), (pointer: coarse)`):]
	targets := coordCSSRule(t, coarseMedia, `.coord-workspace button,`)
	targetStart := strings.Index(coarseMedia, `.coord-workspace button,`)
	targetOpen := strings.Index(coarseMedia[targetStart:], `{`)
	targetSelectors := coarseMedia[targetStart : targetStart+targetOpen]
	for _, selector := range []string{`.coord-context a`} {
		if !strings.Contains(targetSelectors, selector) {
			t.Errorf("coarse target selector missing %q", selector)
		}
	}
	if !strings.Contains(targets, `min-height: 2.75rem;`) {
		t.Fatalf("coarse target rule is below 44 CSS pixels: %s", targets)
	}
	if summary := coordCSSRule(t, coarseMedia, `.coord-workspace summary`); !strings.Contains(summary, `min-height: 2.75rem;`) {
		t.Fatalf("coarse summary target is below 44 CSS pixels: %s", summary)
	}
}

func TestCoordCoarseMessageToolsReturnToGridFlowWithoutCoveringHeader(t *testing.T) {
	css := string(mustReadEmbedded(t, "static/app.css"))
	for _, media := range []string{`@media (hover: none), (pointer: coarse)`, `@media (max-width: 700px)`} {
		mediaCSS := css[strings.Index(css, media):]
		tools := coordCSSRule(t, mediaCSS, `.coord-message-tools`)
		for _, want := range []string{
			`position: relative;`,
			`grid-column: 2;`,
			`top: auto;`,
			`right: auto;`,
		} {
			if !strings.Contains(tools, want) {
				t.Errorf("%s message tools still overlap content; missing %q in %s", media, want, tools)
			}
		}
	}
}

func TestCoordTouchTargetsPreserveNativeDetailsMarker(t *testing.T) {
	css := string(mustReadEmbedded(t, "static/app.css"))
	coarseMedia := css[strings.Index(css, `@media (hover: none), (pointer: coarse)`):]
	summary := coordCSSRule(t, coarseMedia, `.coord-workspace summary`)
	if !strings.Contains(summary, `min-height: 2.75rem;`) {
		t.Fatalf("touch summary is below 44 CSS pixels: %s", summary)
	}
	if strings.Contains(summary, `display: inline-flex;`) || strings.Contains(summary, `display: flex;`) {
		t.Fatalf("touch summary overrides its native disclosure marker: %s", summary)
	}
}

func TestCoordInspectorLinksAndAttentionButtonsUseIntentionalFlatStates(t *testing.T) {
	css := string(mustReadEmbedded(t, "static/app.css"))
	links := coordCSSRule(t, css, `:where(.coord-conversation, .coord-context) a:hover`)
	for _, want := range []string{`color: var(--coord-link-hover);`, `text-decoration: underline;`, `text-underline-offset:`} {
		if !strings.Contains(links, want) {
			t.Errorf("inspector link treatment missing %q: %s", want, links)
		}
	}
	buttons := coordCSSRule(t, css, `.coord-attention-card button`)
	for _, want := range []string{`display: inline-flex;`, `border: 1px solid`, `border-radius: var(--coord-radius-sm);`, `background: transparent;`} {
		if !strings.Contains(buttons, want) {
			t.Errorf("attention action treatment missing %q: %s", want, buttons)
		}
	}
}

func coordCSSRule(t *testing.T, css, selector string) string {
	t.Helper()
	start := strings.Index(css, selector)
	if start < 0 {
		t.Fatalf("CSS selector missing %q", selector)
	}
	open := strings.Index(css[start:], "{")
	if open < 0 {
		t.Fatalf("CSS selector %q has no declaration block", selector)
	}
	open += start
	close := strings.Index(css[open:], "}")
	if close < 0 {
		t.Fatalf("CSS selector %q has an unterminated declaration block", selector)
	}
	return css[open+1 : open+close]
}

func TestCoordWorkspaceRendersAuthorizedIdentityLabelsAndHonestPresence(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/identity-ui")
	materializeWebRoom(t, st, room)
	if _, err := st.AddPerson("Alex"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RegisterCoordAgent(store.CoordAgent{
		ExternalID: "sess-peer-secret", PrincipalID: "person:2", Person: "Alex",
		Provider: "codex", DisplayName: "Build Agent", RoomKey: room,
		Worktree: "/worktrees/ui", Branch: "feat/ui",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CoordinationFor(store.Principal{ID: "person:1", Label: "robin"}, "").Send(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room, ClientID: "identity-message",
		Body: "please review", Intent: store.IntentHandoff, Mentions: []string{"sess-peer-secret"},
	}); err != nil {
		t.Fatal(err)
	}
	direct := store.CoordRoom{Key: store.RoomKeyForDirect([]string{"person:1", "person:2"}), Kind: store.RoomDirect, Members: []string{"person:1", "person:2"}}
	if err := st.CoordinationFor(store.Principal{ID: "person:1", Label: "robin"}, "").EnsureDirect(direct); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddPerson("Top Secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RegisterCoordAgent(store.CoordAgent{
		ExternalID: "sess-unrelated-secret", PrincipalID: "person:3", Person: "Top Secret",
		Provider: "test", DisplayName: "Hidden Directory Agent", RoomKey: store.RoomKeyForMachine("other-host"),
	}); err != nil {
		t.Fatal(err)
	}

	html := coordPageBody(t, client, srv.URL+"/ui/coord?room="+url.QueryEscape(room))
	for _, want := range []string{
		"robin (du)", "Build Agent", "Alex", "Erwähnt: Build Agent", "an Build Agent",
		"<span>codex</span>", `title="Worktree: /worktrees/ui"`, "<span>feat/ui</span>",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("identity/presence presentation missing %q", want)
		}
	}
	for _, forbidden := range []string{"Erreichbarkeit: unbekannt", "Arbeitszustand: unbekannt", "Erwähnt: sess-peer-secret", ">sess-peer-secret<", "an sess-peer-secret", "Top Secret", "Hidden Directory Agent"} {
		if strings.Contains(html, forbidden) {
			t.Errorf("coordination UI leaked raw or unauthorized identity %q", forbidden)
		}
	}
}

func TestCoordWorkspaceEscapesAgentContentAndKeepsNoJSForms(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/escape")
	materializeWebRoom(t, st, room)
	for i := int64(1); i <= 50; i++ {
		if _, err := st.AppendCoordMessage(store.CoordMessage{
			DestinationKind: store.DestinationRoom, DestinationID: room,
			SenderExternalID: "safe", ClientID: store.FormatMessageID(i), Body: "safe",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.AppendCoordMessage(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room,
		SenderExternalID: "<img src=x>", ClientID: "unsafe", Body: "<script>alert(1)</script>",
	}); err != nil {
		t.Fatal(err)
	}
	html := coordPageBody(t, client, srv.URL+"/ui/coord?room="+url.QueryEscape(room))
	if strings.Contains(html, "<script>alert(1)</script>") || strings.Contains(html, "<img src=x>") {
		t.Fatal("untrusted coordination content was emitted as markup")
	}
	for _, want := range []string{
		`method="post" action="/ui/coord/send"`,
		`name="room" value="project:github.com/x/escape"`,
		`before=2`,
		`name="csrf_token"`,
		`name="form_id" value="`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("no-JS contract missing %q", want)
		}
	}
	if strings.Contains(html, `<ol class="coord-messages" aria-live=`) {
		t.Fatal("the full message history must not be an aria-live region")
	}
	if !strings.Contains(html, `id="coord-status"`) || !strings.Contains(html, `lang="de"`) {
		t.Fatal("workspace lacks its small status region or language declaration")
	}
}

func TestComposerPersistsRepeatedAuthorizedMentionsAndRejectsForgedOne(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/mentions")
	for _, agent := range []string{"sess-a", "sess-b"} {
		if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: agent, PrincipalID: "person:2", Person: "peer", Provider: "test", DisplayName: agent, RoomKey: room}); err != nil {
			t.Fatal(err)
		}
	}
	materializeWebRoom(t, st, room)
	res := authenticatedPostForm(t, client, srv.URL+"/ui/coord/send", url.Values{
		"room": {room}, "body": {"hello"}, "mentions": {"sess-a", "sess-b", "sess-a"},
	})
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("authorized mentions status=%d", res.StatusCode)
	}
	messages, err := st.CoordMessagesSince(store.DestinationRoom, room, 0, 10)
	if err != nil || len(messages) != 1 {
		t.Fatalf("messages=%+v err=%v", messages, err)
	}
	mentions, err := st.CoordMessageMentions(messages[0].ID)
	if err != nil || len(mentions) != 2 {
		t.Fatalf("mentions=%v err=%v", mentions, err)
	}
	res = authenticatedPostForm(t, client, srv.URL+"/ui/coord/send", url.Values{
		"room": {room}, "body": {"forged"}, "mentions": {"sess-secret"},
	})
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("forged mention status=%d", res.StatusCode)
	}
	messages, _ = st.CoordMessagesSince(store.DestinationRoom, room, 0, 10)
	if len(messages) != 1 {
		t.Fatalf("forged mention wrote a message: %+v", messages)
	}
}

func TestAllCoordRoomManagementMutationsRequireCSRF(t *testing.T) {
	srv, _, client := signedIn(t)
	for _, path := range []string{
		"/ui/coord/direct/start", "/ui/coord/group/create",
		"/ui/coord/group/update", "/ui/coord/group/leave", "/ui/coord/standing/create",
		"/ui/coord/attention/action",
	} {
		res := sameOriginPostForm(t, client, srv.URL+path, url.Values{})
		res.Body.Close()
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("%s without CSRF status=%d", path, res.StatusCode)
		}
	}
}

func TestCoordWorkspaceSeparatesAttentionMentionsAndUnreadAndActsWithCSRF(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/attention")
	materializeWebRoom(t, st, room)
	messageID, err := st.AppendCoordMessage(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room,
		SenderExternalID: "reviewer", AuthorPrincipalID: "person:2", AuthorKind: store.AuthorHuman,
		ClientID: "approval", Body: "Release freigeben?", Intent: store.IntentApproval,
		Mentions: []string{"person:1"}, Refs: []store.CoordRef{{Kind: "request", ID: "REQ-380"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	items, err := st.CoordinationFor(store.Principal{ID: "person:1", Label: "robin"}, "").Attention()
	if err != nil || len(items) != 1 || items[0].MessageID != messageID {
		t.Fatalf("attention=%+v err=%v", items, err)
	}
	page := coordPageBody(t, client, srv.URL+"/ui/coord?room="+url.QueryEscape(room))
	for _, want := range []string{"Braucht dich", "Erwähnungen", "Ungelesen", "Release freigeben?", "nur Koordination", `action="/ui/coord/attention/action"`} {
		if !strings.Contains(page, want) {
			t.Errorf("attention workspace missing %q", want)
		}
	}
	res := authenticatedPostForm(t, client, srv.URL+"/ui/coord/attention/action", url.Values{
		"attention_id": {strconv.FormatInt(items[0].ID, 10)}, "action": {store.AttentionActionApprove}, "room": {room},
	})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("approve status=%d body=%s", res.StatusCode, body(t, res))
	}
	res.Body.Close()
	items, err = st.CoordinationFor(store.Principal{ID: "person:1", Label: "robin"}, "").Attention()
	if err != nil || len(items) != 1 || items[0].State != store.AttentionResolved {
		t.Fatalf("resolved attention=%+v err=%v", items, err)
	}
}

func TestCoordComposerPersistsExplicitAttentionIntent(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/intent")
	materializeWebRoom(t, st, room)
	if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: "sess-peer", PrincipalID: "person:2", Person: "peer", Provider: "test", DisplayName: "Peer", RoomKey: room}); err != nil {
		t.Fatal(err)
	}
	res := authenticatedPostForm(t, client, srv.URL+"/ui/coord/send", url.Values{
		"room": {room}, "body": {"Please investigate"}, "intent": {store.IntentHandoff}, "mentions": {"sess-peer"},
	})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("send status=%d body=%s", res.StatusCode, body(t, res))
	}
	res.Body.Close()
	items, err := st.CoordinationFor(store.Principal{ID: "person:2"}, "sess-peer").Attention()
	if err != nil || len(items) != 1 || items[0].Reason != store.AttentionHandoff {
		t.Fatalf("attention=%+v err=%v", items, err)
	}
}

func TestThreadAttentionAroundLoadsSourceOlderThanLatestWindow(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/thread-attention")
	materializeWebRoom(t, st, room)
	if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: "sess-peer", PrincipalID: "person:2", Person: "peer", Provider: "test", DisplayName: "Peer", RoomKey: room}); err != nil {
		t.Fatal(err)
	}
	owner := st.CoordinationFor(store.Principal{ID: "person:1", Label: "robin"}, "")
	threadID, err := owner.CreateTaskThreadInRoom(room, "Old attention", "", "")
	if err != nil {
		t.Fatal(err)
	}
	peer := st.CoordinationFor(store.Principal{ID: "person:2", Label: "peer"}, "sess-peer")
	if _, err := peer.ThreadPost(threadID, store.CoordMessage{ClientID: "old-question", Body: "Old source", Intent: store.IntentQuestion, Mentions: []string{"person:1"}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 55; i++ {
		if _, err := peer.ThreadPost(threadID, store.CoordMessage{ClientID: "filler-" + strconv.Itoa(i), Body: "filler"}); err != nil {
			t.Fatal(err)
		}
	}
	latest := coordPageBody(t, client, srv.URL+coordThreadURL(room, threadID))
	if strings.Contains(latest, `id="thread-message-1"`) {
		t.Fatal("latest thread window unexpectedly contained old source")
	}
	aroundURL := coordThreadMessageURL(room, threadID, 1)
	around := coordPageBody(t, client, srv.URL+aroundURL)
	if !strings.Contains(around, `id="thread-message-1"`) || !strings.Contains(around, "Old source") {
		t.Fatalf("around window missed old source: %s", around)
	}
}

func TestHumanCanReplyInRoomAndThreadWithoutJavaScript(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/replies")
	foreignRoom := store.RoomKeyForProject("github.com/x/foreign-replies")
	materializeWebRoom(t, st, room)
	materializeWebRoom(t, st, foreignRoom)
	parentID, err := st.AppendCoordMessage(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room,
		SenderExternalID: "fixture:" + room, ClientID: "room-parent", Body: "Room parent",
	})
	if err != nil {
		t.Fatal(err)
	}
	foreignID, err := st.AppendCoordMessage(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: foreignRoom,
		SenderExternalID: "fixture:" + foreignRoom, ClientID: "foreign-parent", Body: "Foreign parent",
	})
	if err != nil {
		t.Fatal(err)
	}

	page := coordPageBody(t, client, srv.URL+coordRoomURL(room, "", 0))
	replyURL := coordRoomReplyURL(room, parentID, 1)
	for _, want := range []string{`href="` + strings.ReplaceAll(replyURL, "&", "&amp;") + `"`, `>Antworten</a>`} {
		if !strings.Contains(page, want) {
			t.Fatalf("room reply control missing %q", want)
		}
	}
	replyPage := coordPageBody(t, client, srv.URL+replyURL)
	if !strings.Contains(replyPage, `type="hidden" name="reply_to" value="`+strconv.FormatInt(parentID, 10)+`"`) || !strings.Contains(replyPage, `id="coord-message-body"`) {
		t.Fatalf("room reply selection was not carried to focused composer: %s", replyPage)
	}
	res := authenticatedPostForm(t, client, srv.URL+"/ui/coord/send", url.Values{
		"room": {room}, "body": {"Room child"}, "reply_to": {strconv.FormatInt(parentID, 10)}, "form_id": {"room-reply"},
	})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("room reply status=%d body=%s", res.StatusCode, body(t, res))
	}
	res.Body.Close()
	roomMessages, err := st.CoordMessagesSince(store.DestinationRoom, room, 0, 10)
	if err != nil || len(roomMessages) != 2 || roomMessages[1].ReplyTo != parentID {
		t.Fatalf("room reply messages=%+v err=%v", roomMessages, err)
	}
	rendered := coordPageBody(t, client, srv.URL+coordRoomURL(room, "", 0))
	if !strings.Contains(rendered, "Antwort auf") || !strings.Contains(rendered, "Room parent") {
		t.Fatalf("room reply preview missing: %s", rendered)
	}
	res = authenticatedPostForm(t, client, srv.URL+"/ui/coord/send", url.Values{
		"room": {room}, "body": {"Forged room child"}, "reply_to": {strconv.FormatInt(foreignID, 10)},
	})
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-room reply status=%d body=%s", res.StatusCode, body(t, res))
	}
	res.Body.Close()

	access := st.CoordinationFor(store.Principal{ID: "person:1", Label: "robin"}, "")
	threadID, err := access.CreateTaskThreadInRoom(room, "Replies", "", "")
	if err != nil {
		t.Fatal(err)
	}
	threadParentID, err := access.ThreadPost(threadID, store.CoordMessage{ClientID: "thread-parent", Body: "Thread parent"})
	if err != nil {
		t.Fatal(err)
	}
	threadURL := coordThreadURL(room, threadID)
	threadPage := coordPageBody(t, client, srv.URL+threadURL)
	threadReplyURL := coordThreadReplyURL(room, threadID, threadParentID, 1)
	if !strings.Contains(threadPage, `href="`+strings.ReplaceAll(threadReplyURL, "&", "&amp;")+`"`) {
		t.Fatalf("thread reply control missing: %s", threadPage)
	}
	selected := coordPageBody(t, client, srv.URL+threadReplyURL)
	clearThreadReplyURL := strings.TrimSuffix(threadURL, "#coord-thread") + "#coord-thread-body"
	if !strings.Contains(selected, `type="hidden" name="reply_to" value="`+strconv.FormatInt(threadParentID, 10)+`"`) || !strings.Contains(selected, `id="coord-thread-body"`) || !strings.Contains(selected, `href="`+strings.ReplaceAll(clearThreadReplyURL, "&", "&amp;")+`"`) {
		t.Fatalf("thread reply selection was not carried to focused composer: %s", selected)
	}
	res = authenticatedPostForm(t, client, srv.URL+"/ui/coord/thread/post", url.Values{
		"thread_id": {strconv.FormatInt(threadID, 10)}, "body": {"Thread child"},
		"reply_to": {strconv.FormatInt(threadParentID, 10)}, "form_id": {"thread-reply"},
	})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("thread reply status=%d body=%s", res.StatusCode, body(t, res))
	}
	res.Body.Close()
	threadMessages, err := st.CoordMessagesSince(store.DestinationDiscussion, store.ThreadDestinationID(threadID), 0, 10)
	if err != nil || len(threadMessages) != 2 || threadMessages[1].ReplyTo != threadParentID {
		t.Fatalf("thread reply messages=%+v err=%v", threadMessages, err)
	}
	res = authenticatedPostForm(t, client, srv.URL+"/ui/coord/thread/post", url.Values{
		"thread_id": {strconv.FormatInt(threadID, 10)}, "body": {"Forged thread child"},
		"reply_to": {strconv.FormatInt(parentID, 10)},
	})
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-destination thread reply status=%d body=%s", res.StatusCode, body(t, res))
	}
	res.Body.Close()
}

func TestThreadPagingAndOldReplyTargetsRemainReachable(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/thread-paging")
	materializeWebRoom(t, st, room)
	access := st.CoordinationFor(store.Principal{ID: "person:1", Label: "robin"}, "")
	threadID, err := access.CreateTaskThreadInRoom(room, "Paged", "", "")
	if err != nil {
		t.Fatal(err)
	}
	var firstID int64
	for i := 1; i <= 75; i++ {
		message := store.CoordMessage{ClientID: "thread-page-" + strconv.Itoa(i), Body: "thread body"}
		if i == 75 {
			message.Body = "Reply to old parent"
			message.ReplyTo = firstID
		}
		id, postErr := access.ThreadPost(threadID, message)
		if postErr != nil {
			t.Fatal(postErr)
		}
		if i == 1 {
			firstID = id
		}
	}

	base := coordThreadURL(room, threadID)
	latest := coordPageBody(t, client, srv.URL+base)
	for _, want := range []string{`id="thread-message-26"`, `id="thread-message-75"`, `thread_before=26`, `thread_around=1`, `#thread-message-1`} {
		if !strings.Contains(latest, want) {
			t.Errorf("latest thread page missing %q", want)
		}
	}
	if strings.Contains(latest, `id="thread-message-25"`) {
		t.Fatal("latest thread page rendered more than 50 messages")
	}
	before := coordPageBody(t, client, srv.URL+coordThreadPageURL(room, threadID, "thread_before", 26))
	if !strings.Contains(before, `id="thread-message-1"`) || !strings.Contains(before, `id="thread-message-25"`) || strings.Contains(before, `id="thread-message-26"`) || !strings.Contains(before, `thread_after=25`) {
		t.Fatalf("older thread page has wrong window/navigation: %s", before)
	}
	after := coordPageBody(t, client, srv.URL+coordThreadPageURL(room, threadID, "thread_after", 25))
	if !strings.Contains(after, `id="thread-message-26"`) || !strings.Contains(after, `id="thread-message-75"`) || strings.Contains(after, `id="thread-message-25"`) {
		t.Fatalf("newer thread page has wrong window: %s", after)
	}
	deep := coordPageBody(t, client, srv.URL+coordThreadMessageURL(room, threadID, 1))
	if !strings.Contains(deep, `id="thread-message-1"`) {
		t.Fatalf("old reply target not reachable: %s", deep)
	}
	for _, suffix := range []string{
		"&thread_before=2&thread_after=1", "&thread_before=0", "&thread_after=0", "&thread_around=-1",
	} {
		res, getErr := client.Get(srv.URL + strings.TrimSuffix(base, "#coord-thread") + suffix)
		if getErr != nil {
			t.Fatal(getErr)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("invalid thread paging %q status=%d", suffix, res.StatusCode)
		}
	}
}

func TestReplyCountsRenderInRoomAndThread(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/reply-counts")
	materializeWebRoom(t, st, room)
	parent, err := st.AppendCoordMessage(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room,
		SenderExternalID: "fixture:" + room, ClientID: "count-parent", Body: "Parent",
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		if _, err := st.AppendCoordMessage(store.CoordMessage{
			DestinationKind: store.DestinationRoom, DestinationID: room,
			SenderExternalID: "fixture:" + room, ClientID: "count-child-" + strconv.Itoa(i), Body: "Child", ReplyTo: parent,
		}); err != nil {
			t.Fatal(err)
		}
	}
	access := st.CoordinationFor(store.Principal{ID: "person:1", Label: "robin"}, "")
	threadID, err := access.CreateTaskThreadInRoom(room, "Counted", "", "")
	if err != nil {
		t.Fatal(err)
	}
	threadParent, err := access.ThreadPost(threadID, store.CoordMessage{ClientID: "thread-count-parent", Body: "Thread parent"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		if _, err := access.ThreadPost(threadID, store.CoordMessage{ClientID: "thread-count-child-" + strconv.Itoa(i), Body: "Thread child", ReplyTo: threadParent}); err != nil {
			t.Fatal(err)
		}
	}
	if err := access.SetThreadArchived(threadID, true); err != nil {
		t.Fatal(err)
	}

	page := coordPageBody(t, client, srv.URL+coordThreadURL(room, threadID))
	if got := strings.Count(page, `>2 Antworten<`); got != 2 {
		t.Fatalf("room and thread reply counters: got %d occurrences, want 2", got)
	}
}

func TestCoordDrawerScriptMakesOutsideRegionsInertAndRestoresThem(t *testing.T) {
	srv, _, _ := signedIn(t)
	res, err := http.Get(srv.URL + "/static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	scriptBytes, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	script := string(scriptBytes)
	for _, want := range []string{"setOutsideInert", "sibling.inert = true", "restoreOutside", `event.key === "Escape"`, "returnFocus"} {
		if !strings.Contains(script, want) {
			t.Errorf("drawer script missing %q", want)
		}
	}
}

func TestStartDirectAndCreateGroupUseSignedInPrincipal(t *testing.T) {
	srv, st, client := signedIn(t)
	shared := store.RoomKeyForProject("github.com/x/people")
	for _, id := range []string{"sess-peer", "sess-two"} {
		if _, err := st.RegisterCoordAgent(store.CoordAgent{
			ExternalID: id, PrincipalID: "person:2", Person: "peer", Provider: "test", DisplayName: id, RoomKey: shared,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.RegisterCoordAgent(store.CoordAgent{
		ExternalID: "sess-owner", PrincipalID: "person:1", Person: "robin", Provider: "test", DisplayName: "owner", RoomKey: shared,
	}); err != nil {
		t.Fatal(err)
	}

	directRes := authenticatedPostForm(t, client, srv.URL+"/ui/coord/direct/start", url.Values{
		"principal_id": {"sess-peer"}, "actor": {"spoofed"},
	})
	directRes.Body.Close()
	if directRes.StatusCode != http.StatusSeeOther {
		t.Fatalf("start direct status=%d", directRes.StatusCode)
	}
	directRoom := store.RoomKeyForDirect([]string{"person:1", "sess-peer"})
	if location := directRes.Header.Get("Location"); !strings.Contains(location, url.QueryEscape(directRoom)) {
		t.Fatalf("direct redirect=%q", location)
	}

	groupRes := authenticatedPostForm(t, client, srv.URL+"/ui/coord/group/create", url.Values{
		"label": {"Release"}, "members": {"sess-peer sess-two"}, "creator": {"spoofed"},
	})
	groupRes.Body.Close()
	if groupRes.StatusCode != http.StatusSeeOther {
		t.Fatalf("create group status=%d", groupRes.StatusCode)
	}
	location := groupRes.Header.Get("Location")
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	groupRoom := parsed.Query().Get("room")
	room, err := st.CoordinationFor(store.Principal{ID: "person:1", Label: "robin"}, "").Room(groupRoom)
	if err != nil {
		t.Fatal(err)
	}
	if room.Kind != store.RoomGroup || room.Label != "Release" {
		t.Fatalf("created room=%+v", room)
	}
	if !containsString(room.Members, "person:1") || containsString(room.Members, "spoofed") {
		t.Fatalf("creator came from form instead of session: %+v", room.Members)
	}
}

func TestStartDirectRejectsAnUnseenPrincipal(t *testing.T) {
	srv, _, client := signedIn(t)
	res := authenticatedPostForm(t, client, srv.URL+"/ui/coord/direct/start", url.Values{
		"principal_id": {"arbitrary-unregistered-id"},
	})
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("unseen recipient status=%d", res.StatusCode)
	}
}

func TestGroupManagementRequiresManagerAndCSRF(t *testing.T) {
	srv, st, client := signedIn(t)
	group, err := st.CreateCoordGroup(store.GroupInput{
		Label: "Private", Creator: "sess-manager", Members: []string{"sess-manager", "person:1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := authenticatedPostForm(t, client, srv.URL+"/ui/coord/group/update", url.Values{
		"room": {group.Key}, "label": {"stolen"}, "actor": {"sess-manager"},
	})
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("non-manager update status=%d", res.StatusCode)
	}

	withoutCSRF := sameOriginPostForm(t, client, srv.URL+"/ui/coord/group/update", url.Values{
		"room": {group.Key}, "label": {"stolen"},
	})
	withoutCSRF.Body.Close()
	if withoutCSRF.StatusCode != http.StatusForbidden {
		t.Fatalf("group update without csrf status=%d", withoutCSRF.StatusCode)
	}
}

func TestGroupManagerCanUpdateAndMemberCanLeave(t *testing.T) {
	srv, st, client := signedIn(t)
	group, err := st.CreateCoordGroup(store.GroupInput{
		Label: "Before", Creator: "person:1", Members: []string{"person:1", "sess-peer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := authenticatedPostForm(t, client, srv.URL+"/ui/coord/group/update", url.Values{
		"room": {group.Key}, "label": {"After"}, "add_managers": {"sess-peer"},
	})
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("manager update status=%d", res.StatusCode)
	}
	updated, err := st.CoordinationFor(store.Principal{ID: "person:1", Label: "robin"}, "").Room(group.Key)
	if err != nil || updated.Label != "After" {
		t.Fatalf("updated room=%+v err=%v", updated, err)
	}

	res = authenticatedPostForm(t, client, srv.URL+"/ui/coord/group/leave", url.Values{"room": {group.Key}})
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/ui/coord" {
		t.Fatalf("leave status=%d location=%q", res.StatusCode, res.Header.Get("Location"))
	}
	if _, err := st.CoordinationFor(store.Principal{ID: "person:1", Label: "robin"}, "").Room(group.Key); !errors.Is(err, store.ErrCoordNotFound) {
		t.Fatalf("left group still visible: %v", err)
	}
}

func TestLastGroupManagerCannotLeaveThroughWorkspace(t *testing.T) {
	srv, st, client := signedIn(t)
	group, err := st.CreateCoordGroup(store.GroupInput{
		Label: "Owned", Creator: "person:1", Members: []string{"person:1", "sess-peer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := authenticatedPostForm(t, client, srv.URL+"/ui/coord/group/leave", url.Values{"room": {group.Key}})
	res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("last-manager leave status=%d", res.StatusCode)
	}
}

func TestForgedDirectRoomLeaveIsRejectedWithoutMutation(t *testing.T) {
	srv, st, client := signedIn(t)
	roomKey := store.RoomKeyForDirect([]string{"person:1", "sess-peer"})
	if err := st.EnsureCoordRoom(store.CoordRoom{Key: roomKey, Kind: store.RoomDirect, Members: []string{"person:1", "sess-peer"}}); err != nil {
		t.Fatal(err)
	}
	res := authenticatedPostForm(t, client, srv.URL+"/ui/coord/group/leave", url.Values{"room": {roomKey}})
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("direct leave status=%d", res.StatusCode)
	}
	if _, err := st.CoordinationFor(store.Principal{ID: "person:1"}, "").Room(roomKey); err != nil {
		t.Fatalf("forged leave mutated membership: %v", err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func coordPageBody(t *testing.T, client *http.Client, target string) string {
	t.Helper()
	res, err := client.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status=%d body=%s", target, res.StatusCode, body)
	}
	return string(body)
}

// cssRule ist eine Blattregel aus app.css. Umschließende @media-Blöcke sind
// aufgelöst; es zählt nur Selektor und Deklarationstext.
type cssRule struct{ selector, body string }

var cssComment = regexp.MustCompile(`(?s)/\*.*?\*/`)

// parseCSSRules zerlegt CSS in Blattregeln. Es reicht für dieses Stylesheet:
// keine Strings mit Klammern, keine verschachtelten Regeln außer @media.
func parseCSSRules(css string) []cssRule {
	css = cssComment.ReplaceAllString(css, "")
	var rules []cssRule
	type frame struct {
		selector string
		body     strings.Builder
		children bool
	}
	var stack []*frame
	var buf strings.Builder
	for _, r := range css {
		switch r {
		case '{':
			if len(stack) > 0 {
				stack[len(stack)-1].children = true
			}
			stack = append(stack, &frame{selector: strings.TrimSpace(buf.String())})
			buf.Reset()
		case '}':
			if len(stack) == 0 {
				continue
			}
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if !top.children {
				top.body.WriteString(buf.String())
				rules = append(rules, cssRule{top.selector, top.body.String()})
			}
			buf.Reset()
		default:
			buf.WriteRune(r)
		}
	}
	return rules
}

func isCoordRule(r cssRule) bool { return strings.Contains(r.selector, "coord") }

// shadowHasBlur meldet, ob eine Ebene eines box-shadow einen Weichzeichner hat.
// Erlaubt sind Fokusringe und Linien (Blur 0); alles andere ist ein Glow
// oder ein weicher Schlagschatten.
func shadowHasBlur(value string) bool {
	color := regexp.MustCompile(`(?i)(rgba?|hsla?)\([^)]*\)|#[0-9a-f]{3,8}\b`)
	value = color.ReplaceAllString(value, "")
	for _, layer := range strings.Split(value, ",") {
		var lengths []string
		for _, f := range strings.Fields(layer) {
			if f == "inset" || strings.HasPrefix(f, "var(") {
				continue
			}
			lengths = append(lengths, f)
		}
		if len(lengths) >= 3 {
			v, err := strconv.ParseFloat(strings.TrimRight(lengths[2], "pxrem"), 64)
			if err != nil || v != 0 {
				return true
			}
		}
	}
	return false
}

// TestCoordVisualSystemAvoidsAntiSlopPatterns hält die Do-not-Liste der
// Coordination-Spec (specs/2026-09-18-coordination-visual-redesign.md) als
// maschinelle Prüfung fest. Geprüft werden alle Regeln, deren Selektor "coord"
// enthält, samt dem --coord-Token-Block.
func TestCoordVisualSystemAvoidsAntiSlopPatterns(t *testing.T) {
	raw, err := os.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	rules := parseCSSRules(string(raw))

	// Runde Formen sind nur für Avatare und den Statuspunkt gedacht; alles
	// andere hat Ecken von höchstens 4px (keine Pills).
	roundAllowed := map[string]string{
		".coord-message-avatar-human":                "Avatar eines Menschen ist rund, Agenten sind quadratisch",
		".coord-avatar":                              "Teilnehmer-Avatar in der Kontextspalte",
		".coord-live-status:not(.coord-nojs-status)": "Statuspunkt im schmalen Layout",
	}
	forbidden := []struct {
		name string
		re   *regexp.Regexp
	}{
		{"gradient", regexp.MustCompile(`(?i)(linear|radial|conic|repeating-[a-z]+)-gradient\(`)},
		{"backdrop-filter", regexp.MustCompile(`(?i)backdrop-filter\s*:`)},
		{"text-shadow", regexp.MustCompile(`(?i)text-shadow\s*:`)},
		{"filter blur", regexp.MustCompile(`(?i)(^|[;\s])filter\s*:[^;]*blur\(`)},
		{"pill radius", regexp.MustCompile(`(?i)border-radius\s*:\s*(\d{3,}px|\d{3,}rem|9+em)`)},
	}
	shadow := regexp.MustCompile(`(?i)box-shadow\s*:\s*([^;]+)`)
	radius := regexp.MustCompile(`(?i)border-radius\s*:\s*([^;]+)`)
	coordVar := regexp.MustCompile(`var\(--coord-`)

	seen := 0
	for _, r := range rules {
		if !isCoordRule(r) {
			if coordVar.MatchString(r.body) {
				t.Errorf("%s uses --coord tokens outside a coord selector", r.selector)
			}
			continue
		}
		seen++
		for _, f := range forbidden {
			if f.re.MatchString(r.body) {
				t.Errorf("%s: forbidden %s", r.selector, f.name)
			}
		}
		for _, m := range shadow.FindAllStringSubmatch(r.body, -1) {
			if shadowHasBlur(m[1]) {
				t.Errorf("%s: box-shadow with blur (glow or soft shadow): %s", r.selector, m[1])
			}
		}
		for _, m := range radius.FindAllStringSubmatch(r.body, -1) {
			if strings.Contains(m[1], "%") {
				if _, ok := roundAllowed[r.selector]; !ok {
					t.Errorf("%s: round shape is only allowed for avatars and the status dot", r.selector)
				}
			}
		}
	}
	if seen < 100 {
		t.Fatalf("parsed only %d coord rules; the extractor is probably broken", seen)
	}
	for sel := range roundAllowed {
		found := false
		for _, r := range rules {
			if r.selector == sel && strings.Contains(r.body, "border-radius: 50%") {
				found = true
			}
		}
		if !found {
			t.Errorf("stale allowlist entry %q", sel)
		}
	}

	// Dokumentierte Tokens müssen im Token-Block stehen.
	var tokens string
	for _, r := range rules {
		if r.selector == ".coord-workspace" && strings.Contains(r.body, "--coord-signal:") {
			tokens = r.body
		}
	}
	for _, name := range []string{
		"--coord-chrome", "--coord-conversation", "--coord-inspector", "--coord-line",
		"--coord-ink", "--coord-muted", "--coord-signal", "--coord-danger", "--coord-success",
		"--coord-radius-sm", "--coord-radius",
		"--coord-gap-1", "--coord-gap-2", "--coord-gap-3",
		"--coord-text-micro", "--coord-text-label", "--coord-text-note",
		"--coord-text-meta", "--coord-text-small", "--coord-text-title",
	} {
		if !strings.Contains(tokens, name+":") {
			t.Errorf("token %s is missing from the .coord-workspace token block", name)
		}
	}
}

// Regression: a media-query rule `.coord-workspace button { display: inline-flex }`
// outranked the UA `[hidden]` rule and the base `display: none` of the drawer
// toggles, so the hidden backdrop dimmed narrow layouts and, without JS, the
// toolbar showed link and button variants at once.
func TestCoordHiddenAndNoScriptToolbarSurviveNarrowButtonRules(t *testing.T) {
	raw, err := files.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(raw)
	hidden := coordCSSRule(t, css, `.coord-workspace [hidden]`)
	if !strings.Contains(hidden, `display: none !important`) {
		t.Errorf("[hidden] elements in the workspace must stay display:none, got %q", hidden)
	}
	noJS := coordCSSRule(t, css, `html:not(.coord-enhanced) .coord-drawer-toggle`)
	if !strings.Contains(noJS, `display: none !important`) {
		t.Errorf("drawer toggles must be hidden without coord-enhanced, got %q", noJS)
	}
	template, err := files.ReadFile("templates/coord.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(template), `class="coord-backdrop" data-coord-drawer-close hidden`) {
		t.Error("backdrop must carry the hidden attribute")
	}
}

// coordSidebarAndContextFixture legt einen Projektraum an, in dem robin eine
// offene Frage aus einer privaten Direktnachricht mit bob hat.
func coordPrivateAttentionFixture(t *testing.T) (srv *httptest.Server, st *store.Store, client *http.Client, project, direct string) {
	t.Helper()
	srv, st, client = signedIn(t)
	if _, err := st.AddPerson("bob"); err != nil {
		t.Fatal(err)
	}
	project = store.RoomKeyForProject("github.com/x/privacy")
	materializeWebRoom(t, st, project)
	materializeWebRoomFor(t, st, project, "person:2", "bob")
	direct = store.RoomKeyForDirect([]string{"person:1", "person:2"})
	robin := st.CoordinationFor(store.Principal{ID: "person:1", Label: "robin"}, "")
	if err := robin.EnsureDirect(store.CoordRoom{Key: direct, Kind: store.RoomDirect, Members: []string{"person:1", "person:2"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendCoordMessage(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: direct,
		SenderExternalID: "person:2", AuthorPrincipalID: "person:2", AuthorKind: store.AuthorHuman,
		ClientID: "dm-secret", Body: "Vertraulich: Staging-Token rotieren?", Intent: store.IntentQuestion,
		Mentions: []string{"person:1"},
	}); err != nil {
		t.Fatal(err)
	}
	return srv, st, client, project, direct
}

func TestCoordNeedsYouLeadsContextAndNamesSenderRoomAndPrivacy(t *testing.T) {
	srv, _, client, project, _ := coordPrivateAttentionFixture(t)
	page := coordPageBody(t, client, srv.URL+"/ui/coord?room="+url.QueryEscape(project))
	needs := strings.Index(page, "Braucht dich")
	participants := strings.Index(page, "<h2>Teilnehmende</h2>")
	threads := strings.Index(page, "<h2>Aufgaben-Threads</h2>")
	if needs < 0 || participants < 0 || threads < 0 || needs > participants || needs > threads {
		t.Fatalf("Braucht dich must open the context: needs=%d threads=%d participants=%d", needs, threads, participants)
	}
	start := strings.Index(page, `class="coord-attention-card`)
	if start < 0 {
		t.Fatal("no attention card rendered")
	}
	card := page[start : start+strings.Index(page[start:], "</article>")]
	for _, want := range []string{"von bob", "bob", `class="coord-private-mark"`, "Privat"} {
		if !strings.Contains(card, want) {
			t.Errorf("private attention card missing %q: %s", want, card)
		}
	}
	if !strings.Contains(card, "coord-attention-origin") {
		t.Errorf("card must carry an origin line: %s", card)
	}
}

func TestCoordAttentionCardOfProjectRoomHasNoPrivateMark(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/public-card")
	materializeWebRoom(t, st, room)
	if _, err := st.AppendCoordMessage(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room,
		SenderExternalID: "reviewer", AuthorPrincipalID: "person:2", AuthorKind: store.AuthorHuman,
		ClientID: "q", Body: "Offene Frage", Intent: store.IntentQuestion, Mentions: []string{"person:1"},
	}); err != nil {
		t.Fatal(err)
	}
	page := coordPageBody(t, client, srv.URL+"/ui/coord?room="+url.QueryEscape(room))
	if strings.Contains(page, `class="coord-private-mark"`) {
		t.Error("project room card must not be marked private")
	}
	if !strings.Contains(page, "public-card") || !strings.Contains(page, "coord-attention-origin") {
		t.Error("card must name its origin room")
	}
}

// Privacy: the attention list is ACL-filtered (store.CoordAccess.Attention
// runs canReadTx per item). A signed-in non-member must not see the DM card,
// its text, or the DM room anywhere, and must not enter it by URL.
func TestCoordPrivateDirectAttentionIsInvisibleToNonMembers(t *testing.T) {
	srv, st, _, project, direct := coordPrivateAttentionFixture(t)
	carolToken, err := st.AddPerson("carol")
	if err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	carol := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	sameOriginPostForm(t, carol, srv.URL+"/ui/login", url.Values{"token": {carolToken}}).Body.Close()
	materializeWebRoomFor(t, st, project, "person:3", "carol")

	for _, target := range []string{
		srv.URL + "/ui/coord",
		srv.URL + "/ui/coord?room=" + url.QueryEscape(project),
	} {
		page := coordPageBody(t, carol, target)
		for _, secret := range []string{"Vertraulich", "Staging-Token", "coord-private-mark", url.QueryEscape(direct)} {
			if strings.Contains(page, secret) {
				t.Errorf("%s leaked %q to a non-member", target, secret)
			}
		}
	}
	res, err := carol.Get(srv.URL + "/ui/coord?room=" + url.QueryEscape(direct))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusOK {
		t.Fatalf("non-member entered the direct room, status=%d", res.StatusCode)
	}
}

func materializeWebRoomFor(t *testing.T, st *store.Store, room, principal, person string) {
	t.Helper()
	if _, err := st.RegisterCoordAgent(store.CoordAgent{
		ExternalID: "fixture:" + person + ":" + room, PrincipalID: principal, Person: person,
		Provider: "test", DisplayName: "fixture " + person, RoomKey: room,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCoordDirectRoomShowsPrivateSignalInHeadAndComposer(t *testing.T) {
	srv, _, client, project, direct := coordPrivateAttentionFixture(t)
	page := coordPageBody(t, client, srv.URL+"/ui/coord?room="+url.QueryEscape(direct))
	head := page[strings.Index(page, `class="coord-conversation-head"`):]
	head = head[:strings.Index(head, "</header>")]
	if !strings.Contains(head, "Privat · nur du und bob") {
		t.Errorf("conversation head lacks the private signal: %s", head)
	}
	composer := page[strings.Index(page, `class="coord-composer"`):]
	composer = composer[:strings.Index(composer, "</form>")]
	if !strings.Contains(composer, `class="coord-private-hint"`) || !strings.Contains(composer, "bob") {
		t.Errorf("composer lacks the private hint: %s", composer)
	}
	other := coordPageBody(t, client, srv.URL+"/ui/coord?room="+url.QueryEscape(project))
	if strings.Contains(other, "Privat · nur du") || strings.Contains(other, `class="coord-private-hint"`) {
		t.Error("project rooms must not carry the private signal")
	}
}

func TestCoordSidebarShowsOneActiveRowAndSplitCounters(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/counters")
	materializeWebRoom(t, st, room)
	if _, err := st.AppendCoordMessage(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room,
		SenderExternalID: "reviewer", AuthorPrincipalID: "person:2", AuthorKind: store.AuthorHuman,
		ClientID: "q", Body: "Bitte", Intent: store.IntentQuestion, Mentions: []string{"person:1"},
	}); err != nil {
		t.Fatal(err)
	}
	page := coordPageBody(t, client, srv.URL+"/ui/coord?room="+url.QueryEscape(room))
	rail := page[strings.Index(page, `id="coord-rooms"`):]
	rail = rail[:strings.Index(rail, `class="coord-start"`)]
	if got := strings.Count(rail, `aria-current="page"`); got != 1 {
		t.Errorf("sidebar marks %d rows as current, want exactly 1", got)
	}
	if strings.Contains(rail, "@1") {
		t.Error("mention counter must not use the cryptic @1 form")
	}
	for _, want := range []string{`class="coord-room-name" title="github.com/x/counters"`, `class="coord-room-counts"`, "1 offen", "1 @"} {
		if !strings.Contains(rail, want) {
			t.Errorf("sidebar row missing %q", want)
		}
	}
}

func TestCoordSidebarCSSKeepsCountersOnOneLineAndEllipsizesNames(t *testing.T) {
	raw, err := files.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(raw)
	counts := coordCSSRule(t, css, "\n.coord-room-counts {")
	if !strings.Contains(counts, "white-space: nowrap") || !strings.Contains(counts, "flex: 0 0 auto") {
		t.Errorf("counter column must not wrap or shrink: %q", counts)
	}
	name := coordCSSRule(t, css, "\n.coord-room-name {")
	for _, want := range []string{"text-overflow: ellipsis", "white-space: nowrap", "overflow: hidden", "min-width: 0"} {
		if !strings.Contains(name, want) {
			t.Errorf("room name must ellipsize, missing %q in %q", want, name)
		}
	}
}

func TestCoordParticipantsHideUnknownStatesButKeepKnownOnes(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/participants")
	if _, err := st.RegisterCoordAgent(store.CoordAgent{
		ExternalID: "sess-a", PrincipalID: "person:1", Person: "robin", Provider: "claude",
		DisplayName: "Build Agent", RoomKey: room, Branch: "feat/ui", Worktree: "/wt/ui",
	}); err != nil {
		t.Fatal(err)
	}
	page := coordPageBody(t, client, srv.URL+"/ui/coord?room="+url.QueryEscape(room))
	list := page[strings.Index(page, `class="coord-participants"`):]
	list = list[:strings.Index(list, "</ul>")]
	for _, gone := range []string{"unbekannt", "Erreichbarkeit", "Arbeitszustand", "idle", "untätig"} {
		if strings.Contains(list, gone) {
			t.Errorf("participants must not print %q for unobserved state", gone)
		}
	}
	for _, want := range []string{"Build Agent", "claude", "feat/ui"} {
		if !strings.Contains(list, want) {
			t.Errorf("compact participant line lost %q", want)
		}
	}
	template, _ := files.ReadFile("templates/coord.html")
	if !strings.Contains(string(template), `Erreichbarkeit: {{.Reachability}}`) || !strings.Contains(string(template), `ne .Reachability "unbekannt"`) {
		t.Error("known reachability must still be rendered, only unknown is hidden")
	}
}

func TestCoordRoomsToggleCarriesTotalSignalWithAccessibleName(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/badge")
	materializeWebRoom(t, st, room)
	empty := coordPageBody(t, client, srv.URL+"/ui/coord?room="+url.QueryEscape(room))
	if strings.Contains(empty, `class="coord-count"`) {
		t.Error("badge must be absent when nothing needs attention")
	}
	if !strings.Contains(empty, "data-coord-rooms-count") {
		t.Error("badge hook must exist even when empty so live updates can fill it")
	}
	if _, err := st.AppendCoordMessage(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room,
		SenderExternalID: "reviewer", AuthorPrincipalID: "person:2", AuthorKind: store.AuthorHuman,
		ClientID: "q", Body: "Bitte", Intent: store.IntentQuestion, Mentions: []string{"person:1"},
	}); err != nil {
		t.Fatal(err)
	}
	page := coordPageBody(t, client, srv.URL+"/ui/coord?room="+url.QueryEscape(room))
	toggle := page[strings.Index(page, `data-coord-drawer-target="coord-rooms"`):]
	toggle = toggle[:strings.Index(toggle, "</button>")]
	for _, want := range []string{`class="coord-count" aria-hidden="true">1</span>`, ", 1 brauchen dich", "data-coord-rooms-count"} {
		if !strings.Contains(toggle, want) {
			t.Errorf("a question with a mention is one message that needs the viewer; toggle missing %q: %s", want, toggle)
		}
	}
	if _, err := st.AppendCoordMessage(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room,
		SenderExternalID: "reviewer", AuthorPrincipalID: "person:2", AuthorKind: store.AuthorHuman,
		ClientID: "q2", Body: "Noch eine", Intent: store.IntentApproval, Mentions: []string{"person:1"},
	}); err != nil {
		t.Fatal(err)
	}
	page = coordPageBody(t, client, srv.URL+"/ui/coord?room="+url.QueryEscape(room))
	if !strings.Contains(page, `class="coord-count" aria-hidden="true">2</span>`) {
		t.Error("two different messages that need the viewer must count as 2")
	}
	js, _ := files.ReadFile("static/app.js")
	if !strings.Contains(string(js), "[data-coord-rooms-count]") {
		t.Error("live refresh must replace the rooms badge")
	}
}
