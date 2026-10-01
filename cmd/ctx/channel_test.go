package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// TestChannelChild ist der Kindprozess: er läuft als `ctx channel` und spricht
// MCP über die geerbten Pipes. Ohne die Umgebungsvariable ist er ein No-op.
func TestChannelChild(t *testing.T) {
	if os.Getenv("GHOSTTREE_TEST_CHANNEL_CHILD") != "1" {
		t.Skip("subprocess helper")
	}
	os.Exit(cmdChannel(nil, os.Stderr))
}

func TestChannelCapabilitiesFlag(t *testing.T) {
	var out strings.Builder
	if code := cmdChannel([]string{"--capabilities"}, &out); code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"can   wake_idle_session", "can   receive_at_safe_point", "lacks human_steer", "OptInAtStartOnly"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("capabilities output lacks %q:\n%s", want, out.String())
		}
	}
}

type channelEnv struct {
	url, token string
	st         *store.Store
	cfgHome    string
	a, b       *client.Client // a sendet, b ist die Claude-Session
}

const channelSelf = "sess-claude"

func newChannelEnv(t *testing.T) *channelEnv {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("robin")
	srv := httptest.NewServer(server.New(st))
	t.Cleanup(srv.Close)
	home := t.TempDir()
	cfgDir := filepath.Join(home, "ghosttree")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{ServerURL: srv.URL, Token: token, Machine: "chanbox"}
	raw, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	c := client.New(cfg)
	room := store.RoomKeyForMachine("chanbox")
	for _, ref := range []string{"sess-sender", channelSelf} {
		if _, err := c.RegisterCoordAgent(store.CoordAgent{ExternalID: ref, Provider: "test", RoomKey: room, DisplayName: ref}); err != nil {
			t.Fatal(err)
		}
	}
	return &channelEnv{url: srv.URL, token: token, st: st, cfgHome: home, a: c, b: c}
}

func (e *channelEnv) sendDM(t *testing.T, body, clientID string) (key string, id int64) {
	t.Helper()
	members := []string{"sess-sender", channelSelf}
	key = store.RoomKeyForDirect(members)
	if err := e.a.EnsureCoordRoom(store.CoordRoom{Key: key, Kind: store.RoomDirect, Members: members}, "sess-sender"); err != nil {
		t.Fatal(err)
	}
	id, err := e.a.SendCoordMessage(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: key, SenderExternalID: "sess-sender",
		ClientID: clientID, OriginEventID: "ev-" + clientID, Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	return key, id
}

// channelProc ist ein laufendes `ctx channel` mit MCP-Gegenstelle.
type channelProc struct {
	t      *testing.T
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lines  chan map[string]any
	nextID int
	mu     sync.Mutex
}

func startChannel(t *testing.T, e *channelEnv) *channelProc {
	t.Helper()
	return startChannelAs(t, e, channelSelf)
}

// startChannelAs startet ein `ctx channel` mit eigener Identität.
func startChannelAs(t *testing.T, e *channelEnv, self string) *channelProc {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestChannelChild$")
	cmd.Dir = t.TempDir() // kein Repo: nur der Maschinenraum
	cmd.Env = append(os.Environ(), "GHOSTTREE_TEST_CHANNEL_CHILD=1", "XDG_CONFIG_HOME="+e.cfgHome,
		"CLAUDE_CODE_SESSION_ID="+self, "CODEX_SESSION_ID=", "CODEX_THREAD_ID=", "OPENCODE_SESSION_ID=",
		"XDG_STATE_HOME="+t.TempDir())
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &channelProc{t: t, cmd: cmd, stdin: stdin, lines: make(chan map[string]any, 64)}
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var m map[string]any
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				p.lines <- m
			}
		}
		close(p.lines)
	}()
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return p
}

func (p *channelProc) send(msg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, err := io.WriteString(p.stdin, msg+"\n"); err != nil {
		p.t.Fatal(err)
	}
}

func (p *channelProc) next(timeout time.Duration) (map[string]any, bool) {
	select {
	case m, ok := <-p.lines:
		return m, ok
	case <-time.After(timeout):
		return nil, false
	}
}

