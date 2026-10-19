package store

import (
	"fmt"
	"strconv"
	"strings"
)

// The gardener (REQ-457) writes notes that a person confirms with one click, and
// two of those clicks change other entries (merge duplicates, record a regression
// verdict). What such a note says about its target comes from its session
// reference and its text, so who may write a note that claims to be the
// gardener's is a security question: a note that only LOOKS like the gardener's
// would make the review page act on a target the card does not show.
//
// The gardener is an account NUMBER, set by whoever has the database of the
// server in their hands (`ctx account set-gardener`), kept in org_state, and read
// whenever it is asked. It is never an account name: a name is what a person may
// change for themselves (SetOwnName, the name the identity provider reports), so
// whoever could take the name would take the role. The number of an account never
// changes and is never given out again, so renaming the gardener's account does
// not take the role away, and renaming another account to the old name gives it
// nothing. Nothing reachable over HTTP writes this setting.
//
// Without a gardener account nobody may write harness "gardener" or a "gardener:"
// reference (the API refuses it), and the review page acts on no note of the
// gardener's.
const (
	// GardenerHarness is the harness of every entry the gardener writes.
	GardenerHarness = "gardener"
	// GardenerRefPrefix starts the session reference of a gardener note; what
	// follows names the kind and the entries it is about.
	GardenerRefPrefix = "gardener:"

	// gardenerAccountKey is the org_state key that holds the account number.
	gardenerAccountKey = "gardener_account"
)

// GardenerAccountID is the number of the account the gardener runs as (the n of
// "person:n"), 0 when none is set.
func (s *Store) GardenerAccountID() int64 {
	if s.reader != nil {
		return s.reader.GardenerAccountID()
	}
	var v string
	if s.db.QueryRow(`SELECT value FROM org_state WHERE key=?`, gardenerAccountKey).Scan(&v) != nil {
		return 0
	}
	id, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || id < 1 {
		return 0
	}
	return id
}

// GardenerAccount is the account the gardener runs as, as it is named now. The
// name is for people to read; nothing decides by it.
func (s *Store) GardenerAccount() (Account, bool) {
	if s.reader != nil {
		return s.reader.GardenerAccount()
	}
	id := s.GardenerAccountID()
	if id == 0 {
		return Account{}, false
	}
	a, err := s.AccountByPrincipalID(fmt.Sprintf("person:%d", id))
	return a, err == nil
}

// SetGardenerAccount makes the account with this number the gardener's. The
// account has to exist and be active; the number is what is stored.
func (s *Store) SetGardenerAccount(id int64) error {
	if s.writer != nil {
		return queueWrite(s, []any{id}, func(d *Store, p []any) error { return d.SetGardenerAccount(p[0].(int64)) })
	}
	a, err := s.AccountByPrincipalID(fmt.Sprintf("person:%d", id))
	if err != nil {
		return fmt.Errorf("%w: no account with the number %d", ErrInvalidInput, id)
	}
	if a.State != "active" {
		return fmt.Errorf("%w: the account %s is %s, not active", ErrInvalidInput, a.Name, a.State)
	}
	_, err = s.db.Exec(`INSERT INTO org_state(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, gardenerAccountKey, strconv.FormatInt(id, 10))
	return err
}

// ClearGardenerAccount takes the role away: nobody is the gardener afterwards.
func (s *Store) ClearGardenerAccount() error {
	if s.writer != nil {
		return queueWrite(s, nil, func(d *Store, _ []any) error { return d.ClearGardenerAccount() })
	}
	_, err := s.db.Exec(`DELETE FROM org_state WHERE key=?`, gardenerAccountKey)
	return err
}

// IsGardenerPrincipal says whether the caller is the gardener's account with a
// token. A browser session of that account is not: nothing the gardener writes
// comes from one.
func (s *Store) IsGardenerPrincipal(p Principal) bool {
	if s.reader != nil {
		return s.reader.IsGardenerPrincipal(p)
	}
	want := s.GardenerAccountID()
	if want == 0 || p.TokenKind == WebSessionKind {
		return false
	}
	got, ok := accountNumericID(p.ID)
	return ok && got == want
}

// ReservedForGardener says whether a new entry claims to be the gardener's:
// harness "gardener" or a "gardener:" session reference, in any spelling of case
// or surrounding space.
func ReservedForGardener(k Knowledge) bool {
	return strings.EqualFold(strings.TrimSpace(k.Harness), GardenerHarness) ||
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(k.SessionRef)), GardenerRefPrefix)
}

// WrittenByGardener says whether the account that wrote the entry is the
// gardener's. The author is the account number stored when the entry was
// written (knowledge_authors), never a name or a label the writer sent along; an
// entry without an author (legacy, or one that fitted two accounts) is nobody's.
func (s *Store) WrittenByGardener(k Knowledge) bool {
	if s.reader != nil {
		return s.reader.WrittenByGardener(k)
	}
	want := s.GardenerAccountID()
	return want != 0 && k.ID != 0 && s.knowledgeAuthorID(k.ID) == want
}
