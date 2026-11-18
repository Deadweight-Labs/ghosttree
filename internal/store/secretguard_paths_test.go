package store

import (
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/runbook"
)

// Ghost-Beschreibungen und Runbooks sind Freitext wie jeder andere: ein
// Geheimnis wird abgelehnt, nichts wird gespeichert.
func TestGhostAndRunbookRefuseSecrets(t *testing.T) {
	st := openTest(t)
	_, err := st.PutGhostFile(GhostFile{Project: "github.com/o/r", Path: "a.go", Description: withSecret("Der Dienst")})
	if ruleCode(err) != "secret_detected" {
		t.Fatalf("ghost description with a secret: %v", err)
	}
	if _, err := st.GhostFileByPath("github.com/o/r", "a.go"); err == nil {
		t.Fatal("the ghost was stored although it was refused")
	}
	spec := runbook.Spec{Purpose: "deploy", Steps: []runbook.Step{{Title: "call", Command: "curl -H 'Authorization: Bearer " + "sk-ant-" + "api03-abcdefghijklmnopqrstuvwx" + "' https://x"}}}
	_, err = st.WriteRunbook(RunbookWrite{Project: "github.com/o/r", Name: "deploy", Spec: spec, Person: "p", AuthorID: 1})
	if ruleCode(err) != "secret_detected" {
		t.Fatalf("runbook with a token in a command: %v", err)
	}
	spec.Steps[0].Command = "curl -H \"Authorization: Bearer $TOKEN\" https://x"
	if _, err := st.WriteRunbook(RunbookWrite{Project: "github.com/o/r", Name: "deploy", Spec: spec, Person: "p", AuthorID: 1}); err != nil {
		t.Fatalf("a runbook that names the variable: %v", err)
	}
	_, err = st.WriteRunbook(RunbookWrite{Project: "github.com/o/r", Name: "deploy2", Spec: runbook.Spec{Purpose: "x"}, Note: withSecret("Notiz"), Person: "p", AuthorID: 1})
	if ruleCode(err) != "secret_detected" {
		t.Fatalf("runbook note with a secret: %v", err)
	}
}

func TestLabelWorkflowAndDocumentTextRefuseSecrets(t *testing.T) {
	st := openTest(t)
	if _, err := st.CreateLabel(LabelInput{Project: "github.com/o/r", Name: "ok", Description: withSecret("Label")}); ruleCode(err) != "secret_detected" {
		t.Fatalf("label: %v", err)
	}
	if _, err := st.CreateDocument(Document{Project: "github.com/o/r", Slug: "a", Kind: "spec", Title: "A", Person: "p"}, "text", withSecret("Meldung")); ruleCode(err) != "secret_detected" {
		t.Fatalf("document message: %v", err)
	}
}
