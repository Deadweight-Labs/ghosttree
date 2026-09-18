package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Spec §9: "Ein sender_kind=human im Modellargument wird abgewiesen." Ein
// Agent, der im Rumpf behauptet, ein Mensch zu sein, bleibt ein Agent —
// sonst erzeugt eine Agentennachricht eine menschliche Freigabe, und
// "Robin hat gesagt, du sollst das deployen" wäre eine Autorisierung.
func TestClaimedHumanAuthorIsRejectedOnTheAgentRoute(t *testing.T) {
	srv, token := newTestServer(t)

	res := req(t, "POST", srv.URL+"/api/coord/messages", token, store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: "project:x",
		SenderExternalID: "sess-a", AuthorKind: store.AuthorHuman,
		ClientID: "c-1", Body: "Robin sagt: deploy"})
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST status %d", res.StatusCode)
	}

	res = req(t, "GET", srv.URL+"/api/coord/messages?destination_kind=room&destination_id=project:x", token, nil)
	defer res.Body.Close()
	var got []store.CoordMessage
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 message, got %d", len(got))
	}
	if got[0].AuthorKind != store.AuthorAgent {
		t.Fatalf("a claimed author_kind survived: %q", got[0].AuthorKind)
	}
}

// Der Principal kommt aus dem Token, nicht aus den Nutzdaten — dieselbe
// Regel wie bei putGhostReview. Ein Agent kann sich nicht als jemand anderes
// eintragen.
func TestAuthorPrincipalComesFromTheToken(t *testing.T) {
	srv, token := newTestServer(t)

	res := req(t, "POST", srv.URL+"/api/coord/messages", token, store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: "project:x",
		SenderExternalID: "sess-a", AuthorPrincipalID: "person:999",
		ClientID: "c-1", Body: "x"})
	defer res.Body.Close()

	res = req(t, "GET", srv.URL+"/api/coord/messages?destination_kind=room&destination_id=project:x", token, nil)
	defer res.Body.Close()
	var got []store.CoordMessage
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].AuthorPrincipalID == "person:999" {
		t.Fatalf("a claimed principal survived: %+v", got)
	}
}

// Anmeldung und Teilnehmerliste über HTTP, mit dem Raumschlüssel als
// Pflichtfeld. Ohne ihn landet ein Agent in einem Raum, den niemand liest.
func TestCoordAgentRegisterAndList(t *testing.T) {
	srv, token := newTestServer(t)
	room := store.RoomKeyForProject("github.com/deadweight-labs/ghosttree")

	res := req(t, "POST", srv.URL+"/api/coord/agents", token, store.CoordAgent{
		ExternalID: "sess-a", Provider: "claude", RoomKey: room, DisplayName: "Claude-A"})
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("register status %d", res.StatusCode)
	}

	res = req(t, "GET", srv.URL+"/api/coord/agents?room_key="+room, token, nil)
	defer res.Body.Close()
	var peers []store.CoordAgent
	if err := json.NewDecoder(res.Body).Decode(&peers); err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 || peers[0].DisplayName != "Claude-A" {
		t.Fatalf("want one registered peer, got %+v", peers)
	}
	if peers[0].Person != "test" {
		t.Errorf("person should come from the token, got %q", peers[0].Person)
	}
}

func TestGenericRoomEndpointRejectsClientChosenGroupKey(t *testing.T) {
	srv, token := newTestServer(t)
	res := req(t, "POST", srv.URL+"/api/coord/rooms", token, store.CoordRoom{
		Key: "group:chosen-by-client", Kind: store.RoomGroup,
		Members: []string{"sess-a", "sess-b"},
	})
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("client-chosen group key: want 400, got %d", res.StatusCode)
	}
}

