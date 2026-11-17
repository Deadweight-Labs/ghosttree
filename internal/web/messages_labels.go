package web

// Antworten des Label-Formulars an Issue, Pain und Idee (REQ-690, R04).
func init() {
	for key, text := range map[string][2]string{
		"labels.done": {"Labels saved.", "Labels gespeichert."},
		"labels.fail": {"The label could not be set. It may have been renamed or removed; reload the page and pick it again.", "Das Label konnte nicht gesetzt werden. Vielleicht wurde es umbenannt oder entfernt; lade die Seite neu und wähle es noch einmal."},
	} {
		messages[key] = text[0]
		messagesDE[key] = text[1]
	}
}
