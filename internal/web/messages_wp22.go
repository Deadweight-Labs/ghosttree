package web

// Texte zum Posteingang (REQ-689): der Haken trägt seinen Namen, ein gekürzter Fragetext lässt sich
// aufklappen. Sie kommen beim Start in beide Kataloge.

var messagesWP22 = map[string]string{
	"inbox.full_text":  "Full text",
	"inbox.short.done": "Done",
	"inbox.short.read": "Read",
}

var messagesWP22DE = map[string]string{
	"inbox.full_text":  "Ganzer Text",
	"inbox.short.done": "Erledigt",
	"inbox.short.read": "Gelesen",
}

func init() {
	for k, v := range messagesWP22 {
		messages[k] = v
	}
	for k, v := range messagesWP22DE {
		messagesDE[k] = v
	}
}