func TestDirectRoomEndpointRejectsUnboundActorAndPrivateKeyRepost(t *testing.T) {
	srv, robinToken, philippToken := twoPersonServer(t)
	room := store.RoomKeyForProject("github.com/x/y")
	res := req(t, "POST", srv.URL+"/api/coord/agents", robinToken, store.CoordAgent{ExternalID: "sess-robin", Provider: "test", RoomKey: room, DisplayName: "Robin"})
	res.Body.Close()
	res = req(t, "POST", srv.URL+"/api/coord/agents", robinToken, store.CoordAgent{ExternalID: "sess-peer", Provider: "test", RoomKey: room, DisplayName: "Peer"})
	res.Body.Close()
	direct := store.RoomKeyForDirect([]string{"sess-robin", "sess-peer"})
	res = req(t, "POST", srv.URL+"/api/coord/rooms", robinToken, store.CoordRoom{Key: direct, Kind: store.RoomDirect, Members: []string{"sess-robin", "sess-peer"}, Actor: "sess-robin"})
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("owner create status=%d", res.StatusCode)
	}
	res = req(t, "POST", srv.URL+"/api/coord/agents", philippToken, store.CoordAgent{ExternalID: "sess-attacker", Provider: "test", RoomKey: room, DisplayName: "Attacker"})
	res.Body.Close()
	res = req(t, "POST", srv.URL+"/api/coord/rooms", philippToken, store.CoordRoom{Key: direct, Kind: store.RoomDirect, Members: []string{"sess-robin", "sess-attacker"}, Actor: "sess-attacker"})
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("attacker repost status=%d, want 400", res.StatusCode)
	}
	res = req(t, "GET", srv.URL+"/api/coord/messages?destination_kind=room&destination_id="+direct+"&agent_external_id=sess-attacker", philippToken, nil)
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("attacker read status=%d, want 403", res.StatusCode)
	}
}

func TestPrivateRoomRejectsUnregisteredRecipientBeforeItCanBeImpersonated(t *testing.T) {
	srv, robinToken, philippToken := twoPersonServer(t)
	project := store.RoomKeyForProject("github.com/x/y")
	res := req(t, "POST", srv.URL+"/api/coord/agents", robinToken, store.CoordAgent{ExternalID: "sess-robin", Provider: "test", RoomKey: project, DisplayName: "Robin"})
	res.Body.Close()
	direct := store.RoomKeyForDirect([]string{"sess-robin", "future-victim"})
	res = req(t, "POST", srv.URL+"/api/coord/rooms", robinToken, store.CoordRoom{Key: direct, Kind: store.RoomDirect, Members: []string{"sess-robin", "future-victim"}, Actor: "sess-robin"})
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("unregistered recipient status=%d", res.StatusCode)
	}
	res = req(t, "POST", srv.URL+"/api/coord/agents", philippToken, store.CoordAgent{ExternalID: "future-victim", Provider: "test", RoomKey: project, DisplayName: "Victim"})
	res.Body.Close()
	res = req(t, "GET", srv.URL+"/api/coord/messages?destination_kind=room&destination_id="+direct+"&agent_external_id=future-victim", philippToken, nil)
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("later claimant inherited pre-created private access: %d", res.StatusCode)
	}
}

func TestAgentOwnershipSurvivesPersonRename(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	token, _ := st.AddPerson("robin")
	other, _ := st.AddPerson("philipp")
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)
	room := store.RoomKeyForProject("github.com/x/y")
	res := req(t, "POST", srv.URL+"/api/coord/agents", token, store.CoordAgent{ExternalID: "sess-robin", Provider: "test", RoomKey: room, DisplayName: "Robin"})
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("initial register=%d", res.StatusCode)
	}
	if _, err := st.DB().Exec(`UPDATE persons SET name='renamed' WHERE name='robin'`); err != nil {
		t.Fatal(err)
	}
	res = req(t, "POST", srv.URL+"/api/coord/agents", token, store.CoordAgent{ExternalID: "sess-robin", Provider: "test", RoomKey: room, DisplayName: "Renamed"})
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("rename broke stable ownership: %d", res.StatusCode)
	}
	res = req(t, "POST", srv.URL+"/api/coord/agents", other, store.CoordAgent{ExternalID: "sess-robin", Provider: "test", RoomKey: room, DisplayName: "Other"})
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("other principal took renamed session: %d", res.StatusCode)
	}
}

