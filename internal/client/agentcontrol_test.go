package client

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestAgentControlRoundtrip(t *testing.T) {
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("alice")
	srv := httptest.NewServer(server.New(st))
	t.Cleanup(srv.Close)
	c := New(config.Config{ServerURL: srv.URL, Token: token, Machine: "ws"})
	const agent = "claude:ws:1"
	if _, err := c.RegisterCoordAgent(store.CoordAgent{ExternalID: agent, Provider: "claude", RoomKey: "machine:ws"}); err != nil {
		t.Fatal(err)
	}
	if got, err := c.AgentControl(agent); err != nil || got != nil {
		t.Fatalf("idle = %+v %v", got, err)
	}
	made, err := st.RequestAgentControl(store.Principal{ID: "person:1", Label: "alice", TokenKind: store.WebSessionKind}, agent, store.ControlPause, "r", "web")
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.AgentControl(agent)
	if err != nil || got == nil || got.ID != made.ID || got.State != store.ControlRequested {
		t.Fatalf("active = %+v %v", got, err)
	}
	if ok, err := c.RecordControlEvent(made.ID, store.ControlEvent{Kind: store.ControlEventAck, ToolUseID: "t1"}); err != nil || !ok {
		t.Fatalf("ack: %v %v", ok, err)
	}
	if err := c.RecordControlProof(made.ID, store.ControlEvent{Kind: store.ControlEventProof, ToolUseID: "t1"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.AgentControl(agent); got.State != store.ControlEffective {
		t.Fatalf("after ack and proof = %+v", got)
	}
	// Nach dem Fortsetzen nennt der Server den aufgehobenen Vorgang samt dem,
	// der ihn aufgehoben hat; der Channel braucht das für die Meldung an die Session.
	if _, err := st.ResumeAgentControl(store.Principal{ID: "person:1", Label: "alice", TokenKind: store.WebSessionKind}, agent); err != nil {
		t.Fatal(err)
	}
	active, resumed, err := c.AgentControlState(agent)
	if err != nil || active != nil || resumed == nil || resumed.ID != made.ID || resumed.ResumedByLabel != "alice" {
		t.Fatalf("after resume = %+v %+v %v", active, resumed, err)
	}
	// Ein unbekannter Vorgang ist kein Grund, den Stapel ewig zu wiederholen.
	if err := c.RecordControlProof(9999, store.ControlEvent{Kind: store.ControlEventProof, ToolUseID: "x"}); err != nil {
		t.Fatalf("unknown control must be swallowed: %v", err)
	}
}

func TestRecordControlProofRetriesServerErrors(t *testing.T) {
	for code, wantErr := range map[int]bool{400: false, 404: false, 401: true, 429: true, 500: true, 503: true} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte("nope"))
		}))
		c := New(config.Config{ServerURL: srv.URL, Token: "x"})
		err := c.RecordControlProof(1, store.ControlEvent{Kind: store.ControlEventProof, ToolUseID: "t"})
		srv.Close()
		if (err != nil) != wantErr {
			t.Errorf("status %d: err=%v, want error=%v", code, err, wantErr)
		}
	}
}
