package web

// Texte der Anmelde-, Einladungs- und Kopplungsseiten, die in der UI-Durchsicht
// (Paket P10) neu dazukamen. Die Schlüssel sind in beiden Katalogen dieselben;
// geänderte Texte stehen an ihrer alten Stelle in messages.go und messages_de.go.

var messagesAuth = map[string]string{
	"login.tab":              "Sign in",
	"login.title_setup":      "Set up ghosttree",
	"login.bootstrap_hint":   "It is in the server log from the first start.",
	"login.error_empty":      "Enter a code.",
	"login.error_link":       "The link has expired or was already used. Ask for a new one.",
	"login.error_bootstrap":  "The code is wrong. It is in the server log.",
	"login.new_to":           "New to ghosttree?",
	"join.error_name":        "That name is taken or not allowed. Pick another one.",
	"join.expires_soon":      "Expires in under an hour",
	"join.expires_hour":      "Expires in 1 hour",
	"join.expires_hours":     "Expires in %d hours",
	"join.expires_day":       "Expires in 1 day",
	"join.expires_days":      "Expires in %d days",
	"join.confirm_pre":       "This is me,",
	"join.with_join":         "Join with %s",
	"join.invite_tab":        "Invitation",
	"join.member_tab":        "Already a member",
	"pair.same_net":          "on your network",
	"pair.code_placeholder":  "····",
	"adm.device.warn_pre":    "Only approve if you just ran",
	"adm.device.warn_post":   "yourself.",
	"adm.device.cmd":         "ctx login",
	"adm.login.readonly_sso": "Your token can only read. Sign in to change this.",
	"setup.login_to_approve": "Approving a device needs a login.",
}

var messagesAuthDE = map[string]string{
	"login.tab":              "Anmelden",
	"login.title_setup":      "ghosttree einrichten",
	"login.bootstrap_hint":   "Steht im Log des Servers beim ersten Start.",
	"login.error_empty":      "Gib einen Code ein.",
	"login.error_link":       "Der Link ist abgelaufen oder wurde schon benutzt. Lass dir einen neuen geben.",
	"login.error_bootstrap":  "Der Code stimmt nicht. Er steht im Log des Servers.",
	"login.new_to":           "Neu bei ghosttree?",
	"join.error_name":        "Der Name ist schon vergeben oder nicht erlaubt. Nimm einen anderen.",
	"join.expires_soon":      "Läuft in unter einer Stunde ab",
	"join.expires_hour":      "Läuft ab in 1 Stunde",
	"join.expires_hours":     "Läuft ab in %d Stunden",
	"join.expires_day":       "Läuft ab in 1 Tag",
	"join.expires_days":      "Läuft ab in %d Tagen",
	"join.confirm_pre":       "Das bin ich,",
	"join.with_join":         "Mit %s beitreten",
	"join.invite_tab":        "Einladung",
	"join.member_tab":        "Schon Mitglied",
	"pair.same_net":          "im selben Netz",
	"pair.code_placeholder":  "····",
	"adm.device.warn_pre":    "Gib nur frei, wenn du gerade",
	"adm.device.warn_post":   "ausgeführt hast.",
	"adm.device.cmd":         "ctx login",
	"adm.login.readonly_sso": "Dein Token darf nur lesen. Melde dich an, um das zu ändern.",
	"setup.login_to_approve": "Zum Freigeben von Geräten brauchst du einen Login.",
}

func init() {
	for k, v := range messagesAuth {
		messages[k] = v
	}
	for k, v := range messagesAuthDE {
		messagesDE[k] = v
	}
}
