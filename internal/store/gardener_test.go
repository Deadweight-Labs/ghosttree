package store

import (
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func TestReservedForGardenerCatchesEveryClaimToBeTheGardeners(t *testing.T) {
	for name, k := range map[string]Knowledge{
		"harness":              {Harness: "gardener"},
		"harness, other case":  {Harness: "Gardener"},
		"harness with space":   {Harness: "  gardener\t"},
		"reference":            {SessionRef: "gardener:regression:12:existing"},
		"reference, upper":     {SessionRef: "GARDENER:relation:duplicate:1-2"},
		"reference with space": {SessionRef: " gardener:check-knowledge:4"},
		"both":                 {Harness: "gardener", SessionRef: "gardener:x"},
	} {
		if !ReservedForGardener(k) {
			t.Errorf("%s is not reserved", name)
		}
	}
	for name, k := range map[string]Knowledge{
		"nothing":                   {},
		"another harness":           {Harness: "claude"},
		"a harness that continues":  {Harness: "gardener-bot"},
		"a reference without colon": {SessionRef: "gardener"},
		"a session reference":       {SessionRef: "session:12#3"},
		"a word in the title":       {Title: "gardener", Body: "gardener:"},
	} {
		if ReservedForGardener(k) {
			t.Errorf("%s is reserved", name)
		}
	}
}

func TestTheGardenersAccountIsTheOneThatHoldsTheNameNow(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, n := range []string{"alice", "gardener", "mallory"} {
		if _, err := st.AddPerson(n); err != nil {
			t.Fatal(err)
		}
	}
	gardener := Principal{ID: "person:2", Label: "gardener", TokenKind: "token"}

	// Nothing set: nobody is the gardener, not even the account of that name.
	if st.GardenerAccountID() != 0 || st.IsGardenerPrincipal(gardener) {
		t.Fatal("an account is the gardener's without being named")
	}
	st.SetGardenerAccount(" gardener ")
	if st.GardenerAccountName() != "gardener" || st.GardenerAccountID() != 2 {
		t.Fatalf("name %q id %d", st.GardenerAccountName(), st.GardenerAccountID())
	}
	if !st.IsGardenerPrincipal(gardener) {
		t.Error("the account of that name is not the gardener")
	}
	for name, p := range map[string]Principal{
		"another account":                  {ID: "person:1", Label: "alice", TokenKind: "token"},
		"mallory with the label gardener":  {ID: "person:3", Label: "gardener", TokenKind: "token"},
		"the gardener in a browser":        {ID: "person:2", Label: "gardener", TokenKind: WebSessionKind},
		"no principal":                     {},
		"an id that is not an account":     {ID: "gardener", Label: "gardener"},
		"an agent id with the same number": {ID: "agent:2", Label: "gardener"},
	} {
		if st.IsGardenerPrincipal(p) {
			t.Errorf("%s is the gardener", name)
		}
	}
	// A name that is not an account, and the name given back to nobody.
	st.SetGardenerAccount("nobody")
	if st.GardenerAccountID() != 0 || st.IsGardenerPrincipal(gardener) {
		t.Error("an unknown name names an account")
	}
	st.SetGardenerAccount("")
	if st.GardenerAccountID() != 0 || st.IsGardenerPrincipal(gardener) {
		t.Error("an empty name names an account")
	}
	// An account that is renamed is not the gardener any more; the name goes to whoever holds it.
	st.SetGardenerAccount("gardener")
	if _, err := st.DB().Exec(`UPDATE persons SET name='gardener-old' WHERE id=2`); err != nil {
		t.Fatal(err)
	}
	if st.IsGardenerPrincipal(gardener) {
		t.Error("a renamed account is still the gardener")
	}
	if _, err := st.DB().Exec(`UPDATE persons SET name='gardener' WHERE id=3`); err != nil {
		t.Fatal(err)
	}
	if !st.IsGardenerPrincipal(Principal{ID: "person:3", TokenKind: "token"}) || st.IsGardenerPrincipal(gardener) {
		t.Error("the name does not follow the account that holds it")
	}
}

func TestAnEntryIsTheGardenersWhenTheGardenersAccountWroteIt(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, n := range []string{"alice", "gardener"} {
		if _, err := st.AddPerson(n); err != nil {
			t.Fatal(err)
		}
	}
	st.SetGardenerAccount("gardener")
	write := func(author int64, person string) Knowledge {
		id, err := st.InsertKnowledge(Knowledge{Type: "note", Title: "t", Body: "b", Scope: scope.Axes{Project: "github.com/o/r"}, Person: person, AuthorID: author, Harness: "gardener", SessionRef: "gardener:regression:1:existing"})
		if err != nil {
			t.Fatal(err)
		}
		k, err := st.KnowledgeByID(id)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	if !st.WrittenByGardener(write(2, "gardener")) {
		t.Error("an entry of the gardener's account is not the gardener's")
	}
	// The label a writer sends is no proof: the author is the account number.
	if st.WrittenByGardener(write(1, "gardener")) {
		t.Error("the label gardener on an entry of another account counts")
	}
	if st.WrittenByGardener(Knowledge{}) {
		t.Error("an entry that was never written counts")
	}
	st.SetGardenerAccount("")
	if st.WrittenByGardener(write(2, "gardener")) {
		t.Error("an entry counts while no account is named")
	}
}
