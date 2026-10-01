package claudechannel

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCapabilitiesClaimOnlyWhatWasMeasured(t *testing.T) {
	caps := map[string]bool{}
	for _, c := range Capabilities() {
		caps[c] = true
	}
	if !caps[CapReceiveAtSafePoint] || !caps[CapWakeIdleSession] {
		t.Errorf("both measured capabilities must be claimed: %v", Capabilities())
	}
	missing := MissingCapabilities()
	for _, c := range []string{CapReceiveForSubagent, CapHumanSteer, CapHumanInterrupt, CapActivityObserve} {
		if caps[c] || missing[c] == "" {
			t.Errorf("%s must be a named gap with a reason, not a claim", c)
		}
	}
	if len(Notes()) != 1 || Notes()[0] != OptInAtStartOnly {
		t.Errorf("notes = %v", Notes())
	}
}

// Der Wire-Test: die Notification ist eine jsonrpc-Request ohne id, mit genau
// der Form, die Claude Code erwartet, und der Server deklariert die Fähigkeit.
func TestNotificationWireForm(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	serverT, clientT := mcp.NewInMemoryTransports()
	tr := NewTransport(serverT)
	srv := mcp.NewServer(&mcp.Implementation{Name: "ghosttree-channel", Version: "test"}, ServerOptions())
	ss, err := srv.Connect(ctx, tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()

	conn, err := clientT.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	send := func(raw string) {
		msg, err := jsonrpc.DecodeMessage([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.Write(ctx, msg); err != nil {
			t.Fatal(err)
		}
	}
	read := func() map[string]any {
		msg, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := jsonrpc.EncodeMessage(msg)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
	init := read()
	caps := init["result"].(map[string]any)["capabilities"].(map[string]any)
	exp, _ := caps["experimental"].(map[string]any)
	if _, ok := exp["claude/channel"]; !ok {
		t.Fatalf("capabilities.experimental[claude/channel] missing: %v", caps)
	}
	if tr.Ready() {
		t.Fatal("transport must not be ready before notifications/initialized")
	}
	if err := tr.Notify(ctx, Notification{}); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("notify before handshake: %v", err)
	}
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	for deadline := time.Now().Add(5 * time.Second); !tr.Ready(); {
		if time.Now().After(deadline) {
			t.Fatal("transport never became ready after notifications/initialized")
		}
		time.Sleep(time.Millisecond)
	}

	room := store.CoordRoom{Key: "direct:a:b", Kind: store.RoomDirect}
	m := store.CoordMessage{ID: 42, SenderExternalID: "sess-b", OriginEventID: "ev-7"}
	if err := tr.Notify(ctx, NewNotification(room, m, "hallo")); err != nil {
		t.Fatal(err)
	}
	got := read()
	want := map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/claude/channel",
		"params": map[string]any{
			"content": "hallo",
			"meta": map[string]any{
				"message_id": "42", "origin_event_id": "ev-7",
				"room": "direct:a:b", "room_kind": "direct", "sender": "sess-b",
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wire form\n got %v\nwant %v", got, want)
	}
	if _, hasID := got["id"]; hasID {
		t.Fatal("a notification must not carry an id")
	}
	ss.Close()
	if tr.Ready() {
		t.Fatal("transport must not be ready after close")
	}
	if err := tr.Notify(ctx, Notification{}); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("notify after close: %v", err)
	}
}

func TestNotifyBeforeConnectFails(t *testing.T) {
	a, _ := mcp.NewInMemoryTransports()
	if err := NewTransport(a).Notify(context.Background(), Notification{}); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("err = %v", err)
	}
}

func TestWakeFilter(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Minute).Format(time.RFC3339)
	future := now.Add(time.Hour).Format(time.RFC3339)
	msg := func(sender, expires string, expired bool) store.CoordMessage {
		return store.CoordMessage{ID: 1, SenderExternalID: sender, ExpiresAt: expires, Expired: expired}
	}
	for _, c := range []struct {
		name     string
		kind     string
		m        store.CoordMessage
		mentions []string
		want     bool
	}{
		{"direct wakes", store.RoomDirect, msg("peer", "", false), nil, true},
		{"group wakes", store.RoomGroup, msg("peer", "", false), nil, true},
		{"project without mention", store.RoomProject, msg("peer", "", false), nil, false},
		{"project mentioning someone else", store.RoomProject, msg("peer", "", false), []string{"other"}, false},
		{"project mentioning me", store.RoomProject, msg("peer", "", false), []string{"other", "me"}, true},
		{"machine mentioning me", store.RoomMachine, msg("peer", "", false), []string{"me"}, true},
		{"own message in direct", store.RoomDirect, msg("me", "", false), nil, false},
		{"own subagent message", store.RoomDirect, msg("me/reviewer", "", false), nil, false},
		{"similar name is not own", store.RoomDirect, msg("me2", "", false), nil, true},
		{"own message mentioning me", store.RoomProject, msg("me", "", false), []string{"me"}, false},
		{"flagged expired", store.RoomDirect, msg("peer", "", true), nil, false},
		{"expires_at in the past", store.RoomDirect, msg("peer", past, false), nil, false},
		{"expires_at in the future", store.RoomDirect, msg("peer", future, false), nil, true},
		{"expired mention", store.RoomProject, msg("peer", past, false), []string{"me"}, false},
		{"unknown room kind", "weird", msg("peer", "", false), []string{"me"}, false},
	} {
		if got := ShouldWake("me", c.kind, c.m, c.mentions, now); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// fakeServer ist der gemeinsame Zustand, gegen den mehrere Poller laufen.
type fakeServer struct {
	mu       sync.Mutex
	rooms    []store.CoordRoom
	msgs     map[string][]store.CoordMessage
	mentions map[int64][]string
	claimed  map[int64]bool
	cursor   map[string]int64
	events   []string
}

func newFakeServer(rooms ...store.CoordRoom) *fakeServer {
	return &fakeServer{rooms: rooms, msgs: map[string][]store.CoordMessage{}, mentions: map[int64][]string{},
		claimed: map[int64]bool{}, cursor: map[string]int64{}}
}

func (f *fakeServer) log(e string) { f.events = append(f.events, e) }

func (f *fakeServer) Rooms(string) ([]store.CoordRoom, error) { return f.rooms, nil }
func (f *fakeServer) Inbox(_ string, room store.CoordRoom, after int64, limit int) ([]store.CoordMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.CoordMessage
	for _, m := range f.msgs[room.Key] {
		if m.ID > after && len(out) < limit {
			out = append(out, m)
		}
	}
	return out, nil
}
func (f *fakeServer) Mentions(_ string, id int64) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mentions[id], nil
}
func (f *fakeServer) Claim(_ string, id int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimed[id] {
		return false, nil
	}
	f.claimed[id] = true
	f.log("claim")
	return true, nil
}
func (f *fakeServer) Cursor(_ string, room store.CoordRoom) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cursor[room.Key], nil
}
func (f *fakeServer) SetCursor(_ string, room store.CoordRoom, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cursor[room.Key] = id
	f.log("cursor")
	return nil
}

type fakeNotifier struct {
	f     *fakeServer
	got   []Notification
	fail  error
	notUp bool // nicht bereit: Handshake fehlt oder Verbindung zu
}

func (n *fakeNotifier) Ready() bool {
	n.f.mu.Lock()
	defer n.f.mu.Unlock()
	return !n.notUp
}

func (n *fakeNotifier) Notify(_ context.Context, note Notification) error {
	n.f.mu.Lock()
	defer n.f.mu.Unlock()
	if n.fail != nil {
		return n.fail
	}
	n.got = append(n.got, note)
	n.f.log("notify")
	return nil
}

// openBudget ist ein Budget ohne Dateizustand. exhausted ruft emit("") wie
// hookbudget bei einem leeren Konto; err scheitert vor emit wie ein
// unlesbares Konto.
type openBudget struct {
	exhausted bool
	err       error
}

func (b openBudget) Deliver(_, text string, emit func(string) error) error {
	if b.err != nil {
		return b.err
	}
	if b.exhausted {
		return emit("")
	}
	return emit(text)
}

var testNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func newPoller(f *fakeServer, n Notifier) *Poller {
	return &Poller{Self: "me", Source: f, Notifier: n, Budget: openBudget{}, Now: func() time.Time { return testNow }}
}

var directRoom = store.CoordRoom{Key: "direct:me:peer", Kind: store.RoomDirect}

func TestTwoPollersDeliverOneMessageExactlyOnce(t *testing.T) {
	f := newFakeServer(directRoom)
	f.msgs[directRoom.Key] = []store.CoordMessage{{ID: 5, SenderExternalID: "peer", Body: "eins"}}
	n1, n2 := &fakeNotifier{f: f}, &fakeNotifier{f: f}
	p1, p2 := newPoller(f, n1), newPoller(f, n2)
	var wg sync.WaitGroup
	for _, p := range []*Poller{p1, p2} {
		wg.Add(1)
		go func(p *Poller) {
			defer wg.Done()
			if _, err := p.Poll(context.Background()); err != nil {
				t.Error(err)
			}
		}(p)
	}
	wg.Wait()
	if total := len(n1.got) + len(n2.got); total != 1 {
		t.Fatalf("notifications = %d, want exactly 1", total)
	}
	// Auch ein späterer Durchlauf stellt nicht erneut zu.
	_, _ = p1.Poll(context.Background())
	_, _ = p2.Poll(context.Background())
	if total := len(n1.got) + len(n2.got); total != 1 {
		t.Fatalf("redelivered: %d", total)
	}
}

func TestPollerOrdersClaimNotifyCursor(t *testing.T) {
	f := newFakeServer(directRoom)
	f.msgs[directRoom.Key] = []store.CoordMessage{
		{ID: 1, SenderExternalID: "peer", Body: "a"},
		{ID: 2, SenderExternalID: "peer", Body: "b"},
	}
	n := &fakeNotifier{f: f}
	if _, err := newPoller(f, n).Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"claim", "notify", "cursor", "claim", "notify", "cursor"}
	if !reflect.DeepEqual(f.events, want) {
		t.Fatalf("events = %v, want %v", f.events, want)
	}
	if f.cursor[directRoom.Key] != 2 {
		t.Fatalf("cursor = %d", f.cursor[directRoom.Key])
	}
	if n.got[0].Meta["message_id"] != "1" || n.got[1].Content != "b" {
		t.Fatalf("notifications = %+v", n.got)
	}
}

