package server

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// An agent id that names the machine of another account is not free for a
// stranger: messages meant for an agent there cannot be taken by registering
// its id first. The owner of the machine and a free machine name pass.
func TestAgentIDNamingAnotherAccountsMachineCannotBeTaken(t *testing.T) {
	f := accessAPI(t, true)
	if err := f.st.ClaimMachine("robin-box", "person:1"); err != nil {
		t.Fatal(err)
	}
	register := func(who, id string) (int, string) {
		return f.call(t, who, "POST", "/api/coord/agents", store.CoordAgent{
			ExternalID: id, Provider: "claude", RoomKey: "project:" + accProject, DisplayName: id, Machine: "robin-box",
		})
	}
	if code, out := register("mia", "claude:robin-box:abc123"); code != 409 || !strings.Contains(out, "machine_name_taken") {
		t.Errorf("a stranger takes an id on robin's machine: %d %s", code, out)
	}
	if code, out := register("robin", "claude:robin-box:abc123"); code != 200 {
		t.Errorf("the owner of the machine: %d %s", code, out)
	}
	if code, out := register("mia", "claude:mia-box:abc123"); code != 200 {
		t.Errorf("a free machine name: %d %s", code, out)
	}
	if code, out := register("mia", "claude:mias-own-name"); code != 200 {
		t.Errorf("an id without a machine: %d %s", code, out)
	}
}
