package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Offene Entscheidungen (REQ-611, Entscheidung zum Datenmodell: eigener Typ mit
// eigenen Tabellen statt Threads oder Wissen, weil sie einen Stand, Varianten und
// Stimmen tragen, die weder ein Thread noch ein Wissenseintrag kennt).
//
// Rechte folgen dem Gegenstand (Idee oder Issue): Fragen, Varianten anlegen und
// stimmen darf, wer am Gegenstand mitarbeiten darf (ActWork); eine Entscheidung
// der Art human treffen darf nur ein Mensch mit Bearbeitungsrecht am Gegenstand
// (ActEdit: Owner der Idee oder Maintainer). Wer den Gegenstand nicht lesen darf,
// bekommt dieselbe Antwort wie bei einer Nummer, die es nicht gibt.

func decisionNotFound(id int64) error {
	return requestdomain.NewRuleError("decision_not_found", fmt.Sprintf("no open decision DEC-%d", id), "list the decisions of the idea or issue to find its number", nil)
}

func subjectNotFound(kind string, id int64) error {
	if kind == idea.SubjectRequest {
		return requestdomain.NewRuleError("request_not_found", fmt.Sprintf("no request REQ-%d", id), "check the request number", nil)
	}
	return ideaNotFound(id)
}

// decisionSubject prüft den Gegenstand einer Entscheidung für eine Aktion und
// nennt sein Projekt. Unbekannt und fremd antworten gleich.
func (a *ProjectAccess) decisionSubject(kind string, id int64, act Action) (string, error) {
	switch kind {
	case idea.SubjectIdea:
		i, err := a.ideaFor(id, act)
		if err != nil {
			return "", err
		}
		return i.Project, nil
	case idea.SubjectRequest:
		ref, err := a.st.RequestRef(RefRequest, id)
		if err != nil {
			a.Filtered()
			return "", subjectNotFound(kind, id)
		}
		obj, visible := a.RequestObject(ref)
		if !visible {
			a.Filtered()
			return "", subjectNotFound(kind, id)
		}
		if err := a.Check(ref.Project, ResRequest, act, obj); err != nil {
			if errors.Is(err, ErrAccessNotFound) {
				return "", subjectNotFound(kind, id)
			}
			return "", err
		}
		return ref.Project, nil
	}
	return "", requestdomain.NewRuleError("subject_invalid", fmt.Sprintf("unknown subject %q", kind), "use idea or request", nil)
}

// canSeeDecisionSubject ist der Lesefilter für Listen.
func (a *ProjectAccess) canSeeDecisionSubject(kind string, id int64) bool {
	_, err := a.decisionSubject(kind, id, ActRead)
	return err == nil
}

// ---- Lesen

const decisionCols = `id,project,subject_kind,subject_id,question,detail,way,way_why,aspects,state,answer,reason,trigger_text,
	COALESCE(chosen_variant_id,0),COALESCE(knowledge_id,0),decided_by,decided_by_agent,decided_at,person,author_id,created_at,updated_at`

func scanDecisions(rows *sql.Rows) ([]idea.Decision, error) {
	var out []idea.Decision
	for rows.Next() {
		var d idea.Decision
		var why, aspects string
		var chosen int64
		var agent int
		if err := rows.Scan(&d.ID, &d.Project, &d.Subject, &d.SubjectID, &d.Question, &d.Detail, &d.Way, &why, &aspects, &d.State, &d.Answer, &d.Reason,
			&d.Trigger, &chosen, &d.KnowledgeID, &d.DecidedBy, &agent, &d.DecidedAt, &d.Person, &d.AuthorID, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		d.ByAgent = agent != 0
		d.Decider = d.Way
		// Wie die Entscheidung geschlossen wurde: vertagt und fallengelassen sind
		// eigene Arten; sonst steht da, wer entscheidet.
		if d.State == idea.DecisionDeferred || d.State == idea.DecisionDropped {
			d.Way = d.State
		}
		d.WayWhy = splitLines(why)
		d.Aspects = splitLines(aspects)
		if chosen != 0 {
			d.ChosenVariant = strconv.FormatInt(chosen, 10) // wird beim Anhängen der Varianten zum Buchstaben
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// attachDecisionParts hängt Varianten (mit Anhängen), Stimmen und Verlauf an.
func (s *Store) attachDecisionParts(list []idea.Decision) error {
	for i := range list {
		d := &list[i]
		chosenID, _ := strconv.ParseInt(d.ChosenVariant, 10, 64)
		d.ChosenVariant = ""
		rows, err := s.db.Query(`SELECT id,label,title,text,pro,contra FROM decision_variants WHERE decision_id=? ORDER BY label`, d.ID)
		if err != nil {
			return err
		}
		labels := map[int64]string{}
		for rows.Next() {
			var v idea.Variant
			if err := rows.Scan(&v.ID, &v.Label, &v.Title, &v.Text, &v.Pro, &v.Contra); err != nil {
				rows.Close()
				return err
			}
			labels[v.ID] = v.Label
			if v.ID == chosenID {
				v.Chosen = true
				d.ChosenVariant = v.Label
			}
			d.Variants = append(d.Variants, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		metas, err := s.attachmentMetas(d.Subject, d.SubjectID)
		if err != nil {
			return err
		}
		for vi := range d.Variants {
			for _, m := range metas {
				if m.Owner == idea.OwnerVariant && m.OwnerRef == strconv.FormatInt(d.Variants[vi].ID, 10) {
					d.Variants[vi].Attachments = append(d.Variants[vi].Attachments, m)
				}
			}
		}
		vrows, err := s.db.Query(`SELECT voter,by_agent,variant_id,reason,updated_at FROM decision_votes WHERE decision_id=? ORDER BY updated_at,voter_key`, d.ID)
		if err != nil {
			return err
		}
		for vrows.Next() {
			var v idea.Vote
			var agent int
			var vid int64
			if err := vrows.Scan(&v.Voter, &agent, &vid, &v.Reason, &v.At); err != nil {
				vrows.Close()
				return err
			}
			v.ByAgent = agent != 0
			v.Variant = labels[vid]
			d.Votes = append(d.Votes, v)
		}
		err = vrows.Err()
		vrows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) decisionByID(id int64) (idea.Decision, error) {
	if s.reader != nil {
		return s.reader.decisionByID(id)
	}
	rows, err := s.db.Query(`SELECT `+decisionCols+` FROM open_decisions WHERE id=?`, id)
	if err != nil {
		return idea.Decision{}, err
	}
	list, err := scanDecisions(rows)
	rows.Close()
	if err != nil {
		return idea.Decision{}, err
	}
	if len(list) == 0 {
		return idea.Decision{}, sql.ErrNoRows
	}
	if err := s.attachDecisionParts(list); err != nil {
		return idea.Decision{}, err
	}
	return list[0], nil
}

func (s *Store) decisionsWhere(where string, args ...any) ([]idea.Decision, error) {
	if s.reader != nil {
		return s.reader.decisionsWhere(where, args...)
	}
	rows, err := s.db.Query(`SELECT `+decisionCols+` FROM open_decisions WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	list, err := scanDecisions(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	return list, s.attachDecisionParts(list)
}

// DecisionView liest eine Entscheidung mit Varianten, Stimmen und Anhängen.
func (a *ProjectAccess) DecisionView(id int64) (idea.Decision, error) {
	d, err := a.st.decisionByID(id)
	if err != nil {
		a.Filtered()
		return idea.Decision{}, decisionNotFound(id)
	}
	if !a.canSeeDecisionSubject(d.Subject, d.SubjectID) {
		return idea.Decision{}, decisionNotFound(id)
	}
	return d, nil
}

// DecisionsOf listet die Entscheidungen eines Gegenstands, die offenen zuerst.
func (a *ProjectAccess) DecisionsOf(kind string, id int64) ([]idea.Decision, error) {
	if _, err := a.decisionSubject(kind, id, ActRead); err != nil {
		return nil, err
	}
	list, err := a.st.decisionsWhere(`subject_kind=? AND subject_id=? ORDER BY CASE state WHEN 'open' THEN 0 ELSE 1 END, id`, kind, id)
	if list == nil {
		list = []idea.Decision{}
	}
	return list, err
}

// DecisionFilter begrenzt die Liste der Entscheidungen eines Projekts.
type DecisionFilter struct {
	Project string
	// State: "" heißt open, "all" alle.
	State string
	// Way: "" alle, sonst human, agent, deferred oder dropped.
	Way string
	// NeedsHuman: nur offene der Art human, also der Posteingang.
	NeedsHuman bool
	Limit      int
}

// SearchDecisions liefert die Entscheidungen des Projekts, die der Betrachter
// lesen darf, jüngste zuerst. NeedsHuman ist die Liste für den Posteingang: nur
// was auf einen Menschen wartet; alle anderen Arten sind Hinweise und stehen in
// der Liste mit State all oder Way.
func (a *ProjectAccess) SearchDecisions(f DecisionFilter) ([]idea.Decision, error) {
	f.Project = scope.NormalizeRemote(strings.TrimSpace(f.Project))
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	where, args := `project=?`, []any{f.Project}
	switch f.State {
	case "":
		where += ` AND state='open'`
	case "all":
	case idea.DecisionOpen, idea.DecisionDecided, idea.DecisionDeferred, idea.DecisionDropped:
		where += ` AND state=?`
		args = append(args, f.State)
	default:
		return nil, requestdomain.NewRuleError("invalid_state", fmt.Sprintf("unknown decision state %q", f.State), "use open, decided, deferred, dropped or all", nil)
	}
	if f.NeedsHuman {
		where += ` AND state='open' AND way='human'`
	} else if f.Way != "" {
		switch f.Way {
		case idea.WayHuman, idea.WayAgent, idea.WayDeferred, idea.WayDropped:
			where += ` AND way=?`
			args = append(args, f.Way)
		default:
			return nil, requestdomain.NewRuleError("way_invalid", fmt.Sprintf("unknown way %q", f.Way), "use human, agent, deferred or dropped", nil)
		}
	}
	// Mehr holen, als die Seite braucht: der Sichtfilter entfernt danach Zeilen.
	list, err := a.st.decisionsWhere(where+` ORDER BY id DESC LIMIT ?`, append(args, f.Limit*4)...)
	if err != nil {
		return nil, err
	}
	out := make([]idea.Decision, 0, len(list))
	for _, d := range list {
		if len(out) >= f.Limit {
			break
		}
		if a.canSeeDecisionSubject(d.Subject, d.SubjectID) {
			out = append(out, d)
		}
	}
	return out, nil
}

// ---- Policy je Projekt

func (s *Store) decisionPolicy(project string) string {
	if s.reader != nil {
		return s.reader.decisionPolicy(project)
	}
	var mode string
	if err := s.db.QueryRow(`SELECT mode FROM decision_policy WHERE project=?`, project).Scan(&mode); err != nil {
		return idea.PolicyRule
	}
	return mode
}

// DecisionPolicy sagt, wer in diesem Projekt entscheiden darf: rule (die Regel
// aus idea.DecideWho) oder human (jede Entscheidung wartet auf einen Menschen).
func (a *ProjectAccess) DecisionPolicy(project string) (string, error) {
	project = scope.NormalizeRemote(strings.TrimSpace(project))
	if err := a.Check(project, ResIdea, ActRead, Object{}); err != nil {
		return "", err
	}
	return a.st.decisionPolicy(project), nil
}

// SetDecisionPolicy stellt die Policy des Projekts um. Das darf, wer im Projekt
// Maintainer ist, und nur als Mensch aus einer Websitzung: mit „rule“ dürfen
// Agenten wieder selbst entscheiden, was die Regel ihnen lässt.
func (a *ProjectAccess) SetDecisionPolicy(project, mode, by string, actor idea.Actor) error {
	if actor.Agent {
		return humanOnly("changing who decides in a project")
	}
	project = scope.NormalizeRemote(strings.TrimSpace(project))
	if !idea.ValidPolicy(mode) {
		return requestdomain.NewRuleError("policy_invalid", fmt.Sprintf("unknown policy %q", mode), "use rule or human", nil)
	}
	if err := a.Check(project, ResIdea, ActEdit, Object{}); err != nil {
		return err
	}
	return a.st.commitDecisionPolicy(project, mode, by)
}

func (s *Store) commitDecisionPolicy(project, mode, by string) error {
	if s.writer != nil {
		return queueWrite(s, []any{project, mode, by}, func(d *Store, p []any) error {
			return d.commitDecisionPolicy(p[0].(string), p[1].(string), p[2].(string))
		})
	}
	_, err := s.db.Exec(`INSERT INTO decision_policy(project,mode,set_by,updated_at) VALUES(?,?,?,?)
		ON CONFLICT(project) DO UPDATE SET mode=excluded.mode, set_by=excluded.set_by, updated_at=excluded.updated_at`, project, mode, by, now())
	return err
}

// ---- Anlegen

// OpenDecisionInput legt eine offene Entscheidung an.
type OpenDecisionInput struct {
	Subject   string
	SubjectID int64
	Question  string
	Detail    string
	// Way ist, was der Fragende ausdrücklich will (human oder agent); leer lässt
	// die Regel entscheiden.
	Way      string
	Aspects  []string
	Variants []idea.VariantInput
	Actor    idea.Actor
}

func cleanDetail(s string) (string, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	if len([]rune(s)) > idea.MaxDetail {
		return "", requestdomain.NewRuleError("detail_too_long", fmt.Sprintf("the detail is longer than %d characters", idea.MaxDetail), "shorten it", nil)
	}
	for _, r := range s {
		if r != '\n' && r != '\t' && (r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == ' ' || r == ' ' || (r >= 0x200b && r <= 0x200f) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) || r == 0xfeff) {
			return "", requestdomain.NewRuleError("detail_invalid", "the detail has control or formatting characters", "remove them", nil)
		}
	}
	return s, nil
}

// OpenDecision stellt eine Frage an eine Idee oder ein Issue, optional mit
// Varianten. Die Art, sie zu schließen, folgt der Regel (idea.ChooseWay): ein
// Agent entscheidet selbst, was umkehrbar, lokal und konventionsgemäß ist; alles
// andere wartet auf einen Menschen. Ein Mensch kann die Art ausdrücklich setzen.
func (a *ProjectAccess) OpenDecision(in OpenDecisionInput) (idea.Decision, error) {
	project, err := a.decisionSubject(in.Subject, in.SubjectID, ActWork)
	if err != nil {
		return idea.Decision{}, err
	}
	question, err := idea.CleanQuestion(in.Question)
	if err != nil {
		return idea.Decision{}, err
	}
	detail, err := cleanDetail(in.Detail)
	if err != nil {
		return idea.Decision{}, err
	}
	aspects, err := idea.NormalizeAspects(in.Aspects)
	if err != nil {
		return idea.Decision{}, err
	}
	if len(in.Variants) == 1 {
		return idea.Decision{}, requestdomain.NewRuleError("variants_too_few", "a choice needs at least two variants", "give two or more, or none", nil)
	}
	if len(in.Variants) > idea.MaxVariants {
		return idea.Decision{}, requestdomain.NewRuleError("variants_too_many", fmt.Sprintf("at most %d variants", idea.MaxVariants), "narrow the choice down", nil)
	}
	variants := make([]idea.VariantInput, 0, len(in.Variants))
	for _, v := range in.Variants {
		c, err := idea.CleanVariant(v)
		if err != nil {
			return idea.Decision{}, err
		}
		variants = append(variants, c)
	}
	way, why, err := idea.ChooseWay(in.Way, aspects, a.st.decisionPolicy(project), in.Actor.Agent)
	if err != nil {
		return idea.Decision{}, err
	}
	id, err := a.st.commitDecisionOpen(decisionOpen{Project: project, Subject: in.Subject, SubjectID: in.SubjectID, Question: question, Detail: detail,
		Way: way, WayWhy: strings.Join(why, "\n"), Aspects: strings.Join(aspects, "\n"), Variants: variants, Actor: in.Actor})
	if err != nil {
		return idea.Decision{}, err
	}
	return a.DecisionView(id)
}

type decisionOpen struct {
	Project, Subject string
	SubjectID        int64
	Question, Detail string
	Way, WayWhy      string
	Aspects          string
	Variants         []idea.VariantInput
	Actor            idea.Actor
}

func (s *Store) commitDecisionOpen(o decisionOpen) (int64, error) {
	if s.writer != nil {
		return queueValue(s, []any{o}, func(d *Store, p []any) (int64, error) { return d.commitDecisionOpen(p[0].(decisionOpen)) })
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ts := now()
	account := authorForWriteTx(tx, o.Actor.AccountID, o.Actor.Person, agentOrigin(o.Actor.Agent), o.Actor.SessionRef)
	res, err := tx.Exec(`INSERT INTO open_decisions(project,subject_kind,subject_id,question,detail,way,way_why,aspects,state,person,author_id,session_ref,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,'open',?,?,?,?,?)`, o.Project, o.Subject, o.SubjectID, o.Question, o.Detail, o.Way, o.WayWhy, o.Aspects, o.Actor.Person, account, o.Actor.SessionRef, ts, ts)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	for n, v := range o.Variants {
		if _, err := tx.Exec(`INSERT INTO decision_variants(decision_id,label,title,text,pro,contra,created_by,created_at) VALUES(?,?,?,?,?,?,?,?)`,
			id, idea.VariantLabel(n), v.Title, v.Text, v.Pro, v.Contra, o.Actor.Person, ts); err != nil {
			return 0, err
		}
	}
	if err := decisionEventTx(tx, id, "opened", o.Actor, o.Question); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func agentOrigin(agent bool) string {
	if agent {
		return "agent"
	}
	return "human"
}

func decisionEventTx(tx *sql.Tx, id int64, kind string, actor idea.Actor, note string) error {
	_, err := tx.Exec(`INSERT INTO decision_events(decision_id,kind,person,by_agent,note,created_at) VALUES(?,?,?,?,?,?)`,
		id, kind, actor.Person, boolInt(actor.Agent), note, now())
	return err
}

// ---- Varianten und Stimmen

// AddVariant fügt einer offenen Entscheidung eine Variante hinzu (der nächste
// Buchstabe).
func (a *ProjectAccess) AddVariant(decisionID int64, v idea.VariantInput, actor idea.Actor) (idea.Decision, error) {
	d, err := a.workableDecision(decisionID)
	if err != nil {
		return idea.Decision{}, err
	}
	c, err := idea.CleanVariant(v)
	if err != nil {
		return idea.Decision{}, err
	}
	if len(d.Variants) >= idea.MaxVariants {
		return idea.Decision{}, requestdomain.NewRuleError("variants_too_many", fmt.Sprintf("at most %d variants", idea.MaxVariants), "narrow the choice down", nil)
	}
	if err := a.st.commitVariant(decisionID, c, actor); err != nil {
		return idea.Decision{}, err
	}
	return a.DecisionView(decisionID)
}

// workableDecision lädt eine offene Entscheidung für Mitarbeit (ActWork am
// Gegenstand). Eine geschlossene nimmt nichts mehr an.
func (a *ProjectAccess) workableDecision(id int64) (idea.Decision, error) {
	d, err := a.st.decisionByID(id)
	if err != nil {
		a.Filtered()
		return idea.Decision{}, decisionNotFound(id)
	}
	if _, err := a.decisionSubject(d.Subject, d.SubjectID, ActWork); err != nil {
		if requestdomain.Code(err) == "idea_not_found" || requestdomain.Code(err) == "request_not_found" {
			return idea.Decision{}, decisionNotFound(id)
		}
		return idea.Decision{}, err
	}
	if d.State != idea.DecisionOpen {
		return idea.Decision{}, requestdomain.NewRuleError("decision_closed", fmt.Sprintf("%s is %s", d.HumanID(), d.State), "reopen it first if it should change", nil)
	}
	return d, nil
}

func (s *Store) commitVariant(decisionID int64, v idea.VariantInput, actor idea.Actor) error {
	if s.writer != nil {
		return queueWrite(s, []any{decisionID, v, actor}, func(d *Store, p []any) error {
			return d.commitVariant(p[0].(int64), p[1].(idea.VariantInput), p[2].(idea.Actor))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	if err := tx.QueryRow(`SELECT state FROM open_decisions WHERE id=?`, decisionID).Scan(&state); err != nil {
		return err
	}
	if state != idea.DecisionOpen {
		return requestdomain.NewRuleError("decision_closed", "the decision was closed in the meantime", "reopen it first", nil)
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM decision_variants WHERE decision_id=?`, decisionID).Scan(&n); err != nil {
		return err
	}
	if n >= idea.MaxVariants {
		return requestdomain.NewRuleError("variants_too_many", "too many variants", "narrow the choice down", nil)
	}
	if _, err := tx.Exec(`INSERT INTO decision_variants(decision_id,label,title,text,pro,contra,created_by,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		decisionID, idea.VariantLabel(n), v.Title, v.Text, v.Pro, v.Contra, actor.Person, now()); err != nil {
		return err
	}
	if err := decisionEventTx(tx, decisionID, "variant", actor, idea.VariantLabel(n)+" "+v.Title); err != nil {
		return err
	}
	return tx.Commit()
}

// VoteDecision hält fest, welche Variante jemand nehmen würde und warum („ich
// würde A nehmen, weil …“). Je Person und Art (Mensch oder Agent) zählt die
// letzte Stimme; die Stimmen sind je Person sichtbar. Eine Stimme entscheidet
// nichts.
func (a *ProjectAccess) VoteDecision(decisionID int64, variant, reason string, actor idea.Actor) (idea.Decision, error) {
	d, err := a.workableDecision(decisionID)
	if err != nil {
		return idea.Decision{}, err
	}
	reason, err = idea.CleanReasonLine(reason, true)
	if err != nil {
		return idea.Decision{}, err
	}
	var vid int64
	variant = strings.ToUpper(strings.TrimSpace(variant))
	for _, v := range d.Variants {
		if v.Label == variant {
			vid = v.ID
		}
	}
	if vid == 0 {
		return idea.Decision{}, requestdomain.NewRuleError("variant_not_found", fmt.Sprintf("%s has no variant %q", d.HumanID(), variant), "choose one of the letters of its variants", nil)
	}
	key := "person:" + strconv.FormatInt(actor.AccountID, 10)
	if actor.AccountID == 0 {
		key = "name:" + actor.Person
	}
	if actor.Agent {
		key += ":agent"
	}
	if err := a.st.commitVote(decisionID, key, actor, vid, reason); err != nil {
		return idea.Decision{}, err
	}
	return a.DecisionView(decisionID)
}

func (s *Store) commitVote(decisionID int64, key string, actor idea.Actor, variantID int64, reason string) error {
	if s.writer != nil {
		return queueWrite(s, []any{decisionID, key, actor, variantID, reason}, func(d *Store, p []any) error {
			return d.commitVote(p[0].(int64), p[1].(string), p[2].(idea.Actor), p[3].(int64), p[4].(string))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	if err := tx.QueryRow(`SELECT state FROM open_decisions WHERE id=?`, decisionID).Scan(&state); err != nil {
		return err
	}
	if state != idea.DecisionOpen {
		return requestdomain.NewRuleError("decision_closed", "the decision was closed in the meantime", "reopen it first", nil)
	}
	ts := now()
	if _, err := tx.Exec(`INSERT INTO decision_votes(decision_id,voter_key,voter,by_agent,variant_id,reason,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(decision_id,voter_key) DO UPDATE SET variant_id=excluded.variant_id, reason=excluded.reason, voter=excluded.voter, updated_at=excluded.updated_at`,
		decisionID, key, actor.Person, boolInt(actor.Agent), variantID, reason, ts, ts); err != nil {
		return err
	}
	if err := decisionEventTx(tx, decisionID, "vote", actor, reason); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- Art umstellen, wieder öffnen

// SetDecisionWay stellt die Art einer offenen Entscheidung um (human oder
// agent). Das darf nur ein Mensch mit Bearbeitungsrecht am Gegenstand: ein Agent
// kann sich die Entscheidung nicht selbst zuschieben.
func (a *ProjectAccess) SetDecisionWay(id int64, way string, actor idea.Actor) (idea.Decision, error) {
	d, err := a.st.decisionByID(id)
	if err != nil {
		a.Filtered()
		return idea.Decision{}, decisionNotFound(id)
	}
	if _, err := a.decisionSubject(d.Subject, d.SubjectID, ActEdit); err != nil {
		return idea.Decision{}, hideDecisionSubject(err, id)
	}
	if actor.Agent {
		return idea.Decision{}, requestdomain.NewRuleError("human_only", "only a person changes who decides", "ask a person", nil)
	}
	if way != idea.WayHuman && way != idea.WayAgent {
		return idea.Decision{}, requestdomain.NewRuleError("way_invalid", fmt.Sprintf("unknown way %q", way), "use human or agent", nil)
	}
	if d.State != idea.DecisionOpen {
		return idea.Decision{}, requestdomain.NewRuleError("decision_closed", fmt.Sprintf("%s is %s", d.HumanID(), d.State), "reopen it first", nil)
	}
	if err := a.st.commitDecisionWay(id, way, actor); err != nil {
		return idea.Decision{}, err
	}
	return a.DecisionView(id)
}

func hideDecisionSubject(err error, id int64) error {
	switch requestdomain.Code(err) {
	case "idea_not_found", "request_not_found":
		return decisionNotFound(id)
	}
	return err
}

func (s *Store) commitDecisionWay(id int64, way string, actor idea.Actor) error {
	if s.writer != nil {
		return queueWrite(s, []any{id, way, actor}, func(d *Store, p []any) error {
			return d.commitDecisionWay(p[0].(int64), p[1].(string), p[2].(idea.Actor))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE open_decisions SET way=?, way_why='set by a person', updated_at=? WHERE id=? AND state='open'`, way, now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return requestdomain.NewRuleError("decision_closed", "the decision was closed in the meantime", "reopen it first", nil)
	}
	if err := decisionEventTx(tx, id, "way", actor, way); err != nil {
		return err
	}
	return tx.Commit()
}

// ReopenDecision macht eine geschlossene Entscheidung wieder offen. Eine vom
// Agenten getroffene kippt nur ein Mensch (sie geht an einen Menschen zurück);
// eine vertagte darf jeder öffnen, der mitarbeiten darf, denn sie kehrt ohnehin
// wieder, wenn ihr Auslöser eintritt. Das alte Wissen (decision) bleibt, bis die
// neue Entscheidung es ablöst.
func (a *ProjectAccess) ReopenDecision(id int64, reason string, actor idea.Actor) (idea.Decision, error) {
	d, err := a.st.decisionByID(id)
	if err != nil {
		a.Filtered()
		return idea.Decision{}, decisionNotFound(id)
	}
	if _, err := a.decisionSubject(d.Subject, d.SubjectID, ActWork); err != nil {
		return idea.Decision{}, hideDecisionSubject(err, id)
	}
	// Nur eine vertagte Entscheidung holt jeder Mitarbeitende zurück. Alles, was
	// geschlossen wurde, kippt nur, wer am Gegenstand Bearbeitungsrecht hat; ein
	// Agent kippt nie still, er kann höchstens eine neue Entscheidung mit Bezug
	// stellen.
	if d.State != idea.DecisionDeferred && d.State != idea.DecisionOpen {
		if _, err := a.decisionSubject(d.Subject, d.SubjectID, ActEdit); err != nil {
			return idea.Decision{}, hideDecisionSubject(err, id)
		}
	}
	if d.State == idea.DecisionOpen {
		return idea.Decision{}, requestdomain.NewRuleError("decision_open", fmt.Sprintf("%s is already open", d.HumanID()), "nothing to do", nil)
	}
	reason, err = idea.CleanReasonLine(reason, true)
	if err != nil {
		return idea.Decision{}, err
	}
	// Die Art, mit der sie gestellt wurde, bleibt; ein Mensch, der eine
	// Entscheidung kippt, nimmt sie an einen Menschen zurück.
	way := idea.WayHuman
	if d.State == idea.DecisionDeferred {
		way = d.Decider
	} else if actor.Agent {
		return idea.Decision{}, requestdomain.NewRuleError("human_only", "only a person overturns a decision", "ask a person to reopen it", nil)
	}
	if err := a.st.commitDecisionReopen(id, way, reason, actor); err != nil {
		return idea.Decision{}, err
	}
	return a.DecisionView(id)
}

func (s *Store) commitDecisionReopen(id int64, way, reason string, actor idea.Actor) error {
	if s.writer != nil {
		return queueWrite(s, []any{id, way, reason, actor}, func(d *Store, p []any) error {
			return d.commitDecisionReopen(p[0].(int64), p[1].(string), p[2].(string), p[3].(idea.Actor))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE open_decisions SET state='open', way=?, way_why=?, answer='', trigger_text='', chosen_variant_id=NULL, decided_by='', decided_by_agent=0, decided_at='', updated_at=?
		WHERE id=? AND state<>'open'`, way, "reopened: "+reason, now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return requestdomain.NewRuleError("decision_changed", "the decision changed in the meantime", "read it again", nil)
	}
	if err := decisionEventTx(tx, id, "reopened", actor, reason); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- Schließen

// CloseDecisionInput schließt eine Entscheidung. Outcome ist decided, deferred
// oder dropped.
type CloseDecisionInput struct {
	ID      int64
	Outcome string
	// Variant ist der Buchstabe der übernommenen Variante; Answer der Text der
	// Entscheidung, wenn es keine Variante gibt oder eine andere Lösung gilt.
	Variant string
	Answer  string
	// Reason ist die Begründung (decided, dropped), Trigger der Auslöser (deferred).
	Reason  string
	Trigger string
	Actor   idea.Actor
}

// CloseDecision schließt eine offene Entscheidung.
//
// decided: braucht eine Variante oder eine Antwort und eine Begründung. Es
// entsteht Wissen (decision) mit Begründung; trifft ein Agent sie, ist der
// Eintrag als „vom Agenten“ markiert, blockiert nichts und ein Mensch kann ihn
// kippen. An einer Idee wandert die Entscheidung samt Bild-Verweis ins Konzept.
// Schließen darf nur, wer am Gegenstand Bearbeitungsrecht hat; die Art human
// trifft zudem nur ein Mensch.
// deferred: braucht einen Auslöser, die Entscheidung kehrt im Briefing zurück,
// wenn er zum Branch oder Paket passt. dropped: braucht einen Grund.
func (a *ProjectAccess) CloseDecision(in CloseDecisionInput) (idea.Decision, error) {
	d, err := a.workableDecision(in.ID)
	if err != nil {
		return idea.Decision{}, err
	}
	// Schließen schreibt: bei decided Wissen (decision) und, an einer Idee, eine
	// neue Fassung des Konzepts; vertagen und fallenlassen nehmen die Frage aus
	// der Liste. Das darf nur, wer am Gegenstand Bearbeitungsrecht hat (Owner der
	// Idee, Maintainer). Wer nur mitarbeiten darf, stimmt ab oder schlägt Varianten
	// vor, schließt aber nicht.
	if _, err := a.decisionSubject(d.Subject, d.SubjectID, ActEdit); err != nil {
		return idea.Decision{}, hideDecisionSubject(err, in.ID)
	}
	// Die Art human trifft ein Mensch; ein Agent kann sie weder entscheiden noch
	// vertagen noch fallenlassen, sonst schöbe er sie aus dem Posteingang.
	if d.Way == idea.WayHuman && in.Actor.Agent {
		return idea.Decision{}, requestdomain.NewRuleError("human_decision", fmt.Sprintf("%s waits for a person: %s", d.HumanID(), strings.Join(d.WayWhy, "; ")), "leave it open, or add your view as a variant vote", nil)
	}
	c := decisionClose{ID: d.ID, Actor: in.Actor}
	switch in.Outcome {
	case idea.DecisionDecided:
		if c.Reason, err = idea.CleanReasonLine(in.Reason, true); err != nil {
			return idea.Decision{}, err
		}
		answer := strings.TrimSpace(in.Answer)
		if answer != "" {
			if answer, err = cleanDetail(answer); err != nil {
				return idea.Decision{}, err
			}
		}
		var chosen *idea.Variant
		if v := strings.ToUpper(strings.TrimSpace(in.Variant)); v != "" {
			for i := range d.Variants {
				if d.Variants[i].Label == v {
					chosen = &d.Variants[i]
				}
			}
			if chosen == nil {
				return idea.Decision{}, requestdomain.NewRuleError("variant_not_found", fmt.Sprintf("%s has no variant %q", d.HumanID(), v), "choose one of the letters of its variants", nil)
			}
		}
		if chosen == nil && answer == "" {
			code := "answer_required"
			msg := "name the decision: an answer, or a variant"
			if len(d.Variants) > 0 {
				code, msg = "variant_required", "this decision has variants: name the one that is taken (or give your own answer)"
			}
			return idea.Decision{}, requestdomain.NewRuleError(code, msg, "pass variant (a letter) or answer", nil)
		}
		c.State, c.Answer = idea.DecisionDecided, answer
		if chosen != nil {
			c.VariantID = chosen.ID
		}
		c.Knowledge, c.ConceptLine = a.decisionKnowledge(d, chosen, answer, c.Reason, in.Actor)
	case idea.DecisionDeferred:
		if c.Trigger, err = idea.CleanTrigger(in.Trigger); err != nil {
			return idea.Decision{}, err
		}
		if c.Reason, err = idea.CleanReasonLine(in.Reason, false); err != nil {
			return idea.Decision{}, err
		}
		c.State = idea.DecisionDeferred
	case idea.DecisionDropped:
		if c.Reason, err = idea.CleanReasonLine(in.Reason, true); err != nil {
			return idea.Decision{}, err
		}
		c.State = idea.DecisionDropped
	default:
		return idea.Decision{}, requestdomain.NewRuleError("outcome_invalid", fmt.Sprintf("unknown outcome %q", in.Outcome), "use decided, deferred or dropped", nil)
	}
	if err := a.st.commitDecisionClose(c); err != nil {
		return idea.Decision{}, err
	}
	return a.DecisionView(in.ID)
}

type decisionClose struct {
	ID          int64
	State       string
	Answer      string
	Reason      string
	Trigger     string
	VariantID   int64
	Knowledge   *Knowledge
	ConceptLine string
	Actor       idea.Actor
}

// decisionKnowledge baut den Wissenseintrag (decision) und die Zeile fürs
// Konzept. Titel und Zeilen sind bereinigt; der Text enthält Frage, Entscheidung,
// Pro und Contra der Variante, Begründung und die Verweise auf Bilder.
func (a *ProjectAccess) decisionKnowledge(d idea.Decision, v *idea.Variant, answer, reason string, actor idea.Actor) (*Knowledge, string) {
	var decision strings.Builder
	var images []string
	if v != nil {
		fmt.Fprintf(&decision, "Variant %s: %s", v.Label, v.Title)
		if v.Text != "" {
			decision.WriteString("\n" + v.Text)
		}
		if v.Pro != "" {
			decision.WriteString("\nPro: " + v.Pro)
		}
		if v.Contra != "" {
			decision.WriteString("\nContra: " + v.Contra)
		}
		for _, m := range v.Attachments {
			images = append(images, fmt.Sprintf("ATT-%d %s", m.ID, m.URL))
		}
	}
	if answer != "" {
		if decision.Len() > 0 {
			decision.WriteString("\n")
		}
		decision.WriteString(answer)
	}
	subject := fmt.Sprintf("IDEA-%d", d.SubjectID)
	if d.Subject == idea.SubjectRequest {
		subject = fmt.Sprintf("REQ-%d", d.SubjectID)
	}
	title := fmt.Sprintf("%s: %s", d.HumanID(), d.Question)
	var body strings.Builder
	fmt.Fprintf(&body, "Question (%s, %s): %s\n\nDecision:\n%s\n\nReason: %s\n", d.HumanID(), subject, d.Question, decision.String(), reason)
	if len(images) > 0 {
		body.WriteString("\nImages: " + strings.Join(images, "; ") + "\n")
	}
	origin := "human"
	if actor.Agent {
		title = "vom Agenten: " + title
		origin = "agent"
		body.WriteString("\nDecided by an agent, not confirmed by a person. It blocks nothing; a person can overturn it with decision_reopen.\n")
	}
	if len(d.Votes) > 0 {
		body.WriteString("\nVotes:")
		for _, vt := range d.Votes {
			who := vt.Voter
			if vt.ByAgent {
				who += " (agent)"
			}
			fmt.Fprintf(&body, "\n- %s: %s, %s", who, vt.Variant, vt.Reason)
		}
		body.WriteString("\n")
	}
	k := &Knowledge{Type: "decision", Title: title, Body: strings.TrimSpace(body.String()), Scope: scope.Axes{Project: d.Project},
		Origin: origin, Confidence: "trusted", Person: actor.Person, AuthorID: actor.AccountID, SessionRef: actor.SessionRef,
		GitAnchor: actor.GitAnchor, OriginContext: actor.Origin}
	line := ""
	if d.Subject == idea.SubjectIdea {
		line = fmt.Sprintf("- %s %s: %s. Reason: %s", d.HumanID(), d.Question, idea.Brief(decision.String(), 400), reason)
		if len(images) > 0 {
			line += " Images: " + strings.Join(images, ", ")
		}
		if actor.Agent {
			line += " (decided by an agent)"
		}
	}
	return k, line
}

func (s *Store) commitDecisionClose(c decisionClose) error {
	if s.writer != nil {
		return queueWrite(s, []any{c}, func(d *Store, p []any) error { return d.commitDecisionClose(p[0].(decisionClose)) })
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state, subject string
	var subjectID, prevKnowledge int64
	if err := tx.QueryRow(`SELECT state, subject_kind, subject_id, COALESCE(knowledge_id,0) FROM open_decisions WHERE id=?`, c.ID).Scan(&state, &subject, &subjectID, &prevKnowledge); err != nil {
		return err
	}
	if state != idea.DecisionOpen {
		return requestdomain.NewRuleError("decision_closed", "the decision was closed in the meantime", "read it again", nil)
	}
	ts := now()
	var knowledgeID any
	if c.Knowledge != nil {
		id, err := insertKnowledgeTx(tx, withKnowledgeDefaults(*c.Knowledge))
		if err != nil {
			return err
		}
		knowledgeID = id
		// Löst die Entscheidung eine frühere ab (gekippt und neu entschieden), wird
		// deren Wissen abgelöst, soweit es noch gilt.
		if prevKnowledge > 0 {
			var status string
			if err := tx.QueryRow(`SELECT status FROM knowledge WHERE id=?`, prevKnowledge).Scan(&status); err == nil && currentStatus(status) {
				if err := supersedeTx(tx, id, []int64{prevKnowledge}, "decision "+fmt.Sprintf("DEC-%d", c.ID)+" was decided again: "+c.Reason, RelationActor{Name: c.Actor.Person, Via: "mcp"}); err != nil {
					return err
				}
			}
		}
	}
	var variant any
	if c.VariantID != 0 {
		variant = c.VariantID
	}
	decidedBy := c.Actor.Person
	if c.State == idea.DecisionDecided {
		// way bleibt, was es war: wer entscheiden durfte, steht in way_why und im Verlauf.
		res, err := tx.Exec(`UPDATE open_decisions SET state='decided', answer=?, reason=?, chosen_variant_id=?, knowledge_id=COALESCE(?,knowledge_id), decided_by=?, decided_by_agent=?, decided_at=?, updated_at=? WHERE id=? AND state='open'`,
			c.Answer, c.Reason, variant, knowledgeID, decidedBy, boolInt(c.Actor.Agent), ts, ts, c.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return requestdomain.NewRuleError("decision_closed", "the decision was closed in the meantime", "read it again", nil)
		}
	} else {
		res, err := tx.Exec(`UPDATE open_decisions SET state=?, reason=?, trigger_text=?, decided_by=?, decided_by_agent=?, decided_at=?, updated_at=? WHERE id=? AND state='open'`,
			c.State, c.Reason, c.Trigger, decidedBy, boolInt(c.Actor.Agent), ts, ts, c.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return requestdomain.NewRuleError("decision_closed", "the decision was closed in the meantime", "read it again", nil)
		}
	}
	if c.ConceptLine != "" && subject == idea.SubjectIdea {
		if err := appendConceptNoteTx(tx, subjectID, c.ConceptLine, c.Actor, fmt.Sprintf("DEC-%d decided", c.ID)); err != nil {
			return err
		}
	}
	note := c.Reason
	if c.State == idea.DecisionDeferred {
		note = c.Trigger
	}
	if err := decisionEventTx(tx, c.ID, c.State, c.Actor, note); err != nil {
		return err
	}
	return tx.Commit()
}

// appendConceptNoteTx hängt eine Zeile an den Abschnitt „Wie“ des Konzepts, als
// neue Fassung. Passt sie nicht mehr in den Abschnitt, bleibt das Konzept, wie es
// ist: das Wissen trägt die Entscheidung ohnehin.
func appendConceptNoteTx(tx *sql.Tx, ideaID int64, line string, actor idea.Actor, message string) error {
	var phase, state string
	if err := tx.QueryRow(`SELECT phase, state FROM ideas WHERE id=?`, ideaID).Scan(&phase, &state); err != nil {
		return err
	}
	if state == idea.Dismissed || phase == idea.PhaseDone {
		return nil
	}
	var c idea.Concept
	head := 0
	var body string
	err := tx.QueryRow(`SELECT revision, body FROM idea_texts WHERE idea_id=? AND part='concept' ORDER BY revision DESC LIMIT 1`, ideaID).Scan(&head, &body)
	switch {
	case err == nil:
		if err := json.Unmarshal([]byte(body), &c); err != nil {
			return err
		}
	case errors.Is(err, sql.ErrNoRows):
	default:
		return err
	}
	next := strings.TrimSpace(c.How + "\n" + line)
	if len([]rune(next)) > idea.MaxSection {
		return nil
	}
	c.How = next
	if c.Requirements == nil {
		c.Requirements = []string{}
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	ts := now()
	if _, err := tx.Exec(`INSERT INTO idea_texts(idea_id,part,revision,body,digest,source,message,person,by_agent,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		ideaID, idea.PartConcept, head+1, string(raw), Digest(string(raw)), "", message, actor.Person, boolInt(actor.Agent), ts); err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE ideas SET updated_at=?, phase=CASE WHEN phase='raw' THEN 'concept' ELSE phase END WHERE id=?`, ts, ideaID)
	return err
}

// ---- Vertagte Entscheidungen im Briefing

// DueDecisions liefert die vertagten Entscheidungen des Projekts, deren Auslöser
// zu dem Kontext passt (Branch, Paketname, Titel der Requests), soweit der
// Betrachter ihren Gegenstand lesen darf. Sie erscheinen im Briefing genau dann.
func (a *ProjectAccess) DueDecisions(project string, context ...string) ([]idea.Decision, error) {
	project = scope.NormalizeRemote(strings.TrimSpace(project))
	list, err := a.st.decisionsWhere(`project=? AND state='deferred' ORDER BY id LIMIT 200`, project)
	if err != nil {
		return nil, err
	}
	var out []idea.Decision
	for _, d := range list {
		if idea.TriggerMatches(d.Trigger, context...) && a.canSeeDecisionSubject(d.Subject, d.SubjectID) {
			out = append(out, d)
		}
	}
	return out, nil
}
