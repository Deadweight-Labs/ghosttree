package web

import (
	"database/sql"
	"errors"
	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/explain"
	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Requests (REQ-435 P6). Die Liste und die Detailseite zeigen nur, was der
// Betrachter lesen darf (RequestFilter, RequestHitView, RequestDetailView);
// Sessions erscheinen nur über ihre Adresse und nur, wenn das Transkript lesbar
// ist. Aktionen prüfen das Recht am Auftrag auf dem Server wie die API
// (checkRequest); eine Ablehnung ohne Leserecht antwortet wie eine unbekannte Nummer.

const (
	requestsShown   = 25
	requestWorkChip = 2
	activityShown   = 30
	maxTextBytes    = 100_000
	maxFieldBytes   = 2_000
	maxTitleBytes   = 500
	maxPriority     = 64
	// Ein Formular kodiert Zeilenumbrüche und Umlaute mehrfach (bis zu sechs
	// Bytes je Zeichen); die Grenze liegt über dem Sechsfachen des längsten Textes.
	requestCorrectForm = 6*maxTextBytes + 16<<10
)

var (
	requestTypes   = []string{"feature", "change", "bug", "investigation"}
	evidenceKinds  = []string{"commit", "test", "file", "decision", "url"}
	requestDoneMsg = map[string]string{
		"added": "requests.done.added", "met": "requests.done.met", "waived": "requests.done.waived",
		"corrected": "requests.done.corrected", "completed": "requests.done.completed", "dropped": "requests.done.dropped",
		"moved": "requests.done.moved",
	}
)

// Die Schlüssel stehen ausgeschrieben, damit der Katalogtest sie findet.
var (
	requestTypeKeys  = map[string]string{"feature": "requests.type.feature", "change": "requests.type.change", "bug": "requests.type.bug", "investigation": "requests.type.investigation"}
	requestStateKeys = map[string]string{"open": "requests.state.open", "done": "requests.state.done", "dropped": "requests.state.dropped", "all": "requests.state.all"}
	criterionKeys    = map[string]string{"open": "requests.criterion.open", "met": "requests.criterion.met", "waived": "requests.criterion.waived"}
	roleKeys         = map[string]string{"primary": "requests.role.primary", "related": "requests.role.related"}
	workKeys         = map[string]string{"active": "requests.work.active", "paused": "requests.work.paused", "completed": "requests.work.completed", "abandoned": "requests.work.abandoned"}
	relationKeys     = map[string]string{"parent": "requests.rel.parent", "related": "requests.rel.related", "blocks": "requests.rel.blocks", "duplicates": "requests.rel.duplicates",
		"supersedes": "requests.rel.supersedes", "derived": "requests.rel.derived", "knowledge": "requests.rel.knowledge", "external": "requests.rel.external"}
	evidenceKeys = map[string]string{"commit": "requests.evidence.commit", "test": "requests.evidence.test", "file": "requests.evidence.file", "decision": "requests.evidence.decision",
		"url": "requests.evidence.url", "session": "requests.evidence.session", "request": "requests.evidence.request"}
)

// label gibt den Text zu einem Wert, sonst den Wert selbst.
func label(l lang, keys map[string]string, v string) string {
	if key, ok := keys[v]; ok {
		return l.msg(key)
	}
	return v
}

type evidenceLine struct {
	Kind, Label, Ref, Href string
	// Note is what the writer put after the address or the file: Ref is the
	// thing itself, Note the words about it. Both stood in one string.
	Note string
	// Person is who gave the proof.
	Person string
	// Git is the anchor the proof was written at.
	Git *anchorView
}

type criterionLine struct {
	Number     int
	ID         int64
	Human      string
	Text       string
	State      string
	StateLabel string
	Evidence   []evidenceLine
	CanResolve bool
	// Event is the anchor of the history entry that met or waived the
	// requirement ("event-12"), empty while it is open.
	Event string
}

type workLine struct {
	Href, Title, Role, State, StateLabel, Summary, Age string
	// Kind is the avatar of the session's harness.
	Kind   string
	Active bool
	// LiveNow: active work and a fresh sign of life (live.go).
	LiveNow bool
	// Handle is "claude@mainex" (harness at machine), Owner the person the
	// session belongs to. Both stay empty for a session the viewer may not read.
	Handle, Owner string
	// Started is when the work began, External the id the agent reports as.
	Started, External string
}

type relationLine struct {
	Kind, Label, Text, Href string
}

type requestPainLine struct {
	ID                                 int64
	Href, Title, StateLabel, Remaining string
}

type requestTaskLine struct {
	KindLabel, Title, Where, StateLabel string
}

type activityLine struct {
	Label, Text, By, Age string
	Code                 bool
}

type requestResult struct {
	Text, Kind string
	Fail       bool
}

type requestView struct {
	// First is the head of the bubble that opens the timeline.
	First firstHead
	// Reactions is the row under the text of the issue page.
	Reactions                      *reactionsView
	Explain                        *explainView
	ID                             int64
	Human, Title, Project          string
	Type, TypeLabel, Priority      string
	State, StateLabel, Person, Age string
	Blocks                         []bodyBlock
	Done, Total, Percent           int
	Progress                       string
	Criteria                       []criterionLine
	Work                           []workLine
	Relations                      []relationLine
	Pains                          []requestPainLine
	// Ideas are the ideas the issue came out of; the issue page shows them as its source.
	Ideas                    []ideaFact
	Tasks                    []requestTaskLine
	Threads                  []coordThreadView
	Activity                 []activityLine
	Result                   *requestResult
	Open, CanEdit, CanWork   bool
	CanComplete              bool
	RawTitle, RawDescription string
	Types, Kinds             []filterOption
	Priorities               []string
	EditOpen                 bool
	Back                     string
	Token                    string
	// ProjectRemote ist das ganze Projekt, Repo das Ziel der Rücklinks.
	ProjectRemote string
	// Nur die Issue-Seite füllt diese Felder (fillIssue).
	Blockers []blockerLine
	Links    []issueLink
	Branch   *branchLine
	Base     string
	// Events is the history as sentences; Panel the open action panel (edit,
	// move, complete, drop), "" for none.
	Events []issueEvent
	// Timeline is Events as entries of the timeline component.
	Timeline []tlItem
	// Composer is the field under the history that closes the issue (nil when the
	// viewer cannot).
	Composer *composerView
	Panel    string
	// Opened is "opened this 3 d ago", the line under the title.
	Opened string
	// Actor is who opened it (REQ-620).
	Actor actorView
	// Live is who works on the issue right now (live.go).
	Live []liveAgent
	// LabelBox is the block "Labels" of the sidebar (issue page only).
	LabelBox *labelBoxView
	// MoveProjects sind die Projekte, in die der Betrachter den Auftrag legen
	// darf; leer heißt: kein Formular.
	MoveProjects []filterOption
}

func requestTypeLabel(l lang, typ string) string { return label(l, requestTypeKeys, typ) }

func requestStateLabel(l lang, state string) string { return label(l, requestStateKeys, state) }

func requestURL(id int64) string { return "/ui/requests/" + strconv.FormatInt(id, 10) }

func requestObject(pa *store.ProjectAccess, id int64) store.Object {
	return store.Object{Own: pa.OwnsRequest(id), RequestID: id}
}

// ----------------------------------------------------------------------- Liste

// --------------------------------------------------------------------- Detail

// lookupRequest löst die Nummer auf und prüft das Recht. Wer den Auftrag nicht
// lesen darf, bekommt bei jeder Aktion dieselbe Antwort wie bei einer
// unbekannten Nummer, nicht "verboten".
func (a *app) lookupRequest(w http.ResponseWriter, r *http.Request, act store.Action) (requestdomain.Detail, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return requestdomain.Detail{}, false
	}
	pa := a.access(r)
	ref, err := a.store.RequestRef(store.RefRequest, id)
	if errors.Is(err, sql.ErrNoRows) {
		pa.Filtered()
		http.NotFound(w, r)
		return requestdomain.Detail{}, false
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return requestdomain.Detail{}, false
	}
	obj, visible := pa.RequestObject(ref)
	if !visible {
		// Aus der privaten Session eines anderen Kontos: wie eine unbekannte Nummer.
		pa.Filtered()
		http.NotFound(w, r)
		return requestdomain.Detail{}, false
	}
	if act != store.ActRead && a.accessDenied(w, r, pa.Check(ref.Project, store.ResRequest, store.ActRead, obj)) {
		return requestdomain.Detail{}, false
	}
	if a.accessDenied(w, r, pa.Check(ref.Project, store.ResRequest, act, obj)) {
		return requestdomain.Detail{}, false
	}
	if act != store.ActRead && pa.RequestHeldOwner(ref) {
		// Der Besitzer der Herkunfts-Session übernimmt den Auftrag.
		_ = a.store.ClaimDistilledRequest(id, store.RefRequest, personOf(r))
	}
	detail, err := a.store.RequestByID(id)
	if err != nil {
		http.NotFound(w, r)
		return requestdomain.Detail{}, false
	}
	return detail, true
}

