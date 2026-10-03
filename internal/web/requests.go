package web

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

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
	// Ein Formular kodiert Zeilenumbrüche und Umlaute mehrfach; die Grenze
	// liegt über dem Dreifachen des längsten Textes.
	requestCorrectForm = 3*maxTextBytes + 8<<10
)

var (
	requestTypes   = []string{"feature", "change", "bug", "investigation"}
	requestStates  = []string{"done", "dropped", "all"}
	evidenceKinds  = []string{"commit", "test", "file", "decision", "url"}
	requestDoneMsg = map[string]string{
		"added": "requests.done.added", "met": "requests.done.met", "waived": "requests.done.waived",
		"corrected": "requests.done.corrected", "completed": "requests.done.completed", "dropped": "requests.done.dropped",
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
		"supersedes": "requests.rel.supersedes", "knowledge": "requests.rel.knowledge", "external": "requests.rel.external"}
	evidenceKeys = map[string]string{"commit": "requests.evidence.commit", "test": "requests.evidence.test", "file": "requests.evidence.file", "decision": "requests.evidence.decision",
		"url": "requests.evidence.url", "session": "requests.evidence.session"}
)

// label gibt den Text zu einem Wert, sonst den Wert selbst.
func label(keys map[string]string, v string) string {
	if key, ok := keys[v]; ok {
		return msg(key)
	}
	return v
}

type requestListRow struct {
	Href, ID, Title, Project string
	Type, TypeLabel          string
	Priority                 string
	State, StateLabel        string
	Done, Total, Percent     int
	Progress                 string
	Working                  []chip
	Age                      string
}

type requestsView struct {
	Q, Project              string
	Selects                 []filterSelect
	Rows                    []requestListRow
	Older                   string
	Guest, Empty, NoMatch   bool
	ClearHref               string
	ShowProject, HasFilters bool
}

type evidenceLine struct {
	Kind, Label, Ref, Href string
}

type criterionLine struct {
	ID         int64
	Human      string
	Text       string
	State      string
	StateLabel string
	Evidence   []evidenceLine
	CanResolve bool
}

type workLine struct {
	Href, Title, Role, State, StateLabel, Summary, Age string
	Active                                             bool
}

type relationLine struct {
	Kind, Label, Text, Href string
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
	Threads                        []coordThreadView
	Activity                       []activityLine
	Result                         *requestResult
	Open, CanEdit, CanWork         bool
	CanComplete                    bool
	RawTitle, RawDescription       string
	Types, Kinds                   []filterOption
	Priorities                     []string
	EditOpen                       bool
	Back                           string
	Token                          string
}

func requestTypeLabel(typ string) string { return label(requestTypeKeys, typ) }

func requestStateLabel(state string) string { return label(requestStateKeys, state) }

func requestURL(id int64) string { return "/ui/requests/" + strconv.FormatInt(id, 10) }

func requestObject(pa *store.ProjectAccess, person string) store.Object {
	return store.Object{Own: pa.IsAuthor(person)}
}

// ----------------------------------------------------------------------- Liste

func requestListHref(q url.Values, cursor string) string {
	v := url.Values{}
	for k, vals := range q {
		v[k] = vals
	}
	if cursor != "" {
		v.Set("cursor", cursor)
	} else {
		v.Del("cursor")
	}
	if len(v) == 0 {
		return "/ui/requests"
	}
	return "/ui/requests?" + v.Encode()
}

