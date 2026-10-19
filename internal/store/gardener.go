package store

import (
	"strings"
)

// The gardener (REQ-457) writes notes that a person confirms with one click, and
// two of those clicks change other entries (merge duplicates, record a regression
// verdict). What such a note says about its target comes from its session
// reference and its text, so who may write a note that claims to be the
// gardener's is a security question: a note that only LOOKS like the gardener's
// would make the review page act on a target the card does not show.
//
// The gardener's account is named by the server (GHOSTTREE_GARDENER_ACCOUNT).
// The name is looked up whenever it is asked, so it is the account that holds the
// name now, and an account that was renamed stops being the gardener's. Nobody
// else may write harness "gardener" or a "gardener:" reference (the API refuses
// it), and the review page only acts on a note the gardener's account wrote.
const (
	// GardenerHarness is the harness of every entry the gardener writes.
	GardenerHarness = "gardener"
	// GardenerRefPrefix starts the session reference of a gardener note; what
	// follows names the kind and the entries it is about.
	GardenerRefPrefix = "gardener:"
)

// SetGardenerAccount names the account the gardener runs as; empty names none,
// and then nobody may write gardener entries. It is shared by the store and its
// readers.
func (s *Store) SetGardenerAccount(name string) {
	n := NormalizeAccountName(strings.TrimSpace(name))
	s.accessCfg().gardener.Store(&n)
}

// GardenerAccountName is the account name set with SetGardenerAccount.
func (s *Store) GardenerAccountName() string {
	if n := s.accessCfg().gardener.Load(); n != nil {
		return *n
	}
	return ""
}

// GardenerAccountID is the number of the account that holds the gardener's name
// now (the n of "person:n"), 0 when none is set or no account has the name.
func (s *Store) GardenerAccountID() int64 {
	name := s.GardenerAccountName()
	if name == "" {
		return 0
	}
	acct, err := s.AccountByName(name)
	if err != nil {
		return 0
	}
	id, ok := accountNumericID(acct.ID)
	if !ok {
		return 0
	}
	return id
}

// IsGardenerPrincipal says whether the caller is the gardener's account with a
// token. A browser session of that account is not: nothing the gardener writes
// comes from one.
func (s *Store) IsGardenerPrincipal(p Principal) bool {
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
// gardener's now. The author is the account number stored when the entry was
// written (knowledge_authors), never a name or a label the writer sent along;
// an entry without an author (legacy, or one that fitted two accounts) is nobody's.
func (s *Store) WrittenByGardener(k Knowledge) bool {
	want := s.GardenerAccountID()
	return want != 0 && k.ID != 0 && s.knowledgeAuthorID(k.ID) == want
}
