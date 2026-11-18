package store

import (
	"bytes"
	"errors"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/secretbox"
)

// Review of #283, M2 in the store: a reservation counts against every cap at
// once, also across runs, and a settled call replaces its estimate.
func TestReservationsCountAgainstTheCapsOfEveryRun(t *testing.T) {
	st, o, _ := aiFixture(t)
	aiConn(t, st, "person:1", o.ID, "openai-a", AIFormatOpenAI, "gpt-a")
	if err := st.SetAICap("person:1", AICap{OrgID: o.ID, MonthUSD: 0.003}); err != nil {
		t.Fatal(err)
	}
	a, b := aiRun(t, st, aiProject, "wf/a"), aiRun(t, st, aiProject, "wf/b")
	ra, _ := st.AIRouteFor(a, "gpt-a")
	rb, _ := st.AIRouteFor(b, "gpt-a")
	id, err := st.ReserveAICall(ra, 0.002)
	if err != nil {
		t.Fatal(err)
	}
	// Run b read its route before a reserved; the writer still sees a.
	var be *AIBudgetError
	if _, err := st.ReserveAICall(rb, 0.002); !errors.As(err, &be) || be.Cap != AICapOwner {
		t.Fatalf("second run over the owner's cap: %v", err)
	}
	if reached, err := st.SettleAICall(id, AICall{InputTokens: 10, OutputTokens: 5, CostUSD: 0.0005, Status: 200}); err != nil || reached != "" {
		t.Fatalf("settle: %q %v", reached, err)
	}
	c := aiRun(t, st, aiProject, "wf/c")
	rc, err := st.AIRouteFor(c, "gpt-a")
	if err != nil {
		t.Fatalf("after the settlement there is room again: %v", err)
	}
	id2, err := st.ReserveAICall(rc, 0.002)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ReleaseAICall(id2); err != nil {
		t.Fatal(err)
	}
	if u, _ := st.AIRunUsageOf(a.ID); u.CostUSD != 0.0005 || u.Calls != 1 {
		t.Fatalf("run a: %+v", u)
	}
	if u, _ := st.AIRunUsageOf(c.ID); u.Calls != 0 {
		t.Fatalf("a released reservation stays: %+v", u)
	}
}

