package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/acme-billing/ledgerly/internal/store"
)

func newTestServer(t *testing.T) (*store.Store, http.Handler) {
	t.Helper()
	st := store.New()
	store.Seed(st)
	return st, NewServer(st).Handler()
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

func TestListCustomers(t *testing.T) {
	_, h := newTestServer(t)
	rec := get(t, h, "/v1/customers")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var body struct {
		Customers []customerDTO `json:"customers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Customers) != 3 || !strings.HasPrefix(body.Customers[0].ID, "cus_") {
		t.Fatalf("got %+v", body.Customers)
	}
}

func TestBalance(t *testing.T) {
	st, h := newTestServer(t)
	acc := st.Accounts()[0]
	rec := get(t, h, "/v1/accounts/"+acc.PublicID()+"/balance")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var b balanceDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &b)
	if b.Invoiced != 21265+17731 || b.Paid != 21265 {
		t.Fatalf("got %+v", b)
	}
}

func TestUnknownIDIs404(t *testing.T) {
	_, h := newTestServer(t)
	for _, p := range []string{"/v1/accounts/acc_zzz/balance", "/v1/accounts/1/balance", "/v1/invoices/cus_abc"} {
		if rec := get(t, h, p); rec.Code != 404 {
			t.Errorf("%s: status %d", p, rec.Code)
		}
	}
}

func TestCreatePaymentValidates(t *testing.T) {
	st, h := newTestServer(t)
	acc := st.Accounts()[0]
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/accounts/"+acc.PublicID()+"/payments", strings.NewReader(`{"amount_cents":-5}`)))
	if rec.Code != 422 {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestIssueInvoice(t *testing.T) {
	st, h := newTestServer(t)
	var draft string
	for _, inv := range st.Invoices() {
		if inv.Status == "draft" {
			draft = inv.PublicID()
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/invoices/"+draft+"/issue", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/invoices/"+draft+"/issue", nil))
	if rec.Code != 409 {
		t.Fatalf("second issue: status %d", rec.Code)
	}
}

func TestPlansAreCached(t *testing.T) {
	_, h := newTestServer(t)
	get(t, h, "/v1/plans")
	if rec := get(t, h, "/v1/plans"); rec.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("X-Cache %q", rec.Header().Get("X-Cache"))
	}
}