var pubRefPattern = regexp.MustCompile(`^session:([A-Za-z0-9_-]+)(#.*)?$`)
var sessionScrub = regexp.MustCompile(`session:[^\s#]+(#\S*)?`)

func (a *app) evidenceLineFor(l lang, pa *store.ProjectAccess, project string, e requestdomain.Evidence) evidenceLine {
	line := evidenceLine{Kind: e.Kind, Label: evidenceLabel(l, e.Kind), Ref: e.Ref, Person: e.Person, Git: anchorViewOf(pa, project, e.GitAnchor, e.Via)}
	line.Ref, line.Note = splitProofRef(e.Kind, e.Ref)
	switch e.Kind {
	case "session":
		line.Ref = ""
		var pub, frag string
		if pa.SeesSessionNumbers(project) {
			var ok bool
			if pub, frag, ok = pa.SessionLink(e.Ref); !ok {
				return line
			}
		} else if m := pubRefPattern.FindStringSubmatch(e.Ref); m != nil {
			pub, frag = m[1], strings.TrimPrefix(m[2], "#")
		} else {
			return line
		}
		line.Href = "/ui/sessions/" + pub + fragmentOf(frag)
		line.Ref = l.msg("requests.open_session")
	case "url":
		if strings.HasPrefix(line.Ref, "https://") || strings.HasPrefix(line.Ref, "http://") {
			line.Href = line.Ref
		}
	}
	return line
}

