package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/feature"
	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	"github.com/Deadweight-Labs/ghosttree/internal/query"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Die Suchsprache am Server (REQ-600). Eine Suchzeile mit Qualifiern läuft hier;
// eine ohne bleibt beim bisherigen Weg von a.search, damit sich für Text
// allein nichts ändert.
//
// Zwei Wege zu den Filtern, ein Ergebnis: Was die Datenbank schon beantwortet
// (Zeitraum und Bereich des Entstehungskontexts, Commit und PR des Ankers) geht
// als SQL in die jeweilige Abfrage. Alles und jeder verneinte Qualifier prüft
// danach query.Match gegen die Eigenschaften des Treffers. Match ist die
// Definition, das SQL nur die Abkürzung. Weil Match erst nach dem Holen
// aussortiert, holen die Abfragen mehr Zeilen, als die Antwort braucht.

// repoRemote macht aus repo:owner/name die Remote des Projekts.
func repoRemote(v string) string {
	v = strings.Trim(strings.TrimSpace(v), "/")
	first, _, _ := strings.Cut(v, "/")
	if !strings.Contains(first, ".") {
		v = "github.com/" + v
	}
	return scope.NormalizeRemote(v)
}

// searchKinds sagt, welche Arten die Anfrage will: der Parameter kind und die
// Qualifier zusammen.
func searchKinds(kindParam string, q query.Query) []string {
	var want []string
	switch strings.ToLower(kindParam) {
	case "", "all":
		want = []string{query.KindKnowledge, query.KindRequest, query.KindPain, query.KindIdea, query.KindFeature, query.KindCriterion, query.KindTask, query.KindFile, query.KindMessage, query.KindSession}
	case "messages", "message":
		want = []string{query.KindMessage}
	case "sessions", "session":
		want = []string{query.KindSession}
	case "files", "file":
		want = []string{query.KindFile}
	case "knowledge":
		want = []string{query.KindKnowledge}
	case "requests", "request", "issues":
		want = []string{query.KindRequest}
	case "pains", "pain":
		want = []string{query.KindPain}
	case "ideas", "idea":
		want = []string{query.KindIdea}
	case "features", "feature":
		want = []string{query.KindFeature}
	case "criteria", "criterion":
		want = []string{query.KindCriterion}
	case "tasks", "task":
		want = []string{query.KindTask}
	}
	out := want[:0:0]
	for _, k := range want {
		if q.WantsKind(k) {
			out = append(out, k)
		}
	}
	return out
}

// mergeOrigin legt den Filter aus den Parametern und den aus der Suchzeile
// zusammen: der engere Zeitraum, die Bereiche beider.
func mergeOrigin(param originctx.Filter, spec originctx.Spec, now time.Time) (originctx.Filter, error) {
	f, err := spec.Resolve(now)
	if err != nil {
		return param, err
	}
	out := param
	if f.Since != "" && f.Since > out.Since {
		out.Since = f.Since
	}
	if f.Until != "" && (out.Until == "" || f.Until < out.Until) {
		out.Until = f.Until
	}
	out.Areas = append(append([]string{}, param.Areas...), f.Areas...)
	if out.Since != "" && out.Until != "" && out.Since > out.Until {
		return out, errSinceAfterUntil
	}
	return out, nil
}

type queryError string

func (e queryError) Error() string { return string(e) }

const errSinceAfterUntil = queryError("since is after until")

// factsOf baut die Eigenschaften eines Treffers für query.Match.
func factsOf(pa *store.ProjectAccess, kind string, number int64, state, typ, repo, author, assignee, created, updated string, of store.ObjectFacts) query.Facts {
	of, assignee = pa.FactsView(repo, of), pa.AssigneeView(repo, assignee)
	return query.Facts{
		Kind: kind, State: state, Type: typ, Repo: repo, Number: number, Author: author, Assignee: assignee,
		Branches: of.Branches, Commits: of.Commits, PRs: of.PRs, Packages: of.Packages, Labels: of.Labels,
		Areas: of.Areas, Parents: of.Parents, Blocks: of.Blocks, Features: of.Features,
		Created: created, Updated: updated, OriginAt: of.OriginAt,
	}
}

