package web

import (
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

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
	for _, want := range []string{"Erwähnungen", "Maschine", "Projekte", "Direkt &amp; Gruppen", "#75", "Teilnehmende", "Threads", "Knowledge"} {
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
	} {
		res := sameOriginPostForm(t, client, srv.URL+path, url.Values{})
		res.Body.Close()
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("%s without CSRF status=%d", path, res.StatusCode)
		}
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
