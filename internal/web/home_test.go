package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/garden"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestGardenerHarnessNameMatchesTheGardener(t *testing.T) {
	if gardenerHarness != garden.Harness {
		t.Fatalf("web reads harness %q, the gardener writes %q", gardenerHarness, garden.Harness)
	}
}

// gardenNote files a note the way the gardener does.
func gardenNote(t *testing.T, st *store.Store, project string, id int, label, subject, reason string) int64 {
	t.Helper()
	k, ok := garden.Proposal(project, garden.Verdict{Kind: garden.Stale, Reason: reason, Entry: garden.Entry{ID: int64(id), Title: subject}})
	if !ok {
		t.Fatal("no proposal")
	}
	k.Person = "gardener"
	if label == "Korrektur?" {
		k.Title = strings.Replace(k.Title, "Veraltet?", "Korrektur?", 1)
	}
	got, err := st.InsertKnowledge(k)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestReviewSpeaksGermanAndEnglishAndNeverBoth(t *testing.T) {
	for _, tc := range []struct {
		lang        string
		want, avoid []string
	}{
		{"de", []string{"Vom Gärtner", "Alle 1 freigeben"}, []string{"From the gardener", "Approve all"}},
		{"en", []string{"From the gardener", "Approve all 1"}, []string{"Vom Gärtner", "Alle 1 freigeben"}},
	} {
		old := testWebOptions
		testWebOptions = append(append([]Option{}, old...), WithLanguage(tc.lang))
		e := ovEnv(t)
		testWebOptions = old
		gardenNote(t, e.St, shellProject, 31, "Veraltet?", "Entry", "Gone.")
		_, review := fetchPage(t, e.Reviewer, e.Base+"/ui/review")
		for _, w := range tc.want {
			if !strings.Contains(review, w) {
				t.Errorf("%s: lacks %q", tc.lang, w)
			}
		}
		for _, w := range tc.avoid {
			if strings.Contains(review, w) {
				t.Errorf("%s: shows %q", tc.lang, w)
			}
		}
	}
}

// ---- Review: the gardener's report and the bulk decision ----

func gardenReviewEnv(t *testing.T) (shellEnv, []int64, int64) {
	t.Helper()
	e := ovEnv(t)
	ids := []int64{
		gardenNote(t, e.St, shellProject, 41, "Veraltet?", "Roles table docs", "The table was renamed in migration 0042."),
		gardenNote(t, e.St, shellProject, 42, "Korrektur?", "Backup schedule", "The job runs at 03:00 now."),
	}
	hidden := gardenNote(t, e.St, shellHiddenProject, 43, "Veraltet?", "SECRET-ENTRY", "Gone.")
	ovKnowledge(t, e.St, shellProject, "Agent proposal", "staged")
	return e, ids, hidden
}

func TestReviewLeadsWithTheGardenersReportAndOffersOnlyWhatMayBeDone(t *testing.T) {
	e, ids, _ := gardenReviewEnv(t)
	_, page := fetchPage(t, e.Lead, e.Base+"/ui/review")
	for _, want := range []string{"From the gardener", "Roles table docs", "Backup schedule", "The table was renamed in migration 0042.", "Outdated?", "Correction?", "Reject all", "Approve all 2"} {
		if !strings.Contains(page, want) {
			t.Errorf("the report lacks %q", want)
		}
	}
	if strings.Index(page, "Roles table docs") > strings.Index(page, "Agent proposal") {
		t.Error("the gardener does not come first")
	}
	if strings.Contains(page, "SECRET") {
		t.Error("a hidden note is in the report")
	}
	if strings.Contains(page, "[Gärtner]") {
		t.Error("the raw title of the note is shown")
	}
	for _, id := range ids {
		if !strings.Contains(page, `action="/ui/review/`+strconv.FormatInt(id, 10)+`/approve"`) {
			t.Errorf("no approve form for note %d", id)
		}
	}
	// A reviewer may approve but not reject: the page offers no rejecting.
	_, reviewer := fetchPage(t, e.Reviewer, e.Base+"/ui/review")
	if !strings.Contains(reviewer, "Approve all 2") || strings.Contains(reviewer, "Reject all") || strings.Contains(reviewer, "/reject") {
		t.Error("a reviewer is offered to reject, or not to approve")
	}
	_, only := fetchPage(t, e.Lead, e.Base+"/ui/review?source=gardener")
	if strings.Contains(only, "Agent proposal") || !strings.Contains(only, "Roles table docs") {
		t.Error("the gardener filter does not narrow the page")
	}
	_, member := fetchPage(t, e.Member, e.Base+"/ui/review")
	if strings.Contains(member, "Approve all") || strings.Contains(member, `/ui/review/bulk`) {
		t.Error("a member is offered bulk buttons")
	}
}

func entryState(t *testing.T, st *store.Store, id int64) string {
	t.Helper()
	k, err := st.KnowledgeByID(id)
	if err != nil {
		t.Fatal(err)
	}
	return k.Confidence + "/" + k.Status
}

func TestReviewBulkIsAllOrNothingAndChecksEveryEntry(t *testing.T) {
	e, ids, hidden := gardenReviewEnv(t)
	post := func(c *http.Client, form url.Values) int {
		t.Helper()
		form.Set("csrf_token", renderedCSRFToken(t, c, e.Base+"/ui/review"))
		resp := sameOriginPostForm(t, c, e.Base+"/ui/review/bulk", form)
		resp.Body.Close()
		return resp.StatusCode
	}
	idv := func(list ...int64) []string {
		var out []string
		for _, id := range list {
			out = append(out, strconv.FormatInt(id, 10))
		}
		return out
	}
	// A note the reviewer may not read: the whole batch answers "not found", none changes.
	if code := post(e.Reviewer, url.Values{"verdict": {"approve"}, "id": idv(ids[0], hidden)}); code != http.StatusNotFound {
		t.Errorf("a batch with a hidden note = %d, want 404", code)
	}
	if got := entryState(t, e.St, ids[0]); got != "staged/active" {
		t.Errorf("a refused batch changed an entry: %s", got)
	}
	// A plain member may not verify.
	if code := post(e.Member, url.Values{"verdict": {"approve"}, "id": idv(ids...)}); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Errorf("member bulk = %d", code)
	}
	if got := entryState(t, e.St, ids[1]); got != "staged/active" {
		t.Errorf("a member changed an entry: %s", got)
	}
	// Without CSRF nothing happens.
	resp := sameOriginPostForm(t, e.Reviewer, e.Base+"/ui/review/bulk", url.Values{"verdict": {"approve"}, "id": idv(ids...)})
	resp.Body.Close()
	if resp.StatusCode == http.StatusSeeOther || resp.StatusCode == http.StatusOK {
		t.Errorf("bulk without a token = %d", resp.StatusCode)
	}
	// A verdict that does not exist.
	if code := post(e.Reviewer, url.Values{"verdict": {"delete"}, "id": idv(ids...)}); code != http.StatusNotFound {
		t.Errorf("unknown verdict = %d", code)
	}
	// The reviewer approves one note.
	if code := post(e.Reviewer, url.Values{"verdict": {"approve"}, "id": idv(ids[0])}); code != http.StatusSeeOther {
		t.Fatalf("bulk approve = %d", code)
	}
	if got := entryState(t, e.St, ids[0]); got != "verified/active" {
		t.Errorf("approved note = %s", got)
	}
	// A batch with a note that is decided already is refused whole.
	if code := post(e.Reviewer, url.Values{"verdict": {"approve"}, "id": idv(ids...)}); code != http.StatusConflict {
		t.Errorf("a batch with a decided note = %d, want 409", code)
	}
	if got := entryState(t, e.St, ids[1]); got != "staged/active" {
		t.Errorf("a refused batch changed the other entry: %s", got)
	}
	// The open one alone goes through.
	if code := post(e.Reviewer, url.Values{"verdict": {"reject"}, "id": idv(ids[1])}); code != http.StatusForbidden {
		t.Errorf("a reviewer rejecting in bulk = %d, want 403", code)
	}
	if code := post(e.Lead, url.Values{"verdict": {"reject"}, "id": idv(ids[1]), "source": {"gardener"}}); code != http.StatusSeeOther {
		t.Fatalf("bulk reject = %d", code)
	}
	if got := entryState(t, e.St, ids[1]); got != "staged/deprecated" {
		t.Errorf("rejected note = %s", got)
	}
	_, page := fetchPage(t, e.Lead, e.Base+"/ui/review?done=bulk_reject&n=1&source=gardener")
	if !strings.Contains(page, "1 note rejected") {
		t.Errorf("no result line after the bulk decision: %s", page)
	}
	if entryState(t, e.St, hidden) != "staged/active" {
		t.Error("the hidden note changed")
	}
}

