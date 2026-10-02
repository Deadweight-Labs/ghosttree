package web

import "fmt"

// messages ist der Textkatalog der Oberfläche: Schlüssel -> englischer Text.
// Vorlagen holen Texte mit {{t "schlüssel"}}, Handler mit msg. Ein späteres
// Deutsch ersetzt diese Tabelle, nicht die Vorlagen. Platzhalter sind %s und
// stehen höchstens einmal je Text.
var messages = map[string]string{
	// Hülle: Navigation, Kopf, Kontomenü.
	"shell.title":              "ghosttree — %s",
	"shell.brand":              "ghosttree",
	"shell.nav":                "Main",
	"shell.project":            "Project",
	"shell.all_projects":       "All projects",
	"shell.switch_project":     "Switch project",
	"shell.search_label":       "Search knowledge",
	"shell.search_placeholder": "Search",
	"shell.search_hint":        "/",
	"shell.account_menu":       "Account menu",
	"shell.join":               "Join with a code",
	"shell.sign_out":           "Sign out",
	"nav.overview":             "Overview",
	"nav.sessions":             "Sessions",
	"shell.menu":               "Menu",
	"nav.agents":               "Agents",
	"nav.rooms":                "Rooms",
	"nav.knowledge":            "Knowledge",
	"nav.requests":             "Requests",
	"nav.search":               "Search",
	"nav.review":               "Review",
	"nav.context":              "Agent context",
	"nav.organization":         "Organization",
	"nav.devices":              "Devices & tokens",
	"nav.my_devices":           "My devices & tokens",
	"role.owner":               "Owner",
	"role.admin":               "Admin",
	"role.lead":                "Lead",
	"role.member":              "Member",
	"role.guest":               "Guest",
	"overview.title":           "Overview",

	// Startseite.
	"age.now":              "now",
	"ov.next":              "Next",
	"ov.agents":            "Agents",
	"ov.requests":          "Requests",
	"ov.knowledge":         "Knowledge",
	"ov.learned":           "Learned this week",
	"ov.connect":           "Connect an agent",
	"ov.connect_another":   "Connect another agent",
	"ov.no_agents":         "No agents yet.",
	"ov.no_requests":       "No open requests.",
	"ov.no_learned":        "Nothing learned this week.",
	"ov.nothing":           "Nothing here yet.",
	"ov.open_room":         "Open room",
	"ov.review":            "Review",
	"ov.approve":           "Approve",
	"ov.code":              "Code from your terminal",
	"ov.proposed_by":       "Proposed by %s",
	"ov.work.working":      "Working",
	"ov.work.waiting_user": "Waiting for you",
	"ov.work.waiting_peer": "Waiting for an agent",
	"ov.work.blocked":      "Blocked",
	"ov.work.paused":       "Paused",
	"ov.state.active":      "Active",
	"ov.state.idle":        "Idle",
	"ov.state.offline":     "Offline",
	"setup.title_first":    "Connect your first agent",
	"setup.title":          "Connect an agent",
	"setup.copy":           "Copy",
	"setup.copied":         "Copied",
	"setup.waiting":        "Waiting for your machine",
	"setup.connected":      "%s is connected",
	"setup.start":          "Start Claude Code in a repository.",

	// Anmeldung.
	"login.title":            "Sign in",
	"login.provider":         "Continue with %s",
	"login.provider_generic": "Continue with your identity provider",
	"login.or":               "or",
	"login.code_label":       "Code or login link",
	"login.code_placeholder": "e.g. 4f9k-2m7q-x8dp",
	"login.submit":           "Sign in",
	"login.token_toggle":     "Paste a person token",
	"login.token_label":      "Token",
	"login.bootstrap_label":  "Bootstrap code",
	"login.name_label":       "Your name",
	"login.name_placeholder": "e.g. robin",
	"login.error_code":       "That code isn't valid. Check it and try again, or ask for a new link.",
	"login.error_token":      "That token isn't valid. Check it and try again.",
	"login.invite_title":     "Join an organization",
	"login.invite_text":      "You were invited. Sign in with your identity provider to create your account and join.",
	"login.code_title":       "One-time code",
	"login.account_name":     "Account name",
	"login.continue":         "Continue",
	"login.back":             "Back",
	"login.back_to_sign_in":  "Back to sign-in",

	// Fehlerseiten der Anmeldung.
	"auth.idp_unreachable.title":       "Identity provider unreachable",
	"auth.idp_unreachable.text":        "The identity provider could not be reached. Try again in a moment, or ask the operator for a one-time login link.",
	"auth.not_verified.title":          "Sign-in could not be verified",
	"auth.not_verified.text":           "This sign-in was not started in this browser or has already been used. Start again from the sign-in page.",
	"auth.sign_in_expired.title":       "Sign-in expired",
	"auth.sign_in_expired.text":        "The sign-in took too long or was already used. Start again.",
	"auth.idp_unreachable_retry.title": "Identity provider unreachable",
	"auth.idp_unreachable_retry.text":  "Try again in a moment.",
	"auth.idp_rejected.title":          "Sign-in failed",
	"auth.idp_rejected.text":           "The identity provider rejected the sign-in.",
	"auth.no_id_token.title":           "Sign-in failed",
	"auth.no_id_token.text":            "The identity provider returned no ID token.",
	"auth.id_token_invalid.title":      "Sign-in failed",
	"auth.id_token_invalid.text":       "The ID token is invalid or expired.",
	"auth.id_token_nonce.title":        "Sign-in failed",
	"auth.id_token_nonce.text":         "The ID token does not belong to this sign-in.",
	"auth.id_token_subject.title":      "Sign-in failed",
	"auth.id_token_subject.text":       "The ID token carries no usable subject.",
	"auth.id_token_client.title":       "Sign-in failed",
	"auth.id_token_client.text":        "The ID token was issued for another client.",
	"auth.no_account.title":            "No ghosttree account for this identity",
	"auth.no_account.text":             "This identity is not linked to a ghosttree account, and accounts are created by invitation only. Ask an owner for an invitation, or for a claim code if your account already exists.",
	"auth.code_not_accepted.title":     "Code not accepted",
	"auth.code_not_accepted.text":      "The code is invalid, expired or was already used. Ask for a new one.",
	"auth.already_connected.title":     "Account already connected",
	"auth.already_connected.text":      "This account is already connected to an identity and cannot be claimed again.",
	"auth.invitation_email.title":      "Invitation is for another email address",
	"auth.invitation_email.text":       "This invitation is bound to an email address that your identity provider did not report as verified for this account. The invitation was not used; ask the inviter for one without an email address, or sign in with the invited address.",
	"auth.too_many_codes.title":        "Too many wrong codes",
	"auth.too_many_codes.text":         "Wait a few minutes before trying again.",
	"auth.account_disabled.title":      "Account disabled",
	"auth.account_disabled.text":       "This account is disabled.",
	"auth.token_disabled.title":        "Token login is disabled",
	"auth.token_disabled.text":         "Sign in with your identity provider, or ask the operator for a one-time login link.",
	"auth.sso_reported.text":           "The identity provider reported: %s.",
	"auth.sso_reported.title":          "Sign-in was not completed",
}

// msg liefert den Text zu einem Schlüssel. Ein unbekannter Schlüssel kommt als
// er selbst zurück, damit ein Tippfehler sichtbar wird statt leer zu bleiben.
func msg(key string, args ...any) string {
	text, ok := messages[key]
	if !ok {
		return key
	}
	if len(args) == 0 {
		return text
	}
	return fmt.Sprintf(text, args...)
}
