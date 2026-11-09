package web

import (
	"bytes"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
)

// Fehlerseiten der neuen Oberfläche (REQ-599, Phase 2 B): ein unbekannter oder
// verborgener Link (404), ein verbotener (403) und ein zu großes Formular (413)
// antworten mit einer Seite in der Hülle statt mit einer Zeile Text. Die 404 sagt
// nichts über Rechte: unbekannt und verborgen sehen gleich aus.

// errorView is the content of an error page.
type errorView struct {
	Code, Icon, Title, Text string
	BackHref, BackLabel     string
	AltHref, AltLabel       string
}

// errorPageWriter holds back the plain-text answer of a handler that ends in 404 or
// 403 and, once the handler is done, writes the page instead.
type errorPageWriter struct {
	http.ResponseWriter
	a        *app
	r        *http.Request
	keep     *formKeep
	code     int
	captured bool
	reject   string
	body     bytes.Buffer
}

// rejectHeader is how a handler tells the wrapper why it said no, where the status
// alone does not (a missing form token is 403 like a missing right).
const rejectHeader = "X-Gt-Reject"

func (w *errorPageWriter) WriteHeader(code int) {
	plain := strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain")
	switch {
	case plain && (code == http.StatusNotFound || code == http.StatusForbidden) && w.r.Method == http.MethodGet:
		w.code, w.captured = code, true
		return
	case plain && code >= http.StatusBadRequest && w.r.Method == http.MethodPost && formNavigation(w.r):
		// A form the browser sent as a page change: the plain-text refusal becomes the
		// page the form was on, or a page of its own, with what was typed (failForm).
		w.code, w.captured = code, true
		w.reject = w.Header().Get(rejectHeader)
		w.Header().Del(rejectHeader)
		return
	}
	w.Header().Del(rejectHeader)
	w.ResponseWriter.WriteHeader(code)
}

func (w *errorPageWriter) Write(b []byte) (int, error) {
	if w.captured {
		if w.r.Method == http.MethodPost {
			w.body.Write(b)
		}
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}

// Flush keeps streams (the event feed of the rooms) working through the wrapper.
func (w *errorPageWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *errorPageWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// finish writes the page for a held-back answer.
func (w *errorPageWriter) finish() {
	if !w.captured {
		return
	}
	h := w.ResponseWriter.Header()
	if w.r.Method == http.MethodPost {
		for _, k := range []string{"Content-Type", "Content-Length", "X-Content-Type-Options"} {
			h.Del(k)
		}
		r := w.r
		if w.keep != nil && w.keep.values != nil {
			r, _ = withFormKeep(r)
			if k, ok := r.Context().Value(formKeepKey{}).(*formKeep); ok {
				k.values = w.keep.values
			}
		}
		text := formFailText(w.a.langFor(r), w.code, w.body.String(), w.reject)
		w.a.failForm(w.ResponseWriter, r, w.code, text, "")
		return
	}
	h.Del("Content-Type")
	h.Del("X-Content-Type-Options")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	r := w.a.errorScope(w.r)
	w.a.renderBrowser(&statusWriter{ResponseWriter: w.ResponseWriter, code: w.code}, r, "errorpage", pageData{Title: w.a.langFor(r).msg(errorTitleKey(w.code)), ErrV: w.a.errorViewFor(r, w.code)})
}

// errorScope puts the head of the repository over an error page when the address
// or the room it asked for belongs to a repository the viewer can see: the page
// then stays inside the place the viewer was in. For anything else (no such
// repository, one without a role) the request is returned as it came, so the page
// says nothing about what exists.
func (a *app) errorScope(r *http.Request) *http.Request {
	if repoFrom(r) != nil {
		return r
	}
	remote, tab := "", ""
	if rest, ok := strings.CutPrefix(r.URL.Path, "/ui/"); ok {
		segs := strings.Split(strings.Trim(rest, "/"), "/")
		if rem, more, ok := parseRepoPath(segs); ok && !reservedSegment(segs[0]) {
			remote, tab = rem, "code"
			if len(more) > 0 && more[0] != "tree" && more[0] != "blob" {
				tab = more[0]
			}
		}
	}
	if remote == "" && strings.HasPrefix(r.URL.Path, "/ui/coord") {
		remote, tab = a.projectOfRoom(r.URL.Query().Get("room")), "rooms"
	}
	if remote == "" {
		return r
	}
	opts, _ := a.projectOptions(r, a.access(r))
	if !slices.ContainsFunc(opts, func(o projectOption) bool { return o.Remote == remote }) {
		return r
	}
	return a.withRepoBand(r, remote, tab)
}

// statusWriter writes the status before the first byte.
type statusWriter struct {
	http.ResponseWriter
	code int
	sent bool
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.sent {
		w.sent = true
		w.ResponseWriter.WriteHeader(w.code)
	}
	return w.ResponseWriter.Write(b)
}

func errorTitleKey(code int) string {
	if code == http.StatusForbidden {
		return "err.403.title"
	}
	return "err.404.title"
}

// errorViewFor words the page for the place the viewer was in.
func (a *app) errorViewFor(r *http.Request, code int) *errorView {
	l := a.langFor(r)
	path := r.URL.Path
	v := &errorView{Code: codeText(code), Icon: "search", BackHref: "/ui/inbox", BackLabel: l.msg("err.back_inbox")}
	if code == http.StatusForbidden {
		v.Icon = "lock"
		v.Title, v.Text = l.msg("err.403.title"), l.msg("err.403.text")
	} else {
		v.Title, v.Text = l.msg("err.404.title"), l.msg("err.404.text")
	}
	switch {
	case strings.HasPrefix(path, "/ui/coord"):
		v.BackHref, v.BackLabel = "/ui/coord", l.msg("coord.forbidden.back")
		if code == http.StatusNotFound {
			v.Title = l.msg("err.404.room")
		}
	case strings.HasPrefix(path, "/ui/knowledge"):
		v.BackHref, v.BackLabel = "/ui/knowledge", l.msg("err.back_knowledge")
	case strings.HasPrefix(path, "/ui/sessions"):
		v.BackHref, v.BackLabel = "/ui/sessions", l.msg("err.back_sessions")
	case strings.HasPrefix(path, "/ui/agents"):
		v.BackHref, v.BackLabel = "/ui/agents", l.msg("err.back_agents")
	default:
		// Inside a repository the way back leads to the repository, whose head
		// the page already shows.
		if rv := repoFrom(r); rv != nil {
			v.BackHref, v.BackLabel = rv.Href, l.msg("err.back_repo")
		}
	}
	return v
}

func codeText(code int) string {
	switch code {
	case http.StatusForbidden:
		return "403"
	case http.StatusNotFound:
		return "404"
	}
	return "413"
}

// serverVersionText is the version label of the running server for the footer of
// the pages that have no page data of their own (the join pages). The last app
// started sets it.
var serverVersionText atomic.Value

func srvVersion() string {
	v, _ := serverVersionText.Load().(string)
	return v
}
