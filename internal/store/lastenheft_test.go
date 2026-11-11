package store

import (
	"reflect"
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
	private := true
	ev.RepoPrivate = &private
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
	if err := st.MarkLastenheftPublished(prProject, 60, lhHead1, d0, 0); err != nil {
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

// Der Check-Run geht nur an private Repos: ein öffentliches Repo (oder eines, dessen
// Sichtbarkeit unbekannt ist) ergibt kein Ziel, auch wenn es ein Paket und einen
// Head-Commit hat. Die Sichtbarkeit kommt von GitHub.
func TestLastenheftTargetsAreOnlyPrivateRepos(t *testing.T) {
	st := prFixture(t)
	req := pkgRequestIn(t, st, prProject, "Pflicht")
	if _, err := st.CreatePackage(requestdomain.PackageInput{Project: prProject, Branch: "feat/vis", Name: "P", Person: "robin",
		Members: []requestdomain.PackageMember{{RequestID: req}}}); err != nil {
		t.Fatal(err)
	}
	ev := prEvent(61, "opened", "open", "feat/vis")
	ev.HeadSHA = lhHead1
	if _, err := st.RecordPullRequest(ev); err != nil {
		t.Fatal(err)
	}
	if got, err := st.LastenheftTargets(); err != nil || len(got) != 0 {
		t.Fatalf("unknown visibility must not be a target: %+v %v", got, err)
	}
	if private, err := st.RepoPrivate(prProject); err != nil || private {
		t.Fatalf("unknown is not private: %v %v", private, err)
	}
	if err := st.SetRepoVisibility([]string{"gitlab.com/x/y", "not a remote"}, []string{prProject}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.LastenheftTargets(); len(got) != 0 {
		t.Fatalf("a public repo is no target: %+v", got)
	}
	if private, _ := st.RepoPrivate("gitlab.com/x/y"); private {
		t.Error("only GitHub remotes are recorded")
	}
	if err := st.SetRepoVisibility([]string{prProject}, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.LastenheftTargets(); len(got) != 1 {
		t.Fatalf("a private repo is a target: %+v", got)
	}
	// Ein späteres Ereignis, das das Repo als öffentlich meldet, nimmt es wieder heraus.
	public := false
	ev.RepoPrivate = &public
	if _, err := st.RecordPullRequest(ev); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.LastenheftTargets(); len(got) != 0 {
		t.Fatalf("made public: %+v", got)
	}
}

// Webhooks kommen nicht in Reihenfolge an: ein älteres Ereignis überschreibt den
// Head-Commit eines neueren nicht.
func TestHeadSHAIgnoresAnOlderEvent(t *testing.T) {
	st := prFixture(t)
	head := func() string {
		var sha string
		if err := st.db.QueryRow(`SELECT head_sha FROM git_pull_requests WHERE project=? AND number=62`, prProject).Scan(&sha); err != nil {
			t.Fatal(err)
		}
		return sha
	}
	at := func(sha, when string) {
		ev := prEvent(62, "synchronize", "open", "feat/order")
		ev.HeadSHA, ev.HeadAt = sha, when
		if _, err := st.RecordPullRequest(ev); err != nil {
			t.Fatal(err)
		}
	}
	at(lhHead1, "2026-11-11T10:00:00Z")
	at(lhHead2, "2026-11-11T10:05:00Z")
	at(lhHead1, "2026-11-11T10:00:00Z") // verspätet
	if got := head(); got != lhHead2 {
		t.Fatalf("an older event replaced the head: %s", got)
	}
	at(lhHead1, "2026-11-11T10:09:00+00:00") // wirklich neuer, andere Schreibweise
	if got := head(); got != lhHead1 {
		t.Fatalf("a newer event must win: %s", got)
	}
	// Ohne Zeit im Ereignis ersetzt es einen Stand mit Zeit nicht.
	at(lhHead2, "")
	if got := head(); got != lhHead1 {
		t.Fatalf("an undated event replaced a dated head: %s", got)
	}
}

// Eine reine Zahl (Datum, PR-Nummer, Größe) ist keine SHA.
func TestEvidenceShaWordsNeedALetterAndADigit(t *testing.T) {
	got := shasInEvidence("PRs #173 (b0cd1b9), #174 (e33198b), am 20261111, Zahl 1234567, 98765432101, defaced, abcdefa")
	want := []string{"b0cd1b9", "e33198b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("shas: %v, want %v", got, want)
	}
}