// splitProofRef parts a proof as people write it, "https://… (what it shows)"
// or "docs/a.md (what is in it)", into the thing and the note about it. An
// address ends at the first blank; a file or a decision at the first " (". A
// note wrapped in one pair of brackets loses them. Other kinds (a commit, a
// test, a session) stay whole.
func splitProofRef(kind, ref string) (thing, note string) {
	ref = strings.TrimSpace(ref)
	switch kind {
	case "url":
		if strings.HasPrefix(ref, "https://") || strings.HasPrefix(ref, "http://") {
			if i := strings.IndexAny(ref, " \t\n"); i > 0 {
				return ref[:i], unwrapNote(ref[i:])
			}
		}
	case "file", "decision":
		if i := strings.Index(ref, " ("); i > 0 {
			return strings.TrimSpace(ref[:i]), unwrapNote(ref[i:])
		}
	}
	return ref, ""
}

// unwrapNote trims a note and takes off one pair of brackets that encloses it
// all; "(PR #169), inklusive" stays as written because its first bracket
// closes before the end.
func unwrapNote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '(' || s[len(s)-1] != ')' {
		return s
	}
	depth := 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 && i < len(s)-1 {
				return s
			}
		}
	}
	return strings.TrimSpace(s[1 : len(s)-1])
}

func evidenceLabel(l lang, kind string) string { return label(l, evidenceKeys, kind) }

// requestPage is the old address of a request. A request of a project the
// viewer can open leads on to its page in the repository; the old page stays
// for the others (requests without a project).
func (a *app) requestPage(w http.ResponseWriter, r *http.Request) {
	if remote := a.requestRepo(r); remote != "" {
		target := repoHref(remote) + "/issues/" + url.PathEscape(r.PathValue("id"))
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusFound)
		return
	}
	a.issueGlobal(w, r)
}

// requestRepo is the project a request belongs to when the viewer can read the
// request and open the project's repository page; "" otherwise.
func (a *app) requestRepo(r *http.Request) string {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return ""
	}
	pa := a.access(r)
	if !a.requestReadable(pa, id) {
		return ""
	}
	ref, err := a.store.RequestRef(store.RefRequest, id)
	if err != nil || ref.Project == "" {
		return ""
	}
	opts, _ := a.projectOptions(r, pa)
	if !slices.ContainsFunc(opts, func(o projectOption) bool { return o.Remote == ref.Project }) {
		return ""
	}
	return ref.Project
}

