package web

import (
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
	res, err := client.PostForm(srv.URL+"/ui/login", url.Values{"token": {token}})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	res.Body.Close()
	return srv, st, client
}

// AC-4 von REQ-350: der Mensch schreibt aus der authentifizierten Oberfläche
// in einen Raum, und der Beitrag trägt seine Herkunft. Bis hierhin war die
// Operator-Oberfläche schreibfrei.
func TestAHumanWritesIntoARoomAndTheMessageCarriesThatOrigin(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/y")

	res, err := client.PostForm(srv.URL+"/ui/coord/send", url.Values{
		"room": {room}, "body": {"für diesen Release nur additive Änderungen"}})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
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
	if !strings.Contains(msgs[0].SenderExternalID, "robin") {
		t.Fatalf("the sender must name the signed-in person, got %q", msgs[0].SenderExternalID)
	}
}

// Eine gezielte Vorgabe bleibt sichtbar und fällt nicht durch nachfolgende
// Nachrichten aus dem Blickfeld. Spec §A4 und §11 — sie endet mit ihrer
// Aufgabe oder durch eine ausdrückliche Geste, nicht durch Chatverkehr.
func TestAStandingInstructionSurvivesLaterTraffic(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/y")

	res, err := client.PostForm(srv.URL+"/ui/coord/send", url.Values{
		"room": {room}, "body": {"keine Breaking Changes in diesem Release"},
		"standing": {"1"}, "mentions": {"sess-backend"}})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
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

	// Und sie endet nur ausdrücklich.
	res, err = client.PostForm(srv.URL+"/ui/coord/standing/end", url.Values{
		"room": {room}, "message_id": {standing[0].MessageID}})
	if err != nil {
		t.Fatalf("end: %v", err)
	}
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
	form := url.Values{"room": {room}, "body": {"einmal"}, "form_id": {"stabil-1"}}

	for i := 0; i < 2; i++ {
		res, err := client.PostForm(srv.URL+"/ui/coord/send", form)
		if err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
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
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("reading a foreign private room: want 403, got %d", res.StatusCode)
	}

	// Und hineinschreiben geht auch nicht.
	post, err := client.PostForm(srv.URL+"/ui/coord/send", url.Values{
		"room": {fremd}, "body": {"ich mische mich ein"}})
	if err != nil {
		t.Fatal(err)
	}
	defer post.Body.Close()
	if post.StatusCode != http.StatusForbidden {
		t.Fatalf("writing into a foreign private room: want 403, got %d", post.StatusCode)
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
	srv, _, client := signedIn(t)
	res, err := client.Get(srv.URL + "/ui/coord?room=" + store.RoomKeyForProject("github.com/x/y"))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("a project room must stay readable, got %d", res.StatusCode)
	}
}