func (p *channelProc) handshake() map[string]any {
	p.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	resp, ok := p.next(10 * time.Second)
	if !ok || resp["id"] == nil {
		p.t.Fatalf("no initialize response: %v", resp)
	}
	p.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	return resp
}

// notifications sammelt Channel-Notifications für d und gibt sie zurück.
func (p *channelProc) notifications(d time.Duration, want int) []map[string]any {
	var out []map[string]any
	deadline := time.After(d)
	for {
		select {
		case m, ok := <-p.lines:
			if !ok {
				return out
			}
			if m["method"] == "notifications/claude/channel" {
				out = append(out, m)
				if len(out) >= want && want > 0 {
					// Danach noch kurz lauschen, ob mehr als erwartet kommt.
					deadline = time.After(1500 * time.Millisecond)
					want = 0
				}
			}
		case <-deadline:
			return out
		}
	}
}

func channelUsedState(t *testing.T, e *channelEnv, id int64) []int64 {
	t.Helper()
	got, err := e.a.CoordInjectedMessages(channelSelf, []int64{id})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestChannelSubprocessDeliversExactlyOnceAndRepliesAcked(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test")
	}
	e := newChannelEnv(t)
	// Diese Nachricht liegt VOR dem Handshake und darf nicht verloren gehen.
	key, id := e.sendDM(t, "bitte pruefen", "one")

	p := newChannelProcWithCheck(t, e)
	notes := p.notifications(20*time.Second, 1)
	if len(notes) != 1 {
		t.Fatalf("want exactly one notification, got %d: %v", len(notes), notes)
	}
	params := notes[0]["params"].(map[string]any)
	if _, hasID := notes[0]["id"]; hasID {
		t.Fatal("notification must not carry an id")
	}
	meta := params["meta"].(map[string]any)
	if params["content"] != "bitte pruefen" || meta["message_id"] != strconv.FormatInt(id, 10) ||
		meta["origin_event_id"] != "ev-one" || meta["room"] != key || meta["room_kind"] != "direct" || meta["sender"] != "sess-sender" {
		t.Fatalf("notification = %v", notes[0])
	}
	if got := channelUsedState(t, e, id); len(got) != 1 {
		t.Fatalf("message must be marked injected, got %v", got)
	}

	// reply: Antwort mit reply_to und causation_id, Zustellung acked.
	p.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"reply","arguments":{"message_id":%q,"text":"erledigt"}}}`, strconv.FormatInt(id, 10)))
	var result map[string]any
	for {
		m, ok := p.next(10 * time.Second)
		if !ok {
			t.Fatal("no reply tool result")
		}
		if fmt.Sprint(m["id"]) == "7" {
			result = m
			break
		}
	}
	if result["error"] != nil || result["result"].(map[string]any)["isError"] == true {
		t.Fatalf("reply failed: %v", result)
	}
	msgs, err := e.a.CoordInbox(store.DestinationRoom, key, "sess-sender", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var reply *store.CoordMessage
	for i := range msgs {
		if msgs[i].Body == "erledigt" {
			reply = &msgs[i]
		}
	}
	if reply == nil || reply.ReplyTo != id || reply.SenderExternalID != channelSelf || reply.CausationID != "ev-one" {
		t.Fatalf("reply message = %+v", reply)
	}
	state := deliveryState(t, e, id)
	if state != store.DeliveryAcked {
		t.Fatalf("delivery state = %q, want acked", state)
	}

	// Neustart: keine zweite Notification für dieselbe Nachricht.
	_ = p.stdin.Close()
	_ = p.cmd.Wait()
	p2 := startChannel(t, e)
	p2.handshake()
	if again := p2.notifications(4*time.Second, 0); len(again) != 0 {
		t.Fatalf("restart redelivered: %v", again)
	}
	// Eine neue Nachricht kommt dagegen an, genau einmal.
	_, id2 := e.sendDM(t, "zweite", "two")
	got := p2.notifications(20*time.Second, 1)
	if len(got) != 1 || got[0]["params"].(map[string]any)["meta"].(map[string]any)["message_id"] != strconv.FormatInt(id2, 10) {
		t.Fatalf("second message: %v", got)
	}
}

// newChannelProcWithCheck startet den Prozess, prüft die deklarierte
// Fähigkeit und die Server-Instruktion und schließt den Handshake erst danach
// ab, damit die Nachricht davor schon lag.
func newChannelProcWithCheck(t *testing.T, e *channelEnv) *channelProc {
	p := startChannel(t, e)
	time.Sleep(500 * time.Millisecond) // die Nachricht liegt, der Handshake fehlt noch
	resp := p.handshake()
	result := resp["result"].(map[string]any)
	caps := result["capabilities"].(map[string]any)
	if _, ok := caps["experimental"].(map[string]any)["claude/channel"]; !ok {
		t.Fatalf("claude/channel capability missing: %v", caps)
	}
	if info := result["serverInfo"].(map[string]any); info["name"] != "ghosttree-channel" {
		t.Fatalf("server name = %v", info["name"])
	}
	if ins, _ := result["instructions"].(string); !strings.Contains(ins, "reply") || !strings.Contains(ins, "send tool") || !strings.Contains(ins, "human_steer") {
		t.Fatalf("instructions must name reply and the capability gaps: %q", ins)
	}
	return p
}

// TestChannelRejectsServerDiscoverAndStillWorks: Claude Code probes with
// server/discover before initialize. The channel server must refuse it, because
// a modern connection carries no channel, and then serve the legacy handshake.
func TestChannelRejectsServerDiscoverAndStillWorks(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test")
	}
	e := newChannelEnv(t)
	p := startChannel(t, e)
	p.send(`{"jsonrpc":"2.0","id":"server-discover-probe-1","method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`)
	resp, ok := p.next(10 * time.Second)
	if !ok {
		t.Fatal("no answer to server/discover")
	}
	rejErr, _ := resp["error"].(map[string]any)
	if resp["id"] != "server-discover-probe-1" || rejErr == nil || rejErr["code"] != float64(-32601) || resp["result"] != nil {
		t.Fatalf("server/discover must be refused with -32601: %v", resp)
	}
	p.handshake()
	_, id := e.sendDM(t, "nach discover", "disc")
	notes := p.notifications(20*time.Second, 1)
	if len(notes) != 1 || notes[0]["params"].(map[string]any)["meta"].(map[string]any)["message_id"] != strconv.FormatInt(id, 10) {
		t.Fatalf("channel must still deliver after a refused discover: %v", notes)
	}
}

