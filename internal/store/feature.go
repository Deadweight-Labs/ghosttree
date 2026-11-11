package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/feature"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Features (REQ-612): Objekt mit Fassungen. Rechte siehe matrixAllows (ResFeature):
// lesen ab Gast, anlegen und eine Fassung schreiben ab member, umbenennen und
// einstellen ab lead. Ein Feature, das es nicht gibt, und eines in einem
// verborgenen Projekt antworten gleich.

func featureNotFound(id int64) error {
	return requestdomain.NewRuleError("feature_not_found", fmt.Sprintf("no feature FEAT-%d", id), "search the features of the project to find its number", nil)
}

// FeatureActorOf nimmt den Schreiber der Idee-Werkstatt in die Form des Features.
func FeatureActorOf(a idea.Actor) feature.Actor {
	return feature.Actor{AccountID: a.AccountID, Person: a.Person, SessionRef: a.SessionRef, Agent: a.Agent, GitAnchor: a.GitAnchor, Origin: a.Origin}
}

// FeatureActorFor bildet den Schreiber aus dem Principal, wie IdeaActorFor: ein
// Mensch ist nur eine Websitzung, jeder Token-Aufruf gilt als Agent.
func FeatureActorFor(p Principal, sessionRef string) feature.Actor {
	return FeatureActorOf(IdeaActorFor(p, sessionRef))
}

const featureCols = `f.id,f.project,f.state,f.reason,f.person,f.author_id,f.created_at,f.updated_at,
	v.revision,v.name,v.summary,v.behavior,v.limits,v.cli,v.mcp,v.api,v.ui,v.areas,
	(SELECT COUNT(*) FROM feature_versions x WHERE x.feature_id=f.id),v.person,
	COALESCE((SELECT group_concat(p, char(31)) FROM (SELECT DISTINCT x.person AS p FROM feature_versions x WHERE x.feature_id=f.id)),'')`

const featureFrom = ` FROM features f JOIN feature_versions v ON v.feature_id=f.id AND v.revision=(SELECT MAX(revision) FROM feature_versions WHERE feature_id=f.id)`

func scanFeatures(rows *sql.Rows) ([]feature.Feature, error) {
	var out []feature.Feature
	for rows.Next() {
		var f feature.Feature
		var areas, persons string
		if err := rows.Scan(&f.ID, &f.Project, &f.State, &f.Reason, &f.Person, &f.AuthorID, &f.CreatedAt, &f.UpdatedAt,
			&f.Revision, &f.Name, &f.Summary, &f.Behavior, &f.Limits, &f.CLI, &f.MCP, &f.API, &f.UI, &areas, &f.Versions, &f.VersionPerson, &persons); err != nil {
			return nil, err
		}
		if persons != "" {
			f.VersionPersons = strings.Split(persons, "\x1f")
		}
		f.Areas = decodeAreas(areas)
		out = append(out, f)
	}
	return out, rows.Err()
}

func decodeAreas(s string) []string {
	var a []string
	if json.Unmarshal([]byte(s), &a) != nil || a == nil {
		return []string{}
	}
	return a
}

// featureByID lädt ein Feature mit seiner jüngsten Fassung, ohne
// Zugriffsprüfung: der Aufrufer prüft. sql.ErrNoRows, wenn es das nicht gibt.
func (s *Store) featureByID(id int64) (feature.Feature, error) {
	if s.reader != nil {
		return s.reader.featureByID(id)
	}
	rows, err := s.db.Query(`SELECT `+featureCols+featureFrom+` WHERE f.id=?`, id)
	if err != nil {
		return feature.Feature{}, err
	}
	list, err := scanFeatures(rows)
	rows.Close()
	if err != nil {
		return feature.Feature{}, err
	}
	if len(list) == 0 {
		return feature.Feature{}, sql.ErrNoRows
	}
	if err := s.attachFeatureMeta(list); err != nil {
		return feature.Feature{}, err
	}
	return list[0], nil
}