// resolveQuery löst, was die Suchzeile nur dem Server überlässt: das Projekt aus
// repo:, Themen zu Bereichen, @me zum Fragenden, und legt die Filter der Zeile
// auf die der Parameter. false heißt, die Antwort ist schon geschrieben.
func (a *api) resolveQuery(w http.ResponseWriter, r *http.Request, q query.Query, ax *scope.Axes, anchor *store.AnchorFilter, origin *originctx.Filter) (query.Query, bool) {
	pa := a.access(r)
	if repo := q.Repo(); repo != "" {
		ax.Project = repoRemote(repo)
	}
	if len(q.Topics()) > 0 {
		if ax.Project == "" {
			writeCoded(w, http.StatusBadRequest, "invalid_query", "search query: topic: needs a project; name one or use repo:")
			return q, false
		}
		// Wer das Projekt nicht lesen darf, erfährt auch nicht, welche Themen es gibt.
		if denyAccess(w, pa.Check(ax.Project, store.ResLabel, store.ActRead, store.Object{})) {
			return q, false
		}
		var err error
		if q, err = q.ResolveTopics(pa.TopicPrefixes(ax.Project)); err != nil {
			writeCoded(w, http.StatusBadRequest, "invalid_query", err.Error())
			return q, false
		}
	}
	q = q.ResolveMe(personOf(r))
	merged, err := mergeOrigin(*origin, q.Origin(), time.Now())
	if err != nil {
		writeCoded(w, http.StatusBadRequest, "invalid_origin_filter", err.Error())
		return q, false
	}
	*origin = merged
	if c := q.Commit(); c != "" {
		anchor.Commit = c
	}
	if n := q.PR(); n != 0 {
		anchor.PR = n
	}
	if err := anchor.Valid(); err != nil {
		writeCoded(w, http.StatusBadRequest, "invalid_commit", err.Error())
		return q, false
	}
	return q, true
}

func (a *api) searchQualified(w http.ResponseWriter, r *http.Request, q query.Query, kindParam string, ax scope.Axes, limit int, anchor store.AnchorFilter, origin originctx.Filter) {
	if res, ok := a.qualifiedResult(w, r, q, kindParam, ax, limit, anchor, origin); ok {
		writeJSON(w, http.StatusOK, res)
	}
}

