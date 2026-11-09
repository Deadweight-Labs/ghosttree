package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Abgelehnte Formulare (UI-Durchsicht P11). Ein Formular, das der Server nicht
// annimmt, verliert nichts: der Betrachter landet wieder auf der Seite, von der er
// kam, mit einer Meldung auf Deutsch (.callout.is-danger) direkt über dem Formular
// und mit allem, was er getippt hatte, wieder im Feld. Die Seite merkt sich dafür
// ihre Adresse im Feld gt_back (ui-flash.js); die Eingaben liegen kurz im
// Arbeitsspeicher des Servers (flashes), nicht im Cookie, denn das fasst 4 KB.

const (
	// maxFormBody ist die Grenze für den Körper eines Formulars. Sie liegt über allem,
	// was ein Feld zulässt: ein Zeichen kann als Formular bis zu zwölf Bytes brauchen
	// (ein Emoji als %XX%XX%XX%XX), und maxLongText Zeichen sind das längste Feld.
	maxFormBody = 1 << 20
	// maxLongText ist die längste Beschreibung oder der längste Kommentar, in Zeichen,
	// so wie das Feld (maxlength) sie zählt. Dazu passt die Grenze der Raum-Nachrichten
	// von 64 KB.
	maxLongText = 64_000

	flashCookie     = "gt_flash"
	flashTTL        = 10 * time.Minute
	flashMaxEntries = 128
	backField       = "gt_back"
)

// charCount zählt Zeichen, nicht Bytes: ein „ä“ ist eins. So zählt auch das Feld.
func charCount(s string) int { return utf8.RuneCountInString(s) }

// tooLong sagt, ob s mehr als limit Zeichen hat.
func tooLong(s string, limit int) bool { return charCount(s) > limit }

// groupThousands schreibt 64000 als „64.000“ (de) oder „64,000“ (en).
func groupThousands(n int, sep string) string {
	digits := strconv.Itoa(n)
	var b strings.Builder
	for i, d := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteString(sep)
		}
		b.WriteRune(d)
	}
	return b.String()
}

func (l lang) number(n int) string {
	if l == langDE {
		return groupThousands(n, ".")
	}
	return groupThousands(n, ",")
}

// formNavigation sagt, ob dieses Formular als Seitenwechsel ankommt. Nur dann
// ersetzt eine Seite die Klartext-Antwort. Ein Skript, das mit fetch schickt und den
// Statuscode selbst liest (die Räume, die Reaktionen), und jeder andere Client
// bekommt sie wie bisher. Browser nennen den Seitenwechsel im Kopf Sec-Fetch-Mode;
// ohne den Kopf (ältere Browser) entscheidet, ob sie HTML erwarten.
func formNavigation(r *http.Request) bool {
	if mode := r.Header.Get("Sec-Fetch-Mode"); mode != "" {
		return mode == "navigate"
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// formKeep reicht die gelesenen Felder eines Formulars vom Schutz gegen
// Seitenfälschung (csrfGate) zur Fehlerbehandlung weiter, auch wenn dazwischen
// ein anderer Request mit neuem Kontext steht.
type formKeep struct{ values url.Values }

type formKeepKey struct{}

func withFormKeep(r *http.Request) (*http.Request, *formKeep) {
	k := &formKeep{}
	return r.WithContext(context.WithValue(r.Context(), formKeepKey{}, k)), k
}

func rememberForm(r *http.Request) {
	if k, ok := r.Context().Value(formKeepKey{}).(*formKeep); ok && k != nil && r.PostForm != nil {
		k.values = r.PostForm
	}
}

// keptFields sind die Eingaben, die nach einem Fehler wiederkommen: alles außer dem
// Token, der Rücksprungadresse und Feldern, die nach einem Kennwort oder Token
// heißen.
func keptFields(v url.Values) url.Values {
	out := url.Values{}
	for name, vals := range v {
		low := strings.ToLower(name)
		if name == "csrf_token" || name == backField || strings.Contains(low, "password") || strings.Contains(low, "token") {
			continue
		}
		out[name] = slices.Clone(vals)
	}
	return out
}

// ---------------------------------------------------------------- Merkzettel

// flashEntry ist eine abgelehnte Eingabe. Sie gehört einer Person und wird beim
// ersten Anzeigen verbraucht.
type flashEntry struct {
	person  string
	Text    string     `json:"-"`
	Field   string     `json:"field,omitempty"`
	Action  string     `json:"action"`
	Fields  url.Values `json:"fields"`
	expires time.Time
}

type flashes struct {
	mu sync.Mutex
	m  map[string]*flashEntry
}

func (f *flashes) put(e *flashEntry) string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	id := hex.EncodeToString(buf)
	now := time.Now()
	e.expires = now.Add(flashTTL)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m == nil {
		f.m = map[string]*flashEntry{}
	}
	for k, v := range f.m {
		if now.After(v.expires) {
			delete(f.m, k)
		}
	}
	for len(f.m) >= flashMaxEntries {
		oldest := ""
		for k, v := range f.m {
			if oldest == "" || v.expires.Before(f.m[oldest].expires) {
				oldest = k
			}
		}
		delete(f.m, oldest)
	}
	f.m[id] = e
	return id
}

func (f *flashes) take(id, person string) *flashEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.m[id]
	if !ok {
		return nil
	}
	if time.Now().After(e.expires) || e.person != person {
		if e.person == person {
			delete(f.m, id)
		}
		return nil
	}
	delete(f.m, id)
	return e
}

