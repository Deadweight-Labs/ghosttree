package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/runbook"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Abläufe (REQ-604). Die Routen hängen am Projekt wie Wissen: Lesen und Schreiben
// einer neuen Fassung darf jedes Mitglied, Freigeben nur Owner und Lead (und bei
// einem Ablauf ohne Projekt sein Autor). Ein Ablauf, den es nicht gibt, einer in
// einem fremden Projekt und einer eines anderen Autors antworten gleich: 404
// not_found.
var runbookRoutes = map[string]routeClass{
	"GET /api/runbooks":                       classProject,
	"POST /api/runbooks":                      classProject,
	"GET /api/runbooks/find":                  classProject,
	"POST /api/runbooks/triggered":            classProject,
	"GET /api/runbooks/{id}":                  classProject,
	"GET /api/runbooks/{id}/history":          classProject,
	"POST /api/runbooks/{id}/approve":         classProject,
	"GET /api/runbooks/{id}/runs":             classProject,
	"POST /api/runbooks/{id}/runs":            classProject,
	"GET /api/runbooks/{id}/proposals":        classProject,
	"POST /api/runbook-proposals/{id}/decide": classProject,
	"GET /api/runbooks/{id}/allowed":          classProject,
	"POST /api/runbooks/{id}/allow":           classProject,
}

func init() {
	for pattern, class := range runbookRoutes {
		accessRoutes[pattern] = class
	}
}

func (a *api) registerRunbookRoutes(mux *http.ServeMux) {
	a.routeFunc(mux, "GET /api/runbooks", a.listRunbooks)
	a.routeFunc(mux, "POST /api/runbooks", a.writeRunbook)
	a.routeFunc(mux, "GET /api/runbooks/find", a.findRunbook)
	a.routeFunc(mux, "POST /api/runbooks/triggered", a.triggeredRunbooks)
	a.routeFunc(mux, "GET /api/runbooks/{id}", a.getRunbook)
	a.routeFunc(mux, "GET /api/runbooks/{id}/history", a.runbookHistory)
	a.routeFunc(mux, "POST /api/runbooks/{id}/approve", a.approveRunbook)
	a.routeFunc(mux, "GET /api/runbooks/{id}/runs", a.runbookRuns)
	a.routeFunc(mux, "POST /api/runbooks/{id}/runs", a.reportRunbookRun)
	a.routeFunc(mux, "GET /api/runbooks/{id}/proposals", a.runbookProposals)
	a.routeFunc(mux, "POST /api/runbook-proposals/{id}/decide", a.decideRunbookProposal)
	a.routeFunc(mux, "GET /api/runbooks/{id}/allowed", a.runbookAllowed)
	a.routeFunc(mux, "POST /api/runbooks/{id}/allow", a.allowRunbook)
}

// writeRunbookNotFound ist die eine Antwort für "gibt es nicht" und "darfst du
// nicht sehen", dieselbe wie bei jeder anderen verborgenen Ressource.
func writeRunbookNotFound(w http.ResponseWriter) {
	writeCoded(w, http.StatusNotFound, "not_found", "not found")
}

func writeRunbookError(w http.ResponseWriter, err error) {
	var conflict *store.ErrRunbookConflict
	switch {
	case errors.Is(err, store.ErrRunbookNotFound):
		writeRunbookNotFound(w)
	case errors.As(err, &conflict):
		writeCoded(w, http.StatusConflict, "runbook_conflict", conflict.Msg)
	default:
		writeStoreError(w, http.StatusBadRequest, err)
	}
}

// runbookGate lädt den Ablauf und prüft die Aktion. Bei Unbekannt und Fremd
// derselbe Weg: Filtered() zählt die Entscheidung, die Antwort ist 404.
func (a *api) runbookGate(w http.ResponseWriter, r *http.Request, id int64, act store.Action) (store.RunbookRef, bool) {
	pa := a.access(r)
	ref, err := a.st.RunbookRefByID(id)
	if err != nil {
		pa.Filtered()
		writeRunbookNotFound(w)
		return ref, false
	}
	if err := pa.CheckRunbook(ref, act); err != nil {
		if denyAccess(w, err) {
			return ref, false
		}
		writeStoreError(w, http.StatusInternalServerError, err)
		return ref, false
	}
	return ref, true
}

