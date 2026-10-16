package web

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const painsShown = 100

var (
	painStateKeys = map[string]string{"active": "pains.state.active", "mitigated": "pains.state.mitigated", "resolved": "pains.state.resolved", "obsolete": "pains.state.obsolete"}
	painEventKeys = map[string]string{"created": "pains.event.created", "state": "pains.event.state", "corrected": "pains.event.corrected",
		"link_added": "pains.event.link_added", "link_removed": "pains.event.link_removed"}
	painEvidenceKeys = map[string]string{"commit": "requests.evidence.commit", "test": "requests.evidence.test", "file": "requests.evidence.file",
		"decision": "requests.evidence.decision", "url": "requests.evidence.url", "session": "requests.evidence.session", "request": "requests.evidence.request"}
)

type painRow struct {
	ID                 int64
	Human, Title, Href string
	State, StateLabel  string
	Remaining, Project string
}

type painsView struct {
	Rows        []painRow
	Project     string
	Empty       bool
	ShowProject bool
}

type painLinkLine struct {
	Kind, Text, Href string
}

type painEventLine struct {
	Label, Text, By, Age string
	Evidence             *evidenceLine
}

type painView struct {
	ID                                int64
	Human, Title, Project, Person     string
	State, StateLabel, Remaining, Age string
	Blocks                            []bodyBlock
	Motivates                         []painLinkLine
	Events                            []painEventLine
	CanWork                           bool
	States                            []filterOption
	EvidenceKinds                     []filterOption
	Token, Back                       string
	Result                            *requestResult
}

func painURL(id int64) string { return "/ui/pains/" + strconv.FormatInt(id, 10) }

func (a *app) painsPage(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	w.Header().Set("Cache-Control", "no-store")
	pa := a.access(r)
	project := selectedProject(a.shellFor(r, "pains"))
	// Every state, active and mitigated first: a resolved pain with its
	// evidence has to stay findable.
	state := "all"
	v := &painsView{Project: project, ShowProject: project == ""}
	for _, p := range visibleProjects(pa, project) {
		list, err := pa.SearchPains(pain.Filter{Project: p, State: state, Limit: painsShown})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, p := range list {
			row := painRow{ID: p.ID, Human: p.HumanID(), Title: p.Title, Href: painURL(p.ID), State: p.State, StateLabel: label(l, painStateKeys, p.State), Remaining: p.Remaining}
			if v.ShowProject {
				row.Project = lastSegment(p.Project)
			}
			v.Rows = append(v.Rows, row)
		}
	}
	v.Empty = len(v.Rows) == 0
	a.renderBrowser(w, r, "pains", pageData{Title: l.msg("pains.title"), PainsV: v})
}

func lastSegment(project string) string {
	if i := strings.LastIndex(project, "/"); i >= 0 {
		return project[i+1:]
	}
	return project
}

