package refunds

import "testing"

func TestPartial(t *testing.T) {
	if r := Partial(119.00, 50); r.Amount != 59.5 {
		t.Fatalf("got %v", r.Amount)
	}
}

func TestExportLine(t *testing.T) {
	if got := ExportLine(Refund{InvoiceNumber: "2026-000101", Amount: 12.5, Reason: "Kulanz"}); got != "2026-000101;12.50;Kulanz" {
		t.Fatal(got)
	}
}
