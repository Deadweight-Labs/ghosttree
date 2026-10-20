package garden

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeIn(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ledgerRepo is a checkout with a squash-style commit that carries a pull request
// number, a Go test, and a commit on a branch that HEAD does not contain.
type ledgerFixture struct {
	repo        *Repo
	dir         string
	squash      string // full hash of the commit "(#12)"
	sideBranch  string // full hash of a commit that is not in the history of HEAD
	headAtStart string
}

func newLedgerFixture(t *testing.T) ledgerFixture {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	writeIn(t, dir, "internal/store/x.go", "package store\n\nfunc X() {}\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "chore: start")
	writeIn(t, dir, "internal/store/x_test.go", "package store\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "feat(store): X mit Test (#12)")
	squash := gitIn(t, dir, "rev-parse", "HEAD")
	gitIn(t, dir, "checkout", "-q", "-b", "side")
	writeIn(t, dir, "side.txt", "nur auf dem Nebenzweig\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "feat: Nebenzweig (#99)")
	side := gitIn(t, dir, "rev-parse", "HEAD")
	gitIn(t, dir, "checkout", "-q", "main")
	// A file nobody tracks.
	writeIn(t, dir, "untracked.go", "package store\n")
	repo, err := OpenRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	return ledgerFixture{repo: repo, dir: dir, squash: squash, sideBranch: side, headAtStart: squash}
}

func ledgerReq() LedgerRequest {
	return LedgerRequest{ID: 457, Title: "Gärtner pflegt das Ledger", Type: "feature", CreatedAt: "2026-10-10T00:00:00Z", Criteria: []LedgerCriterion{
		{ID: 1793, Number: 6, Description: "Erfüllte Kriterien werden mit Nachweis vorgeschlagen"},
		{ID: 1794, Number: 7, Description: "Ein zweites Kriterium"},
	}}
}

// seen is what a session that read the files and saw the commits in tool output has.
func seenBy(read []string, printed ...string) *Looked {
	l := &Looked{Read: map[string]bool{}}
	for _, f := range read {
		l.Read["/work/"+f] = true
	}
	for _, p := range printed {
		l.Out.WriteString(p + " 2026-10-19 t: commit\n")
	}
	return l
}

func proofAnswer(items string) string {
	return fmt.Sprintf(`{"checked":[457],"criteria":[{"request":457,"criterion":1793,"reason":"Der Beleg zeigt es im Code.","evidence":[%s]}]}`, items)
}

func TestLedgerProofsAreHeldToTheCheckout(t *testing.T) {
	fx := newLedgerFixture(t)
	hash12 := fx.squash[:12]
	commit := func(h string) string { return fmt.Sprintf(`{"kind":"commit","ref":%q}`, h) }
	test := func(file, name string) string {
		return fmt.Sprintf(`{"kind":"test","file":%q,"test":%q}`, file, name)
	}
	file := func(f string) string { return fmt.Sprintf(`{"kind":"file","file":%q}`, f) }
	both := seenBy([]string{"internal/store/x_test.go", "internal/store/x.go"}, hash12, fx.sideBranch[:12])

	cases := []struct {
		name     string
		evidence string
		looked   *Looked
		want     []string // refs kept; empty means the proof is dropped
		reject   string   // part of the reason it was named in the report
	}{
		{"a commit with a pull request number", commit(hash12), both, []string{hash12 + " (#12)"}, ""},
		{"the commit by a longer hash", commit(fx.squash), both, []string{hash12 + " (#12)"}, ""},
		{"a commit that never existed", commit("deadbeefdeadbeef"), seenBy(nil, "deadbeefdeadbeef"), nil, "kein Commit"},
		{"not a hash at all", commit("HEAD"), both, nil, "kein Commit-Hash"},
		{"a real commit no tool printed", commit(hash12), seenBy(nil), nil, "kein Werkzeug"},
		{"a commit that is not in the history of HEAD", commit(fx.sideBranch[:12]), both, nil, "nicht im Verlauf"},
		{"a test that exists", test("internal/store/x_test.go", "TestX"), both, []string{"internal/store/x_test.go:TestX"}, ""},
		{"a test that does not exist", test("internal/store/x_test.go", "TestInvented"), both, nil, "definiert keinen Test"},
		{"a test in a file that does not exist", test("internal/store/nope_test.go", "TestX"), both, nil, "definiert keinen Test"},
		{"a test in a file the session did not read", test("internal/store/x_test.go", "TestX"), seenBy(nil), nil, "nicht angesehen"},
		{"a test outside the checkout", test("../x_test.go", "TestX"), both, nil, "unbrauchbar"},
		{"a file that exists", file("internal/store/x.go"), both, []string{"internal/store/x.go"}, ""},
		{"a file that does not exist", file("internal/store/nope.go"), both, nil, "keine versionierte Datei"},
		{"a file nobody tracks", file("untracked.go"), seenBy([]string{"untracked.go"}), nil, "keine versionierte Datei"},
		{"a file the session did not read", file("internal/store/x.go"), seenBy(nil), nil, "nicht angesehen"},
		{"a path into .git", file(".git/config"), seenBy([]string{".git/config"}), nil, "unbrauchbar"},
		{"an absolute path", file("/etc/passwd"), both, nil, "unbrauchbar"},
		{"an unknown kind", `{"kind":"vibes","ref":"x"}`, both, nil, "unbekannte Art"},
		{"one valid and one invented proof keep the valid one", commit(hash12) + "," + test("internal/store/x_test.go", "TestInvented"), both, []string{hash12 + " (#12)"}, "definiert keinen Test"},
		{"the commit comes first whatever the order", file("internal/store/x.go") + "," + test("internal/store/x_test.go", "TestX") + "," + commit(hash12), both,
			[]string{hash12 + " (#12)", "internal/store/x_test.go:TestX", "internal/store/x.go"}, ""},
	}
	for _, c := range cases {
		v, err := validateLedger(fx.repo, proofAnswer(c.evidence), []LedgerRequest{ledgerReq()}, c.looked)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var got []string
		if len(v.Proofs) == 1 {
			for _, it := range v.Proofs[0].Items {
				got = append(got, it.Ref)
			}
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s: kept %v, want %v (rejected: %v)", c.name, got, c.want, v.Rejected)
		}
		if c.want == nil && !strings.Contains(strings.Join(v.Rejected, "\n"), "kein gültiger Beleg") {
			t.Errorf("%s: a dropped proof must be named in the report: %v", c.name, v.Rejected)
		}
		if c.reject != "" && !strings.Contains(strings.Join(v.Rejected, "\n"), c.reject) {
			t.Errorf("%s: rejected %v, want a mention of %q", c.name, v.Rejected, c.reject)
		}
		if c.reject == "" && len(v.Rejected) > 0 {
			t.Errorf("%s: nothing should be rejected: %v", c.name, v.Rejected)
		}
	}
}

func TestLedgerAnswerNamesOnlyOpenCriteriaOfThePackageOnceAndWithAReason(t *testing.T) {
	fx := newLedgerFixture(t)
	good := fmt.Sprintf(`{"kind":"commit","ref":%q}`, fx.squash[:12])
	looked := seenBy(nil, fx.squash[:12])
	item := func(req, crit int64, reason string) string {
		return fmt.Sprintf(`{"request":%d,"criterion":%d,"reason":%q,"evidence":[%s]}`, req, crit, reason, good)
	}
	text := `{"checked":[457,999,457],"criteria":[` + strings.Join([]string{
		item(457, 1793, "Der Commit führt es ein."),
		item(457, 1793, "Doppelt genannt, deshalb verworfen."),
		item(457, 5555, "Ein Kriterium, das es nicht gibt."),
		item(999, 1, "Ein Auftrag, der nicht im Paket ist."),
		item(457, 1794, "kurz"),
	}, ",") + `]}`
	v, err := validateLedger(fx.repo, text, []LedgerRequest{ledgerReq()}, looked)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Proofs) != 1 || v.Proofs[0].Criterion.ID != 1793 || v.Proofs[0].Criterion.Number != 6 || v.Proofs[0].Request.ID != 457 {
		t.Fatalf("proofs = %+v", v.Proofs)
	}
	if len(v.Checked) != 1 || v.Checked[0] != 457 {
		t.Errorf("checked = %v, want only the request of the package, once", v.Checked)
	}
	if len(v.Rejected) != 4 {
		t.Errorf("rejected = %v", v.Rejected)
	}
	for _, in := range []string{"", "fertig", `{"verdicts":[]}`, "{kaputt"} {
		if _, err := validateLedger(fx.repo, in, []LedgerRequest{ledgerReq()}, looked); err == nil {
			t.Errorf("%q accepted as the answer of a ledger session", in)
		}
	}
}

func TestCriterionNoteCarriesTheProofAndItsTargetAsNumbers(t *testing.T) {
	p := Proof{Request: ledgerReq(), Criterion: ledgerReq().Criteria[0], Reason: "Der Beleg zeigt es im Code.",
		Items: []ProofItem{{Kind: ProofCommit, Ref: "4584fb7a1c2d (#149)", Detail: "2026-10-19 feat: x (#149)"}, {Kind: ProofTest, Ref: "cmd/x_test.go:TestX"}}}
	k := CriterionProposal("github.com/o/r", p)
	if k.Harness != "gardener" || k.Confidence != "staged" || k.Status != "active" || k.Type != "note" || k.SessionRef != "gardener:criterion:457:1793" {
		t.Errorf("note = %+v", k)
	}
	if k.Title != "[Gärtner] Erfüllt? #457 Kriterium 6: Erfüllte Kriterien werden mit Nachweis vorgeschlagen" {
		t.Errorf("title = %q", k.Title)
	}
	want := []string{"commit 4584fb7a1c2d (#149)", "test cmd/x_test.go:TestX"}
	if got := proofLines(k.Body); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("proof lines = %v", got)
	}
	for _, it := range p.Items {
		if !ValidProofRef(it.Kind, it.Ref) {
			t.Errorf("%s %q is not of a shape the review page accepts", it.Kind, it.Ref)
		}
	}
	for kind, bad := range map[string][]string{
		ProofCommit: {"HEAD", "4584fb7", "4584FB7A1C2D", "4584fb7a1c2d (#)", "4584fb7a1c2d; rm", "4584fb7a1c2d\n(#1)"},
		ProofTest:   {"x_test.go", "../x_test.go:TestX", "a/.git/x_test.go:TestX", "a b/x_test.go:TestX", "x_test.go:Test X"},
		ProofFile:   {"../x.go", "a/../x.go", "/etc/passwd", "a b.go", ".git/config", "a/./b.go"},
	} {
		for _, ref := range bad {
			if ValidProofRef(kind, ref) {
				t.Errorf("%s %q accepted", kind, ref)
			}
		}
	}
}

func TestCriterionProposedOnlyAsksAgainWithNewEvidence(t *testing.T) {
	p := Proof{Request: ledgerReq(), Criterion: ledgerReq().Criteria[0], Reason: "Der Beleg zeigt es im Code.",
		Items: []ProofItem{{Kind: ProofCommit, Ref: "4584fb7a1c2d (#149)"}}}
	note := func(status, confidence string, items ...ProofItem) store.Knowledge {
		q := p
		q.Items = items
		k := CriterionProposal("github.com/o/r", q)
		k.Status, k.Confidence = status, confidence
		return k
	}
	same := ProofItem{Kind: ProofCommit, Ref: "4584fb7a1c2d (#149)"}
	other := ProofItem{Kind: ProofCommit, Ref: "aaaaaaaaaaaa (#150)"}
	cases := []struct {
		name     string
		existing []store.Knowledge
		want     bool
	}{
		{"nothing before", nil, false},
		{"a note waits for a decision", []store.Knowledge{note("active", "staged", other)}, true},
		{"a rejected note with the same proof", []store.Knowledge{note("deprecated", "staged", same)}, true},
		{"a confirmed note with the same proof", []store.Knowledge{note("active", "verified", same)}, true},
		{"a rejected note with another proof asks again", []store.Knowledge{note("deprecated", "staged", other)}, false},
		{"a note about another criterion", []store.Knowledge{func() store.Knowledge {
			k := note("active", "staged", same)
			k.SessionRef = "gardener:criterion:457:1794"
			return k
		}()}, false},
		{"a note that is not the gardener's", []store.Knowledge{func() store.Knowledge {
			k := note("active", "staged", same)
			k.Harness = "mcp"
			return k
		}()}, false},
	}
	for _, c := range cases {
		if got := CriterionProposed(c.existing, p); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestLedgerRotationTakesNewAndChangedRequestsFirstAndLeavesUnchangedOnes(t *testing.T) {
	now := time.Date(2026, 10, 20, 3, 0, 0, 0, time.UTC)
	mk := func(id int64, created string, crits ...int64) LedgerRequest {
		r := LedgerRequest{ID: id, Title: fmt.Sprint("r", id), CreatedAt: created}
		for _, c := range crits {
			r.Criteria = append(r.Criteria, LedgerCriterion{ID: c, Number: 1, Description: fmt.Sprint("k", c)})
		}
		return r
	}
	newest, oldest, changed, unchanged, recent, aged, closed := mk(1, "2026-10-09T00:00:00Z", 11), mk(2, "2026-09-01T00:00:00Z", 21), mk(3, "2026-09-05T00:00:00Z", 31, 32),
		mk(4, "2026-09-06T00:00:00Z", 41), mk(5, "2026-09-07T00:00:00Z", 51), mk(6, "2026-09-08T00:00:00Z", 61), mk(7, "2026-09-09T00:00:00Z")
	seen := map[int64]Seen{
		3: {CheckedAt: "2026-09-30T00:00:00Z", Commit: "old", EntryHash: "anderer Stand"},
		4: {CheckedAt: "2026-10-01T00:00:00Z", Commit: "head", EntryHash: unchanged.Hash()},
		5: {CheckedAt: "2026-10-15T00:00:00Z", Commit: "old", EntryHash: recent.Hash()},
		6: {CheckedAt: "2026-09-20T00:00:00Z", Commit: "old", EntryHash: aged.Hash()},
	}
	got, rot := RotateLedger([]LedgerRequest{newest, oldest, changed, unchanged, recent, aged, closed}, seen, "head", 14*24*time.Hour, now)
	var ids []int64
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	// Never looked at, oldest request first; then the one whose criteria changed; then the one longest ago.
	if fmt.Sprint(ids) != "[2 1 3 6]" {
		t.Errorf("order = %v, want [2 1 3 6]", ids)
	}
	if rot.Never != 2 || rot.Unchanged != 1 || rot.Recent != 1 {
		t.Errorf("rotation = %+v", rot)
	}
	// A criterion that is settled makes the request due again.
	settled := mk(4, "2026-09-06T00:00:00Z", 41, 42)
	if got, _ := RotateLedger([]LedgerRequest{settled}, seen, "head", 14*24*time.Hour, now); len(got) != 1 {
		t.Error("a request whose open criteria changed was left out")
	}
}

func TestIdleRequestsAreListedFromDataAloneWithTheLongestFirst(t *testing.T) {
	now := time.Date(2026, 10, 20, 3, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return now.Add(-time.Duration(n) * 24 * time.Hour) }
	facts := []RequestFacts{
		{ID: 1, Title: "gerade bearbeitet", LastActivity: day(2)},
		{ID: 2, Title: "knapp unter der Grenze", LastActivity: day(29)},
		{ID: 3, Title: "auf der Grenze", LastActivity: day(30), OpenCriteria: 2},
		{ID: 4, Title: "lange liegen", LastActivity: day(90), Priority: "hoch"},
		{ID: 5, Title: "ohne Zeitstempel"},
	}
	got := IdleRequests(facts, now, 30)
	if len(got) != 2 || got[0].ID != 4 || got[0].Days != 90 || got[1].ID != 3 || got[1].Days != 30 {
		t.Fatalf("idle = %+v", got)
	}
	if IdleRequests(facts, now, 0) != nil {
		t.Error("days 0 must turn the list off")
	}
}

func TestLedgerFromDetailTakesTheNewestSignOfWork(t *testing.T) {
	d := requestdomain.Detail{
		Request:  requestdomain.Request{ID: 9, State: "open", Title: "t", CreatedAt: "2026-08-01T00:00:00Z", UpdatedAt: "2026-08-02T00:00:00Z"},
		Criteria: []requestdomain.Criterion{{ID: 90, Number: 1, State: "met", UpdatedAt: "2026-08-10T00:00:00Z", Evidence: []requestdomain.Evidence{{CreatedAt: "2026-08-11T00:00:00Z"}}}, {ID: 91, Number: 2, State: "open", Description: "offen", UpdatedAt: "2026-08-03T00:00:00Z"}},
		Activity: []requestdomain.Activity{{CreatedAt: "2026-08-20T00:00:00Z"}},
		Work:     []requestdomain.Work{{StartedAt: "2026-09-01T00:00:00Z", EndedAt: "2026-09-02T00:00:00Z"}},
	}
	lr, f, ok := LedgerFromDetail(d)
	if !ok || len(lr.Criteria) != 1 || lr.Criteria[0].ID != 91 || f.OpenCriteria != 1 {
		t.Fatalf("%+v %+v %v", lr, f, ok)
	}
	if want := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC); !f.LastActivity.Equal(want) {
		t.Errorf("last activity = %v, want %v", f.LastActivity, want)
	}
	d.Request.State = "done"
	if _, _, ok := LedgerFromDetail(d); ok {
		t.Error("a request that is not open went into the ledger")
	}
}
