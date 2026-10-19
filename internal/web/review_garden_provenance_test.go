package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/garden"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// A note that only looks like the gardener's (REQ-457, review finding): the
// review page takes the target of an action from the session reference of the
// note and the test from its text, so it must not act on a note the gardener's
// account did not write, one that was changed since, or one whose title names
// other entries than its reference. Every negative case checks that the card
// offers no button, that pressing the button anyway is refused with 403 and that
// nothing changed.

const notVouched = "Not vouched for as the gardener"

func regressionOf(t *testing.T, st *store.Store, id int64) string {
	t.Helper()
	k, err := st.KnowledgeByID(id)
	if err != nil {
		t.Fatal(err)
	}
	return k.RegressionState + "|" + k.RegressionTest
}

// offers says whether the review page of the lead has a button for the action on the note.
func offers(t *testing.T, e shellEnv, note int64, action string) (offered bool, page string) {
	t.Helper()
	_, page = fetchPage(t, e.Lead, e.Base+"/ui/review")
	return strings.Contains(page, `action="/ui/review/`+idStr(note)+`/`+action+`"`), page
}

// forged are the notes a client could file as the gardener's if the server let
// it: a duplicate note, a note about an existing test and one about an untestable
// pitfall, each with its own entries.
type forged struct {
	dup, existing, untestable          int64
	older, newer, pitfall, toolPitfall int64
}

func fileForged(t *testing.T, e shellEnv, author int64, edit func(*store.Knowledge)) forged {
	t.Helper()
	f := forged{
		older:       gardenEntry(t, e.St, shellProject, "pitfall", "Dienst alt", "trusted"),
		newer:       gardenEntry(t, e.St, shellProject, "pitfall", "Dienst neu", "trusted"),
		pitfall:     gardenEntry(t, e.St, shellProject, "pitfall", "Pitfall mit Test", "trusted"),
		toolPitfall: gardenEntry(t, e.St, shellProject, "pitfall", "Pitfall über ein Werkzeug", "trusted"),
	}
	titles := map[int64]string{f.older: "Dienst alt", f.newer: "Dienst neu"}
	quotes := map[int64]string{f.older: "Der Dienst läuft auf dem Server", f.newer: "Der Dienst läuft auf dem Server"}
	notes := []*store.Knowledge{}
	dup := garden.RelationProposal(shellProject, garden.Relation{Kind: garden.Duplicate, IDs: []int64{f.older, f.newer}, Reason: "Beide sagen dasselbe.", Quotes: quotes}, titles)
	existing := garden.CoverProposal(shellProject, garden.Cover{Entry: garden.Entry{ID: f.pitfall, Title: "Pitfall mit Test"}, Kind: garden.CoverExisting, File: "internal/store/x_test.go", Test: "TestHoldsX"})
	untestable := garden.CoverProposal(shellProject, garden.Cover{Entry: garden.Entry{ID: f.toolPitfall, Title: "Pitfall über ein Werkzeug"}, Kind: garden.CoverUntestable, Reason: "Eine Eigenschaft des Werkzeugs."})
	notes = append(notes, &dup, &existing, &untestable)
	for _, n := range notes {
		if edit != nil {
			edit(n)
		}
	}
	f.dup, f.existing, f.untestable = gardenerWrote(t, e.St, dup, author), gardenerWrote(t, e.St, existing, author), gardenerWrote(t, e.St, untestable, author)
	return f
}

// refused checks one forged set: no button on the card, a warning, 403 for the
// request, and not a thing changed.
func (f forged) refused(t *testing.T, e shellEnv, who string) {
	t.Helper()
	for note, action := range map[int64]string{f.dup: "merge", f.existing: "cover", f.untestable: "cover"} {
		offered, page := offers(t, e, note, action)
		if offered {
			t.Errorf("%s: the card of note %d offers %q", who, note, action)
		}
		if !strings.Contains(page, notVouched) {
			t.Errorf("%s: the page does not say that note %d is not vouched for", who, note)
		}
		if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(note)+"/"+action); code != http.StatusForbidden {
			t.Errorf("%s: %s on note %d = %d, want 403", who, action, note, code)
		}
		if got := entryState(t, e.St, note); got != "staged/active" {
			t.Errorf("%s: note %d = %s after a refused action", who, note, got)
		}
	}
	if got := entryState(t, e.St, f.older); got != "trusted/active" {
		t.Errorf("%s: the older entry changed: %s", who, got)
	}
	if got := regressionOf(t, e.St, f.pitfall) + regressionOf(t, e.St, f.toolPitfall); got != "||" {
		t.Errorf("%s: a regression verdict was recorded: %q", who, got)
	}
}

