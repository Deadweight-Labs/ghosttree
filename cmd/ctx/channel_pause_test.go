package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/agentpause"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type fakePauseSource struct {
	control *store.AgentControl
	err     error
	events  []store.ControlEvent
	ids     []int64
	evErr   error
}

func (f *fakePauseSource) ActiveControl(string) (*store.AgentControl, error) { return f.control, f.err }
func (f *fakePauseSource) RecordEvent(id int64, ev store.ControlEvent) (bool, error) {
	if f.evErr != nil {
		return false, f.evErr
	}
	f.ids = append(f.ids, id)
	f.events = append(f.events, ev)
	return true, nil
}

func TestChannelPauseSyncWritesAndRemovesTheFlag(t *testing.T) {
	pauseEnv(t)
	src := &fakePauseSource{}
	p := &pauseSyncer{agent: pauseTestAgent, src: src}
	if err := p.sync(); err != nil {
		t.Fatal(err)
	}
	if _, set := agentpause.ReadFlag(pauseTestAgent); set {
		t.Fatal("no control: no flag")
	}
	src.control = &store.AgentControl{ID: 4, Action: store.ControlPause, RequestedByLabel: "robin", Reason: "why"}
	if err := p.sync(); err != nil {
		t.Fatal(err)
	}
	f, set := agentpause.ReadFlag(pauseTestAgent)
	if !set || f.ControlID != 4 || f.By != "robin" || f.Action != "pause" || f.Reason != "why" {
		t.Fatalf("flag = %+v %v", f, set)
	}
	src.control = nil // resumed
	if err := p.sync(); err != nil {
		t.Fatal(err)
	}
	if _, set := agentpause.ReadFlag(pauseTestAgent); set {
		t.Fatal("resume must remove the flag")
	}
}

func TestChannelPauseSyncKeepsTheFlagWhenTheServerIsUnreachable(t *testing.T) {
	pauseEnv(t)
	src := &fakePauseSource{control: &store.AgentControl{ID: 1, Action: store.ControlPause}}
	p := &pauseSyncer{agent: pauseTestAgent, src: src}
	if err := p.sync(); err != nil {
		t.Fatal(err)
	}
	src.control, src.err = nil, errors.New("connection refused")
	if err := p.sync(); err == nil {
		t.Fatal("the error must surface")
	}
	if _, set := agentpause.ReadFlag(pauseTestAgent); !set {
		t.Fatal("an unreachable server must not silently lift a pause")
	}
	// Ende der Session: das Flag verschwindet mit dem Channel.
	p.close()
	if _, set := agentpause.ReadFlag(pauseTestAgent); set {
		t.Fatal("close must remove the flag")
	}
}

func TestChannelPauseSyncReportsHookAcksOnce(t *testing.T) {
	pauseEnv(t)
	src := &fakePauseSource{control: &store.AgentControl{ID: 8, Action: store.ControlPause}}
	p := &pauseSyncer{agent: pauseTestAgent, src: src}
	if err := p.sync(); err != nil {
		t.Fatal(err)
	}
	// Der Hook blockiert einen Aufruf aus einem Subagent.
	var sink discard
	agentpause.Gate(pauseTestAgent, stringsReader(`{"session_id":"s","tool_name":"Edit","tool_use_id":"toolu_7","agent_id":"sub1"}`), &sink)
	if err := p.sync(); err != nil {
		t.Fatal(err)
	}
	if len(src.events) != 1 {
		t.Fatalf("events = %+v", src.events)
	}
	ev := src.events[0]
	if src.ids[0] != 8 || ev.Kind != store.ControlEventAck || ev.ToolUseID != "toolu_7" || ev.AgentID != "sub1" || ev.ToolName != "Edit" {
		t.Fatalf("ack = %d %+v", src.ids[0], ev)
	}
	if err := p.sync(); err != nil || len(src.events) != 1 {
		t.Fatalf("an ack must be reported once: %v %d", err, len(src.events))
	}
	// Scheitert die Meldung, bleibt der Ack liegen und wird wiederholt.
	agentpause.Gate(pauseTestAgent, stringsReader(`{"tool_use_id":"toolu_8"}`), &sink)
	src.evErr = errors.New("503")
	if err := p.sync(); err == nil {
		t.Fatal("report failure must surface")
	}
	src.evErr = nil
	if err := p.sync(); err != nil || len(src.events) != 2 || src.events[1].ToolUseID != "toolu_8" {
		t.Fatalf("retry: %v %+v", err, src.events)
	}
	// Nach dem Fortsetzen: restliche Acks gemeldet, Datei aufgeraeumt.
	src.control = nil
	if err := p.sync(); err != nil {
		t.Fatal(err)
	}
	if acks, _ := agentpause.ReadAcks(pauseTestAgent, 0); len(acks) != 0 {
		t.Fatalf("acks must be cleared after resume: %+v", acks)
	}
}

type discard struct{}

func (*discard) Write(b []byte) (int, error) { return len(b), nil }

func stringsReader(s string) *strings.Reader { return strings.NewReader(s) }