func TestPollerLeavesUndeliveredTrafficReadableForPull(t *testing.T) {
	project := store.CoordRoom{Key: "project:x", Kind: store.RoomProject}
	f := newFakeServer(project)
	f.msgs[project.Key] = []store.CoordMessage{
		{ID: 1, SenderExternalID: "peer", Body: "chatter"},
		{ID: 2, SenderExternalID: "peer", Body: "@me"},
	}
	f.mentions[2] = []string{"me"}
	n := &fakeNotifier{f: f}
	if _, err := newPoller(f, n).Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(n.got) != 1 || n.got[0].Content != "@me" {
		t.Fatalf("notifications = %+v", n.got)
	}
	if f.cursor[project.Key] != 0 {
		t.Fatalf("cursor moved past unread chatter: %d", f.cursor[project.Key])
	}
}

func TestPollerNeverClaimsWhatItWillNotWake(t *testing.T) {
	project := store.CoordRoom{Key: "project:x", Kind: store.RoomProject}
	f := newFakeServer(project, directRoom)
	f.msgs[project.Key] = []store.CoordMessage{{ID: 1, SenderExternalID: "peer", Body: "chatter"}}
	f.msgs[directRoom.Key] = []store.CoordMessage{
		{ID: 2, SenderExternalID: "me", Body: "own"},
		{ID: 3, SenderExternalID: "peer", Body: "old", Expired: true},
	}
	n := &fakeNotifier{f: f}
	if _, err := newPoller(f, n).Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.claimed) != 0 || len(n.got) != 0 {
		t.Fatalf("claimed=%v notified=%d", f.claimed, len(n.got))
	}
}

