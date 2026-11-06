package billing

import (
	"sort"

	"github.com/acme-billing/ledgerly/internal/domain"
	"github.com/acme-billing/ledgerly/internal/money"
	"github.com/acme-billing/ledgerly/internal/store"
)

// Balance is what a customer owes on an account. Positive means the customer
// owes money, negative means credit.
type Balance struct {
	Invoiced money.Cents
	Paid     money.Cents
	Due      money.Cents
}

// AccountBalance computes the current balance of an account.
func AccountBalance(s *store.Store, accountID int64) (Balance, error) {
	if _, err := s.Account(accountID); err != nil {
		return Balance{}, err
	}
	var b Balance
	for _, inv := range s.InvoicesOf(accountID) {
		b.Invoiced += Outstanding(inv)
	}
	b.Paid = s.SumPayments(accountID)
	b.Due = b.Invoiced - b.Paid
	return b, nil
}

// Ledger returns the bookings of an account in chronological order.
func Ledger(s *store.Store, accountID int64) ([]domain.LedgerEntry, error) {
	if _, err := s.Account(accountID); err != nil {
		return nil, err
	}
	var out []domain.LedgerEntry
	for _, inv := range s.InvoicesOf(accountID) {
		amt := Outstanding(inv)
		if amt == 0 {
			continue
		}
		out = append(out, domain.LedgerEntry{At: inv.IssuedAt, Kind: "invoice", Ref: inv.PublicID(), Amount: amt})
	}
	for _, p := range s.PaymentsOf(accountID) {
		out = append(out, domain.LedgerEntry{At: p.ReceivedAt, Kind: "payment", Ref: p.PublicID(), Amount: -p.Amount})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}
