package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func (a *api) createSession(w http.ResponseWriter, r *http.Request) {
	var s store.Session
	if !readLimitedJSON(w, r, &s, maxSessionBody) {
		return
	}
	if s.Harness == "" || s.ExternalID == "" {
		writeErr(w, http.StatusBadRequest, "harness and external_id are required")
		return
	}
	s.Scope = scope.CanonicalAxes(s.Scope)
	if !a.gateMachine(w, r, s.Scope.Machine, true) || !a.gateProject(w, r, s.Scope.Project) {
		return
	}
	// Besitz kommt aus dem Token, nie aus dem Rumpf.
	acct, ok := accountOf(principalOf(r))
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	s.AccountID, s.Owner = acct, ""
	if denyAccess(w, a.access(r).Check(s.Scope.Project, store.ResSessionMeta, store.ActCreate, store.Object{Own: true})) {
		return
	}
	id, err := a.st.UpsertSession(s)
	if errors.Is(err, store.ErrSessionCollision) {
		writeCoded(w, http.StatusConflict, "session_id_collision", "that session id already belongs to another account or machine")
		return
	}
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	a.writeSessionRef(w, r, s.Scope.Project, id)
}

// writeSessionRef antwortet auf das Anlegen einer Session. Mitglieder bekommen
// die Nummer und die Adresse (ältere Collector brauchen die Nummer), Gäste nur
// die Adresse: laufende Nummern, die ein Gast mit eigenen Sessions erzeugt,
// zählten sonst die verborgenen dazwischen (#2447).
func (a *api) writeSessionRef(w http.ResponseWriter, r *http.Request, project string, id int64) {
	sess, err := a.st.SessionByID(id)
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	out := map[string]any{"public_id": sess.PublicID}
	if a.access(r).SeesSessionNumbers(project) {
		out["id"] = id
	}
	writeJSON(w, 200, out)
}

// Grenzen der Session-Uploads. Der Collector schickt höchstens 500 Zeilen und
// 24 MiB serialisiert je Anfrage (collector.uploadBatch, uploadBatchBytes) und
// halbiert bei 413; die Grenze liegt darüber.
// maxChunkBody ist eine Variable, damit der Test sie senken kann.
var maxChunkBody int64 = 64 << 20

// smallBody ist die Größe, bis zu der ein Upload ohne Platz in bigBodies
// auskommt; darüber dürfen höchstens zwei Uploads gleichzeitig lesen.
const smallBody = 4 << 20

var bigBodies = make(chan struct{}, 2)

// bodyReadWindow ist die Zeit, die ein großer Upload nach Erhalt seines Platzes
// zum Lesen des Körpers hat (so lang wie ReadTimeout in cmd/ctx/serve.go).
const bodyReadWindow = 30 * time.Second

const maxSessionBody = 64 << 10

// readLimitedJSON liest einen JSON-Körper bis limit Bytes (413 darüber).
func readLimitedJSON(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := readJSON(r, v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeCoded(w, http.StatusRequestEntityTooLarge, "body_too_large", "request body is too large")
		} else {
			writeStoreError(w, http.StatusBadRequest, err)
		}
		return false
	}
	return true
}

func (a *api) appendChunks(w http.ResponseWriter, r *http.Request) {
	id, ok := a.sessionPathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad session id")
		return
	}
	// Besitz und Recht vor dem Körper: wer nicht schreiben darf, bringt den
	// Server nicht dazu, bis zu maxChunkBody Bytes zu lesen und zu decodieren.
	if !a.mayWriteSession(w, r, id) {
		return
	}
	if denyAccess(w, a.access(r).Check("", store.ResSessionMeta, store.ActCreate, store.Object{Own: true})) {
		return
	}
	// Große Körper belegen viel Speicher: nur wenige gleichzeitig.
	if r.ContentLength < 0 || r.ContentLength > smallBody {
		select {
		case bigBodies <- struct{}{}:
			defer func() { <-bigBodies }()
		case <-r.Context().Done():
			return
		}
		// Das Warten auf den Platz darf nicht von der Lesefrist des Servers
		// abgehen: sie beginnt neu, sobald der Körper gelesen werden darf. Ein
		// Server ohne Fristen oder ein Recorder meldet ErrNotSupported.
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(bodyReadWindow))
	}
	var body struct {
		Chunks []store.Chunk `json:"chunks"`
	}
	if !readLimitedJSON(w, r, &body, maxChunkBody) {
		return
	}
	if err := a.st.AppendChunks(id, body.Chunks); err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) listSessions(w http.ResponseWriter, r *http.Request) {
	limit := intParam(r, "limit", 50)
	pa := a.access(r)
	dbFilter, viewFilter, _ := a.sessionFilters(axesFromQuery(r), limit)
	// Metadaten (wer arbeitet wo) sehen Mitglieder ab member; die eigenen
	// Sessions bleiben dem Besitzer. Titel, Zähler und Adresse stammen aus dem
	// Transkript und gehören nur dem, der es lesen darf; Gäste sehen weder
	// Maschine, Branch, Pfad noch Besitzer. Die Sichtbarkeit entscheidet vor dem
	// Abschneiden auf limit (#2447): die Antwort hängt nicht davon ab, wie viele
	// verborgene Sessions es gibt oder wie aktuell sie sind.
	sessions, err := a.st.ListSessionsVisible(dbFilter, limit, ownerFilter(r), func(sess store.Session) bool {
		return pa.CanSeeSessionMeta(sess) && pa.MatchesAxes(pa.MetaView(sess), viewFilter)
	}, store.SessionPrefilter{})
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]store.Session, 0, len(sessions))
	for _, sess := range sessions {
		out = append(out, pa.MetaView(sess))
	}
	pa.Filtered()
	writeJSON(w, 200, out)
}