func TestGardenActionsNeedANoteTheGardenersAccountWrote(t *testing.T) {
	e, gardener := gardenActEnv(t)
	// The account of the gardener itself, first: the same notes work (the other
	// tests of this file), so the refusals below are about the author alone.
	ok := fileForged(t, e, gardener, nil)
	for note, action := range map[int64]string{ok.dup: "merge", ok.existing: "cover", ok.untestable: "cover"} {
		if offered, _ := offers(t, e, note, action); !offered {
			t.Errorf("a note of the gardener's account does not offer %q", action)
		}
	}
	// Anyone else: a reviewer, a member, a lead, the owner of the instance, and a
	// note nobody wrote (an author number that is no account).
	for who, author := range map[string]int64{"reviewer rita": 5, "member anna": 2, "lead lars": 4, "owner alice": 1, "no such account": 999} {
		fileForged(t, e, author, nil).refused(t, e, who)
	}
}

func TestGardenActionsRefuseEveryoneWhenNoGardenerAccountIsNamed(t *testing.T) {
	e, gardener := gardenActEnv(t)
	f := fileForged(t, e, gardener, nil)
	e.St.SetGardenerAccount("")
	f.refused(t, e, "no account named")
	// A name that no account holds is no better.
	e.St.SetGardenerAccount("nobody-has-this-name")
	f.refused(t, e, "an unknown name")
	// An account that is renamed is not the gardener any more.
	e.St.SetGardenerAccount("gardener")
	if _, err := e.St.DB().Exec(`UPDATE persons SET name='gardener-renamed' WHERE name='gardener'`); err != nil {
		t.Fatal(err)
	}
	f.refused(t, e, "a renamed account")
	if _, err := e.St.DB().Exec(`UPDATE persons SET name='gardener' WHERE name='gardener-renamed'`); err != nil {
		t.Fatal(err)
	}
	if offered, _ := offers(t, e, f.dup, "merge"); !offered {
		t.Error("the actions do not come back with the name")
	}
}

// The title of a note says which entries it is about, and so does its reference.
// A note whose title names other entries than its reference is refused, for all
// three kinds that have an action.
func TestGardenActionsRefuseATitleThatNamesOtherEntriesThanTheReference(t *testing.T) {
	e, gardener := gardenActEnv(t)
	number := regexp.MustCompile(`#(\d+)`)
	f := fileForged(t, e, gardener, func(n *store.Knowledge) {
		// The reference names the real entries, the title names entries that are none of them.
		n.Title = number.ReplaceAllStringFunc(n.Title, func(m string) string {
			id, _ := strconv.ParseInt(m[1:], 10, 64)
			return "#" + idStr(id+1000)
		})
	})
	f.refused(t, e, "a title that names other entries")
}

