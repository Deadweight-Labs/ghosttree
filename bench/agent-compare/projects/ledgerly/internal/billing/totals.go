// Package billing computes invoice amounts and balances.
package billing

import (
	"sort"

	"github.com/acme-billing/ledgerly/internal/domain"
	"github.com/acme-billing/ledgerly/internal/money"
)

// Totals are the computed amounts of an invoice.
type Totals struct {
	Net      money.Cents // sum of line nets before discount
	Discount money.Cents
	Tax      money.Cents
	Gross    money.Cents // Net - Discount + Tax
	// TaxByRate breaks the tax down per VAT rate, as printed on the invoice.
	TaxByRate map[domain.TaxRate]money.Cents
}

// InvoiceTotals computes net, tax and gross of an invoice. Tax is computed
// per rate on the summed net of that rate, as required for German invoices
// (§14 UStG), not per line.
func InvoiceTotals(inv domain.Invoice) Totals {
	netByRate := map[domain.TaxRate]money.Cents{}
	var net money.Cents
	for _, l := range inv.Lines {
		netByRate[l.TaxRate] += l.Net()
		net += l.Net()
	}
	t := Totals{Net: net, Discount: inv.Discount, TaxByRate: map[domain.TaxRate]money.Cents{}}
	rates := make([]domain.TaxRate, 0, len(netByRate))
	for r := range netByRate {
		rates = append(rates, r)
	}
	sort.Slice(rates, func(i, j int) bool { return rates[i] < rates[j] })
	remainingDiscount := inv.Discount
	for i, r := range rates {
		base := netByRate[r]
		// Spread the discount over the rates proportionally; the last rate
		// takes the remainder so the parts add up exactly.
		var share money.Cents
		if net != 0 {
			if i == len(rates)-1 {
				share = remainingDiscount
			} else {
				share = money.RoundBankers(int64(inv.Discount)*int64(base), int64(net))
				remainingDiscount -= share
			}
		}
		tax := money.RoundBankers(int64(base-share)*int64(r), 10000)
		t.TaxByRate[r] = tax
		t.Tax += tax
	}
	t.Gross = t.Net - t.Discount + t.Tax
	return t
}

// Outstanding is the gross amount of an invoice that counts against the
// account balance. Drafts and void invoices count as zero.
func Outstanding(inv domain.Invoice) money.Cents {
	switch inv.Status {
	case domain.InvoiceIssued, domain.InvoicePaid:
		return InvoiceTotals(inv).Gross
	default:
		return 0
	}
}
