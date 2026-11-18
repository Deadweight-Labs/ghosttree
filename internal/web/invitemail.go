package web

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/Deadweight-Labs/ghosttree/internal/mail"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"golang.org/x/text/unicode/norm"
)

// maxMailLine is how long a name from the sender may be inside a mail text.
const maxMailLine = 60

// dotInside matches a dot between two letters or digits: any word with one may be
// turned into a link by a mail program (evil.example, evil.example,).
var dotInside = regexp.MustCompile(`[\pL\d]\.[\pL\d]`)

// dotLookalikes are characters that read or link as a dot.
var dotLookalikes = strings.NewReplacer("\u3002", ".", "\uff0e", ".", "\uff61", ".", "\u2024", ".", "\ufe52", ".", "\u00b7", ".")

// mailLine makes a text of the sender fit into one line of a mail: no line breaks
// or other control and format characters, no word that is or looks like a link,
// at most max characters. What is left of a name that was only a link is empty.
func mailLine(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, s)
	var words []string
	for _, w := range strings.Fields(s) {
		lower := strings.ToLower(dotLookalikes.Replace(norm.NFKC.String(w)))
		if strings.Contains(lower, "://") || strings.HasPrefix(lower, "www.") || strings.Contains(w, "@") ||
			(dotInside.MatchString(lower) && !isVersionWord(w)) {
			continue
		}
		words = append(words, w)
	}
	out := strings.Join(words, " ")
	if r := []rune(out); len(r) > max {
		out = strings.TrimSpace(string(r[:max]))
	}
	return out
}

// isVersionWord: a word like v1.2 or 3.0.1 is a version, not an address.
func isVersionWord(w string) bool {
	for _, r := range w {
		if !unicode.IsDigit(r) && r != '.' && r != 'v' && r != 'V' {
			return false
		}
	}
	return true
}

// sendInviteMail sends the mail of a new invitation and reports what became of it:
// the page of the owner says honestly whether the person was written to or the
// link has to be passed on (REQ-683). The send is made inside the request, so that
// the answer is not "probably". A mail without an address, on a server that cannot
// send, or that cannot build a link, is not sent at all.
//
// target is what the person is invited to (the organization, or the project of a
// guest link). The link is built the way every mail link is (mailBase): from the
// configured public address, never from the Host header.
func (a *app) sendInviteMail(r *http.Request, l lang, orgID int64, to, target, role, code string, project bool, expires time.Time) inviteMail {
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
	// The mail goes out over this server's address: what the sender typed (the name
	// of the organization, their own name) stays one plain line, and a person, an
	// organization and an address get only a few a day.
	inviter := firstNonEmpty(mailLine(browserPrincipal(r).Label, maxMailLine), "ghosttree")
	target = firstNonEmpty(mailLine(target, maxMailLine), l.msg("mail.invite.target_fallback"))
	if err := a.store.ReserveInviteMail(browserPrincipal(r).ID, orgID, to); err != nil {
		if errors.Is(err, store.ErrInviteMailLimit) {
			return inviteMail{State: inviteMailLimited, To: to}
		}
		return inviteMail{State: inviteMailFailed, To: to}
	}
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
	case inviteMailLimited:
		return l.msg("adm.invite.nomail_limited", m.To)
	}
	return ""
}