func deliveryState(t *testing.T, e *channelEnv, id int64) string {
	t.Helper()
	var state string
	err := e.st.DB().QueryRowContext(context.Background(),
		`SELECT state FROM coord_deliveries WHERE message_id=? AND recipient_external_id=?`, id, channelSelf).Scan(&state)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestChannelStopsCleanlyOnSIGTERMAndOnStdinEOF(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test")
	}
	for _, how := range []string{"sigterm", "stdin"} {
		e := newChannelEnv(t)
		p := startChannel(t, e)
		p.handshake()
		if how == "sigterm" {
			_ = p.cmd.Process.Signal(syscall.SIGTERM)
		} else {
			_ = p.stdin.Close()
		}
		done := make(chan error, 1)
		go func() { done <- p.cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s: exit = %v, want a clean exit", how, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: channel did not stop", how)
		}
	}
}

func newTools(c *client.Client) *channelTools {
	return &channelTools{client: c, self: channelSelf, rec: &recorder{seen: map[int64]originInfo{}}}
}

func repliesTo(t *testing.T, e *channelEnv, room string, id int64) []store.CoordMessage {
	t.Helper()
	msgs, err := e.a.CoordInbox(store.DestinationRoom, room, "sess-sender", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.CoordMessage
	for _, m := range msgs {
		if m.ReplyTo == id {
			out = append(out, m)
		}
	}
	return out
}

// Nach einem Neustart kennt der Prozess die Nachricht nicht mehr. Mention und
// causation_id kommen dann vom Server.
func TestChannelReplyAfterRestartReloadsTheOrigin(t *testing.T) {
	e := newChannelEnv(t)
	room := store.RoomKeyForMachine("chanbox")
	id, err := e.a.SendCoordMessage(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "sess-sender",
		ClientID: "o1", OriginEventID: "ev-o1", Body: "frage", Mentions: []string{channelSelf},
	})
	if err != nil {
		t.Fatal(err)
	}
	tools := newTools(e.b) // frischer Prozess: nichts im Speicher
	if _, _, err := tools.handleReply(context.Background(), nil, channelReplyInput{MessageID: strconv.FormatInt(id, 10), Text: "antwort", Room: room}); err != nil {
		t.Fatal(err)
	}
	got := repliesTo(t, e, room, id)
	if len(got) != 1 || got[0].CausationID != "ev-o1" {
		t.Fatalf("reply = %+v", got)
	}
	mentions, err := e.a.CoordMessageMentions(got[0].ID, "sess-sender")
	if err != nil || len(mentions) != 1 || mentions[0] != "sess-sender" {
		t.Fatalf("mentions = %v err=%v", mentions, err)
	}
	if st := deliveryState(t, e, id); st != store.DeliveryAcked {
		t.Fatalf("state = %q", st)
	}
}

