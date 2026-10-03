package web

import (
	"html"
	"html/template"
	"time"
)

// timeTag schreibt einen RFC3339-Zeitstempel als <time> mit relativer Angabe
// und dem vollen Datum im title; ein unlesbarer Wert bleibt leer.
func timeTag(stamp string) template.HTML {
	t := parseTime(stamp)
	if t.IsZero() {
		return ""
	}
	now := overviewNow().UTC()
	text := msg("age.now")
	switch d := now.Sub(t); {
	case d >= time.Minute:
		text = msg("time.ago", shortAge(now, t))
	case d <= -time.Minute:
		text = msg("time.in", shortAge(t, now))
	}
	full := t.UTC().Format("2006-01-02 15:04 UTC")
	return template.HTML(`<time datetime="` + html.EscapeString(t.UTC().Format(time.RFC3339)) + `" title="` + full + `">` + html.EscapeString(text) + `</time>`)
}

// adminLabel übersetzt gespeicherte Werte (Einladungs-, Token- und Tokenart) in
// den angezeigten Text; ein unbekannter Wert bleibt, wie er ist.
func adminLabel(group, value string) string {
	switch group + "." + value {
	case "invite.pending":
		return msg("adm.status.pending")
	case "invite.accepted":
		return msg("adm.status.accepted")
	case "invite.expired":
		return msg("adm.status.expired")
	case "invite.revoked":
		return msg("adm.status.revoked")
	case "token.active":
		return msg("adm.tokens.active")
	case "token.revoked":
		return msg("adm.tokens.revoked")
	case "token.expired":
		return msg("adm.tokens.expired")
	case "kind.manual":
		return msg("adm.kind.manual")
	case "kind.device":
		return msg("adm.kind.device")
	case "kind.legacy":
		return msg("adm.kind.legacy")
	case "invrole.owner":
		return msg("adm.role.owner")
	case "invrole.member":
		return msg("adm.role.member")
	case "invrole.guest":
		return msg("adm.role.guest")
	}
	return value
}
