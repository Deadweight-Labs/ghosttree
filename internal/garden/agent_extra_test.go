package garden

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/llm"
)

// extraSetup is a run with the three kinds of session: a ledger session for one
// open request, a description session for one file, a knowledge session for
// three entries.
type extraSetup struct {
	fx   ledgerFixture
	opt  AgentOptions
	reqs []LedgerRequest
	gp   []GhostPath
}

func newExtraSetup(t *testing.T, runner SessionRunner) extraSetup {
	t.Helper()
	fx := newLedgerFixture(t)
	gp, _ := SelectGhosts(fx.repo, nil, nil, nil, nil, 0)
	if len(gp) != 1 || gp[0].Path != "internal/store/x.go" {
		t.Fatalf("ghost candidates = %v", paths(gp))
	}
	reqs := []LedgerRequest{ledgerReq()}
	return extraSetup{fx: fx, reqs: reqs, gp: gp, opt: AgentOptions{
		Runner: runner, Project: "p", Pricing: price, MaxUSD: 4, Sessions: 2, PerSession: 4, Turns: 60,
		Ledger: reqs, Ghosts: gp,
	}}
}

// byKind answers every kind of session the way a good session would.
func (s extraSetup) byKind(spec SessionSpec) (SessionResult, error) {
	usage := llm.Usage{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 1000, CacheWriteTokens: 2000}
	switch {
	case strings.Contains(spec.System, "das Ledger"):
		return SessionResult{Text: proofAnswer(fmt.Sprintf(`{"kind":"commit","ref":%q}`, s.fx.squash[:12])), Turns: 6, Usage: usage}, nil
	case strings.Contains(spec.System, "schreibst Beschreibungen"):
		return SessionResult{Text: ghostJSON(ghostItem("internal/store/x.go", "describe", goodText)), Turns: 5, Usage: usage}, nil
	}
	return holdsAll(spec)
}

func TestExtraSessionsRunFirstWithTheirOwnInstructionAndLeaveTheKnowledgeSessionsAlone(t *testing.T) {
	fr := &fakeRunner{}
	s := newExtraSetup(t, fr)
	fr.answers = []func(SessionSpec) (SessionResult, error){s.byKind}
	var checked []int64
	var looked []string
	s.opt.OnLedgerChecked = func(r LedgerRequest) { checked = append(checked, r.ID) }
	s.opt.OnGhostLooked = func(g GhostPath, verdict string) { looked = append(looked, g.Path+":"+verdict) }
	res := CheckAgent(context.Background(), s.fx.repo, agentEntries(3), s.opt)

	if len(fr.specs) != 3 {
		t.Fatalf("%d sessions, want ledger, description and one knowledge session", len(fr.specs))
	}
	if !strings.Contains(fr.specs[0].Name, "Ledger") || !strings.Contains(fr.specs[1].Name, "Beschreibungen") || strings.Contains(fr.specs[2].Name, "Ledger") {
		t.Errorf("order: %q, %q, %q", fr.specs[0].Name, fr.specs[1].Name, fr.specs[2].Name)
	}
	if fr.specs[0].System != LedgerSystemPrompt() || fr.specs[1].System != GhostSystemPrompt() || fr.specs[2].System != "" {
		t.Error("the sessions do not carry the instruction of their kind")
	}
	if fr.specs[0].MaxTurns != 40 || fr.specs[1].MaxTurns != 40 || fr.specs[2].MaxTurns != 60 {
		t.Errorf("turns = %d %d %d", fr.specs[0].MaxTurns, fr.specs[1].MaxTurns, fr.specs[2].MaxTurns)
	}
	for _, sp := range fr.specs[:2] {
		if sp.BudgetUSD > s.opt.MaxUSD/4+1e-9 {
			t.Errorf("%s may spend $%.2f, more than a quarter of the run", sp.Name, sp.BudgetUSD)
		}
	}
	if len(res.Proofs) != 1 || res.Proofs[0].Criterion.ID != 1793 || res.Proofs[0].Session == "" || len(res.GhostNotes) != 1 || res.GhostNotes[0].Path != "internal/store/x.go" || len(res.Verdicts) != 3 {
		t.Fatalf("proofs=%d ghosts=%d verdicts=%d", len(res.Proofs), len(res.GhostNotes), len(res.Verdicts))
	}
	if fmt.Sprint(checked) != "[457]" || fmt.Sprint(looked) != "[internal/store/x.go:proposed]" || res.LedgerChecked != 1 {
		t.Errorf("remembered %v %v", checked, looked)
	}
	if len(res.Sessions) != 3 || res.Sessions[0].Judged != 1 || res.StoppedBy != "" {
		t.Errorf("sessions = %+v stopped by %q", res.Sessions, res.StoppedBy)
	}

	// The knowledge sessions do not carry a byte of the new jobs.
	for _, text := range []string{AgentSystemPrompt(true), AgentSystemPrompt(false), fr.specs[2].Prompt} {
		for _, word := range []string{"Ledger", "Kriterium", "Beschreibung", "AUFTRAG #", "PFAD "} {
			if strings.Contains(text, word) {
				t.Errorf("a knowledge session mentions %q", word)
			}
		}
	}
}

