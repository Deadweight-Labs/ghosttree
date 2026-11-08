package store

import (
	"database/sql"
	"errors"
	"strings"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Pull Requests lesen (REQ-599): die Liste eines Projekts und der PR mit seiner
// Checkliste und dem, was zu ihm gesagt wurde. Geschrieben wird in pullrequest.go
// (Webhook, Nachholen); hier wird nur gelesen. Das Recht ist das Lesen des
// Projekts; was darin an Einträgen hängt, geht durch die Sichtprüfung des
// jeweiligen Objekts.

// Zustände, nach denen die Liste filtert. closed heißt geschlossen ohne Merge
// (anders als bei GitHub, wo ein gemergter PR auch geschlossen ist).
const (
	PullOpen   = "open"
	PullMerged = "merged"
	PullClosed = "closed"
)

// Grenzen einer Seite der Liste.
const (
	PullDefaultLimit = 30
	PullMaxLimit     = 100
)

// PullListFilter wählt aus den PRs eines Projekts.
type PullListFilter struct {
	// State: open, merged, closed; leer oder all heißt alle.
	State string
	// Branch begrenzt auf PRs, deren Head dieser Branch ist.
	Branch string
	// Author begrenzt auf einen GitHub-Login (Groß-/Kleinschreibung egal).
	Author string
	Limit  int
	Offset int
}

// PullSummary ist eine Zeile der Liste.
type PullSummary struct {
	Number    int64  `json:"number"`
	Title     string `json:"title"`
	State     string `json:"state"`
	Head      string `json:"head"`
	Base      string `json:"base,omitempty"`
	Author    string `json:"author,omitempty"`
	OpenedAt  string `json:"opened_at"`
	UpdatedAt string `json:"updated_at"`
	MergedAt  string `json:"merged_at,omitempty"`
	// Commits ist die Zahl der Commits, die der PR beim letzten Abgleich mit
	// GitHub trug (0, solange keiner stattfand).
	Commits int `json:"commits"`
}

// PullCounts sind die PRs je Zustand unter den übrigen Filtern (Branch,
// Autor), unabhängig vom Zustandsfilter: die Zahlen auf den Reitern.
type PullCounts struct {
	Open   int `json:"open"`
	Merged int `json:"merged"`
	Closed int `json:"closed"`
}

// PullList ist eine Seite der Liste.
type PullList struct {
	Project string        `json:"project"`
	Items   []PullSummary `json:"items"`
	// Total ist die Zahl der Treffer aller Seiten.
	Total  int        `json:"total"`
	Counts PullCounts `json:"counts"`
	Limit  int        `json:"limit"`
	Offset int        `json:"offset"`
}

func pullRuleError(code, msg, hint string) error {
	return requestdomain.NewRuleError(code, msg, hint, nil)
}

func normalizePullFilter(f PullListFilter) (PullListFilter, error) {
	f.State = strings.ToLower(strings.TrimSpace(f.State))
	switch f.State {
	case "", "all":
		f.State = ""
	case PullOpen, PullMerged, PullClosed:
	default:
		return f, pullRuleError("pull_state_invalid", "unknown pull request state "+cleanLine(f.State, 40), "use open, merged, closed or all")
	}
	f.Branch = strings.TrimSpace(f.Branch)
	f.Author = strings.TrimSpace(f.Author)
	if f.Limit <= 0 {
		f.Limit = PullDefaultLimit
	}
	if f.Limit > PullMaxLimit {
		f.Limit = PullMaxLimit
	}
	if f.Offset < 0 {
		return f, pullRuleError("pull_offset_invalid", "offset must not be negative", "start at 0")
	}
	return f, nil
}

// PullRequestList liest eine Seite der PRs eines Projekts, die zuletzt
// geänderten zuerst. Das Recht prüft der Aufrufer (ProjectAccess.PullRequests).
func (s *Store) PullRequestList(project string, f PullListFilter) (PullList, error) {
	if s.reader != nil {
		return s.reader.PullRequestList(project, f)
	}
	project = scope.NormalizeRemote(project)
	f, err := normalizePullFilter(f)
	if err != nil {
		return PullList{}, err
	}
	out := PullList{Project: project, Items: []PullSummary{}, Limit: f.Limit, Offset: f.Offset}
	where := `p.project=?`
	args := []any{project}
	if f.Branch != "" {
		where += ` AND p.head_ref=?`
		args = append(args, f.Branch)
	}
	if f.Author != "" {
		where += ` AND p.author=? COLLATE NOCASE`
		args = append(args, f.Author)
	}
	rows, err := s.db.Query(`SELECT p.state, COUNT(*) FROM git_pull_requests p WHERE `+where+` GROUP BY p.state`, args...)
	if err != nil {
		return PullList{}, err
	}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			rows.Close()
			return PullList{}, err
		}
		switch st {
		case PullOpen:
			out.Counts.Open = n
		case PullMerged:
			out.Counts.Merged = n
		case PullClosed:
			out.Counts.Closed = n
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return PullList{}, err
	}
	switch f.State {
	case "":
		out.Total = out.Counts.Open + out.Counts.Merged + out.Counts.Closed
	case PullOpen:
		out.Total = out.Counts.Open
	case PullMerged:
		out.Total = out.Counts.Merged
	case PullClosed:
		out.Total = out.Counts.Closed
	}
	if f.State != "" {
		where += ` AND p.state=?`
		args = append(args, f.State)
	}
	args = append(args, f.Limit, f.Offset)
	rows, err = s.db.Query(`SELECT p.number,p.title,p.state,p.head_ref,p.base_ref,p.author,p.opened_at,p.updated_at,p.merged_at,
		(SELECT COUNT(*) FROM git_pr_commits c WHERE c.project=p.project AND c.pr=p.number AND c.in_pr=1)
		FROM git_pull_requests p WHERE `+where+` ORDER BY p.updated_at DESC, p.number DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return PullList{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var p PullSummary
		if err := rows.Scan(&p.Number, &p.Title, &p.State, &p.Head, &p.Base, &p.Author, &p.OpenedAt, &p.UpdatedAt, &p.MergedAt, &p.Commits); err != nil {
			return PullList{}, err
		}
		out.Items = append(out.Items, p)
	}
	return out, rows.Err()
}

// PullRequests ist die Liste für einen Betrachter: wer das Projekt lesen darf,
// sieht seine PRs. Ein Projekt, das es nicht gibt, und eines, das er nicht
// lesen darf, sind beide sql.ErrNoRows.
func (a *ProjectAccess) PullRequests(project string, f PullListFilter) (PullList, error) {
	project = scope.NormalizeRemote(strings.TrimSpace(project))
	if project == "" {
		a.Filtered()
		return PullList{}, sql.ErrNoRows
	}
	if err := a.Check(project, ResProject, ActRead, Object{}); err != nil {
		return PullList{}, sql.ErrNoRows
	}
	return a.st.PullRequestList(project, f)
}

// PullChecklist fasst zusammen, was am PR noch aussteht; die Einzelheiten stehen
// im Paket (Mitglieder, Kriterien) und in Features. Eine Zahl ist nur so
// vollständig, wie der Betrachter die Requests des Pakets lesen darf.
type PullChecklist struct {
	// HasPackage: ein Arbeitspaket ist an diesen PR gebunden.
	HasPackage bool `json:"has_package"`
	// RequiredTotal sind die Pflicht-Requests des Pakets, RequiredOpen davon die
	// mit mindestens einem offenen Kriterium.
	RequiredTotal int `json:"required_total"`
	RequiredOpen  int `json:"required_open"`
	// FeaturesOpen sind die Feature-Punkte, die noch nicht angesehen sind.
	FeaturesOpen int `json:"features_open"`
	// Uncovered sind Anforderungen von Ideen ohne erfülltes Kriterium.
	Uncovered int `json:"uncovered_requirements"`
	// HumanTasks sind unbestätigte Aufgaben an Menschen an den Requests.
	HumanTasks int `json:"human_tasks"`
}

// PullDetail ist ein PR mit allem, was zu ihm gehört.
type PullDetail struct {
	PullRequestInfo
	// Commits ist die Zahl der Commits des PRs beim letzten Abgleich.
	Commits int `json:"commits"`
	// Package ist das Arbeitspaket des PRs in der Prüfansicht (package_review):
	// Mitglieder mit Kriterien, Nachweisen und Anker. Nil ohne Paket.
	Package *requestdomain.PackageDetail `json:"package,omitempty"`
	// Features sind die Punkte „Feature X: Beschreibung angepasst?“ (REQ-612).
	Features  []requestdomain.FeatureCheck `json:"features"`
	Checklist PullChecklist                `json:"checklist"`
	// Brief ist, was zu dem PR gesagt wurde (Wissen und Ghost-Pfade mit dem Anker
	// des PRs), soweit der Betrachter es lesen darf.
	Brief PullRequestBrief `json:"brief"`
}

// maxBriefEntries begrenzt die Listen des Briefs in einer Antwort.
const maxBriefEntries = 500

// PullRequestBriefView ist PullRequestBriefOf für einen Betrachter: jeder
// Wissenseintrag geht durch die Sichtprüfung des Eintrags, die Ghost-Pfade nur,
// wenn er die Ghosts des Projekts sehen darf. Das Recht, Wissen des Projekts zu
// lesen, wird verlangt (ErrAccessForbidden / ErrAccessNotFound).
func (a *ProjectAccess) PullRequestBriefView(project string, n int64) (PullRequestBrief, error) {
	project = scope.NormalizeRemote(strings.TrimSpace(project))
	if err := a.Check(project, ResKnowledge, ActRead, Object{Confidence: "verified"}); err != nil {
		return PullRequestBrief{}, err
	}
	brief, err := a.st.PullRequestBriefOf(project, n)
	if err != nil {
		return PullRequestBrief{}, err
	}
	a.Filtered()
	seen := brief.Knowledge[:0:0]
	for _, k := range brief.Knowledge {
		if len(seen) >= maxBriefEntries {
			break
		}
		full, err := a.st.KnowledgeByID(k.ID)
		if err != nil || !a.CanSeeKnowledge(full) {
			continue
		}
		seen = append(seen, k)
	}
	brief.Knowledge = seen
	if len(brief.Ghosts) > 0 && !a.CanSeeGhost(GhostFile{Project: project}) {
		brief.Ghosts = []string{}
	}
	if len(brief.Ghosts) > maxBriefEntries {
		brief.Ghosts = brief.Ghosts[:maxBriefEntries]
	}
	return brief, nil
}

// PullRequestDetail liest einen PR mit Paket, Checkliste und Brief für einen
// Betrachter. Ein PR, den GitHub nie gemeldet hat, ein unbekanntes Projekt und
// eines, das er nicht lesen darf, sind alle sql.ErrNoRows. Wer das Projekt
// lesen darf, aber kein Wissen, sieht den PR mit leerem Brief.
func (a *ProjectAccess) PullRequestDetail(project string, n int64) (PullDetail, error) {
	info, err := a.PullRequestView(project, n)
	if err != nil {
		return PullDetail{}, err
	}
	d := PullDetail{PullRequestInfo: info, Features: []requestdomain.FeatureCheck{}}
	counts, err := a.st.pullCommitCount(info.Project, n)
	if err != nil {
		return PullDetail{}, err
	}
	d.Commits = counts
	pkg, err := a.st.PackageForPR(info.Project, n)
	switch {
	case err == nil:
		view := a.PackageView(pkg, true)
		d.Features = append(d.Features, view.Features...)
		view.Features = nil
		d.Package = &view
		d.Checklist = checklistOf(view, d.Features)
	case errors.Is(err, sql.ErrNoRows):
		d.Features = append(d.Features, a.packageFeatureChecks(info.Project, n)...)
		d.Checklist = checklistOf(requestdomain.PackageDetail{}, d.Features)
		d.Checklist.HasPackage = false
	default:
		return PullDetail{}, err
	}
	brief, err := a.PullRequestBriefView(info.Project, n)
	switch {
	case err == nil:
		d.Brief = brief
	case errors.Is(err, ErrAccessForbidden), errors.Is(err, ErrAccessNotFound):
		d.Brief = PullRequestBrief{Project: info.Project, PR: n, Known: true, Knowledge: []PRKnowledge{}, Ghosts: []string{}}
	default:
		return PullDetail{}, err
	}
	return d, nil
}

func checklistOf(p requestdomain.PackageDetail, features []requestdomain.FeatureCheck) PullChecklist {
	c := PullChecklist{HasPackage: p.ID != 0, Uncovered: len(p.Uncovered)}
	for _, m := range p.Members {
		if m.Role == requestdomain.RolePflicht {
			c.RequiredTotal++
			if m.Open > 0 {
				c.RequiredOpen++
			}
		}
		c.HumanTasks += len(m.Tasks)
	}
	for _, f := range features {
		if f.State == requestdomain.FeatureCheckOpen {
			c.FeaturesOpen++
		}
	}
	return c
}

func (s *Store) pullCommitCount(project string, n int64) (int, error) {
	if s.reader != nil {
		return s.reader.pullCommitCount(project, n)
	}
	var c int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM git_pr_commits WHERE project=? AND pr=? AND in_pr=1`, project, n).Scan(&c)
	return c, err
}