func TestNotReadyNotifierLosesNothing(t *testing.T) {
	f := newFakeServer(directRoom)
	f.msgs[directRoom.Key] = []store.CoordMessage{
		{ID: 1, SenderExternalID: "peer", Body: "a"},
		{ID: 2, SenderExternalID: "peer", Body: "b"},
	}
	n := &fakeNotifier{f: f, notUp: true}
	p := newPoller(f, n)
	for i := 0; i < 3; i++ {
		if active, err := p.Poll(context.Background()); err != nil || active {
			t.Fatalf("not ready: active=%v err=%v", active, err)
		}
	}
	if len(f.claimed) != 0 || len(n.got) != 0 {
		t.Fatalf("claimed=%v notified=%d while not ready", f.claimed, len(n.got))
	}
	n.notUp = false
	if _, err := p.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(n.got) != 2 || n.got[0].Content != "a" || n.got[1].Content != "b" {
		t.Fatalf("after readiness want exactly the two messages once, got %+v", n.got)
	}
	_, _ = p.Poll(context.Background())
	if len(n.got) != 2 {
		t.Fatalf("redelivered: %d", len(n.got))
	}
}

// Wird der Notifier mitten im Stapel unbereit, gilt dasselbe.
func TestNotifierGoingAwayMidBatchStopsBeforeTheNextClaim(t *testing.T) {
	f := newFakeServer(directRoom)
	f.msgs[directRoom.Key] = []store.CoordMessage{
		{ID: 1, SenderExternalID: "peer", Body: "a"},
		{ID: 2, SenderExternalID: "peer", Body: "b"},
	}
	n := &fakeNotifier{f: f}
	p := newPoller(f, n)
	p.Budget = budgetFunc(func(_, text string, emit func(string) error) error {
		err := emit(text)
		n.f.mu.Lock()
		n.notUp = true // Verbindung weg nach der ersten Zustellung
		n.f.mu.Unlock()
		return err
	})
	if _, err := p.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(n.got) != 1 || f.claimed[2] {
		t.Fatalf("delivered=%d claimed2=%v", len(n.got), f.claimed[2])
	}
	n.notUp = false
	p.Budget = openBudget{}
	if _, err := p.Poll(context.Background()); err != nil || len(n.got) != 2 {
		t.Fatalf("err=%v delivered=%d", err, len(n.got))
	}
}

