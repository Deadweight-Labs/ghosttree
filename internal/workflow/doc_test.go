package workflow

import (
	"reflect"
	"strings"
	"testing"
)

// A file with everything the block view cannot draw: comments above keys, at
// the end of lines, between items, an unknown key, a line without a key, a
// block list and an instruction below.
const docRich = `---
# Release-Abgleich, gepflegt von Lena
description: Vor jedem Release abgleichen, was Menschen wollten  # kurz halten
on: pr.merged
branches:
  - release/*   # nur Releases
  - hotfix/*
inputs: [pr.text, pr.diff, pr.sessions.user_messages, requests.open]
uses: ghosttree/pr-wishes@v1
results:
  # erst Issues
  issue: 5
  knowledge: 3 # Wissen nur wenig
retry: 2
# bis Release 2.0 auf gruendlich lassen, danach mittel (Lena)
model: gruendlich
budget:
  run_usd: 0.50
  month_usd: 10
das hier ist kein Schlüssel
---
Achte besonders auf Wünsche zur Migration der Datenbank.

Zitiere immer wörtlich.
`

func docTexts(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{"rich": docRich, "starter-like": "---\ndescription:\non: pr.merged\ninputs: [pr.title]\nuses: ghosttree/echo\n---\n",
		"no-front": "nur Text ohne Einstellungen\n", "unclosed": "---\non: pr.merged\n", "empty": "", "crlf-free-tail": "---\non: schedule\nevery: 60\n---"}
	for _, tpl := range Templates() {
		out["tpl:"+tpl.Name] = tpl.Text
	}
	if len(out) < 10 {
		t.Fatalf("expected the four templates among the texts, got %d texts", len(out))
	}
	return out
}

// Text → blocks → text is the same text, byte for byte (AC-697.1).
func TestDocRoundTrip(t *testing.T) {
	for name, text := range docTexts(t) {
		if got := ParseDoc(text).Text(); got != text {
			t.Errorf("%s: round trip changed the text:\n%q\n%q", name, text, got)
		}
	}
}

// Setting every key to the value the view read from it changes nothing: what
// the block view shows and writes back is the file.
func TestDocSetSameValueKeepsText(t *testing.T) {
	for name, text := range docTexts(t) {
		d := ParseDoc(text)
		if !d.OK() {
			continue
		}
		for _, k := range Keys {
			kv, ok := d.Value(k)
			if !ok {
				continue
			}
			e := Edit{Op: EditSet, Key: k, Value: kv.Scalar, List: kv.List, Pairs: kv.Pairs}
			got, err := ApplyEdit(text, e)
			if err != nil {
				t.Fatalf("%s: set %s: %v", name, k, err)
			}
			if got != text {
				t.Errorf("%s: setting %s to its own value changed the file:\n%s\n---\n%s", name, k, text, got)
			}
		}
		if got, _ := ApplyEdit(text, Edit{Op: EditBody, Text: d.Body() + "\n"}); got != text {
			t.Errorf("%s: setting the instruction to itself changed the file", name)
		}
	}
}

// changed lists the lines of b that are not in a, in order.
func changed(a, b string) []string {
	have := map[string]int{}
	for _, l := range strings.Split(a, "\n") {
		have[l]++
	}
	var out []string
	for _, l := range strings.Split(b, "\n") {
		if have[l] > 0 {
			have[l]--
			continue
		}
		out = append(out, l)
	}
	return out
}

func mustApply(t *testing.T, text string, edits ...Edit) string {
	t.Helper()
	for _, e := range edits {
		var err error
		text, err = ApplyEdit(text, e)
		if err != nil {
			t.Fatalf("%+v: %v", e, err)
		}
	}
	return text
}

