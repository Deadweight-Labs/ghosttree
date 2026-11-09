package web

import (
	"regexp"
	"strings"
	"testing"
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
		if !strings.Contains(out, `title="`) || !strings.Contains(out, prefix+"599") {
			t.Errorf("%s: title lacks %s599: %s", kind, prefix, out)
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
