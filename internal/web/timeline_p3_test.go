package web

import (
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// A proof is written as one string, "address (note)"; the page shows the
// address as a pill and the note as plain text beside it.
func TestSplitProofRefPartsTheThingFromTheNote(t *testing.T) {
	for _, tc := range []struct{ kind, ref, thing, note string }{
		{"url", "https://github.com/o/r/pull/163 (gitleaks: no secrets)", "https://github.com/o/r/pull/163", "gitleaks: no secrets"},
		{"url", "https://example.test/a", "https://example.test/a", ""},
		{"url", "https://example.test/a   two words without brackets", "https://example.test/a", "two words without brackets"},
		{"file", `docs/compare.md (PR #169), inklusive "x"`, "docs/compare.md", `(PR #169), inklusive "x"`},
		{"file", "docs/compare.md (shows it)", "docs/compare.md", "shows it"},
		{"decision", "1", "1", ""},
		{"commit", "a1b2c3d (not split)", "a1b2c3d (not split)", ""},
		{"test", "TestX (not split)", "TestX (not split)", ""},
	} {
		thing, note := splitProofRef(tc.kind, tc.ref)
		if thing != tc.thing || note != tc.note {
			t.Errorf("%s %q = (%q, %q), want (%q, %q)", tc.kind, tc.ref, thing, note, tc.thing, tc.note)
		}
	}
}

// The proofs of a requirement and of an entry share one writer: a pill, the
// note as text, nothing in monospace that runs out of the box.
func TestProofChipsWriteThePillAndTheNoteApart(t *testing.T) {
	got := string(proofChips(langDE, []evidenceLine{
		{Kind: "url", Label: "Link", Ref: "https://github.com/o/r/pull/163", Href: "https://github.com/o/r/pull/163", Note: "gitleaks <b>ok</b>"},
		{Kind: "file", Label: "Datei", Ref: "docs/a.md", Note: "the compare page"},
		{Kind: "decision", Label: "Entscheidung", Ref: "1"},
	}))
	for _, want := range []string{`<span class="pfx">`, `<span class="pnote">gitleaks &lt;b&gt;ok&lt;/b&gt;</span>`, `<span class="pnote">the compare page</span>`, `k-file`, `data-ref="DEC-1"`} {
		if !strings.Contains(got, want) {
			t.Errorf("proofs lack %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<b>ok</b>") {
		t.Errorf("a note became markup: %s", got)
	}
}

// A proof that is words, not a short identifier, is no pill: it would run out of
// the box as one line of code. It stands as the icon of its kind and grey text
// that wraps; short identifiers stay pills.
func TestProofChipsLongTextIsNoChipAndShortIdentifiersStayPills(t *testing.T) {
	long := "Robin im Chat 2026-11-08 zu Mock git-native-v2: bisher mag ich ihn sehr; Entscheidungen #4310, #4315 <i>x</i>"
	for _, e := range []evidenceLine{
		{Kind: "decision", Label: "Entscheidung", Ref: long},
		{Kind: "test", Label: "Test", Ref: "Prüf-Agent, Squash 237129c, gezielt grün am 2026-11-09"},
		{Kind: "test", Label: "Test", Ref: strings.Repeat("TestVeryLong", 10)},
		{Kind: "file", Label: "Datei", Ref: "internal/" + strings.Repeat("a/", 50) + "x.html"},
	} {
		got := string(proofChips(langDE, []evidenceLine{e}))
		if strings.Contains(got, `class="chip`) || strings.Contains(got, "k-file") {
			t.Errorf("%s %.30q became a pill:\n%s", e.Kind, e.Ref, got)
		}
		for _, want := range []string{`pfree`, `<span class="pnote">`} {
			if !strings.Contains(got, want) {
				t.Errorf("%s %.30q lacks %q:\n%s", e.Kind, e.Ref, want, got)
			}
		}
		if strings.Contains(got, "<i>x</i>") {
			t.Errorf("a proof became markup: %s", got)
		}
	}
	for _, tc := range []struct {
		e    evidenceLine
		want string
	}{
		{evidenceLine{Kind: "test", Label: "Test", Ref: "TestAnchorBranch"}, `class="chip test"`},
		{evidenceLine{Kind: "decision", Label: "Entscheidung", Ref: "kept-offline"}, `class="chip ar"`},
		{evidenceLine{Kind: "commit", Label: "Commit", Ref: "237129c6db4e0e5f452c32c4bf83bf65dc6ca525"}, `k-commit`},
		{evidenceLine{Kind: "file", Label: "Datei", Ref: "docs/a.md"}, `k-file`},
	} {
		got := string(proofChips(langDE, []evidenceLine{tc.e}))
		if !strings.Contains(got, tc.want) || strings.Contains(got, "pfree") {
			t.Errorf("%s %q is not a plain pill (%s):\n%s", tc.e.Kind, tc.e.Ref, tc.want, got)
		}
	}
	// A commit written with words is the hash as pill and the words as the note.
	got := string(proofChips(langDE, []evidenceLine{{Kind: "commit", Label: "Commit", Ref: "237129c gh pr view 186: MERGED nach dev"}}))
	for _, want := range []string{`k-commit`, `>237129c<`, `<span class="pnote">gh pr view 186: MERGED nach dev</span>`} {
		if !strings.Contains(got, want) {
			t.Errorf("commit with words lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "pfree") {
		t.Errorf("commit with words became a sentence: %s", got)
	}
}

// The first object of an issue is one bubble: the head with its maker, the
// text, the requirements and the explanation in it; the line starts there.
func TestIssueFirstObjectIsOneBubbleWithTheRequirementsInIt(t *testing.T) {
	e := ovEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "One bubble", Desc: "The text.", Criteria: []string{"First requirement"}})
	if err := e.St.SetCriterionState(d.Criteria[0].ID, "met", requestdomain.Evidence{Kind: "url", Ref: "https://example.test/pr/1 (the proof note)", Person: "alice"}); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/issues/"+itoa(d.Request.ID))
	open := strings.Index(page, `<div class="cmt first">`)
	close := strings.Index(page, `data-act="criterion.met"`)
	if open < 0 || close < open {
		t.Fatalf("no bubble before the entries: %d %d", open, close)
	}
	bubble := page[open:close]
	for _, want := range []string{"The text.", `class="reqs"`, "First requirement", `<span class="pnote">the proof note</span>`} {
		if !strings.Contains(bubble, want) {
			t.Errorf("the bubble lacks %q", want)
		}
	}
	if strings.Count(page, `class="cmt first"`) != 1 {
		t.Error("more than one first object")
	}
	// The note stands as text, never as one link that holds address and note.
	if strings.Contains(page, `href="https://example.test/pr/1 (`) || strings.Contains(page, `href="https://example.test/pr/1%20`) {
		t.Error("the address carries the note")
	}
}

// A note written with the status stands once: the comment the same form wrote
// is the only place, a "why" before it stays.
func TestPainStateNoteIsNotRepeatedByTheComment(t *testing.T) {
	for _, n := range []struct {
		note, comment, want string
		same                bool
	}{
		{"Same words.", "Same words.", "", true},
		{"Why we live with it.\n\nSame words.", "Same words.", "Why we live with it.", true},
		{"Different.", "Same words.", "Different.", false},
		{"Anything", "", "Anything", false},
	} {
		got, same := trimmedPainNote(n.note, n.comment)
		if got != n.want || same != n.same {
			t.Errorf("note %q comment %q = (%q, %v), want (%q, %v)", n.note, n.comment, got, same, n.want, n.same)
		}
	}
}
