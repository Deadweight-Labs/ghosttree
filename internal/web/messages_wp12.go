package web

// Texte zu Entscheidungsseite, Posteingang und Erwähnungen (REQ-684). Sie kommen beim
// Start in beide Kataloge.

var messagesWP12 = map[string]string{
	"coord.mention.agent_of":    "Agent of %s",
	"coord.mention.agent":       "Agent",
	"inbox.preview.image":       "Image: %s",
	"inbox.preview.image_plain": "Image",
	"inbox.sent":                "Reply sent.",
	"inbox.sent_to":             "Reply sent to %s.",
	"decision.asked":            "asked this %s",
	"search.group.decisions":    "Decisions",
	"search.decision.waits":     "waits for you",
	"decision.subject":          "Asked on",
	"decision.side.facts":       "Facts about the decision",
	"decision.side.title":       "Decisions",
}

var messagesWP12DE = map[string]string{
	"coord.mention.agent_of":    "Agent von %s",
	"coord.mention.agent":       "Agent",
	"inbox.preview.image":       "Bild: %s",
	"inbox.preview.image_plain": "Bild",
	"inbox.sent":                "Antwort gesendet.",
	"inbox.sent_to":             "Antwort an %s gesendet.",
	"decision.asked":            "hat das %s gefragt",
	"search.group.decisions":    "Entscheidungen",
	"search.decision.waits":     "wartet auf dich",
	"decision.subject":          "Gefragt zu",
	"decision.side.facts":       "Fakten zur Entscheidung",
	"decision.side.title":       "Entscheidungen",
}

func init() {
	for k, v := range messagesWP12 {
		messages[k] = v
	}
	for k, v := range messagesWP12DE {
		messagesDE[k] = v
	}
}