type budgetFunc func(session, text string, emit func(string) error) error

func (b budgetFunc) Deliver(s, text string, emit func(string) error) error { return b(s, text, emit) }

func TestBudgetReadErrorFailsClosed(t *testing.T) {
	f := newFakeServer(directRoom)
	f.msgs[directRoom.Key] = []store.CoordMessage{{ID: 1, SenderExternalID: "peer", Body: "x"}}
	n := &fakeNotifier{f: f}
	p := newPoller(f, n)
	p.Budget = openBudget{err: errors.New("state dir unreadable")}
	active, err := p.Poll(context.Background())
	if err == nil || active {
		t.Fatalf("budget error must surface and not count as activity: active=%v err=%v", active, err)
	}
	if len(f.claimed) != 0 || len(n.got) != 0 {
		t.Fatal("no claim without a reserved budget")
	}
	p.Budget = openBudget{}
	if _, err := p.Poll(context.Background()); err != nil || len(n.got) != 1 {
		t.Fatalf("after recovery: err=%v delivered=%d", err, len(n.got))
	}
}

// Der Budgetfehler geht über OnError, und Run geht in den Backoff.
func TestBudgetReadErrorIsReportedAndBacksOff(t *testing.T) {
	f := newFakeServer(directRoom)
	f.msgs[directRoom.Key] = []store.CoordMessage{{ID: 1, SenderExternalID: "peer", Body: "x"}}
	ctx, cancel := context.WithCancel(context.Background())
	rec := &sleepRecorder{cancel: cancel, stop: 4}
	var errs []error
	p := newPoller(f, &fakeNotifier{f: f})
	p.Budget = openBudget{err: errors.New("unreadable")}
	p.Sleep = rec.Sleep
	p.OnError = func(err error) { errs = append(errs, err) }
	_ = p.Run(ctx)
	s := time.Second
	if len(errs) != 4 || !reflect.DeepEqual(rec.d, []time.Duration{2 * s, 4 * s, 8 * s, 10 * s}) {
		t.Fatalf("errs=%d waits=%v", len(errs), rec.d)
	}
}

