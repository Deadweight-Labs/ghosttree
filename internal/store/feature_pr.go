package store

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/feature"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Die Pflege im Ablauf (REQ-612, Kriterium 3): Ändert ein PR Pfade in den
// Bereichen eines Features, steht in der Checkliste des PRs der Punkt „Feature X:
// Beschreibung angepasst?“. Welche Pfade ein PR ändert, weiß der Server nicht (er
// hat das Repo nicht und fragt GitHub nicht); der Client meldet sie aus git diff.
// Ein Punkt gilt als erledigt, wenn eine Fassung des Features den PR trägt (ihr
// Anker hat seine Nummer, oder sie nennt ihn) oder jemand die Beschreibung für
// diesen PR als unverändert richtig bestätigt hat.

const (
	// maxCheckPaths: so viele geänderte Pfade nennt ein Punkt je Feature.
	maxCheckPaths = 5
	// maxChecklistFeatures begrenzt, wie viele Features eine Checkliste prüft.
	maxChecklistFeatures = 500
)

// ReportPullPaths merkt sich die Pfade, die ein PR ändert, und ersetzt die zuvor
// gemeldeten. Ungültige Pfade (.., absolut, .git) werden verworfen und gezählt.
// Schreibrecht an den Features des Projekts (ActWork): die Pfade bestimmen, welche
// Punkte eine Checkliste zeigt.
func (a *ProjectAccess) ReportPullPaths(project string, pr int64, paths []string, by string) (stored, dropped int, err error) {
	project = scope.NormalizeRemote(strings.TrimSpace(project))
	if project == "" || pr <= 0 {
		return 0, 0, requestdomain.NewRuleError("pr_invalid", "name the project and a positive pull request number", "pass project and pr", nil)
	}
	if err := a.Check(project, ResFeature, ActWork, Object{}); err != nil {
		return 0, 0, err
	}
	if len(paths) > feature.MaxPRPaths {
		return 0, 0, requestdomain.NewRuleError("paths_too_many", fmt.Sprintf("at most %d paths per pull request", feature.MaxPRPaths), "report the directories that changed, or the first files of a very large change", nil)
	}
	seen := map[string]bool{}
	var clean []string
	for _, p := range paths {
		c, ok := feature.CleanPath(p)
		if !ok {
			dropped++
			continue
		}
		if !seen[c] {
			seen[c] = true
			clean = append(clean, c)
		}
	}
	sort.Strings(clean)
	if err := a.st.commitPullPaths(project, pr, clean, by); err != nil {
		return 0, dropped, err
	}
	return len(clean), dropped, nil
}

