package web

import (
	"net/http"
	"testing"
)

// loginAs runs one bootstrap login against a fresh instance and returns the
// name the account got.
func bootstrapName(t *testing.T, setup func(*fakeIdP)) (name string, env *oidcEnv) {
	t.Helper()
	env = newOIDCEnv(t, false)
	env.idp.username = ""
	setup(env.idp)
	code, _, _ := env.store.EnsureBootstrapCode()
	b := newBrowser(t)
	resp := env.callback(t, b, env.startFlow(t, b, code))
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	accounts, _ := env.store.ListAccounts()
	if len(accounts) != 1 {
		t.Fatalf("accounts=%+v", accounts)
	}
	return accounts[0].Name, env
}

func TestOIDCNameComesFromUserinfoWhenTheIDTokenHasNone(t *testing.T) {
	for name, tc := range map[string]struct {
		info map[string]any
		want string
	}{
		"name":           {map[string]any{"sub": "sub-1", "name": "Anna Berger"}, "Anna Berger"},
		"given+family":   {map[string]any{"sub": "sub-1", "given_name": "Anna", "family_name": "Berger"}, "Anna Berger"},
		"given only":     {map[string]any{"sub": "sub-1", "given_name": "Anna"}, "Anna"},
		"nickname":       {map[string]any{"sub": "sub-1", "nickname": "Annie", "preferred_username": "anna.b"}, "Annie"},
		"username":       {map[string]any{"sub": "sub-1", "preferred_username": "anna.b"}, "anna.b"},
		"name beats all": {map[string]any{"sub": "sub-1", "name": "Anna B", "nickname": "n", "preferred_username": "u"}, "Anna B"},
		// A login name that is an address is never shown whole.
		"address as username": {map[string]any{"sub": "sub-1", "preferred_username": "anna@example.test"}, "anna"},
		"verified email only": {map[string]any{"sub": "sub-1", "email": "ben@example.test", "email_verified": true}, "ben"},
		"unverified email":    {map[string]any{"sub": "sub-1", "email": "ben@example.test"}, "admin"},
		// Another subject's profile must not be taken.
		"wrong subject": {map[string]any{"sub": "someone-else", "name": "Mallory"}, "admin"},
		"empty":         {map[string]any{"sub": "sub-1"}, "admin"},
	} {
		t.Run(name, func(t *testing.T) {
			got, env := bootstrapName(t, func(f *fakeIdP) { f.userinfo = tc.info; f.email = "" })
			if got != tc.want {
				t.Fatalf("name %q, want %q", got, tc.want)
			}
			if env.idp.userinfoCalls != 1 {
				t.Fatalf("userinfo calls %d", env.idp.userinfoCalls)
			}
		})
	}
}

func TestOIDCUserinfoFailureNeverBlocksTheLogin(t *testing.T) {
	got, _ := bootstrapName(t, func(f *fakeIdP) { f.userinfo = nil; f.email = "" })
	if got != "admin" {
		t.Fatalf("name %q", got)
	}
}

func TestOIDCSkipsUserinfoWhenTheIDTokenAlreadyNamesThePerson(t *testing.T) {
	got, env := bootstrapName(t, func(f *fakeIdP) {
		f.idTokenName = map[string]any{"name": "Anna Berger"}
		f.userinfo = map[string]any{"sub": "sub-1", "name": "Other"}
	})
	if got != "Anna Berger" || env.idp.userinfoCalls != 0 {
		t.Fatalf("name %q, calls %d", got, env.idp.userinfoCalls)
	}
}

func TestOIDCLoginRefreshesAnIdPNameButNotAChosenOne(t *testing.T) {
	got, env := bootstrapName(t, func(f *fakeIdP) { f.userinfo = map[string]any{"sub": "sub-1"}; f.email = "" })
	if got != "admin" {
		t.Fatalf("first name %q", got)
	}
	login := func() string {
		b := newBrowser(t)
		resp := env.callback(t, b, env.startFlow(t, b, ""))
		resp.Body.Close()
		a, _ := env.store.ListAccounts()
		return a[0].Name
	}
	env.idp.userinfo = map[string]any{"sub": "sub-1", "name": "Anna Berger"}
	if n := login(); n != "Anna Berger" {
		t.Fatalf("second login name %q", n)
	}
	if _, err := env.store.SetOwnName("person:1", "Anni"); err != nil {
		t.Fatal(err)
	}
	if n := login(); n != "Anni" {
		t.Fatalf("chosen name overwritten: %q", n)
	}
}
