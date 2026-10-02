package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
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

func TestCoordAttentionAPIListsAndResolvesForOwnedRecipient(t *testing.T) {
	srv, st, ownerToken, otherToken := coordinationAccessServer(t)
	room := store.RoomKeyForProject("github.com/x/attention-api")
	for _, agent := range []store.CoordAgent{
		{ExternalID: "sess-owner", PrincipalID: "person:1", Person: "owner", Provider: "test", RoomKey: room},
		{ExternalID: "sess-other", PrincipalID: "person:2", Person: "other", Provider: "test", RoomKey: room},
	} {
		if _, err := st.RegisterCoordAgent(agent); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.CoordinationFor(store.Principal{ID: "person:1"}, "sess-owner").Send(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room, ClientID: "api-question",
		Body: "Answer?", Intent: store.IntentQuestion, Mentions: []string{"sess-other"},
	}); err != nil {
		t.Fatal(err)
	}
	res := req(t, "GET", srv.URL+"/api/coord/attention?agent_external_id=sess-other", otherToken, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d", res.StatusCode)
	}
	var items []store.AttentionItem
	if err := json.NewDecoder(res.Body).Decode(&items); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if len(items) != 1 || items[0].State != store.AttentionOpen {
		t.Fatalf("items=%+v", items)
	}
	res = req(t, "POST", srv.URL+"/api/coord/attention/action", otherToken, map[string]any{
		"agent_external_id": "sess-other", "attention_id": items[0].ID, "action": store.AttentionActionAnswer,
	})
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("resolve status=%d", res.StatusCode)
	}
	res = req(t, "POST", srv.URL+"/api/coord/attention/action", ownerToken, map[string]any{
		"agent_external_id": "sess-owner", "attention_id": items[0].ID, "action": store.AttentionActionResolve,
	})
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("nonrecipient action status=%d", res.StatusCode)
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
		{"POST", srv.URL + "/api/coord/deliveries/claim", coordDeliveryInput{MessageID: messageID, Recipient: "sess-other"}},
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

func TestClaimCoordDeliveryRouteGrantsOneWinnerAndRejectsStrangers(t *testing.T) {
	srv, st, ownerToken, otherToken := coordinationAccessServer(t)
	room := store.RoomKeyForProject("github.com/x/claim")
	for _, a := range []store.CoordAgent{
		{ExternalID: "sess-a", PrincipalID: "person:1", Person: "owner", Provider: "test", RoomKey: room},
		{ExternalID: "sess-b", PrincipalID: "person:1", Person: "owner", Provider: "test", RoomKey: room},
		{ExternalID: "sess-other", PrincipalID: "person:2", Person: "other", Provider: "test", RoomKey: room},
	} {
		if _, err := st.RegisterCoordAgent(a); err != nil {
			t.Fatal(err)
		}
	}
	id, err := st.CoordinationFor(store.Principal{ID: "person:1"}, "sess-a").Send(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room, ClientID: "claim-src", Body: "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	claim := func(token, recipient string) (int, bool) {
		res := req(t, "POST", srv.URL+"/api/coord/deliveries/claim", token, coordDeliveryInput{MessageID: id, Recipient: recipient})
		defer res.Body.Close()
		var out struct {
			Claimed bool `json:"claimed"`
		}
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out.Claimed
	}
	if status, claimed := claim(ownerToken, "sess-b"); status != http.StatusOK || !claimed {
		t.Fatalf("first claim: status=%d claimed=%v", status, claimed)
	}
	if status, claimed := claim(ownerToken, "sess-b"); status != http.StatusOK || claimed {
		t.Fatalf("second claim: status=%d claimed=%v", status, claimed)
	}
	if status, claimed := claim(otherToken, "sess-b"); status == http.StatusOK || claimed {
		t.Fatalf("foreign token claimed for another principal's session: status=%d", status)
	}
	res := req(t, "POST", srv.URL+"/api/coord/deliveries/claim", ownerToken, coordDeliveryInput{})
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty claim: status=%d", res.StatusCode)
	}
	list := req(t, "GET", srv.URL+"/api/coord/deliveries/injected?agent_external_id=sess-b&message_ids="+strconv.FormatInt(id, 10), ownerToken, nil)
	var got struct {
		IDs []int64 `json:"message_ids"`
	}
	_ = json.NewDecoder(list.Body).Decode(&got)
	list.Body.Close()
	if len(got.IDs) != 1 || got.IDs[0] != id {
		t.Fatalf("injected = %v", got.IDs)
	}
}

