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
		text = l.msg("time.ago", spanText(l, d, false))
	case d <= -time.Minute:
		text = l.msg("time.in", spanText(l, -d, true))
	}
	full := t.UTC().Format("2006-01-02 15:04 UTC")
	return template.HTML(`<time datetime="` + html.EscapeString(t.UTC().Format(time.RFC3339)) + `" data-local="title" title="` + full + `">` + html.EscapeString(text) + `</time>`)
}

const (
	day   = 24 * time.Hour
	week  = 7 * day
	month = 30 * day
	year  = 365 * day
)

// spanText writes a length of time after "ago" or "in": minutes and hours short,
// from a day on written out ("6 days", "5 weeks"). Ahead of now a number of days
// is rounded to the nearest, so an invitation of three days that was just made
// does not read "2 days".
func spanText(l lang, d time.Duration, ahead bool) string {
	switch {
	case d < time.Hour:
		return l.msg("age.short.min", int(d/time.Minute))
	case d < 48*time.Hour:
		return l.msg("age.short.hours", int(d/time.Hour))
	case d < 2*week:
		n := int(d / day)
		if ahead {
			n = int((d + day/2) / day)
		}
		return l.msg("age.span.days", n)
	case d < 8*week:
		return l.msg("age.span.weeks", int(d/week))
	case d < year:
		return l.msg("age.span.months", int(d/month))
	}
	return l.msg("age.span.years", int(d/year))
}

// adminLabel übersetzt gespeicherte Werte (Einladungs-, Token- und Tokenart) in
// den angezeigten Text; ein unbekannter Wert bleibt, wie er ist.
func adminLabel(l lang, group, value string) string {
	if group == "prole" {
		switch value {
		case "owner", "lead", "member", "guest":
			return l.msg("role." + value)
		}
		return value
	}
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
