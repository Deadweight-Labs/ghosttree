package store

import (
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// Sichtbarkeit nach Rolle (Spec Kapitel 8). ProjectAccess ist die eine Stelle,
// die für (Konto, Projekt, Ressource, Aktion) entscheidet. Rollen kommen
// ausschließlich aus projectRoleTx (Store.ProjectRole); hier wird nichts
// nachgerechnet, nur die Matrix ausgewertet.

// Resource ist die Art des Objekts, auf das zugegriffen wird.
type Resource string

const (
	ResKnowledge   Resource = "knowledge"
	ResRequest     Resource = "request"
	ResSessionMeta Resource = "session"
	ResTranscript  Resource = "transcript"
	// ResRoom ist der Projektraum der Koordination. DMs und Gruppen sind keine
	// Projektressource: dort entscheidet allein die Mitgliedschaft im Raum.
	ResRoom     Resource = "room"
	ResAgents   Resource = "agents"
	ResMembers  Resource = "members"
	ResDocument Resource = "document"
	ResGhost    Resource = "ghost"
	// ResProject ist der Projekteintrag selbst (Name in Listen): sichtbar mit
	// irgendeiner Rolle, nicht schon durch die bloße Mitgliedschaft in der Org.
	ResProject Resource = "project"
	// ResSnapshot ist ein Kontext-Snapshot: eine Kopie des gesamten
	// Projektwissens (Wissen samt Entwürfen, Aufträge, Ghost-Dateien,
	// Dokumente). Anlegen und Lesen ab member, nie für Gäste oder Fremde; die
	// Ablehnung ist immer "nicht gefunden".
	ResSnapshot Resource = "snapshot"
)

// Action ist, was getan werden soll.
type Action string

const (
	ActRead   Action = "read"
	ActCreate Action = "create"
	// ActEdit: ändern, abschließen, verwerfen, korrigieren.
	ActEdit Action = "edit"
	// ActVerify: confidence=verified setzen.
	ActVerify Action = "verify"
	// ActWork: an einem Auftrag arbeiten (jedes Mitglied, auch an fremden).
	ActWork  Action = "work"
	ActShare Action = "share"
)

var (
	// ErrAccessNotFound: für diesen Aufrufer gibt es das Objekt nicht (404).
	ErrAccessNotFound = errors.New("not found")
	// ErrAccessForbidden: der Aufrufer sieht das Objekt, darf die Aktion aber nicht (403).
	ErrAccessForbidden = errors.New("forbidden for your role")
)

// Object beschreibt das einzelne Objekt, soweit die Matrix es braucht.
type Object struct {
	// Own: das Objekt gehört dem Aufrufer (Autor, Besitzer der Session).
	Own bool
	// Shared: der Besitzer hat die Session ausdrücklich freigegeben (Stufe
	// project oder guests).
	Shared bool
	// Guests: die Freigabe schließt Gäste ein (Stufe guests).
	Guests bool
	// Confidence eines Wissenseintrags (für den Gast).
	Confidence string
	// Machine: Maschinen-Achse eines Wissenseintrags; nicht leer heißt, dass
	// nur der Besitzer der Maschine Zugriff hat.
	Machine string
}

// Decision ist das Ergebnis der Matrix.
type Decision struct {
	Allowed bool
	// Hidden: bei Ablehnung 404 statt 403 (Lesen und Nicht-Mitglieder).
	Hidden bool
	Reason string
}

// matrixAllows ist die Matrix aus Spec 8.1 je Rolle, Ressource und Aktion für
// ein Projekt. Eine reine Funktion, damit sie tabellarisch getestet wird.
func matrixAllows(role RoleInfo, res Resource, act Action, obj Object) bool {
	rank := RoleRank(role.Role)
	switch res {
	case ResKnowledge:
		switch act {
		case ActRead:
			if rank >= 2 {
				return true
			}
			return rank == 1 && (obj.Confidence == "trusted" || obj.Confidence == "verified")
		case ActCreate:
			return rank >= 2
		case ActEdit:
			return rank >= 3 || (rank == 2 && obj.Own)
		case ActVerify:
			return rank >= 3 || (rank >= 2 && role.CanReview)
		}
	case ResRequest:
		switch act {
		case ActRead:
			return rank >= 1
		case ActCreate, ActWork:
			return rank >= 2
		case ActEdit:
			return rank >= 3 || (rank == 2 && obj.Own)
		}
	case ResSessionMeta:
		// Die eigene Session anlegen und fortschreiben darf jedes Konto; wem sie
		// gehört, regelt der Upload (gateMachine, mayWriteSession).
		return act == ActCreate || (act == ActRead && (rank >= 2 || (rank == 1 && obj.Guests)))
	case ResTranscript:
		switch act {
		case ActRead:
			return rank >= 3 || obj.Own || (rank >= 2 && obj.Shared) || (rank >= 1 && obj.Guests)
		case ActShare:
			// Der Besitzer der Session und der Owner des Projekts.
			return obj.Own || rank >= 4
		}
	case ResSnapshot:
		return (act == ActRead || act == ActCreate) && rank >= 2
	case ResRoom:
		return (act == ActRead || act == ActCreate) && rank >= 1
	case ResProject:
		return act == ActRead && rank >= 1
	case ResAgents:
		return act == ActRead && rank >= 2
	case ResMembers:
		// Der Gast sieht nur den eigenen Eintrag; das filtert der Aufrufer.
		return act == ActRead && rank >= 1
	case ResDocument:
		switch act {
		case ActRead:
			return rank >= 1
		case ActCreate:
			return rank >= 2
		case ActEdit:
			return rank >= 3 || (rank == 2 && obj.Own)
		}
	case ResGhost:
		switch act {
		case ActRead:
			return rank >= 1
		case ActCreate, ActEdit:
			return rank >= 2
		}
	}
	return false
}

// MatrixAllows macht die Matrix für Tests und Dokumentation sichtbar.
func MatrixAllows(role RoleInfo, res Resource, act Action, obj Object) bool {
	return matrixAllows(role, res, act, obj)
}

// ---------------------------------------------------------------- Modus

// AccessMode schaltet die Durchsetzung. Ohne Enforce wird nur protokolliert,
// was verweigert würde ("would deny"); der Betrieb bleibt unverändert.
type AccessMode struct {
	Enforce bool
	Logger  *slog.Logger
	// LogEvery begrenzt die Zeilen je (Konto, Projekt, Ressource, Aktion);
	// 0 heißt eine Minute.
	LogEvery time.Duration
}

type accessConfig struct {
	enforce atomic.Bool
	logger  atomic.Pointer[slog.Logger]
	every   atomic.Int64

	mu         sync.Mutex
	seen       map[string]accessSeen
	wouldDeny  atomic.Int64
	denied     atomic.Int64
	checkCount atomic.Int64
}

type accessSeen struct {
	at         time.Time
	suppressed int
}

func newAccessConfig() *accessConfig {
	return &accessConfig{seen: map[string]accessSeen{}}
}

// SetAccessMode stellt Durchsetzung und Protokoll ein; für den Store und seine
// Reader gemeinsam.
func (s *Store) SetAccessMode(m AccessMode) {
	c := s.accessCfg()
	c.enforce.Store(m.Enforce)
	if m.Logger != nil {
		c.logger.Store(m.Logger)
	}
	c.every.Store(int64(m.LogEvery))
}

// AccessEnforced sagt, ob verweigert wird.
func (s *Store) AccessEnforced() bool { return s.accessCfg().enforce.Load() }

// AccessCounters liefert (would deny, denied) seit dem Start, für Tests und
// Metriken.
func (s *Store) AccessCounters() (wouldDeny, denied int64) {
	c := s.accessCfg()
	return c.wouldDeny.Load(), c.denied.Load()
}

func (s *Store) accessCfg() *accessConfig {
	s.accessOnce.Do(func() {
		if s.acfg == nil {
			s.acfg = newAccessConfig()
		}
	})
	return s.acfg
}

// apply wendet den Modus auf eine Entscheidung an und liefert, ob der Zugriff
// durchgeht. Im Log-Modus geht alles durch; eine Ablehnung wird dann
// rate-limitiert protokolliert, ohne Inhalte.
func (c *accessConfig) apply(account, project string, res Resource, act Action, d Decision) bool {
	c.checkCount.Add(1)
	if d.Allowed {
		return true
	}
	if c.enforce.Load() {
		c.denied.Add(1)
		return false
	}
	c.wouldDeny.Add(1)
	c.logWouldDeny(account, project, res, act, d)
	return true
}

func (c *accessConfig) logWouldDeny(account, project string, res Resource, act Action, d Decision) {
	every := time.Duration(c.every.Load())
	if every <= 0 {
		every = time.Minute
	}
	key := account + "|" + project + "|" + string(res) + "|" + string(act)
	now := time.Now()
	c.mu.Lock()
	seen, ok := c.seen[key]
	if ok && now.Sub(seen.at) < every {
		seen.suppressed++
		c.seen[key] = seen
		c.mu.Unlock()
		return
	}
	if len(c.seen) >= 4096 {
		c.seen = map[string]accessSeen{}
	}
	c.seen[key] = accessSeen{at: now}
	c.mu.Unlock()
	logger := c.logger.Load()
	if logger == nil {
		logger = slog.Default()
	}
	logger.Warn("access: would deny", "account", account, "project", project,
		"resource", string(res), "action", string(act), "reason", d.Reason, "suppressed", seen.suppressed)
}

// ---------------------------------------------------------------- ProjectAccess

// ProjectAccess entscheidet für ein Konto. Es gilt für genau eine Anfrage: die
// Rollen des Kontos werden beim ersten Gebrauch mit einer einzigen Abfrage
// geladen und danach aus der Map gelesen, die Maschinenbesitzer ebenso. Damit
// kostet jede weitere Prüfung auf dem heißen Pfad (Suche, Bootstrap, Chunks)
// einen Map-Zugriff statt drei Abfragen.
type ProjectAccess struct {
	st        *Store
	principal Principal
	account   int64
	valid     bool

	rolesOnce sync.Once
	roles     map[string]RoleInfo
	admin     bool

	machOnce sync.Once
	machines map[string]int64
	owner    int64

	unclaimed map[string]bool
	writers   map[string]map[string]bool

	checks atomic.Int32
	loads  atomic.Int32
}

func (a *ProjectAccess) rolesLoadCount() int { return int(a.loads.Load()) }

// Checks zählt die Entscheidungen dieser Anfrage; der Routen-Wächter prüft damit,
// dass ein Handler überhaupt gefragt hat.
func (a *ProjectAccess) Checks() int { return int(a.checks.Load()) }

// Access liefert die Zugriffsprüfung für einen authentifizierten Principal.
func (s *Store) Access(p Principal) *ProjectAccess {
	id, ok := accountNumericID(p.ID)
	return &ProjectAccess{st: s, principal: p, account: id, valid: ok}
}

// Principal ist der Aufrufer, für den entschieden wird.
func (a *ProjectAccess) Principal() Principal { return a.principal }

func (a *ProjectAccess) loadRoles() {
	a.rolesOnce.Do(func() {
		a.loads.Add(1)
		a.roles, a.admin = a.st.AccountRoles(a.principal.ID)
	})
}

// Role ist die Rolle des Kontos im Projekt, dieselbe wie Store.ProjectRole.
func (a *ProjectAccess) Role(project string) RoleInfo {
	if !a.valid {
		return RoleInfo{}
	}
	a.loadRoles()
	return a.roles[strings.TrimSpace(project)]
}

// Projects nennt die Projekte, in denen das Konto irgendeine Rolle hat
// (AccessibleProjects der Spec).
func (a *ProjectAccess) Projects() []string {
	a.loadRoles()
	out := make([]string, 0, len(a.roles))
	for remote, r := range a.roles {
		if RoleRank(r.Role) >= 1 {
			out = append(out, remote)
		}
	}
	return out
}

// VisibleProjects behält aus einer Projektliste, was das Konto sehen darf: der
// Org-Owner alles in seiner Org (implizite Owner-Rolle), ein Mitglied nur
// Projekte mit eigener Rolle, der Instanz-Admin wie bisher alles, was die Liste
// ihm gibt. Im Log-Modus bleibt die Liste, und "would deny" wird protokolliert.
func (a *ProjectAccess) VisibleProjects(in []Project) []Project {
	a.Filtered()
	out := make([]Project, 0, len(in))
	for _, p := range in {
		if a.IsAdmin() || a.Allow(p.Remote, ResProject, ActRead, Object{}) {
			out = append(out, p)
		}
	}
	return out
}

// Filtered vermerkt, dass ein Handler sein Ergebnis über Allow/CanSee* filtert;
// auch eine leere Liste hat dann geprüft.
func (a *ProjectAccess) Filtered() { a.checks.Add(1) }

// Unclaimed: zu der Remote gibt es keine Projektzeile (einmal je Anfrage geprüft).
func (a *ProjectAccess) Unclaimed(project string) bool {
	if a.unclaimed == nil {
		a.unclaimed = map[string]bool{}
	}
	if v, ok := a.unclaimed[project]; ok {
		return v
	}
	_, claimed := a.st.ProjectByRemote(project)
	a.unclaimed[project] = !claimed
	return !claimed
}

// IsAdmin: Instanz-Admin (persons.is_admin).
func (a *ProjectAccess) IsAdmin() bool {
	a.loadRoles()
	return a.admin
}

func (a *ProjectAccess) loadMachines() {
	a.machOnce.Do(func() {
		a.machines, a.owner = a.st.machineOwners()
	})
}

// OwnsMachine: die Maschine gehört dem Konto. Altbestand und unbekannte
// Maschinen gehören dem Instanz-Owner.
func (a *ProjectAccess) OwnsMachine(machine string) bool {
	if !a.valid {
		return false
	}
	a.loadMachines()
	if id, ok := a.machines[canonicalMachine(machine)]; ok {
		return id == a.account
	}
	return a.owner == a.account
}

// OwnsSession: die Session gehört dem Konto (Altbestand dem Instanz-Owner).
func (a *ProjectAccess) OwnsSession(sess Session) bool {
	if !a.valid {
		return false
	}
	a.loadMachines()
	return effectiveOwner(sess.AccountID, a.owner) == a.account
}

// IsAuthor: das Label des Kontos steht als Autor am Objekt.
func (a *ProjectAccess) IsAuthor(person string) bool {
	return person != "" && person == a.principal.Label
}

// Decide wertet die Matrix aus, ohne den Modus anzuwenden.
func (a *ProjectAccess) Decide(project string, res Resource, act Action, obj Object) Decision {
	a.checks.Add(1)
	if !a.valid {
		return Decision{Hidden: true, Reason: "no account"}
	}
	project = strings.TrimSpace(project)
	// Maschinen-Achse: nur der Besitzer der Maschine, egal welche Rolle.
	if res == ResKnowledge && obj.Machine != "" {
		if !a.OwnsMachine(obj.Machine) {
			return Decision{Hidden: true, Reason: "machine-axis knowledge of another account"}
		}
		return Decision{Allowed: true}
	}
	if project == "" {
		return a.decideGlobal(res, act, obj)
	}
	role := a.Role(project)
	if matrixAllows(role, res, act, obj) {
		return Decision{Allowed: true}
	}
	rank := RoleRank(role.Role)
	if res == ResSnapshot {
		// Eine Kopie des ganzen Projekts: nur der Instanz-Admin ohne Rolle, und
		// jede Ablehnung ist 404, damit sie nichts über Projekt oder Snapshot
		// verrät. Auch ein unbeanspruchtes Projekt öffnet sich nicht.
		if a.IsAdmin() {
			return Decision{Allowed: true, Reason: "instance admin"}
		}
		return Decision{Hidden: true, Reason: "snapshot needs member role"}
	}
	// Eine Remote ohne Projektzeile ist unbeansprucht. Anlegen bleibt wie bisher
	// möglich (gateProject); der Autor sieht und ändert seine eigenen Einträge
	// dort immer, der Instanz-Admin sieht alles, andere nichts.
	if rank == 0 && a.Unclaimed(project) {
		// Verified setzt nie der Autor selbst: das Recht gibt es nur in einem
		// beanspruchten Projekt (can_review, lead, owner) oder dem Admin.
		if act == ActCreate || (obj.Own && act != ActVerify) || a.IsAdmin() {
			return Decision{Allowed: true, Reason: "unclaimed project"}
		}
	}
	// Die eigene Session bleibt dem Besitzer lesbar, auch ohne Rolle.
	if (res == ResTranscript || res == ResSessionMeta) && act == ActRead && obj.Own {
		return Decision{Allowed: true}
	}
	d := Decision{Hidden: rank == 0 || act == ActRead}
	if rank == 0 {
		d.Reason = "not a member"
	} else {
		d.Reason = "role " + role.Role + " may not " + string(act) + " " + string(res)
	}
	return d
}

// decideGlobal: Objekte ohne Projekt (globales Wissen und globale Aufträge)
// lesen und anlegen alle Konten; ändern darf der Autor und der Instanz-Admin.
// Sessions ohne Projekt gehören ihrem Besitzer.
func (a *ProjectAccess) decideGlobal(res Resource, act Action, obj Object) Decision {
	switch res {
	case ResKnowledge, ResRequest, ResDocument:
		switch act {
		case ActRead, ActCreate, ActWork:
			return Decision{Allowed: true}
		case ActEdit, ActVerify:
			if obj.Own || a.IsAdmin() {
				return Decision{Allowed: true}
			}
			return Decision{Reason: "global entry of another author"}
		}
	case ResSessionMeta, ResTranscript:
		if res == ResSessionMeta && act == ActCreate {
			return Decision{Allowed: true}
		}
		if (act == ActRead || act == ActShare) && obj.Own {
			return Decision{Allowed: true}
		}
		return Decision{Hidden: true, Reason: "session without project of another account"}
	}
	return Decision{Hidden: true, Reason: "no project"}
}

// Allow entscheidet und wendet den Modus an: im Log-Modus true.
func (a *ProjectAccess) Allow(project string, res Resource, act Action, obj Object) bool {
	d := a.Decide(project, res, act, obj)
	return a.st.accessCfg().apply(a.principal.ID, project, res, act, d)
}

// Check ist Allow mit Fehler: ErrAccessNotFound (404) oder ErrAccessForbidden (403).
func (a *ProjectAccess) Check(project string, res Resource, act Action, obj Object) error {
	d := a.Decide(project, res, act, obj)
	if a.st.accessCfg().apply(a.principal.ID, project, res, act, d) {
		return nil
	}
	if d.Hidden {
		return ErrAccessNotFound
	}
	return ErrAccessForbidden
}

// -------- Hilfen je Ressource

func knowledgeObject(a *ProjectAccess, k Knowledge) Object {
	return Object{Own: a.IsAuthor(k.Person), Confidence: k.Confidence, Machine: k.Scope.Machine}
}

// CanSeeKnowledge: Lesefilter für Listen, Suche und Bootstrap.
func (a *ProjectAccess) CanSeeKnowledge(k Knowledge) bool {
	return a.Allow(k.Scope.Project, ResKnowledge, ActRead, knowledgeObject(a, k))
}

// CanSeeKnowledgeHistory: der Verlauf hält frühere Fassungen ohne ihre
// Vertrauensstufe, darunter vielleicht nie freigegebene. Er gilt deshalb wie ein
// Eintrag der untersten Stufe: ab member, nicht für Gäste.
func (a *ProjectAccess) CanSeeKnowledgeHistory(k Knowledge) bool {
	obj := knowledgeObject(a, k)
	obj.Confidence = "staged"
	return a.Allow(k.Scope.Project, ResKnowledge, ActRead, obj)
}

// CheckKnowledge prüft eine Aktion auf einen vorhandenen Eintrag.
func (a *ProjectAccess) CheckKnowledge(k Knowledge, act Action) error {
	return a.Check(k.Scope.Project, ResKnowledge, act, knowledgeObject(a, k))
}

// CheckKnowledgeCreate prüft das Anlegen; ein Eintrag mit confidence=verified
// braucht zusätzlich das Verified-Recht.
func (a *ProjectAccess) CheckKnowledgeCreate(k Knowledge) error {
	obj := knowledgeObject(a, k)
	obj.Own = true
	if err := a.Check(k.Scope.Project, ResKnowledge, ActCreate, obj); err != nil {
		return err
	}
	if k.Confidence == "verified" {
		return a.Check(k.Scope.Project, ResKnowledge, ActVerify, obj)
	}
	return nil
}

// CanSeeSessionMeta: Metadaten (wer arbeitet wo).
func (a *ProjectAccess) CanSeeSessionMeta(s Session) bool {
	return a.Allow(s.Scope.Project, ResSessionMeta, ActRead, Object{Own: a.OwnsSession(s), Guests: s.Visibility == VisGuests})
}

// transcriptObject braucht das Freigabe-Flag der Session.
func (a *ProjectAccess) transcriptObject(s Session) Object {
	return Object{Own: a.OwnsSession(s), Shared: s.Shared, Guests: s.Visibility == VisGuests}
}

// CanSeeTranscript: Transkript und Rohdaten.
func (a *ProjectAccess) CanSeeTranscript(s Session) bool {
	return a.Allow(s.Scope.Project, ResTranscript, ActRead, a.transcriptObject(s))
}

// CheckTranscript prüft Lesen (und Teilen) einer Session.
func (a *ProjectAccess) CheckTranscript(s Session, act Action) error {
	return a.Check(s.Scope.Project, ResTranscript, act, a.transcriptObject(s))
}

// CanSeeProject: Lesefilter für Ressourcen ohne Objektmerkmale.
func (a *ProjectAccess) CanSeeProject(project string, res Resource) bool {
	return a.Allow(project, res, ActRead, Object{})
}

// RequestFilter begrenzt eine Auftragssuche auf die Projekte, die der Aufrufer
// lesen darf. Die Begrenzung steckt in der Abfrage (Seitengröße und Cursor
// bleiben richtig) und gilt nur bei Durchsetzung; im Log-Modus bleibt die Suche
// wie sie ist, und NoteRequestHits protokolliert, was verweigert würde.
func (a *ProjectAccess) RequestFilter(f requestdomain.SearchFilter) requestdomain.SearchFilter {
	a.Filtered()
	if !a.st.AccessEnforced() {
		return f
	}
	f.Restrict, f.Projects = true, a.Projects()
	f.UnclaimedAuthor, f.UnclaimedAll = a.principal.Label, a.IsAdmin()
	return f
}

// NoteRequestHits läuft im Log-Modus über die Treffer einer Auftragssuche, damit
// protokolliert wird, was die Durchsetzung ausblenden würde.
func (a *ProjectAccess) NoteRequestHits(hits []requestdomain.SearchHit) {
	if a.st.AccessEnforced() {
		return
	}
	for _, h := range hits {
		a.Allow(h.Request.Scope.Project, ResRequest, ActRead, Object{})
	}
}

// ResDelivery ist das Ausliefern von Inhalt an Agenten (Bootstrap, relevantes
// Wissen, Ghost-Hook, Suche). Es ist strenger als Lesen: ausgeliefert wird nur,
// was jemand abgelegt hat, der im Projekt jetzt mindestens member ist.
const ResDelivery Resource = "delivery"

func (a *ProjectAccess) writersOf(project string) map[string]bool {
	if a.writers == nil {
		a.writers = map[string]map[string]bool{}
	}
	w, ok := a.writers[project]
	if !ok {
		w = a.st.ProjectWriters(project)
		a.writers[project] = w
	}
	return w
}

// deliverable: der Autor ist im Projekt aktuell mindestens member. Ein leerer
// Autor (Betreiberpfade wie der Distiller) zählt als vertrauenswürdig. Im
// unbeanspruchten Projekt liefert der Aufrufer seine eigenen Einträge aus.
func (a *ProjectAccess) deliverable(project, author, confirmedBy string, verified bool) bool {
	if project == "" || author == "" {
		return true
	}
	if a.Unclaimed(project) {
		return a.IsAuthor(author) || a.IsAdmin()
	}
	w := a.writersOf(project)
	if w[author] {
		return true
	}
	// Übernahme: ein Prüfer, Lead oder Owner, der den Eintrag auf verified setzt,
	// übernimmt ihn; das Setzen verlangt das Verified-Recht.
	return verified && confirmedBy != "" && w[confirmedBy]
}

func (a *ProjectAccess) applyDelivery(project string, ok bool) bool {
	d := Decision{Allowed: ok, Hidden: true, Reason: "author is not a member of the project"}
	return a.st.accessCfg().apply(a.principal.ID, project, ResDelivery, ActRead, d)
}

// CanDeliverKnowledge: Eintrag darf an einen Agenten ausgeliefert werden. Gilt
// zusätzlich zu CanSeeKnowledge. Inhalt, den Fremde vor dem Claim eines Projekts
// abgelegt haben, bleibt gespeichert und sichtbar, wird aber nicht ausgeliefert,
// bis ein Prüfer, Lead oder Owner ihn auf verified setzt.
func (a *ProjectAccess) CanDeliverKnowledge(k Knowledge) bool {
	if k.Scope.Project == "" {
		return true
	}
	return a.applyDelivery(k.Scope.Project, a.deliverable(k.Scope.Project, k.Person, k.ConfirmedBy, k.Confidence == "verified"))
}

// CanDeliverGhost: bei Ghost-Beschreibungen muss der Autor Schreibrecht haben.
func (a *ProjectAccess) CanDeliverGhost(g GhostFile) bool {
	return a.applyDelivery(g.Project, a.deliverable(g.Project, g.Person, "", false))
}

// CanSeeGhost: Lesefilter für Ghost-Listen (der Autor sieht seine eigenen).
func (a *ProjectAccess) CanSeeGhost(g GhostFile) bool {
	return a.Allow(g.Project, ResGhost, ActRead, Object{Own: a.IsAuthor(g.Person)})
}

// CanSeeDocument: Lesefilter für Dokumentlisten.
func (a *ProjectAccess) CanSeeDocument(d Document) bool {
	return a.Allow(d.Project, ResDocument, ActRead, Object{Own: a.IsAuthor(d.Person)})
}

// GateList entscheidet, ob eine Liste mit ausdrücklichem Projekt überhaupt
// angefragt werden darf. perEntry sagt, dass der Aufrufer jeden Eintrag danach
// filtert (mit Own); dann darf auch eine unbeanspruchte Remote durch, weil der
// Filter dem Autor das Seine zeigt und allen anderen nichts.
func (a *ProjectAccess) GateList(project string, res Resource, perEntry bool) error {
	project = strings.TrimSpace(project)
	if perEntry && project != "" && a.Unclaimed(project) && a.Role(project).Role == "" {
		a.Filtered()
		return nil
	}
	return a.Check(project, res, ActRead, Object{Confidence: "verified"})
}