func TestInjectedLookupIsBoundedAndValidated(t *testing.T) {
	srv, _, ownerToken, _ := coordinationAccessServer(t)
	status := func(ids string) int {
		res := req(t, "GET", srv.URL+"/api/coord/deliveries/injected?agent_external_id=sess-b&message_ids="+ids, ownerToken, nil)
		res.Body.Close()
		return res.StatusCode
	}
	many := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = strconv.Itoa(i + 1)
		}
		return strings.Join(parts, ",")
	}
	if got := status(many(maxInjectedLookup)); got == http.StatusBadRequest {
		t.Fatalf("exactly %d ids must be accepted, got %d", maxInjectedLookup, got)
	}
	for name, ids := range map[string]string{
		"too many":    many(maxInjectedLookup + 1),
		"not numeric": "1,abc",
		"zero":        "0",
		"negative":    "1,-5",
	} {
		if got := status(ids); got != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d", name, got)
		}
	}
}

// A bare session UUID is never a registered agent, so the agent-ownership check
// passes for everyone. Activity is therefore bound to the account that owns
// the uploaded session, and its time is the server's (REQ-360 review H1).
func TestActivityIsBoundToTheSessionsAccountAndItsTimeIsClamped(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	robin, _ := st.AddPerson("robin")
	anna, _ := st.AddPerson("anna")
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)
	// Robin's collector has uploaded this transcript (account 0 is the owner).
	if _, err := st.UpsertSession(store.Session{Harness: "claude", ExternalID: "uuid-robin"}); err != nil {
		t.Fatal(err)
	}
	post := func(token, session, at string) int {
		res := req(t, "POST", srv.URL+"/api/activity", token, []store.PathActivity{
			{SessionExternalID: session, Tool: "Edit", Path: "a.go", Quality: store.ActivityIntent, At: at}})
		res.Body.Close()
		return res.StatusCode
	}
	future := "2099-01-01T00:00:00Z"
	if code := post(anna, "uuid-robin", future); code != http.StatusForbidden {
		t.Fatalf("anna posting for robin's session: want 403, got %d", code)
	}
	if code := post(robin, "uuid-never-uploaded", future); code != http.StatusForbidden {
		t.Fatalf("a session nobody uploaded has no owner: want 403, got %d", code)
	}
	if code := post(robin, "uuid-robin", future); code != http.StatusOK {
		t.Fatalf("robin posting for his own session: got %d", code)
	}
	got, err := st.SessionPathActivity("uuid-robin", 1, "2000-01-01T00:00:00Z", 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("stored activity = %+v err=%v", got, err)
	}
	at, _ := time.Parse(time.RFC3339, got[0].At)
	if d := time.Since(at); d < -time.Minute || d > time.Minute {
		t.Fatalf("a future client time must be replaced by server time, stored %s", got[0].At)
	}
	// A time within five minutes is kept (normalised to UTC).
	near := time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339)
	post(robin, "uuid-robin", near)
	rows, _ := st.SessionPathActivity("uuid-robin", 1, "2000-01-01T00:00:00Z", 10)
	found := false
	for _, r := range rows {
		found = found || r.At == near
	}
	if !found {
		t.Fatalf("a time within the skew window must be kept: %+v", rows)
	}
	// Reading is bound the same way.
	res := req(t, "GET", srv.URL+"/api/activity/session?session=uuid-robin", anna, nil)
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("anna reading robin's session activity: got %d", res.StatusCode)
	}
}

