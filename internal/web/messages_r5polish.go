package web

// Texte des Feinschliffs aus Lauf 5 (REQ-711): das „Mehr“ der Reiterleiste und der Hinweis auf einer Dateiseite,
// die nur die Beschreibung zeigt. Sie kommen beim Start in beide Kataloge.

var messagesR5Polish = map[string]string{
	"ui.band.more_menu": "More",
	"code.nofile":       "The file itself is not stored here.",
	// The names of the two price fields where the column heads are gone (the phone).
	"ai.model.in_short":  "In",
	"ai.model.out_short": "Out",
}

var messagesR5PolishDE = map[string]string{
	"ui.band.more_menu":  "Mehr",
	"code.nofile":        "Die Datei selbst liegt nicht hier.",
	"ai.model.in_short":  "Ein",
	"ai.model.out_short": "Aus",
}

func init() {
	for k, v := range messagesR5Polish {
		messages[k] = v
	}
	for k, v := range messagesR5PolishDE {
		messagesDE[k] = v
	}
}