func (a *api) readSession(w http.ResponseWriter, r *http.Request) {
	id, ok := a.sessionPathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad session id")
		return
	}
	if !a.checkTranscript(w, r, id) {
		return
	}
	chunks, err := a.st.ReadSession(id, intParam(r, "from", 0), intParam(r, "limit", 200))
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, 200, chunks)
}

// rawSession reconstructs the original transcript as newline-delimited JSON.
// The harnesses expire their own transcripts, so this is what makes ghosttree
// the long-term copy rather than just an index of one.
func (a *api) rawSession(w http.ResponseWriter, r *http.Request) {
	id, ok := a.sessionPathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad session id")
		return
	}
	if !a.checkTranscript(w, r, id) {
		return
	}
	lines, err := a.st.SessionRaw(id)
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.WriteHeader(200)
	for _, line := range lines {
		fmt.Fprintln(w, line)
	}
}

// knowledgeRequest is store.Knowledge plus the auto_scope envelope: when the
// caller sends no scope at all, the server applies the write defaults for the
// context it was given.
type knowledgeRequest struct {
	store.Knowledge
	AutoScope *struct {
		Context scope.Axes `json:"context"`
	} `json:"auto_scope"`
}

func (a *api) createKnowledge(w http.ResponseWriter, r *http.Request) {
	var req knowledgeRequest
	if err := readJSON(r, &req); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	k := req.Knowledge
	k.Scope = scope.CanonicalAxes(k.Scope)
	if req.AutoScope != nil {
		req.AutoScope.Context = scope.CanonicalAxes(req.AutoScope.Context)
	}
	if k.Type == "" || k.Title == "" {
		writeErr(w, http.StatusBadRequest, "type and title are required")
		return
	}
	if k.Scope.IsGlobal() && req.AutoScope != nil {
		k.Scope = scope.DefaultAxes(k.Type, req.AutoScope.Context)
	}
	if !a.gateMachine(w, r, k.Scope.Machine, false) || !a.gateProject(w, r, k.Scope.Project) {
		return
	}
	// Die Zuschreibung kommt aus dem Token und ist danach unveränderlich.
	k.Person, k.ConfirmedBy = personOf(r), ""
	if k.Confidence == "verified" {
		k.ConfirmedBy = k.Person
	}
	if denyAccess(w, a.access(r).CheckKnowledgeCreate(k)) {
		return
	}
	a.access(r).DropWrittenSessionNumber(&k)
	id, err := a.st.InsertKnowledge(k)
	if err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	saved, err := a.st.KnowledgeByID(id)
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, 200, a.access(r).KnowledgeView(saved))
}

func (a *api) listKnowledge(w http.ResponseWriter, r *http.Request) {
	var ks []store.Knowledge
	var err error
	if !a.listGateEntries(w, r, scope.NormalizeRemote(r.URL.Query().Get("project")), store.ResKnowledge) {
		return
	}
	if r.URL.Query().Get("include_archived") == "1" {
		ks, err = a.st.KnowledgeForProject(scope.NormalizeRemote(r.URL.Query().Get("project")))
	} else {
		ks, err = a.st.KnowledgeForContext(axesFromQuery(r))
	}
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	pa := a.access(r)
	writeJSON(w, 200, pa.KnowledgeViews(filterTo(ks, 0, pa.CanSeeKnowledge)))
}