// Sessions are unique per (harness, external id) only. A foreign account that
// uploads the same id under another harness must neither lock the owner out
// nor be able to speak for the owner's session.
func TestForeignSessionRowWithTheSameIDDoesNotBlockTheOwner(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	robin, _ := st.AddPerson("robin")
	anna, _ := st.AddPerson("anna")
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)
	if _, err := st.UpsertSession(store.Session{Harness: "claude", ExternalID: "uuid-r"}); err != nil {
		t.Fatal(err)
	}
	// anna learns the id and uploads it under another harness.
	if _, err := st.UpsertSession(store.Session{Harness: "codex", ExternalID: "uuid-r", AccountID: 2}); err != nil {
		t.Fatal(err)
	}
	post := func(token string) int {
		res := req(t, "POST", srv.URL+"/api/activity", token, []store.PathActivity{
			{SessionExternalID: "uuid-r", Tool: "Edit", Path: "a.go", Quality: store.ActivityIntent}})
		res.Body.Close()
		return res.StatusCode
	}
	if code := post(robin); code != http.StatusOK {
		t.Fatalf("a foreign row must not lock robin out: got %d", code)
	}
	if code := post(anna); code != http.StatusOK {
		t.Fatalf("anna owns her own row: got %d", code)
	}
	// Each account's rows stay its own.
	mine, _ := st.SessionPathActivity("uuid-r", 1, "2000-01-01T00:00:00Z", 10)
	hers, _ := st.SessionPathActivity("uuid-r", 2, "2000-01-01T00:00:00Z", 10)
	if len(mine) != 1 || len(hers) != 1 || mine[0].AccountID != 1 || hers[0].AccountID != 2 {
		t.Fatalf("rows are not bound to their accounts: %+v / %+v", mine, hers)
	}
}

func TestActivityTimeIsClampedToThirtySecondsAhead(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	for name, c := range map[string]struct{ in, want string }{
		"20 s ahead is kept":     {f(20 * time.Second), f(20 * time.Second)},
		"60 s ahead is replaced": {f(60 * time.Second), f(0)},
		"4 min back is kept":     {f(-4 * time.Minute), f(-4 * time.Minute)},
		"6 min back is replaced": {f(-6 * time.Minute), f(0)},
		"unreadable is replaced": {"yesterday", f(0)},
		"offset is made UTC":     {now.In(time.FixedZone("x", 7200)).Format(time.RFC3339), f(0)},
	} {
		if got := clampActivityTime(c.in, now); got != c.want {
			t.Errorf("%s: got %s, want %s", name, got, c.want)
		}
	}
}

