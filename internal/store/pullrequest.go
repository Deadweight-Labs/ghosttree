package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func scopeNormalize(s string) string { return scope.NormalizeRemote(s) }

// pullRequestSchema (REQ-597, Kriterien 3 und 4): was GitHub über die PRs eines
// Projekts gemeldet hat. Eine Zeile je (Projekt, PR-Nummer). Sie ist die
// Zuordnung Branch zu PR, aus der die Anker ihre PR-Nummer bekommen, und der
// Merge-Stand, aus dem die Übernahme der Branch-Beschreibungen läuft. Titel und
// Branch-Namen sind Fremddaten und stehen nur bereinigt hier (cleanRef,
// cleanLine).
const pullRequestSchema = `
CREATE TABLE IF NOT EXISTS git_pull_requests(
  project TEXT NOT NULL,
  number INTEGER NOT NULL CHECK(number > 0),
  title TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL CHECK(state IN ('open','closed','merged')),
  head_ref TEXT NOT NULL,
  base_ref TEXT NOT NULL DEFAULT '',
  default_branch TEXT NOT NULL DEFAULT '',
  merge_sha TEXT NOT NULL DEFAULT '',
  opened_at TEXT NOT NULL, updated_at TEXT NOT NULL, merged_at TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(project, number));
CREATE INDEX IF NOT EXISTS git_pull_requests_head ON git_pull_requests(project, head_ref);
`

