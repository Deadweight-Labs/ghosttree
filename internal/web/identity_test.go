package web

import (
	"regexp"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

var identityTagRE = regexp.MustCompile(`<[^>]*>`)

// The text a person reads of an identifier is the number; the prefix is only
// in the title attribute (decision #4310).
func TestRefShowsNoPrefixInVisibleText(t *testing.T) {
	for kind, prefix := range map[string]string{"request": "REQ-", "pain": "PAIN-", "idea": "IDEA-", "task": "TASK-"} {
		out := string(refHTML(langDE, kind, 599, "/ui/x"))
		visible := identityTagRE.ReplaceAllString(out, "")
		if visible != "#599" {
			t.Errorf("%s: visible text %q, want #599", kind, visible)
		}
		if strings.Contains(out, `title="`) || !strings.Contains(out, `data-ref="`+prefix+"599") {
			t.Errorf("%s: the card key lacks %s599 or a tooltip stands next to the card: %s", kind, prefix, out)
		}
	}
	if out := string(refHTML(langDE, "request", 1, "")); strings.Contains(out, "<a ") {
		t.Errorf("without an address the identifier is no link: %s", out)
	}
}

func TestAvatarKindsAndEscaping(t *testing.T) {
	cases := map[string]string{"claude": avClaude, "Claude@laptop": avClaude, "codex": avCodex, "gardener": avGardener, "ghosttree": avSystem, "Robin": avHuman}
	for name, want := range cases {
		if got := actorKind(name); got != want {
			t.Errorf("actorKind(%q) = %s, want %s", name, got, want)
		}
	}
	if harnessKind("claude-code") != avClaude || harnessKind("codex") != avCodex || harnessKind("x") != avSystem {
		t.Error("harnessKind maps the harnesses wrongly")
	}
	out := string(avatarHTML(langDE, avHuman, `<b>"x"</b>`, true))
	if strings.Contains(out, "<b>") || !strings.Contains(out, `class="waits"`) {
		t.Errorf("name not escaped or ring missing: %s", out)
	}
	if out := string(avatarHTML(langDE, avClaude, "", false)); !strings.Contains(out, `a-claude`) || !strings.Contains(out, "<svg") || strings.Contains(out, "style=") {
		t.Errorf("claude avatar: %s", out)
	}
	if out := string(avatarHTML(langDE, avHuman, "Robin", false)); !strings.Contains(out, ">RO<") {
		t.Errorf("initials missing: %s", out)
	}
}

// "Claude · Lena Berger · lena-laptop" is Claude of Lena, not a person called
// Claude: the inbox shows the logo with Lena in the corner.
func TestAgentLabelIsRecognisedAsHarnessWithOwner(t *testing.T) {
	for label, want := range map[string]string{
		"Claude · Lena Berger · lena-laptop":               avClaude,
		"Codex · Robin":                                    avCodex,
		"Claude · Robin · mainex · feat/git-native":        avClaude,
		"claude-mainex · Robin · mainex · feat/git-native": avClaude,
		"Agent":                       avSystem,
		"Lena Berger":                 avHuman,
		"Claude Berger · lena-laptop": avHuman,
	} {
		if got := actorKind(label); got != want {
			t.Errorf("actorKind(%q) = %q, want %q", label, got, want)
		}
	}
	out := string(avatarMarkup(langEN, avatarSpec{Name: "Claude · Lena Berger · lena-laptop", Size: "lg"}))
	for _, want := range []string{"a-claude", `class="own hc-`, `data-av="Lena Berger"`, "Claude · agent of Lena Berger"} {
		if !strings.Contains(out, want) {
			t.Errorf("avatar of the label lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "CL") || strings.Contains(out, "lena-laptop") {
		t.Errorf("the label leaks into the avatar:\n%s", out)
	}
}

// An agent is called by its tool or its own name, never by its owner alone.
func TestPeerNameStartsWithToolNotOwner(t *testing.T) {
	peer := store.CoordAgent{ExternalID: "claude:mainex:44444444-aaaa-4bbb-8ccc-000000000004", Provider: "claude", DisplayName: "robin", Owner: "Robin", Person: "Robin", Machine: "mainex", Branch: "feat/git-native", Label: "Robin · mainex · feat/git-native"}
	if got := peerName(langEN, peer); got != "Claude · Robin · mainex · feat/git-native" {
		t.Errorf("peerName = %q", got)
	}
	peer.Provider, peer.ExternalID = "codex", "codex:mainex:55555555-aaaa-4bbb-8ccc-000000000005"
	if got := agentTitle(langEN, peer); got != "Codex" {
		t.Errorf("agentTitle = %q", got)
	}
	peer.DisplayName = "reviewer"
	if got := peerName(langEN, peer); got != "reviewer · Robin · mainex · feat/git-native" {
		t.Errorf("own name lost: %q", got)
	}
	row := agentRow{BareName: peerName(langEN, peer)}
	if row.ShortName() != "reviewer" {
		t.Errorf("short name %q", row.ShortName())
	}
}
