package store

import (
	"errors"
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

func gardenerStore(t *testing.T, names ...string) *Store {
	t.Helper()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, n := range names {
		if _, err := st.AddPerson(n); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func tokenOf(id string) Principal { return Principal{ID: id, TokenKind: "token"} }

// Nothing is the gardener until an account number is set, and the setting
// takes an account that exists and is active.
func TestNobodyIsTheGardenerUntilAnAccountNumberIsSet(t *testing.T) {
	st := gardenerStore(t, "alice", "gardener", "mallory")
	if st.GardenerAccountID() != 0 {
		t.Fatal("an account is the gardener's before one was set")
	}
	if _, ok := st.GardenerAccount(); ok {
		t.Error("an account is shown as the gardener's before one was set")
	}
	for _, p := range []Principal{tokenOf("person:1"), tokenOf("person:2"), tokenOf("person:3")} {
		if st.IsGardenerPrincipal(p) {
			t.Errorf("%s is the gardener with none set", p.ID)
		}
	}
	// The account called "gardener" is not the gardener for its name.
	if err := st.SetGardenerAccount(99); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("an account that does not exist: %v", err)
	}
	if err := st.SetGardenerAccount(0); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("account 0: %v", err)
	}
	if _, err := st.DB().Exec(`UPDATE persons SET state='disabled' WHERE id=3`); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGardenerAccount(3); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("a disabled account: %v", err)
	}
	if st.GardenerAccountID() != 0 {
		t.Fatal("a refused setting was stored")
	}

	if err := st.SetGardenerAccount(2); err != nil {
		t.Fatal(err)
	}
	if a, ok := st.GardenerAccount(); !ok || st.GardenerAccountID() != 2 || a.Name != "gardener" {
		t.Fatalf("account %+v %v id %d", a, ok, st.GardenerAccountID())
	}
	if !st.IsGardenerPrincipal(tokenOf("person:2")) {
		t.Error("the account that was set is not the gardener")
	}
	for name, p := range map[string]Principal{
		"another account":                  tokenOf("person:1"),
		"the gardener in a browser":        {ID: "person:2", TokenKind: WebSessionKind},
		"no principal":                     {},
		"an id that is not an account":     tokenOf("gardener"),
		"an agent id with the same number": tokenOf("agent:2"),
		"a number written another way":     tokenOf("person:02"),
	} {
		if st.IsGardenerPrincipal(p) {
			t.Errorf("%s is the gardener", name)
		}
	}
	// Taking the role away leaves nobody.
	if err := st.ClearGardenerAccount(); err != nil {
		t.Fatal(err)
	}
	if st.GardenerAccountID() != 0 || st.IsGardenerPrincipal(tokenOf("person:2")) {
		t.Error("the gardener stayed after the role was taken away")
	}
}