func TestExtraSessionsThatFailNeverStopTheKnowledgeJob(t *testing.T) {
	fr := &fakeRunner{}
	s := newExtraSetup(t, fr)
	broken := func(spec SessionSpec) (SessionResult, error) {
		if spec.System != "" {
			return SessionResult{Text: "kein JSON"}, nil
		}
		return holdsAll(spec)
	}
	fr.answers = []func(SessionSpec) (SessionResult, error){broken}
	// Two ledger sessions and one for descriptions: the two failures end the extra jobs.
	s.opt.Ledger = nil
	for i := int64(1); i <= 9; i++ {
		r := ledgerReq()
		r.ID = 400 + i
		s.opt.Ledger = append(s.opt.Ledger, r)
	}
	var failed int
	s.opt.OnSession = func(st SessionStat, f bool) {
		if f {
			failed++
		}
	}
	res := CheckAgent(context.Background(), s.fx.repo, agentEntries(3), s.opt)
	if res.StoppedBy != "" || len(res.Verdicts) != 3 {
		t.Fatalf("stopped by %q with %d verdicts: a broken extra job stopped the knowledge job", res.StoppedBy, len(res.Verdicts))
	}
	for _, v := range res.Verdicts {
		if v.Kind != Holds {
			t.Errorf("verdict %+v", v)
		}
	}
	extra := 0
	for _, sp := range fr.specs {
		if sp.System != "" {
			extra++
		}
	}
	if extra != 2 || failed != 2 {
		t.Errorf("%d extra sessions ran and %d failed, want 2 and 2: after two failures the extra jobs stop", extra, failed)
	}
	if len(res.Proofs) != 0 || len(res.GhostNotes) != 0 {
		t.Error("something was proposed from a broken session")
	}
}

func TestExtraSessionsInADryRunAreEstimatedAndCallNothing(t *testing.T) {
	fr := &fakeRunner{answers: []func(SessionSpec) (SessionResult, error){holdsAll}}
	s := newExtraSetup(t, fr)
	s.opt.DryRun = true
	res := CheckAgent(context.Background(), s.fx.repo, agentEntries(3), s.opt)
	if len(fr.specs) != 0 || len(res.Sessions) != 3 {
		t.Fatalf("%d calls, %d sessions listed", len(fr.specs), len(res.Sessions))
	}
	for _, st := range res.Sessions {
		if st.Usage.InputTokens <= 0 {
			t.Errorf("%s has no estimate", st.Name)
		}
	}
	// What the new jobs add to a night, in tokens of prompt and standing instruction.
	t.Logf("prompt growth per night: ledger %d tokens, descriptions %d tokens, knowledge %d tokens",
		res.Sessions[0].Usage.InputTokens, res.Sessions[1].Usage.InputTokens, res.Sessions[2].Usage.InputTokens)
}

func TestAnExtraSessionThatChangesTheCheckoutEndsTheRun(t *testing.T) {
	fr := &fakeRunner{}
	s := newExtraSetup(t, fr)
	tamper := func(spec SessionSpec) (SessionResult, error) {
		if spec.System == LedgerSystemPrompt() {
			if err := os.WriteFile(filepath.Join(s.fx.dir, "planted.txt"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return s.byKind(spec)
	}
	fr.answers = []func(SessionSpec) (SessionResult, error){tamper}
	res := CheckAgent(context.Background(), s.fx.repo, agentEntries(3), s.opt)
	if res.StoppedBy != "tamper" || len(res.Proofs) != 0 || len(res.GhostNotes) != 0 || len(fr.specs) != 1 {
		t.Errorf("stopped by %q, %d proofs, %d descriptions, %d sessions", res.StoppedBy, len(res.Proofs), len(res.GhostNotes), len(fr.specs))
	}
	for _, v := range res.Verdicts {
		if v.Kind != NotRun {
			t.Errorf("a knowledge entry was judged after the tamper: %+v", v)
		}
	}
}

func TestAPartialResultOfAnExtraSessionIsTakenWhenItParses(t *testing.T) {
	fr := &fakeRunner{}
	s := newExtraSetup(t, fr)
	cut := func(spec SessionSpec) (SessionResult, error) {
		r, err := s.byKind(spec)
		if strings.Contains(spec.System, "das Ledger") {
			r.Looked = seenBy(nil, s.fx.squash[:12])
			r.Tools = map[string]int{"Grep": 3}
			return r, errors.New("die Session endete mit error_max_turns")
		}
		return r, err
	}
	fr.answers = []func(SessionSpec) (SessionResult, error){cut}
	res := CheckAgent(context.Background(), s.fx.repo, agentEntries(2), s.opt)
	if len(res.Proofs) != 1 || !strings.HasPrefix(res.Sessions[0].Err, "Teilergebnis") {
		t.Errorf("proofs=%d err=%q", len(res.Proofs), res.Sessions[0].Err)
	}
}
