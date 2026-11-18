package web

// Texte zur Sitzung aus einem Einmal-Link und zur Sperre bei Aktionen, die einen
// echten Nachweis verlangen. Sie kommen beim Start in beide Kataloge.

var messagesSession = map[string]string{
	"adm.relogin.title":  "This needs a fresh sign-in",
	"adm.relogin.text":   "You came in with a one-time link. For roles, access and settings sign in with your password, an email code or single sign-on; you come back to this page.",
	"adm.relogin.action": "Sign in again",
	"adm.link.note":      "Signed in with a one-time link until %s. Everyday work is open; roles, access and settings need your own sign-in.",
}

var messagesSessionDE = map[string]string{
	"adm.relogin.title":  "Dafür musst du dich neu anmelden",
	"adm.relogin.text":   "Du bist mit einem Einmal-Link angemeldet. Für Rollen, Zugänge und Einstellungen melde dich mit Passwort, E-Mail-Code oder Single Sign-on an; danach kommst du auf diese Seite zurück.",
	"adm.relogin.action": "Neu anmelden",
	"adm.link.note":      "Mit einem Einmal-Link angemeldet, bis %s. Die tägliche Arbeit ist offen; Rollen, Zugänge und Einstellungen brauchen deine eigene Anmeldung.",
}

func init() {
	for k, v := range messagesSession {
		messages[k] = v
	}
	for k, v := range messagesSessionDE {
		messagesDE[k] = v
	}
}
