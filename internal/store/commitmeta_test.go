package store

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSplitCommitMessageKeepsTheBodyAndTakesCoAuthorsWithoutAddresses(t *testing.T) {
	msg := "feat: title\r\n\r\nFirst paragraph.\n\n\n\nSecond   paragraph\x00 with a control.\n\nCo-authored-by: Mia Muster <mia@example.org>\nco-authored-by: claude <noreply@anthropic.com>\nSigned-off-by: x"
	body, co := splitCommitMessage(msg)
	if want := []string{"Mia Muster", "claude"}; !reflect.DeepEqual(co, want) {
		t.Fatalf("co-authors %q, want %q", co, want)
	}
	if strings.Contains(body, "@") || strings.Contains(body, "Co-authored") || strings.Contains(body, "feat: title") || strings.ContainsRune(body, 0) {
		t.Fatalf("body must carry neither title, trailer, address nor control character: %q", body)
	}
	if !strings.Contains(body, "First paragraph.\n\nSecond") || strings.Contains(body, "\n\n\n") {
		t.Fatalf("paragraphs must stay, runs of blank lines must collapse: %q", body)
	}
	if b, c := splitCommitMessage("only a title"); b != "" || c != nil {
		t.Fatalf("a title alone has no body: %q %v", b, c)
	}
	long := "t\n\n" + strings.Repeat("ä", maxCommitBodyRunes+50)
	if b, _ := splitCommitMessage(long); len([]rune(b)) != maxCommitBodyRunes {
		t.Fatalf("a long body is cut to %d runes, got %d", maxCommitBodyRunes, len([]rune(b)))
	}
	var many strings.Builder
	many.WriteString("t\n\n")
	for i := 0; i < 30; i++ {
		many.WriteString("Co-authored-by: P <p@x>\n")
	}
	if _, c := splitCommitMessage(many.String()); len(c) != maxCoAuthors {
		t.Fatalf("%d co-authors kept, want %d", len(c), maxCoAuthors)
	}
}

const metaSha = "cccccc33333333333333333333333333333333cc"
const metaParent = "dddddd44444444444444444444444444444444dd"

func metaStore(t *testing.T) (*Store, string) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.AddPerson("robin"); err != nil {
		t.Fatal(err)
	}
	org, err := st.CreateOrg("person:1", "Deadweight", "dw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ConnectSourceWithInstallation("person:1", org.ID, "github", "deadweight-labs", 99, nil); err != nil {
		t.Fatal(err)
	}
	const project = "github.com/deadweight-labs/app"
	if _, err := st.ClaimProject("person:1", project, "dw"); err != nil {
		t.Fatal(err)
	}
	if res, err := st.RecordPullRequest(PullRequestChange{InstallationID: 99, Remote: project, HeadRemote: project, Number: 7, Action: "opened", Title: "A", Head: "feat/a", Base: "main", State: "open"}); err != nil || !res.Applied {
		t.Fatalf("pull: %+v %v", res, err)
	}
	if res, err := st.RecordPullCommits(PullCommitsChange{InstallationID: 99, Remote: project, Number: 7, Commits: []PullCommit{{
		SHA: metaSha, Author: "mia-gh", AuthorName: "Mia", Title: "feat: title", CommittedAt: "2026-11-08T10:00:00Z",
		Message: "feat: title\n\nWhy it was done.\n\nCo-authored-by: claude <noreply@anthropic.com>", Parents: []string{metaParent, "nothex"}}}}); err != nil || res.Recorded != 1 {
		t.Fatalf("commits: %+v %v", res, err)
	}
	return st, project
}

func TestCommitViewCarriesParentsMessageAndCoAuthorsFromThePullSource(t *testing.T) {
	st, project := metaStore(t)
	d, err := st.Access(Principal{ID: "person:1"}).CommitView(project, metaSha)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(d.Parents, []string{metaParent}) || d.Message != "Why it was done." || !reflect.DeepEqual(d.CoAuthors, []string{"claude"}) {
		t.Fatalf("detail: parents %q message %q co %q", d.Parents, d.Message, d.CoAuthors)
	}
	if d.FilesKnown || d.CI != "" {
		t.Fatalf("files and checks are unknown until they were fetched: %+v", d.CommitInfo)
	}
}