// Ein dauerhaft scheiternder Claim blockiert den Raum, geht aber in den Backoff
// statt im 2-s-Takt weiterzulaufen.
func TestPersistentClaimFailureBacksOff(t *testing.T) {
	f := newFakeServer(directRoom)
	f.msgs[directRoom.Key] = []store.CoordMessage{{ID: 1, SenderExternalID: "peer", Body: "x"}}
	n := &fakeNotifier{f: f}
	ctx, cancel := context.WithCancel(context.Background())
	rec := &sleepRecorder{cancel: cancel, stop: 3}
	p := newPoller(f, n)
	p.Source = &claimFails{fakeServer: f}
	p.Sleep = rec.Sleep
	errs := 0
	p.OnError = func(error) { errs++ }
	_ = p.Run(ctx)
	s := time.Second
	if errs != 3 || !reflect.DeepEqual(rec.d, []time.Duration{2 * s, 4 * s, 8 * s}) {
		t.Fatalf("errs=%d waits=%v", errs, rec.d)
	}
	if len(n.got) != 0 {
		t.Fatal("nothing may be delivered when the claim never succeeds")
	}
}

type claimFails struct{ *fakeServer }

func (c *claimFails) Claim(string, int64) (bool, error) { return false, errors.New("server down") }

// Mentions, die dauerhaft scheitern, verhalten sich genauso.
func TestPersistentMentionsFailureBacksOff(t *testing.T) {
	project := store.CoordRoom{Key: "project:x", Kind: store.RoomProject}
	f := newFakeServer(project)
	f.msgs[project.Key] = []store.CoordMessage{{ID: 1, SenderExternalID: "peer", Body: "x"}}
	ctx, cancel := context.WithCancel(context.Background())
	rec := &sleepRecorder{cancel: cancel, stop: 3}
	p := newPoller(f, &fakeNotifier{f: f})
	p.Source = &mentionsFail{fakeServer: f}
	p.Sleep = rec.Sleep
	_ = p.Run(ctx)
	s := time.Second
	if !reflect.DeepEqual(rec.d, []time.Duration{2 * s, 4 * s, 8 * s}) {
		t.Fatalf("waits=%v", rec.d)
	}
}

type mentionsFail struct{ *fakeServer }

func (m *mentionsFail) Mentions(string, int64) ([]string, error) {
	return nil, errors.New("server down")
}

// Erschöpft das Budget erst beim Reservieren (emit("")), ist nichts geclaimt,
// und der Durchlauf zählt nicht als Aktivität.
func TestExhaustedBudgetDoesNotClaimOrCountAsActivity(t *testing.T) {
	f := newFakeServer(directRoom)
	f.msgs[directRoom.Key] = []store.CoordMessage{{ID: 1, SenderExternalID: "peer", Body: "x"}}
	n := &fakeNotifier{f: f}
	p := newPoller(f, n)
	p.Budget = openBudget{exhausted: true}
	active, err := p.Poll(context.Background())
	if err != nil || active {
		t.Fatalf("active=%v err=%v", active, err)
	}
	if len(f.claimed) != 0 || len(n.got) != 0 {
		t.Fatal("an exhausted budget must leave the message unclaimed for the pull path")
	}
	p.Budget = openBudget{}
	if _, err := p.Poll(context.Background()); err != nil || len(n.got) != 1 {
		t.Fatalf("after recovery: err=%v delivered=%d", err, len(n.got))
	}
}

