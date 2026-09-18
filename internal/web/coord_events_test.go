package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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
	if !strings.Contains(page, `data-coord-event-cursor="`) || !strings.Contains(page, `data-coord-live-status`) || !strings.Contains(page, "Live-Verbindung wird aufgebaut") {
		t.Fatalf("coord live bootstrap missing: %s", page)
	}
}

func TestCoordProgressiveClientRefetchesCanonicalVisibleSurfaces(t *testing.T) {
	source := string(mustReadEmbedded(t, "static/app.js"))
	for _, want := range []string{
		"new EventSource", "coord.changed", "resync", "DOMParser", "location.href",
		"URLSearchParams", "around", "thread_around", "data-coord-live-status",
		"replaceChildren", "getBoundingClientRect", "container.scrollTop", "messageScrollTop", "contextScrollTop",
		"captureFocus", "restoreFocus", "[data-coord-sidebar-dynamic]", "[data-coord-read-actions]",
		"raw.lastEventId", "Number.isSafeInteger", "eventID <= lastEventID",
		"captureDrafts", "restoreDrafts", "coordDraftKey", "container.open", "control.checked",
		"session-ended", "source.close()", "visibilitychange",
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
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad cursor status=%d", response.StatusCode)
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
	for i := 0; i < 520; i++ {
		if _, err := st.AppendCoordMessage(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "fixture:" + room, ClientID: "sse-prune-" + strconv.Itoa(i), Body: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	body, _ := readFiniteSSE(t, client, srv.URL+"/ui/coord/events", "1")
	if !strings.Contains(body, "event: resync") {
		t.Fatalf("stream=%q", body)
	}
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