func TestRecordCommitDetailsStoresFilesAndChecksAndKeepsWhatWasNotFetched(t *testing.T) {
	st, project := metaStore(t)
	files := []CommitFileStat{{Path: "internal/web/a.go", Additions: 12, Deletions: 3}, {Path: "bad\npath.go", Additions: -4, Deletions: 1}, {Path: "  ", Additions: 1}}
	ok, err := st.RecordCommitDetails(CommitDetailsChange{InstallationID: 99, Remote: project, SHA: metaSha, HaveFiles: true, Files: files, FilesTotal: 400, Additions: 100, Deletions: 20})
	if err != nil || !ok {
		t.Fatalf("files: %v %v", ok, err)
	}
	// Prüfstand allein: die Dateien bleiben.
	if ok, err := st.RecordCommitDetails(CommitDetailsChange{InstallationID: 99, Remote: project, SHA: metaSha, HaveCI: true, CI: CommitCIFailure}); err != nil || !ok {
		t.Fatalf("ci: %v %v", ok, err)
	}
	d, err := st.Access(Principal{ID: "person:1"}).CommitView(project, metaSha)
	if err != nil {
		t.Fatal(err)
	}
	if !d.FilesKnown || len(d.Files) != 2 || d.Files[0].Path != "internal/web/a.go" || d.Files[1].Path != "bad path.go" || d.Files[1].Additions != 0 {
		t.Fatalf("files: %+v", d.Files)
	}
	if d.FilesTotal != 400 || d.Additions != 100 || d.Deletions != 20 || d.CI != CommitCIFailure {
		t.Fatalf("totals: %+v", d.CommitInfo)
	}
	// Eltern, Nachricht und Mit-Autoren aus der Liste überschreiben weder Dateien noch Prüfstand.
	if _, err := st.RecordPullCommits(PullCommitsChange{InstallationID: 99, Remote: project, Number: 7, Commits: []PullCommit{{SHA: metaSha, Title: "feat: title", Message: "feat: title\n\nnew words", Parents: []string{metaParent}}}}); err != nil {
		t.Fatal(err)
	}
	d, _ = st.Access(Principal{ID: "person:1"}).CommitView(project, metaSha)
	if d.Message != "new words" || len(d.Files) != 2 || d.CI != CommitCIFailure {
		t.Fatalf("after a second list: %+v", d.CommitInfo)
	}
}

func TestRecordCommitDetailsRefusesWhatIsNotTheirs(t *testing.T) {
	st, project := metaStore(t)
	good := CommitDetailsChange{InstallationID: 99, Remote: project, SHA: metaSha, HaveCI: true, CI: CommitCISuccess}
	for name, c := range map[string]CommitDetailsChange{
		"foreign installation":   {InstallationID: 5, Remote: project, SHA: metaSha, HaveCI: true, CI: CommitCISuccess},
		"unknown commit":         {InstallationID: 99, Remote: project, SHA: "eeeeee55555555555555555555555555555555ee", HaveCI: true, CI: CommitCISuccess},
		"foreign project":        {InstallationID: 99, Remote: "github.com/other/app", SHA: metaSha, HaveCI: true, CI: CommitCISuccess},
		"invalid sha":            {InstallationID: 99, Remote: project, SHA: "abc", HaveCI: true, CI: CommitCISuccess},
		"invalid state":          {InstallationID: 99, Remote: project, SHA: metaSha, HaveCI: true, CI: "weird"},
		"nothing to record":      {InstallationID: 99, Remote: project, SHA: metaSha},
		"installation of nobody": {Remote: project, SHA: metaSha, HaveCI: true, CI: CommitCISuccess},
	} {
		if ok, err := st.RecordCommitDetails(c); err != nil || ok {
			t.Errorf("%s: applied=%v err=%v", name, ok, err)
		}
	}
	if d, _ := st.Access(Principal{ID: "person:1"}).CommitView(project, metaSha); d.CI != "" {
		t.Fatalf("a refused change left a trace: %q", d.CI)
	}
	if ok, err := st.RecordCommitDetails(good); err != nil || !ok {
		t.Fatalf("the good one: %v %v", ok, err)
	}
}

