package web

import (
	"html"
	"html/template"
	"time"
)

// timeTag schreibt einen RFC3339-Zeitstempel als <time> mit relativer Angabe
// und dem vollen Datum im title; ein unlesbarer Wert bleibt leer.
func timeTag(l lang, stamp string) template.HTML {
	t := parseTime(stamp)
	if t.IsZero() {
		return ""
	}
	now := overviewNow().UTC()
	text := l.msg("age.now")
	switch d := now.Sub(t); {
	case d >= time.Minute:
		text = l.msg("time.ago", shortAge(l, now, t))
	case d <= -time.Minute:
		text = l.msg("time.in", shortAge(l, t, now))
	}
	full := t.UTC().Format("2006-01-02 15:04 UTC")
	return template.HTML(`<time datetime="` + html.EscapeString(t.UTC().Format(time.RFC3339)) + `" data-local="title" title="` + full + `">` + html.EscapeString(text) + `</time>`)
}

// adminLabel übersetzt gespeicherte Werte (Einladungs-, Token- und Tokenart) in
// den angezeigten Text; ein unbekannter Wert bleibt, wie er ist.
func adminLabel(l lang, group, value string) string {
	switch group + "." + value {
	case "invite.pending":
		return l.msg("adm.status.pending")
	case "invite.accepted":
		return l.msg("adm.status.accepted")
	case "invite.expired":
		return l.msg("adm.status.expired")
	case "invite.revoked":
		return l.msg("adm.status.revoked")
	case "token.active":
		return l.msg("adm.tokens.active")
	case "token.revoked":
		return l.msg("adm.tokens.revoked")
	case "token.expired":
		return l.msg("adm.tokens.expired")
	case "kind.manual":
		return l.msg("adm.kind.manual")
	case "kind.device":
		return l.msg("adm.kind.device")
	case "kind.legacy":
		return l.msg("adm.kind.legacy")
	case "invrole.owner":
		return l.msg("adm.role.owner")
	case "invrole.member":
		return l.msg("adm.role.member")
	case "invrole.guest":
		return l.msg("adm.role.guest")
	}
	return value
}
