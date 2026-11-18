package web

// Texte zu Konto, Profil, ctx web, Kontomenü und Meldungen (REQ-686). Sie kommen
// beim Start in beide Kataloge; die Schlüssel und Platzhalter sind dieselben.

var messagesWP20 = map[string]string{
	"mail.confirm.already_title":  "Address already confirmed",
	"mail.confirm.already_text":   "%s is already confirmed. You can sign in with it.",
	"mail.profile.status_ok":      "Confirmed",
	"mail.profile.password_reset": "Password changed. All other sessions were signed out.",

	"login.signed_out":  "You are signed out.",
	"login.continue_as": "Continue as %s",
	"login.device_link": "ctx web on %s opened this link. The session reads and does everyday work; roles, invitations and settings need your own sign-in.",

	"adm.this_device":    "this device",
	"adm.device.note":    "Signed in through %s. This session reads and does everyday work; roles, invitations and settings need your own sign-in.",
	"adm.device.signin":  "Sign in as yourself",
	"adm.device.locked":  "Needs your own sign-in",
	"nav.team_settings":  "Team settings",
	"shell.theme":        "Appearance",
	"shell.theme_dark":   "Dark",
	"shell.theme_light":  "Light",
	"shell.theme_system": "System",
	"shell.org_current":  "Current organization",
}

var messagesWP20DE = map[string]string{
	"mail.confirm.already_title":  "Adresse schon bestätigt",
	"mail.confirm.already_text":   "%s ist schon bestätigt. Du kannst dich damit anmelden.",
	"mail.profile.status_ok":      "Bestätigt",
	"mail.profile.password_reset": "Passwort geändert. Alle anderen Sitzungen sind abgemeldet.",

	"login.signed_out":  "Du bist abgemeldet.",
	"login.continue_as": "Weiter als %s",
	"login.device_link": "ctx web auf %s hat diesen Link geöffnet. Die Sitzung liest und erledigt die tägliche Arbeit; Rollen, Einladungen und Einstellungen brauchen deine eigene Anmeldung.",

	"adm.this_device":    "diesem Gerät",
	"adm.device.note":    "Angemeldet über %s. Diese Sitzung liest und erledigt die tägliche Arbeit; Rollen, Einladungen und Einstellungen brauchen deine eigene Anmeldung.",
	"adm.device.signin":  "Als du selbst anmelden",
	"adm.device.locked":  "Braucht deine eigene Anmeldung",
	"nav.team_settings":  "Team-Einstellungen",
	"shell.theme":        "Darstellung",
	"shell.theme_dark":   "Dunkel",
	"shell.theme_light":  "Hell",
	"shell.theme_system": "System",
	"shell.org_current":  "Aktuelle Organisation",
}

func init() {
	for k, v := range messagesWP20 {
		messages[k] = v
	}
	for k, v := range messagesWP20DE {
		messagesDE[k] = v
	}
}