// qualifiedResult beantwortet eine Suchzeile; false heißt, die Antwort (ein
// Fehler) ist schon geschrieben.
func (a *api) qualifiedResult(w http.ResponseWriter, r *http.Request, q query.Query, kindParam string, ax scope.Axes, limit int, anchor store.AnchorFilter, origin originctx.Filter) (searchResult, bool) {
	pa := a.access(r)
	q, ok := a.resolveQuery(w, r, q, &ax, &anchor, &origin)
	if !ok {
		return searchResult{}, false
	}
	project := ax.Project
	text := q.Text
	// limit kommt aus der Anfrage: begrenzt, bevor er Slices dimensioniert.
	limit = min(max(limit, 1), maxQualifiedLimit)
	fetchBase := min(max(limit*10, 100), 500)
	pa.Filtered()
	res := searchResult{Knowledge: []store.Knowledge{}, Sessions: []store.SessionHit{}, Requests: []requestdomain.SearchHit{}, Query: q.String()}

	for _, kind := range searchKinds(kindParam, q) {
		switch kind {
		case query.KindKnowledge:
			if err := a.qualifiedKnowledge(r, &res, q, text, ax, limit, fetchBase, anchor, origin); err != nil {
				writeStoreError(w, http.StatusInternalServerError, err)
				return searchResult{}, false
			}
		case query.KindRequest:
			if err := a.qualifiedRequests(r, &res, q, text, ax, limit, fetchBase, anchor, origin); err != nil {
				writeStoreError(w, http.StatusInternalServerError, err)
				return searchResult{}, false
			}
		case query.KindSession:
			if err := a.qualifiedSessions(r, &res, q, text, ax, limit, fetchBase); err != nil {
				writeStoreError(w, http.StatusInternalServerError, err)
				return searchResult{}, false
			}
		case query.KindMessage:
			if err := a.qualifiedMessages(r, &res, q, text, project, limit, fetchBase); err != nil {
				writeStoreError(w, http.StatusInternalServerError, err)
				return searchResult{}, false
			}
		}
		if project == "" {
			continue
		}
		var err error
		switch kind {
		case query.KindPain:
			err = a.qualifiedPains(r, &res, q, text, project, limit, fetchBase)
		case query.KindIdea:
			err = a.qualifiedIdeas(r, &res, q, text, project, limit, fetchBase)
		case query.KindFeature:
			err = a.qualifiedFeatures(r, &res, q, text, project, limit, fetchBase)
		case query.KindCriterion:
			err = a.qualifiedCriteria(r, &res, q, text, project, limit, fetchBase)
		case query.KindTask:
			err = a.qualifiedTasks(r, &res, q, text, project, limit)
		case query.KindFile:
			err = a.qualifiedFiles(r, &res, q, text, project, limit, fetchBase, anchor, origin)
		}
		if err != nil {
			if denyAccess(w, err) {
				return searchResult{}, false
			}
			writeStoreError(w, http.StatusInternalServerError, err)
			return searchResult{}, false
		}
	}
	return res, true
}

// idsOf sammelt Ids.
func idsOf[T any](list []T, id func(T) int64) []int64 {
	out := make([]int64, len(list))
	for i, v := range list {
		out[i] = id(v)
	}
	return out
}

func (a *api) qualifiedKnowledge(r *http.Request, res *searchResult, q query.Query, text string, ax scope.Axes, limit, fetchBase int, anchor store.AnchorFilter, origin originctx.Filter) error {
	pa := a.access(r)
	readable := func(k store.Knowledge) bool { return pa.CanSeeKnowledge(k) && pa.CanDeliverKnowledge(k) }
	ks, err := a.st.SearchKnowledgeWith(text, store.KnowledgeSearch{
		Axes: ax, Union: r.URL.Query().Get("scope") == "union",
		Limit: a.overfetch(fetchBase), Viewer: pa.Viewer(), Keep: readable, Prefilter: pa.KnowledgePrefilter(),
		NoRecord: true, Anchor: anchor, Origin: origin,
	})
	if err != nil {
		return err
	}
	ks = filterTo(ks, 0, readable)
	facts, err := a.st.ObjectFacts("knowledge", idsOf(ks, func(k store.Knowledge) int64 { return k.ID }))
	if err != nil {
		return err
	}
	kept := filterTo(ks, limit, func(k store.Knowledge) bool {
		return q.Match(factsOf(pa, query.KindKnowledge, k.ID, k.Status, k.Type, k.Scope.Project, k.Person, "", k.CreatedAt, k.UpdatedAt, facts[k.ID]))
	})
	a.st.RecordKnowledgeSearchHits(kept)
	res.Knowledge = pa.KnowledgeViews(kept)
	return nil
}

