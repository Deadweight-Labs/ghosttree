package store

import (
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/feature"
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
	// ResPain ist ein Schmerz (Problem ohne Lösung), ResTask eine Aufgabe für
	// Menschen. Beide haben immer ein Projekt.
	ResPain Resource = "pain"
	ResTask Resource = "task"
	// ResIdea ist ein Wunsch (REQ-606), ResLabel ein Label, ein Thema oder eine
	// gespeicherte Suche eines Projekts (REQ-600). Beide haben immer ein Projekt.
	ResIdea  Resource = "idea"
	ResLabel Resource = "label"
	// ResRunbook ist ein Ablauf (REQ-604): benannte Anleitung mit Schritten und
	// optional ausführbaren Zielen. Wie Wissen gilt er für ein Projekt, eine
	// Maschine oder beides; ohne beides gehört er seinem Autor.
	ResRunbook Resource = "runbook"
	// ResFeature ist ein Feature (REQ-612): die lebende Beschreibung dessen, was
	// das Produkt tut. Es hat immer ein Projekt.
	ResFeature Resource = "feature"
	// ResComment ist ein Kommentar an einem Issue, einer Idee oder einem Pain. Wer
	// das Objekt lesen darf, liest ihn; geprüft wird zusätzlich das Objekt.
	ResComment Resource = "comment"
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
	// ActRelease: der Besitzer der privaten Herkunfts-Session hebt die Haltung
	// eines distillierten Fundes auf (confidence staged); verifizieren darf er
	// ihn damit nicht, das bleibt den Prüfern.
	ActRelease Action = "release"
	// ActWork: an einem Auftrag arbeiten (jedes Mitglied, auch an fremden).
	ActWork  Action = "work"
	ActShare Action = "share"
	// ActPost: in den Projektraum schreiben (Nachricht, Antwort, Thread).
	ActPost Action = "post"
	// ActApprove: eine Fassung eines Ablaufs für die Ausführung freigeben, oder
	// einen Vorschlag zu einem Ablauf annehmen. Nur Owner und Lead.
	ActApprove Action = "approve"
	// ActDelete: einen Kommentar weich löschen.
	ActDelete Action = "delete"
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
	// Machine: Maschinen-Achse eines Wissenseintrags OHNE Projekt; nicht leer
	// heißt, dass nur der Besitzer der Maschine (und freigegebene Orgs) Zugriff
	// hat. Bei Einträgen mit Projekt bleibt das Feld leer, dort zählt allein die
	// Projektrolle.
	Machine string
	// SharedOrgs: die Orgs, an die dieser Eintrag mit Maschinen-Scope wirksam
	// freigegeben ist (Freigabe der Maschine besteht, Eintrag liegt noch auf
	// ihr). Leer heißt privat: nur der Besitzer der Maschine.
	SharedOrgs []int64
	// Unentitled: Orgs aus SharedOrgs, über die der Autor die Maschine nicht
	// verantworten darf (keine Schreib-Freigabe für genau diese Org). Sie geben
	// dem Betrachter keinen Zugang.
	Unentitled []int64
	// OriginOwner: der Betrachter besitzt die private Session, aus der ein noch
	// nicht freigegebener, distillierter Eintrag stammt; er darf ihn freigeben
	// oder verwerfen, nie verifizieren.
	OriginOwner bool
	// EntryID (Wissen) und RequestID (Auftrag) bestimmen bei Objekten ohne Projekt
	// den Autor: die Konto-Id aus knowledge_authors und request_authors, nie ein
	// Name. Beides 0 heißt ohne Besitzer.
	EntryID   int64
	RequestID int64
	// Assigned: der Betrachter ist der Zugewiesene einer Aufgabe. Unassigned:
	// niemand ist zugewiesen.
	Assigned   bool
	Unassigned bool
}

