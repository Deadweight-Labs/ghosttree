package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func coordinationAccessServer(t *testing.T) (*httptest.Server, *store.Store, string, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	owner, _ := st.AddPerson("owner")
	other, _ := st.AddPerson("other")
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)
	return srv, st, owner, other
}

func TestCoordAPIBlankOrUnownedAgentCannotAct(t *testing.T) {
	srv, st, token, _ := coordinationAccessServer(t)
	room := store.RoomKeyForProject("github.com/x/y")
	if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: "seed", PrincipalID: "person:1", Person: "owner", Provider: "test", RoomKey: room}); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"", "unregistered"} {
		res := req(t, "POST", srv.URL+"/api/coord/messages", token, store.CoordMessage{
			DestinationKind: store.DestinationRoom, DestinationID: room,
			SenderExternalID: agent, ClientID: "forged-" + agent, Body: "forged",
		})
		res.Body.Close()
		if res.StatusCode != http.StatusForbidden && res.StatusCode != http.StatusBadRequest {
			t.Fatalf("agent %q acted through API: status=%d", agent, res.StatusCode)
		}
	}
}

func TestTaskThreadAPICreatesAndListsHomeThread(t *testing.T) {
	srv, st, token, _ := coordinationAccessServer(t)
	room := store.RoomKeyForProject("github.com/x/y")
	if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: "sess-a", PrincipalID: "person:1", Person: "owner", Provider: "test", RoomKey: room}); err != nil {
		t.Fatal(err)
	}
	anchor, err := st.CoordinationFor(store.Principal{ID: "person:1"}, "sess-a").Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, ClientID: "api-anchor", Body: "Investigate"})
	if err != nil {
		t.Fatal(err)
	}
	res := req(t, "POST", srv.URL+"/api/threads/from-message?agent_external_id=sess-a", token, map[string]any{
		"anchor_message_id": anchor, "title": "API task", "question": "Why?",
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d", res.StatusCode)
	}
	var created map[string]int64
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	threadID := created["id"]
	for _, invalid := range []map[string]any{
		{"anchor_message_id": -1, "room_key": room, "title": "negative"},
		{"anchor_message_id": anchor, "room_key": room, "title": "ambiguous"},
	} {
		res = req(t, "POST", srv.URL+"/api/threads/from-message?agent_external_id=sess-a", token, invalid)
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid task input status=%d", res.StatusCode)
		}
		res.Body.Close()
	}
	res = req(t, "POST", srv.URL+"/api/threads/from-message?agent_external_id=sess-a", token, map[string]any{
		"anchor_message_id": anchor, "title": "Different task",
	})
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("conflicting retry status=%d", res.StatusCode)
	}
	res.Body.Close()
	res = req(t, "POST", srv.URL+"/api/threads/from-message?agent_external_id=sess-a", token, map[string]any{
		"room_key": room, "title": "Room-only task",
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("room task status=%d", res.StatusCode)
	}
	res.Body.Close()

	res = req(t, "GET", srv.URL+"/api/threads/"+strconv.FormatInt(threadID, 10)+"/home?agent_external_id=sess-a", token, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("home status=%d", res.StatusCode)
	}
	res.Body.Close()
	res = req(t, "GET", srv.URL+"/api/threads/home?room_key="+room+"&agent_external_id=sess-a", token, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d", res.StatusCode)
	}
	var listed []store.RoomThread
	if err := json.NewDecoder(res.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if len(listed) != 2 || (listed[0].Thread.ID != threadID && listed[1].Thread.ID != threadID) {
		t.Fatalf("listed=%+v", listed)
	}
}

func TestPrivateCoordTargetsReturn404AcrossHTTP(t *testing.T) {
	srv, st, ownerToken, otherToken := coordinationAccessServer(t)
	project := store.RoomKeyForProject("github.com/x/y")
	for _, a := range []store.CoordAgent{
		{ExternalID: "sess-a", PrincipalID: "person:1", Person: "owner", Provider: "test", RoomKey: project},
		{ExternalID: "sess-b", PrincipalID: "person:1", Person: "owner", Provider: "test", RoomKey: project},
		{ExternalID: "sess-other", PrincipalID: "person:2", Person: "other", Provider: "test", RoomKey: project},
	} {
		if _, err := st.RegisterCoordAgent(a); err != nil {
			t.Fatal(err)
		}
	}
	direct := store.RoomKeyForDirect([]string{"sess-a", "sess-b"})
	if err := st.EnsureCoordRoom(store.CoordRoom{Key: direct, Kind: store.RoomDirect, Members: []string{"sess-a", "sess-b"}}); err != nil {
		t.Fatal(err)
	}
	messageID, err := st.CoordinationFor(store.Principal{ID: "person:1"}, "sess-a").Send(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: direct, ClientID: "private-source", Body: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := st.PromoteMessagesToThread(direct, []int64{messageID}, store.Thread{Project: "github.com/x/y", Title: "secret"}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.LinkThread(store.ThreadLink{ThreadID: thread.ThreadID, Kind: "knowledge", ID: "secret-object"}); err != nil {
		t.Fatal(err)
	}
	discussion := strconv.FormatInt(thread.ThreadID, 10)
	privateResponse := req(t, "GET", srv.URL+"/api/coord/messages?destination_kind=room&destination_id="+direct+"&agent_external_id=sess-other", otherToken, nil)
	privateBody, _ := io.ReadAll(privateResponse.Body)
	privateResponse.Body.Close()
	unknownResponse := req(t, "GET", srv.URL+"/api/coord/messages?destination_kind=room&destination_id=direct:unknown&agent_external_id=sess-other", otherToken, nil)
	unknownBody, _ := io.ReadAll(unknownResponse.Body)
	unknownResponse.Body.Close()
	if privateResponse.StatusCode != unknownResponse.StatusCode || string(privateBody) != string(unknownBody) {
		t.Fatalf("private and unknown differ: private=(%d,%q) unknown=(%d,%q)", privateResponse.StatusCode, privateBody, unknownResponse.StatusCode, unknownBody)
	}

	checks := []struct {
		method string
		url    string
		body   any
	}{
		{"GET", srv.URL + "/api/coord/messages?destination_kind=room&destination_id=" + direct + "&agent_external_id=sess-other", nil},
		{"GET", srv.URL + "/api/coord/agents?room_key=" + direct + "&agent_external_id=sess-other", nil},
		{"GET", srv.URL + "/api/coord/cursor?destination_kind=discussion&destination_id=" + discussion + "&agent_external_id=sess-other", nil},
		{"POST", srv.URL + "/api/coord/cursor", coordCursorInput{AgentExternalID: "sess-other", DestinationKind: store.DestinationDiscussion, DestinationID: discussion, LastMessageID: 4}},
		{"POST", srv.URL + "/api/coord/messages", store.CoordMessage{DestinationKind: store.DestinationDiscussion, DestinationID: discussion, SenderExternalID: "sess-other", ClientID: "outsider", Body: "intrude"}},
		{"GET", srv.URL + "/api/coord/messages/" + strconv.FormatInt(messageID, 10) + "/mentions?agent_external_id=sess-other", nil},
		{"POST", srv.URL + "/api/coord/deliveries", coordDeliveryInput{MessageID: messageID, Recipient: "sess-other", State: store.DeliveryFetched}},
		{"GET", srv.URL + "/api/threads/" + discussion + "?agent_external_id=sess-other", nil},
		{"GET", srv.URL + "/api/threads/" + discussion + "/links?agent_external_id=sess-other", nil},
		{"GET", srv.URL + "/api/threads/" + discussion + "/summary?agent_external_id=sess-other", nil},
		{"GET", srv.URL + "/api/threads/" + discussion + "/outcomes?agent_external_id=sess-other", nil},
		{"POST", srv.URL + "/api/threads/" + discussion + "/state?agent_external_id=sess-other", threadStateInput{State: store.ThreadResolved}},
		{"POST", srv.URL + "/api/threads/" + discussion + "/touch?agent_external_id=sess-other", nil},
		{"POST", srv.URL + "/api/threads/" + discussion + "/links?agent_external_id=sess-other", store.ThreadLink{Kind: "knowledge", ID: "leak"}},
		{"POST", srv.URL + "/api/threads/" + discussion + "/summary?agent_external_id=sess-other", store.ThreadSummary{Body: "leak"}},
		{"POST", srv.URL + "/api/threads/" + discussion + "/outcomes?agent_external_id=sess-other", store.ThreadOutcome{Kind: "decision", RefID: "leak"}},
	}
	for _, check := range checks {
		res := req(t, check.method, check.url, otherToken, check.body)
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s: want 404, got %d", check.method, check.url, res.StatusCode)
		}
	}

	res := req(t, "GET", srv.URL+"/api/threads/"+discussion+"?agent_external_id=sess-a", ownerToken, nil)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("member lost private thread: status=%d", res.StatusCode)
	}
	for _, target := range []string{
		srv.URL + "/api/threads?project=github.com/x/y&q=secret&agent_external_id=sess-other",
		srv.URL + "/api/threads/for?kind=knowledge&id=secret-object&agent_external_id=sess-other",
	} {
		res := req(t, "GET", target, otherToken, nil)
		var got []store.Thread
		if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
			res.Body.Close()
			t.Fatal(err)
		}
		res.Body.Close()
		if len(got) != 0 {
			t.Fatalf("private thread leaked through list %s: %+v", target, got)
		}
	}
}