// The session id is the key activity is attributed under. Members who are
// neither the row's owner nor a lead see the agent that reported it instead
// (REQ-360 round 2), and the asker's own session is left out by UUID too.
func TestPathActivityMasksSessionIDsForOrdinaryMembers(t *testing.T) {
	f, _ := roleAPI(t)
	if err := f.st.SetProjectRole("person:1", apiRoleProject, "person:2", store.RoleMember, false, store.RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetProjectRole("person:1", apiRoleProject, "person:3", store.RoleLead, false, store.RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.UpsertSession(store.Session{Harness: "claude", ExternalID: "uuid-r", Scope: scope.Axes{Project: apiRoleProject}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.RegisterCoordAgent(store.CoordAgent{ExternalID: "claude:h:robin", Provider: "claude", PrincipalID: "person:1",
		RoomKey: store.RoomKeyForProject(apiRoleProject), SessionID: "uuid-r"}); err != nil {
		t.Fatal(err)
	}
	res := req(t, "POST", f.srv.URL+"/api/activity", f.robin, []store.PathActivity{
		{Project: apiRoleProject, SessionExternalID: "uuid-r", Checkout: "/home/robin/repo", Tool: "Edit", Path: "a.go", Quality: store.ActivityIntent}})
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("record: %d", res.StatusCode)
	}
	seen := func(token, query string) []string {
		res := req(t, "GET", f.srv.URL+"/api/activity/path?project="+apiRoleProject+"&path=a.go"+query, token, nil)
		defer res.Body.Close()
		var rows []store.PathActivity
		if err := json.NewDecoder(res.Body).Decode(&rows); err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, r := range rows {
			ids = append(ids, r.SessionExternalID)
			if r.SessionExternalID != "uuid-r" && r.Checkout != "" {
				t.Fatalf("a masked row must not carry the foreign checkout path: %+v", r)
			}
		}
		return ids
	}
	// Log mode: nothing is hidden yet.
	if got := seen(f.anna, ""); len(got) != 1 || got[0] != "uuid-r" {
		t.Fatalf("log mode must not mask: %v", got)
	}
	f.st.SetAccessMode(store.AccessMode{Enforce: true})
	if got := seen(f.anna, ""); len(got) != 1 || got[0] != "claude:h:robin" {
		t.Fatalf("a member must see the agent, not the session id: %v", got)
	}
	if got := seen(f.ben, ""); len(got) != 1 || got[0] != "uuid-r" {
		t.Fatalf("a lead sees the session id: %v", got)
	}
	if got := seen(f.robin, ""); len(got) != 1 || got[0] != "uuid-r" {
		t.Fatalf("the owner of the row sees the session id: %v", got)
	}
	// N4: asking as the agent leaves out its own session, found by its UUID.
	if got := seen(f.robin, "&exclude_session=claude:h:robin"); len(got) != 0 {
		t.Fatalf("the asker's own session must be excluded by its registered UUID: %v", got)
	}
}

// AC 1221 (REQ-360): die Zustellroute mit state acked erzeugt keinen
// Chatbeitrag, keinen Attention-Eintrag, keinen Weckkandidaten und keinen
// Claim für andere Empfänger.
func TestAckedDeliveryRouteCreatesNoPostNoAttentionNoWakeAndNoClaim(t *testing.T) {
	srv, st, ownerToken, otherToken := coordinationAccessServer(t)
	room := store.RoomKeyForProject("github.com/x/ackroute")
	for _, a := range []store.CoordAgent{
		{ExternalID: "sess-a", PrincipalID: "person:1", Person: "owner", Provider: "test", RoomKey: room},
		{ExternalID: "sess-b", PrincipalID: "person:1", Person: "owner", Provider: "test", RoomKey: room},
		{ExternalID: "sess-other", PrincipalID: "person:2", Person: "other", Provider: "test", RoomKey: room},
	} {
		if _, err := st.RegisterCoordAgent(a); err != nil {
			t.Fatal(err)
		}
	}
	sender := st.CoordinationFor(store.Principal{ID: "person:1"}, "sess-a")
	id, err := sender.Send(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room, ClientID: "ack-route-src",
		Body: "please look", Intent: store.IntentQuestion, Mentions: []string{"person:2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := st.CoordMessagesSince(store.DestinationRoom, room, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	other := st.CoordinationFor(store.Principal{ID: "person:2"}, "sess-other")
	attentionBefore, err := other.Attention()
	if err != nil {
		t.Fatal(err)
	}
	senderAttentionBefore, err := sender.Attention()
	if err != nil {
		t.Fatal(err)
	}

	res := req(t, "POST", srv.URL+"/api/coord/deliveries", ownerToken, coordDeliveryInput{MessageID: id, Recipient: "sess-b", State: store.DeliveryAcked})
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("ack route: status=%d", res.StatusCode)
	}

	after, err := st.CoordMessagesSince(store.DestinationRoom, room, 0, 100)
	if err != nil || len(after) != len(before) || after[len(after)-1].ID != before[len(before)-1].ID {
		t.Fatalf("ack created a chat post: before=%d after=%d err=%v", len(before), len(after), err)
	}
	attentionAfter, err := other.Attention()
	if err != nil || len(attentionAfter) != len(attentionBefore) {
		t.Fatalf("ack changed the recipient's attention: %+v err=%v", attentionAfter, err)
	}
	senderAttentionAfter, err := sender.Attention()
	if err != nil || len(senderAttentionAfter) != len(senderAttentionBefore) {
		t.Fatalf("ack changed the sender's attention: %+v err=%v", senderAttentionAfter, err)
	}
	// Kein neuer Beitrag, also nichts, worauf ein Poller wecken könnte.
	fresh := req(t, "GET", srv.URL+"/api/coord/messages?destination_kind=room&destination_id="+room+"&agent_external_id=sess-a&after="+strconv.FormatInt(id, 10), ownerToken, nil)
	var inbox []store.CoordMessage
	_ = json.NewDecoder(fresh.Body).Decode(&inbox)
	fresh.Body.Close()
	if fresh.StatusCode != http.StatusOK {
		t.Fatalf("inbox status=%d", fresh.StatusCode)
	}
	if len(inbox) != 0 {
		t.Fatalf("something new in the sender's inbox after an ack: %+v", inbox)
	}
	// Kein Claim für andere: der Empfänger des anderen Principals gewinnt noch.
	claim := req(t, "POST", srv.URL+"/api/coord/deliveries/claim", otherToken, coordDeliveryInput{MessageID: id, Recipient: "sess-other"})
	var claimed struct {
		Claimed bool `json:"claimed"`
	}
	_ = json.NewDecoder(claim.Body).Decode(&claimed)
	claim.Body.Close()
	if claim.StatusCode != http.StatusOK || !claimed.Claimed {
		t.Fatalf("another recipient's claim was lost after an ack: status=%d claimed=%v", claim.StatusCode, claimed.Claimed)
	}
	if state, _ := st.CoordDeliveryState(id, "sess-b"); state != store.DeliveryAcked {
		t.Fatalf("acked recipient state = %q", state)
	}
}

// Die Agent-Route nimmt intent vom Client an (question, approval, blocker,
// handoff, ack), weist aber alles ab, was Menschen oder dem Store vorbehalten
// ist oder gar nicht existiert.
func TestCoordAPISendAcceptsAttentionIntentAndRejectsReservedOnes(t *testing.T) {
	srv, st, token, _ := coordinationAccessServer(t)
	room := store.RoomKeyForProject("github.com/x/intent")
	for _, id := range []string{"sess-a", "sess-b"} {
		if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: id, PrincipalID: "person:1", Person: "owner", Provider: "test", RoomKey: room}); err != nil {
			t.Fatal(err)
		}
	}
	send := func(clientID, intent string) int {
		res := req(t, "POST", srv.URL+"/api/coord/messages", token, store.CoordMessage{
			DestinationKind: store.DestinationRoom, DestinationID: room,
			SenderExternalID: "sess-a", ClientID: clientID, Body: "x " + clientID,
			Intent: intent, Mentions: []string{"sess-b"},
		})
		res.Body.Close()
		return res.StatusCode
	}
	for _, intent := range []string{"question", "approval", "blocker", "handoff", "ack", ""} {
		if code := send("ok-"+intent, intent); code != http.StatusOK {
			t.Errorf("intent %q: want 200, got %d", intent, code)
		}
	}
	for _, intent := range []string{"standing", "attention", "bogus"} {
		if code := send("bad-"+intent, intent); code != http.StatusBadRequest {
			t.Errorf("intent %q: want 400, got %d", intent, code)
		}
	}
	msgs, err := st.CoordinationFor(store.Principal{ID: "person:1"}, "sess-b").Messages(store.DestinationRoom, room, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	var q bool
	for _, m := range msgs {
		if m.Body == "x ok-question" && m.Intent == store.IntentQuestion {
			q = true
		}
	}
	if !q {
		t.Fatalf("question intent not stored: %+v", msgs)
	}
}