// Scheitert nur das Acken, liefert ein Retry von reply dieselbe Nachricht und
// holt das Acken nach, statt eine zweite Antwort zu erzeugen.
func TestChannelReplyRetryAfterFailedAckDoesNotDuplicate(t *testing.T) {
	e := newChannelEnv(t)
	key, id := e.sendDM(t, "bitte", "r1")
	var mu sync.Mutex
	failed := false
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fail := !failed && r.URL.Path == "/api/coord/deliveries"
		if fail {
			failed = true
		}
		mu.Unlock()
		if fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		target, _ := url.Parse(e.url)
		httputil.NewSingleHostReverseProxy(target).ServeHTTP(w, r)
	}))
	defer proxy.Close()
	tools := newTools(client.New(config.Config{ServerURL: proxy.URL, Token: e.token, Machine: "chanbox"}))
	tools.rec.seen[id] = originInfo{room: key, kind: "direct", sender: "sess-sender", originEventID: "ev-r1"}
	in := channelReplyInput{MessageID: strconv.FormatInt(id, 10), Text: "fertig"}
	if _, _, err := tools.handleReply(context.Background(), nil, in); err == nil {
		t.Fatal("the failed ack must surface")
	}
	if _, _, err := tools.handleReply(context.Background(), nil, in); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := repliesTo(t, e, key, id); len(got) != 1 {
		t.Fatalf("want exactly one reply after a retry, got %d", len(got))
	}
	if st := deliveryState(t, e, id); st != store.DeliveryAcked {
		t.Fatalf("state = %q, want acked after the retry", st)
	}
}