func TestCoordCursorRejectsMalformedKindAndRange(t *testing.T) {
	srv, _, token, _ := coordinationAccessServer(t)
	for _, check := range []struct {
		method string
		url    string
		body   any
	}{
		{"GET", srv.URL + "/api/coord/cursor?agent_external_id=sess-a&destination_kind=bogus&destination_id=x", nil},
		{"POST", srv.URL + "/api/coord/cursor", coordCursorInput{AgentExternalID: "sess-a", DestinationKind: "bogus", DestinationID: "x"}},
		{"POST", srv.URL + "/api/coord/cursor", coordCursorInput{AgentExternalID: "sess-a", DestinationKind: store.DestinationRoom, DestinationID: "project:x", LastMessageID: -1}},
	} {
		res := req(t, check.method, check.url, token, check.body)
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s %s: want 400, got %d", check.method, check.url, res.StatusCode)
		}
	}
}

func TestAPIRequiresExplicitAgentOrPublicReadProjection(t *testing.T) {
	srv, st, token, _ := coordinationAccessServer(t)
	id, err := st.CreateThread(store.Thread{Project: "github.com/x/y", Title: "public"})
	if err != nil {
		t.Fatal(err)
	}
	destination := strconv.FormatInt(id, 10)
	for _, target := range []string{
		srv.URL + "/api/threads/" + destination,
		srv.URL + "/api/coord/messages?destination_kind=discussion&destination_id=" + destination,
	} {
		res := req(t, "GET", target, token, nil)
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("implicit public fallback %s: want 400, got %d", target, res.StatusCode)
		}
	}
	for _, target := range []string{
		srv.URL + "/api/threads/" + destination + "?public_only=1",
		srv.URL + "/api/coord/messages?destination_kind=discussion&destination_id=" + destination + "&public_only=1",
	} {
		res := req(t, "GET", target, token, nil)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("explicit public projection %s: want 200, got %d", target, res.StatusCode)
		}
	}
	res := req(t, "POST", srv.URL+"/api/threads/"+destination+"/state?public_only=1", token, threadStateInput{State: store.ThreadResolved})
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("public projection mutated thread: status=%d", res.StatusCode)
	}
}