func (a *api) insertMigratedKnowledge(w http.ResponseWriter, r *http.Request) {
	var in store.MigratedEntry
	if err := readJSON(r, &in); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	in.Knowledge.Person = personOf(r)
	in.Knowledge.Scope = scope.CanonicalAxes(in.Knowledge.Scope)
	if denyAccess(w, a.access(r).CheckKnowledgeCreate(in.Knowledge)) {
		return
	}
	saved, err := a.st.InsertMigrated(in)
	if err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (a *api) beginMigration(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Project   string            `json:"project"`
		Artifacts map[string]string `json:"artifacts"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if denyAccess(w, a.access(r).Check(scope.NormalizeRemote(body.Project), store.ResKnowledge, store.ActCreate, store.Object{Own: true})) {
		return
	}
	id, err := a.st.BeginMigration(scope.NormalizeRemote(body.Project), body.Artifacts)
	if err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"id": id})
}

func (a *api) completeMigration(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad migration id")
		return
	}
	if !a.checkMigration(w, r, id) {
		return
	}
	if err := a.st.CompleteMigration(id); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) insertDocumentMigration(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad migration id")
		return
	}
	var body struct {
		Source     string `json:"source"`
		Digest     string `json:"digest"`
		DocumentID int64  `json:"document_id"`
		Revision   int    `json:"revision"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if body.Source == "" || body.Digest == "" || body.DocumentID == 0 || body.Revision < 1 {
		writeErr(w, http.StatusBadRequest, "source, digest, document_id and revision are required")
		return
	}
	if !a.checkMigration(w, r, id) {
		return
	}
	if err := a.st.InsertDocumentMigration(id, body.Source, body.Digest, body.DocumentID, body.Revision); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) completedMigrationArtifacts(w http.ResponseWriter, r *http.Request) {
	if denyAccess(w, a.access(r).Check(scope.NormalizeRemote(r.URL.Query().Get("project")), store.ResKnowledge, store.ActCreate, store.Object{Own: true})) {
		return
	}
	out, err := a.st.CompletedMigrationArtifacts(scope.NormalizeRemote(r.URL.Query().Get("project")))
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) completedDocumentArtifacts(w http.ResponseWriter, r *http.Request) {
	project := scope.NormalizeRemote(r.URL.Query().Get("project"))
	if denyAccess(w, a.access(r).Check(project, store.ResKnowledge, store.ActCreate, store.Object{Own: true})) {
		return
	}
	out, err := a.st.CompletedDocumentArtifacts(project)
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// PendingEntry is a knowledge entry plus what a human needs to judge it.
type PendingEntry struct {
	Knowledge         store.Knowledge          `json:"knowledge"`
	Evidence          []store.Evidence         `json:"evidence"`
	MigrationEvidence *store.MigrationEvidence `json:"migration_evidence,omitempty"`
	Recurrence        int                      `json:"recurrence"`
}

func (a *api) pendingKnowledge(w http.ResponseWriter, r *http.Request) {
	limit := intParam(r, "limit", 50)
	if !a.listGateEntries(w, r, scope.NormalizeRemote(r.URL.Query().Get("project")), store.ResKnowledge) {
		return
	}
	ks, err := a.st.PendingKnowledge(r.URL.Query().Get("project"), a.overfetch(limit))
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	pa := a.access(r)
	ks = filterTo(ks, limit, pa.CanSeeKnowledge)
	out := []PendingEntry{}
	for _, k := range ks {
		ev, err := a.st.EvidenceFor(k.ID)
		if err != nil {
			writeStoreError(w, http.StatusInternalServerError, err)
			return
		}
		n, err := a.st.Recurrence(k.ID)
		if err != nil {
			writeStoreError(w, http.StatusInternalServerError, err)
			return
		}
		proof, proofErr := a.st.MigrationEvidenceForKnowledge(k.ID)
		var migrationProof *store.MigrationEvidence
		if proofErr == nil {
			migrationProof = &proof
		} else if proofErr != sql.ErrNoRows {
			writeStoreError(w, http.StatusInternalServerError, proofErr)
			return
		}
		ev, n = pa.EvidenceView(k.Scope.Project, ev, n)
		out = append(out, PendingEntry{Knowledge: pa.KnowledgeView(k), Evidence: ev, MigrationEvidence: pa.MigrationEvidenceView(k.Scope.Project, migrationProof), Recurrence: n})
	}
	writeJSON(w, 200, out)
}

// getKnowledge answers with one entry and its untouched body. Every other read
// path abbreviates: the bootstrap folds against a budget, a search hit shows a
// snippet. Somewhere the whole thing has to come back the way it went in, or
// storing a long document is a promise the archive does not keep.
func (a *api) getKnowledge(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad knowledge id")
		return
	}
	k, err := a.st.KnowledgeByID(id)
	if err == sql.ErrNoRows {
		a.access(r).Filtered()
		writeErr(w, http.StatusNotFound, "no such knowledge entry")
		return
	}
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	if denyAccess(w, a.access(r).CheckKnowledge(k, store.ActRead)) {
		return
	}
	writeJSON(w, 200, a.access(r).KnowledgeView(k))
}

func (a *api) knowledgeHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad knowledge id")
		return
	}
	if !a.checkKnowledgeRef(w, r, id, store.ActRead) {
		return
	}
	history, err := a.st.KnowledgeHistory(id)
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, history)
}

