package web

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// coordCompactAttentionFixture: robin has five open items from bob (question,
// approval x1, blockers) and has asked bob one question himself.
func coordCompactAttentionFixture(t *testing.T) (string, string) {
	t.Helper()
	srv, st, client := signedIn(t)
	if _, err := st.AddPerson("bob"); err != nil {
		t.Fatal(err)
	}
	room := store.RoomKeyForProject("github.com/acme/compact-repo")
	materializeWebRoom(t, st, room)
	materializeWebRoomFor(t, st, room, "person:2", "bob")
	intents := []string{store.IntentQuestion, store.IntentApproval, store.IntentBlocker, store.IntentQuestion, store.IntentHandoff}
	for i, intent := range intents {
		if _, err := st.AppendCoordMessage(store.CoordMessage{
			DestinationKind: store.DestinationRoom, DestinationID: room,
			SenderExternalID: "bob", AuthorPrincipalID: "person:2", AuthorKind: store.AuthorHuman,
			ClientID: fmt.Sprintf("in-%d", i), Body: fmt.Sprintf("Eingehende Anfrage %d", i), Intent: intent, Mentions: []string{"person:1"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.AppendCoordMessage(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room,
		SenderExternalID: "robin", AuthorPrincipalID: "person:1", AuthorKind: store.AuthorHuman,
		ClientID: "out-1", Body: "Ausgehende Frage", Intent: store.IntentQuestion, Mentions: []string{"person:2"},
	}); err != nil {
		t.Fatal(err)
	}
	return coordPageBody(t, client, srv.URL+"/ui/coord?room="+url.QueryEscape(room)), room
}

func TestCoordRoomHasNoRightColumnWithNeedsYou(t *testing.T) {
	page, _ := coordCompactAttentionFixture(t)
	for _, gone := range []string{`id="coord-attention-items"`, `coord-attention-outgoing`, `coord-attention-list`, `coord-drawer-toggle`} {
		if strings.Contains(page, gone) {
			t.Errorf("the room has no right column any more, found %s", gone)
		}
	}
	// What waits for the viewer stays reachable in the room: as cards in the conversation.
	if !strings.Contains(page, "Eingehende Anfrage 0") || !strings.Contains(page, `class="rm-msg is-ask"`) {
		t.Error("the questions to the viewer are cards in the conversation")
	}
}

func TestCoordApprovalActionsAreHonestAboutBeingARecord(t *testing.T) {
	page, _ := coordCompactAttentionFixture(t)
	for _, want := range []string{"Note approval", "Note rejection"} {
		if !strings.Contains(page, want) {
			t.Errorf("approval action %q missing", want)
		}
	}
	if strings.Contains(page, ">Approve<") || strings.Contains(page, ">Reject<") {
		t.Error("buttons must not suggest an external approval")
	}
	note := strings.Index(page, "Coordination only")
	button := strings.Index(page, "Note approval")
	if note < 0 || note > button {
		t.Errorf("the coordination-only note must precede the buttons: note=%d button=%d", note, button)
	}
}

func TestCoordProjectRoomsShowRepoShortNameWithFullNameInTitle(t *testing.T) {
	page, _ := coordCompactAttentionFixture(t)
	if !strings.Contains(page, `title="github.com/acme/compact-repo">compact-repo<`) {
		t.Errorf("sidebar project link must show the repo name and keep the full name in title")
	}
}

func TestCoordShortRoomName(t *testing.T) {
	for in, want := range map[string]string{
		"github.com/acme/repo":     "repo",
		"gitlab.com/g/sub/repo":    "repo",
		"repo":                     "repo",
		"Release":                  "Release",
		"host.example/owner/r.git": "r.git",
	} {
		if got := coordShortRoomName(store.RoomProject, in); got != want {
			t.Errorf("short(%q)=%q want %q", in, got, want)
		}
	}
	if got := coordShortRoomName(store.RoomGroup, "a/b/c"); got != "a/b/c" {
		t.Errorf("only project rooms are shortened, got %q", got)
	}
}

// attentionCard returns the rendered card of the incoming item whose body
// contains needle.
func attentionCard(t *testing.T, page, needle string) string {
	t.Helper()
	at := strings.Index(page, `<p class="ask-t">`+needle+`</p>`)
	if at < 0 {
		t.Fatalf("no attention card for %q", needle)
	}
	start := strings.LastIndex(page[:at], `<article class="rm-msg is-ask">`)
	return page[start : start+strings.Index(page[start:], "</article>")]
}

func TestCoordAttentionPreviewEscapesAgentContent(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/escape-card")
	materializeWebRoom(t, st, room)
	if _, err := st.AppendCoordMessage(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room,
		SenderExternalID: "reviewer", AuthorPrincipalID: "person:2", AuthorKind: store.AuthorHuman,
		ClientID: "esc", Body: `<script>alert(1)</script> "quoted"`, Intent: store.IntentQuestion, Mentions: []string{"person:1"},
	}); err != nil {
		t.Fatal(err)
	}
	page := coordPageBody(t, client, srv.URL+"/ui/coord?room="+url.QueryEscape(room))
	if strings.Contains(page, "<script>alert(1)") {
		t.Fatal("agent content reached the page unescaped")
	}
	if !strings.Contains(page, `&lt;script&gt;alert(1)&lt;/script&gt;`) {
		t.Error("preview must carry the escaped body")
	}
}

func TestCoordAttentionPreviewTruncatesByRunes(t *testing.T) {
	short := "kurz"
	if got := coordAttentionPreview(short); got != short {
		t.Errorf("short body changed: %q", got)
	}
	long := strings.Repeat("ä", coordAttentionPreviewRunes+40)
	got := coordAttentionPreview(long)
	if !utf8.ValidString(got) {
		t.Fatalf("truncation split a rune: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != coordAttentionPreviewRunes+1 || !strings.HasSuffix(got, "…") {
		t.Errorf("want %d runes plus ellipsis, got %d: %q", coordAttentionPreviewRunes, n, got)
	}
}

func TestCoordApprovalVerdictsAreVisibleAndOnlyDismissIsFolded(t *testing.T) {
	page, _ := coordCompactAttentionFixture(t)
	card := attentionCard(t, page, "Eingehende Anfrage 1")
	details := strings.Index(card, `<details class="coord-attention-actions"`)
	if details < 0 {
		t.Fatalf("approval card lost its actions disclosure: %s", card)
	}
	for _, want := range []string{"Note approval", "Note rejection"} {
		at := strings.Index(card, want)
		if at < 0 || at > details {
			t.Errorf("%q must be visible before the disclosure (at=%d, details=%d)", want, at, details)
		}
	}
	folded := card[details:]
	if strings.Contains(folded, "Note ") || !strings.Contains(folded, "Dismiss") {
		t.Errorf("The disclosure must hold only Dismiss: %s", folded)
	}
	note, button := strings.Index(card, "Coordination only"), strings.Index(card, "Note approval")
	if note < 0 || note > button || note > strings.Index(card, `class="acts rm-foot"`) {
		t.Errorf("honest note must sit on the card directly above the verdict row: note=%d button=%d", note, button)
	}
}