func ensurePullRequests(db *sql.DB) error {
	if _, err := db.Exec(pullRequestSchema); err != nil {
		return err
	}
	// author: der Login, mit dem GitHub den PR gemeldet hat (additiv; Altzeilen
	// bleiben leer, bis das nächste Ereignis oder das Nachholen ihn füllt).
	if err := ensureColumn(db, "git_pull_requests", "author", `TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("git_pull_requests.author: %w", err)
	}
	if _, err := db.Exec(pullCommitSchema); err != nil {
		return err
	}
	if _, err := db.Exec(commitMetaSchema); err != nil {
		return err
	}
	return nil
}

// PullRequestChange ist ein PR-Ereignis von GitHub (Webhook oder Nachholen
// über die API), in der Form, die der Store braucht. Alles darin ist
// Fremddaten und wird geprüft.
type PullRequestChange struct {
	InstallationID int64
	// Remote ist das Repo, in dem der PR liegt (Base), HeadRemote das des Heads.
	// Ein PR aus einem Fork trägt nichts bei: sein Head-Branch heißt zufällig
	// wie ein Branch hier, ist aber ein fremder.
	Remote, HeadRemote string
	Number             int64
	// Action ist die GitHub-Aktion (opened, reopened, synchronize, edited,
	// closed) oder "sync" beim Nachholen.
	Action        string
	Title         string
	Head, Base    string
	DefaultBranch string
	MergeSHA      string
	// Author ist der Login des PR-Autors (Fremddaten, wird geprüft).
	Author string
	// State: open, closed oder merged.
	State string
}

// PullRequestResult sagt, was ein Ereignis bewirkt hat.
type PullRequestResult struct {
	Project string
	// Applied: das Ereignis war gültig und gehört zu einem Projekt dieser
	// Installation. Unbekannt, fremd und ungültig sehen gleich aus (false).
	Applied       bool
	AnchorsTagged int
	PackageBound  bool
	Merged        bool
	// Knowledge sind die Einträge, deren Anker nach dem Merge diesen PR tragen.
	Knowledge        []int64
	GhostsPromoted   int
	GhostsKeptNewer  int
	RequestsNotified int
}

// insertAnchorWithPRTx legt einen Anker an, der die PR-Nummer schon trägt
// (Übernahme beim Merge). Sonst wie insertAnchorTx.
func insertAnchorWithPRTx(tx txExec, a *gitanchor.Anchor) (any, error) {
	dirty := 0
	if a.Dirty {
		dirty = 1
	}
	var pr any
	if a.PR != nil {
		pr = *a.PR
	}
	res, err := tx.Exec(`INSERT INTO git_anchors(commit_sha,branch,merge_base,dirty,pr,captured_at) VALUES(?,?,?,?,?,?)`,
		a.Commit, a.Branch, a.MergeBase, dirty, pr, now())
	if err != nil {
		return nil, err
	}
	return res.LastInsertId()
}

// projectAnchorIDs ist die Teilabfrage der Anker eines Projekts. Ein Anker
// trägt kein Projekt; er gehört dem Projekt der Zeilen, die auf ihn zeigen.
// Von den Koordinationsnachrichten zählen die des Projektraums; Direkt- und
// Gruppenräume gehören zu keinem Projekt.
const projectAnchorIDs = `
	SELECT anchor_id FROM knowledge WHERE project=?1 AND anchor_id IS NOT NULL
	UNION SELECT kv.anchor_id FROM knowledge_versions kv JOIN knowledge k ON k.id=kv.knowledge_id WHERE k.project=?1 AND kv.anchor_id IS NOT NULL
	UNION SELECT anchor_id FROM ghost_files WHERE project=?1 AND anchor_id IS NOT NULL
	UNION SELECT anchor_id FROM ghost_file_versions WHERE project=?1 AND anchor_id IS NOT NULL
	UNION SELECT anchor_id FROM ghost_branch_files WHERE project=?1 AND anchor_id IS NOT NULL
	UNION SELECT e.anchor_id FROM request_evidence e JOIN requests r ON r.id=e.request_id WHERE r.project=?1 AND e.anchor_id IS NOT NULL
	UNION SELECT a.anchor_id FROM request_activity a JOIN requests r ON r.id=a.request_id WHERE r.project=?1 AND a.anchor_id IS NOT NULL
	UNION SELECT pe.anchor_id FROM pain_events pe JOIN pains p ON p.id=pe.pain_id WHERE p.project=?1 AND pe.anchor_id IS NOT NULL
	UNION SELECT anchor_id FROM ideas WHERE project=?1 AND anchor_id IS NOT NULL
	UNION SELECT fv.anchor_id FROM feature_versions fv JOIN features f ON f.id=fv.feature_id WHERE f.project=?1 AND fv.anchor_id IS NOT NULL
	UNION SELECT anchor_id FROM coord_messages WHERE destination_kind='room' AND destination_id='project:'||?1 AND anchor_id IS NOT NULL`

// RecordPullRequest nimmt ein PR-Ereignis auf (Kriterien 3 und 4 von REQ-597):
//
//   - merkt sich den PR (Titel, Head, Base, Stand);
//   - solange er offen ist oder gemergt wurde, bekommen alle Anker dieses
//     Projekts, die auf dem Head-Branch entstanden sind und noch keine PR-Nummer
//     tragen, seine Nummer (die Spalte darf nur einmal von NULL auf einen Wert);
//     ein Anker, der schon zu einem früheren PR desselben Branch-Namens gehört,
//     bleibt dort;
//   - ein offenes Arbeitspaket des Head-Branches ohne PR wird an ihn gebunden;
//   - beim Merge (einmal) werden die Branch-Beschreibungen der Ghost-Dateien auf
//     den Base-Branch gehoben und an den Requests des Pakets steht eine Notiz,
//     welches Branch-Wissen hineingegangen ist.
//
// Ein Ereignis, das nicht zu einem Projekt dieser Installation passt, ist kein
// Fehler und bewirkt nichts (Applied false): von außen ist "unbekannt" von
// "fremd" nicht zu unterscheiden.
func (s *Store) RecordPullRequest(c PullRequestChange) (PullRequestResult, error) {
	if s.writer != nil {
		return queueValue(s, []any{c}, func(d *Store, p []any) (PullRequestResult, error) {
			return d.RecordPullRequest(p[0].(PullRequestChange))
		})
	}
	head, okHead := cleanRef(c.Head)
	base, okBase := cleanRef(c.Base)
	remote, rerr := normalizeRemote(c.Remote)
	if !okHead || !okBase || rerr != nil || c.Number <= 0 || c.InstallationID <= 0 {
		return PullRequestResult{}, nil
	}
	if hr, err := normalizeRemote(c.HeadRemote); err != nil || hr != remote {
		return PullRequestResult{}, nil
	}
	state := c.State
	if state != "open" && state != "closed" && state != "merged" {
		return PullRequestResult{}, nil
	}
	defaultBranch, _ := cleanRef(c.DefaultBranch)
	title := cleanLine(c.Title, 200)
	mergeSHA := ""
	if gitanchor.ValidPrefix(gitanchor.Normalize(c.MergeSHA)) {
		mergeSHA = gitanchor.Normalize(c.MergeSHA)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return PullRequestResult{}, err
	}
	defer tx.Rollback()
	// Die Installation muss zu einer Quelle gehören, deren Konto das Repo
	// besitzt, und das Projekt zur selben Organisation: sonst könnte eine
	// fremde Installation Anker eines fremden Projekts markieren.
	var srcOrg int64
	var kind, login string
	if err := tx.QueryRow(`SELECT org_id, kind, login FROM git_sources WHERE installation_id=?`, c.InstallationID).Scan(&srcOrg, &kind, &login); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PullRequestResult{}, nil
		}
		return PullRequestResult{}, err
	}
	host, _ := sourceHost(kind)
	if !strings.HasPrefix(remote, host+"/"+login+"/") {
		return PullRequestResult{}, nil
	}
	proj, known := projectTx(tx, remote)
	if !known || proj.OrgID != srcOrg {
		return PullRequestResult{}, nil
	}
	res := PullRequestResult{Project: remote, Applied: true}

	ts := now()
	var prevState string
	prevErr := tx.QueryRow(`SELECT state FROM git_pull_requests WHERE project=? AND number=?`, remote, c.Number).Scan(&prevState)
	if prevErr != nil && !errors.Is(prevErr, sql.ErrNoRows) {
		return res, prevErr
	}
	justMerged := state == "merged" && prevState != "merged"
	// Ein gemergter PR bleibt gemergt, auch wenn ein verspätetes Ereignis
	// (Wiederholung, "synchronize" nach dem Merge) etwas anderes meldet.
	if prevState == "merged" {
		state = "merged"
	}
	mergedAt := ""
	if state == "merged" {
		mergedAt = ts
	}
	author := cleanLogin(c.Author)
	if _, err := tx.Exec(`INSERT INTO git_pull_requests(project,number,title,state,head_ref,base_ref,default_branch,merge_sha,opened_at,updated_at,merged_at,author)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(project,number) DO UPDATE SET
			author=CASE WHEN excluded.author!='' THEN excluded.author ELSE git_pull_requests.author END,
			title=CASE WHEN excluded.title!='' THEN excluded.title ELSE git_pull_requests.title END,
			state=excluded.state, head_ref=excluded.head_ref, base_ref=excluded.base_ref,
			default_branch=CASE WHEN excluded.default_branch!='' THEN excluded.default_branch ELSE git_pull_requests.default_branch END,
			merge_sha=CASE WHEN excluded.merge_sha!='' THEN excluded.merge_sha ELSE git_pull_requests.merge_sha END,
			updated_at=excluded.updated_at,
			merged_at=CASE WHEN git_pull_requests.merged_at!='' THEN git_pull_requests.merged_at ELSE excluded.merged_at END`,
		remote, c.Number, title, state, head, base, defaultBranch, mergeSHA, ts, ts, mergedAt, author); err != nil {
		return res, err
	}
	if prevState == "merged" {
		return res, tx.Commit()
	}
	if state == "closed" {
		return res, tx.Commit()
	}

	r, err := tx.Exec(`UPDATE git_anchors SET pr=?2 WHERE pr IS NULL AND branch=?3 AND id IN (`+projectAnchorIDs+`)`, remote, c.Number, head)
	if err != nil {
		return res, err
	}
	n, _ := r.RowsAffected()
	res.AnchorsTagged = int(n)
	r, err = tx.Exec(`UPDATE work_packages SET pr=?, updated_at=? WHERE project=? AND branch=? AND state='open' AND pr IS NULL
		AND NOT EXISTS(SELECT 1 FROM work_packages WHERE project=? AND pr=?)`, c.Number, ts, remote, head, remote, c.Number)
	if err != nil {
		return res, err
	}
	n, _ = r.RowsAffected()
	res.PackageBound = n > 0
	if !justMerged {
		return res, tx.Commit()
	}

	res.Merged = true
	var storedDefault, storedSHA, storedTitle string
	if err := tx.QueryRow(`SELECT default_branch, merge_sha, title FROM git_pull_requests WHERE project=? AND number=?`, remote, c.Number).Scan(&storedDefault, &storedSHA, &storedTitle); err != nil {
		return res, err
	}
	res.GhostsPromoted, res.GhostsKeptNewer, err = promoteGhostBranchTx(tx, remote, head, base, storedDefault, c.Number, storedSHA, ts)
	if err != nil {
		return res, err
	}
	rows, err := tx.Query(`SELECT k.id FROM knowledge k JOIN git_anchors a ON a.id=k.anchor_id WHERE k.project=? AND a.pr=? ORDER BY k.id`, remote, c.Number)
	if err != nil {
		return res, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return res, err
		}
		res.Knowledge = append(res.Knowledge, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	rows, err = tx.Query(`SELECT m.request_id FROM work_package_requests m JOIN work_packages p ON p.id=m.package_id WHERE p.project=? AND p.pr=?`, remote, c.Number)
	if err != nil {
		return res, err
	}
	var reqs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return res, err
		}
		reqs = append(reqs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	note := mergeNote(c.Number, storedTitle, base, res.Knowledge, res.GhostsPromoted)
	for _, id := range reqs {
		if _, err := tx.Exec(`INSERT INTO request_activity(request_id,kind,person,data,created_at) VALUES(?,'pr.merged','github',?,?)`, id, note, ts); err != nil {
			return res, err
		}
		res.RequestsNotified++
	}
	return res, tx.Commit()
}

// mergeNote ist der einzeilige Vermerk am Request: welches Branch-Wissen mit dem
// PR hineingegangen ist.
func mergeNote(pr int64, title, base string, knowledge []int64, ghosts int) string {
	const maxListed = 50
	var ids []string
	for i, id := range knowledge {
		if i == maxListed {
			ids = append(ids, fmt.Sprintf("+%d more", len(knowledge)-maxListed))
			break
		}
		ids = append(ids, fmt.Sprintf("#%d", id))
	}
	know := "no branch knowledge"
	if len(ids) > 0 {
		know = "branch knowledge " + strings.Join(ids, ", ")
	}
	out := fmt.Sprintf("PR #%d", pr)
	if title != "" {
		out += " " + title
	}
	return out + fmt.Sprintf(" merged into %s: %s; %d ghost descriptions lifted", base, know, ghosts)
}

// PullRequestBrief ist, was zu einem PR gesagt wurde: das Branch-Wissen (nach
// dem Merge das, was mit hineingegangen ist) und die Ghost-Pfade mit PR-Anker.
type PullRequestBrief struct {
	Project   string        `json:"project"`
	PR        int64         `json:"pr"`
	Known     bool          `json:"known"`
	Title     string        `json:"title,omitempty"`
	State     string        `json:"state,omitempty"`
	Head      string        `json:"head,omitempty"`
	Base      string        `json:"base,omitempty"`
	Knowledge []PRKnowledge `json:"knowledge"`
	Ghosts    []string      `json:"ghost_paths"`
}

// PRKnowledge nennt einen Wissenseintrag, der auf einem Commit des PRs entstand.
type PRKnowledge struct {
	ID      int64  `json:"id"`
	Type    string `json:"type"`
	Title   string `json:"title"`
	Branch  string `json:"branch,omitempty"`
	Status  string `json:"status"`
	Person  string `json:"person,omitempty"`
	Machine string `json:"-"`
}

// PullRequestBriefOf liefert die Einträge mit dem Anker dieses PRs. Das Recht,
// einzelne Einträge zu sehen, prüft der Aufrufer (Server) je Eintrag.
func (s *Store) PullRequestBriefOf(project string, pr int64) (PullRequestBrief, error) {
	if s.reader != nil {
		return s.reader.PullRequestBriefOf(project, pr)
	}
	project = scopeNormalize(project)
	b := PullRequestBrief{Project: project, PR: pr, Knowledge: []PRKnowledge{}, Ghosts: []string{}}
	err := s.db.QueryRow(`SELECT title,state,head_ref,base_ref FROM git_pull_requests WHERE project=? AND number=?`, project, pr).Scan(&b.Title, &b.State, &b.Head, &b.Base)
	switch {
	case err == nil:
		b.Known = true
	case !errors.Is(err, sql.ErrNoRows):
		return b, err
	}
	rows, err := s.db.Query(`SELECT k.id,k.type,k.title,k.branch,k.status,k.person,k.machine FROM knowledge k JOIN git_anchors a ON a.id=k.anchor_id
		WHERE k.project=? AND a.pr=? ORDER BY k.id`, project, pr)
	if err != nil {
		return b, err
	}
	for rows.Next() {
		var k PRKnowledge
		if err := rows.Scan(&k.ID, &k.Type, &k.Title, &k.Branch, &k.Status, &k.Person, &k.Machine); err != nil {
			rows.Close()
			return b, err
		}
		b.Knowledge = append(b.Knowledge, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return b, err
	}
	rows, err = s.db.Query(`SELECT g.path FROM ghost_files g JOIN git_anchors a ON a.id=g.anchor_id WHERE g.project=? AND a.pr=?
		UNION SELECT g.path FROM ghost_branch_files g JOIN git_anchors a ON a.id=g.anchor_id WHERE g.project=? AND a.pr=? ORDER BY 1`, project, pr, project, pr)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return b, err
		}
		b.Ghosts = append(b.Ghosts, p)
	}
	return b, rows.Err()
}

// PullSyncTarget ist ein Projekt mit Installation und den Branches, deren
// Anker noch keine PR-Nummer tragen: das, was das Nachholen abfragen muss.
type PullSyncTarget struct {
	Project        string
	InstallationID int64
	Branches       []string
}

// PullSyncTargets liefert die Ziele des Nachholens von PR-Nummern für bestehende
// Branches. project leer heißt alle Projekte mit Installation.
func (s *Store) PullSyncTargets(project string) ([]PullSyncTarget, error) {
	if s.reader != nil {
		return s.reader.PullSyncTargets(project)
	}
	project = scopeNormalize(project)
	rows, err := s.db.Query(`SELECT p.remote, gs.installation_id FROM projects p
		JOIN git_sources gs ON gs.org_id=p.org_id AND gs.installation_id>0 AND p.remote LIKE 'github.com/' || gs.login || '/%'
		WHERE ?='' OR p.remote=? ORDER BY p.remote`, project, project)
	if err != nil {
		return nil, err
	}
	var out []PullSyncTarget
	for rows.Next() {
		var t PullSyncTarget
		if err := rows.Scan(&t.Project, &t.InstallationID); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		br, err := s.db.Query(`SELECT DISTINCT branch FROM git_anchors WHERE pr IS NULL AND branch!='' AND id IN (`+projectAnchorIDs+`) ORDER BY branch`, out[i].Project)
		if err != nil {
			return nil, err
		}
		for br.Next() {
			var b string
			if err := br.Scan(&b); err != nil {
				br.Close()
				return nil, err
			}
			out[i].Branches = append(out[i].Branches, b)
		}
		br.Close()
	}
	return out, nil
}