func (a *app) painPage(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	w.Header().Set("Cache-Control", "no-store")
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	pa := a.access(r)
	detail, err := pa.PainDetailView(id)
	if err != nil {
		a.painFail(w, r, err)
		return
	}
	p := detail.Pain
	v := &painView{ID: p.ID, Human: p.HumanID(), Title: p.Title, Project: lastSegment(p.Project), Person: p.Person,
		State: p.State, StateLabel: label(l, painStateKeys, p.State), Remaining: p.Remaining, Age: shortAge(l, overviewNow().UTC(), parseTime(p.CreatedAt)),
		Blocks: bodyBlocks(p.Body, 0), Token: csrfOf(r), Back: "/ui/pains?project=" + url.QueryEscape(p.Project)}
	v.CanWork = pa.CheckPain(p, store.ActWork) == nil
	for _, s := range pain.States {
		v.States = append(v.States, filterOption{Value: s, Label: label(l, painStateKeys, s), Selected: s == p.State})
	}
	for _, k := range pain.EvidenceKinds {
		v.EvidenceKinds = append(v.EvidenceKinds, filterOption{Value: k, Label: label(l, painEvidenceKeys, k), Selected: k == "commit"})
	}
	for _, l := range detail.Motivates {
		line := painLinkLine{Kind: l.Kind}
		switch l.Kind {
		case "request":
			line.Text, line.Href = "REQ-"+strconv.FormatInt(l.RequestID, 10)+" "+ellipsize(l.Title, 80), requestURL(l.RequestID)
		case "knowledge":
			line.Text, line.Href = ellipsize(l.Title, 80), "/ui/knowledge/"+strconv.FormatInt(l.KnowledgeID, 10)
		default:
			line.Text = ellipsize(l.ExternalRef, 120)
			if strings.HasPrefix(l.ExternalRef, "https://") || strings.HasPrefix(l.ExternalRef, "http://") {
				line.Href = l.ExternalRef
			}
		}
		v.Motivates = append(v.Motivates, line)
	}
	now := overviewNow().UTC()
	for i := len(detail.Events) - 1; i >= 0; i-- {
		e := detail.Events[i]
		line := painEventLine{Label: label(l, painEventKeys, e.Kind), By: e.Person, Age: shortAge(l, now, parseTime(e.CreatedAt))}
		switch e.Kind {
		case "state":
			line.Label = label(l, painStateKeys, e.ToState)
			line.Text = firstNonEmpty(e.Remaining, e.Note)
			if e.EvidenceKind != "" {
				ev := a.evidenceLineFor(l, pa, p.Project, requestdomain.Evidence{Kind: e.EvidenceKind, Ref: e.EvidenceRef})
				ev.Label = label(l, painEvidenceKeys, e.EvidenceKind)
				line.Evidence = &ev
			}
		case "corrected":
			line.Text = e.Note
		case "link_added", "link_removed":
			line.Text = ellipsize(linkDataText(e.Data), 120)
		}
		v.Events = append(v.Events, line)
	}
	switch {
	case r.URL.Query().Get("fail") != "":
		v.Result = &requestResult{Text: l.msg(painFailKey(r.URL.Query().Get("fail"))), Kind: "failed", Fail: true}
	case r.URL.Query().Get("done") == "state":
		v.Result = &requestResult{Text: l.msg("pains.result.state"), Kind: "done"}
	}
	a.renderBrowser(w, r, "pain", pageData{Title: p.HumanID(), PainV: v})
}

func (a *app) painFail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrAccessForbidden):
		http.Error(w, "forbidden", http.StatusForbidden)
	case errors.Is(err, store.ErrAccessNotFound), errors.Is(err, sql.ErrNoRows):
		http.NotFound(w, r)
	case requestdomain.Code(err) != "":
		http.Error(w, "invalid input: "+requestdomain.Code(err), http.StatusBadRequest)
	default:
		http.Error(w, "could not save", http.StatusInternalServerError)
	}
}

// painState moves a pain along the transition table. The rules (evidence,
// remaining, note) are the store's; this only passes the form through.
func (a *app) painState(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	to := r.FormValue("state")
	if !slices.Contains(pain.States, to) {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	for _, f := range []string{"remaining", "evidence_ref", "note"} {
		if len(r.FormValue(f)) > maxFieldBytes {
			http.Error(w, "invalid input", http.StatusBadRequest)
			return
		}
	}
	accountID, _ := store.AccountNumericID(browserPrincipal(r).ID)
	actor := pain.Actor{AccountID: accountID, Person: personOf(r)}
	ev := pain.Evidence{Kind: r.FormValue("evidence_kind"), Ref: r.FormValue("evidence_ref")}
	if strings.TrimSpace(ev.Ref) == "" {
		ev = pain.Evidence{}
	}
	if _, err := a.access(r).SetPainState(id, to, r.FormValue("remaining"), ev, r.FormValue("note"), actor); err != nil {
		if code := requestdomain.Code(err); code != "" {
			http.Redirect(w, r, painURL(id)+"?fail="+url.QueryEscape(code), http.StatusSeeOther)
			return
		}
		a.painFail(w, r, err)
		return
	}
	http.Redirect(w, r, painURL(id)+"?done=state", http.StatusSeeOther)
}

// linkDataText writes the target of a link event ("request:1", "knowledge:3",
// "external:text") the way the rest of the page names it.
func linkDataText(data string) string {
	kind, rest, _ := strings.Cut(data, ":")
	switch kind {
	case "request":
		return "REQ-" + rest
	case "knowledge":
		return "#" + rest
	case "external":
		return rest
	}
	return data
}

// painFailKey maps the fail= value of a redirect to its message; unknown values
// read as a plain failure.
func painFailKey(code string) string {
	switch code {
	case "remaining_required":
		return "pains.result.remaining_required"
	case "evidence_required":
		return "pains.result.evidence_required"
	case "evidence_invalid":
		return "pains.result.evidence_invalid"
	case "note_required":
		return "pains.result.note_required"
	case "invalid_transition":
		return "pains.result.invalid_transition"
	}
	return "pains.result.failed"
}
