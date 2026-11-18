package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Der Owner einer Organisation entscheidet, ob das Sprachmodell des Servers
// ihre Texte liest; ein Mitglied sieht den Abschnitt nicht und kann nichts ändern.
func TestOrgOwnerSetsTheLanguageModelSwitch(t *testing.T) {
	e := shellWebAll(t)
	org, err := e.St.OrgByRef("alpha")
	if err != nil {
		t.Fatal(err)
	}
	// Alpha gehört dem Instanz-Owner: der Schalter ist an.
	if !e.St.ExternalModelsFor(org.ID) {
		t.Fatal("the org of the instance owner starts with the switch off")
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/orgs?org=alpha")
	mustContain(t, "owner settings", page, `action="/ui/orgs/models"`, "Language model of the server")
	_, member := fetchPage(t, e.Member, e.Base+"/ui/orgs?org=alpha")
	mustNotContain(t, "member settings", member, `action="/ui/orgs/models"`)

	// Ein Mitglied ändert nichts.
	postForm(t, e, e.Member, "/ui/orgs/models", url.Values{"org": {"alpha"}})
	if !e.St.ExternalModelsFor(org.ID) {
		t.Fatal("a member switched the setting")
	}
	// Der Owner schaltet aus (Kästchen ohne Haken sendet nichts).
	if r := postForm(t, e, e.Owner, "/ui/orgs/models", url.Values{"org": {"alpha"}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("owner post: %d", r.StatusCode)
	}
	if e.St.ExternalModelsFor(org.ID) {
		t.Fatal("the owner could not switch it off")
	}
	_, page = fetchPage(t, e.Owner, e.Base+"/ui/orgs?org=alpha")
	if strings.Contains(page, `name="external_models" value="1" checked`) {
		t.Fatal("the box is still checked after switching off")
	}
	if r := postForm(t, e, e.Owner, "/ui/orgs/models", url.Values{"org": {"alpha"}, "external_models": {"1"}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("owner post: %d", r.StatusCode)
	}
	if !e.St.ExternalModelsFor(org.ID) {
		t.Fatal("the owner could not switch it on")
	}
}

// Die Richtlinienseite sagt, dass die automatische Übernahme nur für Belege aus
// Mitgliedertext gilt.
func TestWorkflowPolicyPageSaysAutoCoversMemberTextOnly(t *testing.T) {
	e := shellWebAll(t)
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/orgs?org=alpha")
	mustContain(t, "policy page", page, "only when the quotes come from text written by members")
}
