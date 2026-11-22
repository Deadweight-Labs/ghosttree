package web

// Texte zu REQ-683 (WP10 + WP11, Dach REQ-674): die Einladungsmail, die ehrliche
// Rückmeldung, ob eine Mail ging, die Gast-Mitgliedschaft auf der Org-Seite. Eigene
// Datei, kommt beim Start in beide Kataloge.

var messagesInvite = map[string]string{
	"mail.invite.subject": "%s invited you to ghosttree",
	"mail.invite.body":    "%[1]s invited you to %[2]s on ghosttree (role: %[3]s).\n\nOpen this link to accept the invitation:\n\n    %[4]s\n\nThe link works once and expires on %[5]s.\nIf you do not know %[1]s, ignore this mail.\n",
	// The banner after "Invite"
	"adm.invite.sent":             "Invitation sent to %s",
	"adm.invite.ready_nomail":     "Invitation ready, no mail sent",
	"adm.invite.nomail_none":      "No mail was sent because no address was entered. Pass the link on to the person you invite.",
	"adm.invite.nomail_off":       "No mail was sent: this server cannot send mail. Pass the link on to %s.",
	"adm.invite.nomail_off_any":   "No mail was sent: this server cannot send mail. Pass the link on to the person you invite.",
	"adm.invite.nomail_failed":    "The mail to %s could not be sent. Pass the link on yourself.",
	"adm.invite.nomail_limited":   "No mail was sent: too many invitations went out today. Pass the link on to %s.",
	"mail.invite.target_fallback": "an organization",
	// The rows of the invitation list
	"adm.invite.row_sent":   "Mail sent to %s",
	"adm.invite.row_nomail": "No mail sent",
	"adm.invite.no_link":    "The link is not shown any more. Revoke the invitation and make a new one.",
	// The guest's own view of the organization page
	"adm.guest_in":       "Guest in %s",
	"adm.guest_projects": "You see only the projects shared with you.",
}

var messagesInviteDE = map[string]string{
	"mail.invite.subject": "%s hat dich zu ghosttree eingeladen",
	"mail.invite.body":    "%[1]s hat dich zu %[2]s auf ghosttree eingeladen (Rolle: %[3]s).\n\nÖffne diesen Link, um die Einladung anzunehmen:\n\n    %[4]s\n\nDer Link gilt einmal und läuft am %[5]s ab.\nKennst du %[1]s nicht, ignoriere diese Mail.\n",

	"adm.invite.sent":             "Einladung an %s verschickt",
	"adm.invite.ready_nomail":     "Einladung bereit, keine Mail verschickt",
	"adm.invite.nomail_none":      "Es ging keine Mail raus, weil keine Adresse eingetragen ist. Gib den Link an die eingeladene Person weiter.",
	"adm.invite.nomail_off":       "Es ging keine Mail raus: dieser Server kann keine Mails senden. Gib den Link an %s weiter.",
	"adm.invite.nomail_off_any":   "Es ging keine Mail raus: dieser Server kann keine Mails senden. Gib den Link an die eingeladene Person weiter.",
	"adm.invite.nomail_failed":    "Die Mail an %s ließ sich nicht senden. Gib den Link selbst weiter.",
	"adm.invite.nomail_limited":   "Es ging keine Mail raus: heute sind schon zu viele Einladungen verschickt worden. Gib den Link an %s weiter.",
	"mail.invite.target_fallback": "eine Organisation",

	"adm.invite.row_sent":   "Mail an %s verschickt",
	"adm.invite.row_nomail": "Keine Mail verschickt",
	"adm.invite.no_link":    "Der Link wird nicht mehr angezeigt. Zieh die Einladung zurück und erstelle eine neue.",

	"adm.guest_in":       "Gast in %s",
	"adm.guest_projects": "Du siehst nur die Projekte, die mit dir geteilt sind.",
}

func init() {
	for k, v := range messagesInvite {
		messages[k] = v
	}
	for k, v := range messagesInviteDE {
		messagesDE[k] = v
	}
}
