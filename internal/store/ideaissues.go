package store

import (
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Aus der freigegebenen Idee werden Issues (REQ-611), und der Agent, der daran
// arbeitet, bekommt Kurzfassung, Entscheidungen und offene Fragen im Briefing.

// IssueSpec beschreibt ein Issue, das aus einer Idee entsteht.
type IssueSpec struct {
	Type        string `json:"type,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Priority    string `json:"priority,omitempty"`
	// Criteria sind eigene Kriterien, Requirements die Nummern (ab 1) der
	// Anforderungen des Konzepts, die als Kriterien übernommen werden.
	Criteria     []string `json:"criteria,omitempty"`
	Requirements []int    `json:"requirements,omitempty"`
}

// IdeaIssuesInput legt Issues aus einer Idee an. Ohne Specs entsteht ein Issue mit
// dem Titel der Idee und allen Anforderungen als Kriterien.
type IdeaIssuesInput struct {
	Idea   int64
	Issues []IssueSpec
	// Branch bindet die Issues an ein Arbeitspaket dieses Branches (neu oder
	// vorhanden); PackageName benennt ein neues.
	Branch      string
	PackageName string
	Actor       idea.Actor
}

// IdeaIssuesResult sind die entstandenen Issues und das Paket.
type IdeaIssuesResult struct {
	Idea     idea.Idea                    `json:"idea"`
	Requests []int64                      `json:"requests"`
	Package  *requestdomain.PackageDetail `json:"package,omitempty"`
}

const maxIssuesFromIdea = 10

// CreateIssuesFromIdea macht aus einer freigegebenen Idee ein oder mehrere
// Issues mit Kriterien aus den Anforderungen und verknüpft sie mit der Idee (die
// Idee geht auf „in Umsetzung“, jedes Issue nennt sie in der Beschreibung und
// über die Verknüpfung). Das darf der Owner der Idee oder ein Maintainer, und wer
// Requests im Projekt anlegen darf. Mit Branch landen die Issues in einem
// Arbeitspaket. Entsteht ein Issue und scheitert danach ein Schritt, bleibt das
// Entstandene verknüpft und die Fehlermeldung nennt es.
func (a *ProjectAccess) CreateIssuesFromIdea(in IdeaIssuesInput) (IdeaIssuesResult, error) {
	i, err := a.ideaFor(in.Idea, ActEdit)
	if err != nil {
		return IdeaIssuesResult{}, err
	}
	if i.Phase != idea.PhaseApproved && i.Phase != idea.PhaseBuilding {
		return IdeaIssuesResult{}, requestdomain.NewRuleError("not_approved", fmt.Sprintf("%s is %s, not approved", i.HumanID(), i.Phase),
			"approve it first (its owner or a maintainer); a small idea without a concept can be promoted directly with idea_update promote", nil)
	}
	// Nicht die Phase allein: der Freigabe-Marker, den nur ein Mensch setzt.
	if _, approved, err := a.st.activeApproval(i.ID); err != nil {
		return IdeaIssuesResult{}, err
	} else if !approved {
		return IdeaIssuesResult{}, requestdomain.NewRuleError("not_approved", fmt.Sprintf("%s carries no approval of a person", i.HumanID()),
			"a person approves it in the web interface: its owner or a maintainer", nil)
	}
	if err := a.Check(i.Project, ResRequest, ActCreate, Object{Own: true}); err != nil {
		return IdeaIssuesResult{}, err
	}
	concept, _, err := a.st.ideaConcept(i.ID, 0)
	if err != nil {
		return IdeaIssuesResult{}, err
	}
	specs := in.Issues
	if len(specs) == 0 {
		all := make([]int, len(concept.Concept.Requirements))
		for n := range all {
			all[n] = n + 1
		}
		specs = []IssueSpec{{Requirements: all}}
	}
	if len(specs) > maxIssuesFromIdea {
		return IdeaIssuesResult{}, requestdomain.NewRuleError("too_many_issues", fmt.Sprintf("at most %d issues at once", maxIssuesFromIdea), "create the rest in a second call", nil)
	}
	type plan struct {
		spec     IssueSpec
		criteria []string
		// reqNo sind die Nummern der Anforderungen, aus denen die ersten Kriterien
		// stammen, in der Reihenfolge der Kriterien.
		reqNo []int
	}
	plans := make([]plan, 0, len(specs))
	for _, sp := range specs {
		p := plan{spec: sp}
		for _, n := range sp.Requirements {
			if n < 1 || n > len(concept.Concept.Requirements) {
				return IdeaIssuesResult{}, requestdomain.NewRuleError("requirement_not_found", fmt.Sprintf("the concept has no requirement %d", n),
					fmt.Sprintf("it has %d; count from 1", len(concept.Concept.Requirements)), nil)
			}
			p.criteria = append(p.criteria, concept.Concept.Requirements[n-1])
			p.reqNo = append(p.reqNo, n)
		}
		extra, err := idea.CleanRequirements(sp.Criteria)
		if err != nil {
			return IdeaIssuesResult{}, err
		}
		p.criteria = append(p.criteria, extra...)
		if strings.TrimSpace(sp.Title) != "" {
			if p.spec.Title, err = idea.CleanOneLine(sp.Title, "title", "the issue title", idea.MaxTitle, true); err != nil {
				return IdeaIssuesResult{}, err
			}
		} else {
			p.spec.Title = i.Title
		}
		plans = append(plans, p)
	}
	var res IdeaIssuesResult
	for _, p := range plans {
		typ := p.spec.Type
		if typ == "" {
			typ = "feature"
		}
		desc := strings.TrimSpace(p.spec.Description)
		if desc == "" {
			desc = idea.Brief(concept.Concept.About, 600)
		}
		desc = strings.TrimSpace(desc + "\n\nFrom " + i.HumanID() + ": " + i.Title + ". Read the concept and the decisions there before you build (idea_get).")
		d, err := a.st.CreateRequest(requestdomain.CreateInput{
			Request: requestdomain.Request{Type: typ, Title: p.spec.Title, Description: desc, Priority: p.spec.Priority,
				Scope: scope.Axes{Project: i.Project}, Origin: agentOrigin(in.Actor.Agent), Person: in.Actor.Person, SessionRef: in.Actor.SessionRef},
			Criteria: p.criteria, AuthorID: in.Actor.AccountID, GitAnchor: in.Actor.GitAnchor, Origin: in.Actor.Origin,
		})
		if err != nil {
			return res, partialIssues(res.Requests, err)
		}
		res.Requests = append(res.Requests, d.Request.ID)
		if err := a.st.commitIdeaLink(i.ID, d.Request.ID, in.Actor.Person); err != nil {
			return res, partialIssues(res.Requests, err)
		}
		// Jedes Kriterium aus einer Anforderung verweist auf sie.
		var links []criterionRequirement
		for n, no := range p.reqNo {
			if n < len(d.Criteria) {
				links = append(links, criterionRequirement{CriterionID: d.Criteria[n].ID, No: no, Text: concept.Concept.Requirements[no-1]})
			}
		}
		if err := a.st.commitCriterionRequirements(i.ID, links); err != nil {
			return res, partialIssues(res.Requests, err)
		}
	}
	if branch := strings.TrimSpace(in.Branch); branch != "" {
		members := make([]requestdomain.PackageMember, 0, len(res.Requests))
		for _, id := range res.Requests {
			members = append(members, requestdomain.PackageMember{RequestID: id, Role: requestdomain.RolePflicht})
		}
		pkg, err := a.packageForIdeaBranch(i, branch, in, members)
		if err != nil {
			return res, partialIssues(res.Requests, err)
		}
		res.Package = &pkg
	}
	res.Idea, err = a.IdeaView(i.ID)
	return res, err
}

func partialIssues(ids []int64, err error) error {
	if len(ids) == 0 {
		return err
	}
	names := make([]string, len(ids))
	for n, id := range ids {
		names[n] = fmt.Sprintf("REQ-%d", id)
	}
	return fmt.Errorf("%s were created and linked to the idea, then it failed: %w (do not create them again)", strings.Join(names, ", "), err)
}

func (a *ProjectAccess) packageForIdeaBranch(i idea.Idea, branch string, in IdeaIssuesInput, members []requestdomain.PackageMember) (requestdomain.PackageDetail, error) {
	if existing, err := a.st.PackageForBranch(i.Project, branch, nil); err == nil && existing.State == requestdomain.PackageOpen && existing.InheritedFrom == "" {
		return a.st.AddPackageRequests(existing.ID, members, "from "+i.HumanID(), in.Actor.Person)
	}
	name := strings.TrimSpace(in.PackageName)
	if name == "" {
		name = idea.Brief(i.Title, 150)
	}
	return a.st.CreatePackage(requestdomain.PackageInput{Project: i.Project, Branch: branch, Name: name, Members: members, Person: in.Actor.Person})
}

// ---- Briefing

// IdeaBriefingForRequest ist der Abschnitt, den ein Agent bekommt, der an einem
// Issue arbeitet, das aus einer Idee entstand: Kurzfassung des Konzepts, die
// getroffenen Entscheidungen, die offenen Fragen und die vertagten Entscheidungen,
// deren Auslöser jetzt passt. Alles einzeilig bereinigt und auf budget Zeichen
// begrenzt; was nicht mehr passt, steht als Zahl da. Leer, wenn das Issue weder
// aus einer lesbaren Idee stammt noch eigene Entscheidungen trägt.
func (a *ProjectAccess) IdeaBriefingForRequest(requestID int64, budget int) (string, error) {
	ref, err := a.st.RequestRef(RefRequest, requestID)
	if err != nil || !requestReadable(a, ref) {
		a.Filtered()
		return "", requestNotInProject(requestID)
	}
	ideas, err := a.st.ideasOfRequest(requestID)
	if err != nil {
		return "", err
	}
	var visible []idea.Idea
	for _, i := range ideas {
		if a.CanSeeIdea(i) {
			visible = append(visible, i)
		}
	}
	own, err := a.st.decisionsWhere(`subject_kind='request' AND subject_id=? ORDER BY id`, requestID)
	if err != nil {
		return "", err
	}
	if len(visible) == 0 && len(own) == 0 {
		return "", nil
	}
	var b strings.Builder
	limit := func(line string) bool {
		if b.Len()+len(line) > budget {
			return false
		}
		b.WriteString(line)
		return true
	}
	b.WriteString("## Idea context (ghosttree)\n")
	var decided, open []string
	context := []string{ref.Project, a.st.requestTitle(requestID)}
	if pk, err := a.PackagesOfRequest(requestID); err == nil {
		for _, p := range pk {
			context = append(context, p.Name, p.Branch)
		}
	}
	for n, i := range visible {
		if n >= 2 {
			break
		}
		context = append(context, i.Title)
		fmt.Fprintf(&b, "%s [%s] %s\n", i.HumanID(), i.Phase, idea.Brief(i.Title, 150))
		summary := ""
		if t, err := a.st.ideaTextAt(i.ID, idea.PartSummary, 0); err == nil {
			summary = t.Body
		} else if c, ok, _ := a.st.ideaConcept(i.ID, 0); ok {
			summary = c.Concept.About
		}
		if summary != "" {
			limit("Summary: " + idea.Brief(summary, 700) + "\n")
		}
		if c, ok, _ := a.st.ideaConcept(i.ID, 0); ok && strings.TrimSpace(c.Concept.Not) != "" {
			limit("Not in scope: " + idea.Brief(c.Concept.Not, 300) + "\n")
		}
		ds, err := a.st.decisionsWhere(`subject_kind='idea' AND subject_id=? ORDER BY id`, i.ID)
		if err != nil {
			return "", err
		}
		own = append(ds, own...)
	}
	for _, d := range own {
		switch d.State {
		case idea.DecisionDecided:
			by := ""
			if d.ByAgent {
				by = " (decided by an agent, not confirmed)"
			}
			decided = append(decided, fmt.Sprintf("- %s %s: %s. Reason: %s%s\n", d.HumanID(), idea.Brief(d.Question, 160), idea.Brief(decisionOutcome(d), 240), idea.Brief(d.Reason, 200), by))
		case idea.DecisionOpen:
			who := "a person decides"
			if d.Way == idea.WayAgent {
				who = "agent may decide and must say why"
			}
			open = append(open, fmt.Sprintf("- %s %s (%s)\n", d.HumanID(), idea.Brief(d.Question, 200), who))
		}
	}
	for n, i := range visible {
		if n >= 2 {
			break
		}
		if c, ok, _ := a.st.ideaConcept(i.ID, 0); ok && strings.TrimSpace(c.Concept.Questions) != "" {
			open = append(open, "- (concept) "+idea.Brief(c.Concept.Questions, 300)+"\n")
		}
	}
	section := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		if !limit(title + "\n") {
			return
		}
		for n, l := range lines {
			if !limit(l) {
				limit(fmt.Sprintf("- and %d more (decision_list)\n", len(lines)-n))
				return
			}
		}
	}
	section("Decisions already made, do not ask again:", decided)
	section("Open questions:", open)
	if due, err := a.DueDecisions(ref.Project, context...); err == nil {
		var lines []string
		for _, d := range due {
			lines = append(lines, fmt.Sprintf("- %s %s (deferred until: %s)\n", d.HumanID(), idea.Brief(d.Question, 160), idea.Brief(d.Trigger, 120)))
		}
		section("Deferred decisions that may be due now:", lines)
	}
	limit("Ask what is unclear as an open decision on the idea (decision_open with via_request), not in the chat.\n")
	return b.String(), nil
}

func decisionOutcome(d idea.Decision) string {
	var parts []string
	for _, v := range d.Variants {
		if v.Chosen {
			parts = append(parts, "variant "+v.Label+": "+v.Title)
		}
	}
	if d.Answer != "" {
		parts = append(parts, d.Answer)
	}
	return strings.Join(parts, " ")
}

// IdeaOfRequest nennt die erste lesbare Idee, aus der ein Request entstand
// (für Rückfragen, die an der Idee landen sollen).
func (a *ProjectAccess) IdeaOfRequest(requestID int64) (idea.Idea, bool) {
	ref, err := a.st.RequestRef(RefRequest, requestID)
	if err != nil || !requestReadable(a, ref) {
		a.Filtered()
		return idea.Idea{}, false
	}
	list, err := a.st.ideasOfRequest(requestID)
	if err != nil {
		return idea.Idea{}, false
	}
	for _, i := range list {
		if a.CanSeeIdea(i) {
			return i, true
		}
	}
	return idea.Idea{}, false
}