// The title is checked against the reference even when nobody changed the note:
// a note the gardener's account wrote with such a title is refused too.
func TestGardenActionsCheckTheTitleAgainstTheReferenceOnItsOwn(t *testing.T) {
	e, gardener := gardenActEnv(t)
	real := gardenEntry(t, e.St, shellProject, "pitfall", "Der echte Pitfall", "trusted")
	other := gardenEntry(t, e.St, shellProject, "pitfall", "Ein ganz anderer Pitfall", "trusted")
	note := garden.CoverProposal(shellProject, garden.Cover{Entry: garden.Entry{ID: real, Title: "Der echte Pitfall"}, Kind: garden.CoverUntestable, Reason: "Eine Eigenschaft des Werkzeugs."})
	note.Title = "[Gärtner] Nicht testbar? #" + idStr(other) + ": Ein ganz anderer Pitfall"
	id := gardenerWrote(t, e.St, note, gardener)

	offered, page := offers(t, e, id, "cover")
	if offered {
		t.Error("a note whose title names another entry than its reference offers an action")
	}
	if !strings.Contains(page, notVouched) {
		t.Error("the page does not say that the note is not vouched for")
	}
	// The card shows the entry the reference names, with title and link, not the title's claim.
	if !strings.Contains(page, `href="/ui/knowledge/`+idStr(real)+`"`) || !strings.Contains(page, "#"+idStr(real)+" Der echte Pitfall") {
		t.Errorf("the card does not show the entry of the reference:\n%s", page)
	}
	if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(id)+"/cover"); code != http.StatusForbidden {
		t.Errorf("cover = %d, want 403", code)
	}
	if got := regressionOf(t, e.St, real) + regressionOf(t, e.St, other); got != "||" {
		t.Errorf("a verdict was recorded: %q", got)
	}

	// The same for a duplicate note: the title names #other, the reference the two real entries.
	a := gardenEntry(t, e.St, shellProject, "pitfall", "Dienst alt", "trusted")
	b := gardenEntry(t, e.St, shellProject, "pitfall", "Dienst neu", "trusted")
	dup := garden.RelationProposal(shellProject, garden.Relation{Kind: garden.Duplicate, IDs: []int64{a, b}, Reason: "r",
		Quotes: map[int64]string{a: "Der Dienst läuft auf dem Server", b: "Der Dienst läuft auf dem Server"}}, map[int64]string{a: "Dienst alt", b: "Dienst neu"})
	dup.Title = "[Gärtner] Duplikat? #" + idStr(a) + " und #" + idStr(other) + ": Dienst alt"
	did := gardenerWrote(t, e.St, dup, gardener)
	if offered, _ := offers(t, e, did, "merge"); offered {
		t.Error("a duplicate note with a foreign title offers to merge")
	}
	if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(did)+"/merge"); code != http.StatusForbidden {
		t.Errorf("merge = %d, want 403", code)
	}
	for _, id := range []int64{a, b, other} {
		if got := entryState(t, e.St, id); got != "trusted/active" {
			t.Errorf("#%d changed: %s", id, got)
		}
	}
}

// A note that somebody edited is not what the gardener wrote any more. The one
// way to edit it is the edit form of the entry, so that is the path tried.
func TestGardenActionsRefuseANoteThatWasEditedAfterTheGardenerWroteIt(t *testing.T) {
	e, gardener := gardenActEnv(t)
	f := fileForged(t, e, gardener, nil)
	edit := func(note int64, mutate func(body string) string) {
		t.Helper()
		k, err := e.St.KnowledgeByID(note)
		if err != nil {
			t.Fatal(err)
		}
		form := url.Values{"csrf_token": {renderedCSRFToken(t, e.Lead, e.Base+"/ui/review")}, "title": {k.Title}, "type": {k.Type}, "body": {mutate(k.Body)}}
		resp := sameOriginPostForm(t, e.Lead, e.Base+"/ui/knowledge/"+idStr(note)+"/edit", form)
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("edit of note %d = %d", note, resp.StatusCode)
		}
	}
	// The test line is what would be recorded: change it to another test.
	edit(f.existing, func(b string) string {
		return strings.Replace(b, "internal/store/x_test.go:TestHoldsX", "internal/store/evil_test.go:TestEvil", 1)
	})
	edit(f.untestable, func(b string) string { return b + "\nAuch das sagt der Gärtner." })
	edit(f.dup, func(b string) string { return b + "\nNachtrag." })
	k, _ := e.St.KnowledgeByID(f.existing)
	if !strings.Contains(k.Body, "TestEvil") || k.Confidence != "staged" {
		t.Fatalf("the edit did not apply or changed the level: %+v", k)
	}
	f.refused(t, e, "an edited note")
}