func TestDocEditTouchesOnlyItsKey(t *testing.T) {
	got := mustApply(t, docRich, Edit{Op: EditSet, Key: KeyModel, Value: "mittel"})
	if diff := changed(docRich, got); !reflect.DeepEqual(diff, []string{"model: mittel"}) {
		t.Fatalf("changing the model should change one line, got %q", diff)
	}
	if !strings.Contains(got, "# bis Release 2.0 auf gruendlich lassen, danach mittel (Lena)\nmodel: mittel") {
		t.Fatalf("the comment above the model has to stay above it:\n%s", got)
	}
	// Results: the comment above issue and the one after knowledge stay with their items.
	got = mustApply(t, docRich, Edit{Op: EditSet, Key: KeyResults, Pairs: []KV{{"issue", "4"}, {"knowledge", "3"}, {"criterion", "2"}}})
	if !strings.Contains(got, "results:\n  # erst Issues\n  issue: 4\n  knowledge: 3 # Wissen nur wenig\n  criterion: 2\nretry: 2") {
		t.Fatalf("results lost their comments or order:\n%s", got)
	}
	// Removing an item keeps the comment that was on it.
	got = mustApply(t, docRich, Edit{Op: EditSet, Key: KeyResults, Pairs: []KV{{"issue", "5"}}})
	if !strings.Contains(got, "# Wissen nur wenig") || strings.Contains(got, "knowledge: 3") {
		t.Fatalf("removing a kind has to drop its line but keep its comment:\n%s", got)
	}
	// A list written as "- item" lines stays one, with its comments.
	got = mustApply(t, docRich, Edit{Op: EditSet, Key: KeyBranches, List: []string{"release/*", "main"}})
	if !strings.Contains(got, "branches:\n  - release/*   # nur Releases\n  - main\n  # Wissen") && !strings.Contains(got, "branches:\n  - release/*   # nur Releases\n  - main\n") {
		t.Fatalf("block list not kept:\n%s", got)
	}
	// Unknown key, stray line and instruction are untouched by all of this.
	for _, keep := range []string{"retry: 2", "das hier ist kein Schlüssel", "Zitiere immer wörtlich."} {
		if !strings.Contains(got, keep) {
			t.Fatalf("%q got lost:\n%s", keep, got)
		}
	}
}

func TestDocRemoveKeepsComments(t *testing.T) {
	got := mustApply(t, docRich, Edit{Op: EditRemove, Key: KeyDescription})
	if strings.Contains(got, "description:") || !strings.Contains(got, "# kurz halten") {
		t.Fatalf("removing a key keeps the comment at the end of its line:\n%s", got)
	}
	got = mustApply(t, docRich, Edit{Op: EditRemove, Key: KeyResults})
	for _, c := range []string{"# erst Issues", "# Wissen nur wenig"} {
		if !strings.Contains(got, c) {
			t.Fatalf("removing results lost %q:\n%s", c, got)
		}
	}
	d, _ := Parse(got)
	if len(d.Results) != 0 {
		t.Fatalf("results still there: %+v", d.Results)
	}
}

func TestDocInsertInOrder(t *testing.T) {
	text := "---\non: pr.merged\nuses: ghosttree/pr-knowledge\n# Modell kommt vom Repo\nmodel: mittel\n---\n"
	got := mustApply(t, text,
		Edit{Op: EditSet, Key: KeyResults, Pairs: []KV{{"knowledge", "5"}}},
		Edit{Op: EditSet, Key: KeyBranches, List: []string{"main"}},
		Edit{Op: EditSet, Key: KeyDescription, Value: "Wissen: nach dem Merge"},
		Edit{Op: EditSet, Key: KeyBudget, Pairs: []KV{{BudgetRun, "0.20"}}})
	want := "---\ndescription: Wissen: nach dem Merge\non: pr.merged\nbranches: [main]\nuses: ghosttree/pr-knowledge\nresults:\n  knowledge: 5\n# Modell kommt vom Repo\nmodel: mittel\nbudget:\n  run_usd: 0.20\n---\n"
	if got != want {
		t.Fatalf("inserted keys in the wrong place:\n%s\nwant\n%s", got, want)
	}
}

// Values that need quotes come back from Parse as they were typed.
func TestDocScalarsSurviveParse(t *testing.T) {
	for _, v := range []string{"Wissen: prüfen", "#1 Wunsch", "a #b", "*stern", `sagt "hallo"`, "it's [x]", "@robin", "  rand  ", "ok"} {
		got := mustApply(t, "---\non: pr.merged\n---\n", Edit{Op: EditSet, Key: KeyDescription, Value: v})
		d, probs := Parse(got)
		if len(probs) != 0 || d.Description != strings.TrimSpace(v) {
			t.Errorf("%q: Parse read %q, problems %v, text\n%s", v, d.Description, probs, got)
		}
	}
	if _, err := FormatScalar(`a "b" 'c' #d`); err == nil {
		t.Fatal("a value with both quotes that needs quoting cannot be written; expected an error")
	}
	got := mustApply(t, "---\non: pr.merged\n---\n", Edit{Op: EditSet, Key: KeyBranches, List: []string{"a,b", "release/*"}})
	if d, probs := Parse(got); len(probs) != 0 || !reflect.DeepEqual(d.Branches, []string{"a,b", "release/*"}) {
		t.Fatalf("a list item with a comma: %v %v\n%s", d.Branches, probs, got)
	}
}