// setRegressionCover trägt ein, womit ein Eintrag abgesichert ist. Eigener
// Endpunkt statt eines Feldes in patchKnowledge: eine Aussage ÜBER den Text ist
// keine Korrektur des Textes und darf keine neue Fassung in der Historie
// anlegen.
func (a *api) setRegressionCover(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad knowledge id")
		return
	}
	var in struct {
		State string `json:"state"`
		Test  string `json:"test"`
	}
	if err := readJSON(r, &in); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	// Eine Aussage über die Absicherung ist eine Beurteilung wie "verified".
	if !a.checkKnowledgeRef(w, r, id, store.ActVerify) {
		return
	}
	if err := a.st.SetRegressionCover(id, in.State, in.Test); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) regressionGaps(w http.ResponseWriter, r *http.Request) {
	if !a.listGate(w, r, scope.NormalizeRemote(r.URL.Query().Get("project")), store.ResKnowledge) {
		return
	}
	gaps, unreviewed, err := a.st.RegressionGapsVisible(axesFromQuery(r), a.access(r).CanSeeKnowledge)
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	// Die Zahl der Unbeurteilten reist mit: eine kurze Lückenliste ohne sie
	// liest sich als Entwarnung, obwohl niemand hingesehen hat.
	writeJSON(w, http.StatusOK, map[string]any{"gaps": a.access(r).KnowledgeViews(gaps), "unreviewed": unreviewed})
}

func (a *api) patchKnowledge(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad knowledge id")
		return
	}
	var patch map[string]string
	if err := readJSON(r, &patch); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	// Der Token ist die einzige vertrauenswürdige Quelle für den Bestätiger;
	// ein mitgesendeter Name darf keine fremde Freigabe vortäuschen. Die
	// Zuschreibung (person) ist nicht patchbar und bleibt dem ersten Autor.
	delete(patch, "confirmed_by")
	// Eine Beurteilung (confidence) verlangt lead, owner oder can_review: sie ist
	// keine Änderung des Textes, und der Prüfer ändert fremde Einträge nicht,
	// sondern beurteilt sie. Alles andere ist Ändern: Autor, lead, owner.
	_, judges := patch["confidence"]
	if len(patch) > 1 || !judges {
		if !a.checkKnowledgeRef(w, r, id, store.ActEdit) {
			return
		}
	}
	if confidence, ok := patch["confidence"]; ok {
		if !a.checkKnowledgeRef(w, r, id, store.ActVerify) {
			return
		}
		if confidence == "verified" {
			patch["confirmed_by"] = personOf(r)
		} else {
			patch["confirmed_by"] = ""
		}
	}
	if err := a.st.UpdateKnowledgeBy(id, patch, personOf(r)); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type searchResult struct {
	Knowledge []store.Knowledge         `json:"knowledge"`
	Sessions  []store.SessionHit        `json:"sessions"`
	Requests  []requestdomain.SearchHit `json:"requests"`
}

