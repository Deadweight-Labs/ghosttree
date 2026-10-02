package main

import (
	"context"
	"errors"
	"github.com/Deadweight-Labs/ghosttree/internal/claudechannel"
	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/agentpause"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type fakePauseSource struct {
	control *store.AgentControl
	resumed *store.AgentControl
	err     error
	events  []store.ControlEvent
	ids     []int64
	evErr   error
}

func (f *fakePauseSource) ControlState(string) (*store.AgentControl, *store.AgentControl, error) {
	return f.control, f.resumed, f.err
}
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

func TestChannelPauseSyncDoesNotWedgeOnUnreportableAcks(t *testing.T) {
	pauseEnv(t)
	src := &fakePauseSource{control: &store.AgentControl{ID: 2, Action: store.ControlPause}}
	p := &pauseSyncer{agent: pauseTestAgent, src: src}
	_ = p.sync()
	var sink discard
	// A damaged flag.json: the hook acks with control_id 0.
	dir, _ := agentpause.Dir(pauseTestAgent)
	_ = os.WriteFile(filepath.Join(dir, "flag.json"), []byte("{broken"), 0o600)
	agentpause.Gate(pauseTestAgent, stringsReader(`{"tool_use_id":"toolu_zero"}`), &sink)
	_ = agentpause.WriteFlag(pauseTestAgent, agentpause.Flag{ControlID: 2})
	agentpause.Gate(pauseTestAgent, stringsReader(`{"tool_use_id":"toolu_good"}`), &sink)
	if err := p.sync(); err != nil {
		t.Fatalf("a control_id 0 ack must be skipped, got %v", err)
	}
	if len(src.events) != 1 || src.events[0].ToolUseID != "toolu_good" {
		t.Fatalf("events = %+v", src.events)
	}
	// A permanent rejection (400) advances past the line.
	agentpause.Gate(pauseTestAgent, stringsReader(`{"tool_use_id":"toolu_bad"}`), &sink)
	src.evErr = &client.StatusError{Method: "POST", Path: "/x", Status: 400}
	if err := p.sync(); err != nil {
		t.Fatalf("a 400 must not wedge the offset: %v", err)
	}
	src.evErr = nil
	if err := p.sync(); err != nil || len(src.events) != 1 {
		t.Fatalf("the rejected line must not be retried: %v %+v", err, src.events)
	}
	// Resume clears the acks even when reporting fails.
	agentpause.Gate(pauseTestAgent, stringsReader(`{"tool_use_id":"toolu_late"}`), &sink)
	src.control, src.evErr = nil, errors.New("503")
	if err := p.sync(); err == nil {
		t.Fatal("transient error must surface")
	}
	if acks, _ := agentpause.ReadAcks(pauseTestAgent, 0); len(acks) != 0 {
		t.Fatalf("acks must be cleared on resume: %+v", acks)
	}
}

type fakeNotifier struct {
	ready bool
	got   []claudechannel.Notification
	err   error
}

func (f *fakeNotifier) Ready() bool { return f.ready }
func (f *fakeNotifier) Notify(_ context.Context, n claudechannel.Notification) error {
	if f.err != nil {
		return f.err
	}
	f.got = append(f.got, n)
	return nil
}

func resumeFixture(t *testing.T) (*fakePauseSource, *fakeNotifier, func() *pauseSyncer) {
	t.Helper()
	pauseEnv(t)
	src := &fakePauseSource{}
	n := &fakeNotifier{ready: true}
	return src, n, func() *pauseSyncer {
		return &pauseSyncer{agent: pauseTestAgent, src: src, notifier: n}
	}
}

func resumeOf(id int64, by string) *store.AgentControl {
	return &store.AgentControl{ID: id, Action: store.ControlPause, ResumedBy: "person:1", ResumedByLabel: by, ResumedAt: "2026-10-02T10:00:00Z"}
}

