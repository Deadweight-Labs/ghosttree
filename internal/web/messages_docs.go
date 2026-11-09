package web

// Texte des Reiters Docs (REQ-626): Liste der Dokumente eines Repositorys und
// die Seite eines Dokuments mit Revisionen und Vergleich. Sie kommen beim Start
// in beide Kataloge; die Schlüssel sind dieselben.

var messagesDocs = map[string]string{
	"repo.tab.docs":           "Docs",
	"docs.search":             "Search documents",
	"docs.n":                  "%d documents",
	"docs.last_rev":           "Latest revision",
	"docs.empty":              "No documents yet.",
	"docs.rev":                "r%d",
	"docs.rev_of":             "Revision %[1]d of %[2]d",
	"docs.to_head":            "Newest revision",
	"docs.raw":                "Raw",
	"docs.copy_link":          "Copy link",
	"docs.archived":           "Archived",
	"docs.revisions":          "Revisions",
	"docs.compare":            "Compare",
	"docs.compare_from":       "Older revision",
	"docs.compare_to":         "Newer revision",
	"docs.diff":               "Diff",
	"docs.diff_h":             "Revision %[1]d → %[2]d",
	"docs.diff_same":          "Both revisions have the same text.",
	"docs.plus":               "+%d",
	"docs.minus":              "−%d",
	"docs.show_text":          "Text",
	"docs.kind.spec":          "Spec",
	"docs.kind.plan":          "Plan",
	"docs.kind.investigation": "Investigation",
	"docs.kind.report":        "Report",
	"docs.kind.other":         "Other",
}

var messagesDocsDE = map[string]string{
	"repo.tab.docs":           "Dokumente",
	"docs.search":             "Dokumente suchen",
	"docs.n":                  "%d Dokumente",
	"docs.last_rev":           "Letzte Revision",
	"docs.empty":              "Noch keine Dokumente.",
	"docs.rev":                "r%d",
	"docs.rev_of":             "Revision %[1]d von %[2]d",
	"docs.to_head":            "Neueste Revision",
	"docs.raw":                "Roh",
	"docs.copy_link":          "Link kopieren",
	"docs.archived":           "Archiviert",
	"docs.revisions":          "Revisionen",
	"docs.compare":            "Vergleichen",
	"docs.compare_from":       "Ältere Revision",
	"docs.compare_to":         "Neuere Revision",
	"docs.diff":               "Diff",
	"docs.diff_h":             "Revision %[1]d → %[2]d",
	"docs.diff_same":          "Beide Revisionen haben denselben Text.",
	"docs.plus":               "+%d",
	"docs.minus":              "−%d",
	"docs.show_text":          "Text",
	"docs.kind.spec":          "Spezifikation",
	"docs.kind.plan":          "Plan",
	"docs.kind.investigation": "Untersuchung",
	"docs.kind.report":        "Bericht",
	"docs.kind.other":         "Sonstiges",
}

func init() {
	for k, v := range messagesDocs {
		messages[k] = v
	}
	for k, v := range messagesDocsDE {
		messagesDE[k] = v
	}
}