// Zwei verschiedene Antworten auf dieselbe Nachricht sind zwei Nachrichten:
// "ich schaue" und das Ergebnis danach dürfen nicht zusammenfallen.
func TestChannelDistinctRepliesStayDistinct(t *testing.T) {
	e := newChannelEnv(t)
	key, id := e.sendDM(t, "bitte", "d1")
	tools := newTools(e.b)
	tools.rec.seen[id] = originInfo{room: key, kind: "direct", sender: "sess-sender", originEventID: "ev-d1"}
	for _, text := range []string{"ich schaue es mir an", "Ergebnis: alles gruen", "Ergebnis: alles gruen"} {
		if _, _, err := tools.handleReply(context.Background(), nil, channelReplyInput{MessageID: strconv.FormatInt(id, 10), Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	got := repliesTo(t, e, key, id)
	if len(got) != 2 || got[0].Body != "ich schaue es mir an" || got[1].Body != "Ergebnis: alles gruen" {
		t.Fatalf("want two distinct replies (identical text deduplicated), got %+v", got)
	}
}

// Scheitert das Nachladen der Ursprungsnachricht, bricht reply ab, statt ohne
// Mention zu senden.
func TestChannelReplyAbortsWhenTheOriginCannotBeLoaded(t *testing.T) {
	e := newChannelEnv(t)
	room := store.RoomKeyForMachine("chanbox")
	id, err := e.a.SendCoordMessage(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room,
		SenderExternalID: "sess-sender", ClientID: "x1", Body: "frage"})
	if err != nil {
		t.Fatal(err)
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer down.Close()
	idStr := strconv.FormatInt(id, 10)
	broken := newTools(client.New(config.Config{ServerURL: down.URL, Token: e.token, Machine: "chanbox"}))
	if _, _, err := broken.handleReply(context.Background(), nil, channelReplyInput{MessageID: idStr, Text: "a", Room: room}); err == nil ||
		!strings.Contains(err.Error(), "cannot load message") {
		t.Fatalf("err = %v", err)
	}
	// Ein falscher Raum oder eine falsche ID ist ein klarer Fehler, keine stille Antwort.
	ok := newTools(e.b)
	if _, _, err := ok.handleReply(context.Background(), nil, channelReplyInput{MessageID: "9999", Text: "a", Room: room}); err == nil ||
		!strings.Contains(err.Error(), "is not in room") {
		t.Fatalf("err = %v", err)
	}
	if got := repliesTo(t, e, room, id); len(got) != 0 {
		t.Fatalf("nothing may be sent: %+v", got)
	}
}

// callTool ruft ein Tool des Channel-Prozesses und gibt Ergebnistext und
// isError zurück. Dazwischenliegende Notifications werden verworfen.
func (p *channelProc) callTool(id int, name, args string) (string, bool) {
	p.t.Helper()
	p.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, id, name, args))
	for {
		m, ok := p.next(10 * time.Second)
		if !ok {
			p.t.Fatalf("no result for tool %s", name)
		}
		if fmt.Sprint(m["id"]) != strconv.Itoa(id) {
			continue
		}
		if m["error"] != nil {
			return fmt.Sprint(m["error"]), true
		}
		res := m["result"].(map[string]any)
		text := ""
		if content, _ := res["content"].([]any); len(content) > 0 {
			text, _ = content[0].(map[string]any)["text"].(string)
		}
		isErr, _ := res["isError"].(bool)
		return text, isErr
	}
}

// Ein Channel-Agent beginnt mit send ein Gespräch: die Nachricht trägt Mention
// und Intent, der erwähnte Channel-Agent wird geweckt, der Absender nicht. Die
// Antwort des anderen weckt dann den Fragenden.
func TestChannelSendStartsAConversationAndWakesTheMentionedAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test")
	}
	e := newChannelEnv(t)
	const other = "sess-other"
	room := store.RoomKeyForMachine("chanbox")
	if _, err := e.a.RegisterCoordAgent(store.CoordAgent{ExternalID: other, Provider: "test", RoomKey: room, DisplayName: other}); err != nil {
		t.Fatal(err)
	}
	asker := startChannelAs(t, e, channelSelf)
	asker.handshake()
	answerer := startChannelAs(t, e, other)
	answerer.handshake()

	text, isErr := asker.callTool(10, "send", fmt.Sprintf(`{"text":"kannst du das pruefen?","mention":[%q],"intent":"question","room":"machine"}`, other))
	if isErr || !strings.Contains(text, "stored as message") {
		t.Fatalf("send = %q isError=%v", text, isErr)
	}
	notes := answerer.notifications(20*time.Second, 1)
	if len(notes) != 1 {
		t.Fatalf("the mentioned agent must be woken once, got %v", notes)
	}
	params := notes[0]["params"].(map[string]any)
	meta := params["meta"].(map[string]any)
	if params["content"] != "kannst du das pruefen?" || meta["sender"] != channelSelf || meta["room"] != room {
		t.Fatalf("notification = %v", notes[0])
	}
	if own := asker.notifications(1500*time.Millisecond, 0); len(own) != 0 {
		t.Fatalf("the sender must not wake itself: %v", own)
	}
	msgs, err := e.a.CoordInbox(store.DestinationRoom, room, "sess-sender", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var sent *store.CoordMessage
	for i := range msgs {
		if msgs[i].Body == "kannst du das pruefen?" {
			sent = &msgs[i]
		}
	}
	if sent == nil || sent.Intent != store.IntentQuestion || sent.SenderExternalID != channelSelf {
		t.Fatalf("stored message = %+v", sent)
	}
	if got, err := e.a.CoordMessageMentions(sent.ID, "sess-sender"); err != nil || len(got) != 1 || got[0] != other {
		t.Fatalf("mentions = %v err=%v", got, err)
	}

	// Die Antwort weckt den Fragenden.
	text, isErr = answerer.callTool(11, "reply", fmt.Sprintf(`{"message_id":%q,"text":"ja, erledigt"}`, strconv.FormatInt(sent.ID, 10)))
	if isErr {
		t.Fatalf("reply = %q", text)
	}
	back := asker.notifications(20*time.Second, 1)
	if len(back) != 1 || back[0]["params"].(map[string]any)["content"] != "ja, erledigt" {
		t.Fatalf("the asker must be woken by the answer: %v", back)
	}
}

