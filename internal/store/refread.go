package store

import (
	"database/sql"
	"errors"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// PullRequestInfo ist die Zeile eines PRs, wie GitHub sie gemeldet hat: Titel,
// Zustand, Branches und Zeiten. Der Autor ist der Login, den GitHub gemeldet hat (leer bei Altzeilen).
type PullRequestInfo struct {
	Project   string `json:"project"`
	Number    int64  `json:"number"`
	Title     string `json:"title"`
	State     string `json:"state"`
	Head      string `json:"head"`
	Base      string `json:"base,omitempty"`
	Author    string `json:"author,omitempty"`
	OpenedAt  string `json:"opened_at"`
	UpdatedAt string `json:"updated_at"`
	MergedAt  string `json:"merged_at,omitempty"`
	// Body ist der Text des PRs, wie GitHub ihn gemeldet hat (bereinigt, begrenzt);
	// leer, solange noch kein Ereignis ihn gebracht hat.
	Body string `json:"body,omitempty"`
}

// PullRequestOf liest einen PR. Ein PR, den GitHub nie gemeldet hat, ist
// sql.ErrNoRows. Das Recht prüft der Aufrufer (ProjectAccess.PullRequestView).
func (s *Store) PullRequestOf(project string, n int64) (PullRequestInfo, error) {
	if s.reader != nil {
		return s.reader.PullRequestOf(project, n)
	}
	project = scopeNormalize(project)
	p := PullRequestInfo{Project: project, Number: n}
	err := s.db.QueryRow(`SELECT title,state,head_ref,base_ref,author,opened_at,updated_at,merged_at,body FROM git_pull_requests WHERE project=? AND number=?`,
		project, n).Scan(&p.Title, &p.State, &p.Head, &p.Base, &p.Author, &p.OpenedAt, &p.UpdatedAt, &p.MergedAt, &p.Body)
	return p, err
}

// PullRequestView ist der PR für einen Betrachter: wer das Projekt sehen darf,
// sieht seine PRs. Alles andere, auch ein unbekannter PR, ist sql.ErrNoRows.
func (a *ProjectAccess) PullRequestView(project string, n int64) (PullRequestInfo, error) {
	project = scopeNormalize(project)
	if project == "" || n <= 0 {
		a.Filtered()
		return PullRequestInfo{}, sql.ErrNoRows
	}
	if err := a.Check(project, ResProject, ActRead, Object{}); err != nil {
		return PullRequestInfo{}, sql.ErrNoRows
	}
	return a.st.PullRequestOf(project, n)
}

// RequestRead liest einen Auftrag so, wie der Server ihn an GET /api/requests/{id}
// ausliefert: mit der Sichtprüfung auf den Auftrag und der Ansicht für den
// Betrachter. Was es nicht gibt und was er nicht lesen darf, ist beides
// sql.ErrNoRows.
func (a *ProjectAccess) RequestRead(id int64) (requestdomain.Detail, error) {
	ref, err := a.st.RequestRef(RefRequest, id)
	if err != nil {
		a.Filtered()
		return requestdomain.Detail{}, sql.ErrNoRows
	}
	obj, visible := a.RequestObject(ref)
	if !visible || a.Check(ref.Project, ResRequest, ActRead, obj) != nil {
		return requestdomain.Detail{}, sql.ErrNoRows
	}
	detail, err := a.st.RequestByID(id)
	if err != nil {
		return requestdomain.Detail{}, sql.ErrNoRows
	}
	return a.RequestDetailView(detail), nil
}

// KnowledgeRead liest einen Wissenseintrag wie GET /api/knowledge/{id}. Was es
// nicht gibt und was er nicht lesen darf, ist beides sql.ErrNoRows.
func (a *ProjectAccess) KnowledgeRead(id int64) (Knowledge, error) {
	k, err := a.st.KnowledgeByID(id)
	if err != nil {
		a.Filtered()
		return Knowledge{}, sql.ErrNoRows
	}
	if err := a.CheckKnowledge(k, ActRead); err != nil {
		if errors.Is(err, ErrAccessNotFound) || errors.Is(err, ErrAccessForbidden) {
			return Knowledge{}, sql.ErrNoRows
		}
		return Knowledge{}, err
	}
	return a.KnowledgeView(k), nil
}
