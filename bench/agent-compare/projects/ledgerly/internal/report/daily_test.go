package report

import (
	"testing"
	"time"

	"github.com/acme-billing/ledgerly/internal/domain"
)

func TestDaily(t *testing.T) {
	line := []domain.Line{{Quantity: 1, UnitPrice: 1000, TaxRate: domain.VATStandard}}
	invs := []domain.Invoice{
		{Status: domain.InvoiceIssued, IssuedAt: time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC), Lines: line},
		{Status: domain.InvoicePaid, IssuedAt: time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC), Lines: line},
		{Status: domain.InvoiceDraft, IssuedAt: time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC), Lines: line},
		{Status: domain.InvoiceIssued, IssuedAt: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC), Lines: line},
	}
	got := Daily(invs)
	if len(got) != 2 || got[0].Date != "2026-03-01" || got[1].Invoices != 2 || got[1].Gross != 2380 {
		t.Fatalf("got %+v", got)
	}
}
