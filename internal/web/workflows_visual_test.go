package web

import (
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/Deadweight-Labs/ghosttree/internal/workflow"
)

// visualEdit posts one edit of the block view and decodes the answer.
func visualEdit(t *testing.T, e shellEnv, c *http.Client, text string, edit *workflow.Edit) (int, wfVisualModel, string) {
	t.Helper()
	form := url.Values{"project": {shellProject}, "text": {text}}
	if edit != nil {
		raw, _ := json.Marshal(edit)
		form.Set("edit", string(raw))
	}
	form.Set("csrf_token", renderedCSRFToken(t, c, e.Base+"/ui/overview"))
	resp := sameOriginPostForm(t, c, e.Base+"/ui/workflows/visual", form)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m wfVisualModel
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m, string(raw)
}

const visualText = `---
description: Release-Wünsche
on: pr.merged
branches: [release/*]
inputs: [pr.text, pr.diff]
uses: ghosttree/pr-wishes@v1
results:
  issue: 5
# bis Release 2.0 gründlich (Lena)
model: gruendlich
budget:
  run_usd: 0.50
---
Zitiere wörtlich.
`

// AC-697.1 over the wire: every template and a file with comments come back
// unchanged when the block view only reads them, an edit of one block changes
// only its line, and the comment stays a text block beside the model.
func TestVisualViewKeepsTheFileAndEditsOneBlock(t *testing.T) {
	e, _ := workflowEnv(t)
	texts := []string{visualText}
	for _, tpl := range workflow.Templates() {
		texts = append(texts, tpl.Text)
	}
	for _, text := range texts {
		status, m, raw := visualEdit(t, e, e.Lead, text, nil)
		if status != http.StatusOK || m.Text != text {
			t.Fatalf("reading changed the text: %d %s", status, raw)
		}
	}
	status, m, raw := visualEdit(t, e, e.Lead, visualText, &workflow.Edit{Op: workflow.EditSet, Key: workflow.KeyModel, Value: "mittel"})
	if status != http.StatusOK || m.Text != strings.Replace(visualText, "model: gruendlich", "model: mittel", 1) {
		t.Fatalf("set model: %d %s", status, raw)
	}
	var note *workflow.DocBlock
	for i, b := range m.View.Blocks {
		if strings.Contains(b.Text, "Lena") {
			note = &m.View.Blocks[i]
		}
	}
	if note == nil || note.Owner != workflow.KeyModel || note.Kind != workflow.SegComment {
		t.Fatalf("the comment is a text block beside the model: %+v", m.View.Blocks)
	}
	if m.View.Body != "Zitiere wörtlich." || m.View.Stage != workflow.StageInstruction {
		t.Fatalf("instruction and stage: %q %d", m.View.Body, m.View.Stage)
	}
	// The check of the project names the problem at its key: this project has no alias "mittel".
	var atModel bool
	for _, p := range m.View.Errors {
		atModel = atModel || p.Key == workflow.KeyModel
	}
	if !atModel {
		t.Fatalf("expected the unknown alias at the model block: %+v", m.View.Errors)
	}
	// Removing a result kind keeps the rest of the file as it was.
	status, m, raw = visualEdit(t, e, e.Lead, visualText, &workflow.Edit{Op: workflow.EditRemove, Key: workflow.KeyResults})
	if status != http.StatusOK || strings.Contains(m.Text, "issue: 5") || !strings.Contains(m.Text, "# bis Release 2.0 gründlich (Lena)\nmodel: gruendlich") {
		t.Fatalf("remove results: %d %s", status, raw)
	}
	// An edit the format cannot take says why and changes nothing.
	status, _, raw = visualEdit(t, e, e.Lead, visualText, &workflow.Edit{Op: workflow.EditSet, Key: "retries", Value: "2"})
	if status != http.StatusUnprocessableEntity || !strings.Contains(raw, "unknown key retries") {
		t.Fatalf("unknown key: %d %s", status, raw)
	}
	if status, _, _ := visualEdit(t, e, e.Lead, visualText, nil); status != http.StatusOK {
		t.Fatal("lead reads")
	}
	// Members read workflows but change none, also not through the block view.
	if status, _, _ := visualEdit(t, e, e.Member, visualText, &workflow.Edit{Op: workflow.EditRemove, Key: workflow.KeyModel}); status != http.StatusForbidden {
		t.Fatalf("member edits: %d", status)
	}
	// Without the token of the form nothing happens.
	resp := sameOriginPostForm(t, e.Lead, e.Base+"/ui/workflows/visual", url.Values{"project": {shellProject}, "text": {visualText}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("without csrf: %d", resp.StatusCode)
	}
}

// AC-697.2: a new workflow is put together from blocks only, in the order a
// person clicks them in the block view, and saved with the form; what is saved
// is the text the block view built.
func TestVisualViewBuildsANewWorkflowFromBlocks(t *testing.T) {
	e, _ := workflowEnv(t)
	text := workflowStarter
	for _, ed := range []workflow.Edit{
		{Op: workflow.EditSet, Key: workflow.KeyDescription, Value: "Merges: melden"},
		{Op: workflow.EditSet, Key: workflow.KeyBranches, List: []string{"main"}},
		{Op: workflow.EditSet, Key: workflow.KeyInputs, List: []string{"pr.title", "pr.number"}},
		{Op: workflow.EditSet, Key: workflow.KeyChain, Value: "true"},
		{Op: workflow.EditRemove, Key: workflow.KeyChain},
	} {
		status, m, raw := visualEdit(t, e, e.Lead, text, &ed)
		if status != http.StatusOK {
			t.Fatalf("%+v: %d %s", ed, status, raw)
		}
		text = m.Text
	}
	if text != "---\ndescription: Merges: melden\non: pr.merged\nbranches: [main]\ninputs: [pr.title, pr.number]\nuses: ghosttree/echo\n---\n" {
		t.Fatalf("built text:\n%s", text)
	}
	if r := postForm(t, e, e.Lead, "/ui/workflows/save", url.Values{"project": {shellProject}, "mode": {"new"}, "name": {"aus-bloecken"}, "text": {text}, "note": {"aus Blöcken"}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("save: %d", r.StatusCode)
	}
	d, err := e.St.WorkflowDefByName(shellProject, "aus-bloecken")
	if err != nil || d.Text != text || d.Event != store.EventPRMerged {
		t.Fatalf("saved: %+v %v", d, err)
	}
}

// The pages carry the block view, and without JavaScript the text stays: the
// file page keeps its <pre>, the form its textarea.
func TestVisualViewOnThePagesWithTheTextAsFallback(t *testing.T) {
	e, _ := workflowEnv(t)
	if r := postForm(t, e, e.Lead, "/ui/workflows/save", url.Values{"project": {shellProject}, "mode": {"new"}, "name": {"melden"}, "text": {"---\non: pr.merged\n# nur Titel\ninputs: [pr.title]\nuses: ghosttree/echo\n---\n"}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: %d", r.StatusCode)
	}
	_, page := fetchPage(t, e.Member, e.Base+"/ui/x/shell/workflows/melden")
	mustContain(t, "file page", page, `data-wfv`, `class="wftext wffile wfv-text"`, "ui-wfvisual.js", "ui-wfvisual.css")
	mustNotContain(t, "member file page", page, "data-edit")
	model := attr(page, "data-model")
	var m wfVisualModel
	if err := json.Unmarshal([]byte(model), &m); err != nil || !strings.Contains(m.Text, "# nur Titel") || len(m.View.Blocks) != 1 || m.View.Blocks[0].Owner != workflow.KeyInputs {
		t.Fatalf("model on the page: %v %+v", err, m)
	}
	var cat wfVisualCatalog
	if err := json.Unmarshal([]byte(attr(page, "data-catalog")), &cat); err != nil || len(cat.Blocks) == 0 || len(cat.Inputs) == 0 {
		t.Fatalf("catalogue: %v %+v", err, cat)
	}
	for _, b := range cat.Blocks {
		if b.Name == store.GardenBlock {
			t.Fatal("the gardener is started by its settings, not offered in the palette")
		}
	}
	_, form := fetchPage(t, e.Lead, e.Base+"/ui/x/shell/workflows/melden?edit=1")
	mustContain(t, "form", form, `data-wfv data-edit`, `name="text"`, `action="/ui/workflows/save"`)
}

// attr is the unescaped value of the first attribute name="…" on a page.
func attr(page, name string) string {
	i := strings.Index(page, name+`="`)
	if i < 0 {
		return ""
	}
	rest := page[i+len(name)+2:]
	return html.UnescapeString(rest[:strings.Index(rest, `"`)])
}