func (a *app) flashBox() *flashes {
	a.flashOnce.Do(func() { a.flashStore = &flashes{} })
	return a.flashStore
}

// flashNote ist, was eine Seite nach einem abgelehnten Formular zeigt.
type flashNote struct {
	Text string
	Data *flashEntry
}

// takeFlash holt die Meldung dieser Person ab und löscht das Cookie.
func (a *app) takeFlash(w http.ResponseWriter, r *http.Request) *flashNote {
	c, err := r.Cookie(flashCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	http.SetCookie(w, a.flashCookieFor(r, "", -1))
	e := a.flashBox().take(c.Value, browserPrincipal(r).ID)
	if e == nil {
		return nil
	}
	return &flashNote{Text: e.Text, Data: e}
}

func (a *app) flashCookieFor(r *http.Request, value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: flashCookie, Value: value, Path: "/", HttpOnly: true,
		Secure: a.secureCookies(r), SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
}

// formBackOf ist die Seite, auf der das Formular stand: gt_back, wenn es ein
// interner Pfad ist.
func formBackOf(r *http.Request) string {
	raw := r.PostFormValue(backField)
	if raw == "" {
		return ""
	}
	return safeNext(raw)
}

// failForm antwortet auf ein Formular, das nicht angenommen wurde. Der Browser
// kommt auf die Seite des Formulars zurück (303), mit der Meldung text und den
// Eingaben; field nennt das Feld, das nicht stimmt. Ohne bekannte Seite zeigt eine
// eigene Seite die Meldung samt den Eingaben. status ist der Code dieser Seite.
func (a *app) failForm(w http.ResponseWriter, r *http.Request, status int, text, field string) {
	a.failFormTo(w, r, "", status, text, field)
}

// failFormTo ist failForm für einen Handler, der die Seite des Formulars selbst
// kennt (back): sie gilt, wenn das Formular keine Rücksprungadresse mitschickte.
func (a *app) failFormTo(w http.ResponseWriter, r *http.Request, back string, status int, text, field string) {
	a.failFormKeeping(w, r, back, status, text, field, "")
}

// failFormKeeping is failFormTo that leaves one field out of what comes back: the text
// that was refused because it looks like a secret is never written onto a page again.
func (a *app) failFormKeeping(w http.ResponseWriter, r *http.Request, back string, status int, text, field, drop string) {
	var values url.Values
	if k, ok := r.Context().Value(formKeepKey{}).(*formKeep); ok && k != nil {
		values = k.values
	}
	if values == nil {
		values = r.PostForm
	}
	kept := keptFields(values)
	if drop != "" {
		delete(kept, drop)
	}
	target := formBackOf(r)
	if target == "" {
		target = safeNext(back)
	}
	if target != "" && formNavigation(r) {
		id := a.flashBox().put(&flashEntry{person: browserPrincipal(r).ID, Text: text, Field: field, Action: r.URL.Path, Fields: kept})
		http.SetCookie(w, a.flashCookieFor(r, id, int(flashTTL/time.Second)))
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	if !formNavigation(r) {
		http.Error(w, text, status)
		return
	}
	a.formFailedPage(w, r, status, text, kept)
}

// formFailedPage zeigt die Meldung auf einer Seite in der Hülle, mit den
// Eingaben, die zu lang oder zu wichtig sind, um sie zu verlieren, und mit einem Weg
// zurück.
func (a *app) formFailedPage(w http.ResponseWriter, r *http.Request, status int, text string, kept url.Values) {
	l := a.langFor(r)
	w.Header().Set("Cache-Control", "no-store")
	view := &formFailedView{Text: text, BackHref: "/ui/inbox", BackLabel: l.msg("err.back_inbox")}
	if ref, err := url.Parse(r.Header.Get("Referer")); err == nil && ref.Path != "" && ref.Path != "/" {
		if p := safeNext(ref.RequestURI()); p != "" {
			view.BackHref, view.BackLabel = p, l.msg("err.form.back")
		}
	}
	names := make([]string, 0, len(kept))
	for name := range kept {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		for _, v := range kept[name] {
			if charCount(v) >= 40 || strings.Contains(v, "\n") {
				view.Kept = append(view.Kept, formKept{Name: name, Text: v})
			}
		}
	}
	w.WriteHeader(status)
	a.renderBrowser(w, r, "formfailed", pageData{Title: l.msg("err.form.title"), FormFailed: view})
}

type formFailedView struct {
	Text, BackHref, BackLabel string
	Kept                      []formKept
}

type formKept struct{ Name, Text string }

// formFailText nennt auf Deutsch (oder in der Servereinstellung), was ein
// Klartext-Fehler eines Handlers bedeutet. reject ist der Grund, den die Handler
// selbst setzen (csrf).
func formFailText(l lang, code int, body, reject string) string {
	body = strings.ToLower(strings.TrimSpace(body))
	switch {
	case reject == "csrf":
		return l.msg("err.form.expired")
	case code == http.StatusRequestEntityTooLarge:
		return l.msg("err.form.toolarge", l.number(maxLongText))
	case code == http.StatusForbidden:
		return l.msg("err.form.forbidden")
	case code == http.StatusNotFound:
		return l.msg("err.form.gone")
	case code == http.StatusTooManyRequests:
		return l.msg("err.form.busy")
	case body == "nothing to change" && code == http.StatusBadRequest:
		return l.msg("err.form.nochange")
	case code == http.StatusConflict:
		return l.msg("err.form.conflict")
	case strings.Contains(body, "required"):
		return l.msg("err.form.required")
	case code >= 500:
		return l.msg("err.form.failed")
	}
	return l.msg("err.form.invalid")
}

// formTooLarge antwortet auf einen Körper über maxFormBody. Es gibt nichts zu
// retten, weil der Server ihn nicht gelesen hat; die Seite sagt, wie lang ein Text
// sein darf, und führt dorthin zurück, wo das Formular stand: zum Issue, wenn es
// dessen Formular war.
func (a *app) formTooLarge(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	w.Header().Set("Cache-Control", "no-store")
	view := &errorView{Code: "413", Icon: "file", Title: l.msg("requests.toolarge.title"),
		Text: l.msg("err.form.toolarge", l.number(maxLongText)), BackHref: "/ui/inbox", BackLabel: l.msg("err.back_inbox")}
	if id, ok := requestOfPath(r.URL.Path); ok {
		view.BackHref, view.BackLabel = requestURL(id), l.msg("requests.toolarge.back_issue")
		view.AltHref, view.AltLabel = "/ui/requests", l.msg("requests.toolarge.back")
		if ref, err := a.store.RequestRef(store.RefRequest, id); err == nil {
			if _, visible := a.access(r).RequestObject(ref); visible {
				r = a.withRepoBand(r, ref.Project, "issues")
			}
		}
	}
	w.WriteHeader(http.StatusRequestEntityTooLarge)
	a.renderBrowser(w, r, "requesttoolarge", pageData{Title: l.msg("requests.toolarge.title"), BackURL: "/ui/requests", ErrV: view})
}

// requestOfPath is the number in /ui/requests/{id}/…, the forms of one issue.
func requestOfPath(path string) (int64, bool) {
	rest, ok := strings.CutPrefix(path, "/ui/requests/")
	if !ok {
		return 0, false
	}
	num, _, _ := strings.Cut(rest, "/")
	id, err := strconv.ParseInt(num, 10, 64)
	return id, err == nil && id > 0
}

// tooLongForm answers a text that has more characters than its field allows: the
// message names the count and the limit, the text stays in the field.
func (a *app) tooLongForm(w http.ResponseWriter, r *http.Request, back, field, value string, limit int) {
	l := a.langFor(r)
	a.failFormTo(w, r, back, http.StatusBadRequest, l.msg("err.form.toolong", l.number(limit), l.number(charCount(value))), field)
}
