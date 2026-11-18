package web

// Texte zu REQ-680 (WP7, Dach REQ-674): Rückfragen vor folgenreichen
// Verwaltungsaktionen, Rollenwahl beim Einladen, Meldungen mit Person und Objekt,
// die Einladungsliste. Eigene Datei, kommt beim Start in beide Kataloge.

var messagesAdmin = map[string]string{
	// Confirmation before an action with consequences
	"adm.ask.make_owner":       "Make %[1]s an owner of %[2]s? Owners manage the members, the invitations and every setting of the organization.",
	"adm.ask.make_member":      "Make %[1]s a member of %[2]s? They can then no longer manage members, invitations or settings.",
	"adm.ask.make_member_self": "Make yourself a member of %s? You lose the owner rights at once: you can no longer manage members, invitations or settings, and only another owner can give them back.",
	"adm.ask.remove":           "Remove %[1]s from %[2]s? They lose access to the projects of the organization and their roles in them, and can only come back through a new invitation.",
	"adm.ask.leave":            "Leave %s? You lose access to its projects and your roles in them, and can only come back through a new invitation.",
	"adm.ask.revoke":           "Revoke the invitation for %s? The link stops working at once; nobody can join with it any more.",
	// What an action did, with person and object
	"adm.done.owner":              "%[1]s is now an owner of %[2]s.",
	"adm.done.member":             "%[1]s is now a member of %[2]s.",
	"adm.done.member_self":        "You are now a member of %s and no longer an owner.",
	"adm.done.removed":            "%[1]s was removed from %[2]s.",
	"adm.done.left":               "You left %s.",
	"adm.done.revoked":            "The invitation for %s was revoked.",
	"adm.done.prole":              "%[1]s is now %[2]s in %[3]s.",
	"adm.done.prole_review":       "%[1]s is now %[2]s in %[3]s and can review knowledge.",
	"adm.done.prole_none":         "%[1]s has no role in %[2]s any more.",
	"adm.done.shared":             "%[1]s is now shared with everyone in %[2]s.",
	"adm.done.private":            "%s is now private: only people with a role in it see it.",
	"adm.done.moved":              "%[1]s moved to %[2]s.",
	"settings.done.created_named": "Label “%s” created.",
	"settings.done.updated_named": "Label “%s” saved.",
	"settings.done.deleted_named": "Label “%s” deleted.",
	// Invitation: the role to choose and what it allows
	"adm.invite.role":          "Role",
	"adm.invite.role_member":   "Member",
	"adm.invite.role_member_d": "Works in the shared projects of the organization. Only owners manage members and settings.",
	"adm.invite.role_guest":    "Guest",
	"adm.invite.role_guest_d":  "Sees one project only, nothing else of the organization.",
	"adm.invite.guest_for":     "Project",
	// Invitation list
	"adm.invite.for":        "For %s",
	"adm.invite.once":       "Works once",
	"adm.invite.list":       "Invitations",
	"adm.invite.state_open": "Open",
	"adm.invite.ended":      "Expired %s",
	// Roles of a project
	"adm.reviewer_can": "Can review knowledge",
}

var messagesAdminDE = map[string]string{
	"adm.ask.make_owner":          "%[1]s zum Owner von %[2]s machen? Owner verwalten die Mitglieder, die Einladungen und jede Einstellung der Organisation.",
	"adm.ask.make_member":         "%[1]s zum Mitglied von %[2]s machen? Danach kann die Person keine Mitglieder, Einladungen oder Einstellungen mehr verwalten.",
	"adm.ask.make_member_self":    "Dich selbst zum Mitglied von %s machen? Du verlierst die Owner-Rechte sofort: Du kannst keine Mitglieder, Einladungen oder Einstellungen mehr verwalten, und nur ein anderer Owner kann sie dir zurückgeben.",
	"adm.ask.remove":              "%[1]s aus %[2]s entfernen? Die Person verliert den Zugriff auf die Projekte der Organisation samt ihren Rollen darin und kommt nur über eine neue Einladung zurück.",
	"adm.ask.leave":               "%s verlassen? Du verlierst den Zugriff auf die Projekte und deine Rollen darin und kommst nur über eine neue Einladung zurück.",
	"adm.ask.revoke":              "Einladung für %s widerrufen? Der Link funktioniert sofort nicht mehr; niemand kann damit noch beitreten.",
	"adm.done.owner":              "%[1]s ist jetzt Owner von %[2]s.",
	"adm.done.member":             "%[1]s ist jetzt Mitglied von %[2]s.",
	"adm.done.member_self":        "Du bist jetzt Mitglied von %s und nicht mehr Owner.",
	"adm.done.removed":            "%[1]s wurde aus %[2]s entfernt.",
	"adm.done.left":               "Du hast %s verlassen.",
	"adm.done.revoked":            "Die Einladung für %s wurde widerrufen.",
	"adm.done.prole":              "%[1]s ist jetzt %[2]s in %[3]s.",
	"adm.done.prole_review":       "%[1]s ist jetzt %[2]s in %[3]s und darf Wissen prüfen.",
	"adm.done.prole_none":         "%[1]s hat keine Rolle mehr in %[2]s.",
	"adm.done.shared":             "%[1]s ist jetzt mit allen in %[2]s geteilt.",
	"adm.done.private":            "%s ist jetzt privat: Nur Leute mit einer Rolle darin sehen es.",
	"adm.done.moved":              "%[1]s ist nach %[2]s umgezogen.",
	"settings.done.created_named": "Label „%s“ angelegt.",
	"settings.done.updated_named": "Label „%s“ gespeichert.",
	"settings.done.deleted_named": "Label „%s“ gelöscht.",
	"adm.invite.role":             "Rolle",
	"adm.invite.role_member":      "Mitglied",
	"adm.invite.role_member_d":    "Arbeitet in den geteilten Projekten der Organisation mit. Mitglieder und Einstellungen verwalten nur Owner.",
	"adm.invite.role_guest":       "Gast",
	"adm.invite.role_guest_d":     "Sieht nur ein einzelnes Projekt, sonst nichts von der Organisation.",
	"adm.invite.guest_for":        "Projekt",
	"adm.invite.for":              "Für %s",
	"adm.invite.once":             "Gilt einmal",
	"adm.invite.list":             "Einladungen",
	"adm.invite.state_open":       "Offen",
	"adm.invite.ended":            "Abgelaufen %s",
	"adm.reviewer_can":            "Darf Wissen prüfen",
}

func init() {
	for k, v := range messagesAdmin {
		messages[k] = v
	}
	for k, v := range messagesAdminDE {
		messagesDE[k] = v
	}
}
