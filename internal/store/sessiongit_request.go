package store

import (
	"regexp"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
)

// Sessions eines Requests (IDEA-31 P4, DEC-10 A): nichts davon steht in
// request_work. Die Verbindung wird beim Lesen aus zwei Wegen berechnet, damit
// eine bessere Zuordnung (matched, Nachlauf des PR-Abgleichs) ohne Nachpflege
// stimmt und abgeleitete Zeilen nicht als liegengebliebene Arbeit erscheinen.

// Herkunft einer abgeleiteten Verbindung zu einem Request.
const (
	// RequestOriginPR: die Session hat den PR bedient oder einen seiner Commits
	// geschrieben, und der PR ist an ein Paket gebunden, in dem der Request steht.
	RequestOriginPR = "pr"
	// RequestOriginEvidence: die Session schrieb einen Commit, den ein Nachweis des
	// Requests nennt.
	RequestOriginEvidence = "evidence"
)

const (
	// maxRequestPulls und maxRequestEvidenceShas begrenzen die Arbeit je Anfrage:
	// ein Request mit hunderten Nachweisen darf die Seite nicht langsam machen.
	maxRequestPulls       = 20
	maxRequestEvidenceRef = 60
	maxRequestEvidenceSha = 30
)

// evidenceShaWord findet in einem Nachweistext Wörter, die eine Commit-SHA sein
// können. Der Text ist frei geschrieben ("PRs #173 (b0cd1b9), #174 (e33198b)"),
// darum zählt nur ein Wort aus sieben bis vierzig Hexzeichen mit mindestens einer
// Ziffer: "defaced" ist ein Wort, keine SHA.
var evidenceShaWord = regexp.MustCompile(`(?i)\b[0-9a-f]{7,40}\b`)

// shasInEvidence liest die SHAs aus dem Text eines Commit-Nachweises.
func shasInEvidence(ref string) []string {
	var out []string
	for _, w := range evidenceShaWord.FindAllString(ref, -1) {
		w = gitanchor.Normalize(w)
		if strings.ContainsAny(w, "0123456789") && gitanchor.ValidPrefix(w) {
			out = append(out, w)
		}
	}
	return out
}

// SessionsForRequest nennt die Sessions, die über einen PR im Paket des Requests
// oder über einen Commit-Nachweis zu ihm gehören, ohne Sichtprüfung. Die Sessions
// aus request_work (start_work) stehen nicht darin: das ist der ausdrückliche Weg,
// und die Seite zeigt sie schon. Aufrufer mit einem Betrachter gehen über
// ProjectAccess.RequestSessions.
func (s *Store) SessionsForRequest(requestID int64) ([]GitSessionLink, error) {
	if s.reader != nil {
		return s.reader.SessionsForRequest(requestID)
	}
	ref, err := s.RequestRef(RefRequest, requestID)
	if err != nil {
		return nil, err
	}
	project := normalizeProject(ref.Project)
	if project == "" {
		return nil, nil
	}
	set := linkSet{}

	// Der Weg über die PRs im Paket. Ein Paket eines anderen Projekts zählt nicht.
	prs, err := s.pullNumbers(`SELECT DISTINCT p.pr FROM work_package_requests m JOIN work_packages p ON p.id=m.package_id
		WHERE m.request_id=? AND p.project=? AND p.pr IS NOT NULL AND p.pr > 0 ORDER BY p.pr LIMIT ?`, requestID, ref.Project, maxRequestPulls)
	if err != nil {
		return nil, err
	}
	for _, n := range prs {
		part := linkSet{}
		if err := s.sessionsForPull(project, n, "", part); err != nil {
			return nil, err
		}
		for _, l := range part.list() {
			l.Origins = []string{RequestOriginPR}
			l.PR = n
			set.add(l)
		}
	}

	// Der Weg über Commit-Nachweise.
	rows, err := s.db.Query(`SELECT ref FROM request_evidence WHERE request_id=? AND kind='commit' ORDER BY id LIMIT ?`, requestID, maxRequestEvidenceRef)
	if err != nil {
		return nil, err
	}
	var shas []string
	seen := map[string]bool{}
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			rows.Close()
			return nil, err
		}
		for _, sha := range shasInEvidence(text) {
			if !seen[sha] && len(shas) < maxRequestEvidenceSha {
				seen[sha] = true
				shas = append(shas, sha)
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for _, sha := range shas {
		links, err := s.SessionsForCommit(project, sha)
		if err != nil {
			return nil, err
		}
		for _, l := range links {
			l.Origins = []string{RequestOriginEvidence}
			set.add(l)
		}
	}
	if len(set) == 0 {
		return nil, nil
	}

	// Was start_work schon festhält, bleibt dort.
	rows, err = s.db.Query(`SELECT DISTINCT session_id FROM request_work WHERE request_id=?`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		delete(set, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return set.list(), nil
}

// RequestSessions nennt die Sessions eines Requests in dem Umfang, den der
// Betrachter sehen darf (DEC-11 A, wie an Commit und PR). Einen Request, den es
// nicht gibt oder den er nicht lesen darf, beantwortet sie gleich.
func (a *ProjectAccess) RequestSessions(requestID int64) (GitSessionLinks, error) {
	ref, err := a.st.RequestRef(RefRequest, requestID)
	if err != nil || !requestReadable(a, ref) {
		a.Filtered()
		return GitSessionLinks{}, requestNotInProject(requestID)
	}
	links, err := a.st.SessionsForRequest(requestID)
	if err != nil {
		return GitSessionLinks{}, err
	}
	return shapeGitLinks(a.st, normalizeProject(ref.Project), links, a.CanSeeTranscript, a.CanSeeSessionMeta)
}
