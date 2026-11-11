package store

import (
	"database/sql"
	"fmt"
	"strings"
	"unicode"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

const ideaCols = `i.id,i.project,i.title,i.body,i.state,i.reason,i.person,i.created_at,i.updated_at,COALESCE(au.account_id,0),i.phase`

const ideaFrom = ` FROM ideas i LEFT JOIN idea_authors au ON au.idea_id=i.id`

func scanIdeas(rows *sql.Rows) ([]idea.Idea, error) {
	var out []idea.Idea
	for rows.Next() {
		var i idea.Idea
		var phase string
		if err := rows.Scan(&i.ID, &i.Project, &i.Title, &i.Body, &i.State, &i.Reason, &i.Person, &i.CreatedAt, &i.UpdatedAt, &i.AuthorID, &phase); err != nil {
			return nil, err
		}
		i.Phase = idea.EffectivePhase(i.State, phase)
		out = append(out, i)
	}
	return out, rows.Err()
}

func ideaNotFound(id int64) error {
	return requestdomain.NewRuleError("idea_not_found", fmt.Sprintf("no idea IDEA-%d", id), "search the ideas of the project to find its number", nil)
}

// IdeaByID lädt eine Idee mit ihren Requests, ihrem Anker und ihrem Kontext,
// ohne Zugriffsprüfung: der Aufrufer prüft. sql.ErrNoRows, wenn es sie nicht gibt.
func (s *Store) IdeaByID(id int64) (idea.Idea, error) {
	if s.reader != nil {
		return s.reader.IdeaByID(id)
	}
	rows, err := s.db.QueryContext(s.ctx(), `SELECT `+ideaCols+ideaFrom+` WHERE i.id=?`, id)
	if err != nil {
		return idea.Idea{}, err
	}
	list, err := scanIdeas(rows)
	rows.Close()
	if err != nil {
		return idea.Idea{}, err
	}
	if len(list) == 0 {
		return idea.Idea{}, sql.ErrNoRows
	}
	if err := s.attachIdeaMeta(list); err != nil {
		return idea.Idea{}, err
	}
	return list[0], nil
}

// attachIdeaMeta hängt Requests, Anker und Kontext an.
func (s *Store) attachIdeaMeta(list []idea.Idea) error {
	ids := make([]int64, len(list))
	idx := map[int64]int{}
	for i, v := range list {
		ids[i] = v.ID
		idx[v.ID] = i
	}
	anchors, err := s.anchorsFor("ideas", ids)
	if err != nil {
		return err
	}
	origins, err := s.originsFor("ideas", ids)
	if err != nil {
		return err
	}
	for id, i := range idx {
		list[i].GitAnchor = anchors[id]
		list[i].OriginContext = origins[id]
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	if err := s.attachIdeaWorkshopMeta(list, idx, args); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(s.ctx(), `SELECT idea_id, request_id FROM idea_requests WHERE idea_id IN (`+placeholders(len(ids))+`) ORDER BY request_id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var iid, rid int64
		if err := rows.Scan(&iid, &rid); err != nil {
			return err
		}
		list[idx[iid]].Requests = append(list[idx[iid]].Requests, rid)
	}
	return rows.Err()
}

func (a *ProjectAccess) ideaObject(i idea.Idea) Object {
	return Object{Own: a.valid && a.account != 0 && i.AuthorID == a.account, HeldAuthor: a.heldAuthor(HeldIdea, i.ID)}
}

// CanSeeIdea ist der Lesefilter für jede Liste von Ideen.
func (a *ProjectAccess) CanSeeIdea(i idea.Idea) bool {
	if a.heldBack(HeldIdea, i.ID, i.Project, ResIdea) {
		return false
	}
	return a.Allow(i.Project, ResIdea, ActRead, a.ideaObject(i))
}

// CheckIdea prüft eine Aktion auf eine vorhandene Idee.
func (a *ProjectAccess) CheckIdea(i idea.Idea, act Action) error {
	if a.heldBack(HeldIdea, i.ID, i.Project, ResIdea) {
		return ErrAccessNotFound
	}
	return a.Check(i.Project, ResIdea, act, a.ideaObject(i))
}

// IdeaView liefert eine Idee für den Betrachter samt Labels. Eine Idee, die es
// nicht gibt, und eine in einem verborgenen Projekt antworten gleich.
func (a *ProjectAccess) IdeaView(id int64) (idea.Idea, error) {
	i, err := a.st.IdeaByID(id)
	if err != nil {
		a.Filtered()
		return idea.Idea{}, ideaNotFound(id)
	}
	if err := a.CheckIdea(i, ActRead); err != nil {
		if err == ErrAccessNotFound {
			return idea.Idea{}, ideaNotFound(id)
		}
		return idea.Idea{}, err
	}
	list := []idea.Idea{i}
	a.attachIdeaLabels(list)
	a.attachIdeaPains(list)
	a.attachIdeaFeatures(list)
	if list[0].Phase == idea.PhaseBuilding || list[0].Phase == idea.PhaseDone {
		list[0].UncoveredRequirements = a.st.uncoveredCount(i.ID)
	}
	return list[0], nil
}

// attachIdeaPains hängt an jede Idee die Pains, die sie motivieren, soweit der
// Betrachter sie lesen darf (REQ-606).
func (a *ProjectAccess) attachIdeaPains(list []idea.Idea) {
	for n := range list {
		for _, p := range a.PainsFor("idea", list[n].ID) {
			list[n].Pains = append(list[n].Pains, idea.PainRef{ID: p.ID, Title: p.Title, State: p.State})
		}
	}
}

func (a *ProjectAccess) attachIdeaLabels(list []idea.Idea) {
	ids := make([]int64, len(list))
	for i, v := range list {
		ids[i] = v.ID
	}
	labels, err := a.st.LabelsFor("idea", ids)
	if err != nil {
		return
	}
	for i := range list {
		for _, l := range labels[list[i].ID] {
			list[i].Labels = append(list[i].Labels, idea.Label{Name: l.Name, Color: l.Color})
		}
	}
}

// CreateIdeaInput legt eine Idee an.
type CreateIdeaInput struct {
	Project string
	Title   string
	Body    string
	Actor   idea.Actor
	// AllowDuplicate legt die Idee auch an, wenn es eine ähnliche offene gibt.
	AllowDuplicate bool
}

// DuplicateIdeaError meldet ähnliche offene Ideen statt eine weitere anzulegen.
func duplicateIdeaError(similar []idea.Idea) error {
	var parts, ids []string
	for _, s := range similar {
		parts = append(parts, fmt.Sprintf("%s %s", s.HumanID(), s.Title))
		ids = append(ids, s.HumanID())
	}
	return requestdomain.NewRuleError("idea_duplicate", "there is already a similar open idea: "+strings.Join(parts, "; "),
		"use the existing idea, or record this one anyway with allow_duplicate", ids)
}

// CreateIdea hält einen Wunsch fest: Titel, optional ein kurzer Text. Es gibt
// keinen Typ, keine Priorität und keine Kriterien. Gibt es im Projekt schon eine
// ähnliche offene Idee, wird sie genannt und keine neue angelegt.
func (a *ProjectAccess) CreateIdea(in CreateIdeaInput) (idea.Idea, error) {
	in.Project = scope.NormalizeRemote(strings.TrimSpace(in.Project))
	title, err := idea.CleanTitle(in.Title)
	if err != nil {
		return idea.Idea{}, err
	}
	body, err := idea.CleanBody(in.Body)
	if err != nil {
		return idea.Idea{}, err
	}
	in.Title, in.Body = title, body
	if err := refuseSecrets("title", in.Title, "body", in.Body); err != nil {
		return idea.Idea{}, err
	}
	if in.Project == "" {
		return idea.Idea{}, requestdomain.NewRuleError("project_required", "an idea belongs to a project", "name the project", nil)
	}
	if err := a.Check(in.Project, ResIdea, ActCreate, Object{Own: true}); err != nil {
		return idea.Idea{}, err
	}
	if !in.AllowDuplicate {
		similar, err := a.similarIdeas(in.Project, in.Title)
		if err != nil {
			return idea.Idea{}, err
		}
		if len(similar) > 0 {
			return idea.Idea{}, duplicateIdeaError(similar)
		}
	}
	id, err := a.st.commitIdea(in)
	if err != nil {
		return idea.Idea{}, err
	}
	return a.IdeaView(id)
}

func titleWords(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(w) > 2 {
			out[w] = true
		}
	}
	return out
}

// similarIdeas nennt offene Ideen des Projekts, deren Titel (bis auf
// Groß-/Kleinschreibung und Satzzeichen) gleich ist oder zu mindestens vier
// Fünfteln dieselben Wörter hat. Nur Ideen, die der Schreiber lesen darf.
func (a *ProjectAccess) similarIdeas(project, title string) ([]idea.Idea, error) {
	list, err := a.st.openIdeasOf(project)
	if err != nil {
		return nil, err
	}
	mine := titleWords(title)
	var out []idea.Idea
	for _, c := range list {
		if !a.CanSeeIdea(c) {
			continue
		}
		theirs := titleWords(c.Title)
		if len(mine) == 0 || len(theirs) == 0 {
			if strings.EqualFold(strings.TrimSpace(c.Title), title) {
				out = append(out, c)
			}
			continue
		}
		common := 0
		for w := range mine {
			if theirs[w] {
				common++
			}
		}
		union := len(mine) + len(theirs) - common
		if union > 0 && common*5 >= union*4 {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *Store) openIdeasOf(project string) ([]idea.Idea, error) {
	if s.reader != nil {
		return s.reader.openIdeasOf(project)
	}
	rows, err := s.db.QueryContext(s.ctx(), `SELECT `+ideaCols+ideaFrom+` WHERE i.project=? AND i.state='open' ORDER BY i.id DESC LIMIT 500`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIdeas(rows)
}

func (s *Store) commitIdea(in CreateIdeaInput) (int64, error) {
	if s.writer != nil {
		return queueValue(s, []any{in}, func(d *Store, p []any) (int64, error) { return d.commitIdea(p[0].(CreateIdeaInput)) })
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ts := now()
	res, err := tx.Exec(`INSERT INTO ideas(project,title,body,state,reason,person,session_ref,created_at,updated_at,anchor_id,origin_id) VALUES(?,?,?,'open','',?,?,?,?,NULL,NULL)`,
		in.Project, in.Title, in.Body, in.Actor.Person, in.Actor.SessionRef, ts, ts)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	account := authorForWriteTx(tx, in.Actor.AccountID, in.Actor.Person, "agent", "")
	if _, err := tx.Exec(`INSERT INTO idea_authors(idea_id,account_id) VALUES(?,?)`, id, account); err != nil {
		return 0, err
	}
	anchorID, originID, err := insertWriteMetaTx(tx, in.Actor.GitAnchor, in.Actor.Origin)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE ideas SET anchor_id=?, origin_id=? WHERE id=?`, anchorID, originID, id); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// ideaReadFactor: the query of a search reads this many times the limit, so that
// the rights have rows to drop.
const ideaReadFactor = 4

// SearchIdeas liefert die Ideen des Projekts, die der Betrachter lesen darf, die
// jüngsten zuerst. State "" heißt offene, "all" alle.
func (a *ProjectAccess) SearchIdeas(f idea.Filter) ([]idea.Idea, error) {
	out, _, err := a.SearchIdeasCut(f)
	return out, err
}

// SearchIdeasCut is SearchIdeas that also says whether the read of the project was
// cut: the query reads four times the limit (the answer may be longer than the
// limit) and stops there, before the rights drop the rows the viewer may not
// read, so a short answer alone does not prove that nothing lies behind it. A
// caller that counts hits needs this.
func (a *ProjectAccess) SearchIdeasCut(f idea.Filter) ([]idea.Idea, bool, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 500 {
		f.Limit = 500
	}
	f.Project = scope.NormalizeRemote(f.Project)
	if f.State != "" && f.State != "all" && !idea.ValidState(f.State) {
		return nil, false, requestdomain.NewRuleError("invalid_state", fmt.Sprintf("unknown idea state %q", f.State), "use open, accepted, dismissed or all", nil)
	}
	list, err := a.st.searchIdeas(f)
	if err != nil {
		return nil, false, err
	}
	out := make([]idea.Idea, 0, len(list))
	for _, i := range list {
		if a.CanSeeIdea(i) {
			out = append(out, i)
		}
	}
	a.attachIdeaLabels(out)
	a.attachIdeaPains(out)
	return out, len(list) >= f.Limit*ideaReadFactor, nil
}

func (s *Store) searchIdeas(f idea.Filter) ([]idea.Idea, error) {
	if s.reader != nil {
		return s.reader.searchIdeas(f)
	}
	q := `SELECT ` + ideaCols + ideaFrom + ` WHERE i.project=?`
	args := []any{f.Project}
	switch f.State {
	case "":
		q += ` AND i.state='open'`
	case "all":
	default:
		q += ` AND i.state=?`
		args = append(args, f.State)
	}
	for _, w := range strings.Fields(strings.ToLower(f.Query)) {
		w = strings.Trim(w, `"`)
		if w == "" {
			continue
		}
		q += ` AND (instr(lower(i.title), ?) > 0 OR instr(lower(i.body), ?) > 0)`
		args = append(args, w, w)
	}
	q += ` ORDER BY i.id DESC LIMIT ?`
	args = append(args, f.Limit*ideaReadFactor)
	rows, err := s.db.QueryContext(s.ctx(), q, args...)
	if err != nil {
		return nil, err
	}
	list, err := scanIdeas(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return list, nil
	}
	if err := s.attachIdeaMeta(list); err != nil {
		return nil, err
	}
	return list, nil
}

// SetIdeaState nimmt eine Idee an, verwirft sie (mit Grund) oder öffnet eine
// verworfene wieder.
func (a *ProjectAccess) SetIdeaState(id int64, to, reason string) (idea.Idea, error) {
	if err := refuseSecrets("reason", reason); err != nil {
		return idea.Idea{}, err
	}
	i, err := a.st.IdeaByID(id)
	if err != nil {
		a.Filtered()
		return idea.Idea{}, ideaNotFound(id)
	}
	if err := a.CheckIdea(i, ActEdit); err != nil {
		if err == ErrAccessNotFound {
			return idea.Idea{}, ideaNotFound(id)
		}
		return idea.Idea{}, err
	}
	reason, err = idea.CleanReason(reason)
	if err != nil {
		return idea.Idea{}, err
	}
	if err := idea.ValidateTransition(i.State, to, reason); err != nil {
		return idea.Idea{}, err
	}
	if to == idea.Accepted && len(i.Requests) == 0 {
		return idea.Idea{}, requestdomain.NewRuleError("request_required", "an idea is accepted when a request came out of it", "promote the idea, or link the request it became", nil)
	}
	if to != idea.Dismissed {
		reason = ""
	}
	if err := a.st.commitIdeaState(id, i.State, to, reason); err != nil {
		return idea.Idea{}, err
	}
	return a.IdeaView(id)
}

func (s *Store) commitIdeaState(id int64, from, to, reason string) error {
	if s.writer != nil {
		return queueWrite(s, []any{id, from, to, reason}, func(d *Store, p []any) error {
			return d.commitIdeaState(p[0].(int64), p[1].(string), p[2].(string), p[3].(string))
		})
	}
	res, err := s.db.Exec(`UPDATE ideas SET state=?, reason=?, updated_at=? WHERE id=? AND state=?`, to, reason, now(), id, from)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return requestdomain.NewRuleError("idea_changed", "the idea changed in the meantime", "read it again", nil)
	}
	return nil
}

// LinkIdeaRequest verknüpft eine Idee mit einem Request, der aus ihr entstand,
// und setzt die Idee auf angenommen. Beide müssen im selben Projekt liegen, der
// Betrachter muss die Idee bearbeiten und am Request arbeiten dürfen. Der
// Aufrufer, der den Request kennt, prüft dessen Recht (der Handler tut es mit
// requestGate); hier gilt nur die Idee und das gemeinsame Projekt.
// checkIdeaLinkable: a request may be tied to an idea only by someone who may
// edit it, and only once it is approved. Shared by LinkIdeaRequest and the
// derived_from of a new request, so both ways ask the same.
func (a *ProjectAccess) checkIdeaLinkable(i idea.Idea) error {
	if err := a.CheckIdea(i, ActEdit); err != nil {
		return err
	}
	if i.Phase == idea.PhaseConcept || i.Phase == idea.PhaseDiscussion {
		return requestdomain.NewRuleError("not_approved", "this idea is being worked out and is not approved yet", "approve it first; only its owner or a maintainer can", nil)
	}
	return nil
}

func (a *ProjectAccess) LinkIdeaRequest(id, requestID int64, by string) (idea.Idea, error) {
	i, err := a.st.IdeaByID(id)
	if err != nil {
		a.Filtered()
		return idea.Idea{}, ideaNotFound(id)
	}
	if err := a.checkIdeaLinkable(i); err != nil {
		if err == ErrAccessNotFound {
			return idea.Idea{}, ideaNotFound(id)
		}
		return idea.Idea{}, err
	}
	ref, err := a.st.RequestRef(RefRequest, requestID)
	if err != nil || ref.Project != i.Project {
		return idea.Idea{}, requestdomain.NewRuleError("request_not_found", fmt.Sprintf("no request REQ-%d", requestID), "name a request of the same project", nil)
	}
	if err := a.st.commitIdeaLink(id, requestID, by); err != nil {
		return idea.Idea{}, err
	}
	return a.IdeaView(id)
}

func (s *Store) commitIdeaLink(id, requestID int64, by string) error {
	if s.writer != nil {
		return queueWrite(s, []any{id, requestID, by}, func(d *Store, p []any) error {
			return d.commitIdeaLink(p[0].(int64), p[1].(int64), p[2].(string))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := linkIdeaRequestTx(tx, id, requestID, by, now(), true); err != nil {
		return err
	}
	return tx.Commit()
}

// linkIdeaRequestTx hält fest, dass der Request aus der Idee entstand. Mit
// promote nimmt es die Idee an (angenommen, bei freigegebener Idee in Umsetzung);
// ohne bleibt es bei der Verknüpfung. Der Weg des Anlegens eines Requests aus
// einer Idee nutzt es mit dem Recht des Anlegenden; ResolveRequestLinks lässt nur
// durch, wer die Idee bearbeiten darf (checkIdeaLinkable), dort ist promote
// immer wahr.
func linkIdeaRequestTx(tx *sql.Tx, id, requestID int64, by, ts string, promote bool) error {
	if _, err := tx.Exec(`INSERT OR IGNORE INTO idea_requests(idea_id,request_id,linked_by,linked_at) VALUES(?,?,?,?)`, id, requestID, by, ts); err != nil {
		return err
	}
	if !promote {
		return nil
	}
	if _, err := tx.Exec(`UPDATE ideas SET state='accepted', reason='', updated_at=? WHERE id=? AND state IN ('open','dismissed')`, ts, id); err != nil {
		return err
	}
	// Aus einer freigegebenen Idee wird Arbeit: in Umsetzung. Eine nie ausgearbeitete
	// Idee, die direkt zum Issue wird, bleibt raw (Zustand angenommen): die Phasen
	// laufen nur über die Freigabe eines Menschen.
	_, err := tx.Exec(`UPDATE ideas SET phase='building' WHERE id=? AND phase='approved'`, id)
	return err
}

// ideasOfRequest liefert die Ideen, aus denen ein Request entstand.
func (s *Store) ideasOfRequest(requestID int64) ([]idea.Idea, error) {
	if s.reader != nil {
		return s.reader.ideasOfRequest(requestID)
	}
	rows, err := s.db.QueryContext(s.ctx(), `SELECT `+ideaCols+ideaFrom+` JOIN idea_requests l ON l.idea_id=i.id WHERE l.request_id=? ORDER BY i.id`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIdeas(rows)
}
