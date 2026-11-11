package server

import (
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/deliverylog"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Zustellprotokoll, Sitzungssignale und die Berichte darüber (REQ-636, P0).
// Geschrieben wird wie bei den Werkzeugzählern: wer im Projekt Wissen anlegen
// darf, meldet für seine Sitzung. Gelesen werden nur Summen, über die
// Projekte, die der Leser vollständig sehen darf (statsProjects); weder Pfade
// noch Objektnummern noch Sitzungen verlassen den Store.
//
// Die Pfade liegen nicht unter /api/context/ und nicht unter /api/runbooks/:
// ein älterer Server läse das Wort dahinter als Nummer und antwortete 400,
// was ein Client von einer Ablehnung nicht unterscheiden könnte.
var deliveryLogRoutes = map[string]routeClass{
	"POST /api/context-log":     classProject,
	"GET /api/usage/tools":      classProject,
	"GET /api/usage/deliveries": classProject,
	"GET /api/usage/signals":    classProject,
}

func init() {
	for pattern, class := range deliveryLogRoutes {
		accessRoutes[pattern] = class
	}
}

func (a *api) registerDeliveryLogRoutes(mux *http.ServeMux) {
	a.routeFunc(mux, "POST /api/context-log", a.recordContextLog)
	a.routeFunc(mux, "GET /api/usage/tools", a.usageTools)
	a.routeFunc(mux, "GET /api/usage/deliveries", a.usageDeliveries)
	a.routeFunc(mux, "GET /api/usage/signals", a.usageSignals)
}

func (a *api) recordContextLog(w http.ResponseWriter, r *http.Request) {
	var b store.ContextLogBatch
	if !readLimitedJSON(w, r, &b, smallBody) {
		return
	}
	b.Project = scope.NormalizeRemote(b.Project)
	if err := b.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	err := a.access(r).RecordContextLog(b)
	if denyAccess(w, err) {
		return
	}
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// sinceDay is the start of a report: a day (2026-11-10) or a time. Anything
// else is a mistake the caller should hear about rather than a window that
// silently covers everything.
var sinceDay = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}(T\d{2}:\d{2}:\d{2}Z)?$`)

func reportWindow(w http.ResponseWriter, r *http.Request) (project, since string, ok bool) {
	since = r.URL.Query().Get("since")
	if !sinceDay.MatchString(since) {
		writeErr(w, http.StatusBadRequest, "since must be a day like 2026-11-10 (UTC)")
		return "", "", false
	}
	project = r.URL.Query().Get("project")
	if project != "" {
		project = scope.NormalizeRemote(project)
	}
	return project, since, true
}

func (a *api) usageTools(w http.ResponseWriter, r *http.Request) {
	project, since, ok := reportWindow(w, r)
	if !ok {
		return
	}
	rep, err := a.access(r).ToolReportSince(project, since)
	if denyAccess(w, err) {
		return
	}
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (a *api) usageDeliveries(w http.ResponseWriter, r *http.Request) {
	project, since, ok := reportWindow(w, r)
	if !ok {
		return
	}
	rep, err := a.access(r).DeliveryReport(project, since)
	if denyAccess(w, err) {
		return
	}
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	lines := func(in []store.DeliveryLine) []deliverylog.DeliveryLine {
		out := make([]deliverylog.DeliveryLine, 0, len(in))
		for _, l := range in {
			out = append(out, deliverylog.DeliveryLine{Via: l.Via, Kind: l.Kind, Parts: l.Parts, Chars: l.Chars, Sessions: l.Sessions})
		}
		return out
	}
	writeJSON(w, http.StatusOK, deliverylog.DeliveryUsage{
		Since: rep.Since, Sessions: rep.Sessions, Chars: rep.Chars,
		SubagentChars: rep.SubagentChars, SubagentSessions: rep.SubagentSessions,
		Lines: lines(rep.Lines), Folded: lines(rep.Folded), FoldedFrom: rep.FoldedFrom,
	})
}

func (a *api) usageSignals(w http.ResponseWriter, r *http.Request) {
	project, since, ok := reportWindow(w, r)
	if !ok {
		return
	}
	rep, err := a.access(r).SignalReport(project, since)
	if denyAccess(w, err) {
		return
	}
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	lines := make([]deliverylog.SignalLine, 0, len(rep.Lines))
	for _, l := range rep.Lines {
		lines = append(lines, deliverylog.SignalLine{Name: l.Name, Value: l.Value, Sessions: l.Sessions, Measured: l.Measured, Short: l.Short})
	}
	writeJSON(w, http.StatusOK, deliverylog.SignalUsage{Since: rep.Since, Lines: lines})
}

// setDelivered tells the hook how the body about to be sent divides into parts.
// It must run before the header is written. No parts, no header: a body that
// carries nothing deliverable has nothing to say.
func setDelivered(w http.ResponseWriter, parts []deliverylog.Part) {
	if h := deliverylog.Header(parts); h != "" {
		w.Header().Set(deliverylog.HeaderName, h)
	}
}

// bodyBuffer collects an answer that is sent in one piece. It stands in for
// the ResponseWriter of the helpers that write sections of a briefing, so a
// header can still be set after they ran.
type bodyBuffer struct {
	h http.Header
	b strings.Builder
}

func newBodyBuffer() *bodyBuffer { return &bodyBuffer{h: http.Header{}} }

func (x *bodyBuffer) Header() http.Header         { return x.h }
func (x *bodyBuffer) Write(p []byte) (int, error) { return x.b.Write(p) }
func (x *bodyBuffer) WriteHeader(int)             {}
func (x *bodyBuffer) String() string              { return x.b.String() }
func (x *bodyBuffer) runes() int                  { return utf8.RuneCountInString(x.b.String()) }
