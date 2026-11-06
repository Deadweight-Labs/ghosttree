// Package store is the persistence layer. Production runs on PostgreSQL via
// the schema in migrations/; this in-memory implementation backs the API in
// tests and in the local dev server and mirrors the same semantics.
package store

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/acme-billing/ledgerly/internal/domain"
	"github.com/acme-billing/ledgerly/internal/money"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("store: not found")

// Store is a concurrency-safe in-memory store.
type Store struct {
	mu        sync.RWMutex
	nextID    int64
	customers map[int64]domain.Customer
	accounts  map[int64]domain.Account
	invoices  map[int64]domain.Invoice
	payments  map[int64]domain.Payment
	now       func() time.Time
}

// New returns an empty store.
func New() *Store {
	return &Store{
		customers: map[int64]domain.Customer{},
		accounts:  map[int64]domain.Account{},
		invoices:  map[int64]domain.Invoice{},
		payments:  map[int64]domain.Payment{},
		now:       time.Now,
	}
}

// SetClock replaces the clock, for tests.
func (s *Store) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

func (s *Store) id() int64 {
	s.nextID++
	return s.nextID
}

// AddCustomer inserts a customer and returns it with its ID.
func (s *Store) AddCustomer(c domain.Customer) domain.Customer {
	s.mu.Lock()
	defer s.mu.Unlock()
	c.ID = s.id()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = s.now()
	}
	s.customers[c.ID] = c
	return c
}

// Customer returns a customer by ID.
func (s *Store) Customer(id int64) (domain.Customer, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.customers[id]
	if !ok {
		return domain.Customer{}, ErrNotFound
	}
	return c, nil
}

// Customers lists all customers ordered by ID.
func (s *Store) Customers() []domain.Customer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Customer, 0, len(s.customers))
	for _, c := range s.customers {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// AddAccount inserts an account.
func (s *Store) AddAccount(a domain.Account) (domain.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.customers[a.CustomerID]; !ok {
		return domain.Account{}, ErrNotFound
	}
	a.ID = s.id()
	if a.Status == "" {
		a.Status = domain.AccountActive
	}
	if a.OpenedAt.IsZero() {
		a.OpenedAt = s.now()
	}
	s.accounts[a.ID] = a
	return a, nil
}

// Account returns an account by ID.
func (s *Store) Account(id int64) (domain.Account, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.accounts[id]
	if !ok {
		return domain.Account{}, ErrNotFound
	}
	return a, nil
}

// Accounts lists all accounts ordered by ID.
func (s *Store) Accounts() []domain.Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Account, 0, len(s.accounts))
	for _, a := range s.accounts {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// AccountsOf lists the accounts of a customer.
func (s *Store) AccountsOf(customerID int64) []domain.Account {
	var out []domain.Account
	for _, a := range s.Accounts() {
		if a.CustomerID == customerID {
			out = append(out, a)
		}
	}
	return out
}

// AddInvoice inserts an invoice. CustomerID is taken from the account.
func (s *Store) AddInvoice(inv domain.Invoice) (domain.Invoice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	acc, ok := s.accounts[inv.AccountID]
	if !ok {
		return domain.Invoice{}, ErrNotFound
	}
	inv.ID = s.id()
	inv.CustomerID = acc.CustomerID
	if inv.Status == "" {
		inv.Status = domain.InvoiceDraft
	}
	s.invoices[inv.ID] = inv
	return inv, nil
}

// UpdateInvoice replaces an existing invoice.
func (s *Store) UpdateInvoice(inv domain.Invoice) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.invoices[inv.ID]; !ok {
		return ErrNotFound
	}
	s.invoices[inv.ID] = inv
	return nil
}

// Invoice returns an invoice by ID.
func (s *Store) Invoice(id int64) (domain.Invoice, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	inv, ok := s.invoices[id]
	if !ok {
		return domain.Invoice{}, ErrNotFound
	}
	return inv, nil
}

// Invoices lists all invoices ordered by ID.
func (s *Store) Invoices() []domain.Invoice {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Invoice, 0, len(s.invoices))
	for _, inv := range s.invoices {
		out = append(out, inv)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// InvoicesOf lists the invoices of an account ordered by ID.
func (s *Store) InvoicesOf(accountID int64) []domain.Invoice {
	var out []domain.Invoice
	for _, inv := range s.Invoices() {
		if inv.AccountID == accountID {
			out = append(out, inv)
		}
	}
	return out
}

// AddPayment records a payment.
func (s *Store) AddPayment(p domain.Payment) (domain.Payment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accounts[p.AccountID]; !ok {
		return domain.Payment{}, ErrNotFound
	}
	if p.Amount <= 0 {
		return domain.Payment{}, errors.New("store: payment amount must be positive")
	}
	p.ID = s.id()
	if p.ReceivedAt.IsZero() {
		p.ReceivedAt = s.now()
	}
	s.payments[p.ID] = p
	return p, nil
}

// PaymentsOf lists the payments of an account ordered by ID.
func (s *Store) PaymentsOf(accountID int64) []domain.Payment {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Payment
	for _, p := range s.payments {
		if p.AccountID == accountID {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// SumPayments adds up the payments of an account.
func (s *Store) SumPayments(accountID int64) money.Cents {
	var total money.Cents
	for _, p := range s.PaymentsOf(accountID) {
		total += p.Amount
	}
	return total
}