// attachFeatureMeta hängt Anker und Kontext der jüngsten Fassung und die
// rohen Verweise (ohne Titel; die füllt der Betrachter) an.
func (s *Store) attachFeatureMeta(list []feature.Feature) error {
	idx := map[int64]int{}
	ids := make([]int64, len(list))
	for i, f := range list {
		idx[f.ID] = i
		ids[i] = f.ID
	}
	if err := chunked(ids, func(part []int64) error {
		// Zeilen-Id der jüngsten Fassung je Feature.
		rows, err := s.db.Query(`SELECT v.id, v.feature_id FROM feature_versions v WHERE v.revision=(SELECT MAX(revision) FROM feature_versions WHERE feature_id=v.feature_id)
			AND v.feature_id IN (`+placeholders(len(part))+`)`, factIDArgs(part)...)
		if err != nil {
			return err
		}
		rowOf := map[int64]int64{}
		var rowIDs []int64
		for rows.Next() {
			var vid, fid int64
			if err := rows.Scan(&vid, &fid); err != nil {
				rows.Close()
				return err
			}
			rowOf[vid] = fid
			rowIDs = append(rowIDs, vid)
		}
		rows.Close()
		anchors, err := s.anchorsFor("feature_versions", rowIDs)
		if err != nil {
			return err
		}
		origins, err := s.originsFor("feature_versions", rowIDs)
		if err != nil {
			return err
		}
		for vid, fid := range rowOf {
			list[idx[fid]].GitAnchor = anchors[vid]
			list[idx[fid]].OriginContext = origins[vid]
		}
		return nil
	}); err != nil {
		return err
	}
	return chunked(ids, func(part []int64) error {
		rows, err := s.db.Query(`SELECT feature_id, kind, ref_id, rel FROM feature_links WHERE feature_id IN (`+placeholders(len(part))+`) ORDER BY added_at, ref_id`, factIDArgs(part)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var fid, ref int64
			var kind, rel string
			if err := rows.Scan(&fid, &kind, &ref, &rel); err != nil {
				return err
			}
			r := feature.Ref{Kind: kind, ID: ref}
			f := &list[idx[fid]]
			if rel == feature.RelOrigin {
				f.Origin = append(f.Origin, r)
			} else {
				f.Affects = append(f.Affects, r)
			}
		}
		return rows.Err()
	})
}

// FeatureView liefert ein Feature für den Betrachter. Verweise auf Ideen und
// Issues, die er nicht lesen darf, fehlen; die übrigen tragen Titel und Stand.
func (a *ProjectAccess) FeatureView(id int64) (feature.Feature, error) {
	f, err := a.featureFor(id, ActRead)
	if err != nil {
		return feature.Feature{}, err
	}
	a.fillFeatureRefs(&f)
	return f, nil
}

// featureFor lädt ein Feature für eine Aktion.
func (a *ProjectAccess) featureFor(id int64, act Action) (feature.Feature, error) {
	f, err := a.st.featureByID(id)
	if err != nil {
		a.Filtered()
		return feature.Feature{}, featureNotFound(id)
	}
	if err := a.Check(f.Project, ResFeature, act, Object{}); err != nil {
		if err == ErrAccessNotFound {
			return feature.Feature{}, featureNotFound(id)
		}
		return feature.Feature{}, err
	}
	return f, nil
}

// fillFeatureRefs ergänzt Titel und Stand der Verweise und streicht, was der
// Betrachter nicht lesen darf. Advanced sagt, ob eine Fassung den Verweis trägt.
func (a *ProjectAccess) fillFeatureRefs(f *feature.Feature) {
	advIdeas, advReqs, _ := a.st.advancedRefs(f.ID)
	fill := func(in []feature.Ref) []feature.Ref {
		var out []feature.Ref
		for _, r := range in {
			switch r.Kind {
			case feature.KindIdea:
				i, err := a.st.IdeaByID(r.ID)
				if err != nil || i.Project != f.Project || !a.CanSeeIdea(i) {
					continue
				}
				r.Title, r.State, r.Advanced = i.Title, i.Phase, advIdeas[r.ID]
			case feature.KindRequest:
				ref, err := a.st.RequestRef(RefRequest, r.ID)
				if err != nil || ref.Project != f.Project || !requestReadable(a, ref) {
					continue
				}
				r.Title = a.st.requestTitle(r.ID)
				r.State, _ = a.st.requestStateOf(r.ID)
				r.Advanced = advReqs[r.ID]
			}
			out = append(out, r)
		}
		return out
	}
	f.Origin = fill(f.Origin)
	f.Affects = fill(f.Affects)
}

// advancedRefs sagt, welche Ideen und Issues eine Fassung des Features tragen.
func (s *Store) advancedRefs(featureID int64) (ideas, requests map[int64]bool, err error) {
	if s.reader != nil {
		return s.reader.advancedRefs(featureID)
	}
	ideas, requests = map[int64]bool{}, map[int64]bool{}
	rows, err := s.db.Query(`SELECT idea_id FROM feature_versions WHERE feature_id=? AND idea_id>0`, featureID)
	if err != nil {
		return ideas, requests, err
	}
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return ideas, requests, err
		}
		ideas[n] = true
	}
	rows.Close()
	rows, err = s.db.Query(`SELECT r.request_id FROM feature_version_requests r JOIN feature_versions v ON v.id=r.version_id WHERE v.feature_id=?`, featureID)
	if err != nil {
		return ideas, requests, err
	}
	defer rows.Close()
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			return ideas, requests, err
		}
		requests[n] = true
	}
	return ideas, requests, rows.Err()
}

// ---- Fassungen lesen