// Decision ist das Ergebnis der Matrix.
type Decision struct {
	Allowed bool
	// Hidden: bei Ablehnung 404 statt 403 (Lesen und Nicht-Mitglieder).
	Hidden bool
	Reason string
	// Strict: die Ablehnung gilt auch im Log-Modus (strictDenial).
	Strict bool
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
			return rank >= 3 || (rank == 2 && (obj.Own || obj.OriginOwner))
		case ActVerify:
			return rank >= 3 || (rank >= 2 && role.CanReview)
		case ActRelease:
			return rank >= 2 && obj.OriginOwner
		}
	case ResRequest:
		switch act {
		case ActRead:
			return rank >= 1
		case ActCreate, ActWork:
			return rank >= 2
		case ActEdit:
			return rank >= 3 || (rank == 2 && (obj.Own || obj.OriginOwner))
		}
	case ResPain:
		switch act {
		case ActRead:
			return rank >= 1
		case ActCreate, ActWork:
			return rank >= 2
		case ActEdit:
			return rank >= 3 || (rank == 2 && obj.Own)
		}
	case ResIdea:
		// Wie ein Pain: lesen ab Gast, anlegen und bearbeiten wie dort.
		switch act {
		case ActRead:
			return rank >= 1
		case ActCreate, ActWork:
			return rank >= 2
		case ActEdit:
			return rank >= 3 || (rank == 2 && obj.Own)
		}
	case ResFeature:
		// Lesen ab Gast. Anlegen und eine neue Fassung schreiben (ActWork) ab
		// member: die Beschreibung pflegt, wer den PR schreibt. Umbenennen,
		// einstellen und wieder aufnehmen (ActEdit) ab lead. Die Zuordnung steht
		// hier an einer Stelle: wer sie enger will, ändert nur diese Zeilen.
		switch act {
		case ActRead:
			return rank >= 1
		case ActCreate, ActWork:
			return rank >= 2
		case ActEdit:
			return rank >= 3
		}
	case ResComment:
		// Lesen ab Gast, schreiben ab member. Ändern darf nur der Autor, löschen
		// der Autor und jeder Lead oder Owner (Moderation).
		switch act {
		case ActRead:
			return rank >= 1
		case ActCreate:
			return rank >= 2
		case ActEdit:
			return rank >= 2 && obj.Own
		case ActDelete:
			return rank >= 3 || (rank >= 2 && obj.Own)
		}
	case ResLabel:
		// Lesen ab Gast, anlegen ab member, ändern und löschen ab lead. Ein Label
		// zu setzen prüft das Objekt selbst (ActWork auf dem Objekt).
		switch act {
		case ActRead:
			return rank >= 1
		case ActCreate:
			return rank >= 2
		case ActEdit:
			return rank >= 3
		}
	case ResTask:
		switch act {
		case ActRead:
			return rank >= 2 || (rank == 1 && obj.Assigned)
		case ActCreate:
			return rank >= 2
		case ActWork:
			// Ein Gast liest nur, auch eine ihm zugewiesene Aufgabe meldet er
			// nicht als erledigt (Robin, 2026-11-09).
			return rank >= 3 || (rank == 2 && (obj.Assigned || obj.Unassigned))
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
			// Privat heißt privat: eine nicht freigegebene Session liest nur ihr
			// Besitzer, auch kein Lead, kein Projekt-Owner (REQ-438 D).
			return obj.Own || (rank >= 2 && obj.Shared) || (rank >= 1 && obj.Guests)
		case ActShare:
			// Der Besitzer der Session; der Projekt-Owner darf eine freigegebene
			// Session zurücknehmen, eine private nie erreichen.
			return obj.Own || (rank >= 4 && obj.Shared)
		}
	case ResRunbook:
		switch act {
		case ActRead, ActCreate:
			return rank >= 2
		case ActEdit:
			// Wie bei Wissen (#1444): wer es besser weiß, schreibt eine neue
			// Fassung. Ausführbar wird sie dadurch nicht, das tut erst die
			// Freigabe.
			return rank >= 2
		case ActApprove:
			return rank >= 3
		}
	case ResSnapshot:
		return (act == ActRead || act == ActCreate) && rank >= 2
	case ResRoom:
		return ((act == ActRead || act == ActCreate) && rank >= 1) || (act == ActPost && rank >= 2)
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
	if c.enforce.Load() || d.Strict {
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
	mi       *machineIndex
	miOnce   sync.Once
	orgRoles map[string]string

	unclaimed map[string]bool
	writers   map[string]map[string]bool

	ownersOnce     sync.Once
	ownerIDs       map[string]int64
	ownerDefault   int64
	entryAuthors   map[int64]int64
	requestAuthors map[int64]int64

	defBranches sync.Map // Projekt -> Standard-Branch, je Betrachter gemerkt

	// actorSess merkt, welche Sessions hinter Akteur-Stempeln sichtbar sind.
	actorSess actorSessions

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

// OwnsMachine: das Konto verwaltet die Maschine (Besitzer, bei Org-Maschinen
// ein Maintainer). Altbestand und unbekannte Maschinen gehören dem Instanz-Owner.
func (a *ProjectAccess) OwnsMachine(machine string) bool {
	return a.machineLevel(machine, nil) == levelManage
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
	// Maschinen-Achse: der Besitzer der Maschine (bei Org-Maschinen ein
	// Maintainer) und Mitglieder einer Org, an die der Eintrag ausdrücklich
	// freigegeben ist, soweit die Freigabe es erlaubt. Sie gilt nur für Einträge
	// OHNE Projekt. Steht ein Projekt am Eintrag (Wissen zum Betrieb eines Repos
	// auf einer Maschine), entscheidet allein die Projektrolle über Sehen,
	// Ändern und Ausliefern; die Maschine ist dann Zuordnung, keine zweite Hürde
	// und keine zweite Tür (Entscheidung #4349).
	if (res == ResKnowledge || res == ResRunbook) && obj.Machine != "" && project == "" {
		level := a.machineLevel(obj.Machine, withoutOrgs(obj.SharedOrgs, obj.Unentitled))
		note := false
		switch {
		case level == levelManage:
		case level > levelNone:
			if !shareAllows(level, res, act, obj) {
				if act == ActRead {
					return Decision{Hidden: true, Reason: "machine-axis knowledge is not shared with you in this state"}
				}
				return Decision{Reason: "machine share does not allow " + string(act)}
			}
		default:
			// Der Autor liest und pflegt seine eigene Notiz auch ohne Zugang. Ist
			// sie an eine Org freigegeben, ändert er sie nicht mehr: ein
			// ehemaliges Mitglied bearbeitet nichts, was andere noch lesen.
			note = a.ownNoteOnUnclaimedMachine(obj, act) && !(act == ActEdit && len(obj.SharedOrgs) > 0)
			if !note {
				return Decision{Hidden: true, Reason: "machine-axis knowledge of another account"}
			}
		}
		switch {
		case level == levelManage:
			return Decision{Allowed: true}
		case level > levelNone:
			return Decision{Allowed: true, Reason: "machine knowledge shared with your organization"}
		}
		return Decision{Allowed: true, Reason: "own note on a machine nobody has claimed"}
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
		hiddenSession := res == ResTranscript && !obj.Own && !obj.Shared && !obj.Guests
		if act == ActCreate || (obj.Own && act != ActVerify && act != ActApprove) || (a.IsAdmin() && !hiddenSession) {
			return Decision{Allowed: true, Reason: "unclaimed project"}
		}
	}
	// Die eigene Session bleibt dem Besitzer lesbar, auch ohne Rolle.
	if (res == ResTranscript || res == ResSessionMeta) && act == ActRead && obj.Own {
		return Decision{Allowed: true}
	}
	d := Decision{Hidden: rank == 0 || act == ActRead, Strict: strictDenial(rank, act)}
	if rank == 0 {
		d.Reason = "not a member"
	} else {
		d.Reason = "role " + role.Role + " may not " + string(act) + " " + string(res)
	}
	return d
}

// strictDenial sagt, welche Ablehnungen auch im Log-Modus gelten (Robin,
// 2026-11-09, nach der UI-Durchsicht auf einer Instanz ohne Durchsetzung):
//
//   - Ein Gast schreibt nichts, was die Matrix ihm nicht gibt. Gäste sind
//     Außenstehende; der Log-Modus ist für die Umstellung der Mitglieder da,
//     nicht dafür, dass ein Gast Aufgaben erledigt, Abläufe anlegt oder Wissen
//     ändert.
//   - Wissen freigeben (verified) ohne Prüfrecht. Die Freigabe ist wie die eines
//     Ablaufs ein Sicherheitsmerkmal: freigegebenes Wissen bekommen Agenten als
//     geprüft geliefert.
//
// Lesen bleibt im Log-Modus offen, wie bisher.
func strictDenial(rank int, act Action) bool {
	if act == ActVerify {
		return true
	}
	return rank == 1 && act != ActRead
}

// decideGlobal: "global" heißt "überall für mich". Wissen, Aufträge und
// Dokumente ohne Projekt (und ohne Maschine) gehören allein ihrem Autorenkonto,
// auch der Instanz-Admin hat keinen Sonderblick. Anlegen darf jedes Konto.
// Sessions ohne Projekt gehören ihrem Besitzer.
func (a *ProjectAccess) decideGlobal(res Resource, act Action, obj Object) Decision {
	switch res {
	case ResRunbook:
		// Ein Ablauf ohne Projekt und Maschine gehört allein seinem Autor, auch
		// für die Freigabe: es gibt niemanden sonst, der ihn lesen dürfte.
		if act == ActCreate || obj.Own {
			return Decision{Allowed: true}
		}
		return Decision{Hidden: true, Reason: "global runbook of another author"}
	case ResKnowledge, ResRequest, ResDocument:
		if act == ActCreate {
			return Decision{Allowed: true}
		}
		if a.ownsGlobal(obj) {
			return Decision{Allowed: true}
		}
		return Decision{Hidden: true, Reason: "global entry of another author"}
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
	obj := Object{Own: a.ownsKnowledge(k), Confidence: k.Confidence, EntryID: k.ID}
	// Die Maschinen-Achse gibt es nur ohne Projekt; mit Projekt ist die Maschine
	// Zuordnung und lässt Besitz und Freigaben der Maschine außen vor.
	if k.Scope.Machine != "" && k.Scope.Project == "" {
		obj.Machine = k.Scope.Machine
		obj.SharedOrgs = a.sharedOrgsOf(EntryKnowledge, k.ID, k.Scope.Machine)
		// Eine Org zählt nur, wenn der Autor über genau sie schreiben darf.
		if len(obj.SharedOrgs) > 0 {
			ok := a.entitledSharedOrgs(k.Scope.Machine, a.st.knowledgeAuthorID(k.ID), obj.SharedOrgs)
			obj.Unentitled = withoutOrgs(obj.SharedOrgs, ok)
		}
		if k.ID == 0 {
			// Anlegen: gilt die Freigabe, die der Schreiber mitschickt, soweit er
			// über diese Org freigeben darf (mayShareOnCreate).
			for _, org := range k.ShareOrgIDs {
				if a.mayShareOnCreate(k.Scope.Machine, org) {
					obj.SharedOrgs = append(obj.SharedOrgs, org)
				}
			}
		}
	}
	if ref := knowledgeHeldRef(k); ref != "" {
		_, obj.OriginOwner = a.heldOrigin(ref)
	}
	return obj
}

// knowledgeHeld: gestagtes Wissen aus der privaten Session eines anderen Kontos.
func (a *ProjectAccess) knowledgeHeld(k Knowledge) bool {
	ref := knowledgeHeldRef(k)
	if ref == "" {
		return false
	}
	held, _ := a.heldOrigin(ref)
	return held
}

// CanSeeKnowledge: Lesefilter für Listen, Suche und Bootstrap.
func (a *ProjectAccess) CanSeeKnowledge(k Knowledge) bool {
	if a.knowledgeHeld(k) {
		return false
	}
	return a.Allow(k.Scope.Project, ResKnowledge, ActRead, knowledgeObject(a, k))
}

// CanSeeKnowledgeHistory: der Verlauf hält frühere Fassungen ohne ihre
// Vertrauensstufe, darunter vielleicht nie freigegebene. Er gilt deshalb wie ein
// Eintrag der untersten Stufe: ab member, nicht für Gäste.
func (a *ProjectAccess) CanSeeKnowledgeHistory(k Knowledge) bool {
	if a.knowledgeHeld(k) {
		return false
	}
	obj := knowledgeObject(a, k)
	obj.Confidence = "staged"
	return a.Allow(k.Scope.Project, ResKnowledge, ActRead, obj)
}

// CheckKnowledge prüft eine Aktion auf einen vorhandenen Eintrag.
func (a *ProjectAccess) CheckKnowledge(k Knowledge, act Action) error {
	if a.knowledgeHeld(k) {
		return ErrAccessNotFound
	}
	return a.Check(k.Scope.Project, ResKnowledge, act, knowledgeObject(a, k))
}

// CheckKnowledgeCreate prüft das Anlegen; ein Eintrag mit confidence=verified
// braucht zusätzlich das Verified-Recht.
func (a *ProjectAccess) CheckKnowledgeCreate(k Knowledge) error {
	if k.Scope.Machine != "" && k.Scope.Project == "" {
		// Ohne Verwaltungsrecht über die Maschine bleibt jede Org, über die der
		// Schreiber nicht schreiben darf, eine Verweigerung, kein stilles Weglassen.
		a.loadMachines()
		if a.machineLevel(k.Scope.Machine, nil) != levelManage {
			for _, org := range k.ShareOrgIDs {
				if !a.mayShareOnCreate(k.Scope.Machine, org) {
					return ErrAccessForbidden
				}
			}
		}
	}
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
	// Eine private Session erscheint nur ihrem Besitzer, auch nicht als Zeile.
	if !s.Shared && s.Visibility != VisGuests && !a.OwnsSession(s) && a.st.AccessEnforced() {
		return false
	}
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
	f.GlobalAccount, f.GlobalOwner = a.account, a.IsInstanceOwner()
	f.UnclaimedAuthor, f.UnclaimedAll = a.principal.Label, a.IsAdmin()
	// Distillierte, nicht übernommene Requests aus fremden privaten Sessions
	// bleiben dem Besitzer dieser Session vorbehalten.
	a.loadMachines()
	f.HideHeld, f.HeldViewer, f.HeldInstanceOwner = true, a.account, a.owner
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
// unbeanspruchten Projekt liefert der Aufrufer seine eigenen Einträge aus; auch
// der Instanz-Admin darf dort fremde Einträge nur lesen, in den Kontext seiner
// Agenten gelangen sie nicht.
func (a *ProjectAccess) deliverable(project, author, confirmedBy string, verified bool) bool {
	if project == "" || author == "" {
		return true
	}
	if a.Unclaimed(project) {
		return a.IsAuthor(author)
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
		if k.Scope.Machine != "" {
			return a.applyDelivery("", a.machineAuthorDelivers(k))
		}
		return true
	}
	// Mit Projekt gilt allein die Auslieferungssperre des Projekts (der Autor ist
	// dort jetzt Schreiber, #2581), auch wenn eine Maschine am Eintrag steht: sie
	// ist Zuordnung, nicht Berechtigung.
	return a.applyDelivery(k.Scope.Project, a.deliverable(k.Scope.Project, k.Person, k.ConfirmedBy, k.Confidence == "verified"))
}

// CanDeliverGhost: bei Ghost-Beschreibungen muss der Autor Schreibrecht haben.
func (a *ProjectAccess) CanDeliverGhost(g GhostFile) bool {
	return a.applyDelivery(g.Project, a.deliverable(g.Project, g.Person, "", false))
}

// CanDeliverFeature: ein Feature darf in den Hook-Kontext und die PR-Checkliste,
// wenn die Schreiber aller seiner Fassungen im Projekt Schreibrecht haben (eine
// spätere Fassung eines Mitglieds kann den Text einer früheren übernehmen). Ein
// Fremder kann in einem unbeanspruchten Projekt Features anlegen; der Text
// erreicht die Agenten des späteren Besitzers nicht.
func (a *ProjectAccess) CanDeliverFeature(f feature.Feature) bool {
	ok := a.deliverable(f.Project, f.VersionPerson, "", false) && a.deliverable(f.Project, f.Person, "", false)
	for _, p := range f.VersionPersons {
		ok = ok && a.deliverable(f.Project, p, "", false)
	}
	return a.applyDelivery(f.Project, ok)
}

// CanSeeGhost: Lesefilter für Ghost-Listen (der Autor sieht seine eigenen).
func (a *ProjectAccess) CanSeeGhost(g GhostFile) bool {
	return a.Allow(g.Project, ResGhost, ActRead, Object{Own: a.IsAuthor(g.Person)})
}

// CanSeeDocument: Lesefilter für Dokumentlisten.
func (a *ProjectAccess) CanSeeDocument(d Document) bool {
	return a.Allow(d.Project, ResDocument, ActRead, Object{Own: a.IsAuthor(d.Person)})
}

// globalListResource: nur Wissen und Aufträge haben globale Einträge, die sich
// die Liste selbst auf die eigenen begrenzt; jede andere Ressource ohne Projekt
// geht wie vorher durch die Matrix.
func globalListResource(res Resource) bool { return res == ResKnowledge || res == ResRequest }

// GateList entscheidet, ob eine Liste mit ausdrücklichem Projekt überhaupt
// angefragt werden darf. perEntry sagt, dass der Aufrufer jeden Eintrag danach
// filtert (mit Own); dann darf auch eine unbeanspruchte Remote durch, weil der
// Filter dem Autor das Seine zeigt und allen anderen nichts.
func (a *ProjectAccess) GateList(project string, res Resource, perEntry bool) error {
	project = strings.TrimSpace(project)
	if project == "" && globalListResource(res) {
		// Ohne Projekt liest jeder, was ihm gehört: die Liste begrenzt sich selbst
		// auf die eigenen globalen Einträge (decideGlobal) und die Projekte des Kontos.
		a.Filtered()
		return nil
	}
	if perEntry && project != "" && a.Unclaimed(project) && a.Role(project).Role == "" {
		a.Filtered()
		return nil
	}
	return a.Check(project, res, ActRead, Object{Confidence: "verified"})
}

// Viewer beschreibt den Betrachter für Abfragen, die private Sessions anderer
// Konten selbst ausschließen.
func (a *ProjectAccess) Viewer() SessionViewer {
	if !a.st.AccessEnforced() {
		return SessionViewer{}
	}
	a.loadMachines()
	return SessionViewer{Enforced: true, Account: a.account, Owner: a.owner}
}