// Review of #283, m3: the sealed key is bound to its row. A copy into another
// row does not open there, and keys sealed the old way are sealed again once.
func TestSealedKeyIsBoundToItsRowAndOldKeysAreSealedAgain(t *testing.T) {
	st, o, other := aiFixture(t)
	box, _ := secretbox.New(bytes.Repeat([]byte{7}, 32))
	save := func(orgID int64, actor, name, key string) AIConnection {
		t.Helper()
		c, err := st.SaveAIConnection(actor, AIConnectionInput{OrgID: orgID, Name: name, Format: AIFormatOpenAI, BaseURL: "https://api.example.com",
			Models: []AIModel{{Model: "m"}}}, func(purpose string) ([]byte, error) { return box.Seal(key, purpose) })
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	mine := save(o.ID, "person:1", "mine", "sk-mine-0001")
	theirs := save(other.ID, "person:3", "theirs", "sk-theirs-0002")
	sealedMine, _, _ := st.AIConnectionKey(mine.ID)
	if k, err := box.Open(sealedMine, AIKeyPurposeFor(o.ID, mine.ID)); err != nil || k != "sk-mine-0001" {
		t.Fatalf("own row: %q %v", k, err)
	}
	// Someone with write access to the database copies a key into their row.
	if _, err := st.db.Exec(`UPDATE ai_connections SET key_sealed=? WHERE id=?`, sealedMine, theirs.ID); err != nil {
		t.Fatal(err)
	}
	copied, _, _ := st.AIConnectionKey(theirs.ID)
	if _, err := box.Open(copied, AIKeyPurposeFor(other.ID, theirs.ID)); err == nil {
		t.Fatal("a key copied into another row opens there")
	}

	// Rows from before the binding: one sealed the old way, one damaged, and
	// the connection from llm.json without a sealed key.
	old, _ := box.Seal("sk-old-0003", aiKeyPurposeV1)
	legacy := save(o.ID, "person:1", "legacy", "x")
	if _, err := st.db.Exec(`UPDATE ai_connections SET key_sealed=? WHERE id=?`, old, legacy.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ImportFileAIConnection(AIFileConnection{Name: "standard", Format: AIFormatAnthropic, BaseURL: "https://api.anthropic.com", Model: "claude-haiku-5-5", KeyLast4: "AAAA"}); err != nil {
		t.Fatal(err)
	}
	resealed, unusable, err := st.ResealAIKeys(box.Open, box.Seal)
	if err != nil || resealed != 1 || unusable != 1 {
		t.Fatalf("reseal: %d resealed, %d unusable, %v", resealed, unusable, err)
	}
	now, _, _ := st.AIConnectionKey(legacy.ID)
	if k, err := box.Open(now, AIKeyPurposeFor(o.ID, legacy.ID)); err != nil || k != "sk-old-0003" {
		t.Fatalf("resealed key: %q %v", k, err)
	}
	if _, err := box.Open(now, aiKeyPurposeV1); err == nil {
		t.Fatal("the old purpose still opens the key")
	}
	if r, u, err := st.ResealAIKeys(box.Open, box.Seal); err != nil || r != 0 || u != 1 {
		t.Fatalf("second start: %d %d %v", r, u, err)
	}
}

// Review of #283, m5: a loan holds only for the address the borrowing
// organization agreed to and only while its lender belongs to it; the owners
// of the organization see the host.
func TestLoanEndsWithANewAddressAndWithTheLendersMembership(t *testing.T) {
	st, o, _ := aiFixture(t)
	ns, err := st.EnsureUserNamespace("person:2")
	if err != nil {
		t.Fatal(err)
	}
	lend := func(name string) (AIConnection, AILoan) {
		t.Helper()
		c := aiConn(t, st, "person:2", ns.ID, name, AIFormatOpenAI, "gpt-x")
		l, err := st.LendAIConnection("person:2", c.ID, o.ID, 3)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.SetAIAlias("person:1", AIAlias{Level: AILevelOwner, OrgID: o.ID, Alias: name, ConnectionID: c.ID, Model: "gpt-x"}); err != nil {
			t.Fatal(err)
		}
		return c, l
	}
	moved, _ := lend("moved")
	if list, _ := st.AIConnectionsFor(o.ID); len(list) != 1 || list[0].Host != "api.example.com" || list[0].BaseURL != "" {
		t.Fatalf("the org sees the host of the loan: %+v", list)
	}
	run := aiRun(t, st, aiProject, "wf/a")
	if _, err := st.AIRouteFor(run, "moved"); err != nil {
		t.Fatal(err)
	}
	// The lender points the connection elsewhere, with a fresh key.
	if _, err := st.SaveAIConnection("person:2", AIConnectionInput{ID: moved.ID, Name: moved.Name, Format: moved.Format, BaseURL: "https://collector.example",
		Models: moved.Models, KeyLast4: "NEW1"}, sealedAs("new")); err != nil {
		t.Fatal(err)
	}
	if r, err := st.AIRouteFor(run, "moved"); !errors.Is(err, ErrAIUnavailable) {
		t.Fatalf("calls follow the new address: %+v %v", r, err)
	}
	if aliases, _ := st.AIAliasesFor(o.ID); len(aliases) != 0 {
		t.Fatalf("the org's alias on the moved connection stays: %+v", aliases)
	}
	// A change of cap or models alone keeps the loan.
	kept, _ := lend("kept")
	if _, err := st.SaveAIConnection("person:2", AIConnectionInput{ID: kept.ID, Name: kept.Name, Format: kept.Format, BaseURL: kept.BaseURL,
		Models: kept.Models, MonthlyCapUSD: 2}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AIRouteFor(run, "kept"); err != nil {
		t.Fatalf("a new cap ends the loan: %v", err)
	}
	// The owner removes the lender from the org: the loan goes with them.
	if err := st.RemoveOrgMember("person:1", o.ID, "person:2"); err != nil {
		t.Fatal(err)
	}
	if r, err := st.AIRouteFor(run, "kept"); !errors.Is(err, ErrAIUnavailable) {
		t.Fatalf("the loan of a removed member still routes: %+v %v", r, err)
	}
	if loans, _ := st.AILoansOf(kept.ID); len(loans) != 0 {
		t.Fatalf("loans of a removed member: %+v", loans)
	}
	// A membership that ends another way stops the loan as well.
	if _, err := st.db.Exec(`INSERT INTO org_members(org_id, account_id, role, joined_at) VALUES(?,2,'member','2026-11-17T00:00:00Z')`, o.ID); err != nil {
		t.Fatal(err)
	}
	third, _ := lend("third")
	if _, err := st.db.Exec(`DELETE FROM org_members WHERE org_id=? AND account_id=2`, o.ID); err != nil {
		t.Fatal(err)
	}
	if r, err := st.AIRouteFor(run, "third"); !errors.Is(err, ErrAIUnavailable) {
		t.Fatalf("a loan without its lender in the org routes: %+v %v", r, err)
	}
	_ = third
}

// Review of #283, m6: the release "all" needs a cap per namespace, which
// holds for personal namespaces too; a release "all" stored without one
// releases nothing.
func TestReleaseToAllNeedsADefaultCap(t *testing.T) {
	st, o, _ := aiFixture(t)
	aiConn(t, st, "person:1", 0, "standard", AIFormatAnthropic, "claude-haiku-5-5")
	if err := st.SetAIRelease("person:1", AIReleaseAll, 0); !errors.Is(err, ErrAICap) {
		t.Fatalf("all without a cap: %v", err)
	}
	if err := st.SetAIRelease("person:1", AIReleaseAll, 5); err != nil {
		t.Fatal(err)
	}
	ns, err := st.EnsureUserNamespace("person:2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateNamedProject("person:2", "notes", ns.Slug); err != nil {
		t.Fatal(err)
	}
	personal := aiRun(t, st, ns.Slug+"/notes", "wf/p")
	r, err := st.AIRouteFor(personal, "standard/claude-haiku-5-5")
	if err != nil || r.TightestCap != AICapServer || r.Remaining != 5 {
		t.Fatalf("a profile under all: %+v %v", r, err)
	}
	// A grant with a cap of its own still decides for its namespace.
	if err := st.SetAIGrant("person:1", o.ID, 9); err != nil {
		t.Fatal(err)
	}
	run := aiRun(t, st, aiProject, "wf/a")
	if r, err := st.AIRouteFor(run, "standard/claude-haiku-5-5"); err != nil || r.Remaining != 9 {
		t.Fatalf("grant under all: %+v %v", r, err)
	}
	// A release saved before the default cap existed.
	if _, err := st.db.Exec(`UPDATE ai_server_release SET default_cap_usd=0`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AIRouteFor(personal, "standard/claude-haiku-5-5"); !errors.Is(err, ErrAIUnavailable) {
		t.Fatalf("all without a stored cap: %v", err)
	}
	// Switching to selected keeps the stored cap for the next "all".
	if err := st.SetAIRelease("person:1", AIReleaseAll, 4); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAIRelease("person:1", AIReleaseSelected, 0); err != nil || st.AIReleaseDefaultCap() != 4 {
		t.Fatalf("selected: %v, default %v", err, st.AIReleaseDefaultCap())
	}
}