func TestChannelSendValidatesItsInput(t *testing.T) {
	e := newChannelEnv(t)
	tools := newTools(e.b)
	tools.machineRoom = store.RoomKeyForMachine("chanbox")
	for _, c := range []struct {
		name string
		in   channelSendInput
		want string
	}{
		{"empty text", channelSendInput{Text: "  ", Room: "machine"}, "text is required"},
		{"no project room", channelSendInput{Text: "x"}, "not bound to a repository"},
		{"unknown room", channelSendInput{Text: "x", Room: "lobby"}, "unknown room"},
		{"unknown intent", channelSendInput{Text: "x", Room: "machine", Intent: "shout", Mention: []string{"sess-sender"}}, "unknown intent"},
		{"standing is not allowed", channelSendInput{Text: "x", Room: "machine", Intent: "standing", Mention: []string{"sess-sender"}}, "unknown intent"},
		{"question without mention", channelSendInput{Text: "x", Room: "machine", Intent: "question"}, "needs a mention"},
		{"empty mention", channelSendInput{Text: "x", Room: "machine", Mention: []string{" "}}, "empty agent id"},
		{"self mention", channelSendInput{Text: "x", Room: "machine", Mention: []string{channelSelf}}, "mention yourself"},
		{"mention of a stranger", channelSendInput{Text: "x", Room: "machine", Mention: []string{"nobody"}}, ""},
	} {
		_, _, err := tools.handleSend(context.Background(), nil, c.in)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want containing %q", c.name, err, c.want)
		}
	}
}

// Wiederholt das Modell send mit denselben Argumenten, entsteht keine zweite
// Nachricht; ein anderer Text oder eine andere Mention ist eine neue.
func TestChannelSendRetryDoesNotDuplicate(t *testing.T) {
	e := newChannelEnv(t)
	tools := newTools(e.b)
	room := store.RoomKeyForMachine("chanbox")
	tools.machineRoom = room
	count := func() int {
		msgs, err := e.a.CoordInbox(store.DestinationRoom, room, "sess-sender", 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, m := range msgs {
			if m.SenderExternalID == channelSelf {
				n++
			}
		}
		return n
	}
	send := func(in channelSendInput) {
		t.Helper()
		in.Room = "machine"
		if _, _, err := tools.handleSend(context.Background(), nil, in); err != nil {
			t.Fatal(err)
		}
	}
	send(channelSendInput{Text: "hallo", Mention: []string{"sess-sender"}})
	send(channelSendInput{Text: "hallo", Mention: []string{"sess-sender"}})
	if n := count(); n != 1 {
		t.Fatalf("retry produced %d messages, want 1", n)
	}
	send(channelSendInput{Text: "hallo anders", Mention: []string{"sess-sender"}})
	send(channelSendInput{Text: "hallo"})
	if n := count(); n != 3 {
		t.Fatalf("distinct sends produced %d messages, want 3", n)
	}
}
