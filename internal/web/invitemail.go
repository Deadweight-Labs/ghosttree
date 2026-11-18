package web

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/mail"
)

// sendInviteMail sends the mail of a new invitation and reports what became of it:
// the page of the owner says honestly whether the person was written to or the
// link has to be passed on (REQ-683). The send is made inside the request, so that
// the answer is not "probably". A mail without an address, on a server that cannot
// send, or that cannot build a link, is not sent at all.
//
// target is what the person is invited to (the organization, or the project of a
// guest link). The link is built the way every mail link is (mailBase): from the
// configured public address, never from the Host header.
func (a *app) sendInviteMail(r *http.Request, l lang, to, target, role, code string, project bool, expires time.Time) inviteMail {
	to = strings.TrimSpace(to)
	base := ""
	if a.mailer != nil {
		base = a.mailBase(r)
	}
	switch {
	case base == "":
		// This server cannot write a mail with a link, with or without an address.
		return inviteMail{State: inviteMailOff, To: to}
	case to == "":
		return inviteMail{State: inviteMailNone}
	}
	link := base + "/ui/login/code?code=" + url.QueryEscape(code)
	if project {
		link = base + "/join/" + code
	}
	inviter := browserPrincipal(r).Label
	msg := mail.Message{To: to, Subject: l.msg("mail.invite.subject", inviter),
		Text: l.msg("mail.invite.body", inviter, target, role, link, expires.Local().Format("2006-01-02"))}
	if err := a.mailer.Send(msg); err != nil {
		logger := a.logger
		if logger == nil {
			logger = slog.Default()
		}
		// SMTP answers like to name the address; the log never gets it.
		logger.Error("mail_send_failed", "purpose", "invite", "error", strings.ReplaceAll(err.Error(), to, "<address>"))
		return inviteMail{State: inviteMailFailed, To: to}
	}
	return inviteMail{State: inviteMailSent, To: to}
}

// inviteNotice is the line under the link of a fresh invitation: what became of
// the mail. Empty when it went out (the title says so then).
func inviteNotice(l lang, m inviteMail) string {
	switch m.State {
	case inviteMailNone:
		return l.msg("adm.invite.nomail_none")
	case inviteMailOff:
		if m.To == "" {
			return l.msg("adm.invite.nomail_off_any")
		}
		return l.msg("adm.invite.nomail_off", m.To)
	case inviteMailFailed:
		return l.msg("adm.invite.nomail_failed", m.To)
	}
	return ""
}
