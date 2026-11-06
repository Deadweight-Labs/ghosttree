package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/acme-billing/ledgerly/internal/billing"
	"github.com/acme-billing/ledgerly/internal/domain"
	"github.com/acme-billing/ledgerly/internal/money"
)

type plan struct {
	Code    string      `json:"code"`
	Name    string      `json:"name"`
	Monthly money.Cents `json:"monthly_cents"`
}

var plans = []plan{
	{Code: "solo", Name: "Solo", Monthly: 1900},
	{Code: "business-m", Name: "Business M", Monthly: 14900},
	{Code: "business-l", Name: "Business L", Monthly: 49900},
	{Code: "addon-archive", Name: "Archiv-Zusatz", Monthly: 900},
}

func (s *Server) listPlans(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"plans": plans})
}

// pathID parses the {id} path value as a public ID with the given prefix.
func pathID(w http.ResponseWriter, r *http.Request, prefix string) (int64, bool) {
	id, ok := domain.ParsePublicID(prefix, r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return 0, false
	}
	return id, true
}

func (s *Server) listCustomers(w http.ResponseWriter, r *http.Request) {
	out := []customerDTO{}
	for _, c := range s.store.Customers() {
		out = append(out, toCustomerDTO(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"customers": out})
}

func (s *Server) getCustomer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "cus")
	if !ok {
		return
	}
	c, err := s.store.Customer(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toCustomerDTO(c))
}

func (s *Server) listCustomerAccounts(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "cus")
	if !ok {
		return
	}
	if _, err := s.store.Customer(id); err != nil {
		writeStoreError(w, err)
		return
	}
	out := []accountDTO{}
	for _, a := range s.store.AccountsOf(id) {
		out = append(out, toAccountDTO(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": out})
}

func (s *Server) getAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "acc")
	if !ok {
		return
	}
	a, err := s.store.Account(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toAccountDTO(a))
}

func (s *Server) getBalance(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "acc")
	if !ok {
		return
	}
	b, err := billing.AccountBalance(s.store, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, balanceDTO{AccountID: domain.Account{ID: id}.PublicID(), Invoiced: b.Invoiced, Paid: b.Paid,
		Due: b.Due, AsOf: s.now().UTC()})
}

func (s *Server) getLedger(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "acc")
	if !ok {
		return
	}
	entries, err := billing.Ledger(s.store, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out := []ledgerEntryDTO{}
	for _, e := range entries {
		out = append(out, ledgerEntryDTO{At: e.At, Kind: e.Kind, Ref: e.Ref, Amount: e.Amount})
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": out})
}

func (s *Server) listPayments(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "acc")
	if !ok {
		return
	}
	if _, err := s.store.Account(id); err != nil {
		writeStoreError(w, err)
		return
	}
	out := []paymentDTO{}
	for _, p := range s.store.PaymentsOf(id) {
		out = append(out, toPaymentDTO(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"payments": out})
}

type createPaymentRequest struct {
	AmountCents int64  `json:"amount_cents"`
	Reference   string `json:"reference"`
}

func (s *Server) createPayment(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "acc")
	if !ok {
		return
	}
	var req createPaymentRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if req.AmountCents <= 0 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_amount", "amount_cents must be positive")
		return
	}
	p, err := s.store.AddPayment(domain.Payment{AccountID: id, Amount: money.Cents(req.AmountCents),
		Reference: strings.TrimSpace(req.Reference), ReceivedAt: s.now()})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toPaymentDTO(p))
}

func (s *Server) getInvoice(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "inv")
	if !ok {
		return
	}
	inv, err := s.store.Invoice(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toInvoiceDTO(inv))
}

func (s *Server) issueInvoice(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "inv")
	if !ok {
		return
	}
	inv, err := s.store.Invoice(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if inv.Status != domain.InvoiceDraft {
		writeError(w, http.StatusConflict, "not_draft", "only draft invoices can be issued")
		return
	}
	inv.Status = domain.InvoiceIssued
	inv.IssuedAt = s.now()
	inv.DueAt = inv.IssuedAt.AddDate(0, 0, 14)
	if err := s.store.UpdateInvoice(inv); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toInvoiceDTO(inv))
}