func (a *api) qualifiedRequests(r *http.Request, res *searchResult, q query.Query, text string, ax scope.Axes, limit, fetchBase int, anchor store.AnchorFilter, origin originctx.Filter) error {
	pa := a.access(r)
	page, err := a.st.SearchRequestsWith(a.requestFilter(r, requestdomain.SearchFilter{
		Query: text, Scope: scope.Axes{Project: ax.Project}, Limit: fetchBase, Commit: anchor.Commit, PR: anchor.PR, Origin: origin,
	}), store.SearchAuto)
	if err != nil {
		return err
	}
	hits := page.Results
	facts, err := a.st.ObjectFacts("request", idsOf(hits, func(h requestdomain.SearchHit) int64 { return h.Request.ID }))
	if err != nil {
		return err
	}
	readableReq := a.readableRequests(pa, q, facts)
	kept := make([]requestdomain.SearchHit, 0, len(hits))
	for _, h := range hits {
		of := facts[h.Request.ID]
		of.Parents, of.Blocks = readableReq(of.Parents), readableReq(of.Blocks)
		f := factsOf(pa, query.KindRequest, h.Request.ID, h.Request.State, h.Request.Type, h.Request.Scope.Project, h.Request.Person, "", h.Request.CreatedAt, h.Request.UpdatedAt, of)
		if !q.Match(f) {
			continue
		}
		kept = append(kept, pa.RequestHitView(h))
		if len(kept) >= limit {
			break
		}
	}
	res.Requests = kept
	a.noteRequestHits(r, kept)
	return nil
}

// readableRequests liefert eine Funktion, die aus einer Liste von Request-Nummern
// nur die behält, die der Betrachter lesen darf. Parent:7 verriete sonst, dass
// ein verborgener Request 7 existiert. Ohne parent:/blocks: in der Suchzeile
// wird nichts geprüft, denn niemand fragt danach.
func (a *api) readableRequests(pa *store.ProjectAccess, q query.Query, _ map[int64]store.ObjectFacts) func([]int64) []int64 {
	if !q.Has(query.KeyParent) && !q.Has(query.KeyBlocks) {
		return func(in []int64) []int64 { return nil }
	}
	cache := map[int64]bool{}
	return func(in []int64) []int64 {
		var out []int64
		for _, id := range in {
			ok, seen := cache[id]
			if !seen {
				ref, err := a.st.RequestRef(store.RefRequest, id)
				ok = err == nil && requestVisibleTo(pa, ref)
				cache[id] = ok
			}
			if ok {
				out = append(out, id)
			}
		}
		return out
	}
}

func (a *api) qualifiedPains(r *http.Request, res *searchResult, q query.Query, text, project string, limit, fetchBase int) error {
	pa := a.access(r)
	if err := pa.GateList(project, store.ResPain, true); err != nil {
		return nil
	}
	list, err := pa.SearchPains(pain.Filter{Project: project, State: "all", Query: text, Limit: min(fetchBase, 200)})
	if err != nil {
		return err
	}
	facts, err := a.st.ObjectFacts("pain", idsOf(list, func(p pain.Pain) int64 { return p.ID }))
	if err != nil {
		return err
	}
	res.Pains = filterTo(list, limit, func(p pain.Pain) bool {
		return q.Match(factsOf(pa, query.KindPain, p.ID, p.State, "", p.Project, p.Person, "", p.CreatedAt, p.UpdatedAt, facts[p.ID]))
	})
	return nil
}

func (a *api) qualifiedIdeas(r *http.Request, res *searchResult, q query.Query, text, project string, limit, fetchBase int) error {
	pa := a.access(r)
	if err := pa.GateList(project, store.ResIdea, true); err != nil {
		return nil
	}
	list, err := pa.SearchIdeas(idea.Filter{Project: project, State: "all", Query: text, Limit: fetchBase})
	if err != nil {
		return err
	}
	facts, err := a.st.ObjectFacts("idea", idsOf(list, func(i idea.Idea) int64 { return i.ID }))
	if err != nil {
		return err
	}
	res.Ideas = filterTo(list, limit, func(i idea.Idea) bool {
		return q.Match(factsOf(pa, query.KindIdea, i.ID, i.State, "", i.Project, i.Person, "", i.CreatedAt, i.UpdatedAt, facts[i.ID]))
	})
	return nil
}

