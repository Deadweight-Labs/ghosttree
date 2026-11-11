package request

import (
	"fmt"
	"strings"
)

// Rollen eines Requests im Arbeitspaket. Pflicht gehört zum Lastenheft: ohne
// ihn ist das Paket nicht fertig. Optional ist mitgeführt, bindet aber nicht.
const (
	RolePflicht  = "pflicht"
	RoleOptional = "optional"
)

// Zustände eines Arbeitspakets. Ein geschlossenes Paket (Branch gemergt oder
// aufgegeben) wird nicht mehr an abgezweigte Branches vererbt und kommt in
// keinem Briefing mehr vor.
const (
	PackageOpen   = "open"
	PackageClosed = "closed"
)

// Package ist ein benannter Zusammenschluss von Requests, gebunden an Projekt
// und Branch und später an einen PR (REQ-601).
type Package struct {
	ID      int64  `json:"id"`
	Project string `json:"project"`
	Branch  string `json:"branch"`
	Name    string `json:"name"`
	// NameHidden: der Betrachter sieht statt des Namens eine neutrale Nummer, weil
	// der Name den Branch verraten könnte; die Oberfläche setzt ihr eigenes Wort ein.
	NameHidden bool   `json:"name_hidden,omitempty"`
	State      string `json:"state"`
	// PR ist die Nummer des Pull Requests, 0 solange keiner bekannt ist. Das
	// Feld wird von Hand oder von der GitHub-Anbindung gesetzt; hier gibt es
	// keinen Abgleich mit GitHub.
	PR        int64  `json:"pr,omitempty"`
	CreatedBy string `json:"created_by,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func (p Package) HumanID() string { return fmt.Sprintf("PKG-%d", p.ID) }

// PackageMember bindet einen Request an ein Paket.
type PackageMember struct {
	RequestID int64  `json:"request_id"`
	Role      string `json:"role"`
	AddedBy   string `json:"added_by,omitempty"`
	AddedAt   string `json:"added_at,omitempty"`
}

// PackageMemberView ist ein Mitglied samt dem, was der Betrachter vom Request
// lesen darf: Stand, Kriterien-Zahlen, offene Human-Tasks. Criteria (mit
// Nachweisen und Git-Ankern) füllt nur die Prüfansicht.
type PackageMemberView struct {
	PackageMember
	Title    string `json:"title"`
	Type     string `json:"type"`
	State    string `json:"state"`
	Priority string `json:"priority,omitempty"`
	Open     int    `json:"open_criteria"`
	Met      int    `json:"met_criteria"`
	Waived   int    `json:"waived_criteria"`
	// Criteria nur in der Prüfansicht.
	Criteria []Criterion `json:"criteria,omitempty"`
	// Tasks sind die Human-Tasks, die noch nicht bestätigt sind (offen oder
	// erledigt und wartend auf Bestätigung).
	Tasks []TaskRef `json:"tasks,omitempty"`
}

// PackageDetail ist ein Paket mit seinen Mitgliedern.
type PackageDetail struct {
	Package
	// InheritedFrom nennt den Branch, dessen Paket hier gilt, wenn die Abfrage
	// für einen abgezweigten Branch kam. Leer beim eigenen Paket.
	InheritedFrom string              `json:"inherited_from,omitempty"`
	Members       []PackageMemberView `json:"members"`
	// Uncovered sind Anforderungen von Ideen der Mitglieder, die noch kein
	// erfülltes Kriterium haben und nicht erlassen sind. Nur die Prüfansicht
	// (package_review) füllt es (REQ-611).
	Uncovered []UncoveredRequirement `json:"uncovered_requirements,omitempty"`
	// Features sind die Punkte „Feature X: Beschreibung angepasst?“ für einen
	// Paket-PR: Features, in deren Bereichen der PR Pfade ändert (REQ-612). Nur
	// gefüllt, wenn das Paket einen PR trägt und dessen Pfade gemeldet sind.
	Features []FeatureCheck `json:"features,omitempty"`
}

// Stände eines Feature-Punktes der Checkliste.
const (
	// FeatureCheckOpen: die Beschreibung ist noch nicht für diesen PR angesehen.
	FeatureCheckOpen = "open"
	// FeatureCheckUpdated: eine neue Fassung trägt diesen PR.
	FeatureCheckUpdated = "updated"
	// FeatureCheckUnchanged: jemand hat geprüft, dass die Beschreibung stimmt.
	FeatureCheckUnchanged = "unchanged"
)

// FeatureCheck ist ein Punkt der Checkliste eines PRs: ein Feature, das der PR
// berührt, und ob seine Beschreibung dafür angesehen wurde.
type FeatureCheck struct {
	Feature int64  `json:"feature"`
	Name    string `json:"name"`
	// Text ist der Punkt, wie ihn die Checkliste zeigt.
	Text  string `json:"text"`
	State string `json:"state"`
	// Revision ist die jüngste Fassung; Revisions, die den PR tragen, stehen in
	// PRRevisions.
	Revision    int   `json:"revision"`
	PRRevisions []int `json:"pr_revisions,omitempty"`
	// Paths sind einige der geänderten Pfade im Bereich des Features, PathCount
	// ihre Zahl.
	Paths     []string `json:"paths,omitempty"`
	PathCount int      `json:"path_count"`
	// Note ist die Notiz der Prüfung bei unchanged.
	Note string `json:"note,omitempty"`
}

// FeatureCheckText ist der Wortlaut des Punktes.
func FeatureCheckText(id int64, name string) string {
	return fmt.Sprintf("Feature FEAT-%d %s: Beschreibung angepasst?", id, name)
}

// UncoveredRequirement ist eine Anforderung einer Idee ohne erfülltes Kriterium.
// State ist none (kein Kriterium) oder open (Kriterien da, keines erfüllt).
type UncoveredRequirement struct {
	Idea      int64  `json:"idea"`
	IdeaTitle string `json:"idea_title"`
	No        int    `json:"no"`
	Text      string `json:"text"`
	State     string `json:"state"`
}

// PackageInput legt ein Paket an.
type PackageInput struct {
	Project string
	Branch  string
	Name    string
	PR      int64
	Members []PackageMember
	Person  string
}

// PackagePatch ändert ein Paket. nil lässt das Feld unberührt; PR 0 löst die
// Bindung an den PR.
type PackagePatch struct {
	Name  *string `json:"name,omitempty"`
	PR    *int64  `json:"pr,omitempty"`
	State *string `json:"state,omitempty"`
}

// ValidateRole lässt nur die zwei bekannten Rollen zu. Leer heißt pflicht: wer
// einen Request ins Paket stellt, meint ihn in der Regel als Pflichtumfang.
func ValidateRole(role string) (string, error) {
	switch strings.TrimSpace(role) {
	case "", RolePflicht:
		return RolePflicht, nil
	case RoleOptional:
		return RoleOptional, nil
	}
	return "", NewRuleError("invalid_package_role", "package role must be pflicht or optional", "use pflicht or optional", nil)
}

// ImportItem ist ein Schritt der Übernahme alter branch-Relationen.
type ImportItem struct {
	RelationID int64  `json:"relation_id"`
	RequestID  int64  `json:"request_id"`
	Project    string `json:"project"`
	Branch     string `json:"branch"`
	PackageID  int64  `json:"package_id,omitempty"`
	// Result: "added" (Mitglied neu), "already_member" (war schon drin, nur die
	// Relation entfällt) oder "skipped" (mit Reason).
	Result string `json:"result"`
	Reason string `json:"reason,omitempty"`
}

// ImportReport sagt, was die Übernahme getan hat (oder bei dry_run täte).
type ImportReport struct {
	DryRun          bool         `json:"dry_run"`
	PackagesCreated []int64      `json:"packages_created,omitempty"`
	Items           []ImportItem `json:"items"`
	Added           int          `json:"added"`
	AlreadyMember   int          `json:"already_member"`
	Skipped         int          `json:"skipped"`
}

// Add trägt einen Schritt ein und zählt ihn nach seinem Ergebnis.
func (r *ImportReport) Add(item ImportItem) {
	r.Items = append(r.Items, item)
	switch item.Result {
	case "added":
		r.Added++
	case "already_member":
		r.AlreadyMember++
	default:
		r.Skipped++
	}
}

// PackageOfRequest ist ein Paket, in dem ein Request steckt, mit seiner Rolle dort.
type PackageOfRequest struct {
	Package
	Role string `json:"role"`
}

// PackageSummary ist ein Paket in einer Projektliste: Requests und offene
// Kriterien zählen nur, was der Betrachter lesen darf.
type PackageSummary struct {
	Package
	Requests     int `json:"requests"`
	OpenCriteria int `json:"open_criteria"`
}