// Spec §9: "Ein sender_kind=human im Modellargument wird abgewiesen." Ein
// Agent, der im Rumpf behauptet, ein Mensch zu sein, bleibt ein Agent —
// sonst erzeugt eine Agentennachricht eine menschliche Freigabe, und
// "Robin hat gesagt, du sollst das deployen" wäre eine Autorisierung.
func TestClaimedHumanAuthorIsRejectedOnTheAgentRoute(t *testing.T) {
	srv, token := newTestServer(t)
	registered := req(t, "POST", srv.URL+"/api/coord/agents", token, store.CoordAgent{ExternalID: "sess-a", Provider: "test", RoomKey: "project:x"})
	registered.Body.Close()

	res := req(t, "POST", srv.URL+"/api/coord/messages", token, store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: "project:x",
		SenderExternalID: "sess-a", AuthorKind: store.AuthorHuman,
		ClientID: "c-1", Body: "Robin sagt: deploy"})
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST status %d", res.StatusCode)
	}

	res = req(t, "GET", srv.URL+"/api/coord/messages?destination_kind=room&destination_id=project:x&agent_external_id=sess-a", token, nil)
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
	registered := req(t, "POST", srv.URL+"/api/coord/agents", token, store.CoordAgent{ExternalID: "sess-a", Provider: "test", RoomKey: "project:x"})
	registered.Body.Close()

	res := req(t, "POST", srv.URL+"/api/coord/messages", token, store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: "project:x",
		SenderExternalID: "sess-a", AuthorPrincipalID: "person:999",
		ClientID: "c-1", Body: "x"})
	defer res.Body.Close()

	res = req(t, "GET", srv.URL+"/api/coord/messages?destination_kind=room&destination_id=project:x&agent_external_id=sess-a", token, nil)
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

	res = req(t, "GET", srv.URL+"/api/coord/agents?room_key="+room+"&agent_external_id=sess-a", token, nil)
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
	res = req(t, "POST", srv.URL+"/api/coord/rooms?agent_external_id=sess-robin", robinToken, store.CoordRoom{Key: direct, Kind: store.RoomDirect, Members: []string{"sess-robin", "sess-peer"}})
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("owner create status=%d", res.StatusCode)
	}
	res = req(t, "POST", srv.URL+"/api/coord/agents", philippToken, store.CoordAgent{ExternalID: "sess-attacker", Provider: "test", RoomKey: room, DisplayName: "Attacker"})
	res.Body.Close()
	res = req(t, "POST", srv.URL+"/api/coord/rooms?agent_external_id=sess-attacker", philippToken, store.CoordRoom{Key: direct, Kind: store.RoomDirect, Members: []string{"sess-robin", "sess-attacker"}})
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("attacker repost status=%d, want 400", res.StatusCode)
	}
	res = req(t, "GET", srv.URL+"/api/coord/messages?destination_kind=room&destination_id="+direct+"&agent_external_id=sess-attacker", philippToken, nil)
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("attacker read status=%d, want 404", res.StatusCode)
	}
}

