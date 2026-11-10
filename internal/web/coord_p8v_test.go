package web

import (
	"slices"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// A card with one item per addressee (an agent and a person) shows each action
// once: one main action, the rest in the menu, withdrawing last and for both items.
func TestCoordRequestMergesTheItemsOfAllAddressees(t *testing.T) {
	approve := coordAttentionActionView{Value: store.AttentionActionApprove, Label: "Note approval"}
	reject := coordAttentionActionView{Value: store.AttentionActionReject, Label: "Note rejection"}
	dismiss := coordAttentionActionView{Value: store.AttentionActionDismiss, Label: "Dismiss"}
	request := coordRequestView{WithdrawLabel: "Withdraw", Items: []coordAttentionView{
		{ID: 1, RoomKey: "r", CSRFToken: "c", Incoming: true, CanWithdraw: true, CoordinationOnlyApproval: true, Inline: []coordAttentionActionView{approve, reject}, More: []coordAttentionActionView{dismiss}},
		{ID: 2, RoomKey: "r", CSRFToken: "c", CanWithdraw: true, CoordinationOnlyApproval: true},
	}}
	got := request.Merged()
	if got == nil || got.Main == nil || got.Main.Value != store.AttentionActionApprove || !got.Note {
		t.Fatalf("main action: %+v", got)
	}
	var values []string
	for _, a := range got.Menu {
		values = append(values, a.Value)
	}
	if want := []string{store.AttentionActionReject, store.AttentionActionDismiss, store.AttentionActionWithdraw}; !slices.Equal(values, want) {
		t.Errorf("menu = %v, want %v", values, want)
	}
	if last := got.Menu[len(got.Menu)-1]; !slices.Equal(last.IDs, []int64{1, 2}) {
		t.Errorf("withdrawing acts on both items: %v", last.IDs)
	}
	if got.Menu[0].IDs[0] != 1 || len(got.Menu[0].IDs) != 1 {
		t.Errorf("a verdict only acts on the item that offers it: %v", got.Menu[0].IDs)
	}
	if (coordRequestView{}).Merged() != nil {
		t.Error("a card without open items has no foot")
	}
}

// The system line about a mutual wait is written in English by the store; the
// page says it in its own language and leaves other lines alone.
func TestCoordSystemLineIsInTheLanguageOfThePage(t *testing.T) {
	en := "Mutual wait: the agents mentioned here are waiting on each other."
	if got := coordSystemText(langDE, en); got != langDE.msg("coord.system.mutual_wait") || strings.Contains(got, "Mutual") {
		t.Errorf("german line: %q", got)
	}
	if got := coordSystemText(langDE, "Something else"); got != "Something else" {
		t.Errorf("another line changed: %q", got)
	}
}

// What an addressee is called in the head: the agent without owner, machine and branch, each name once.
func TestCoordAddresseesAreShortNamesOnce(t *testing.T) {
	var names []string
	for _, label := range []string{"Claude · Robin · mainex · feat/git-native", "Robin", "Claude · Robin · build-1"} {
		names = appendShortName(names, label)
	}
	if !slices.Equal(names, []string{"Claude", "Robin"}) {
		t.Errorf("names = %v", names)
	}
}