func (a *api) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "all"
	}
	filter := axesFromQuery(r)
	limit := intParam(r, "limit", 20)
	pa := a.access(r)
	fetch := a.overfetch(limit)
	pa.Filtered()
	res := searchResult{Knowledge: []store.Knowledge{}, Sessions: []store.SessionHit{}, Requests: []requestdomain.SearchHit{}}
	if kind == "knowledge" || kind == "all" {
		// scope=union searches what the session would read, not an exact match.
		search := a.st.SearchKnowledge
		if r.URL.Query().Get("scope") == "union" {
			search = a.st.SearchKnowledgeForContext
		}
		ks, err := search(q, filter, fetch)
		if err != nil {
			writeStoreError(w, http.StatusInternalServerError, err)
			return
		}
		// Suche und Bootstrap beliefern Agenten: zusätzlich zur Sichtbarkeit gilt
		// die Auslieferungsregel (Autor ist im Projekt aktuell member).
		res.Knowledge = pa.KnowledgeViews(filterTo(ks, limit, func(k store.Knowledge) bool { return pa.CanSeeKnowledge(k) && pa.CanDeliverKnowledge(k) }))
	}
	if kind == "sessions" || kind == "all" {
		dbFilter, viewFilter, fetchSessions := a.sessionFilters(filter, fetch)
		// Mit Durchsetzung steht die lesbare Menge vor dem Rang fest: ein
		// verborgener Treffer darf weder einen sichtbaren verdrängen noch
		// verraten, wie viele es gibt (#2447). Das Limit gilt danach.
		// Die Lese-Regel steckt als SQL in der Abfrage (TranscriptPrefilter): die
		// Zeilenprüfung läuft nur über lesbare Kandidaten, und wer ohnehin alles
		// in seinem Filter lesen darf, braucht keine. Der Volltext-MATCH selbst
		// läuft über den ganzen Index, die Laufzeit hängt also von den Treffern
		// verborgener Sessions mit ab.
		var readable func(store.Session) bool
		var pre store.SessionPrefilter
		if a.st.AccessEnforced() {
			fetchSessions = limit
			pre = pa.TranscriptPrefilter()
			if !pre.Exact || viewFilter.Machine != "" || viewFilter.Branch != "" {
				readable = func(sess store.Session) bool {
					return pa.CanSeeTranscript(sess) && pa.MatchesAxes(pa.MetaView(sess), viewFilter)
				}
			}
		}
		hits, err := a.st.SearchSessionsVisible(q, dbFilter, r.URL.Query().Get("exclude_session"), fetchSessions, readable, pre)
		if err != nil {
			writeStoreError(w, http.StatusInternalServerError, err)
			return
		}
		// Ein Treffer zeigt einen Ausschnitt des Transkripts: es gilt die Regel
		// für Transkripte, nicht die für Metadaten.
		res.Sessions = res.Sessions[:0]
		for _, h := range hits {
			if !pa.CanSeeTranscript(h.Session) {
				continue
			}
			h.Session = pa.MetaView(h.Session)
			if pa.MatchesAxes(h.Session, viewFilter) {
				res.Sessions = append(res.Sessions, h)
				if len(res.Sessions) >= limit {
					break
				}
			}
		}
	}
	if kind == "requests" || kind == "all" {
		page, err := a.st.SearchRequests(a.requestFilter(r, requestdomain.SearchFilter{Query: q, Scope: scope.Axes{Project: filter.Project}, Limit: limit}))
		if err != nil {
			writeStoreError(w, http.StatusInternalServerError, err)
			return
		}
		for i := range page.Results {
			page.Results[i] = pa.RequestHitView(page.Results[i])
		}
		res.Requests = page.Results
		a.noteRequestHits(r, page.Results)
	}
	writeJSON(w, 200, res)
}

const defaultBudget = 4000

func (a *api) bootstrap(w http.ResponseWriter, r *http.Request) {
	actx, err := activationFromQuery(r)
	if err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	entries, err := a.st.KnowledgeForActivatedContext(axesFromQuery(r), actx)
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	// Was beim Sessionstart ausgeliefert wird, folgt derselben Sichtbarkeit wie
	// jede andere Lesung: globales Wissen für alle, Projektwissen nach Rolle,
	// Maschinenwissen nur für den Besitzer der Maschine.
	pa := a.access(r)
	entries = filterTo(entries, 0, func(k store.Knowledge) bool { return pa.CanSeeKnowledge(k) && pa.CanDeliverKnowledge(k) })
	openRequests := 0
	if pa.CanSeeProject(axesFromQuery(r).Project, store.ResRequest) {
		if openRequests, err = a.st.CountOpenRequests(axesFromQuery(r)); err != nil {
			writeStoreError(w, http.StatusInternalServerError, err)
			return
		}
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(200)
	fmt.Fprint(w, RenderBootstrap(entries, intParam(r, "budget", defaultBudget)))
	if openRequests > 0 {
		plural := "request"
		if openRequests != 1 {
			plural = "requests"
		}
		fmt.Fprintf(w, "\n## Work ledger (ghosttree)\n\n%d open %s in this scope. For substantial feature, architecture, migration, or multi-session work, search the request ledger first; continue a match or create one with explicit acceptance criteria. Trivial local fixes and routine maintenance do not require a request.\n", openRequests, plural)
	}
	// Der Zähler sagt, dass es etwas gibt; erst diese Zeile sagt, dass etwas
	// angefangen und liegengeblieben ist. Ein Fehler kostet nur die Auskunft:
	// der Bootstrap ist bis hierhin schon geschrieben.
	if !pa.Allow(axesFromQuery(r).Project, store.ResSessionMeta, store.ActRead, store.Object{}) {
		return
	}
	if threads, err := a.st.InterruptedWork(axesFromQuery(r),
		time.Now().UTC().Add(-interruptedWindow).Format(time.RFC3339),
		r.URL.Query().Get("session"), maxInterruptedThreads); err == nil {
		fmt.Fprint(w, renderInterrupted(threads, time.Now().UTC()))
	}
}

func (a *api) interrupted(w http.ResponseWriter, r *http.Request) {
	// Wie der Bootstrap: wer das Projekt nicht sieht, bekommt keine Liste.
	if !a.access(r).Allow(axesFromQuery(r).Project, store.ResSessionMeta, store.ActRead, store.Object{}) {
		writeJSON(w, http.StatusOK, []store.InterruptedThread{})
		return
	}
	threads, err := a.st.InterruptedWork(axesFromQuery(r),
		time.Now().UTC().Add(-interruptedWindow).Format(time.RFC3339),
		r.URL.Query().Get("session"), maxInterruptedThreads)
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, threads)
}

