package web

// Texte des Blocks „Benachrichtigungen“ im Profil. Sie kommen beim Start in beide
// Kataloge; die Schlüssel sind dieselben.

var messagesNotify = map[string]string{
	"notify.title":    "Notifications",
	"notify.channel":  "Inbox",
	"notify.aria":     "%s in the inbox and at the bell",
	"notify.mention":  "Mentions",
	"notify.question": "Questions to me",
	"notify.decision": "Decisions",
	"notify.task":     "Tasks",
	"notify.gardener": "Gardener",
}

var messagesNotifyDE = map[string]string{
	"notify.title":    "Benachrichtigungen",
	"notify.channel":  "Posteingang",
	"notify.aria":     "%s im Posteingang und an der Glocke",
	"notify.mention":  "Erwähnungen",
	"notify.question": "Fragen an mich",
	"notify.decision": "Entscheidungen",
	"notify.task":     "Aufgaben",
	"notify.gardener": "Gärtner",
}

func init() {
	for k, v := range messagesNotify {
		messages[k] = v
	}
	for k, v := range messagesNotifyDE {
		messagesDE[k] = v
	}
}