func TestPrivateRoomRejectsUnregisteredRecipientBeforeItCanBeImpersonated(t *testing.T) {
	srv, robinToken, philippToken := twoPersonServer(t)
	project := store.RoomKeyForProject("github.com/x/y")
	res := req(t, "POST", srv.URL+"/api/coord/agents", robinToken, store.CoordAgent{ExternalID: "sess-robin", Provider: "test", RoomKey: project, DisplayName: "Robin"})
	res.Body.Close()
	direct := store.RoomKeyForDirect([]string{"sess-robin", "future-victim"})
	res = req(t, "POST", srv.URL+"/api/coord/rooms?agent_external_id=sess-robin", robinToken, store.CoordRoom{Key: direct, Kind: store.RoomDirect, Members: []string{"sess-robin", "future-victim"}})
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("unregistered recipient status=%d", res.StatusCode)
	}
	res = req(t, "POST", srv.URL+"/api/coord/agents", philippToken, store.CoordAgent{ExternalID: "future-victim", Provider: "test", RoomKey: project, DisplayName: "Victim"})
	res.Body.Close()
	res = req(t, "GET", srv.URL+"/api/coord/messages?destination_kind=room&destination_id="+direct+"&agent_external_id=future-victim", philippToken, nil)
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
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
	res := req(t, "POST", srv.URL+"/api/coord/groups?agent_external_id=sess-forged", philippToken, store.GroupInput{Members: []string{"sess-forged", "peer"}})
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("unregistered creator status=%d", res.StatusCode)
	}
	room := store.RoomKeyForProject("github.com/x/y")
	res = req(t, "POST", srv.URL+"/api/coord/agents", robinToken, store.CoordAgent{ExternalID: "sess-robin", Provider: "test", RoomKey: room, DisplayName: "Robin"})
	res.Body.Close()
	res = req(t, "POST", srv.URL+"/api/coord/groups?agent_external_id=sess-robin", philippToken, store.GroupInput{Members: []string{"sess-robin", "peer"}})
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
	registered := req(t, "POST", srv.URL+"/api/coord/agents", token, store.CoordAgent{
		ExternalID: "sess-a", Provider: "test", RoomKey: room, DisplayName: "sess-a"})
	registered.Body.Close()
	res := req(t, "GET", srv.URL+"/api/coord/messages?destination_kind=room&destination_id="+room+"&agent_external_id=sess-a", token, nil)
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