// The test a note names is recorded as the note holds it, so it has to have the
// shape of a test: a relative path and a function name. The server has no
// checkout to look the test up in; the shape, the author, the title and the
// unedited text are what it can vouch for, and the card shows the test whole.
func TestRegressionNoteWithATestOfAnUnusualShapeIsNeverRecorded(t *testing.T) {
	e, gardener := gardenActEnv(t)
	for name, c := range map[string]garden.Cover{
		"up and out":       {File: "../../etc/passwd_test.go", Test: "TestX"},
		"a space in path":  {File: "a b/x_test.go", Test: "TestX"},
		"absolute":         {File: "/abs/x_test.go", Test: "TestX"},
		"inside .git":      {File: ".git/hooks/x_test.go", Test: "TestX"},
		"a dot segment":    {File: "a/./x_test.go", Test: "TestX"},
		"a space in name":  {File: "x_test.go", Test: "Test X"},
		"a one-letter":     {File: "x_test.go", Test: "T"},
		"a shell":          {File: "x_test.go", Test: "TestX;rm"},
		"a second colon":   {File: "x_test.go:y", Test: "TestX"},
		"far too long":     {File: strings.Repeat("a/", 200) + "x_test.go", Test: "TestX"},
		"a backslash path": {File: `a\x_test.go`, Test: "TestX"},
	} {
		p := gardenEntry(t, e.St, shellProject, "pitfall", "Pitfall "+name, "trusted")
		c.Kind, c.Entry = garden.CoverExisting, garden.Entry{ID: p, Title: "Pitfall " + name}
		note := gardenerWrote(t, e.St, garden.CoverProposal(shellProject, c), gardener)
		if offered, _ := offers(t, e, note, "cover"); offered {
			t.Errorf("%s: a test of that shape is offered", name)
		}
		if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(note)+"/cover"); code != http.StatusForbidden {
			t.Errorf("%s: cover = %d, want 403", name, code)
		}
		if got := regressionOf(t, e.St, p); got != "|" {
			t.Errorf("%s: recorded %q", name, got)
		}
	}
	// The shapes of real tests are recorded.
	for _, c := range []garden.Cover{
		{File: "internal/store/x_test.go", Test: "TestHoldsX"},
		{File: "web/app.test.js", Test: "checksLogin"},
		{File: "a-b/c_d.go", Test: "Test_X1"},
	} {
		p := gardenEntry(t, e.St, shellProject, "pitfall", "Pitfall "+c.Test, "trusted")
		c.Kind, c.Entry = garden.CoverExisting, garden.Entry{ID: p, Title: "Pitfall " + c.Test}
		note := gardenerWrote(t, e.St, garden.CoverProposal(shellProject, c), gardener)
		_, page := fetchPage(t, e.Lead, e.Base+"/ui/review")
		if !strings.Contains(page, c.File+":"+c.Test) {
			t.Errorf("the card does not show the test %s:%s", c.File, c.Test)
		}
		if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(note)+"/cover"); code != http.StatusSeeOther {
			t.Errorf("%s:%s = %d", c.File, c.Test, code)
		}
		if got := regressionOf(t, e.St, p); got != "covered|"+c.File+":"+c.Test {
			t.Errorf("recorded %q", got)
		}
	}
}

// The request that presses the button carries no target and no test: they come
// from the note. A form with extra fields changes nothing about what is recorded.
func TestPressingTheButtonCarriesNoTargetAndNoTestOfItsOwn(t *testing.T) {
	e, gardener := gardenActEnv(t)
	f := fileForged(t, e, gardener, nil)
	bystander := gardenEntry(t, e.St, shellProject, "pitfall", "Unbeteiligt", "trusted")
	form := url.Values{
		"csrf_token": {renderedCSRFToken(t, e.Lead, e.Base+"/ui/review")},
		"test":       {"internal/store/evil_test.go:TestEvil"}, "state": {"not_applicable"}, "id": {idStr(bystander)},
		"k": {idStr(bystander)}, "pitfall": {idStr(bystander)}, "supersedes": {idStr(bystander)}, "next": {"item"},
	}
	resp := sameOriginPostForm(t, e.Lead, e.Base+"/ui/review/"+idStr(f.existing)+"/cover", form)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("cover = %d", resp.StatusCode)
	}
	if got := regressionOf(t, e.St, f.pitfall); got != "covered|internal/store/x_test.go:TestHoldsX" {
		t.Errorf("the pitfall of the note = %q", got)
	}
	if got := regressionOf(t, e.St, bystander); got != "|" {
		t.Errorf("a field of the form reached another entry: %q", got)
	}
	form.Set("csrf_token", renderedCSRFToken(t, e.Lead, e.Base+"/ui/review"))
	resp = sameOriginPostForm(t, e.Lead, e.Base+"/ui/review/"+idStr(f.dup)+"/merge", form)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("merge = %d", resp.StatusCode)
	}
	if got := entryState(t, e.St, bystander); got != "trusted/active" {
		t.Errorf("a field of the form reached another entry: %s", got)
	}
	if k, _ := e.St.KnowledgeByID(f.older); k.SupersededBy != f.newer {
		t.Errorf("the older entry is superseded by #%d, want #%d", k.SupersededBy, f.newer)
	}
}