// Ein echter Write-Fehler nach dem Claim bleibt die dokumentierte
// at-most-once-Grenze: Fehler sichtbar, Nachricht nicht erneut zugestellt.
func TestWriteFailureAfterClaimIsTheDocumentedAtMostOnceLimit(t *testing.T) {
	f := newFakeServer(directRoom)
	f.msgs[directRoom.Key] = []store.CoordMessage{{ID: 9, SenderExternalID: "peer", Body: "x"}}
	n := &fakeNotifier{f: f, fail: errors.New("write failed")}
	p := newPoller(f, n)
	if _, err := p.Poll(context.Background()); err == nil {
		t.Fatal("write failure must surface")
	}
	n.fail = nil
	_, _ = p.Poll(context.Background())
	if len(n.got) != 0 || f.cursor[directRoom.Key] != 0 {
		t.Fatal("a claimed message is not delivered again and the cursor does not move")
	}
}

func TestDefaultBudgetTruncatesAndStopsClaiming(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	f := newFakeServer(directRoom)
	f.msgs[directRoom.Key] = []store.CoordMessage{
		{ID: 1, SenderExternalID: "peer", Body: strings.Repeat("x", 20000)},
		{ID: 2, SenderExternalID: "peer", Body: "later"},
	}
	n := &fakeNotifier{f: f}
	p := newPoller(f, n)
	p.Budget = nil
	if _, err := p.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(n.got) != 1 || !strings.Contains(n.got[0].Content, "rate-limited") {
		t.Fatalf("want one truncated notification, got %d", len(n.got))
	}
	if f.claimed[2] {
		t.Fatal("message 2 must stay unclaimed while the budget is exhausted")
	}
}

type sleepRecorder struct {
	d      []time.Duration
	cancel context.CancelFunc
	stop   int
	hook   func(call int)
}

func (s *sleepRecorder) Sleep(ctx context.Context, d time.Duration) error {
	s.d = append(s.d, d)
	if s.hook != nil {
		s.hook(len(s.d))
	}
	if len(s.d) >= s.stop {
		s.cancel()
		return ctx.Err()
	}
	return nil
}

func TestBackoffGrowsWhenIdleAndResetsOnActivity(t *testing.T) {
	f := newFakeServer(directRoom)
	n := &fakeNotifier{f: f}
	ctx, cancel := context.WithCancel(context.Background())
	rec := &sleepRecorder{cancel: cancel, stop: 8}
	// Nach dem vierten Schlaf kommt eine Nachricht.
	rec.hook = func(call int) {
		if call == 4 {
			f.mu.Lock()
			f.msgs[directRoom.Key] = []store.CoordMessage{{ID: 1, SenderExternalID: "peer", Body: "x"}}
			f.mu.Unlock()
		}
	}
	p := newPoller(f, n)
	p.Sleep = rec.Sleep
	if err := p.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	s := time.Second
	want := []time.Duration{2 * s, 4 * s, 8 * s, 10 * s, 2 * s, 2 * s, 4 * s, 8 * s}
	if !reflect.DeepEqual(rec.d, want) {
		t.Fatalf("waits = %v, want %v", rec.d, want)
	}
	if len(n.got) != 1 {
		t.Fatalf("delivered = %d", len(n.got))
	}
}

func TestRunReportsErrorsAndKeepsPolling(t *testing.T) {
	f := newFakeServer(directRoom)
	f.msgs[directRoom.Key] = []store.CoordMessage{{ID: 1, SenderExternalID: "peer", Body: "x"}}
	n := &fakeNotifier{f: f, fail: errors.New("down")}
	ctx, cancel := context.WithCancel(context.Background())
	rec := &sleepRecorder{cancel: cancel, stop: 2}
	var errs []error
	p := newPoller(f, n)
	p.Sleep = rec.Sleep
	p.OnError = func(err error) { errs = append(errs, err) }
	_ = p.Run(ctx)
	if len(errs) == 0 || len(rec.d) != 2 {
		t.Fatalf("errs=%v sleeps=%v", errs, rec.d)
	}
}