func (a *app) requestsPage(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	q := strings.TrimSpace(query.Get("q"))
	state := query.Get("state")
	if !slices.Contains(requestStates, state) {
		state = ""
	}
	typ := query.Get("type")
	if !slices.Contains(requestTypes, typ) {
		typ = ""
	}
	priority := strings.TrimSpace(query.Get("priority"))
	if len(priority) > maxPriority {
		priority = ""
	}
	project := a.projectParam(r)
	pa := a.access(r)
	hidden, stop := a.gateList(w, r, pa, project, store.ResRequest)
	if stop {
		return
	}
	filter := requestdomain.SearchFilter{Scope: scope.Axes{Project: project}, Query: q, Type: typ, Limit: requestsShown, Cursor: query.Get("cursor")}
	switch state {
	case "":
		filter.State = "open"
	case "all":
	default:
		filter.State = state
	}
	if _, err := strconv.ParseInt(filter.Cursor, 10, 64); err != nil {
		filter.Cursor = ""
	}
	filter.Priority = priority
	var page requestdomain.SearchPage
	priorities := map[string]bool{}
	progress := map[int64]store.CriteriaProgress{}
	var work map[int64][]store.ActiveRequestWork
	if !hidden {
		var err error
		if page, err = a.store.SearchRequests(pa.RequestFilter(filter)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		pa.NoteRequestHits(page.Results)
		// Die Auswahl hängt nicht von der Seite ab: alle Prioritäten der lesbaren Menge.
		known, err := a.store.RequestPriorities(pa.RequestFilter(requestdomain.SearchFilter{Scope: scope.Axes{Project: project}}))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, p := range known {
			priorities[p] = true
		}
		if priority != "" {
			priorities[priority] = true
		}
		ids, openIDs := make([]int64, len(page.Results)), []int64{}
		for i, h := range page.Results {
			ids[i] = h.Request.ID
			if h.Request.State == "open" {
				openIDs = append(openIDs, h.Request.ID)
			}
		}
		if progress, err = a.store.CriteriaProgress(ids); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if work, err = a.store.ActiveWorkOnRequests(pa, openIDs); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	now := overviewNow().UTC()
	v := &requestsView{Q: q, Project: project, ShowProject: project == "", Guest: a.shellBaseFor(r).who.kind == viewerGuest}
	for _, raw := range page.Results {
		h := pa.RequestHitView(raw)
		row := requestListRow{
			Href: requestURL(h.Request.ID), ID: h.Request.HumanID(), Title: h.Request.Title,
			Type: h.Request.Type, TypeLabel: requestTypeLabel(h.Request.Type), Priority: h.Request.Priority,
			State: h.Request.State, StateLabel: requestStateLabel(h.Request.State),
			Age: shortAge(now, parseTime(h.Request.CreatedAt)),
		}
		if project == "" && h.Request.Scope.Project != "" {
			row.Project = path.Base(h.Request.Scope.Project)
		}
		if p := progress[h.Request.ID]; p.Total > 0 {
			row.Done, row.Total, row.Percent = p.Done, p.Total, p.Done*100/p.Total
			row.Progress = strconv.Itoa(p.Done) + "/" + strconv.Itoa(p.Total)
		}
		if h.Request.State == "open" {
			row.Working = workingChips(work[h.Request.ID])
		}
		v.Rows = append(v.Rows, row)
	}
	if page.NextCursor != "" {
		v.Older = requestListHref(query, page.NextCursor)
	}
	v.Selects = requestSelects(state, typ, priority, priorities)
	v.HasFilters = q != "" || state != "" || typ != "" || priority != ""
	v.Empty, v.NoMatch = len(v.Rows) == 0 && !v.HasFilters && filter.Cursor == "", len(v.Rows) == 0 && (v.HasFilters || filter.Cursor != "")
	v.ClearHref = "/ui/requests"
	if project != "" {
		v.ClearHref += "?project=" + url.QueryEscape(project)
	}
	a.renderBrowser(w, r, "requests", pageData{Title: "Requests", Project: project, RequestsV: v})
}

func requestSelects(state, typ, priority string, priorities map[string]bool) []filterSelect {
	stateSel := filterSelect{Name: "state", Label: requestStateLabel("open")}
	for _, s := range requestStates {
		stateSel.Options = append(stateSel.Options, filterOption{Value: s, Label: requestStateLabel(s), Selected: s == state})
	}
	typeSel := filterSelect{Name: "type", Label: msg("requests.all_types")}
	for _, t := range requestTypes {
		typeSel.Options = append(typeSel.Options, filterOption{Value: t, Label: requestTypeLabel(t), Selected: t == typ})
	}
	sels := []filterSelect{stateSel, typeSel}
	delete(priorities, "")
	if len(priorities) > 0 {
		list := make([]string, 0, len(priorities))
		for p := range priorities {
			list = append(list, p)
		}
		slices.Sort(list)
		prSel := filterSelect{Name: "priority", Label: msg("requests.all_priorities")}
		for _, p := range list {
			prSel.Options = append(prSel.Options, filterOption{Value: p, Label: p, Selected: p == priority})
		}
		sels = append(sels, prSel)
	}
	return sels
}

// workingChips nennt die Sessions, die gerade an einem Auftrag arbeiten; die
// Abfrage hat schon nur lesbare Transkripte geliefert, per Adresse.
func workingChips(work []store.ActiveRequestWork) []chip {
	var out []chip
	more := 0
	for _, wk := range work {
		if len(out) >= requestWorkChip {
			more++
			continue
		}
		title := msg("requests.session")
		if wk.Title != "" {
			title = ellipsize(wk.Title, 36)
		}
		out = append(out, chip{Href: "/ui/sessions/" + wk.SessionPublicID, Text: title})
	}
	if more > 0 {
		out = append(out, chip{Text: "+" + strconv.Itoa(more)})
	}
	return out
}

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
	obj := store.Object{Own: pa.IsAuthor(ref.Person)}
	if act != store.ActRead && a.accessDenied(w, r, pa.Check(ref.Project, store.ResRequest, store.ActRead, obj)) {
		return requestdomain.Detail{}, false
	}
	if a.accessDenied(w, r, pa.Check(ref.Project, store.ResRequest, act, obj)) {
		return requestdomain.Detail{}, false
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

func (a *app) evidenceLineFor(pa *store.ProjectAccess, project string, e requestdomain.Evidence) evidenceLine {
	line := evidenceLine{Kind: e.Kind, Label: evidenceLabel(e.Kind), Ref: e.Ref}
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
		line.Ref = msg("requests.open_session")
	case "url":
		if strings.HasPrefix(e.Ref, "https://") || strings.HasPrefix(e.Ref, "http://") {
			line.Href = e.Ref
		}
	}
	return line
}

func evidenceLabel(kind string) string { return label(evidenceKeys, kind) }

func (a *app) requestPage(w http.ResponseWriter, r *http.Request) {
	detail, ok := a.lookupRequest(w, r, store.ActRead)
	if !ok {
		return
	}
	pa := a.access(r)
	req := detail.Request
	obj := requestObject(pa, req.Person)
	project := req.Scope.Project
	linked, err := a.browserCoord(r).ThreadsForObject("request", req.HumanID())
	if err != nil {
		coordHTTPError(w, err)
		return
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
		ID: req.ID, Human: req.HumanID(), Title: req.Title, Type: req.Type, TypeLabel: requestTypeLabel(req.Type),
		Priority: req.Priority, State: req.State, StateLabel: requestStateLabel(req.State), Person: req.Person,
		Age: shortAge(now, parseTime(req.CreatedAt)), Blocks: bodyBlocks(req.Description, 0), Threads: threads,
		Open: req.State == "open", Token: csrfOf(r), Back: "/ui/requests",
		RawTitle: req.Title, RawDescription: req.Description,
		Types: requestTypeOptions(req.Type), Kinds: evidenceKindOptions(),
	}
	if project != "" {
		v.Project = path.Base(project)
		v.Back += "?project=" + url.QueryEscape(project)
	}
	v.CanEdit = pa.Check(project, store.ResRequest, store.ActEdit, obj) == nil
	v.CanWork = pa.Check(project, store.ResRequest, store.ActWork, obj) == nil
	v.EditOpen = v.CanEdit && r.URL.Query().Get("edit") == "1"
	if v.CanEdit {
		v.Priorities = a.knownPriorities(pa, project, req.Priority)
	}
	open := 0
	humans := map[int64]string{}
	for _, c := range detail.Criteria {
		line := criterionLine{ID: c.ID, Human: c.HumanID(), Text: c.Description, State: c.State, StateLabel: criterionStateLabel(c.State),
			CanResolve: v.Open && v.CanWork && c.State == "open"}
		humans[c.ID] = c.HumanID()
		if c.State == "open" {
			open++
		} else {
			v.Done++
		}
		for _, e := range c.Evidence {
			line.Evidence = append(line.Evidence, a.evidenceLineFor(pa, project, e))
		}
		v.Criteria = append(v.Criteria, line)
	}
	v.Total = len(detail.Criteria)
	if v.Total > 0 {
		v.Percent = v.Done * 100 / v.Total
		v.Progress = strconv.Itoa(v.Done) + "/" + strconv.Itoa(v.Total)
	}
	v.CanComplete = v.Open && v.CanEdit && open == 0
	v.Work = a.workLines(detail.Work, now)
	v.Relations = a.relationLines(pa, detail.Relations)
	v.Activity = activityLines(a.visibleActivity(pa, detail), humans, now)
	v.Result = a.requestResult(r, raw)
	a.renderBrowser(w, r, "request", pageData{Title: req.HumanID(), RequestV: v})
}

func criterionStateLabel(state string) string { return label(criterionKeys, state) }

func requestTypeOptions(current string) []filterOption {
	out := make([]filterOption, 0, len(requestTypes))
	for _, t := range requestTypes {
		out = append(out, filterOption{Value: t, Label: requestTypeLabel(t), Selected: t == current})
	}
	return out
}

func evidenceKindOptions() []filterOption {
	out := make([]filterOption, 0, len(evidenceKinds))
	for i, k := range evidenceKinds {
		out = append(out, filterOption{Value: k, Label: evidenceLabel(k), Selected: i == 0})
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

func (a *app) workLines(work []requestdomain.Work, now time.Time) []workLine {
	var out []workLine
	for _, wk := range work {
		line := workLine{Role: label(roleKeys, wk.Role), State: wk.State, StateLabel: label(workKeys, wk.State),
			Summary: wk.Summary, Active: wk.State == "active", Age: shortAge(now, parseTime(firstNonEmpty(wk.EndedAt, wk.StartedAt)))}
		line.Title = msg("requests.private_session")
		if wk.SessionPublicID != "" {
			line.Href = "/ui/sessions/" + wk.SessionPublicID
			line.Title = msg("requests.session")
			if sess, err := a.store.SessionByPublicID(wk.SessionPublicID); err == nil && strings.TrimSpace(sess.Title) != "" {
				line.Title = ellipsize(sess.Title, 80)
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
func (a *app) relationLines(pa *store.ProjectAccess, rels []requestdomain.Relation) []relationLine {
	var out []relationLine
	for _, rel := range rels {
		line := relationLine{Kind: rel.Kind, Label: label(relationKeys, rel.Kind)}
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
	return err == nil && pa.Check(ref.Project, store.ResRequest, store.ActRead, requestObject(pa, ref.Person)) == nil
}

func (a *app) knowledgeReadable(pa *store.ProjectAccess, id int64) bool {
	k, err := a.store.KnowledgeByID(id)
	return err == nil && pa.CanSeeKnowledge(k)
}

var removedTarget = regexp.MustCompile(`^\S+ (?:REQ-(\d+)|knowledge #(\d+))(?: |$)`)

// visibleActivity lässt die Beziehungs-Einträge weg, deren Gegenüber der
// Betrachter nicht lesen darf: im Verlauf stünde sonst, dass es sie gibt. Das
// Ziel von "relation.added" steht nicht im Eintrag, es wird über Art und
// Zeitpunkt der Beziehung gefunden; was sich nicht auflösen lässt (die
// Beziehung wurde inzwischen zurückgenommen), bleibt weg.
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
	out := make([]requestdomain.Activity, 0, len(d.Activity))
	for _, act := range d.Activity {
		switch act.Kind {
		case "relation.added":
			found := false
			ok := true
			for _, rel := range d.Relations {
				if rel.Kind == act.Data && rel.CreatedAt == act.CreatedAt {
					found = true
					ok = ok && readable(rel)
				}
			}
			if !found || !ok {
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
	"request.created": "requests.act.created", "request.corrected": "requests.act.corrected", "request.done": "requests.act.done",
	"request.dropped": "requests.act.dropped", "request.migrated": "requests.act.migrated", "criterion.added": "requests.act.criterion_added",
	"criterion.met": "requests.act.criterion_met", "criterion.waived": "requests.act.criterion_waived",
	"relation.added": "requests.act.relation_added", "relation.removed": "requests.act.relation_removed",
	"work.started": "requests.act.work_started", "work.resumed": "requests.act.work_resumed", "work.finished": "requests.act.work_finished",
	"evidence.migrated": "requests.act.evidence_migrated",
}

// activityLines schreibt den Verlauf neu auf: Sessionnummern und Zeilen-Ids
// verlassen den Server nie, die Aktivitäten (schon vom Betrachter gefiltert)
// stehen neu nach alt.
func activityLines(acts []requestdomain.Activity, humans map[int64]string, now time.Time) []activityLine {
	out := make([]activityLine, 0, len(acts))
	for i := len(acts) - 1; i >= 0 && len(out) < activityShown; i-- {
		act := acts[i]
		key, ok := activityKinds[act.Kind]
		line := activityLine{Label: act.Kind, By: act.Person, Age: shortAge(now, parseTime(act.CreatedAt))}
		if ok {
			line.Label = msg(key)
		}
		switch act.Kind {
		case "criterion.added", "request.dropped", "request.corrected", "work.finished", "request.done":
			line.Text = ellipsize(sessionScrub.ReplaceAllString(act.Data, msg("requests.session")), 200)
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
				line.Text = label(roleKeys, role)
			}
		}
		out = append(out, line)
	}
	return out
}

// requestResult ist die Ergebniszeile nach einer Aktion. Sie erscheint nur,
// wenn der Zustand sie stützt; eine erfundene Adresse zeigt nichts.
func (a *app) requestResult(r *http.Request, d requestdomain.Detail) *requestResult {
	q := r.URL.Query()
	if fail := q.Get("fail"); fail == "open_criteria" && d.Request.State == "open" {
		return &requestResult{Text: msg("requests.fail.open_criteria"), Kind: "failed", Fail: true}
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
				res.Text = msg(key, ellipsize(c.Description, 80))
			}
		}
		if res.Text == "" {
			return nil
		}
	case "completed":
		if d.Request.State != "done" {
			return nil
		}
		res.Text = msg(key)
	case "dropped":
		if d.Request.State != "dropped" {
			return nil
		}
		res.Text = msg(key)
	default:
		res.Text = msg(key)
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
	c, err := a.store.AddCriterion(detail.Request.ID, text, personOf(r))
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
	if err := a.store.SetCriterionState(cid, state, requestdomain.Evidence{Kind: kind, Ref: ref, Person: personOf(r)}); err != nil {
		a.requestFail(w, r, detail.Request.ID, err)
		return
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
	if err := a.store.UpdateRequest(req.ID, patch, personOf(r), reason); err != nil {
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
	if err := a.store.CompleteRequest(detail.Request.ID, requestdomain.Evidence{Kind: kind, Ref: ref, Person: personOf(r)}); err != nil {
		a.requestFail(w, r, detail.Request.ID, err)
		return
	}
	a.requestDone(w, r, detail.Request.ID, "completed", nil)
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
	if err := a.store.DropRequest(detail.Request.ID, reason, personOf(r)); err != nil {
		a.requestFail(w, r, detail.Request.ID, err)
		return
	}
	a.requestDone(w, r, detail.Request.ID, "dropped", nil)
}
