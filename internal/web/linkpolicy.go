package web

import (
	"net/http"
	"net/url"
	"strings"
	"time"
)

// linkSessionMaxAge ist die feste Laufzeit einer Sitzung aus einem Einmal-Link
// (Owner-Link für ein Mitglied, ctx web). Sie rückt bei Nutzung nicht nach: ein
// Link, der Minuten gilt, soll keine Sitzung von Wochen werden.
const linkSessionMaxAge = 12 * time.Hour

// linkRule sagt, was eine Sitzung aus einem Einmal-Link an einer Route darf.
type linkRule int

const (
	// linkAllowed: gewöhnliche Arbeit; sie ändert weder Rechte noch Zugänge und
	// berührt keine Geheimnisse.
	linkAllowed linkRule = iota + 1
	// linkProof: die Route ändert Rechte oder Zugänge, gibt etwas frei oder berührt
	// Geheimnisse. Sie verlangt eine Anmeldung mit echtem Nachweis (Passwort,
	// E-Mail-Code, SSO), nicht einen Link, der sich weiterreichen lässt.
	linkProof
)

// linkSideEffectGET sind die lesenden Routen, die trotzdem etwas ändern.
var linkSideEffectGET = map[string]bool{
	"GET /ui/orgs/source/callback": true,
}