// runbookScopeFromQuery liest Projekt und Maschine. Der Branch zählt bei
// Abläufen nicht.
func runbookScopeFromQuery(r *http.Request) scope.Axes {
	ax := axesFromQuery(r)
	return scope.Axes{Project: ax.Project, Machine: ax.Machine}
}

func (a *api) accountOf(r *http.Request) int64 { return authorIDOf(r) }

// viewForReader ergänzt, was nur für diesen Leser gilt: die verlinkten Fallen,
// die er sehen darf.
func (a *api) runbookViewFor(r *http.Request, v runbook.View) runbook.View {
	v.Pitfalls = a.access(r).RunbookTitles(v.Spec.Pitfalls)
	return v
}

func (a *api) listGateRunbooks(w http.ResponseWriter, r *http.Request, project string) bool {
	if project == "" {
		a.access(r).Filtered()
		return true
	}
	return a.listGate(w, r, project, store.ResRunbook)
}

func (a *api) listRunbooks(w http.ResponseWriter, r *http.Request) {
	ax := runbookScopeFromQuery(r)
	if !a.listGateRunbooks(w, r, ax.Project) {
		return
	}
	refs, err := a.st.RunbookRefsIn(ax, a.accountOf(r))
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	pa := a.access(r)
	out := []runbook.Summary{}
	for _, ref := range refs {
		if !pa.CanSeeRunbook(ref) {
			continue
		}
		sum, err := a.st.RunbookSummary(ref)
		if err != nil {
			continue
		}
		out = append(out, sum)
	}
	writeJSON(w, http.StatusOK, out)
}

func versionParam(r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("version")
	if raw == "" {
		return 0, true
	}
	n, err := strconv.Atoi(raw)
	return n, err == nil && n > 0
}

// findRunbook löst einen Namen im Kontext auf: der spezifischste Ablauf, den der
// Aufrufer sehen darf.
func (a *api) findRunbook(w http.ResponseWriter, r *http.Request) {
	ax := runbookScopeFromQuery(r)
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	version, ok := versionParam(r)
	if !ok {
		writeCoded(w, http.StatusBadRequest, "invalid_version", "version must be a positive number")
		return
	}
	if !a.listGateRunbooks(w, r, ax.Project) {
		return
	}
	if runbook.ValidName(name) != nil {
		a.access(r).Filtered()
		writeRunbookNotFound(w)
		return
	}
	refs, err := a.st.RunbookCandidates(name, ax, a.accountOf(r))
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	pa := a.access(r)
	for _, ref := range refs {
		if !pa.CanSeeRunbook(ref) {
			continue
		}
		v, err := a.st.RunbookView(ref.ID, version)
		if err != nil {
			writeRunbookError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, a.runbookViewFor(r, v))
		return
	}
	pa.Filtered()
	writeRunbookNotFound(w)
}

func (a *api) getRunbook(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	version, vok := versionParam(r)
	if !ok || !vok {
		writeErr(w, http.StatusBadRequest, "bad runbook id or version")
		return
	}
	if _, ok := a.runbookGate(w, r, id, store.ActRead); !ok {
		return
	}
	v, err := a.st.RunbookView(id, version)
	if err != nil {
		writeRunbookError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a.runbookViewFor(r, v))
}

func (a *api) runbookHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad runbook id")
		return
	}
	if _, ok := a.runbookGate(w, r, id, store.ActRead); !ok {
		return
	}
	hist, err := a.st.RunbookHistory(id)
	if err != nil {
		writeRunbookError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, hist)
}