func (s *Store) commitPullPaths(project string, pr int64, paths []string, by string) error {
	if s.writer != nil {
		return queueWrite(s, []any{project, pr, paths, by}, func(d *Store, p []any) error {
			return d.commitPullPaths(p[0].(string), p[1].(int64), p[2].([]string), p[3].(string))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM feature_pr_paths WHERE project=? AND pr=?`, project, pr); err != nil {
		return err
	}
	ts := now()
	for _, p := range paths {
		if _, err := tx.Exec(`INSERT INTO feature_pr_paths(project,pr,path,reported_by,reported_at) VALUES(?,?,?,?,?)`, project, pr, p, by, ts); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) pullPaths(project string, pr int64) ([]string, error) {
	if s.reader != nil {
		return s.reader.pullPaths(project, pr)
	}
	rows, err := s.db.Query(`SELECT path FROM feature_pr_paths WHERE project=? AND pr=? ORDER BY path`, project, pr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// prFeatureState liefert je Feature die Fassungen, die den PR tragen, und die
// Bestätigungen „unverändert“.
func (s *Store) prFeatureState(ids []int64, pr int64) (revs map[int64][]int, notes map[int64]string, err error) {
	if s.reader != nil {
		return s.reader.prFeatureState(ids, pr)
	}
	revs, notes = map[int64][]int{}, map[int64]string{}
	err = chunked(ids, func(part []int64) error {
		args := append(factIDArgs(part), pr, pr)
		rows, err := s.db.Query(`SELECT v.feature_id, v.revision FROM feature_versions v LEFT JOIN git_anchors a ON a.id=v.anchor_id
			WHERE v.feature_id IN (`+placeholders(len(part))+`) AND (v.pr=? OR a.pr=?) ORDER BY v.revision`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var fid int64
			var rev int
			if err := rows.Scan(&fid, &rev); err != nil {
				rows.Close()
				return err
			}
			revs[fid] = append(revs[fid], rev)
		}
		rows.Close()
		cr, err := s.db.Query(`SELECT feature_id, note FROM feature_pr_checks WHERE pr=? AND feature_id IN (`+placeholders(len(part))+`)`, append([]any{pr}, factIDArgs(part)...)...)
		if err != nil {
			return err
		}
		defer cr.Close()
		for cr.Next() {
			var fid int64
			var note string
			if err := cr.Scan(&fid, &note); err != nil {
				return err
			}
			notes[fid] = note
		}
		return cr.Err()
	})
	return revs, notes, err
}

// FeatureChecklist liefert die Punkte „Feature X: Beschreibung angepasst?“ für
// einen PR: die aktiven Features, deren Bereiche einen gemeldeten Pfad des PRs
// enthalten. Offene Punkte stehen zuerst. Ohne gemeldete Pfade ist die Liste leer.
func (a *ProjectAccess) FeatureChecklist(project string, pr int64) ([]requestdomain.FeatureCheck, error) {
	project = scope.NormalizeRemote(strings.TrimSpace(project))
	if project == "" || pr <= 0 {
		return nil, requestdomain.NewRuleError("pr_invalid", "name the project and a positive pull request number", "pass project and pr", nil)
	}
	if err := a.GateList(project, ResFeature, true); err != nil {
		return nil, err
	}
	if !a.Allow(project, ResFeature, ActRead, Object{}) {
		return []requestdomain.FeatureCheck{}, nil
	}
	return a.featureChecks(project, pr)
}

func (a *ProjectAccess) featureChecks(project string, pr int64) ([]requestdomain.FeatureCheck, error) {
	paths, err := a.st.pullPaths(project, pr)
	if err != nil {
		return nil, err
	}
	out := []requestdomain.FeatureCheck{}
	if len(paths) == 0 {
		return out, nil
	}
	list, err := a.st.searchFeatures(feature.Filter{Project: project, Limit: maxChecklistFeatures})
	if err != nil {
		return nil, err
	}
	var hit []feature.Feature
	var ids []int64
	matched := map[int64][]string{}
	for _, f := range list {
		if !a.CanDeliverFeature(f) {
			continue
		}
		for _, p := range paths {
			if f.Content.Covers(p) {
				matched[f.ID] = append(matched[f.ID], p)
			}
		}
		if len(matched[f.ID]) > 0 {
			hit = append(hit, f)
			ids = append(ids, f.ID)
		}
	}
	revs, notes, err := a.st.prFeatureState(ids, pr)
	if err != nil {
		return nil, err
	}
	for _, f := range hit {
		c := requestdomain.FeatureCheck{Feature: f.ID, Name: f.Name, Text: requestdomain.FeatureCheckText(f.ID, f.Name), State: requestdomain.FeatureCheckOpen,
			Revision: f.Revision, PathCount: len(matched[f.ID]), Paths: matched[f.ID]}
		if len(c.Paths) > maxCheckPaths {
			c.Paths = c.Paths[:maxCheckPaths]
		}
		switch {
		case len(revs[f.ID]) > 0:
			c.State, c.PRRevisions = requestdomain.FeatureCheckUpdated, revs[f.ID]
		default:
			if note, ok := notes[f.ID]; ok {
				c.State, c.Note = requestdomain.FeatureCheckUnchanged, note
			}
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		oi, oj := out[i].State == requestdomain.FeatureCheckOpen, out[j].State == requestdomain.FeatureCheckOpen
		if oi != oj {
			return oi
		}
		return out[i].Feature < out[j].Feature
	})
	return out, nil
}

// packageFeatureChecks füllt die Feature-Punkte eines Pakets mit PR. Ein Fehler
// kostet nur die Auskunft.
func (a *ProjectAccess) packageFeatureChecks(project string, pr int64) []requestdomain.FeatureCheck {
	if pr <= 0 || !a.Allow(project, ResFeature, ActRead, Object{}) {
		return nil
	}
	out, err := a.featureChecks(project, pr)
	if err != nil {
		return nil
	}
	return out
}

// ConfirmFeatureUnchanged hält fest, dass die Beschreibung eines Features für
// diesen PR angesehen und richtig befunden wurde, ohne neue Fassung: ein Umbau,
// der kein Verhalten ändert. Die Notiz sagt, warum sie noch stimmt.
func (a *ProjectAccess) ConfirmFeatureUnchanged(id, pr int64, note, person string) error {
	f, err := a.featureFor(id, ActWork)
	if err != nil {
		return err
	}
	if pr <= 0 {
		return requestdomain.NewRuleError("pr_invalid", "pr must be a positive pull request number", "pass the PR the check is for", nil)
	}
	note, err = idea.CleanOneLine(note, "note", "the note", feature.MaxNote, true)
	if err != nil {
		return err
	}
	return a.st.commitFeatureCheck(f.ID, pr, note, person)
}

func (s *Store) commitFeatureCheck(id, pr int64, note, person string) error {
	if s.writer != nil {
		return queueWrite(s, []any{id, pr, note, person}, func(d *Store, p []any) error {
			return d.commitFeatureCheck(p[0].(int64), p[1].(int64), p[2].(string), p[3].(string))
		})
	}
	_, err := s.db.Exec(`INSERT INTO feature_pr_checks(feature_id,pr,note,person,created_at) VALUES(?,?,?,?,?)
		ON CONFLICT(feature_id,pr) DO UPDATE SET note=excluded.note, person=excluded.person, created_at=excluded.created_at`, id, pr, note, person, now())
	return err
}