// maxRelevantEntries caps what one prompt may pull in. Three is enough to
// answer a sentence and few enough that a wrong guess stays cheap.
const maxRelevantEntries = 3

// relevant answers with knowledge the text gives a reason to deliver, or with
// nothing at all — which is the usual case, and is an empty body rather than an
// error.
func (a *api) relevant(w http.ResponseWriter, r *http.Request) {
	limit := intParam(r, "limit", maxRelevantEntries)
	if limit > maxRelevantEntries {
		limit = maxRelevantEntries
	}
	pa := a.access(r)
	pa.Filtered()
	entries, err := a.st.RelevantKnowledge(r.URL.Query().Get("q"), axesFromQuery(r), a.overfetch(limit))
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	entries = filterTo(entries, limit, func(k store.Knowledge) bool { return pa.CanSeeKnowledge(k) && pa.CanDeliverKnowledge(k) })
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(200)
	if len(entries) == 0 {
		return
	}
	// A different heading from the bootstrap on purpose. This arrived because of
	// what was just said, and a reader who cannot tell the two apart cannot judge
	// why it is in front of them.
	fmt.Fprint(w, "## Possibly relevant to what you just said (ghosttree)\n\n")
	for _, k := range entries {
		fmt.Fprintf(w, "- [%s|%s] %s — %s\n", k.Type, scopeLabel(k.Scope), k.Title, truncate(oneLine(k.Body), 400))
	}
}

// renderBootstrap builds the auto-injected context package. Binding
// instructions are always complete and first; other confirmed knowledge comes
// before unconfirmed knowledge so a tight budget cuts uncertain material first.
func RenderBootstrap(entries []store.Knowledge, budget int) string {
	return renderBootstrap(entries, budget, false)
}

// RenderBootstrapPreview includes staged entries, clearly separated from the
// binding context. It is for operator inspection, never automatic injection.
func RenderBootstrapPreview(entries []store.Knowledge, budget int) string {
	return renderBootstrap(entries, budget, true)
}

func renderBootstrap(entries []store.Knowledge, budget int, includeStaged bool) string {
	if budget <= 0 {
		budget = defaultBudget
	}
	if len(entries) == 0 {
		return ""
	}
	var instructions, confirmed, staged []store.Knowledge
	held := map[string]int{}
	for _, k := range entries {
		if k.Confidence == "staged" {
			if includeStaged {
				staged = append(staged, k)
			}
		} else if k.Type == "instruction" {
			instructions = append(instructions, k)
		} else if pushedTypes[k.Type] {
			confirmed = append(confirmed, k)
		} else {
			held[k.Type]++
		}
	}
	var b strings.Builder
	b.WriteString("## Known context (ghosttree)\n")
	if len(instructions) > 0 {
		b.WriteString("\n### Instructions (binding)\n")
		for _, k := range instructions {
			label := scopeLabel(k.Scope)
			if gate := activationLabel(k.Activation); gate != "" {
				label += " | " + gate
			}
			fmt.Fprintf(&b, "- [%s] %s — %s\n", label, k.Title, oneLine(k.Body))
		}
	}
	// Instructions do not compete for the context budget. Preserve the same
	// allowance for all remaining groups regardless of instruction length.
	contentLimit := budget + b.Len()
	broad, projectScoped := splitByScopeBreadth(confirmed)
	// Global and machine knowledge is bounded by construction: adding a
	// repository does not add machine facts. Project knowledge is unbounded and
	// will always win a straight contest, which is how a distiller release
	// displaced the two machine notes that were the only entries with a
	// measured effect. Broad knowledge therefore goes first, and its ceiling
	// applies only while there is project knowledge to protect. Whatever the
	// reserve does not use stays available to the project.
	broadLimit := contentLimit
	if len(projectScoped) > 0 {
		broadLimit = min(b.Len()+budget/broadScopeReserveDivisor, contentLimit)
	}
	// The two passes exist for the reserve, not for the reader: one shared set
	// of headings keeps them from looking like two separate sections.
	headings := map[string]bool{}
	truncated := writeGroups(&b, broad, broadLimit, "", headings)
	truncated = writeGroups(&b, projectScoped, contentLimit, "", headings) || truncated
	if len(staged) > 0 && !truncated {
		truncated = writePreviewGroup(&b, staged, contentLimit)
	}
	if truncated {
		b.WriteString("…(truncated, use context_search for more)\n")
	}
	writeHeldIndex(&b, held)
	return b.String()
}