func (a *api) qualifiedFeatures(r *http.Request, res *searchResult, q query.Query, text, project string, limit, fetchBase int) error {
	pa := a.access(r)
	if err := pa.GateList(project, store.ResFeature, true); err != nil {
		return nil
	}
	// Eingestellte Features bleiben suchbar (is:retired); ohne Zustandsangabe
	// stehen alle in der Liste und die Zeile entscheidet.
	list, err := pa.SearchFeatures(feature.Filter{Project: project, State: "all", Query: text, Limit: fetchBase})
	if err != nil {
		return err
	}
	facts, err := a.st.ObjectFacts("feature", idsOf(list, func(f feature.Feature) int64 { return f.ID }))
	if err != nil {
		return err
	}
	res.Features = filterTo(list, limit, func(f feature.Feature) bool {
		return q.Match(factsOf(pa, query.KindFeature, f.ID, f.State, "", f.Project, f.Person, "", f.CreatedAt, f.UpdatedAt, facts[f.ID]))
	})
	return nil
}

func (a *api) qualifiedCriteria(r *http.Request, res *searchResult, q query.Query, text, project string, limit, fetchBase int) error {
	pa := a.access(r)
	list, err := pa.SearchCriteria(project, "all", text, 2000)
	if err != nil {
		return nil
	}
	facts, err := a.st.ObjectFacts("criterion", idsOf(list, func(c store.CriterionHit) int64 { return c.ID }))
	if err != nil {
		return err
	}
	readableReq := a.readableRequests(pa, q, nil)
	res.Criteria = filterTo(list, limit, func(c store.CriterionHit) bool {
		of := facts[c.ID]
		of.Parents, of.Blocks = readableReq(of.Parents), readableReq(of.Blocks)
		return q.Match(factsOf(pa, query.KindCriterion, c.RequestID, c.State, "", project, "", "", "", c.UpdatedAt, of))
	})
	return nil
}

func (a *api) qualifiedTasks(r *http.Request, res *searchResult, q query.Query, text, project string, limit int) error {
	pa := a.access(r)
	if err := pa.Check(project, store.ResProject, store.ActRead, store.Object{}); err != nil {
		return nil
	}
	list, err := a.st.ListHumanTasks(pa, store.TaskFilter{Project: project, State: "all", Limit: 500})
	if err != nil {
		return err
	}
	words := strings.Fields(strings.ToLower(text))
	list = filterTo(list, 0, func(t humantask.Task) bool {
		hay := strings.ToLower(t.Title + " " + t.Description)
		for _, w := range words {
			if !strings.Contains(hay, strings.Trim(w, `"`)) {
				return false
			}
		}
		return true
	})
	facts, err := a.st.ObjectFacts("task", idsOf(list, func(t humantask.Task) int64 { return t.ID }))
	if err != nil {
		return err
	}
	readableReq := a.readableRequests(pa, q, nil)
	res.Tasks = filterTo(list, limit, func(t humantask.Task) bool {
		of := facts[t.ID]
		of.Parents, of.Blocks = readableReq(of.Parents), readableReq(of.Blocks)
		return q.Match(factsOf(pa, query.KindTask, t.ID, t.State, t.Kind, project, t.CreatedBy, t.Assignee, t.CreatedAt, t.UpdatedAt, of))
	})
	return nil
}

// ghostFacts: eine Datei- oder Verzeichnisbeschreibung als Treffer. Ihr Bereich
// ist ihr Pfad.
func ghostFacts(g store.GhostFile) query.Facts {
	f := query.Facts{Kind: query.KindFile, Repo: g.Project, Author: g.Person, Created: g.DescribedAt, Updated: g.UpdatedAt}
	if g.Path != "" {
		f.Areas = []string{g.Path}
	}
	if a := g.GitAnchor; a != nil {
		f.Commits = []string{a.Commit}
		if a.Branch != "" {
			f.Branches = []string{a.Branch}
		}
		if a.PR != nil {
			f.PRs = []int64{*a.PR}
		}
	}
	if c := g.OriginContext; c != nil {
		f.OriginAt = c.CapturedAt
		for _, ar := range c.Areas {
			f.Areas = append(f.Areas, ar.Path)
		}
	}
	return f
}

