package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func bearerCall(t *testing.T, srv *httptest.Server, method, path, token string) int {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestRevokeOwnTokenWorksAtOnceReleasesTheMachineAndLeavesOthersAlone(t *testing.T) {
	srv, st := deviceFixture(t)
	if _, err := st.AddPerson("anna"); err != nil {
		t.Fatal(err)
	}
	mine, _, err := st.CreateDeviceToken("person:2", "annas-box")
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := st.CreateDeviceToken("person:2", "annas-other")
	if err != nil {
		t.Fatal(err)
	}
	alice, _, err := st.CreateDeviceToken("person:1", "alices-box")
	if err != nil {
		t.Fatal(err)
	}
	if code := bearerCall(t, srv, "GET", "/api/whoami", mine); code != 200 {
		t.Fatalf("whoami before: %d", code)
	}
	if code := bearerCall(t, srv, "DELETE", "/api/tokens/self", mine); code != http.StatusNoContent {
		t.Fatalf("revoke: %d", code)
	}
	if code := bearerCall(t, srv, "GET", "/api/whoami", mine); code != http.StatusUnauthorized {
		t.Fatalf("revoked token still works: %d", code)
	}
	if code := bearerCall(t, srv, "DELETE", "/api/tokens/self", mine); code != http.StatusUnauthorized {
		t.Fatalf("second revoke: %d", code)
	}
	for name, tok := range map[string]string{"other token of the same account": other, "token of another account": alice} {
		if code := bearerCall(t, srv, "GET", "/api/whoami", tok); code != 200 {
			t.Fatalf("%s was touched: %d", name, code)
		}
	}
	machines, _ := st.ListMachines("person:2")
	var names []string
	for _, m := range machines {
		names = append(names, m.Name)
	}
	for _, n := range names {
		if n == "annas-box" {
			t.Fatalf("machine not released: %v", names)
		}
	}
	if len(names) != 1 {
		t.Fatalf("machines %v", names)
	}
	// Der Name ist wieder frei.
	if _, _, err := st.CreateDeviceToken("person:1", "annas-box"); err != nil {
		t.Fatalf("name not free: %v", err)
	}
}

func TestRevokeOwnTokenRefusesAWebSession(t *testing.T) {
	_, st := deviceFixture(t)
	a := &api{st: st}
	for _, p := range []store.Principal{
		{ID: "person:1", Label: "alice", TokenKind: store.WebSessionKind},
		{ID: "person:1", Label: "alice"},
	} {
		req := httptest.NewRequest("DELETE", "/api/tokens/self", nil)
		req = req.WithContext(context.WithValue(req.Context(), personKey{}, p))
		w := httptest.NewRecorder()
		a.revokeOwnToken(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%+v: %d", p, w.Code)
		}
	}
}
