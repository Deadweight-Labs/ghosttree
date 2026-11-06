package billing

import (
	"testing"
	"time"

	"github.com/acme-billing/ledgerly/internal/domain"
	"github.com/acme-billing/ledgerly/internal/money"
	"github.com/acme-billing/ledgerly/internal/store"
)

func TestInvoiceTotalsMixedRates(t *testing.T) {
	inv := domain.Invoice{Lines: []domain.Line{
		{Quantity: 1, UnitPrice: 1900, TaxRate: domain.VATStandard},
		{Quantity: 1, UnitPrice: 2499, TaxRate: domain.VATReduced},
	}}
	got := InvoiceTotals(inv)
	if got.Net != 4399 || got.Tax != 361+175 || got.Gross != 4399+536 {
		t.Fatalf("got %+v", got)
	}
}

func TestInvoiceTotalsWithDiscount(t *testing.T) {
	inv := domain.Invoice{Discount: 1000, Lines: []domain.Line{
		{Quantity: 2, UnitPrice: 5000, TaxRate: domain.VATStandard},
	}}
	got := InvoiceTotals(inv)
	if got.Net != 10000 || got.Tax != 1710 || got.Gross != 10710 {
		t.Fatalf("got %+v", got)
	}
}

func TestAccountBalance(t *testing.T) {
	s := store.New()
	c := s.AddCustomer(domain.Customer{Name: "x"})
	a, _ := s.AddAccount(domain.Account{CustomerID: c.ID})
	_, _ = s.AddInvoice(domain.Invoice{AccountID: a.ID, Status: domain.InvoiceIssued, IssuedAt: time.Now(),
		Lines: []domain.Line{{Quantity: 1, UnitPrice: 10000, TaxRate: domain.VATStandard}}})
	_, _ = s.AddInvoice(domain.Invoice{AccountID: a.ID, Status: domain.InvoiceDraft,
		Lines: []domain.Line{{Quantity: 1, UnitPrice: 999999, TaxRate: domain.VATStandard}}})
	_, _ = s.AddPayment(domain.Payment{AccountID: a.ID, Amount: 5000})
	b, err := AccountBalance(s, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if b.Invoiced != 11900 || b.Paid != 5000 || b.Due != money.Cents(6900) {
		t.Fatalf("got %+v", b)
	}
}