// writeHeldIndex names what was deliberately not sent. Withholding silently
// would not defer the knowledge, it would hide it: an agent cannot search for a
// kind of material it has no reason to think exists. A line of counts costs
// almost nothing and turns the omission into an invitation.
func writeHeldIndex(b *strings.Builder, held map[string]int) {
	if len(held) == 0 {
		return
	}
	var parts []string
	for _, t := range []string{"decision", "note", "plan"} {
		n := held[t]
		if n == 0 {
			continue
		}
		label := t
		if n != 1 {
			label += "s"
		}
		parts = append(parts, fmt.Sprintf("%d %s", n, label))
	}
	if len(parts) == 0 {
		return
	}
	fmt.Fprintf(b, "\nAlso in scope, not shown: %s. These answer questions you will "+
		"know you have — use context_search for them.\n", strings.Join(parts, ", "))
}

func writePreviewGroup(b *strings.Builder, entries []store.Knowledge, limit int) bool {
	b.WriteString("\n## Unconfirmed preview (not binding; approve before agent delivery)\n")
	for _, k := range entries {
		line := fmt.Sprintf("- [preview only | %s] %s — %s\n", scopeLabel(k.Scope), k.Title, oneLine(k.Body))
		if b.Len()+len(line) > limit {
			return true
		}
		b.WriteString(line)
	}
	return false
}

func activationFromQuery(r *http.Request) (activation.Context, error) {
	return activation.NormalizeContext(activation.Context{
		RepoPath: r.URL.Query().Get("repo_path"),
		Paths:    r.URL.Query()["path"],
	})
}

func activationLabel(r activation.Rule) string {
	var parts []string
	if len(r.Paths) > 0 {
		parts = append(parts, "paths:"+strings.Join(r.Paths, ","))
	}
	return strings.Join(parts, " | ")
}

// pushedTypes decides what the bootstrap carries into every session, as opposed
// to what waits to be asked for.
//
// The test is not importance, it is whether the reader could know to look. A
// pitfall fires before a mistake nobody has made yet, so it cannot be searched
// for: not knowing about it is precisely the condition it addresses.
// Instructions bind whether or not anyone reads them, so they have to arrive
// with the session too.
//
// Everything else is reference. A decision explains why something is the way it
// is, which you want when you are about to change that thing; a note records how
// things stand; a plan records where work got to. In each case the moment of
// need is recognisable from inside the work, and search reaches them.
//
// Measured on 2026-08-24, which is what settled it: the two machine-scoped
// entries in the archive had each been delivered 62 times that day and matched
// a search zero times. One of them, an inventory of the local Ollama models,
// went into every session of every project — a fact about one workstation that
// changes nothing until somebody picks a local model.
var pushedTypes = map[string]bool{"pitfall": true}

// broadScopeReserveDivisor bounds what global and machine knowledge may take of
// the content budget. A quarter is enough for the handful of facts that hold
// everywhere and small enough that a project keeps most of its own allowance.
const broadScopeReserveDivisor = 4

// splitByScopeBreadth separates knowledge that holds regardless of repository
// from knowledge about one repository.
func splitByScopeBreadth(entries []store.Knowledge) (broad, projectScoped []store.Knowledge) {
	for _, k := range entries {
		if k.Scope.Project == "" {
			broad = append(broad, k)
		} else {
			projectScoped = append(projectScoped, k)
		}
	}
	return broad, projectScoped
}

// writeGroups appends entries grouped by type and reports whether the budget
// ran out. header is written lazily, so an empty group prints nothing.
// headings is shared across calls so a type printed by one pass is not printed
// again by the next.
func writeGroups(b *strings.Builder, entries []store.Knowledge, budget int, header string, headings map[string]bool) bool {
	if len(entries) == 0 {
		return false
	}
	byType := map[string][]store.Knowledge{}
	for _, k := range entries {
		byType[k.Type] = append(byType[k.Type], k)
	}
	wroteHeader := header == ""
	// Pitfalls first: one stops a mistake that is about to be made, while a
	// decision explains one already made. Under a budget the explanation is
	// what can wait for a search.
	for _, t := range []string{"pitfall", "decision", "note", "plan"} {
		group := byType[t]
		if len(group) == 0 {
			continue
		}
		for _, k := range group {
			label := scopeLabel(k.Scope)
			if provenance := store.KnowledgeProvenance(k); provenance != "" {
				label += " | " + provenance
			}
			line := fmt.Sprintf("- [%s] %s — %s\n", label, k.Title, truncate(oneLine(k.Body), 200))
			if !headings[t] {
				line = "\n### " + t + "\n" + line
			}
			if !wroteHeader {
				line = header + line
			}
			if b.Len()+len(line) > budget {
				return true
			}
			b.WriteString(line)
			wroteHeader, headings[t] = true, true
		}
	}
	return false
}

func scopeLabel(ax scope.Axes) string { return ax.Label() }

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func (a *api) putGhost(w http.ResponseWriter, r *http.Request) {
	var g store.GhostFile
	if err := readJSON(r, &g); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if g.Project == "" {
		writeErr(w, http.StatusBadRequest, "project is required")
		return
	}
	g.Project = scope.NormalizeRemote(g.Project)
	if !a.gateProject(w, r, g.Project) {
		return
	}
	if denyAccess(w, a.access(r).Check(g.Project, store.ResGhost, store.ActCreate, store.Object{})) {
		return
	}
	g.Person = personOf(r)
	id, err := a.st.PutGhostFile(g)
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, 200, map[string]int64{"id": id})
}

