package web

// Texte der Mängelrunde zur Sichtprüfung (REQ-599): Anlegen-Menü im Kopf,
// Agentenliste, Filter. Schlüssel sind in beiden Katalogen gleich.

var messagesMaengel = map[string]string{
	"shell.new":              "Create new",
	"ideas.make_issue":       "Make an issue of it",
	"org.n_repos_one":        "1 repository",
	"org.n_people_one":       "1 person",
	"org.n_rooms_one":        "1 room",
	"org.filter_visibility":  "Visibility",
	"newissue.pick_labels":   "Choose labels",
	"err.back_repo":          "Back to the repository",
	"agents.filter.active":   "%d active",
	"agents.filter.owner":    "Owner",
	"agents.filter.machine":  "Machine",
	"agents.filter.repo":     "Repository",
	"agents.filter.all":      "All",
	"agents.search_org":      "Search agents, machines or issues",
	"agents.connect_machine": "Connect a machine",
}

var messagesMaengelDE = map[string]string{
	"shell.new":              "Neu anlegen",
	"ideas.make_issue":       "Daraus Issue machen",
	"org.n_repos_one":        "1 Repository",
	"org.n_people_one":       "1 Person",
	"org.n_rooms_one":        "1 Raum",
	"org.filter_visibility":  "Sichtbarkeit",
	"newissue.pick_labels":   "Labels wählen",
	"err.back_repo":          "Zurück zum Repo",
	"agents.filter.active":   "%d aktiv",
	"agents.filter.owner":    "Owner",
	"agents.filter.machine":  "Maschine",
	"agents.filter.repo":     "Repo",
	"agents.filter.all":      "Alle",
	"agents.search_org":      "Agenten, Maschinen oder Issues suchen",
	"agents.connect_machine": "Maschine verbinden",
}

func init() {
	for k, v := range messagesMaengel {
		messages[k] = v
	}
	for k, v := range messagesMaengelDE {
		messagesDE[k] = v
	}
}
