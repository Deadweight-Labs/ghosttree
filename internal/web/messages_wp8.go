package web

// Texte zu Reiterband, Kontomenü und Leerzuständen (REQ-681). Sie kommen beim
// Start in beide Kataloge.

var messagesWP8 = map[string]string{
	"ui.band.more_tabs":     "More tabs",
	"features.show_active":  "Show active features",
	"packages.show_open":    "Show open packages",
	"packages.all_branches": "All branches",
}

var messagesWP8DE = map[string]string{
	"ui.band.more_tabs":     "Weitere Reiter",
	"features.show_active":  "Aktive Features zeigen",
	"packages.show_open":    "Offene Pakete zeigen",
	"packages.all_branches": "Alle Branches",
}

func init() {
	for k, v := range messagesWP8 {
		messages[k] = v
	}
	for k, v := range messagesWP8DE {
		messagesDE[k] = v
	}
}
