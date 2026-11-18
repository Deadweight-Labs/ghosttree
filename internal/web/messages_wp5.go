package web

// Texte zu Agentenstatus, Maschinen und Leerzuständen (REQ-678). Sie kommen beim
// Start in beide Kataloge.

var messagesWP5 = map[string]string{
	"ov.work.awaiting_reply":        "Waiting for reply",
	"ov.state.ended":                "Ended",
	"coord.presence.awaiting_reply": "waiting for reply",
	"sessions.empty_agent":          "An agent is running on %s, but no session has arrived yet. Sessions come in through ctx watch on that machine.",
	"sessions.empty_machine":        "%s is connected, but no session has arrived yet. Sessions come in through ctx watch on that machine.",
	"sessions.open_machine":         "Open %s",
	"agents.none_active":            "No agent is working right now.",
	"agents.start_one":              "Start an agent",
	"machine.offer_title":           "Your machines",
	"machine.offer_share":           "Share with the org",
}

var messagesWP5DE = map[string]string{
	"ov.work.awaiting_reply":        "Wartet auf Antwort",
	"ov.state.ended":                "Beendet",
	"coord.presence.awaiting_reply": "wartet auf Antwort",
	"sessions.empty_agent":          "Auf %s läuft ein Agent, aber noch ist keine Session angekommen. Sessions kommen über ctx watch auf dieser Maschine.",
	"sessions.empty_machine":        "%s ist verbunden, aber noch ist keine Session angekommen. Sessions kommen über ctx watch auf dieser Maschine.",
	"sessions.open_machine":         "%s öffnen",
	"agents.none_active":            "Gerade arbeitet kein Agent.",
	"agents.start_one":              "Agent starten",
	"machine.offer_title":           "Deine Maschinen",
	"machine.offer_share":           "Mit der Org teilen",
}

func init() {
	for k, v := range messagesWP5 {
		messages[k] = v
	}
	for k, v := range messagesWP5DE {
		messagesDE[k] = v
	}
}