// The card of a regression note names the pitfall of the reference, and so does a
// note of the gardener that is only displayed (a sketch).
func TestCardShowsTheEntryTheReferenceNamesWithTitleAndLink(t *testing.T) {
	e, gardener := gardenActEnv(t)
	p := gardenEntry(t, e.St, shellProject, "pitfall", "Der Pitfall, um den es geht", "trusted")
	gardenerWrote(t, e.St, garden.CoverProposal(shellProject, garden.Cover{Entry: garden.Entry{ID: p, Title: "Der Pitfall, um den es geht"}, Kind: garden.CoverSketch,
		File: "internal/x/x_test.go", Test: "TestNewX", Steps: "eins\nzwei\ndrei"}), gardener)
	_, page := fetchPage(t, e.Lead, e.Base+"/ui/review")
	for _, want := range []string{"Concerns:", `href="/ui/knowledge/` + idStr(p) + `"`, "#" + idStr(p) + " Der Pitfall, um den es geht", "internal/x/x_test.go:TestNewX"} {
		if !strings.Contains(page, want) {
			t.Errorf("the card lacks %q", want)
		}
	}
	if strings.Contains(page, notVouched) {
		t.Error("a note of the gardener's account carries the warning")
	}
	// A sketch of somebody else's is displayed with the warning and has no action either way.
	q := gardenEntry(t, e.St, shellProject, "pitfall", "Ein zweiter Pitfall", "trusted")
	gardenerWrote(t, e.St, garden.CoverProposal(shellProject, garden.Cover{Entry: garden.Entry{ID: q, Title: "Ein zweiter Pitfall"}, Kind: garden.CoverSketch,
		File: "internal/x/y_test.go", Test: "TestNewY", Steps: "eins\nzwei\ndrei"}), 5)
	_, page = fetchPage(t, e.Lead, e.Base+"/ui/review")
	if !strings.Contains(page, notVouched) {
		t.Error("a sketch of another account carries no warning")
	}
}

func TestGardenNoteShapesAreReadStrictly(t *testing.T) {
	for title, want := range map[string][]int64{
		"[Gärtner] Duplikat? #12 und #15: Titel":       {12, 15},
		"[Gärtner] Widerspruch? #3 und #4 und #5: x":   {3, 4, 5},
		"[Gärtner] Test fehlt? #7: Pitfall: Teil":      {7},
		"[Gärtner] Test vorhanden? #7: ein #8 im Text": {7},
	} {
		got, ok := titleIDs(title)
		if !ok || !sameIDs(got, want) {
			t.Errorf("%q read as %v %v, want %v", title, got, ok, want)
		}
	}
	for _, title := range []string{"", "[Gärtner] ohne Nummer: x", "[Gärtner] Test? #x: y", "[Gärtner] Test? #0: y", "[Gärtner] Test? #7", "[Gärtner] Test? #7 und #: y", "[Gärtner] Test? #-1: y"} {
		if got, ok := titleIDs(title); ok {
			t.Errorf("%q read as %v", title, got)
		}
	}
	if !sameIDs([]int64{2, 1}, []int64{1, 2}) || sameIDs([]int64{1, 2}, []int64{1, 2, 3}) || sameIDs([]int64{1, 1}, []int64{1, 2}) {
		t.Error("sameIDs")
	}
	for _, ok := range []string{"internal/store/x_test.go:TestHoldsX", "x_test.go:TestX", "web/app.test.js:checksLogin", "a-b/c_d.go:Test_X1", "a.b/c:Ab"} {
		if !validTestID(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "x_test.go", ":TestX", "x_test.go:", "../x_test.go:TestX", "a/../x_test.go:TestX", "/x_test.go:TestX", "a//x_test.go:TestX", "a/x_test.go:Test X",
		"a b/x_test.go:TestX", "x_test.go:TestX\n", "x_test.go:T", ".git/x_test.go:TestX", "./x_test.go:TestX", "x_test.go:1Test", "x_test.go:Test-X"} {
		if validTestID(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