// buildRequestView reads a request for the viewer and fills everything both
// pages show. It answers the failure cases itself (ok is false then).
func (a *app) buildRequestView(w http.ResponseWriter, r *http.Request) (*requestView, requestdomain.Detail, bool) {
	l := a.langFor(r)
	detail, ok := a.lookupRequest(w, r, store.ActRead)
	if !ok {
		return nil, detail, false
	}
	pa := a.access(r)
	req := detail.Request
	obj := requestObject(pa, req.ID)
	project := req.Scope.Project
	linked, err := a.browserCoord(r).ThreadsForObject("request", req.HumanID())
	if err != nil {
		coordHTTPError(l, w, err)
		return nil, detail, false
	}
	var threads []coordThreadView
	for _, thread := range linked {
		home, homeErr := a.browserCoord(r).ThreadHome(thread.ID)
		if homeErr != nil {
			continue
		}
		threads = append(threads, coordThreadView{ID: thread.ID, Title: thread.Title, Question: thread.Question, State: thread.State, URL: coordThreadURL(home.RoomKey, thread.ID)})
	}
	raw := detail
	detail = pa.RequestDetailView(detail)
	now := overviewNow().UTC()
	v := &requestView{
		ID: req.ID, Human: req.HumanID(), Title: req.Title, Type: req.Type, TypeLabel: requestTypeLabel(l, req.Type),
		Priority: req.Priority, State: req.State, StateLabel: requestStateLabel(l, req.State), Person: req.Person,
		Age: shortAge(l, now, parseTime(req.CreatedAt)), Blocks: bodyBlocks(req.Description, 0), Threads: threads,
		Open: req.State == "open", Token: csrfOf(r), Back: "/ui/requests",
		RawTitle: req.Title, RawDescription: req.Description,
		Types: requestTypeOptions(l, req.Type), Kinds: evidenceKindOptions(l),
	}
	v.Actor = a.actorViewOf(l, pa, actor.Request, req.ID, req.Person, true)
	if project != "" {
		v.Project = path.Base(project)
		v.Back += "?project=" + url.QueryEscape(project)
	}
	v.Explain = a.explainFor(l, explain.KindRequest, req.ID)
	v.CanEdit = pa.Check(project, store.ResRequest, store.ActEdit, obj) == nil
	v.CanWork = pa.Check(project, store.ResRequest, store.ActWork, obj) == nil
	v.EditOpen = v.CanEdit && r.URL.Query().Get("edit") == "1"
	if v.CanEdit {
		v.Priorities = a.knownPriorities(pa, project, req.Priority)
		v.MoveProjects = writableOptions(pa, project)
	}
	open := 0
	humans := map[int64]string{}
	for _, c := range detail.Criteria {
		line := criterionLine{Number: c.Number, ID: c.ID, Human: c.HumanID(), Text: c.Description, State: c.State, StateLabel: criterionStateLabel(l, c.State),
			CanResolve: v.Open && v.CanWork && c.State == "open"}
		humans[c.ID] = c.HumanID()
		if c.State == "open" {
			open++
		} else {
			v.Done++
		}
		for _, e := range c.Evidence {
			line.Evidence = append(line.Evidence, a.evidenceLineFor(l, pa, project, e))
		}
		v.Criteria = append(v.Criteria, line)
	}
	v.Total = len(detail.Criteria)
	if v.Total > 0 {
		v.Percent = v.Done * 100 / v.Total
		v.Progress = strconv.Itoa(v.Done) + "/" + strconv.Itoa(v.Total)
	}
	v.CanComplete = v.Open && v.CanEdit && open == 0
	v.Work = a.workLines(l, pa, detail.Work, now)
	v.Relations = a.relationLines(l, pa, detail.Relations)
	for _, p := range detail.Pains {
		v.Pains = append(v.Pains, requestPainLine{ID: p.ID, Href: painURL(p.ID), Title: p.Title, StateLabel: label(l, painStateKeys, p.State), Remaining: p.Remaining})
	}
	for _, t := range detail.Tasks {
		line := requestTaskLine{KindLabel: label(l, taskKindKeys, t.Kind), Title: t.Title, Where: t.Location}
		if t.State != "open" {
			line.StateLabel = label(l, taskStateKeys, t.State)
		}
		v.Tasks = append(v.Tasks, line)
	}
	v.Activity = activityLines(l, a.visibleActivity(pa, detail), humans, now)
	v.Result = a.requestResult(r, raw)
	v.ProjectRemote = project
	return v, detail, true
}

func criterionStateLabel(l lang, state string) string { return label(l, criterionKeys, state) }

func requestTypeOptions(l lang, current string) []filterOption {
	out := make([]filterOption, 0, len(requestTypes))
	for _, t := range requestTypes {
		out = append(out, filterOption{Value: t, Label: requestTypeLabel(l, t), Selected: t == current})
	}
	return out
}

func evidenceKindOptions(l lang) []filterOption {
	out := make([]filterOption, 0, len(evidenceKinds))
	for i, k := range evidenceKinds {
		out = append(out, filterOption{Value: k, Label: evidenceLabel(l, k), Selected: i == 0})
	}
	return out
}