func (a *api) qualifiedFiles(r *http.Request, res *searchResult, q query.Query, text, project string, limit, fetchBase int, anchor store.AnchorFilter, origin originctx.Filter) error {
	pa := a.access(r)
	if err := pa.GateList(project, store.ResGhost, true); err != nil {
		return nil
	}
	readable := func(g store.GhostFile) bool { return pa.CanSeeGhost(g) && pa.CanDeliverGhost(g) }
	entries, err := a.st.SearchGhostFilesWith(text, store.GhostSearch{
		Project: project, Limit: a.overfetch(fetchBase), Keep: readable, Prefilter: pa.GhostPrefilter(),
		Anchor: anchor, Origin: origin,
	})
	if err != nil {
		return err
	}
	res.Files = filterTo(entries, limit, func(g store.GhostFile) bool { return readable(g) && q.Match(ghostFacts(g)) })
	return nil
}

// sessionFacts: eine Session als Treffer. Sie hat Projekt, Branch, Besitzer und
// Zeiten, keinen Zustand, kein Label und keinen Git-Anker: is:open, label:,
// commit: und pr: treffen sie nie.
func sessionFacts(h store.SessionHit) query.Facts {
	s := h.Session
	f := query.Facts{Kind: query.KindSession, Repo: s.Scope.Project, Author: s.Owner, Created: s.StartedAt, Updated: s.LastSeenAt}
	if s.Scope.Branch != "" {
		f.Branches = []string{s.Scope.Branch}
	}
	return f
}

// qualifiedSessions sucht in den Transkripten, die der Betrachter lesen darf
// (dieselbe Regel wie die Suche ohne Qualifier: private Sessions bleiben dem
// Besitzer). Ohne Suchwort gibt es keine Session-Treffer: das Transkript wird
// über seinen Text gefunden, nicht aufgelistet.
func (a *api) qualifiedSessions(r *http.Request, res *searchResult, q query.Query, text string, ax scope.Axes, limit, fetchBase int) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	pa := a.access(r)
	hits, err := a.searchSessions(pa, text, ax, r.URL.Query().Get("exclude_session"), fetchBase, a.overfetch(fetchBase), store.SearchAuto)
	if err != nil {
		return err
	}
	res.Sessions = filterTo(hits, limit, func(h store.SessionHit) bool { return q.Match(sessionFacts(h)) })
	return nil
}

// messageFacts: eine Raum-Nachricht als Treffer; Anker und Bereich kommen aus
// dem, was beim Schreiben festgehalten wurde.
func messageFacts(pa *store.ProjectAccess, h store.MessageHit, of store.ObjectFacts) query.Facts {
	of = pa.FactsView(h.Project, of)
	return query.Facts{
		Kind: query.KindMessage, Repo: h.Project, Author: h.Sender,
		Branches: of.Branches, Commits: of.Commits, PRs: of.PRs, Areas: of.Areas,
		Created: h.CreatedAt, Updated: h.CreatedAt, OriginAt: of.OriginAt,
	}
}

// qualifiedMessages sucht in den Raum-Nachrichten, die der Betrachter auch sonst
// lesen darf. Die Menge der lesbaren Räume steht in der Abfrage fest
// (CoordAccess.SearchMessages), nicht erst danach.
func (a *api) qualifiedMessages(r *http.Request, res *searchResult, q query.Query, text, project string, limit, fetchBase int) error {
	hits, err := a.coordAccess(r, r.URL.Query().Get("agent_external_id")).SearchMessages(project, text, fetchBase)
	if err != nil {
		if errors.Is(err, store.ErrCoordForbidden) || errors.Is(err, store.ErrCoordNotFound) {
			return nil
		}
		return err
	}
	facts, err := a.st.ObjectFacts("message", idsOf(hits, func(h store.MessageHit) int64 { return h.ID }))
	if err != nil {
		return err
	}
	res.Messages = filterTo(hits, limit, func(h store.MessageHit) bool { return q.Match(messageFacts(a.access(r), h, facts[h.ID])) })
	return nil
}

// maxQualifiedLimit ist die größte Trefferzahl je Art einer qualifizierten Suche.
const maxQualifiedLimit = 200
