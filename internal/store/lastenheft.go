package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// Der Stand des Lastenhefts eines PRs für den Check-Run (REQ-601 AC-3): nur Zahlen
// je Request, ohne Titel, Kriterientext und Nachweise. Der Check-Run steht auf
// GitHub und ist für jeden sichtbar, der den PR lesen darf; wer in ghosttree etwas
// nicht lesen darf, soll es dort nicht erfahren. Den Text zeigt die Seite des Pakets
// hinter der Anmeldung.

const lastenheftSchema = `
CREATE TABLE IF NOT EXISTS github_check_runs(
  project TEXT NOT NULL,
  pr INTEGER NOT NULL,
  head_sha TEXT NOT NULL,
  digest TEXT NOT NULL,
  published_at TEXT NOT NULL,
  PRIMARY KEY(project, pr)) WITHOUT ROWID;
`

// LastenheftMember ist ein Request im Paket mit den Zahlen seiner Kriterien.
type LastenheftMember struct {
	RequestID         int64
	Role, State       string
	Open, Met, Waived int
}

// Lastenheft ist der Stand des Pakets eines PRs.
type Lastenheft struct {
	Project   string
	PR        int64
	PackageID int64
	Members   []LastenheftMember
}

// Required zählt die Pflichtmitglieder und ihre Kriterien.
func (l Lastenheft) Required() (requests, open, met, waived int) {
	for _, m := range l.Members {
		if m.Role != requestdomain.RolePflicht {
			continue
		}
		requests++
		open += m.Open
		met += m.Met
		waived += m.Waived
	}
	return
}

// Conclusion: ohne Pflichtrequest gibt es nichts zu prüfen (neutral), mit offenen
// Pflichtkriterien ist es nicht grün (failure), sonst success.
func (l Lastenheft) Conclusion() string {
	requests, open, _, _ := l.Required()
	switch {
	case requests == 0:
		return "neutral"
	case open > 0:
		return "failure"
	}
	return "success"
}

// Digest ändert sich genau dann, wenn sich ein Wert ändert, den der Check-Run zeigt.
func (l Lastenheft) Digest() string {
	var b strings.Builder
	fmt.Fprintf(&b, "pkg:%d", l.PackageID)
	for _, m := range l.Members {
		fmt.Fprintf(&b, "|%d:%s:%s:%d/%d/%d", m.RequestID, m.Role, m.State, m.Open, m.Met, m.Waived)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// PullLastenheft liest das Paket eines PRs mit den Zahlen je Request, ohne Sichtprüfung:
// der Aufrufer ist der Server selbst und gibt nur Zahlen weiter. ok ist false, wenn
// der PR kein offenes Paket trägt.
func (s *Store) PullLastenheft(project string, pr int64) (Lastenheft, bool, error) {
	if s.reader != nil {
		return s.reader.PullLastenheft(project, pr)
	}
	project = scopeNormalize(project)
	var pkgID int64
	err := s.db.QueryRow(`SELECT id FROM work_packages WHERE project=? AND pr=? AND state='open'`, project, pr).Scan(&pkgID)
	if err == sql.ErrNoRows {
		return Lastenheft{}, false, nil
	}
	if err != nil {
		return Lastenheft{}, false, err
	}
	rows, err := s.db.Query(`SELECT m.request_id, m.role, r.state,
			COALESCE(SUM(c.state='open'),0), COALESCE(SUM(c.state='met'),0), COALESCE(SUM(c.state='waived'),0)
		FROM work_package_requests m JOIN requests r ON r.id=m.request_id
		LEFT JOIN request_criteria c ON c.request_id=m.request_id
		WHERE m.package_id=? GROUP BY m.request_id, m.role, r.state ORDER BY m.request_id`, pkgID)
	if err != nil {
		return Lastenheft{}, false, err
	}
	defer rows.Close()
	l := Lastenheft{Project: project, PR: pr, PackageID: pkgID}
	for rows.Next() {
		var m LastenheftMember
		if err := rows.Scan(&m.RequestID, &m.Role, &m.State, &m.Open, &m.Met, &m.Waived); err != nil {
			return Lastenheft{}, false, err
		}
		l.Members = append(l.Members, m)
	}
	return l, true, rows.Err()
}

// LastenheftTarget ist ein offener PR mit Paket, bekanntem Head-Commit und
// Installation: das, woran ein Check-Run hängen kann.
type LastenheftTarget struct {
	Project        string
	PR             int64
	HeadSHA        string
	InstallationID int64
}

// maxLastenheftTargets begrenzt einen Durchlauf.
const maxLastenheftTargets = 200

// LastenheftTargets liefert die offenen PRs mit Paket, jüngste Änderung zuerst.
func (s *Store) LastenheftTargets() ([]LastenheftTarget, error) {
	if s.reader != nil {
		return s.reader.LastenheftTargets()
	}
	rows, err := s.db.Query(`SELECT g.project, g.number, g.head_sha, gs.installation_id
		FROM git_pull_requests g
		JOIN work_packages w ON w.project=g.project AND w.pr=g.number AND w.state='open'
		JOIN projects p ON p.remote=g.project
		JOIN git_sources gs ON gs.org_id=p.org_id AND gs.installation_id>0 AND g.project LIKE 'github.com/' || gs.login || '/%'
		WHERE g.state='open' AND g.head_sha!='' ORDER BY g.updated_at DESC LIMIT ?`, maxLastenheftTargets)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LastenheftTarget
	for rows.Next() {
		var t LastenheftTarget
		if err := rows.Scan(&t.Project, &t.PR, &t.HeadSHA, &t.InstallationID); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// LastenheftPublished sagt, an welchen Commit und mit welchem Stand der Check-Run
// zuletzt geschrieben wurde; leer, wenn noch nie.
func (s *Store) LastenheftPublished(project string, pr int64) (headSHA, digest string, err error) {
	if s.reader != nil {
		return s.reader.LastenheftPublished(project, pr)
	}
	err = s.db.QueryRow(`SELECT head_sha, digest FROM github_check_runs WHERE project=? AND pr=?`, scopeNormalize(project), pr).Scan(&headSHA, &digest)
	if err == sql.ErrNoRows {
		return "", "", nil
	}
	return headSHA, digest, err
}

// MarkLastenheftPublished merkt sich, was geschrieben wurde.
func (s *Store) MarkLastenheftPublished(project string, pr int64, headSHA, digest string) error {
	if s.writer != nil {
		return queueWrite(s, []any{project, pr, headSHA, digest}, func(d *Store, p []any) error {
			return d.MarkLastenheftPublished(p[0].(string), p[1].(int64), p[2].(string), p[3].(string))
		})
	}
	_, err := s.db.Exec(`INSERT INTO github_check_runs(project,pr,head_sha,digest,published_at) VALUES(?,?,?,?,?)
		ON CONFLICT(project,pr) DO UPDATE SET head_sha=excluded.head_sha, digest=excluded.digest, published_at=excluded.published_at`,
		scopeNormalize(project), pr, headSHA, digest, now())
	return err
}