// knownPriorities schlägt die Prioritäten vor, die im Projekt schon vorkommen.
func (a *app) knownPriorities(pa *store.ProjectAccess, project, current string) []string {
	set := map[string]bool{}
	if current != "" {
		set[current] = true
	}
	if known, err := a.store.RequestPriorities(pa.RequestFilter(requestdomain.SearchFilter{Scope: scope.Axes{Project: project}})); err == nil {
		for _, p := range known {
			set[p] = true
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// workLines names the sessions behind the work of a request. The detail has
// already dropped the sessions the viewer may not read (RequestDetailView); the
// check here repeats that rule, so no handle, owner or title of a session leaks
// if a caller ever passes an unfiltered detail.
func (a *app) workLines(l lang, pa *store.ProjectAccess, work []requestdomain.Work, now time.Time) []workLine {
	var out []workLine
	for _, wk := range work {
		line := workLine{Role: label(l, roleKeys, wk.Role), State: wk.State, StateLabel: label(l, workKeys, wk.State),
			Summary: wk.Summary, Active: wk.State == "active", Age: shortAge(l, now, parseTime(firstNonEmpty(wk.EndedAt, wk.StartedAt)))}
		line.Title = l.msg("requests.private_session")
		if wk.SessionPublicID != "" {
			line.Href = "/ui/sessions/" + wk.SessionPublicID
			line.Title = l.msg("requests.session")
			if raw, err := a.store.SessionByPublicID(wk.SessionPublicID); err == nil && pa.CanSeeTranscript(raw) {
				// Die Ansicht des Betrachters: ein Gast sieht weder Besitzer noch
				// Kennung noch Maschine einer fremden Session.
				sess := pa.SessionView(raw)
				line.Kind = harnessKind(sess.Harness)
				line.Handle = sessionHandle(pa, sess)
				line.External, line.Started = sess.ExternalID, wk.StartedAt
				if sess.AccountID != 0 {
					if acct, err := a.store.AccountByPrincipalID("person:" + strconv.FormatInt(sess.AccountID, 10)); err == nil {
						line.Owner = acct.Name
					}
				}
				if strings.TrimSpace(sess.Title) != "" {
					line.Title = ellipsize(sess.Title, 80)
				}
			}
		}
		out = append(out, line)
	}
	slices.SortStableFunc(out, func(x, y workLine) int {
		switch {
		case x.Active && !y.Active:
			return -1
		case !x.Active && y.Active:
			return 1
		}
		return 0
	})
	return out
}

// relationLines zeigt nur Gegenstellen, die der Betrachter lesen darf: eine
// Nummer aus einem verborgenen Projekt verriete, dass es sie gibt.
func (a *app) relationLines(l lang, pa *store.ProjectAccess, rels []requestdomain.Relation) []relationLine {
	var out []relationLine
	for _, rel := range rels {
		line := relationLine{Kind: rel.Kind, Label: label(l, relationKeys, rel.Kind)}
		switch {
		case rel.OtherRequestID != 0:
			if !a.requestReadable(pa, rel.OtherRequestID) {
				continue
			}
			other, err := a.store.RequestByID(rel.OtherRequestID)
			if err != nil {
				continue
			}
			line.Href, line.Text = requestURL(other.Request.ID), other.Request.HumanID()+" "+ellipsize(other.Request.Title, 80)
		case rel.KnowledgeID != 0:
			k, err := a.store.KnowledgeByID(rel.KnowledgeID)
			if err != nil || !pa.CanSeeKnowledge(k) {
				continue
			}
			line.Href, line.Text = "/ui/knowledge/"+strconv.FormatInt(k.ID, 10), ellipsize(pa.KnowledgeView(k).Title, 80)
		default:
			line.Text = ellipsize(rel.ExternalRef, 120)
			if strings.HasPrefix(rel.ExternalRef, "https://") || strings.HasPrefix(rel.ExternalRef, "http://") {
				line.Href = rel.ExternalRef
			}
		}
		out = append(out, line)
	}
	return out
}

func (a *app) requestReadable(pa *store.ProjectAccess, id int64) bool {
	ref, err := a.store.RequestRef(store.RefRequest, id)
	if err != nil {
		return false
	}
	obj, visible := pa.RequestObject(ref)
	return visible && pa.Check(ref.Project, store.ResRequest, store.ActRead, obj) == nil
}

func (a *app) knowledgeReadable(pa *store.ProjectAccess, id int64) bool {
	k, err := a.store.KnowledgeByID(id)
	return err == nil && pa.CanSeeKnowledge(k)
}

var removedTarget = regexp.MustCompile(`^\S+ (?:REQ-(\d+)|knowledge #(\d+))(?: |$)`)

// visibleActivity lässt die Beziehungs-Einträge weg, deren Gegenüber der
// Betrachter nicht lesen darf: im Verlauf stünde sonst, dass es sie gibt.
// "relation.added" nennt das Ziel ("<Art> REQ-N", "<Art> knowledge #N"); das
// entscheidet. Ältere Einträge nennen nur die Art: sie werden der Reihe nach
// je einer noch bestehenden Beziehung gleicher Art und Zeit zugeordnet (jede
// höchstens einmal). Was sich nicht zuordnen lässt, sehen nur Betrachter, die
// alle Beziehungen des Auftrags lesen dürfen; für alle anderen bleibt es weg.
func (a *app) visibleActivity(pa *store.ProjectAccess, d requestdomain.Detail) []requestdomain.Activity {
	readable := func(rel requestdomain.Relation) bool {
		switch {
		case rel.OtherRequestID != 0:
			return a.requestReadable(pa, rel.OtherRequestID)
		case rel.KnowledgeID != 0:
			return a.knowledgeReadable(pa, rel.KnowledgeID)
		}
		return true
	}
	seesAll := pa.SeesSessionNumbers(d.Request.Scope.Project)
	for _, rel := range d.Relations {
		seesAll = seesAll && readable(rel)
	}
	used := make([]bool, len(d.Relations))
	out := make([]requestdomain.Activity, 0, len(d.Activity))
	for _, act := range d.Activity {
		switch act.Kind {
		case "relation.added":
			if m := removedTarget.FindStringSubmatch(act.Data); m != nil {
				var rel requestdomain.Relation
				rel.OtherRequestID, _ = strconv.ParseInt(m[1], 10, 64)
				rel.KnowledgeID, _ = strconv.ParseInt(m[2], 10, 64)
				if !readable(rel) {
					continue
				}
				break
			}
			if strings.Contains(act.Data, " ") {
				// Neuer Eintrag mit externem Ziel: nichts, was verborgen sein könnte.
				break
			}
			matched := false
			ok := true
			for i, rel := range d.Relations {
				if !used[i] && rel.Kind == act.Data && rel.CreatedAt == act.CreatedAt {
					used[i], matched, ok = true, true, readable(rel)
					break
				}
			}
			if (matched && !ok) || (!matched && !seesAll) {
				continue
			}
		case "relation.removed":
			m := removedTarget.FindStringSubmatch(act.Data)
			if m == nil {
				// Eine externe Verknüpfung nennt keine Nummer.
				if strings.Contains(act.Data, "REQ-") || strings.Contains(act.Data, "knowledge #") {
					continue
				}
				break
			}
			var rel requestdomain.Relation
			rel.OtherRequestID, _ = strconv.ParseInt(m[1], 10, 64)
			rel.KnowledgeID, _ = strconv.ParseInt(m[2], 10, 64)
			if !readable(rel) {
				continue
			}
		}
		out = append(out, act)
	}
	return out
}

var activityKinds = map[string]string{
	"request.created": "requests.act.created", "request.corrected": "requests.act.corrected", "request.moved": "requests.act.moved", "request.done": "requests.act.done",
	"request.dropped": "requests.act.dropped", "request.migrated": "requests.act.migrated", "criterion.added": "requests.act.criterion_added",
	"criterion.met": "requests.act.criterion_met", "criterion.waived": "requests.act.criterion_waived",
	"relation.added": "requests.act.relation_added", "relation.removed": "requests.act.relation_removed",
	"work.started": "requests.act.work_started", "work.resumed": "requests.act.work_resumed", "work.finished": "requests.act.work_finished",
	"evidence.migrated": "requests.act.evidence_migrated",
	"request.reopened":  "requests.act.request_reopened", "criterion.reopened": "requests.act.criterion_reopened",
	"relation.taken_over": "requests.act.relation_taken_over", "pr.merged": "requests.act.pr_merged",
	"package.added": "requests.act.package_added", "package.removed": "requests.act.package_removed", "package.role": "requests.act.package_role",
	"pain.linked": "requests.act.pain_linked", "pain.unlinked": "requests.act.pain_unlinked",
	"task.created": "requests.act.task_created", "task.done": "requests.act.task_done", "task.confirmed": "requests.act.task_confirmed",
	"task.reopened": "requests.act.task_reopened", "task.dropped": "requests.act.task_dropped",
}

// activityLines schreibt den Verlauf neu auf: Sessionnummern und Zeilen-Ids
// verlassen den Server nie, die Aktivitäten (schon vom Betrachter gefiltert)
// stehen neu nach alt.
func activityLines(l lang, acts []requestdomain.Activity, humans map[int64]string, now time.Time) []activityLine {
	out := make([]activityLine, 0, len(acts))
	for i := len(acts) - 1; i >= 0 && len(out) < activityShown; i-- {
		act := acts[i]
		key, ok := activityKinds[act.Kind]
		line := activityLine{Label: act.Kind, By: act.Person, Age: shortAge(l, now, parseTime(act.CreatedAt))}
		if ok {
			line.Label = l.msg(key)
		}
		switch act.Kind {
		case "criterion.added", "request.dropped", "request.corrected", "request.moved", "work.finished", "request.done":
			line.Text = ellipsize(sessionScrub.ReplaceAllString(act.Data, l.msg("requests.session")), 200)
		case "pain.linked", "pain.unlinked", "task.created", "task.done", "task.confirmed", "task.reopened", "task.dropped":
			line.Text = ellipsize(act.Data, 200)
		case "criterion.met", "criterion.waived":
			if n, err := strconv.ParseInt(strings.TrimPrefix(act.Data, "AC-"), 10, 64); err == nil {
				line.Text, line.Code = humans[n], humans[n] != ""
			}
		case "work.started", "work.resumed":
			_, role, _ := strings.Cut(act.Data, "role:")
			role, _, _ = strings.Cut(strings.TrimSpace(role), " ")
			if act.Data != "" && !strings.Contains(act.Data, "role:") {
				role = act.Data
			}
			if _, ok := roleKeys[role]; ok {
				line.Text = label(l, roleKeys, role)
			}
		}
		out = append(out, line)
	}
	return out
}

// requestResult ist die Ergebniszeile nach einer Aktion. Sie erscheint nur,
// wenn der Zustand sie stützt; eine erfundene Adresse zeigt nichts.
func (a *app) requestResult(r *http.Request, d requestdomain.Detail) *requestResult {
	l := a.langFor(r)
	q := r.URL.Query()
	if fail := q.Get("fail"); fail == "open_criteria" && d.Request.State == "open" {
		return &requestResult{Text: l.msg("requests.fail.open_criteria"), Kind: "failed", Fail: true}
	}
	kind := q.Get("done")
	key, ok := requestDoneMsg[kind]
	if !ok {
		return nil
	}
	res := &requestResult{Kind: kind}
	switch kind {
	case "added", "met", "waived":
		n, err := strconv.ParseInt(q.Get("c"), 10, 64)
		if err != nil {
			return nil
		}
		for _, c := range d.Criteria {
			if c.ID == n && (kind == "added" || c.State == kind) {
				res.Text = l.msg(key, ellipsize(c.Description, 80))
			}
		}
		if res.Text == "" {
			return nil
		}
	case "completed":
		if d.Request.State != "done" {
			return nil
		}
		res.Text = l.msg(key)
	case "dropped":
		if d.Request.State != "dropped" {
			return nil
		}
		res.Text = l.msg(key)
	default:
		res.Text = l.msg(key)
	}
	return res
}

// -------------------------------------------------------------------- Aktionen

// requestFail antwortet auf einen Verstoß gegen eine Regel des Auftrags: eine
// offene Prüfung wird auf der Seite gezeigt, alles andere ist ein Fehlerstatus
// wie bei der API.
func (a *app) requestFail(w http.ResponseWriter, r *http.Request, id int64, err error) {
	var rule *requestdomain.RuleError
	switch {
	case errors.As(err, &rule) && rule.ErrorCode == "open_criteria":
		http.Redirect(w, r, requestURL(id)+"?fail=open_criteria", http.StatusSeeOther)
	case errors.As(err, &rule) && (rule.ErrorCode == "request_terminal" || rule.ErrorCode == "criterion_terminal" || rule.ErrorCode == "request_not_completable"):
		http.Error(w, "nothing to change", http.StatusConflict)
	case errors.As(err, &rule):
		http.Error(w, "invalid input", http.StatusBadRequest)
	case errors.Is(err, sql.ErrNoRows):
		http.NotFound(w, r)
	default:
		http.Error(w, "could not save", http.StatusInternalServerError)
	}
}

// requestFormTooLarge liest das Formular vor requireCSRF und antwortet auf eine
// überschrittene Grenze mit 413 und einer gestalteten Seite, statt dass der
// Lesefehler dort als fehlendes Token (403) erscheint.
func (a *app) requestFormTooLarge(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				w.Header().Set("Cache-Control", "no-store")
				l := a.langFor(r)
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				a.renderBrowser(w, r, "requesttoolarge", pageData{Title: l.msg("requests.toolarge.title"), BackURL: "/ui/requests",
					ErrV: &errorView{Code: "413", Icon: "file", Title: l.msg("requests.toolarge.title"), Text: l.msg("requests.toolarge.body"), BackHref: r.URL.Path, BackLabel: l.msg("requests.toolarge.back_issue"), AltHref: "/ui/requests", AltLabel: l.msg("requests.toolarge.back")}})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func lineBreaks(s string) string { return strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(s) }

func cleanText(s string, limit int) (string, bool) {
	s = strings.TrimSpace(lineBreaks(s))
	return s, s != "" && len(s) <= limit
}

func (a *app) requestDone(w http.ResponseWriter, r *http.Request, id int64, done string, extra url.Values) {
	q := url.Values{"done": {done}}
	for k, v := range extra {
		q[k] = v
	}
	http.Redirect(w, r, requestURL(id)+"?"+q.Encode(), http.StatusSeeOther)
}

func (a *app) requestAddCriterion(w http.ResponseWriter, r *http.Request) {
	detail, ok := a.lookupRequest(w, r, store.ActEdit)
	if !ok {
		return
	}
	text, valid := cleanText(r.FormValue("description"), maxFieldBytes)
	if !valid {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	c, err := a.store.AddCriterion(detail.Request.ID, text, personOf(r), gitanchor.Web())
	if err != nil {
		a.requestFail(w, r, detail.Request.ID, err)
		return
	}
	a.requestDone(w, r, detail.Request.ID, "added", url.Values{"c": {strconv.FormatInt(c.ID, 10)}})
}

// requestResolve markiert ein Kriterium als erfüllt oder erlassen; der Beleg
// ist Pflicht, wie in der API.
func (a *app) requestResolve(w http.ResponseWriter, r *http.Request) {
	cid, err := strconv.ParseInt(r.PathValue("cid"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	detail, ok := a.lookupRequest(w, r, store.ActWork)
	if !ok {
		return
	}
	var found bool
	for _, c := range detail.Criteria {
		found = found || c.ID == cid
	}
	if !found {
		a.access(r).Filtered()
		http.NotFound(w, r)
		return
	}
	state := "met"
	if r.FormValue("state") == "waived" {
		state = "waived"
	}
	ref, valid := cleanText(r.FormValue("evidence_ref"), maxFieldBytes)
	kind := r.FormValue("evidence_kind")
	if !valid || !slices.Contains(evidenceKinds, kind) {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	if err := a.store.SetCriterionState(cid, state, requestdomain.Evidence{Kind: kind, Ref: ref, Person: personOf(r), GitAnchor: gitanchor.Web()}); err != nil {
		a.requestFail(w, r, detail.Request.ID, err)
		return
	}
	if state == "met" {
		a.stampActor(r, actor.Criterion, cid)
	}
	a.requestDone(w, r, detail.Request.ID, state, url.Values{"c": {strconv.FormatInt(cid, 10)}})
}

// requestCorrect ändert nur, was sich wirklich geändert hat, und prüft Länge
// nur dort: ein Altbestand über der Grenze bleibt korrigierbar, solange das
// Feld unberührt bleibt. Die Beschreibung wird nicht getrimmt (ein
// eingerückter Codeblock bleibt), nur die Zeilenumbrüche werden vereinheitlicht;
// sie darf leer werden, wie in der API.
func (a *app) requestCorrect(w http.ResponseWriter, r *http.Request) {
	detail, ok := a.lookupRequest(w, r, store.ActEdit)
	if !ok {
		return
	}
	req := detail.Request
	reason, valid := cleanText(r.FormValue("reason"), maxFieldBytes)
	if !valid {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	patch := map[string]string{}
	if title := strings.TrimSpace(lineBreaks(r.FormValue("title"))); title != strings.TrimSpace(req.Title) {
		if title == "" || len(title) > maxTitleBytes {
			http.Error(w, "invalid input", http.StatusBadRequest)
			return
		}
		patch["title"] = title
	}
	if description := lineBreaks(r.FormValue("description")); description != lineBreaks(req.Description) {
		if len(description) > maxTextBytes {
			http.Error(w, "invalid input", http.StatusBadRequest)
			return
		}
		patch["description"] = description
	}
	if typ := r.FormValue("type"); typ != req.Type {
		if !slices.Contains(requestTypes, typ) {
			http.Error(w, "invalid input", http.StatusBadRequest)
			return
		}
		patch["type"] = typ
	}
	if priority := strings.TrimSpace(r.FormValue("priority")); priority != strings.TrimSpace(req.Priority) {
		if len(priority) > maxPriority {
			http.Error(w, "invalid input", http.StatusBadRequest)
			return
		}
		patch["priority"] = priority
	}
	if len(patch) == 0 {
		http.Error(w, "nothing to change", http.StatusBadRequest)
		return
	}
	if err := a.store.UpdateRequest(req.ID, patch, personOf(r), reason, gitanchor.Web()); err != nil {
		a.requestFail(w, r, req.ID, err)
		return
	}
	a.requestDone(w, r, req.ID, "corrected", nil)
}

func (a *app) requestComplete(w http.ResponseWriter, r *http.Request) {
	detail, ok := a.lookupRequest(w, r, store.ActEdit)
	if !ok {
		return
	}
	ref, valid := cleanText(r.FormValue("evidence_ref"), maxFieldBytes)
	kind := r.FormValue("evidence_kind")
	if !valid || !slices.Contains(evidenceKinds, kind) {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	if err := a.store.CompleteRequest(detail.Request.ID, requestdomain.Evidence{Kind: kind, Ref: ref, Person: personOf(r), GitAnchor: gitanchor.Web()}); err != nil {
		a.requestFail(w, r, detail.Request.ID, err)
		return
	}
	a.stampActor(r, actor.RequestDone, detail.Request.ID)
	a.requestDone(w, r, detail.Request.ID, "completed", a.alsoComment(r, detail.Request.ID))
}

func (a *app) requestDrop(w http.ResponseWriter, r *http.Request) {
	detail, ok := a.lookupRequest(w, r, store.ActEdit)
	if !ok {
		return
	}
	reason, valid := cleanText(r.FormValue("reason"), maxFieldBytes)
	if !valid {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	if err := a.store.DropRequest(detail.Request.ID, reason, personOf(r), gitanchor.Web()); err != nil {
		a.requestFail(w, r, detail.Request.ID, err)
		return
	}
	a.requestDone(w, r, detail.Request.ID, "dropped", a.alsoComment(r, detail.Request.ID))
}