// ghostsForPath ist der Auslieferungspfad. Er hat einen Nebeneffekt — er merkt
// sich, was gesagt wurde — und ist deshalb bewusst nicht als reines GET zu
// lesen. Ein zweiter Umlauf zum Quittieren wäre sauberer und passt nicht in das
// 900-ms-Budget des Hooks.
func (a *api) ghostsForPath(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	// Auslieferungspfad des Hooks: ohne Leserecht kommt eine leere Liste und es
	// wird nichts als gesagt vermerkt.
	if !a.access(r).Allow(scope.NormalizeRemote(q.Get("project")), store.ResGhost, store.ActRead, store.Object{}) {
		writeJSON(w, 200, []store.GhostFile{})
		return
	}
	entries, err := a.st.GhostFilesForDelivery(q.Get("project"), q.Get("path"), q.Get("session"))
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	pa := a.access(r)
	writeJSON(w, 200, filterTo(entries, 0, func(g store.GhostFile) bool { return pa.CanSeeGhost(g) && pa.CanDeliverGhost(g) }))
}

// ghostsMove hängt eine Beschreibung samt Historie auf einen neuen Pfad. Die
// Entscheidung, DASS es ein Umzug ist, fällt auf der Client-Seite: nur dort
// liegt die Dateiliste, an der Verschiebung und Kopie zu unterscheiden sind.
func (a *api) ghostsMove(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Project string `json:"project"`
		From    string `json:"from"`
		To      string `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if denyAccess(w, a.access(r).Check(scope.NormalizeRemote(in.Project), store.ResGhost, store.ActEdit, store.Object{})) {
		return
	}
	if err := a.st.MoveGhostFile(in.Project, in.From, in.To); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, 200, map[string]string{"from": in.From, "to": in.To})
}

func (a *api) ghostHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !a.listGate(w, r, scope.NormalizeRemote(q.Get("project")), store.ResGhost) {
		return
	}
	// Der Hook will nur die Zahl. Den ganzen Text zu übertragen, um ihn dann
	// zu zählen, wäre auf einem Pfad mit 900-ms-Budget die falsche Rechnung.
	if q.Get("count") != "" {
		n, err := a.st.GhostHistoryCount(q.Get("project"), q.Get("path"))
		if err != nil {
			writeStoreError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, 200, map[string]int{"count": n})
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	read := a.st.GhostFileHistory
	// Die Kette nimmt die aktuelle Fassung als Kopf dazu. Ohne sie hat die
	// neueste abgeloeste Fassung keinen Nachfolger, und genau der Vergleich
	// mit ihm ist die Frage, die jemand an eine Historie stellt.
	if q.Get("chain") != "" {
		read = a.st.GhostFileChain
	}
	versions, err := read(q.Get("project"), q.Get("path"), limit)
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, 200, versions)
}

func (a *api) ghostTree(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !a.listGateEntries(w, r, scope.NormalizeRemote(q.Get("project")), store.ResGhost) {
		return
	}
	entries, err := a.st.GhostFilesUnder(q.Get("project"), q.Get("prefix"))
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, 200, filterTo(entries, 0, a.access(r).CanSeeGhost))
}

func (a *api) searchGhosts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	pa := a.access(r)
	if q.Get("project") != "" && !a.listGateEntries(w, r, scope.NormalizeRemote(q.Get("project")), store.ResGhost) {
		return
	}
	pa.Filtered()
	limit := intParam(r, "limit", 20)
	entries, err := a.st.SearchGhostFiles(q.Get("q"), q.Get("project"), a.overfetch(limit))
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, 200, filterTo(entries, limit, func(g store.GhostFile) bool { return pa.CanSeeGhost(g) && pa.CanDeliverGhost(g) }))
}

// sessionFilters trennt den Filter für die Datenbank von dem, der auf das
// angewendet wird, was der Betrachter von einer Session sieht. Maschine und
// Branch gelten nie auf dem Rohwert: ein Gast sähe sonst an der Trefferzahl, wo
// eine Session läuft. Ohne Durchsetzung bleibt es beim Datenbankfilter.
func (a *api) sessionFilters(f scope.Axes, fetch int) (db, view scope.Axes, limit int) {
	if !a.st.AccessEnforced() || (f.Machine == "" && f.Branch == "") {
		return f, scope.Axes{}, fetch
	}
	view = scope.Axes{Machine: f.Machine, Branch: f.Branch}
	f.Machine, f.Branch = "", ""
	return f, view, max(fetch, 1000)
}
