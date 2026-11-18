package web

// Texte des Raum-Threads aus Lauf 5 (REQ-706): was die Nachrichtenarten
// bedeuten, wer den Status ändern darf, und dass „N Antworten“ im Raum kein
// Thread ist. Sie kommen beim Start in beide Kataloge.

var messagesR5Thread = map[string]string{
	"coord.kind.hint.message":  "Plain text, nobody has to answer",
	"coord.kind.hint.question": "Someone has to answer",
	"coord.kind.hint.approval": "Asks for a yes or a no",
	"coord.kind.hint.blocker":  "Work waits until this is solved",
	"coord.kind.hint.handoff":  "Passes the work on to someone",
	"coord.thread_state_lock":  "Only people who wrote in this thread, its author, the room managers and leads change the status.",
	"coord.replies_in_room":    "Replies in the room. Start a thread to keep them together.",
}

var messagesR5ThreadDE = map[string]string{
	"coord.kind.hint.message":  "Einfacher Text, niemand muss antworten",
	"coord.kind.hint.question": "Jemand muss antworten",
	"coord.kind.hint.approval": "Fragt nach Ja oder Nein",
	"coord.kind.hint.blocker":  "Die Arbeit wartet, bis das gelöst ist",
	"coord.kind.hint.handoff":  "Gibt die Arbeit an jemanden weiter",
	"coord.thread_state_lock":  "Den Status ändern nur, wer in diesem Thread geschrieben hat, wer ihn begonnen hat, die Raumleitung und Leads.",
	"coord.replies_in_room":    "Antworten im Raum. Mit einem Thread bleiben sie zusammen.",
}

func init() {
	for k, v := range messagesR5Thread {
		messages[k] = v
	}
	for k, v := range messagesR5ThreadDE {
		messagesDE[k] = v
	}
}
