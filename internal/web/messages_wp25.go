package web

// Texte zum Stand oben in der Werkstatt einer Idee (REQ-700): wie viele Entscheidungen offen sind
// und was als Nächstes kommt. Sie kommen beim Start in beide Kataloge.

var messagesWP25 = map[string]string{
	"workshop.stand":      "Status",
	"workshop.stand.open": "%[1]d of %[2]d decisions open",
	"workshop.stand.done": "All %d decisions made",
	"workshop.next":       "Next",
}

var messagesWP25DE = map[string]string{
	"workshop.stand":      "Stand",
	"workshop.stand.open": "%[1]d von %[2]d Entscheidungen offen",
	"workshop.stand.done": "Alle %d Entscheidungen getroffen",
	"workshop.next":       "Als Nächstes",
}

func init() {
	for k, v := range messagesWP25 {
		messages[k] = v
	}
	for k, v := range messagesWP25DE {
		messagesDE[k] = v
	}
}
