package web

// Texte der Oberflächen-Kriterien REQ-599 (Paketfilter der Listen,
// Qualifier-Vorschläge im Suchfeld) und REQ-628 (Strg+K). Sie kommen beim Start
// in beide Kataloge; die Schlüssel sind dieselben.

var messagesOpenAC = map[string]string{
	"issues.filter.package":     "Package",
	"issues.filter.any_package": "Any package",

	"search.suggest_for":  "Search for “%[1]s”",
	"search.key.is":       "State",
	"search.key.kind":     "Kind",
	"search.key.type":     "Type",
	"search.key.repo":     "Repository",
	"search.key.branch":   "Branch",
	"search.key.package":  "Package",
	"search.key.author":   "Author",
	"search.key.for":      "Assigned to",
	"search.key.label":    "Label",
	"search.key.area":     "Folder",
	"search.key.created":  "Created",
	"search.key.updated":  "Updated",
	"search.key.pr":       "Pull request",
	"search.key.priority": "Priority",
}

var messagesOpenACDE = map[string]string{
	"issues.filter.package":     "Paket",
	"issues.filter.any_package": "Jedes Paket",

	"search.suggest_for":  "Suchen nach „%[1]s“",
	"search.key.is":       "Zustand",
	"search.key.kind":     "Art",
	"search.key.type":     "Typ",
	"search.key.repo":     "Repository",
	"search.key.branch":   "Branch",
	"search.key.package":  "Paket",
	"search.key.author":   "Autor",
	"search.key.for":      "Zuständig",
	"search.key.label":    "Label",
	"search.key.area":     "Ordner",
	"search.key.created":  "Erstellt",
	"search.key.updated":  "Geändert",
	"search.key.pr":       "Pull Request",
	"search.key.priority": "Priorität",
}

func init() {
	for k, v := range messagesOpenAC {
		messages[k] = v
	}
	for k, v := range messagesOpenACDE {
		messagesDE[k] = v
	}
}