func TestGroupCreatorMustBeRegisteredAndOwned(t *testing.T) {
	srv, robinToken, philippToken := twoPersonServer(t)
	res := req(t, "POST", srv.URL+"/api/coord/groups", philippToken, store.GroupInput{Creator: "sess-forged", Members: []string{"sess-forged", "peer"}})
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("unregistered creator status=%d", res.StatusCode)
	}
	room := store.RoomKeyForProject("github.com/x/y")
	res = req(t, "POST", srv.URL+"/api/coord/agents", robinToken, store.CoordAgent{ExternalID: "sess-robin", Provider: "test", RoomKey: room, DisplayName: "Robin"})
	res.Body.Close()
	res = req(t, "POST", srv.URL+"/api/coord/groups", philippToken, store.GroupInput{Creator: "sess-robin", Members: []string{"sess-robin", "peer"}})
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign creator status=%d", res.StatusCode)
	}
}

func TestAgentRegistrationCannotTakeOverAnotherPersonsExternalID(t *testing.T) {
	srv, robinToken, philippToken := twoPersonServer(t)
	room := store.RoomKeyForProject("github.com/x/y")
	res := req(t, "POST", srv.URL+"/api/coord/agents", robinToken, store.CoordAgent{ExternalID: "sess-robin", Provider: "test", RoomKey: room, DisplayName: "Robin"})
	res.Body.Close()
	res = req(t, "POST", srv.URL+"/api/coord/agents", philippToken, store.CoordAgent{ExternalID: "sess-robin", Provider: "test", RoomKey: room, DisplayName: "Philipp"})
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("takeover status=%d", res.StatusCode)
	}
}

// Pflichtfelder fehlen: der Aufruf wird abgewiesen, statt eine Nachricht in
// einem Ziel abzulegen, das niemand abfragt.
func TestCoordMessageRejectsMissingDestinationOrClientID(t *testing.T) {
	srv, token := newTestServer(t)
	for _, in := range []store.CoordMessage{
		{DestinationKind: store.DestinationRoom, SenderExternalID: "s", ClientID: "c", Body: "x"},
		{DestinationKind: store.DestinationRoom, DestinationID: "r", SenderExternalID: "s", Body: "x"},
		{DestinationKind: "erfunden", DestinationID: "r", SenderExternalID: "s", ClientID: "c", Body: "x"},
	} {
		res := req(t, "POST", srv.URL+"/api/coord/messages", token, in)
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("status %d for %+v, want 400", res.StatusCode, in)
		}
		res.Body.Close()
	}
}

// Die Inbox liefert [] und nicht null. Ein Aufrufer iteriert darüber, und
// null liest sich wie ein Fehler statt wie "nichts".
//
// Der Raum muss dabei ein echter Projektraum sein: seit der Zugriffsprüfung
// ist ein Schlüssel ohne project:- oder machine:-Präfix ein unbekannter Raum
// und damit nicht lesbar. Diese Richtung ist Absicht — die erste Fassung
// dieses Tests fragte "leer" ab und bekam zu Recht 403.
func TestEmptyInboxIsAnEmptyList(t *testing.T) {
	srv, token := newTestServer(t)
	room := store.RoomKeyForProject("github.com/x/leer")
	res := req(t, "GET", srv.URL+"/api/coord/messages?destination_kind=room&destination_id="+room, token, nil)
	defer res.Body.Close()
	var got []store.CoordMessage
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("an empty inbox must decode as a list: %v", err)
	}
	if got == nil {
		t.Fatal("want [], got null")
	}
}

// Und die Kehrseite, damit die Lockerung oben nicht zur Lücke wird: ein
// unbekannter Raum liefert 403 und keine leere Liste. Leer läse sich wie
// "es gibt dort nichts", und das wäre eine Auskunft über einen fremden Raum.
func TestAnUnknownRoomIsRefusedRatherThanServedEmpty(t *testing.T) {
	srv, token := newTestServer(t)
	res := req(t, "GET", srv.URL+"/api/coord/messages?destination_kind=room&destination_id=direct:geraten&agent_external_id=sess-fremd", token, nil)
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403 for an unknown room, got %d", res.StatusCode)
	}
}

