package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func inviteEnv(t *testing.T) (*oidcEnv, store.Org) {
	t.Helper()
	env := newOIDCEnv(t, true)
	org, err := env.store.CreateOrg("person:1", "Alpha", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	return env, org
}

func (e *oidcEnv) invite(t *testing.T, org store.Org, email string) string {
	t.Helper()
	code, _, err := e.store.CreateInvitation("person:1", org.ID, email, store.OrgMember, 0)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func TestOIDCInvitationCreatesAccountAndMembershipOnce(t *testing.T) {
	env, org := inviteEnv(t)
	code := env.invite(t, org, "")
	env.idp.subject, env.idp.username, env.idp.email = "sub-anna", "anna", "anna@example.test"
	b := newBrowser(t)
	resp := env.callback(t, b, env.startFlow(t, b, code))
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || !env.signedIn(t, b) {
		t.Fatalf("invited sign-in status=%d", resp.StatusCode)
	}
	anna, err := env.store.AccountByName("anna")
	if err != nil || anna.Admin || env.store.OrgRole(org.ID, anna.ID) != store.OrgMember {
		t.Fatalf("anna=%+v err=%v", anna, err)
	}
	// Einmalig: eine zweite Identität mit demselben Code bekommt nichts.
	env.idp.subject, env.idp.username = "sub-mallory", "mallory"
	b2 := newBrowser(t)
	resp = env.callback(t, b2, env.startFlow(t, b2, code))
	text := body(t, resp)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(text, "Code not accepted") || env.signedIn(t, b2) {
		t.Fatalf("reused invitation status=%d body=%s", resp.StatusCode, text)
	}
	if _, err := env.store.AccountByName("mallory"); err == nil {
		t.Fatal("reused invitation created an account")
	}
}

// Eine an eine Adresse gebundene Einladung verlangt eine VERIFIZIERTE gleiche
// E-Mail. Meldet der IdP sie als unverifiziert, entsteht nichts und der Code
// bleibt für den Richtigen gültig.
func TestOIDCInvitationWithEmailNeedsAVerifiedMatch(t *testing.T) {
	env, org := inviteEnv(t)
	code := env.invite(t, org, "anna@example.test")
	env.idp.subject, env.idp.username, env.idp.email = "sub-mallory", "mallory", "anna@example.test"
	env.idp.unverified = true
	b := newBrowser(t)
	resp := env.callback(t, b, env.startFlow(t, b, code))
	text := body(t, resp)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(text, "another email address") || env.signedIn(t, b) {
		t.Fatalf("unverified claim of the invited address: status=%d body=%s", resp.StatusCode, text)
	}
	if accounts, _ := env.store.ListAccounts(); len(accounts) != 1 {
		t.Fatalf("accounts=%+v", accounts)
	}
	// Verifiziert, aber eine andere Adresse.
	env.idp.unverified, env.idp.email = false, "mallory@example.test"
	b = newBrowser(t)
	resp = env.callback(t, b, env.startFlow(t, b, code))
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("other verified address status=%d", resp.StatusCode)
	}
	// Der Eingeladene mit verifizierter Adresse kommt durch.
	env.idp.subject, env.idp.username, env.idp.email = "sub-anna", "anna", "Anna@Example.test"
	b = newBrowser(t)
	resp = env.callback(t, b, env.startFlow(t, b, code))
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || !env.signedIn(t, b) {
		t.Fatalf("verified match status=%d", resp.StatusCode)
	}
}

func TestOIDCKnownAccountJoinsASecondOrgByInvitation(t *testing.T) {
	env, _ := inviteEnv(t)
	second, err := env.store.CreateOrg("person:1", "Beta", "beta")
	if err != nil {
		t.Fatal(err)
	}
	anna, _ := env.store.AddAccount("anna", "", false)
	claim, _, _ := env.store.CreateAccountCode(store.CodeClaim, "anna")
	env.idp.subject, env.idp.username = "sub-anna", "anna"
	b := newBrowser(t)
	env.callback(t, b, env.startFlow(t, b, claim)).Body.Close()

	b = newBrowser(t)
	resp := env.callback(t, b, env.startFlow(t, b, env.invite(t, second, "")))
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || env.store.OrgRole(second.ID, anna.ID) != store.OrgMember {
		t.Fatalf("join status=%d role=%q", resp.StatusCode, env.store.OrgRole(second.ID, anna.ID))
	}
}

func TestOIDCWrongInvitationCodesLockTheIdentity(t *testing.T) {
	env, org := inviteEnv(t)
	good := env.invite(t, org, "")
	env.idp.subject, env.idp.username = "sub-guesser", "guesser"
	for i := 0; i < 5; i++ {
		b := newBrowser(t)
		resp := env.callback(t, b, env.startFlow(t, b, "guess-"+strings.Repeat("x", i)))
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("attempt %d status=%d", i, resp.StatusCode)
		}
	}
	b := newBrowser(t)
	resp := env.callback(t, b, env.startFlow(t, b, good))
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests || env.signedIn(t, b) {
		t.Fatalf("locked identity status=%d", resp.StatusCode)
	}
}