func TestQuestionTitleCutsWhereTheQuestionEnds(t *testing.T) {
	long := "Die Migration setzt prospects.source auf NOT NULL. In Prod gibt es 214 Zeilen ohne Quelle. Backfill mit 'import' oder verwerfen? Ohne Antwort lasse ich sie liegen und baue weiter."
	if got, want := questionTitle(long, 80), "Backfill mit 'import' oder verwerfen?"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := questionTitle("Kurz?", 80); got != "Kurz?" {
		t.Errorf("short text must stay: %q", got)
	}
	if got := questionTitle(strings.Repeat("ohne Frage ", 20), 40); !strings.HasSuffix(got, "…") {
		t.Errorf("no question: plain cut expected, got %q", got)
	}
}

func TestShortWordsCutsAtAWordBoundary(t *testing.T) {
	got := shortWords("Der Import schneidet bei Zeile 50.000 ab, weil der Puffer ohne Fehler überläuft", 40)
	if got != "Der Import schneidet bei Zeile 50.000…" && !strings.HasSuffix(got, "…") {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(got, "abz") || strings.HasSuffix(strings.TrimSuffix(got, "…"), " ") {
		t.Errorf("cut mid-word or left a space before the ellipsis: %q", got)
	}
	if got := shortWords("kurz", 40); got != "kurz" {
		t.Errorf("short text must stay: %q", got)
	}
	if got := shortWords(strings.Repeat("x", 50), 40); got != strings.Repeat("x", 40)+"…" {
		t.Errorf("no space: hard cut expected, got %q", got)
	}
}
