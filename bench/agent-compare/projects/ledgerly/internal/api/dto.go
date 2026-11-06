package api

import (
	"time"

	"github.com/acme-billing/ledgerly/internal/billing"
	"github.com/acme-billing/ledgerly/internal/domain"
	"github.com/acme-billing/ledgerly/internal/money"
)

// The DTOs below are the wire format. Field names are snake_case and amounts
// are integers in cents.

type customerDTO struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Country   string    `json:"country"`
	Business  bool      `json:"business"`
	CreatedAt time.Time `json:"created_at"`
}

func toCustomerDTO(c domain.Customer) customerDTO {
	return customerDTO{ID: c.PublicID(), Name: c.Name, Email: c.Email, Country: c.Country, Business: c.Business, CreatedAt: c.CreatedAt}
}

type accountDTO struct {
	ID         string    `json:"id"`
	CustomerID string    `json:"customer_id"`
	Plan       string    `json:"plan"`
	Status     string    `json:"status"`
	OpenedAt   time.Time `json:"opened_at"`
}

func toAccountDTO(a domain.Account) accountDTO {
	return accountDTO{ID: a.PublicID(), CustomerID: domain.Customer{ID: a.CustomerID}.PublicID(), Plan: a.Plan,
		Status: string(a.Status), OpenedAt: a.OpenedAt}
}

type lineDTO struct {
	Description string      `json:"description"`
	Quantity    int64       `json:"quantity"`
	UnitPrice   money.Cents `json:"unit_price_cents"`
	TaxRateBP   int64       `json:"tax_rate_bp"`
}

type invoiceDTO struct {
	ID         string      `json:"id"`
	AccountID  string      `json:"account_id"`
	CustomerID string      `json:"customer_id"`
	Number     string      `json:"number"`
	Status     string      `json:"status"`
	IssuedAt   *time.Time  `json:"issued_at,omitempty"`
	DueAt      *time.Time  `json:"due_at,omitempty"`
	Lines      []lineDTO   `json:"lines"`
	Net        money.Cents `json:"net_cents"`
	Discount   money.Cents `json:"discount_cents"`
	Tax        money.Cents `json:"tax_cents"`
	Gross      money.Cents `json:"gross_cents"`
}

func toInvoiceDTO(inv domain.Invoice) invoiceDTO {
	t := billing.InvoiceTotals(inv)
	d := invoiceDTO{
		ID:         inv.PublicID(),
		AccountID:  domain.Account{ID: inv.AccountID}.PublicID(),
		CustomerID: domain.Customer{ID: inv.CustomerID}.PublicID(),
		Number:     inv.Number,
		Status:     string(inv.Status),
		Lines:      []lineDTO{},
		Net:        t.Net,
		Discount:   t.Discount,
		Tax:        t.Tax,
		Gross:      t.Gross,
	}
	if !inv.IssuedAt.IsZero() {
		at := inv.IssuedAt
		d.IssuedAt = &at
	}
	if !inv.DueAt.IsZero() {
		at := inv.DueAt
		d.DueAt = &at
	}
	for _, l := range inv.Lines {
		d.Lines = append(d.Lines, lineDTO{Description: l.Description, Quantity: l.Quantity, UnitPrice: l.UnitPrice, TaxRateBP: int64(l.TaxRate)})
	}
	return d
}

type balanceDTO struct {
	AccountID string      `json:"account_id"`
	Invoiced  money.Cents `json:"invoiced_cents"`
	Paid      money.Cents `json:"paid_cents"`
	Due       money.Cents `json:"due_cents"`
	AsOf      time.Time   `json:"as_of"`
}

type ledgerEntryDTO struct {
	At     time.Time   `json:"at"`
	Kind   string      `json:"kind"`
	Ref    string      `json:"ref"`
	Amount money.Cents `json:"amount_cents"`
}

type paymentDTO struct {
	ID         string      `json:"id"`
	AccountID  string      `json:"account_id"`
	Amount     money.Cents `json:"amount_cents"`
	ReceivedAt time.Time   `json:"received_at"`
	Reference  string      `json:"reference"`
}

func toPaymentDTO(p domain.Payment) paymentDTO {
	return paymentDTO{ID: p.PublicID(), AccountID: domain.Account{ID: p.AccountID}.PublicID(), Amount: p.Amount,
		ReceivedAt: p.ReceivedAt, Reference: p.Reference}
}