func TestCommitsNeedingDetailsListsWhatIsMissingOrStillRunning(t *testing.T) {
	st, project := metaStore(t)
	need := func() []string {
		t.Helper()
		got, err := st.CommitsNeedingDetails(project, 10, true, true)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := need(); !reflect.DeepEqual(got, []string{metaSha}) {
		t.Fatalf("fresh commit: %v", got)
	}
	st.RecordCommitDetails(CommitDetailsChange{InstallationID: 99, Remote: project, SHA: metaSha, HaveFiles: true, HaveCI: true, CI: CommitCIPending})
	if got := need(); len(got) != 1 {
		t.Fatalf("a running check is asked again: %v", got)
	}
	st.RecordCommitDetails(CommitDetailsChange{InstallationID: 99, Remote: project, SHA: metaSha, HaveCI: true, CI: CommitCISuccess})
	if got := need(); len(got) != 0 {
		t.Fatalf("done: %v", got)
	}
	// Ohne Prüfung gelaufen (leerer Stand) ist auch fertig: es wird nicht ewig gefragt.
	st.RecordCommitDetails(CommitDetailsChange{InstallationID: 99, Remote: project, SHA: metaSha, HaveCI: true, CI: ""})
	if got := need(); len(got) != 0 {
		t.Fatalf("no checks at all: %v", got)
	}
}

// Fehlt der App nur ein Recht, bleibt die zugehörige Gruppe für immer leer. Fragt
// der Aufrufer sie dann nicht mehr an, darf der Commit nicht in der Warteschlange
// bleiben (sonst holt jeder Lauf dieselben neuesten Commits neu und die älteren
// kommen nie dran).
func TestCommitsNeedingDetailsFiltersEachGroupByItsOwnMarker(t *testing.T) {
	st, project := metaStore(t)
	need := func(files, ci bool) []string {
		t.Helper()
		got, err := st.CommitsNeedingDetails(project, 10, files, ci)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	// Dateien sind da, der Prüfstand fehlt, weil die App "Checks: read" nicht hat.
	st.RecordCommitDetails(CommitDetailsChange{InstallationID: 99, Remote: project, SHA: metaSha, HaveFiles: true})
	if got := need(true, true); len(got) != 1 {
		t.Fatalf("with both groups the commit still lacks its checks: %v", got)
	}
	if got := need(true, false); len(got) != 0 {
		t.Fatalf("without the checks group the commit is done and must leave the queue: %v", got)
	}
	if got := need(false, true); len(got) != 1 {
		t.Fatalf("the checks group alone still wants it: %v", got)
	}
	if got := need(false, false); len(got) != 0 {
		t.Fatalf("nothing asked, nothing listed: %v", got)
	}
	// Umgekehrt: nur der Prüfstand ist da, "Contents: read" fehlt.
	st2, project2 := metaStore(t)
	st2.RecordCommitDetails(CommitDetailsChange{InstallationID: 99, Remote: project2, SHA: metaSha, HaveCI: true, CI: CommitCISuccess})
	if got, _ := st2.CommitsNeedingDetails(project2, 10, false, true); len(got) != 0 {
		t.Fatalf("without the files group the commit is done: %v", got)
	}
	if got, _ := st2.CommitsNeedingDetails(project2, 10, true, false); len(got) != 1 {
		t.Fatalf("the files group alone still wants it: %v", got)
	}
	// Eine laufende Prüfung bleibt Sache der Prüfstand-Gruppe.
	st2.RecordCommitDetails(CommitDetailsChange{InstallationID: 99, Remote: project2, SHA: metaSha, HaveFiles: true, HaveCI: true, CI: CommitCIPending})
	if got, _ := st2.CommitsNeedingDetails(project2, 10, true, false); len(got) != 0 {
		t.Fatalf("a running check is none of the files group's business: %v", got)
	}
	if got, _ := st2.CommitsNeedingDetails(project2, 10, false, true); len(got) != 1 {
		t.Fatalf("a running check is asked again: %v", got)
	}
}

func TestCommitMetaSchemaIsAdditive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`DROP TABLE git_commit_meta`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	for round := 1; round <= 2; round++ {
		st, err = Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", round, err)
		}
		var n int
		if err := st.DB().QueryRow(`SELECT COUNT(*) FROM git_commit_meta`).Scan(&n); err != nil {
			t.Fatalf("open %d: %v", round, err)
		}
		st.Close()
	}
}
