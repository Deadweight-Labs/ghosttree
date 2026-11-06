package garden

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
)

func reportOf(res Result) string {
	var b bytes.Buffer
	WriteReport(&b, "github.com/o/r", res, time.Date(2026, 10, 21, 3, 5, 0, 0, time.UTC))
	return b.String()
}

func TestReportListsIdleRequestsWithoutAModelCall(t *testing.T) {
	now := time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC)
	facts := []RequestFacts{
		{ID: 12, Title: "Alter Auftrag", Priority: "hoch", OpenCriteria: 3, LastActivity: now.Add(-75 * 24 * time.Hour)},
		{ID: 13, Title: "Mittel alt", OpenCriteria: 1, LastActivity: now.Add(-31 * 24 * time.Hour)},
		{ID: 14, Title: "Frisch", LastActivity: now.Add(-3 * 24 * time.Hour)},
	}
	// A dry run: no session ran, and the list is there all the same.
	out := reportOf(Result{DryRun: true, Agent: true, Idle: IdleRequests(facts, now, DefaultIdleDays), IdleDays: DefaultIdleDays})
	for _, want := range []string{"## Liegengeblieben", "mindestens 30 Tagen", "- #12 Alter Auftrag: seit 75 Tagen, 3 offene Kriterien, Priorität hoch", "- #13 Mittel alt: seit 31 Tagen, 1 offene Kriterien",
		"Arbeit in einer privaten Session eines anderen Kontos fehlt hier"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Frisch") {
		t.Error("a request with recent activity is listed")
	}
	if strings.Index(out, "#12") > strings.Index(out, "#13") {
		t.Error("the longest idle is not first")
	}
	// Off: no section.
	if strings.Contains(reportOf(Result{Idle: nil, IdleDays: 0}), "Liegengeblieben") || strings.Contains(reportOf(Result{Idle: IdleRequests(facts, now, 30), IdleDays: 0}), "Liegengeblieben") {
		t.Error("the section is there although it is off")
	}
	// A long list is cut and says how many are left.
	var many []IdleRequest
	for i := 1; i <= maxIdleListed+5; i++ {
		many = append(many, IdleRequest{RequestFacts: RequestFacts{ID: int64(i), Title: fmt.Sprint("r", i)}, Days: 100})
	}
	if out := reportOf(Result{Idle: many, IdleDays: 30}); !strings.Contains(out, "… und 5 weitere") {
		t.Errorf("a long list is not cut:\n%s", out)
	}
}

func TestReportShowsProofsAndProposedDescriptionsAsProposals(t *testing.T) {
	proof := Proof{Request: ledgerReq(), Criterion: ledgerReq().Criteria[0], Reason: "Der Commit zeigt es.",
		Items: []ProofItem{{Kind: ProofCommit, Ref: "4584fb7a1c2d (#149)", Detail: "2026-10-19 feat: x (#149)"}}}
	g := GhostProposalData{GhostPath: GhostPath{Path: "internal/store/x.go", State: GhostStale, Percent: 42}, Description: "Erste Zeile.\nZweite Zeile.", Reason: "umgeschrieben"}
	out := reportOf(Result{Agent: true, Proofs: []Proof{proof}, GhostNotes: []GhostProposalData{g}, GhostKept: []GhostPath{{Path: "cmd/ok.go"}}})
	for _, want := range []string{"## Ledger: erfüllte Kriterien (Vorschläge)", "Geschlossen wird nichts", "- #457 Gärtner pflegt das Ledger, Kriterium 6:", "Beleg commit: 4584fb7a1c2d (#149)",
		"## Beschreibungen (Ghosts)", "- internal/store/x.go (veraltet (42 % der Zeilen seit der Beschreibung geändert))", "  > Erste Zeile.\n  > Zweite Zeile.", "stimmt noch", "cmd/ok.go"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(reportOf(Result{Agent: true}), "Ledger: erfüllte") {
		t.Error("an empty section is printed")
	}
}