// Die Session hat zuletzt die stopReason des Hooks gesehen und hielte sich
// ohne Meldung für weiter pausiert. Die Aufhebung kommt deshalb als Channel-
// Meldung, genau einmal je Aufhebung, und nennt die Aufgabe als fortgesetzt.
func TestChannelPauseResumeNotifiesTheSessionOnce(t *testing.T) {
	src, n, mk := resumeFixture(t)
	p := mk()
	src.control = &store.AgentControl{ID: 7, Action: store.ControlPause, RequestedByLabel: "robin"}
	if err := p.sync(); err != nil {
		t.Fatal(err)
	}
	if len(n.got) != 0 {
		t.Fatalf("a pause alone must not notify: %+v", n.got)
	}
	src.control, src.resumed = nil, resumeOf(7, "Robin")
	for i := 0; i < 3; i++ {
		if err := p.sync(); err != nil {
			t.Fatal(err)
		}
	}
	if len(n.got) != 1 {
		t.Fatalf("want exactly one notification, got %d: %+v", len(n.got), n.got)
	}
	got := n.got[0]
	for _, want := range []string{"Pause aufgehoben durch Robin", "control #7", "du kannst weiterarbeiten", "Aufgabe"} {
		if !strings.Contains(got.Content, want) {
			t.Errorf("content lacks %q: %s", want, got.Content)
		}
	}
	if got.Meta["control_id"] != "7" || got.Meta["resumed_by"] != "Robin" || got.Meta["event"] != "pause_resumed" {
		t.Errorf("meta = %+v", got.Meta)
	}
	if _, set := agentpause.ReadFlag(pauseTestAgent); set {
		t.Error("the flag must be gone")
	}
	// Eine zweite Pause und ihre Aufhebung ist eine zweite Meldung.
	src.control, src.resumed = &store.AgentControl{ID: 9, Action: store.ControlPause}, nil
	_ = p.sync()
	src.control, src.resumed = nil, resumeOf(9, "Robin")
	_ = p.sync()
	if len(n.got) != 2 || n.got[1].Meta["control_id"] != "9" {
		t.Fatalf("second resume: %+v", n.got)
	}
}

// Ein neu gestarteter Channel kennt die aufgehobene Pause nur aus der
// Serverantwort. Er hat sie nie aktiv gesehen und meldet sie nicht noch einmal.
func TestChannelPauseResumeIsNotRepeatedAfterRestart(t *testing.T) {
	src, n, mk := resumeFixture(t)
	first := mk()
	src.control = &store.AgentControl{ID: 3, Action: store.ControlPause}
	_ = first.sync()
	src.control, src.resumed = nil, resumeOf(3, "Robin")
	_ = first.sync()
	if len(n.got) != 1 {
		t.Fatalf("setup: %d", len(n.got))
	}
	first.close()
	restarted := mk()
	for i := 0; i < 3; i++ {
		_ = restarted.sync()
	}
	if len(n.got) != 1 {
		t.Fatalf("restart repeated the notification: %+v", n.got)
	}
	// Läuft die Pause über den Neustart hinweg weiter, meldet der neue Prozess
	// ihre Aufhebung dagegen genau einmal.
	src.control, src.resumed = &store.AgentControl{ID: 4, Action: store.ControlPause}, nil
	_ = first.sync()
	first.close()
	again := mk()
	_ = again.sync()
	src.control, src.resumed = nil, resumeOf(4, "Robin")
	_ = again.sync()
	_ = again.sync()
	if len(n.got) != 2 || n.got[1].Meta["control_id"] != "4" {
		t.Fatalf("resume across restart: %+v", n.got)
	}
}

// Ist der Transport noch nicht bereit oder scheitert das Schreiben, bleibt die
// Meldung offen und geht mit dem nächsten Durchlauf raus. Die Pause ist
// trotzdem sofort aufgehoben.
func TestChannelPauseResumeRetriesUntilTheNotificationIsWritten(t *testing.T) {
	src, n, mk := resumeFixture(t)
	p := mk()
	src.control = &store.AgentControl{ID: 5, Action: store.ControlPause}
	_ = p.sync()
	src.control, src.resumed = nil, resumeOf(5, "Robin")
	n.ready = false
	_ = p.sync()
	if _, set := agentpause.ReadFlag(pauseTestAgent); set {
		t.Fatal("an unready transport must not keep the agent paused")
	}
	n.ready, n.err = true, errors.New("pipe closed")
	_ = p.sync()
	if len(n.got) != 0 {
		t.Fatal("failed write counted as delivered")
	}
	n.err = nil
	_ = p.sync()
	_ = p.sync()
	if len(n.got) != 1 {
		t.Fatalf("want one after recovery, got %d", len(n.got))
	}
}
