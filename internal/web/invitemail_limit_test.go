package web

import (
	"strings"
	"testing"
)

// Der Org-Name steht im Mailtext: er bleibt eine Zeile ohne Link und ohne
// Steuerzeichen, damit eine Einladung kein Träger fremder Texte ist.
func TestMailLineIsOneLineWithoutLinks(t *testing.T) {
	for in, want := range map[string]string{
		"Werkstatt Keller":                        "Werkstatt Keller",
		"Win now https://evil.example/login team": "Win now team",
		"Visit evil.example today":                "Visit today",
		"www.evil.example":                        "",
		"Line\none‮two":                           "Line one two",
		"  spaced   out  ":                        "spaced out",
		"v1.2 Team":                               "v1.2 Team",
		"Visit evil.example, today":               "Visit today",
		"(evil.example) now":                      "now",
		"evil。example":                            "",
		"evil.example.":                           "",
		"go to evil.example/login":                "go to",
	} {
		if got := mailLine(in, 60); got != want {
			t.Errorf("mailLine(%q) = %q, want %q", in, got, want)
		}
	}
	if got := mailLine(strings.Repeat("a", 200), 60); len([]rune(got)) > 60 {
		t.Errorf("a long name is cut: %d runes", len([]rune(got)))
	}
}

func TestInviteMailTextUsesTheCleanedOrgName(t *testing.T) {
	m := newMailInstance(t, false)
	m.account("alice", "alice@example.test")
	if _, err := m.st.CreateOrg("person:1", "Gewinn jetzt https://evil.example/x holen", "gewinn"); err != nil {
		t.Fatal(err)
	}
	owner := loginInteractive(t, m.srv, m.st, "alice")
	postOrgLoc(t, owner, m.srv.URL, "/ui/orgs/invite", inviteForm("org", "gewinn", "email", "jonas@werkstatt.test"))
	mails := m.mails("jonas@werkstatt.test")
	if len(mails) != 1 {
		t.Fatalf("mails: %d", len(mails))
	}
	if strings.Contains(mails[0], "evil.example") {
		t.Errorf("the link of the org name reached the mail:\n%s", mails[0])
	}
}

// Eine Adresse bekommt am Tag nur wenige Einladungen, von wem auch immer; die
// Einladung entsteht trotzdem, nur die Mail unterbleibt, und die Seite sagt es.
func TestInviteMailToOneAddressIsLimited(t *testing.T) {
	m, owner := mailedOrg(t)
	var loc string
	for i := 0; i < 4; i++ {
		loc = postOrgLoc(t, owner, m.srv.URL, "/ui/orgs/invite", inviteForm("email", "jonas@werkstatt.test"))
	}
	if n := len(m.mails("jonas@werkstatt.test")); n != 3 {
		t.Fatalf("mails sent to one address: %d, want 3", n)
	}
	page := pageAt(t, owner, m.srv.URL, loc)
	if !strings.Contains(page, "No mail was sent: too many invitations") {
		t.Errorf("the page does not say why no mail went out: %s", pageSpan(t, page, `id="members"`, `id="projects"`))
	}
}
