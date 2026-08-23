package migrate

import (
	"context"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
	"github.com/Deadweight-Labs/ghosttree/internal/llm"
)

type fakeLLM struct{ reply, gotSystem, gotUser string }

type repairingLLM struct {
	replies []string
	calls   int
}

func (f *repairingLLM) Complete(_ context.Context, _ string, _ []llm.Message, _ int) (string, error) {
	out := f.replies[f.calls]
	f.calls++
	return out, nil
}

func (f *fakeLLM) Complete(_ context.Context, system string, msgs []llm.Message, _ int) (string, error) {
	f.gotSystem = system
	if len(msgs) > 0 {
		f.gotUser = msgs[0].Content
	}
	return f.reply, nil
}

func TestDistillPassesExistingTitlesAndParsesItems(t *testing.T) {
	f := &fakeLLM{reply: `{"items":[{"type":"instruction","title":"build","body":"make test","quote":"run make test"}],"dropped":["Verzeichnisstruktur: ableitbar"]}`}
	res, err := Distill(context.Background(), f, Artifact{Rel: "CLAUDE.md", Kind: "rules"}, "# Build\nrun make test\n", []string{"existing entry"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || res.Items[0].Type != "instruction" {
		t.Fatalf("items=%+v", res.Items)
	}
	if res.Items[0].Source != "CLAUDE.md" {
		t.Errorf("source=%q", res.Items[0].Source)
	}
	if !strings.Contains(f.gotUser, "existing entry") {
		t.Error("existing titles absent")
	}
	if !strings.Contains(strings.ToLower(f.gotSystem), "repo") {
		t.Error("derivability criterion absent")
	}
	if !strings.Contains(strings.ToLower(f.gotSystem), "contiguous") {
		t.Error("quote grounding rule absent")
	}
}

func TestDistillPassesActivationAsMetadata(t *testing.T) {
	f := &fakeLLM{reply: `{"items":[],"dropped":[]}`}
	_, err := Distill(context.Background(), f, Artifact{Rel: "core/AGENTS.md", Kind: "rules", Activation: activation.Rule{Paths: []string{"core/**"}}}, "# Rules", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Proposed activation paths: core/**", "metadata, not content", "must not invent task"} {
		if !strings.Contains(f.gotUser, want) {
			t.Errorf("prompt missing %q:\n%s", want, f.gotUser)
		}
	}
}

func TestDistillSurvivesBrokenJSON(t *testing.T) {
	if _, err := Distill(context.Background(), &fakeLLM{reply: "not json"}, Artifact{}, "x", nil); err == nil {
		t.Error("broken output accepted")
	}
}

func TestDistillAcceptsStructuredDroppedReasons(t *testing.T) {
	f := &fakeLLM{reply: `{"items":[],"dropped":[{"content":"directory layout","reason":"derivable from repo"}]}`}
	res, err := Distill(context.Background(), f, Artifact{Rel: "CLAUDE.md"}, "x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Dropped) != 1 || !strings.Contains(res.Dropped[0], "derivable from repo") {
		t.Fatalf("dropped=%v", res.Dropped)
	}
}

func TestDistillGroundsQuoteAcrossSourceLineWrap(t *testing.T) {
	f := &fakeLLM{reply: `{"items":[{"type":"instruction","title":"language","body":"keep German","quote":"Dokumentation und Code sind auf Deutsch. Diese Sprache beibehalten."}],"dropped":[]}`}
	content := "Dokumentation und Code sind auf Deutsch.\nDiese Sprache beibehalten.\n"
	res, err := Distill(context.Background(), f, Artifact{Rel: "CLAUDE.md"}, content, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Items[0].Quote != "Dokumentation und Code sind auf Deutsch.\nDiese Sprache beibehalten." {
		t.Fatalf("quote=%q", res.Items[0].Quote)
	}
}

func TestDistillRetriesUngroundedModelOutput(t *testing.T) {
	f := &repairingLLM{replies: []string{
		`{"items":[{"type":"instruction","title":"build","body":"run tests","quote":"paraphrased"}],"dropped":[]}`,
		`{"items":[{"type":"instruction","title":"build","body":"run tests","quote":"mix test"}],"dropped":[]}`,
	}}
	res, err := Distill(context.Background(), f, Artifact{Rel: "CLAUDE.md"}, "Run mix test before committing.", nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.calls != 2 || res.Items[0].Quote != "mix test" {
		t.Fatalf("calls=%d result=%+v", f.calls, res)
	}
}

func TestGroundQuoteRestoresSourceMarkdownMarkers(t *testing.T) {
	content := "Verifikation: **CHECKPOINT mit Alice** — Zielnummer bestätigen."
	got, ok := groundQuote(content, "CHECKPOINT mit Alice — Zielnummer bestätigen.")
	if !ok || got != "**CHECKPOINT mit Alice** — Zielnummer bestätigen." {
		t.Fatalf("got=%q ok=%v", got, ok)
	}
}
