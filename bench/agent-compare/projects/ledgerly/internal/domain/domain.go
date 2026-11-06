// Package domain contains ledgerly's core entities.
package domain

import (
	"encoding/base32"
	"encoding/binary"
	"strings"
	"time"

	"github.com/acme-billing/ledgerly/internal/money"
)

var idEncoding = base32.NewEncoding("abcdefghijkmnpqrstuvwxyz23456789").WithPadding(base32.NoPadding)

// publicID derives the stable external identifier for an internal row ID.
func publicID(prefix string, id int64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(id)^0x5bd1e9955bd1e995)
	return prefix + "_" + strings.TrimLeft(idEncoding.EncodeToString(buf[:]), "a")
}

// ParsePublicID reverses publicID. ok is false for malformed input or a
// prefix mismatch.
func ParsePublicID(prefix, s string) (int64, bool) {
	if !strings.HasPrefix(s, prefix+"_") {
		return 0, false
	}
	body := strings.TrimPrefix(s, prefix+"_")
	for len(body) < 13 {
		body = "a" + body
	}
	raw, err := idEncoding.DecodeString(body)
	if err != nil || len(raw) != 8 {
		return 0, false
	}
	return int64(binary.BigEndian.Uint64(raw) ^ 0x5bd1e9955bd1e995), true
}

// Customer is a paying party.
type Customer struct {
	ID        int64
	Name      string
	Email     string
	Country   string // ISO 3166-1 alpha-2
	Business  bool
	CreatedAt time.Time
}

// PublicID is the identifier exposed to clients.
func (c Customer) PublicID() string { return publicID("cus", c.ID) }

// AccountStatus is the lifecycle state of an account.
type AccountStatus string

const (
	AccountActive    AccountStatus = "active"
	AccountSuspended AccountStatus = "suspended"
	AccountClosed    AccountStatus = "closed"
)

// Account is a billing account of a customer. A customer may have several.
type Account struct {
	ID         int64
	CustomerID int64
	Plan       string
	Status     AccountStatus
	OpenedAt   time.Time
}

// PublicID is the identifier exposed to clients.
func (a Account) PublicID() string { return publicID("acc", a.ID) }

// InvoiceStatus is the lifecycle state of an invoice.
type InvoiceStatus string

const (
	InvoiceDraft  InvoiceStatus = "draft"
	InvoiceIssued InvoiceStatus = "issued"
	InvoicePaid   InvoiceStatus = "paid"
	InvoiceVoid   InvoiceStatus = "void"
)

// TaxRate is a VAT rate in basis points.
type TaxRate = money.BasisPoints

const (
	VATStandard TaxRate = 1900
	VATReduced  TaxRate = 700
	VATZero     TaxRate = 0
)

// Line is a single invoice position.
type Line struct {
	Description string
	Quantity    int64
	UnitPrice   money.Cents // net
	TaxRate     TaxRate
}

// Net is quantity times unit price.
func (l Line) Net() money.Cents { return money.Cents(l.Quantity) * l.UnitPrice }

// Invoice is a bill sent to a customer for one account.
type Invoice struct {
	ID         int64
	AccountID  int64
	CustomerID int64
	Number     string // human readable, e.g. "2026-000123"
	Status     InvoiceStatus
	IssuedAt   time.Time
	DueAt      time.Time
	Lines      []Line
	// Discount applied to the net amount, in cents. Filled by billing.
	Discount money.Cents
}

// PublicID is the identifier exposed to clients.
func (i Invoice) PublicID() string { return publicID("inv", i.ID) }

// Payment records money received for an account.
type Payment struct {
	ID         int64
	AccountID  int64
	Amount     money.Cents
	ReceivedAt time.Time
	Reference  string
}

// PublicID is the identifier exposed to clients.
func (p Payment) PublicID() string { return publicID("pay", p.ID) }

// LedgerEntry is one booking on an account: invoices debit, payments credit.
type LedgerEntry struct {
	At     time.Time
	Kind   string // "invoice" or "payment"
	Ref    string // public ID of the invoice or payment
	Amount money.Cents
}
