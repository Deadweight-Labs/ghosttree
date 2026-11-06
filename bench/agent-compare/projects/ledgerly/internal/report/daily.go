// Package report builds the numbers the finance team looks at.
package report

import (
	"sort"
	"time"

	"github.com/acme-billing/ledgerly/internal/billing"
	"github.com/acme-billing/ledgerly/internal/domain"
	"github.com/acme-billing/ledgerly/internal/money"
)

// Day is one row of the daily report.
type Day struct {
	Date     string // YYYY-MM-DD
	Invoices int
	Gross    money.Cents
}

// Daily groups issued and paid invoices by the day they were issued.
func Daily(invoices []domain.Invoice) []Day {
	byDay := map[string]*Day{}
	for _, inv := range invoices {
		if inv.Status != domain.InvoiceIssued && inv.Status != domain.InvoicePaid {
			continue
		}
		key := inv.IssuedAt.UTC().Format("2006-01-02")
		d, ok := byDay[key]
		if !ok {
			d = &Day{Date: key}
			byDay[key] = d
		}
		d.Invoices++
		d.Gross += billing.InvoiceTotals(inv).Gross
	}
	out := make([]Day, 0, len(byDay))
	for _, d := range byDay {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}

// DayRange returns the start and end of the day containing t.
func DayRange(t time.Time) (time.Time, time.Time) {
	t = t.UTC()
	start := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 0, 1)
}