// A name is what a person changes for themselves, so it must carry no right: the
// gardener's account keeps the role when it is renamed, and another account that
// takes the name gets nothing. Both are done with the functions a person has,
// SetOwnName (what the profile page calls).
func TestRenamingAccountsNeitherGivesNorTakesTheGardenersRole(t *testing.T) {
	st := gardenerStore(t, "alice", "gardener", "mallory")
	if err := st.SetGardenerAccount(2); err != nil {
		t.Fatal(err)
	}
	gardener, mallory := tokenOf("person:2"), tokenOf("person:3")

	// The gardener's account is renamed (by itself, the way any account may rename itself):
	// it is still the gardener.
	if _, err := st.SetOwnName("person:2", "garden-keeper"); err != nil {
		t.Fatal(err)
	}
	if !st.IsGardenerPrincipal(gardener) || st.GardenerAccountID() != 2 {
		t.Error("renaming the gardener's account took the role away")
	}
	if a, ok := st.GardenerAccount(); !ok || a.Name != "garden-keeper" {
		t.Errorf("the account is shown as %+v", a)
	}
	// The store keeps an old name for the account that held it, so another account
	// cannot take it with SetOwnName at all.
	if _, err := st.SetOwnName("person:3", "gardener"); err == nil {
		t.Error("another account took the name the gardener's account had")
	}
	// The role does not rest on that: should a name get to another account by any
	// other way (the name the identity provider reports, an administrator's tool, a
	// future mistake), it gives nothing, and the gardener keeps the role.
	for _, q := range []string{
		`UPDATE persons SET name='garden-keeper-2' WHERE id=2`,
		`UPDATE persons SET name='gardener' WHERE id=3`,
	} {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if a, _ := st.AccountByName("gardener"); a.ID != "person:3" {
		t.Fatalf("the test did not move the name: %+v", a)
	}
	if st.IsGardenerPrincipal(mallory) {
		t.Fatal("an account that holds the gardener's name became the gardener")
	}
	if !st.IsGardenerPrincipal(gardener) || st.GardenerAccountID() != 2 {
		t.Error("the gardener lost the role to the account that holds its old name")
	}
}

func TestAnEntryIsTheGardenersWhenTheGardenersAccountWroteIt(t *testing.T) {
	st := gardenerStore(t, "alice", "gardener")
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
	if st.WrittenByGardener(write(2, "gardener")) {
		t.Error("an entry counts while no account is set")
	}
	if err := st.SetGardenerAccount(2); err != nil {
		t.Fatal(err)
	}
	mine, labelled := write(2, "gardener"), write(1, "gardener")
	if !st.WrittenByGardener(mine) {
		t.Error("an entry of the gardener's account is not the gardener's")
	}
	// The label a writer sends is no proof: the author is the account number.
	if st.WrittenByGardener(labelled) {
		t.Error("the label gardener on an entry of another account counts")
	}
	if st.WrittenByGardener(Knowledge{}) {
		t.Error("an entry that was never written counts")
	}
	// Another account made the gardener's: the earlier entries are not its own any more.
	if err := st.SetGardenerAccount(1); err != nil {
		t.Fatal(err)
	}
	if st.WrittenByGardener(mine) || !st.WrittenByGardener(labelled) {
		t.Error("the role did not move with the number")
	}
	if err := st.ClearGardenerAccount(); err != nil {
		t.Fatal(err)
	}
	if st.WrittenByGardener(mine) || st.WrittenByGardener(labelled) {
		t.Error("an entry counts after the role was taken away")
	}
}

// The server runs the store with a writer and a read-only pool: the setting goes
// through the writer, and every read, from either side, sees what was set. A
// second process (ctx account set-gardener on the host) writes the same file.
func TestGardenerAccountWorksWithTheRuntimeWriterAndFromAnotherProcess(t *testing.T) {
	path := t.TempDir() + "/g.db"
	st, err := OpenRuntime(path, DefaultWriterConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, n := range []string{"alice", "gardener"} {
		if _, err := st.AddPerson(n); err != nil {
			t.Fatal(err)
		}
	}
	if st.GardenerAccountID() != 0 {
		t.Fatal("set before anything was")
	}
	if err := st.SetGardenerAccount(2); err != nil {
		t.Fatal(err)
	}
	if st.GardenerAccountID() != 2 || !st.IsGardenerPrincipal(tokenOf("person:2")) {
		t.Error("the setting through the writer is not seen")
	}
	if err := st.SetGardenerAccount(77); err == nil {
		t.Error("an account that does not exist was set through the writer")
	}
	if st.GardenerAccountID() != 2 {
		t.Error("a refused setting changed the stored one")
	}

	// The command line opens the file itself, beside the running server.
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.SetGardenerAccount(1); err != nil {
		t.Fatal(err)
	}
	other.Close()
	if st.GardenerAccountID() != 1 || st.IsGardenerPrincipal(tokenOf("person:2")) || !st.IsGardenerPrincipal(tokenOf("person:1")) {
		t.Error("the running server does not see the change of another process")
	}
	if err := st.ClearGardenerAccount(); err != nil {
		t.Fatal(err)
	}
	if st.GardenerAccountID() != 0 {
		t.Error("clearing through the writer is not seen")
	}
}