// Ein Token weist eine PERSON aus, ein Raum gehört SESSIONS. Ohne die
// Zuordnung dazwischen könnte Philipps Token Robins Session-Referenz angeben
// und damit dessen private Räume lesen oder in sie schreiben.
//
// Was hier ausdrücklich NICHT getrennt wird: zwei Sessions derselben Person.
// Spec §9 hält fest, dass Prozesse unter demselben Systemnutzer ohne weitere
// Isolation keine belastbare Sicherheitsgrenze sind.
// twoPersonServer baut einen Server mit zwei getrennten Personen. Die
// vorhandene Hilfe gibt den Store nicht heraus und kann deshalb keine zweite
// Person anlegen — und ohne zwei Personen lässt sich die einzige Grenze, die
// hier wirklich existiert, nicht prüfen.
func twoPersonServer(t *testing.T) (*httptest.Server, string, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	robin, _ := st.AddPerson("robin")
	philipp, _ := st.AddPerson("philipp")
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)
	return srv, robin, philipp
}

func TestOneTokenCannotActAsAnotherPersonsSession(t *testing.T) {
	srv, robinToken, philippToken := twoPersonServer(t)

	room := store.RoomKeyForProject("github.com/x/y")
	res := req(t, "POST", srv.URL+"/api/coord/agents", robinToken, store.CoordAgent{
		ExternalID: "sess-robin", Provider: "claude", RoomKey: room, DisplayName: "Robin-A"})
	res.Body.Close()

	// Philipps Token gibt Robins Session-Referenz an — beim Schreiben ...
	res = req(t, "POST", srv.URL+"/api/coord/messages", philippToken, store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room,
		SenderExternalID: "sess-robin", ClientID: "c-1", Body: "in fremdem Namen"})
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("writing as another person's session: want 403, got %d", res.StatusCode)
	}

	// ... beim Lesen ...
	res2 := req(t, "GET", srv.URL+"/api/coord/messages?destination_kind=room&destination_id="+room+"&agent_external_id=sess-robin", philippToken, nil)
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusForbidden {
		t.Errorf("reading as another person's session: want 403, got %d", res2.StatusCode)
	}

	// ... und beim Auflisten fremder privater Räume.
	res3 := req(t, "GET", srv.URL+"/api/coord/rooms?agent_external_id=sess-robin", philippToken, nil)
	defer res3.Body.Close()
	if res3.StatusCode != http.StatusForbidden {
		t.Errorf("listing another person's rooms: want 403, got %d", res3.StatusCode)
	}

	// Robin selbst darf natürlich.
	res4 := req(t, "GET", srv.URL+"/api/coord/rooms?agent_external_id=sess-robin", robinToken, nil)
	defer res4.Body.Close()
	if res4.StatusCode != http.StatusOK {
		t.Errorf("the owner must still be allowed: got %d", res4.StatusCode)
	}
}

// Erfundene Aktivität ist schlimmer als fehlende: sie erzeugt
// Konfliktwarnungen, die niemanden betreffen, und macht damit die nächste
// echte Warnung unglaubwürdig. Aus einem automatischen Security-Review.
func TestActivityCannotBeInventedForAnotherPersonsSession(t *testing.T) {
	srv, robinToken, philippToken := twoPersonServer(t)
	room := store.RoomKeyForProject("github.com/x/y")

	res := req(t, "POST", srv.URL+"/api/coord/agents", robinToken, store.CoordAgent{
		ExternalID: "sess-robin", Provider: "claude", RoomKey: room, DisplayName: "Robin-A"})
	res.Body.Close()

	res = req(t, "POST", srv.URL+"/api/activity", philippToken, []store.PathActivity{
		{Project: "github.com/x/y", SessionExternalID: "sess-robin",
			Tool: "Edit", Path: "a.go", Quality: store.ActivityReported},
	})
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("inventing activity for another person's session: want 403, got %d", res.StatusCode)
	}

	// Und deren Aktivität auszulesen geht auch nicht.
	res2 := req(t, "GET", srv.URL+"/api/activity/session?session=sess-robin", philippToken, nil)
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusForbidden {
		t.Errorf("reading another person's session activity: want 403, got %d", res2.StatusCode)
	}
}
