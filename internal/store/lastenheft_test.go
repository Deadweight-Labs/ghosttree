package store

import (
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

const lhHead1, lhHead2 = "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"

// Der Stand des Lastenhefts besteht aus Zahlen; er ändert sich mit dem Fortschritt
// und nur dann, und der Head-Commit kommt mit dem Ereignis.
func TestLastenheftCountsAndDigestFollowTheCriteria(t *testing.T) {
	st := prFixture(t)
	req := pkgRequestIn(t, st, prProject, "Pflicht")
	opt := pkgRequestIn(t, st, prProject, "Optional")
	if _, err := st.CreatePackage(requestdomain.PackageInput{Project: prProject, Branch: "feat/lh", Name: "P", Person: "robin",
		Members: []requestdomain.PackageMember{{RequestID: req}, {RequestID: opt, Role: requestdomain.RoleOptional}}}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.PullLastenheft(prProject, 60); err != nil || ok {
		t.Fatalf("no PR, no list: %v %v", ok, err)
	}
	ev := prEvent(60, "opened", "open", "feat/lh")
	ev.HeadSHA = lhHead1
	if res, err := st.RecordPullRequest(ev); err != nil || !res.PackageBound {
		t.Fatalf("opened: %+v %v", res, err)
	}
	l, ok, err := st.PullLastenheft(prProject, 60)
	if err != nil || !ok || len(l.Members) != 2 {
		t.Fatalf("lastenheft: %+v %v %v", l, ok, err)
	}
	requests, open, met, waived := l.Required()
	if requests != 1 || open != 1 || met != 0 || waived != 0 || l.Conclusion() != "failure" {
		t.Fatalf("required: %d %d %d %d %s", requests, open, met, waived, l.Conclusion())
	}
	d0 := l.Digest()
	if l.Digest() != d0 {
		t.Fatal("the digest is not stable")
	}

	targets, err := st.LastenheftTargets()
	if err != nil || len(targets) != 1 || targets[0].PR != 60 || targets[0].HeadSHA != lhHead1 || targets[0].InstallationID != prInstall {
		t.Fatalf("targets: %+v %v", targets, err)
	}
	if sha, digest, err := st.LastenheftPublished(prProject, 60); err != nil || sha != "" || digest != "" {
		t.Fatalf("published before: %q %q %v", sha, digest, err)
	}
	if err := st.MarkLastenheftPublished(prProject, 60, lhHead1, d0); err != nil {
		t.Fatal(err)
	}
	if sha, digest, _ := st.LastenheftPublished(prProject, 60); sha != lhHead1 || digest != d0 {
		t.Fatalf("published after: %q %q", sha, digest)
	}

	// Ein erfülltes Pflichtkriterium ändert Stand und Digest; das des optionalen Requests auch den Digest, nicht das Ergebnis.
	d, _ := st.RequestByID(req)
	if err := st.SetCriterionState(d.Criteria[0].ID, "met", requestdomain.Evidence{RequestID: req, Kind: "test", Ref: "t", Person: "robin"}); err != nil {
		t.Fatal(err)
	}
	l, _, _ = st.PullLastenheft(prProject, 60)
	if l.Conclusion() != "success" || l.Digest() == d0 {
		t.Fatalf("after: %s %v", l.Conclusion(), l.Digest() == d0)
	}

	// Ein neuer Commit ersetzt den Head; ein Ereignis ohne Commit lässt ihn stehen; eine
	// Angabe, die keine volle SHA ist, wird nicht gespeichert.
	ev.Action, ev.HeadSHA = "synchronize", lhHead2
	if _, err := st.RecordPullRequest(ev); err != nil {
		t.Fatal(err)
	}
	ev.Action, ev.HeadSHA = "edited", ""
	if _, err := st.RecordPullRequest(ev); err != nil {
		t.Fatal(err)
	}
	ev.HeadSHA = "abc"
	if _, err := st.RecordPullRequest(ev); err != nil {
		t.Fatal(err)
	}
	if targets, _ := st.LastenheftTargets(); len(targets) != 1 || targets[0].HeadSHA != lhHead2 {
		t.Fatalf("head after the events: %+v", targets)
	}
	// Ein geschlossener PR ist kein Ziel mehr.
	ev.State = "closed"
	if _, err := st.RecordPullRequest(ev); err != nil {
		t.Fatal(err)
	}
	if targets, _ := st.LastenheftTargets(); len(targets) != 0 {
		t.Fatalf("a closed pull request is no target: %+v", targets)
	}
}
