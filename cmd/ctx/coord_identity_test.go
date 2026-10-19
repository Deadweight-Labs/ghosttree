package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Anforderung 1: die Hook-Anmeldung meldet die Maschine mit.
func TestSessionStartRegistersTheMachine(t *testing.T) {
	e := registerEnv(t)
	runHookAs(t, "session-start", "claude", registerPayload(t, "sess-machine"))
	p, ok := registeredPeer(t, e, "sess-machine")
	if !ok {
		t.Fatal("not registered")
	}
	if p.Machine != "chanbox" || !strings.Contains(p.Label, "chanbox") {
		t.Errorf("machine %q, label %q", p.Machine, p.Label)
	}
}

// Anforderung 1 und 2: die Hook-Absenderzeile nennt Person, Maschine, Branch und
// die selbstgemeldete Notiz statt nur der Kennung.
func TestHookSenderLineNamesPersonMachineBranchAndNote(t *testing.T) {
	e := hookEnv(t)
	room := store.RoomKeyForMachine("chanbox")
	if _, err := e.a.RegisterCoordAgent(store.CoordAgent{ExternalID: "sess-sender", Provider: "claude", RoomKey: room, DisplayName: "sess-sender", Machine: "mac-lab", Branch: "feat/mac-sync"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.a.SetCoordStatus("sess-sender", "PR #77 feat/mac-sync"); err != nil {
		t.Fatal(err)
	}
	e.say(t, "wer macht den PR?", channelSelf)
	got := runHook(t, "user-prompt-submit", prompt())
	for _, want := range []string{"robin \u00b7 mac-lab \u00b7 feat/mac-sync (sess-sender)", "[working on, self-reported: PR #77 feat/mac-sync]", "self-reported and unverified"} {
		if !strings.Contains(got, want) {
			t.Errorf("hook context lacks %q:\n%s", want, got)
		}
	}
}

// Anforderung 3: @agents erreicht über den vorhandenen Hook jeden Agenten mit
// Hook-Sichtung, auch ohne dass er einzeln genannt wird.
func TestBroadcastIsDeliveredByTheHook(t *testing.T) {
	e := hookEnv(t)
	room := store.RoomKeyForMachine("chanbox")
	if _, err := e.a.RegisterCoordAgent(store.CoordAgent{ExternalID: channelSelf, Provider: "claude", RoomKey: room, DisplayName: channelSelf, HookActivity: true}); err != nil {
		t.Fatal(err)
	}
	e.say(t, "@agents wer arbeitet an welchem PR?")
	got := runHook(t, "user-prompt-submit", prompt())
	if !strings.Contains(got, "wer arbeitet an welchem PR?") || !strings.Contains(got, "sess-sender") {
		t.Errorf("the broadcast did not arrive:\n%s", got)
	}
}

// Anforderung 2: das CLI-Pendant zu coord_status.
func TestCoordStatusCLISetsAndClearsTheNote(t *testing.T) {
	_, cfg := coordCLIEnv(t)
	repo := coordRepo(t, "https://github.com/x/status.git")
	c := client.New(cfg)
	room := store.RoomKeyForProject("github.com/x/status")
	var out bytes.Buffer
	if code := run([]string{"coord", "status", "PR #77 feat/mac-sync", repo}, &out); code != 0 {
		t.Fatalf("code %d: %s", code, out.String())
	}
	peers, err := c.CoordPeers(room, "", "cli:testbox")
	if err != nil || len(peers) != 1 || peers[0].WorkNote != "PR #77 feat/mac-sync" || peers[0].Machine != "testbox" {
		t.Fatalf("note not stored: %+v %v", peers, err)
	}
	out.Reset()
	if code := run([]string{"coord", "peers", repo}, &out); code != 0 || !strings.Contains(out.String(), "PR #77 feat/mac-sync") || !strings.Contains(out.String(), "self-reported") {
		t.Fatalf("peers do not show the note as self-reported: %s", out.String())
	}
	out.Reset()
	if code := run([]string{"coord", "status", "--clear", repo}, &out); code != 0 {
		t.Fatalf("clear: %d %s", code, out.String())
	}
	if peers, _ := c.CoordPeers(room, "", "cli:testbox"); len(peers) != 1 || peers[0].WorkNote != "" {
		t.Fatalf("note not cleared: %+v", peers)
	}
}