const featureVersionCols = `v.id,v.revision,v.name,v.summary,v.behavior,v.limits,v.cli,v.mcp,v.api,v.ui,v.areas,v.digest,v.message,v.idea_id,v.pr,v.person,v.by_agent,v.created_at`

func scanFeatureVersion(row interface{ Scan(...any) error }, rowID *int64) (feature.Version, error) {
	var v feature.Version
	var areas string
	var agent int
	err := row.Scan(rowID, &v.Revision, &v.Name, &v.Summary, &v.Behavior, &v.Limits, &v.CLI, &v.MCP, &v.API, &v.UI, &areas, &v.Digest, &v.Message, &v.Idea, &v.PR, &v.Person, &agent, &v.CreatedAt)
	v.ByAgent = agent != 0
	v.Areas = decodeAreas(areas)
	return v, err
}

// featureVersions liefert die Fassungen, älteste zuerst, mit Anker und Kontext
// und den Issues, die sie ausgelöst haben.
func (s *Store) featureVersions(featureID int64, revision int) ([]feature.Version, error) {
	if s.reader != nil {
		return s.reader.featureVersions(featureID, revision)
	}
	q := `SELECT ` + featureVersionCols + ` FROM feature_versions v WHERE v.feature_id=?`
	args := []any{featureID}
	if revision > 0 {
		q += ` AND v.revision=?`
		args = append(args, revision)
	}
	rows, err := s.db.Query(q+` ORDER BY v.revision`, args...)
	if err != nil {
		return nil, err
	}
	var out []feature.Version
	var rowIDs []int64
	rowOf := map[int64]int{}
	for rows.Next() {
		var rid int64
		v, err := scanFeatureVersion(rows, &rid)
		if err != nil {
			rows.Close()
			return nil, err
		}
		rowOf[rid] = len(out)
		rowIDs = append(rowIDs, rid)
		out = append(out, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(out) == 0 {
		return out, err
	}
	anchors, err := s.anchorsFor("feature_versions", rowIDs)
	if err != nil {
		return nil, err
	}
	origins, err := s.originsFor("feature_versions", rowIDs)
	if err != nil {
		return nil, err
	}
	for rid, i := range rowOf {
		out[i].GitAnchor = anchors[rid]
		out[i].OriginContext = origins[rid]
	}
	err = chunked(rowIDs, func(part []int64) error {
		rr, err := s.db.Query(`SELECT version_id, request_id FROM feature_version_requests WHERE version_id IN (`+placeholders(len(part))+`) ORDER BY request_id`, factIDArgs(part)...)
		if err != nil {
			return err
		}
		defer rr.Close()
		for rr.Next() {
			var vid, req int64
			if err := rr.Scan(&vid, &req); err != nil {
				return err
			}
			out[rowOf[vid]].Requests = append(out[rowOf[vid]].Requests, req)
		}
		return rr.Err()
	})
	return out, err
}

// FeatureVersions liefert die Fassungen eines Features. withContent false lässt
// die langen Felder weg (nur Name, Kurzbeschreibung, Verweise, Anker): eine
// Historie ist eine Liste, keine Seite.
func (a *ProjectAccess) FeatureVersions(id int64, withContent bool) ([]feature.Version, error) {
	if _, err := a.featureFor(id, ActRead); err != nil {
		return nil, err
	}
	list, err := a.st.featureVersions(id, 0)
	if err != nil {
		return nil, err
	}
	if !withContent {
		for i := range list {
			list[i].Behavior, list[i].Limits, list[i].CLI, list[i].MCP, list[i].API, list[i].UI = "", "", "", "", "", ""
		}
	}
	return a.visibleVersionRefs(list), nil
}

// visibleVersionRefs streicht aus den Fassungen Idee und Issues, die der
// Betrachter nicht lesen darf: eine Fassung verrät keine fremde Nummer.
func (a *ProjectAccess) visibleVersionRefs(list []feature.Version) []feature.Version {
	for i := range list {
		if list[i].Idea > 0 {
			if idv, err := a.st.IdeaByID(list[i].Idea); err != nil || !a.CanSeeIdea(idv) {
				list[i].Idea = 0
			}
		}
		var keep []int64
		for _, r := range list[i].Requests {
			if ref, err := a.st.RequestRef(RefRequest, r); err == nil && requestReadable(a, ref) {
				keep = append(keep, r)
			}
		}
		list[i].Requests = keep
	}
	return list
}

// FeatureVersion liefert eine Fassung mit Inhalt (revision 0 = jüngste).
func (a *ProjectAccess) FeatureVersion(id int64, revision int) (feature.Version, error) {
	if _, err := a.featureFor(id, ActRead); err != nil {
		return feature.Version{}, err
	}
	list, err := a.st.featureVersions(id, revision)
	if err != nil {
		return feature.Version{}, err
	}
	if len(list) == 0 {
		return feature.Version{}, requestdomain.NewRuleError("version_not_found", "no such version", "list the versions of the feature", nil)
	}
	list = a.visibleVersionRefs(list)
	return list[len(list)-1], nil
}

// ---- Anlegen

// CreateFeatureInput legt ein Feature an.
type CreateFeatureInput struct {
	Project string
	Content feature.Patch
	Message string
	// Origin: Ideen und Issues, aus denen das Feature entstand.
	Ideas    []int64
	Requests []int64
	// FromIdea überführt eine umgesetzte Idee in ein Feature: die Idee und ihre
	// Issues werden Herkunft, die erste Fassung trägt die Idee.
	FromIdea int64
	PR       int64
	Actor    feature.Actor
}

// featureWrite ist, was der Store je Schreibvorgang braucht.
type featureWrite struct {
	Project  string
	Content  feature.Content
	Message  string
	Idea     int64
	Requests []int64
	PR       int64
	Actor    feature.Actor
	Base     int
	// Links sind die Verweise, die das Anlegen mitbringt (Art, Nummer, Beziehung).
	Links []featureLink
}

type featureLink struct {
	Kind string
	ID   int64
	Rel  string
}

func (a *ProjectAccess) cleanFeatureRefs(project string, ideas, requests []int64) ([]int64, []int64, error) {
	ideas, err := feature.CleanRefs(ideas, "ideas")
	if err != nil {
		return nil, nil, err
	}
	requests, err = feature.CleanRefs(requests, "requests")
	if err != nil {
		return nil, nil, err
	}
	for _, n := range ideas {
		i, err := a.st.IdeaByID(n)
		if err != nil || i.Project != project || !a.CanSeeIdea(i) {
			return nil, nil, requestdomain.NewRuleError("idea_not_found", fmt.Sprintf("no idea IDEA-%d", n), "name an idea of the same project", nil)
		}
	}
	for _, n := range requests {
		ref, err := a.st.RequestRef(RefRequest, n)
		if err != nil || ref.Project != project || !requestReadable(a, ref) {
			return nil, nil, requestdomain.NewRuleError("request_not_found", fmt.Sprintf("no request REQ-%d", n), "name a request of the same project", nil)
		}
	}
	return ideas, requests, nil
}

// ideaImplemented sagt, ob eine Idee umgesetzt ist: Phase done, oder eine Idee,
// die nie ausgearbeitet wurde und deren Issues alle zu sind.
func (a *ProjectAccess) ideaImplemented(i idea.Idea) bool {
	if i.Phase == idea.PhaseDone {
		return true
	}
	if i.Phase != idea.PhaseRaw || i.State != idea.Accepted || len(i.Requests) == 0 {
		return false
	}
	for _, r := range i.Requests {
		if st, err := a.st.requestStateOf(r); err != nil || st == "open" {
			return false
		}
	}
	return true
}

// CreateFeature legt ein Feature an. Der Name ist je Projekt eindeutig. Aus einer
// umgesetzten Idee wird ein Feature mit FromIdea; betrifft die Idee schon ein
// Feature, wird keines neu angelegt, sondern dessen Beschreibung fortgeschrieben.
// featureFields lists the texts of a feature patch for the secret check.
func featureFields(p feature.Patch, message string) []string {
	var f []string
	for _, s := range []struct {
		name string
		text *string
	}{{"name", p.Name}, {"summary", p.Summary}, {"behavior", p.Behavior}, {"limits", p.Limits}, {"cli", p.CLI}, {"mcp", p.MCP}, {"api", p.API}, {"ui", p.UI}} {
		if s.text != nil {
			f = append(f, s.name, *s.text)
		}
	}
	return append(f, "message", message)
}

func (a *ProjectAccess) CreateFeature(in CreateFeatureInput) (feature.Feature, error) {
	if err := refuseSecrets(featureFields(in.Content, in.Message)...); err != nil {
		return feature.Feature{}, err
	}
	in.Project = scope.NormalizeRemote(strings.TrimSpace(in.Project))
	if in.Project == "" {
		return feature.Feature{}, requestdomain.NewRuleError("project_required", "a feature belongs to a project", "name the project", nil)
	}
	if err := a.Check(in.Project, ResFeature, ActCreate, Object{Own: true}); err != nil {
		return feature.Feature{}, err
	}
	content, err := feature.Content{Areas: []string{}}.Apply(in.Content)
	if err != nil {
		return feature.Feature{}, err
	}
	if content.Name == "" {
		return feature.Feature{}, requestdomain.NewRuleError("name_required", "a feature needs a name", "name what the product does, in a few words", nil)
	}
	msg, err := idea.CleanOneLine(in.Message, "message", "the note", feature.MaxNote, false)
	if err != nil {
		return feature.Feature{}, err
	}
	ideas, requests, err := a.cleanFeatureRefs(in.Project, in.Ideas, in.Requests)
	if err != nil {
		return feature.Feature{}, err
	}
	w := featureWrite{Project: in.Project, Content: content, Message: msg, PR: in.PR, Actor: in.Actor}
	if in.PR < 0 {
		return feature.Feature{}, requestdomain.NewRuleError("pr_invalid", "pr must be a positive pull request number", "leave it out when there is none", nil)
	}
	if in.FromIdea > 0 {
		i, err := a.ideaFor(in.FromIdea, ActWork)
		if err != nil {
			return feature.Feature{}, err
		}
		if i.Project != in.Project {
			return feature.Feature{}, requestdomain.NewRuleError("idea_not_found", fmt.Sprintf("no idea IDEA-%d", in.FromIdea), "name an idea of the same project", nil)
		}
		if !a.ideaImplemented(i) {
			return feature.Feature{}, requestdomain.NewRuleError("idea_not_implemented", fmt.Sprintf("%s is not implemented yet (phase %s)", i.HumanID(), i.Phase),
				"a feature describes what is built; finish the idea (phase done) or its issues first", nil)
		}
		if aff, err := a.st.featuresAffectedBy(i.ID); err == nil && len(aff) > 0 {
			return feature.Feature{}, requestdomain.NewRuleError("feature_exists", fmt.Sprintf("%s changes FEAT-%d: its description is carried forward, not a new feature", i.HumanID(), aff[0]),
				fmt.Sprintf("write a new version of FEAT-%d with idea=%d (feature action=update)", aff[0], i.ID), []string{fmt.Sprintf("FEAT-%d", aff[0])})
		}
		w.Idea = i.ID
		ideas = append(ideas, i.ID)
		requests = append(requests, i.Requests...)
	}
	for _, n := range feature.DedupInts(ideas) {
		w.Links = append(w.Links, featureLink{feature.KindIdea, n, feature.RelOrigin})
	}
	w.Requests = feature.DedupInts(requests)
	for _, n := range w.Requests {
		w.Links = append(w.Links, featureLink{feature.KindRequest, n, feature.RelOrigin})
	}
	id, err := a.st.commitFeatureCreate(w)
	if err != nil {
		return feature.Feature{}, err
	}
	return a.FeatureView(id)
}

func featureNameTaken(name string) error {
	return requestdomain.NewRuleError("feature_name_taken", fmt.Sprintf("a feature called %q exists in this project", name), "carry its description forward instead of creating a second one, or choose another name", nil)
}

func isUniqueName(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// insertFeatureVersionTx hängt eine Fassung an und liefert ihre Zeilen-Id.
func insertFeatureVersionTx(tx *sql.Tx, featureID int64, revision int, w featureWrite) (int64, error) {
	areas, err := json.Marshal(w.Content.Areas)
	if err != nil {
		return 0, err
	}
	anchor, origin, err := insertWriteMetaTx(tx, w.Actor.GitAnchor, w.Actor.Origin)
	if err != nil {
		return 0, err
	}
	pr := w.PR
	if pr == 0 && w.Actor.GitAnchor != nil && w.Actor.GitAnchor.Branch != "" {
		// Läuft der Schreiber auf dem Branch eines offenen PRs, trägt die Fassung
		// ihn gleich, auch wenn die Anker erst später ihre Nummer bekommen.
		_ = tx.QueryRow(`SELECT number FROM git_pull_requests WHERE project=? AND head_ref=? AND state='open' ORDER BY number DESC LIMIT 1`,
			w.Project, w.Actor.GitAnchor.Branch).Scan(&pr)
	}
	res, err := tx.Exec(`INSERT INTO feature_versions(feature_id,revision,name,summary,behavior,limits,cli,mcp,api,ui,areas,digest,message,idea_id,pr,person,by_agent,created_at,anchor_id,origin_id)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		featureID, revision, w.Content.Name, w.Content.Summary, w.Content.Behavior, w.Content.Limits, w.Content.CLI, w.Content.MCP, w.Content.API, w.Content.UI,
		string(areas), featureDigest(w.Content), w.Message, w.Idea, pr, w.Actor.Person, boolInt(w.Actor.Agent), now(), anchor, origin)
	if err != nil {
		return 0, err
	}
	vid, _ := res.LastInsertId()
	for _, r := range w.Requests {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO feature_version_requests(version_id,request_id) VALUES(?,?)`, vid, r); err != nil {
			return 0, err
		}
	}
	return vid, nil
}

// featureDigest ist der Hash des Inhalts: gleicher Inhalt, gleiche Prüfsumme.
func featureDigest(c feature.Content) string {
	raw, _ := json.Marshal(c)
	return Digest(string(raw))
}

func (s *Store) commitFeatureCreate(w featureWrite) (int64, error) {
	if s.writer != nil {
		return queueValue(s, []any{w}, func(d *Store, p []any) (int64, error) { return d.commitFeatureCreate(p[0].(featureWrite)) })
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ts := now()
	account := authorForWriteTx(tx, w.Actor.AccountID, w.Actor.Person, "agent", "")
	res, err := tx.Exec(`INSERT INTO features(project,name,state,person,author_id,session_ref,created_at,updated_at) VALUES(?,?,'active',?,?,?,?,?)`,
		w.Project, w.Content.Name, w.Actor.Person, account, w.Actor.SessionRef, ts, ts)
	if err != nil {
		if isUniqueName(err) {
			return 0, featureNameTaken(w.Content.Name)
		}
		return 0, err
	}
	id, _ := res.LastInsertId()
	if _, err := insertFeatureVersionTx(tx, id, 1, w); err != nil {
		return 0, err
	}
	for _, l := range w.Links {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO feature_links(feature_id,kind,ref_id,rel,added_by,added_at) VALUES(?,?,?,?,?,?)`, id, l.Kind, l.ID, l.Rel, w.Actor.Person, ts); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

// ---- Fortschreiben

// UpdateFeatureInput schreibt eine neue Fassung.
type UpdateFeatureInput struct {
	Patch   feature.Patch
	Message string
	// Base ist die Fassung, auf der der Schreiber aufbaut (0 = egal).
	Base int
	// Idea, Requests und PR sind, was die Fassung ausgelöst hat.
	Idea     int64
	Requests []int64
	PR       int64
	Actor    feature.Actor
}

// UpdateFeature schreibt eine neue Fassung: die jüngste mit dem Patch darüber.
// Ändert der Patch nichts und nennt er nichts Neues (Idee, Issues, PR), entsteht
// keine Fassung (unchanged). Nennt er eine Idee, die das Feature betrifft, ist
// deren Beschreibung damit fortgeschrieben.
func (a *ProjectAccess) UpdateFeature(id int64, in UpdateFeatureInput) (feature.Version, bool, error) {
	if err := refuseSecrets(featureFields(in.Patch, in.Message)...); err != nil {
		return feature.Version{}, false, err
	}
	f, err := a.featureFor(id, ActWork)
	if err != nil {
		return feature.Version{}, false, err
	}
	if f.State == feature.Retired {
		return feature.Version{}, false, requestdomain.NewRuleError("feature_retired", fmt.Sprintf("%s is retired", f.HumanID()), "take it up again first (feature action=state)", nil)
	}
	if in.PR < 0 {
		return feature.Version{}, false, requestdomain.NewRuleError("pr_invalid", "pr must be a positive pull request number", "leave it out when there is none", nil)
	}
	msg, err := idea.CleanOneLine(in.Message, "message", "the note", feature.MaxNote, false)
	if err != nil {
		return feature.Version{}, false, err
	}
	var ideas []int64
	if in.Idea > 0 {
		ideas = []int64{in.Idea}
	}
	ideas, requests, err := a.cleanFeatureRefs(f.Project, ideas, in.Requests)
	if err != nil {
		return feature.Version{}, false, err
	}
	next, err := f.Content.Apply(in.Patch)
	if err != nil {
		return feature.Version{}, false, err
	}
	w := featureWrite{Project: f.Project, Content: next, Message: msg, Requests: requests, PR: in.PR, Actor: in.Actor, Base: in.Base}
	if len(ideas) > 0 {
		w.Idea = ideas[0]
		// Wer eine Idee nennt, die das Feature ändert, schreibt sie fort; eine
		// Idee, die noch nicht verknüpft war, wird mit dem Schreiben verknüpft.
		w.Links = append(w.Links, featureLink{feature.KindIdea, ideas[0], feature.RelAffects})
	}
	for _, r := range requests {
		w.Links = append(w.Links, featureLink{feature.KindRequest, r, feature.RelAffects})
	}
	rev, unchanged, err := a.st.commitFeatureVersion(id, w)
	if err != nil {
		return feature.Version{}, false, err
	}
	list, err := a.st.featureVersions(id, rev)
	if err != nil || len(list) == 0 {
		return feature.Version{}, unchanged, err
	}
	return a.visibleVersionRefs(list)[0], unchanged, nil
}

func (s *Store) commitFeatureVersion(id int64, w featureWrite) (int, bool, error) {
	if s.writer != nil {
		type result struct {
			Rev       int
			Unchanged bool
		}
		r, err := queueValue(s, []any{id, w}, func(d *Store, p []any) (result, error) {
			rev, u, err := d.commitFeatureVersion(p[0].(int64), p[1].(featureWrite))
			return result{rev, u}, err
		})
		return r.Rev, r.Unchanged, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	head, headDigest := 0, ""
	if err := tx.QueryRow(`SELECT revision, digest FROM feature_versions WHERE feature_id=? ORDER BY revision DESC LIMIT 1`, id).Scan(&head, &headDigest); err != nil {
		return 0, false, err
	}
	if w.Base > 0 && w.Base != head {
		return 0, false, requestdomain.NewRuleError("version_changed", fmt.Sprintf("the feature is at revision %d, not %d", head, w.Base), "read the current version and apply your change to it", nil)
	}
	ts := now()
	for _, l := range w.Links {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO feature_links(feature_id,kind,ref_id,rel,added_by,added_at) VALUES(?,?,?,?,?,?)`, id, l.Kind, l.ID, l.Rel, w.Actor.Person, ts); err != nil {
			return 0, false, err
		}
	}
	// Gleicher Inhalt und nichts, was die Fassung auslöst: keine neue Fassung.
	if featureDigest(w.Content) == headDigest && w.Idea == 0 && len(w.Requests) == 0 && w.PR == 0 {
		return head, true, tx.Commit()
	}
	// Inhalt gleich, aber die Fassung nennt eine Idee, Issues oder einen PR: die
	// Beschreibung wurde dafür angesehen und für richtig befunden. Sie bleibt als
	// Fassung stehen, denn die Historie soll zeigen, was den Stand ausgelöst hat.
	if _, err := insertFeatureVersionTx(tx, id, head+1, w); err != nil {
		return 0, false, err
	}
	if _, err := tx.Exec(`UPDATE features SET name=?, updated_at=? WHERE id=?`, w.Content.Name, ts, id); err != nil {
		if isUniqueName(err) {
			return 0, false, featureNameTaken(w.Content.Name)
		}
		return 0, false, err
	}
	return head + 1, false, tx.Commit()
}

// SetFeatureState stellt ein Feature ein (mit Grund) oder nimmt es wieder auf.
func (a *ProjectAccess) SetFeatureState(id int64, to, reason string) (feature.Feature, error) {
	if err := refuseSecrets("reason", reason); err != nil {
		return feature.Feature{}, err
	}
	f, err := a.featureFor(id, ActEdit)
	if err != nil {
		return feature.Feature{}, err
	}
	if !feature.ValidState(to) {
		return feature.Feature{}, requestdomain.NewRuleError("invalid_state", fmt.Sprintf("unknown feature state %q", to), "use active or retired", nil)
	}
	if f.State == to {
		return feature.Feature{}, requestdomain.NewRuleError("invalid_transition", fmt.Sprintf("%s is already %s", f.HumanID(), to), "nothing to do", nil)
	}
	if to == feature.Retired {
		if reason, err = feature.CleanReason(reason); err != nil {
			return feature.Feature{}, err
		}
	} else {
		reason = ""
	}
	if err := a.st.commitFeatureState(id, f.State, to, reason); err != nil {
		return feature.Feature{}, err
	}
	return a.FeatureView(id)
}

func (s *Store) commitFeatureState(id int64, from, to, reason string) error {
	if s.writer != nil {
		return queueWrite(s, []any{id, from, to, reason}, func(d *Store, p []any) error {
			return d.commitFeatureState(p[0].(int64), p[1].(string), p[2].(string), p[3].(string))
		})
	}
	res, err := s.db.Exec(`UPDATE features SET state=?, reason=?, updated_at=? WHERE id=? AND state=?`, to, reason, now(), id, from)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return requestdomain.NewRuleError("feature_changed", "the feature changed in the meantime", "read it again", nil)
	}
	return nil
}

// ---- Verweise

// LinkFeature verknüpft ein Feature mit einer Idee oder einem Issue. rel ist origin
// (daraus entstanden) oder affects (das ändert es). Schreibrecht am Feature
// (ActWork), Leserecht am Ziel, beide im selben Projekt. Eine Idee, die ein Feature
// betrifft („betrifft FEAT-n“), trägt es danach in ihrer Ansicht.
func (a *ProjectAccess) LinkFeature(id int64, kind string, ref int64, rel string, by string) (feature.Feature, error) {
	f, err := a.featureFor(id, ActWork)
	if err != nil {
		return feature.Feature{}, err
	}
	if rel != feature.RelOrigin && rel != feature.RelAffects {
		return feature.Feature{}, requestdomain.NewRuleError("invalid_relation", fmt.Sprintf("unknown relation %q", rel), "use origin or affects", nil)
	}
	var ideas, requests []int64
	switch kind {
	case feature.KindIdea:
		ideas = []int64{ref}
	case feature.KindRequest:
		requests = []int64{ref}
	default:
		return feature.Feature{}, requestdomain.NewRuleError("invalid_kind", fmt.Sprintf("cannot link a %q", kind), "use idea or request", nil)
	}
	if _, _, err := a.cleanFeatureRefs(f.Project, ideas, requests); err != nil {
		return feature.Feature{}, err
	}
	if err := a.st.commitFeatureLink(id, kind, ref, rel, by, true); err != nil {
		return feature.Feature{}, err
	}
	return a.FeatureView(id)
}

// UnlinkFeature löst einen Verweis. Eine Fassung, die ihn trägt, bleibt
// unverändert stehen: die Historie ändert sich nicht.
func (a *ProjectAccess) UnlinkFeature(id int64, kind string, ref int64, rel string) (feature.Feature, error) {
	if _, err := a.featureFor(id, ActWork); err != nil {
		return feature.Feature{}, err
	}
	if err := a.st.commitFeatureLink(id, kind, ref, rel, "", false); err != nil {
		return feature.Feature{}, err
	}
	return a.FeatureView(id)
}

func (s *Store) commitFeatureLink(id int64, kind string, ref int64, rel, by string, add bool) error {
	if s.writer != nil {
		return queueWrite(s, []any{id, kind, ref, rel, by, add}, func(d *Store, p []any) error {
			return d.commitFeatureLink(p[0].(int64), p[1].(string), p[2].(int64), p[3].(string), p[4].(string), p[5].(bool))
		})
	}
	if !add {
		_, err := s.db.Exec(`DELETE FROM feature_links WHERE feature_id=? AND kind=? AND ref_id=? AND rel=?`, id, kind, ref, rel)
		return err
	}
	_, err := s.db.Exec(`INSERT OR IGNORE INTO feature_links(feature_id,kind,ref_id,rel,added_by,added_at) VALUES(?,?,?,?,?,?)`, id, kind, ref, rel, by, now())
	return err
}

// featuresAffectedBy liefert die Features, die eine Idee betrifft.
func (s *Store) featuresAffectedBy(ideaID int64) ([]int64, error) {
	if s.reader != nil {
		return s.reader.featuresAffectedBy(ideaID)
	}
	rows, err := s.db.Query(`SELECT l.feature_id FROM feature_links l JOIN features f ON f.id=l.feature_id WHERE l.kind='idea' AND l.ref_id=? AND l.rel='affects' ORDER BY l.feature_id`, ideaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ---- Suchen

// SearchFeatures liefert die Features des Projekts, die der Betrachter lesen darf,
// nach Nummer. State "" heißt aktive, "all" alle.
func (a *ProjectAccess) SearchFeatures(f feature.Filter) ([]feature.Feature, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	if f.Limit > 500 {
		f.Limit = 500
	}
	f.Project = scope.NormalizeRemote(f.Project)
	if f.State != "" && f.State != "all" && !feature.ValidState(f.State) {
		return nil, requestdomain.NewRuleError("invalid_state", fmt.Sprintf("unknown feature state %q", f.State), "use active, retired or all", nil)
	}
	if err := a.GateList(f.Project, ResFeature, true); err != nil {
		return nil, err
	}
	list, err := a.st.searchFeatures(f)
	if err != nil {
		return nil, err
	}
	out := make([]feature.Feature, 0, len(list))
	for _, x := range list {
		if a.Allow(x.Project, ResFeature, ActRead, Object{}) {
			a.fillFeatureRefs(&x)
			out = append(out, x)
		}
	}
	if len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

func (s *Store) searchFeatures(f feature.Filter) ([]feature.Feature, error) {
	if s.reader != nil {
		return s.reader.searchFeatures(f)
	}
	q := `SELECT ` + featureCols + featureFrom + ` WHERE f.project=?`
	args := []any{f.Project}
	switch f.State {
	case "":
		q += ` AND f.state='active'`
	case "all":
	default:
		q += ` AND f.state=?`
		args = append(args, f.State)
	}
	for _, w := range strings.Fields(strings.ToLower(f.Query)) {
		w = strings.Trim(w, `"`)
		if w == "" {
			continue
		}
		q += ` AND (instr(lower(v.name), ?) > 0 OR instr(lower(v.summary), ?) > 0 OR instr(lower(v.behavior), ?) > 0 OR instr(lower(v.limits), ?) > 0 OR instr(lower(v.areas), ?) > 0)`
		args = append(args, w, w, w, w, w)
	}
	q += ` ORDER BY f.id`
	if f.Path == "" {
		q += ` LIMIT ?`
		args = append(args, f.Limit*4)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	list, err := scanFeatures(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if f.Path != "" {
		kept := list[:0]
		for _, x := range list {
			if x.Content.Covers(f.Path) {
				kept = append(kept, x)
			}
		}
		list = kept
	}
	if len(list) == 0 {
		return list, nil
	}
	if err := s.attachFeatureMeta(list); err != nil {
		return nil, err
	}
	return list, nil
}
