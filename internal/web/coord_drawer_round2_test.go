package web

import (
	"os/exec"
	"strings"
	"testing"
)

func TestCoordRoomsDrawerClosesAfterProgressiveNavigation(t *testing.T) {
	source := string(mustReadEmbedded(t, "static/app.js"))
	start := strings.Index(source, "  const shouldCloseRoomsDrawer =")
	end := strings.Index(source, "  const bindDrawerCloseControls =")
	if start < 0 || end <= start {
		t.Fatal("drawer auto-close policy is not independently testable")
	}
	program := source[start:end] + `
if (!shouldCloseRoomsDrawer("coord-rooms", "push", "a", "b")) throw new Error("room switch kept the drawer open");
if (!shouldCloseRoomsDrawer("coord-rooms", "push", "a", "a")) throw new Error("tapping a room link kept the drawer open");
if (shouldCloseRoomsDrawer("coord-context", "push", "a", "a")) throw new Error("context drawer must survive navigation");
if (shouldCloseRoomsDrawer(null, "push", "a", "b")) throw new Error("nothing open, nothing to close");
if (shouldCloseRoomsDrawer("coord-rooms", "", "a", "a")) throw new Error("live refresh closed the drawer");
`
	command := exec.Command("node", "-e", program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("auto-close policy failed: %v\n%s", err, output)
	}
	applyStart := strings.Index(source, "  const applyCoordPage =")
	applyEnd := strings.Index(source, "  const fetchCoordPage =")
	apply := source[applyStart:applyEnd]
	for _, want := range []string{"shouldCloseRoomsDrawer(", "returnFocus = null", "coord-conversation-head h2", "tabIndex = -1"} {
		if !strings.Contains(apply, want) {
			t.Errorf("apply must close the drawer and move focus to the heading: missing %q", want)
		}
	}
	if !strings.Contains(source, "quietSuccess") {
		t.Error("navigation success must be announced without covering the page")
	}
}