// linkRules ordnet jede Route, die etwas ändert, einer der beiden Regeln zu. Eine
// Route ohne Eintrag gilt als linkProof, und ein Test bricht, bis sie hier steht.
var linkRules = map[string]linkRule{
	// ---- gewöhnliche Arbeit
	"POST /ui/logout":                       linkAllowed,
	"POST /join/{code}/signout":             linkAllowed,
	"POST /ui/welcome/dismiss":              linkAllowed,
	"POST /ui/searches":                     linkAllowed,
	"POST /ui/inbox/mention-read":           linkAllowed,
	"POST /ui/inbox/mention-reply":          linkAllowed,
	"POST /ui/inbox/reply":                  linkAllowed,
	"POST /ui/inbox/done":                   linkAllowed,
	"POST /ui/comments/{id}/edit":           linkAllowed,
	"POST /ui/comments/{id}/delete":         linkAllowed,
	"POST /ui/reactions":                    linkAllowed,
	"POST /ui/issues/create":                linkAllowed,
	"POST /ui/ideas/{id}/state":             linkAllowed,
	"POST /ui/ideas/{id}/correct":           linkAllowed,
	"POST /ui/ideas/{id}/text":              linkAllowed,
	"POST /ui/ideas/{id}/concept":           linkAllowed,
	"POST /ui/ideas/{id}/decisions":         linkAllowed,
	"POST /ui/ideas/{id}/attach":            linkAllowed,
	"POST /ui/decisions/{id}/vote":          linkAllowed,
	"POST /ui/decisions/{id}/variant":       linkAllowed,
	"POST /ui/requests/{id}/criteria":       linkAllowed,
	"POST /ui/requests/{id}/criteria/{cid}": linkAllowed,
	"POST /ui/requests/{id}/correct":        linkAllowed,
	"POST /ui/requests/{id}/move":           linkAllowed,
	"POST /ui/requests/{id}/close":          linkAllowed,
	"POST /ui/requests/{id}/complete":       linkAllowed,
	"POST /ui/requests/{id}/drop":           linkAllowed,
	"POST /ui/requests/{id}/attach":         linkAllowed,
	"POST /ui/pains/{id}/state":             linkAllowed,
	"POST /ui/pains/{id}/idea":              linkAllowed,
	"POST /ui/pains/{id}/correct":           linkAllowed,
	"POST /ui/pains/{id}/attach":            linkAllowed,
	"POST /ui/knowledge/{id}/comment":       linkAllowed,
	"POST /ui/knowledge/{id}/edit":          linkAllowed,
	"POST /ui/knowledge/{id}/move":          linkAllowed,
	"POST /ui/labels/create":                linkAllowed,
	"POST /ui/labels/{id}/update":           linkAllowed,
	"POST /ui/labels/{id}/delete":           linkAllowed,
	"POST /ui/labels/assign":                linkAllowed,
	"POST /ui/labels/make":                  linkAllowed,
	"POST /ui/packages/create":              linkAllowed,
	"POST /ui/packages/{id}/update":         linkAllowed,
	"POST /ui/packages/{id}/state":          linkAllowed,
	"POST /ui/packages/{id}/add":            linkAllowed,
	"POST /ui/packages/{id}/remove":         linkAllowed,
	"POST /ui/features/create":              linkAllowed,
	"POST /ui/features/{id}/edit":           linkAllowed,
	"POST /ui/features/{id}/link":           linkAllowed,
	"POST /ui/features/{id}/unlink":         linkAllowed,
	"POST /ui/agents/{id}/seen":             linkAllowed,
	"POST /ui/workflows/check":              linkAllowed,
	"POST /ui/coord/send":                   linkAllowed,
	"POST /ui/coord/thread/create":          linkAllowed,
	"POST /ui/coord/thread/post":            linkAllowed,
	"POST /ui/coord/thread/state":           linkAllowed,
	"POST /ui/coord/read":                   linkAllowed,
	"POST /ui/coord/unread":                 linkAllowed,
	"POST /ui/coord/attention/action":       linkAllowed,
	"POST /ui/coord/standing/end":           linkAllowed,
	"POST /ui/coord/standing/create":        linkAllowed,
	"POST /ui/coord/direct/start":           linkAllowed,
	"POST /ui/coord/group/create":           linkAllowed,
	"POST /ui/coord/group/update":           linkAllowed,
	"POST /ui/coord/group/leave":            linkAllowed,

	// ---- Rechte, Zugänge, Freigaben, Geheimnisse
	"POST /ui/profile":                      linkProof,
	"POST /ui/profile/email":                linkProof,
	"POST /ui/profile/password":             linkProof,
	"POST /ui/profile/notifications":        linkProof,
	"POST /ui/profile/github/link":          linkProof,
	"POST /ui/profile/github/unlink":        linkProof,
	"POST /ui/email/confirm":                linkProof,
	"POST /ui/account/tokens/revoke":        linkProof,
	"POST /ui/account/tokens/revoke-unused": linkProof,
	"POST /ui/device":                       linkProof,
	"POST /ui/device/decide":                linkProof,
	"POST /join/pair":                       linkProof,
	"POST /join/pair/decide":                linkProof,
	"POST /join/{code}/accept":              linkProof,
	"POST /ui/orgs/accept":                  linkProof,
	"POST /ui/orgs/create":                  linkProof,
	"POST /ui/orgs/rename":                  linkProof,
	"POST /ui/orgs/invite":                  linkProof,
	"POST /ui/orgs/invite/revoke":           linkProof,
	"POST /ui/orgs/member/role":             linkProof,
	"POST /ui/orgs/member/remove":           linkProof,
	"POST /ui/orgs/member/login-link":       linkProof,
	"POST /ui/orgs/project/move":            linkProof,
	"POST /ui/orgs/project/role":            linkProof,
	"POST /ui/orgs/project/create":          linkProof,
	"POST /ui/orgs/project/visibility":      linkProof,
	"POST /ui/orgs/source/connect":          linkProof,
	"POST /ui/orgs/source/add":              linkProof,
	"POST /ui/orgs/source/remove":           linkProof,
	"POST /ui/orgs/source/import":           linkProof,
	"POST /ui/orgs/prefix/visibility":       linkProof,
	"POST /ui/orgs/follow":                  linkProof,
	"POST /ui/orgs/assign":                  linkProof,
	"POST /ui/orgs/garden":                  linkProof,
	"POST /ui/orgs/workflows":               linkProof,
	"POST /ui/orgs/workflow-policy":         linkProof,
	"POST /ui/orgs/ai/connection":           linkProof,
	"POST /ui/orgs/ai/connection/delete":    linkProof,
	"POST /ui/orgs/ai/connection/test":      linkProof,
	"POST /ui/orgs/ai/alias":                linkProof,
	"POST /ui/orgs/ai/alias/delete":         linkProof,
	"POST /ui/orgs/ai/cap":                  linkProof,
	"POST /ui/orgs/ai/lend":                 linkProof,
	"POST /ui/orgs/ai/loan/revoke":          linkProof,
	"POST /ui/orgs/ai/release":              linkProof,
	"POST /ui/orgs/ai/grant":                linkProof,
	"POST /ui/gardener/undo":                linkProof,
	"POST /ui/handover":                     linkProof,
	"POST /ui/orgs/takeover":                linkProof,
	"GET /ui/orgs/source/callback":          linkProof,
	"POST /ui/m/{machine}/share":            linkProof,
	"POST /ui/m/{machine}/unshare":          linkProof,
	"POST /ui/m/{machine}/owner":            linkProof,
	"POST /ui/m/{machine}/entry":            linkProof,
	"POST /ui/runbooks/write":               linkProof,
	"POST /ui/workflows/save":               linkProof,
	"POST /ui/workflows/visual":             linkProof,
	"POST /ui/workflows/enabled":            linkProof,
	"POST /ui/workflows/template":           linkProof,
	"POST /ui/workflows/undo":               linkProof,
	"POST /ui/workflows/rerun":              linkProof,
	"POST /ui/review/bulk":                  linkProof,
	"POST /ui/review/held":                  linkProof,
	"POST /ui/review/{id}/{verdict}":        linkProof,
	"POST /ui/review/{id}/merge":            linkProof,
	"POST /ui/review/{id}/criterion":        linkProof,
	"POST /ui/review/{id}/describe":         linkProof,
	"POST /ui/review/{id}/cover":            linkProof,
	"POST /ui/sessions/{id}/share":          linkProof,
	"POST /ui/agents/{id}/signal":           linkProof,
	"POST /ui/coord/agent/control":          linkProof,
	"POST /ui/tasks/done":                   linkProof,
	"POST /ui/tasks/{id}/steps/{n}":         linkProof,
	"POST /ui/tasks/{id}/note":              linkProof,
	"POST /ui/inbox/answer":                 linkProof,
	"POST /ui/ideas/{id}/phase":             linkProof,
	"POST /ui/decisions/{id}/close":         linkProof,
	"POST /ui/decisions/{id}/reopen":        linkProof,
}

// linkNeedsProof sagt, ob die Anfrage einer Link-Sitzung eine Anmeldung mit Nachweis
// braucht. Lesende Anfragen brauchen sie nie, ausgenommen die mit Nebenwirkung; eine
// ändernde Route ohne Zuordnung verlangt sie (zu, bis ein Test sie zuordnet).
func linkNeedsProof(r *http.Request) bool {
	pattern := r.Pattern
	if rule, ok := linkRules[pattern]; ok {
		return rule == linkProof
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// reloginHref ist der Weg zur Anmeldung mit Nachweis; die Seite, von der der
// Nutzer kam, ist danach das Ziel.
func reloginHref(r *http.Request) string {
	target := "/ui/inbox"
	if ref, err := url.Parse(r.Referer()); err == nil && ref.Host == r.Host {
		if next := safeNext(ref.RequestURI()); next != "" && !strings.HasPrefix(next, "/join/") {
			target = next
		}
	}
	return "/ui/login?relogin=1&next=" + url.QueryEscape(target)
}
