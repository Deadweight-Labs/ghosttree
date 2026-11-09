package store

import (
	"database/sql"
	"strings"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// CountRequests zählt die Requests, die die Listenabfrage mit demselben Filter
// liefern würde: gleiche Bereichs- und Sichtbarkeitsklauseln (requestAttrWhere),
// ohne Suchtext, Cursor und Seitengröße.
func (s *Store) CountRequests(f requestdomain.SearchFilter) (int, error) {
	if s.reader != nil {
		return s.reader.CountRequests(f)
	}
	where, args := requestAttrWhere(f)
	q := `SELECT COUNT(*) FROM requests r`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	var n int
	err := s.db.QueryRow(q, args...).Scan(&n)
	return n, err
}

// CountRequests zählt für den Betrachter, mit der Semantik der Listenabfrage
// (GET /api/requests): ein Request ohne Branch gilt für jeden Branch, einer mit
// anderem Branch nicht, fremde Projekte und gehaltene Aufträge fehlen. state ""
// zählt alle, sonst nur diesen Zustand (open, done, dropped).
func (a *ProjectAccess) CountRequests(ax scope.Axes, state string) (int, error) {
	return a.st.CountRequests(a.RequestFilter(requestdomain.SearchFilter{Scope: ax, State: state}))
}

// packagesOfRequest liefert die Pakete, in denen ein Request steckt, ohne
// Zugriffsprüfung: der Aufrufer prüft den Request.
func (s *Store) packagesOfRequest(requestID int64) ([]requestdomain.PackageOfRequest, error) {
	if s.reader != nil {
		return s.reader.packagesOfRequest(requestID)
	}
	rows, err := s.db.Query(`SELECT p.id,p.project,p.branch,p.name,p.state,COALESCE(p.pr,0),p.created_by,p.created_at,p.updated_at,m.role
		FROM work_package_requests m JOIN work_packages p ON p.id=m.package_id
		WHERE m.request_id=? ORDER BY p.id`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []requestdomain.PackageOfRequest{}
	for rows.Next() {
		var p requestdomain.PackageOfRequest
		if err := rows.Scan(&p.ID, &p.Project, &p.Branch, &p.Name, &p.State, &p.PR, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt, &p.Role); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PackagesOfRequest nennt die Pakete eines Requests. Einen Request, den es nicht
// gibt oder den der Betrachter nicht lesen darf, beantwortet sie gleich
// (request_not_found), und ein Paket erscheint nur, wenn es im Projekt des
// Requests liegt.
func (a *ProjectAccess) PackagesOfRequest(requestID int64) ([]requestdomain.PackageOfRequest, error) {
	ref, err := a.st.RequestRef(RefRequest, requestID)
	if err != nil || !requestReadable(a, ref) {
		a.Filtered()
		return nil, requestNotInProject(requestID)
	}
	list, err := a.st.packagesOfRequest(requestID)
	if err != nil {
		return nil, err
	}
	out := list[:0]
	for _, p := range list {
		if p.Project == ref.Project {
			p.Package = a.PackageNameView(p.Package)
			out = append(out, p)
		}
	}
	return out, nil
}

// packagesOfProject liefert die Pakete eines Projekts samt Mitglieds-Ids.
func (s *Store) packagesOfProject(project, state string) ([]requestdomain.PackageDetail, error) {
	if s.reader != nil {
		return s.reader.packagesOfProject(project, state)
	}
	q := `SELECT id FROM work_packages WHERE project=?`
	args := []any{project}
	if state != "" {
		q += ` AND state=?`
		args = append(args, state)
	}
	q += ` ORDER BY id DESC LIMIT 500`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	out := make([]requestdomain.PackageDetail, 0, len(ids))
	for _, id := range ids {
		d, err := loadPackage(s.db, id)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func (s *Store) openCriteriaOf(requestID int64) int {
	if s.reader != nil {
		return s.reader.openCriteriaOf(requestID)
	}
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM request_criteria WHERE request_id=? AND state='open'`, requestID).Scan(&n)
	if err != nil && err != sql.ErrNoRows {
		return 0
	}
	return n
}

// PackagesOfProject ist die Paketliste eines Projekts, jüngstes zuerst. state ""
// liefert alle, sonst open oder closed. Der Betrachter braucht Leserecht an den
// Requests des Projekts; Requests, die er nicht lesen darf, zählen weder bei
// Requests noch bei offenen Kriterien mit, ohne Platzhalter.
func (a *ProjectAccess) PackagesOfProject(project, state string) ([]requestdomain.PackageSummary, error) {
	project = scope.NormalizeRemote(strings.TrimSpace(project))
	if state != "" && state != requestdomain.PackageOpen && state != requestdomain.PackageClosed {
		return nil, requestdomain.NewRuleError("invalid_package_state", "package state must be open or closed", "use open or closed, or leave it out", nil)
	}
	if err := a.Check(project, ResRequest, ActRead, Object{}); err != nil {
		return nil, err
	}
	list, err := a.st.packagesOfProject(project, state)
	if err != nil {
		return nil, err
	}
	out := make([]requestdomain.PackageSummary, 0, len(list))
	for _, d := range list {
		sum := requestdomain.PackageSummary{Package: a.PackageNameView(d.Package)}
		for _, m := range d.Members {
			ref, err := a.st.RequestRef(RefRequest, m.RequestID)
			if err != nil || !requestReadable(a, ref) {
				continue
			}
			sum.Requests++
			sum.OpenCriteria += a.st.openCriteriaOf(m.RequestID)
		}
		out = append(out, sum)
	}
	return out, nil
}
