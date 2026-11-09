package web

// Texte der Verweis-Pill (UI-Durchsicht P2): die Namen der Arten, die in der
// Hover-Karte über einer Pill stehen, und die Wörter der Karten von Wartenden.
// Sie kommen beim Start in beide Kataloge; die Schlüssel sind dieselben.

var messagesPill = map[string]string{
	"ref.kind.pull":      "Pull request",
	"ref.kind.commit":    "Commit",
	"ref.kind.branch":    "Branch",
	"ref.kind.knowledge": "Knowledge",
	"ref.kind.org":       "Organisation",
	"ref.kind.machine":   "Machine",
	"ref.kind.link":      "Link",
	"ref.kind.file":      "File",
	"ref.kind.folder":    "Folder",
	"ref.kind.hidden":    "Branch",
	"ref.kind.waiting":   "Waiting for",
}

var messagesPillDE = map[string]string{
	"ref.kind.pull":      "Pull Request",
	"ref.kind.commit":    "Commit",
	"ref.kind.branch":    "Branch",
	"ref.kind.knowledge": "Wissen",
	"ref.kind.org":       "Organisation",
	"ref.kind.machine":   "Maschine",
	"ref.kind.link":      "Link",
	"ref.kind.file":      "Datei",
	"ref.kind.folder":    "Ordner",
	"ref.kind.hidden":    "Branch",
	"ref.kind.waiting":   "Wartet auf",
}

func init() {
	for k, v := range messagesPill {
		messages[k] = v
	}
	for k, v := range messagesPillDE {
		messagesDE[k] = v
	}
}
