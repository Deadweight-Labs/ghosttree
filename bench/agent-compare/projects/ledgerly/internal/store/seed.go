package store

import (
	"time"

	"github.com/acme-billing/ledgerly/internal/domain"
	"github.com/acme-billing/ledgerly/internal/money"
)

// Seed fills a store with a small, deterministic data set for local
// development and API tests.
func Seed(s *Store) {
	berlin := time.FixedZone("CET", 3600)
	c1 := s.AddCustomer(domain.Customer{Name: "Bäckerei Krume GmbH", Email: "buchhaltung@krume.example", Country: "DE", Business: true,
		CreatedAt: time.Date(2025, 11, 3, 9, 0, 0, 0, berlin)})
	c2 := s.AddCustomer(domain.Customer{Name: "Jana Vogt", Email: "jana@vogt.example", Country: "DE",
		CreatedAt: time.Date(2026, 1, 14, 18, 30, 0, 0, berlin)})
	c3 := s.AddCustomer(domain.Customer{Name: "Studio Lumen BV", Email: "finance@lumen.example", Country: "NL", Business: true,
		CreatedAt: time.Date(2026, 2, 2, 11, 0, 0, 0, berlin)})

	a1, _ := s.AddAccount(domain.Account{CustomerID: c1.ID, Plan: "business-m", OpenedAt: time.Date(2025, 11, 3, 9, 5, 0, 0, berlin)})
	a2, _ := s.AddAccount(domain.Account{CustomerID: c2.ID, Plan: "solo", OpenedAt: time.Date(2026, 1, 14, 18, 35, 0, 0, berlin)})
	a3, _ := s.AddAccount(domain.Account{CustomerID: c3.ID, Plan: "business-l", OpenedAt: time.Date(2026, 2, 2, 11, 10, 0, 0, berlin)})
	_, _ = s.AddAccount(domain.Account{CustomerID: c1.ID, Plan: "addon-archive", Status: domain.AccountSuspended})

	add := func(acc domain.Account, number string, issued time.Time, status domain.InvoiceStatus, lines ...domain.Line) {
		_, _ = s.AddInvoice(domain.Invoice{AccountID: acc.ID, Number: number, Status: status, IssuedAt: issued,
			DueAt: issued.AddDate(0, 0, 14), Lines: lines})
	}
	add(a1, "2026-000101", time.Date(2026, 1, 1, 6, 0, 0, 0, time.UTC), domain.InvoicePaid,
		domain.Line{Description: "Plan business-m, Januar", Quantity: 1, UnitPrice: money.FromEuros(149, 0), TaxRate: domain.VATStandard},
		domain.Line{Description: "Zusatznutzer", Quantity: 3, UnitPrice: money.FromEuros(9, 90), TaxRate: domain.VATStandard})
	add(a1, "2026-000134", time.Date(2026, 2, 1, 6, 0, 0, 0, time.UTC), domain.InvoiceIssued,
		domain.Line{Description: "Plan business-m, Februar", Quantity: 1, UnitPrice: money.FromEuros(149, 0), TaxRate: domain.VATStandard})
	add(a2, "2026-000140", time.Date(2026, 2, 1, 6, 0, 0, 0, time.UTC), domain.InvoiceIssued,
		domain.Line{Description: "Plan solo, Februar", Quantity: 1, UnitPrice: money.FromEuros(19, 0), TaxRate: domain.VATStandard},
		domain.Line{Description: "Fachbuch-Paket", Quantity: 1, UnitPrice: money.FromEuros(24, 99), TaxRate: domain.VATReduced})
	add(a3, "2026-000141", time.Date(2026, 2, 28, 23, 15, 0, 0, time.UTC), domain.InvoiceDraft,
		domain.Line{Description: "Plan business-l, März", Quantity: 1, UnitPrice: money.FromEuros(499, 0), TaxRate: domain.VATZero})

	_, _ = s.AddPayment(domain.Payment{AccountID: a1.ID, Amount: 21265, ReceivedAt: time.Date(2026, 1, 9, 10, 0, 0, 0, berlin), Reference: "SEPA 88123"})
	_, _ = s.AddPayment(domain.Payment{AccountID: a2.ID, Amount: 1000, ReceivedAt: time.Date(2026, 2, 5, 8, 0, 0, 0, berlin), Reference: "PayPal 5KX"})
}
