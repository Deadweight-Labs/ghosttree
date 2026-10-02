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

func TestCoordAttentionListShowsThreeItemsAndFoldsTheRest(t *testing.T) {
	page, _ := coordCompactAttentionFixture(t)
	start := strings.Index(page, `aria-labelledby="coord-attention-items"`)
	if start < 0 {
		t.Fatal("Needs you section missing")
	}
	section := page[start : start+strings.Index(page[start:], "</section>")]
	more := strings.Index(section, `<details class="coord-attention-more"`)
	if more < 0 {
		t.Fatalf("items beyond three must sit in a native details: %s", section)
	}
	if n := strings.Count(section[:more], `class="coord-attention-card`); n != 3 {
		t.Errorf("visible items before the fold = %d, want 3", n)
	}
	if n := strings.Count(section[more:], `class="coord-attention-card`); n != 2 {
		t.Errorf("folded items = %d, want 2", n)
	}
	if !strings.Contains(section[more:], "2 more") {
		t.Errorf("fold summary must count the rest: %s", section[more:])
	}
	if strings.Contains(section, "<p>Eingehende Anfrage") {
		t.Error("compact entries carry label, sender, room and link, not the body as a paragraph")
	}
	for _, want := range []string{"Question", "Approval", "Blocker", "Handoff", "from bob", "Go to post"} {
		if !strings.Contains(section, want) {
			t.Errorf("compact entry missing %q", want)
		}
	}
}

func TestCoordOutgoingAttentionIsACollapsedDisclosureWithCount(t *testing.T) {
	page, _ := coordCompactAttentionFixture(t)
	start := strings.Index(page, `<details class="coord-attention-outgoing `)
	if start < 0 {
		t.Fatal("Asked by you must be a details element")
	}
	open := page[start : start+strings.Index(page[start:], ">")]
	if strings.Contains(open, "open") {
		t.Errorf("outgoing attention must start collapsed: %s", open)
	}
	summary := page[start : start+strings.Index(page[start:], "</summary>")]
	if !strings.Contains(summary, "Asked by you") || !strings.Contains(summary, "1") {
		t.Errorf("summary needs title and count: %s", summary)
	}
}

func TestCoordThreadsFollowTheCompactAttentionDirectly(t *testing.T) {
	page, _ := coordCompactAttentionFixture(t)
	needs := strings.Index(page, `id="coord-attention-items"`)
	outgoing := strings.Index(page, `class="coord-attention-outgoing `)
	threads := strings.Index(page, "<h3>Threads</h3>")
	if !(needs >= 0 && needs < outgoing && outgoing < threads) {
		t.Fatalf("order must be needs-you, outgoing, threads: %d %d %d", needs, outgoing, threads)
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
	if !strings.Contains(page, `title="github.com/acme/compact-repo">acme/compact-repo<`) {
		t.Errorf("sidebar project link must show owner/repo and keep the full name in title")
	}
	start := strings.Index(page, `id="coord-attention-items"`)
	section := page[start : start+strings.Index(page[start:], "</section>")]
	if !strings.Contains(section, `title="github.com/acme/compact-repo">acme/compact-repo<`) {
		t.Errorf("attention entries must use the short name too: %s", section)
	}
}

func TestCoordShortRoomName(t *testing.T) {
	for in, want := range map[string]string{
		"github.com/acme/repo":     "acme/repo",
		"gitlab.com/g/sub/repo":    "sub/repo",
		"repo":                     "repo",
		"Release":                  "Release",
		"host.example/owner/r.git": "owner/r.git",
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
	start := strings.Index(page, `<article class="coord-attention-card" title="`+needle+`"`)
	if start < 0 {
		t.Fatalf("no attention card for %q", needle)
	}
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
	if !strings.Contains(page, `<p class="coord-attention-preview">&lt;script&gt;alert(1)&lt;/script&gt;`) {
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
	if note < 0 || note > button || note > strings.Index(card, `class="coord-attention-row"`) {
		t.Errorf("honest note must sit on the card directly above the verdict row: note=%d button=%d", note, button)
	}
}
