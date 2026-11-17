package web

// Texte des Feinschliffs B (REQ-687): Bildkacheln, Verschieben, Abschließen, Labels, Repo-Reiter,
// Projekt anlegen. Sie kommen beim Start in beide Kataloge.

var messagesPolishB = map[string]string{
	"workshop.insert":         "Insert into text",
	"labels.create":           "Create a label",
	"adm.project.placeholder": "github.com/owner/name",
	"adm.projects.empty_form": "Create one below, or connect an agent in a repo",
	"adm.share.private_why":   "Only people with a role here see it",
	"adm.share.shared_why":    "Everyone in the org sees it",
	"context.described":       "Descriptions of files and folders",
	"issue.cx.complete_open":  "Done is possible once all requirements are met (%[1]d of %[2]d still open)",
}

var messagesPolishBDE = map[string]string{
	"workshop.insert":         "In Text einfügen",
	"labels.create":           "Label anlegen",
	"adm.project.placeholder": "github.com/owner/name",
	"adm.projects.empty_form": "Lege unten eines an oder verbinde einen Agenten in einem Repo",
	"adm.share.private_why":   "Nur wer hier eine Rolle hat, sieht es",
	"adm.share.shared_why":    "Alle in der Org sehen es",
	"context.described":       "Beschreibungen von Dateien und Ordnern",
	"issue.cx.complete_open":  "Erledigt geht, sobald alle Anforderungen erfüllt sind (noch %[1]d von %[2]d offen)",
}

func init() {
	for k, v := range messagesPolishB {
		messages[k] = v
	}
	for k, v := range messagesPolishBDE {
		messagesDE[k] = v
	}
}