// writeRunbook legt einen Ablauf oder eine neue Fassung an. Die Freigabe durch
// den Schreiber gilt nur, wenn er sie verlangt und freigeben darf: ein Agent,
// der den Ablauf anlegt, verlangt sie nicht, und ein Mitglied bekommt sie nicht.
func (a *api) writeRunbook(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Project string          `json:"project"`
		Machine string          `json:"machine"`
		Name    string          `json:"name"`
		Spec    json.RawMessage `json:"spec"`
		Note    string          `json:"note"`
		Approve bool            `json:"approve"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	ax := scope.CanonicalAxes(scope.Axes{Project: body.Project, Machine: body.Machine})
	pa := a.access(r)
	if err := runbook.ValidName(body.Name); err != nil {
		pa.Filtered()
		writeCoded(w, http.StatusBadRequest, "invalid_runbook", err.Error())
		return
	}
	spec, err := runbook.ParseSpec(body.Spec)
	if err != nil {
		pa.Filtered()
		writeCoded(w, http.StatusBadRequest, "invalid_runbook", err.Error())
		return
	}
	// Mit Projekt sagt die Maschine nur, wo der Ablauf gilt; ausführbar wird er
	// erst durch Freigabe und Erlaubnis auf der Maschine, und gesehen wird er über
	// die Projektrolle. Der Maschinenname ist dann kein Anspruch.
	if (ax.Project == "" && !a.gateMachineNote(w, r, ax.Machine)) || !a.gateProject(w, r, ax.Project) {
		return
	}
	own := true
	ref, err := a.st.RunbookRefByScope(ax.Project, ax.Machine, body.Name, a.accountOf(r))
	switch {
	case err == nil:
		if denyAccess(w, pa.CheckRunbook(ref, store.ActEdit)) {
			return
		}
		own = pa.OwnsRunbook(ref)
	case errors.Is(err, store.ErrRunbookNotFound):
		if denyAccess(w, pa.CheckRunbookCreate(ax.Project, ax.Machine)) {
			return
		}
	default:
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	approve := body.Approve && pa.MayApproveRunbook(ax.Project, ax.Machine, own)
	res, err := a.st.WriteRunbook(store.RunbookWrite{
		Project: ax.Project, Machine: ax.Machine, Name: body.Name, Spec: spec, Note: body.Note,
		Person: personOf(r), AuthorID: a.accountOf(r), Approve: approve,
		// Nie aus dem Body: nur der Header trägt einen gemessenen Stand.
		Anchor: anchorFromRequest(r),
		Origin: a.originFor(r, "", originNamedFromGlobs(spec.Triggers.Paths)...),
	})
	if err != nil {
		writeRunbookError(w, err)
		return
	}
	code := http.StatusOK
	if res.NewVersion {
		code = http.StatusCreated
	}
	writeJSON(w, code, map[string]any{
		"id": res.ID, "version": res.Version, "hash": res.Hash,
		"new_runbook": res.NewRunbook, "new_version": res.NewVersion,
		"approved": res.Approved, "approval_requested": body.Approve,
	})
}

func (a *api) approveRunbook(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad runbook id")
		return
	}
	var body struct {
		Version int    `json:"version"`
		Hash    string `json:"hash"`
	}
	if err := readJSON(r, &body); err != nil || body.Version <= 0 {
		a.access(r).Filtered()
		writeCoded(w, http.StatusBadRequest, "invalid_version", "name the version to approve")
		return
	}
	ref, ok := a.runbookGate(w, r, id, store.ActApprove)
	if !ok {
		return
	}
	// Ausdrücklich und unabhängig vom Durchsetzungsmodus: nur Owner und Lead (bei
	// einem Ablauf ohne Projekt der Autor) machen eine Fassung ausführbar.
	if !a.access(r).MayApproveRunbook(ref.Project, ref.Machine, a.access(r).OwnsRunbook(ref)) {
		denyAccess(w, store.ErrAccessForbidden)
		return
	}
	// Freigegeben wird, was der Freigebende gesehen hat: der Hash der Fassung
	// muss stimmen, sonst wurde zwischen Ansehen und Freigabe nichts anderes
	// untergeschoben als das, was er gelesen hat.
	v, err := a.st.RunbookView(id, body.Version)
	if err != nil {
		writeRunbookError(w, err)
		return
	}
	if body.Hash != "" && body.Hash != v.Hash {
		writeCoded(w, http.StatusConflict, "runbook_conflict", "the hash does not match this version; read it again before approving")
		return
	}
	if err := a.st.ApproveRunbookVersion(id, body.Version, a.accountOf(r), personOf(r)); err != nil {
		writeRunbookError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "version": body.Version, "approved": true})
}

func (a *api) runbookRuns(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad runbook id")
		return
	}
	if _, ok := a.runbookGate(w, r, id, store.ActRead); !ok {
		return
	}
	runs, err := a.st.RunbookRuns(id, intParam(r, "limit", 20))
	if err != nil {
		writeRunbookError(w, err)
		return
	}
	if runs == nil {
		runs = []runbook.Run{}
	}
	writeJSON(w, http.StatusOK, runs)
}

// reportRunbookRun nimmt die Rückmeldung zu einem Lauf an. Wer den Ablauf lesen
// darf, darf einen Lauf melden; Abweichungen werden zu einem Vorschlag, nie zu
// einer Änderung.
func (a *api) reportRunbookRun(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad runbook id")
		return
	}
	var body struct {
		Version    int                 `json:"version"`
		Target     string              `json:"target"`
		Mode       string              `json:"mode"`
		Result     string              `json:"result"`
		Params     map[string]string   `json:"params"`
		Note       string              `json:"note"`
		Machine    string              `json:"machine"`
		Deviations []runbook.Deviation `json:"deviations"`
	}
	if err := readJSON(r, &body); err != nil {
		a.access(r).Filtered()
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if _, ok := a.runbookGate(w, r, id, store.ActRead); !ok {
		return
	}
	if body.Version == 0 {
		head, err := a.st.RunbookView(id, 0)
		if err != nil {
			writeRunbookError(w, err)
			return
		}
		body.Version = head.Version
	}
	machine := principalOf(r).Machine
	if machine == "" {
		machine = body.Machine
	}
	res, err := a.st.RecordRunbookRun(store.RunbookRunInput{
		RunbookID: id, Version: body.Version, Target: body.Target, Mode: body.Mode, Result: body.Result,
		Params: body.Params, Note: body.Note, Machine: machine, Person: personOf(r), AuthorID: a.accountOf(r),
		Deviations: body.Deviations, Anchor: anchorFromRequest(r), Origin: a.originFor(r, ""),
	})
	if err != nil {
		writeRunbookError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"run_id": res.RunID, "proposal_id": res.ProposalID})
}

func (a *api) runbookProposals(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad runbook id")
		return
	}
	if _, ok := a.runbookGate(w, r, id, store.ActRead); !ok {
		return
	}
	ps, err := a.st.RunbookProposals(id, r.URL.Query().Get("state"))
	if err != nil {
		writeRunbookError(w, err)
		return
	}
	if ps == nil {
		ps = []runbook.Proposal{}
	}
	writeJSON(w, http.StatusOK, ps)
}

func (a *api) decideRunbookProposal(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad proposal id")
		return
	}
	var body struct {
		Accept bool `json:"accept"`
	}
	if err := readJSON(r, &body); err != nil {
		a.access(r).Filtered()
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	pa := a.access(r)
	prop, err := a.st.RunbookProposalByID(id)
	if err != nil {
		pa.Filtered()
		writeRunbookNotFound(w)
		return
	}
	ref, ok := a.runbookGate(w, r, prop.RunbookID, store.ActApprove)
	if !ok {
		return
	}
	// Annehmen schreibt eine freigegebene Fassung: dieselbe harte Prüfung wie bei
	// der Freigabe, auch im Log-Modus. Ablehnen ändert nichts und braucht sie
	// trotzdem, damit kein Mitglied einen Vorschlag eines anderen wegräumt.
	if !a.access(r).MayApproveRunbook(ref.Project, ref.Machine, a.access(r).OwnsRunbook(ref)) {
		denyAccess(w, store.ErrAccessForbidden)
		return
	}
	out, err := a.st.DecideRunbookProposal(id, body.Accept, a.accountOf(r), personOf(r), anchorFromRequest(r), a.originFor(r, ""))
	if err != nil {
		writeRunbookError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// Grenzen der automatischen Auslieferung.
const (
	maxRunbookTriggers = 2
	// maxRunbookBriefing begrenzt, was ein einzelner Ablauf in die Session
	// bringt; mehr steht hinter `ctx run` und runbook_get.
	maxRunbookBriefing = 1800
	maxTriggerPrompt   = 16 << 10
	maxTriggerCommand  = 8 << 10
	maxTriggerPaths    = 64
)

// triggeredRunbooks beantwortet, welche Abläufe zu dem passen, was gerade
// geschieht: ein Stichwort im Prompt, ein Befehl, ein berührter Pfad. Geliefert
// wird die neueste freigegebene Fassung, höchstens zwei Abläufe je Aufruf und
// jeder Ablauf höchstens einmal je Session. Die Antwort ist ohne Treffer leer.
func (a *api) triggeredRunbooks(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Prompt   string   `json:"prompt"`
		Command  string   `json:"command"`
		Paths    []string `json:"paths"`
		RepoPath string   `json:"repo_path"`
		Session  string   `json:"session"`
		Project  string   `json:"project"`
		Machine  string   `json:"machine"`
	}
	if err := readJSON(r, &body); err != nil {
		a.access(r).Filtered()
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	pa := a.access(r)
	pa.Filtered()
	ax := scope.CanonicalAxes(scope.Axes{Project: body.Project, Machine: body.Machine})
	ax = scope.Axes{Project: ax.Project, Machine: ax.Machine}
	if len(body.Prompt) > maxTriggerPrompt {
		body.Prompt = body.Prompt[:maxTriggerPrompt]
	}
	if len(body.Command) > maxTriggerCommand {
		body.Command = body.Command[:maxTriggerCommand]
	}
	if len(body.Paths) > maxTriggerPaths {
		body.Paths = body.Paths[:maxTriggerPaths]
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	if ax.Project != "" && !pa.CanSeeProject(ax.Project, store.ResRunbook) {
		w.WriteHeader(http.StatusOK)
		return
	}
	cands, err := a.st.RunbookTriggerable(ax, a.accountOf(r))
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	type hit struct {
		d      store.RunbookDeliverable
		reason string
	}
	var hits []hit
	in := runbook.Input{Prompt: body.Prompt, Command: body.Command, RepoPath: body.RepoPath, Paths: body.Paths}
	for _, c := range cands {
		if !pa.CanSeeRunbook(c.Ref) {
			continue
		}
		if reason, ok := c.View.Spec.Match(in); ok {
			hits = append(hits, hit{c, reason})
		}
	}
	// Der spezifischste Ablauf zuerst.
	spec := func(h hit) int {
		n := 0
		if h.d.Ref.Project != "" {
			n++
		}
		if h.d.Ref.Machine != "" {
			n++
		}
		return n
	}
	sort.SliceStable(hits, func(i, j int) bool { return spec(hits[i]) > spec(hits[j]) })
	session := store.CleanSessionKey(body.Session)
	var out []string
	for _, h := range hits {
		if len(out) == maxRunbookTriggers {
			break
		}
		if session != "" {
			fresh, err := a.st.ClaimContextDeliveries(principalOf(r).ID, session, store.DeliveredByRunbook,
				[]store.ContextDelivery{{Term: "runbook:" + strconv.FormatInt(h.d.Ref.ID, 10)}})
			if err != nil {
				writeStoreError(w, http.StatusInternalServerError, err)
				return
			}
			if len(fresh) == 0 {
				continue
			}
		}
		v := a.runbookViewFor(r, h.d.View)
		out = append(out, runbook.Briefing(v, h.reason, maxRunbookBriefing))
	}
	w.WriteHeader(http.StatusOK)
	if len(out) == 0 {
		return
	}
	fmt.Fprint(w, "## Runbooks for what you are doing (ghosttree)\n\n")
	fmt.Fprint(w, strings.Join(out, "\n"))
}

// runbookAllowed beantwortet, ob genau diese Fassung auf dieser Maschine laufen
// darf. Das ist die Quelle für ctx run --run; eine lokale Datei reicht nie.
func (a *api) runbookAllowed(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	version, vok := versionParam(r)
	if !ok || !vok {
		writeErr(w, http.StatusBadRequest, "bad runbook id or version")
		return
	}
	if _, ok := a.runbookGate(w, r, id, store.ActRead); !ok {
		return
	}
	if version == 0 {
		head, err := a.st.RunbookView(id, 0)
		if err != nil {
			writeRunbookError(w, err)
			return
		}
		version = head.Version
	}
	machine := r.URL.Query().Get("machine")
	if machine == "" {
		machine = principalOf(r).Machine
	}
	state, err := a.st.RunbookAllowed(id, version, machine)
	if err != nil {
		writeRunbookError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// allowRunbook gibt es über die API nicht. Eine Freigabe für eine Maschine
// entscheidet ein Mensch: in einer Web-Sitzung (Browser, CSRF) oder der Betreiber
// mit Datenbankzugriff (ctx runbook allow --db). Ein Token, auch ein Gerätetoken,
// liegt in der Konfiguration jeder Maschine, auf der ein Agent läuft; wer damit
// freigeben könnte, bräuchte keinen Menschen. Wer den Ablauf nicht sehen darf,
// bekommt dieselbe Antwort wie bei einer unbekannten Nummer.
func (a *api) allowRunbook(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad runbook id")
		return
	}
	if _, ok := a.runbookGate(w, r, id, store.ActRead); !ok {
		return
	}
	writeCoded(w, http.StatusForbidden, "web_session_required",
		"allowing a runbook on a machine needs a person, not a token: an operator runs `ctx runbook allow --db <database>` on the server (the web interface will offer it as an interactive page)")
}
