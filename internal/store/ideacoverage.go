package store

import (
	"fmt"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// Rückverfolgung (REQ-611): jedes Kriterium eines Issues, das aus der Idee
// entstand, verweist auf seine Anforderung (criterion_requirements, beim Anlegen
// gesetzt). Eine Idee ist erst umgesetzt, wenn jede Anforderung ein erfülltes
// Kriterium hat oder ausdrücklich erlassen ist (idea_requirement_waivers).
// Anforderungen ohne beides erscheinen an der Idee und in der Prüfansicht des
// Pakets.

const ideaCoverageSchema = `
CREATE TABLE IF NOT EXISTS criterion_requirements(
  criterion_id INTEGER PRIMARY KEY REFERENCES request_criteria(id) ON DELETE RESTRICT,
  idea_id INTEGER NOT NULL REFERENCES ideas(id) ON DELETE RESTRICT,
  requirement_no INTEGER NOT NULL,
  requirement_text TEXT NOT NULL,
  created_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS criterion_requirements_idea ON criterion_requirements(idea_id);
CREATE TABLE IF NOT EXISTS idea_requirement_waivers(
  idea_id INTEGER NOT NULL REFERENCES ideas(id) ON DELETE RESTRICT,
  requirement_key TEXT NOT NULL,
  requirement_text TEXT NOT NULL,
  reason TEXT NOT NULL CHECK(trim(reason) != ''),
  person TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  PRIMARY KEY(idea_id, requirement_key));
`

type criterionRequirement struct {
	CriterionID int64
	No          int
	Text        string
}

// commitCriterionRequirements verknüpft Kriterien mit den Anforderungen, aus denen
// sie entstanden.
func (s *Store) commitCriterionRequirements(ideaID int64, links []criterionRequirement) error {
	if len(links) == 0 {
		return nil
	}
	if s.writer != nil {
		return queueWrite(s, []any{ideaID, links}, func(d *Store, p []any) error {
			return d.commitCriterionRequirements(p[0].(int64), p[1].([]criterionRequirement))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	for _, l := range links {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO criterion_requirements(criterion_id,idea_id,requirement_no,requirement_text,created_at) VALUES(?,?,?,?,?)`,
			l.CriterionID, ideaID, l.No, l.Text, ts); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// requirementCoverage rechnet für jede Anforderung des jüngsten Konzepts aus,
// ob sie abgedeckt ist. Ohne Rücksicht auf den Betrachter: es ist die Wahrheit,
// an der das Umsetzen der Idee hängt.
func (s *Store) requirementCoverage(ideaID int64) ([]idea.RequirementCoverage, error) {
	if s.reader != nil {
		return s.reader.requirementCoverage(ideaID)
	}
	texts, err := s.requirementTexts(ideaID)
	if err != nil || len(texts) == 0 {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT l.requirement_text, c.request_id, c.id, c.number, c.state
		FROM criterion_requirements l JOIN request_criteria c ON c.id=l.criterion_id
		WHERE l.idea_id=? ORDER BY c.request_id, c.number`, ideaID)
	if err != nil {
		return nil, err
	}
	byKey := map[string][]idea.CoverageCriterion{}
	for rows.Next() {
		var text string
		var cc idea.CoverageCriterion
		if err := rows.Scan(&text, &cc.RequestID, &cc.CriterionID, &cc.Number, &cc.State); err != nil {
			rows.Close()
			return nil, err
		}
		key := idea.RequirementKey(text)
		byKey[key] = append(byKey[key], cc)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	waivers := map[string]string{}
	wrows, err := s.db.Query(`SELECT requirement_key, reason FROM idea_requirement_waivers WHERE idea_id=?`, ideaID)
	if err != nil {
		return nil, err
	}
	for wrows.Next() {
		var k, r string
		if err := wrows.Scan(&k, &r); err != nil {
			wrows.Close()
			return nil, err
		}
		waivers[k] = r
	}
	err = wrows.Err()
	wrows.Close()
	if err != nil {
		return nil, err
	}
	out := make([]idea.RequirementCoverage, 0, len(texts))
	for n, text := range texts {
		key := idea.RequirementKey(text)
		cov := idea.RequirementCoverage{No: n + 1, Text: text, Criteria: byKey[key], State: idea.CoverNone}
		if len(cov.Criteria) > 0 {
			cov.State = idea.CoverOpen
			for _, cc := range cov.Criteria {
				if cc.State == "met" {
					cov.State = idea.CoverMet
				}
			}
		}
		if reason, ok := waivers[key]; ok && cov.State != idea.CoverMet {
			cov.State, cov.Reason = idea.CoverWaived, reason
		}
		out = append(out, cov)
	}
	return out, nil
}

// requirementTexts sind die Anforderungen, an denen die Abdeckung hängt: die zum
// Zeitpunkt der aktiven Freigabe (Schnappschuss), danach die, die im Konzept
// später dazukamen. Eine Anforderung, die nach der Freigabe aus dem Konzept
// verschwand, bleibt also in der Liste und muss erfüllt oder erlassen werden.
func (s *Store) requirementTexts(ideaID int64) ([]string, error) {
	c, ok, err := s.ideaConcept(ideaID, 0)
	if err != nil {
		return nil, err
	}
	var current []string
	if ok {
		current = c.Concept.Requirements
	}
	ap, has, err := s.activeApproval(ideaID)
	if err != nil || !has {
		return current, err
	}
	out := append([]string(nil), ap.Requirements...)
	seen := map[string]bool{}
	for _, t := range out {
		seen[idea.RequirementKey(t)] = true
	}
	for _, t := range current {
		if !seen[idea.RequirementKey(t)] {
			seen[idea.RequirementKey(t)] = true
			out = append(out, t)
		}
	}
	return out, nil
}

func uncoveredOf(cov []idea.RequirementCoverage) []idea.RequirementCoverage {
	var out []idea.RequirementCoverage
	for _, c := range cov {
		if !c.Covered() {
			out = append(out, c)
		}
	}
	return out
}

// IdeaCoverage zeigt je Anforderung des Konzepts, ob ein erfülltes Kriterium sie
// deckt oder sie erlassen ist. Kriterien von Issues, die der Betrachter nicht
// lesen darf, fehlen in der Liste, zählen aber für den Stand mit.
func (a *ProjectAccess) IdeaCoverage(id int64) ([]idea.RequirementCoverage, error) {
	if _, err := a.ideaFor(id, ActRead); err != nil {
		return nil, err
	}
	cov, err := a.st.requirementCoverage(id)
	if err != nil {
		return nil, err
	}
	for n := range cov {
		kept := cov[n].Criteria[:0:0]
		for _, c := range cov[n].Criteria {
			if ref, err := a.st.RequestRef(RefRequest, c.RequestID); err == nil && requestReadable(a, ref) {
				kept = append(kept, c)
			}
		}
		cov[n].Criteria = kept
	}
	if cov == nil {
		cov = []idea.RequirementCoverage{}
	}
	return cov, nil
}

// WaiveIdeaRequirement erlässt eine Anforderung ausdrücklich, mit Grund: niemand
// baut sie, und die Idee kann trotzdem umgesetzt werden. Das darf, wer die Idee
// bearbeiten darf (Owner, Maintainer), und nur als Mensch aus einer Websitzung:
// ein Agent, der eine Anforderung erlässt, höbe sein eigenes Hindernis auf.
func (a *ProjectAccess) WaiveIdeaRequirement(id int64, no int, reason string, actor idea.Actor) ([]idea.RequirementCoverage, error) {
	i, err := a.ideaFor(id, ActEdit)
	if err != nil {
		return nil, err
	}
	if actor.Agent {
		return nil, humanOnly("waiving a requirement")
	}
	reason, err = idea.CleanReasonLine(reason, true)
	if err != nil {
		return nil, err
	}
	texts, err := a.st.requirementTexts(id)
	if err != nil {
		return nil, err
	}
	if no < 1 || no > len(texts) {
		return nil, requestdomain.NewRuleError("requirement_not_found", fmt.Sprintf("%s has no requirement %d", i.HumanID(), no), "count from 1 in the requirements of the concept (coverage lists them)", nil)
	}
	text := texts[no-1]
	if err := a.st.commitRequirementWaiver(id, text, reason, actor.Person); err != nil {
		return nil, err
	}
	return a.IdeaCoverage(id)
}

func (s *Store) commitRequirementWaiver(id int64, text, reason, person string) error {
	if s.writer != nil {
		return queueWrite(s, []any{id, text, reason, person}, func(d *Store, p []any) error {
			return d.commitRequirementWaiver(p[0].(int64), p[1].(string), p[2].(string), p[3].(string))
		})
	}
	_, err := s.db.Exec(`INSERT OR REPLACE INTO idea_requirement_waivers(idea_id,requirement_key,requirement_text,reason,person,created_at) VALUES(?,?,?,?,?,?)`,
		id, idea.RequirementKey(text), text, reason, person, now())
	return err
}

// uncoveredCount zählt die unabgedeckten Anforderungen einer Idee; für die
// Ansicht, die sie nur bei einer Idee in Umsetzung oder umgesetzt nennt.
func (s *Store) uncoveredCount(ideaID int64) int {
	cov, err := s.requirementCoverage(ideaID)
	if err != nil {
		return 0
	}
	return len(uncoveredOf(cov))
}

// criterionRequirements liefert für die Kriterien eines Requests die
// Anforderungen, aus denen sie entstanden.
func (s *Store) criterionRequirements(requestID int64) (map[int64]requestdomain.RequirementRef, error) {
	if s.reader != nil {
		return s.reader.criterionRequirements(requestID)
	}
	rows, err := s.db.Query(`SELECT l.criterion_id, l.idea_id, l.requirement_no, l.requirement_text
		FROM criterion_requirements l JOIN request_criteria c ON c.id=l.criterion_id WHERE c.request_id=?`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]requestdomain.RequirementRef{}
	for rows.Next() {
		var id int64
		var r requestdomain.RequirementRef
		if err := rows.Scan(&id, &r.Idea, &r.No, &r.Text); err != nil {
			return nil, err
		}
		out[id] = r
	}
	return out, rows.Err()
}

// attachRequirementRefs setzt an die Kriterien einer Ansicht den Verweis auf die
// Anforderung, soweit der Betrachter die Idee lesen darf.
func (a *ProjectAccess) attachRequirementRefs(d *requestdomain.Detail) {
	refs, err := a.st.criterionRequirements(d.Request.ID)
	if err != nil || len(refs) == 0 {
		return
	}
	visible := map[int64]bool{}
	for n := range d.Criteria {
		ref, ok := refs[d.Criteria[n].ID]
		if !ok {
			continue
		}
		seen, known := visible[ref.Idea]
		if !known {
			i, err := a.st.IdeaByID(ref.Idea)
			seen = err == nil && a.CanSeeIdea(i)
			visible[ref.Idea] = seen
		}
		if seen {
			r := ref
			d.Criteria[n].Requirement = &r
		}
	}
}

// uncoveredForMembers sammelt die unabgedeckten Anforderungen der Ideen, aus
// denen die Requests eines Pakets entstanden, soweit der Betrachter die Ideen
// lesen darf.
func (a *ProjectAccess) uncoveredForMembers(requestIDs []int64) []requestdomain.UncoveredRequirement {
	seen := map[int64]bool{}
	var out []requestdomain.UncoveredRequirement
	for _, rid := range requestIDs {
		ideas, err := a.st.ideasOfRequest(rid)
		if err != nil {
			continue
		}
		for _, i := range ideas {
			if seen[i.ID] || !a.CanSeeIdea(i) {
				continue
			}
			seen[i.ID] = true
			cov, err := a.st.requirementCoverage(i.ID)
			if err != nil {
				continue
			}
			for _, c := range uncoveredOf(cov) {
				out = append(out, requestdomain.UncoveredRequirement{Idea: i.ID, IdeaTitle: i.Title, No: c.No, Text: c.Text, State: c.State})
			}
		}
	}
	return out
}