func TestDocBodyAndBlocks(t *testing.T) {
	got := mustApply(t, "---\non: pr.merged\nuses: ghosttree/ask\n---\n", Edit{Op: EditBody, Text: "Kurz antworten.\n"})
	if got != "---\non: pr.merged\nuses: ghosttree/ask\n---\nKurz antworten.\n" {
		t.Fatalf("body: %q", got)
	}
	if got := mustApply(t, got, Edit{Op: EditBody, Text: ""}); got != "---\non: pr.merged\nuses: ghosttree/ask\n---\n" {
		t.Fatalf("empty body: %q", got)
	}
	d := ParseDoc(docRich)
	var comment, stray Segment
	idc, ids := -1, -1
	for i, s := range d.Segments() {
		if s.Kind == SegComment && strings.Contains(d.SegmentText(s), "Release 2.0") {
			comment, idc = s, i
		}
		if s.Kind == SegRaw {
			stray, ids = s, i
		}
	}
	if idc < 0 || ids < 0 {
		t.Fatal("expected a comment block and a raw block in the rich file")
	}
	got = mustApply(t, docRich, Edit{Op: EditBlock, Block: idc, Old: d.SegmentText(comment), Text: "# ab jetzt mittel"})
	if diff := changed(docRich, got); !reflect.DeepEqual(diff, []string{"# ab jetzt mittel"}) {
		t.Fatalf("a block edit changes only that block: %q", diff)
	}
	if _, err := ApplyEdit(docRich, Edit{Op: EditBlock, Block: ids, Old: "something else", Text: "x"}); err == nil {
		t.Fatal("a block that changed since it was shown must not be overwritten")
	}
	got = mustApply(t, docRich, Edit{Op: EditBlock, Block: ids, Old: d.SegmentText(stray), Text: ""})
	if strings.Contains(got, "kein Schlüssel") {
		t.Fatal("an emptied block goes away")
	}
	if _, err := ApplyEdit(docRich, Edit{Op: EditBlock, Block: idc, Old: d.SegmentText(comment), Text: "---"}); err == nil {
		t.Fatal("a block must not close the settings")
	}
}

func TestDocWholeAndEmpty(t *testing.T) {
	d := ParseDoc("nur Text\n")
	if d.OK() || len(d.Segments()) != 1 || d.Segments()[0].Kind != SegWhole {
		t.Fatalf("text without front matter is one block: %+v", d.Segments())
	}
	if _, err := ApplyEdit("nur Text\n", Edit{Op: EditSet, Key: KeyOn, Value: "pr.merged"}); err == nil {
		t.Fatal("a key cannot go into a text without settings")
	}
	got := mustApply(t, "nur Text\n", Edit{Op: EditBlock, Block: 0, Old: "nur Text\n", Text: "---\non: pr.merged\n---\n"})
	if !ParseDoc(got).OK() {
		t.Fatalf("the whole text block replaces the text: %q", got)
	}
	// A new workflow starts empty and gets its first block.
	got = mustApply(t, "", Edit{Op: EditSet, Key: KeyOn, Value: "idea.created"}, Edit{Op: EditSet, Key: KeyUses, Value: "ghosttree/idea-match"})
	if got != "---\non: idea.created\nuses: ghosttree/idea-match\n---\n" {
		t.Fatalf("empty text: %q", got)
	}
}

func TestViewOfOwnersAndItems(t *testing.T) {
	v := ViewOf(docRich, Env{Policy: Policy{DenyKinds: []string{ResultIssue}}})
	owners := map[string]string{}
	for _, b := range v.Blocks {
		owners[strings.SplitN(b.Text, "\n", 2)[0]] = b.Owner
	}
	if owners["# bis Release 2.0 auf gruendlich lassen, danach mittel (Lena)"] != KeyModel {
		t.Fatalf("the comment above model belongs to model: %v", owners)
	}
	if owners["retry: 2"] != KeyModel || owners["das hier ist kein Schlüssel"] != "" {
		t.Fatalf("owners: %v", owners)
	}
	var policy, unknown bool
	for _, p := range v.Errors {
		if p.Key == KeyResults && p.Item == ResultIssue && strings.Contains(p.Msg, "policy") {
			policy = true
		}
		if p.Key == "retry" {
			unknown = true
		}
	}
	if !policy || !unknown {
		t.Fatalf("expected the policy error at results/knowledge and the unknown key retry: %+v", v.Errors)
	}
	if v.Body == "" || v.Stage != StageInstruction {
		t.Fatalf("body %q stage %d", v.Body, v.Stage)
	}
}
