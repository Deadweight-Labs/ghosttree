// Package refunds is the refund calculation carried over from the PHP
// system. It is still used by the nightly refund export and will be replaced.
package refunds

import (
	"fmt"
	"math"
)

// Refund is a refund request as the export file describes it.
type Refund struct {
	InvoiceNumber string
	Amount        float64 // EUR
	Reason        string
}

// Partial computes a partial refund of an invoice amount by percentage.
func Partial(invoiceAmount float64, percent float64) Refund {
	amt := math.Round(invoiceAmount*percent) / 100
	return Refund{Amount: amt}
}

// ExportLine renders a refund for the nightly export file.
func ExportLine(r Refund) string {
	return fmt.Sprintf("%s;%.2f;%s", r.InvoiceNumber, r.Amount, r.Reason)
}
