package links

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/Deadweight-Labs/ghosttree/internal/feature"
	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/runbook"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Card is the compact description of one object: what it is, where it stands,
// what is next and who is on the move. It lands in an agent's context, so every
// field is one line without control or format characters and has a length cap.
type Card struct {
	Kind   string `json:"kind"`
	Ref    string `json:"ref,omitempty"`
	Title  string `json:"title"`
	Status string `json:"status,omitempty"`
	Next   string `json:"next,omitempty"`
	Who    string `json:"who,omitempty"`
	URL    string `json:"url,omitempty"`
}

const (
	maxTitle = 100
	maxField = 140
)

// Clean makes untrusted text one line: control, format and line-separator
// characters become blanks, runs of blanks one blank, and the text is cut at
// max runes with an ellipsis.
func Clean(s string, max int) string {
	var b strings.Builder
	space := true
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) || unicode.IsSpace(r) {
			if !space {
				b.WriteByte(' ')
				space = true
			}
			continue
		}
		b.WriteRune(r)
		space = false
	}
	out := strings.TrimSpace(b.String())
	if max > 0 {
		if r := []rune(out); len(r) > max {
			out = strings.TrimSpace(string(r[:max-1])) + "…"
		}
	}
	return out
}

// Sanitized returns the card with every field cleaned. The constructors call it;
// a Card built by hand should too before it is printed.
func (c Card) Sanitized() Card {
	c.Kind = Clean(c.Kind, 24)
	c.Ref = Clean(c.Ref, 40)
	c.Title = Clean(c.Title, maxTitle)
	c.Status = Clean(c.Status, 60)
	c.Next = Clean(c.Next, maxField)
	c.Who = Clean(c.Who, 60)
	c.URL = Clean(c.URL, 2048)
	return c
}

// String is the card as text for MCP and CLI: three lines at most.
//
//	Issue REQ-603: Kurzwege zwischen Agent und Mensch
//	state: open | next: 7 criteria open | with: agent
//	link: https://host/ui/owner/repo/issues/603
func (c Card) String() string {
	c = c.Sanitized()
	head := c.Kind
	if c.Ref != "" {
		head += " " + c.Ref
	}
	var b strings.Builder
	b.WriteString(head)
	if c.Title != "" {
		b.WriteString(": " + c.Title)
	}
	var facts []string
	if c.Status != "" {
		facts = append(facts, "state: "+c.Status)
	}
	if c.Next != "" {
		facts = append(facts, "next: "+c.Next)
	}
	if c.Who != "" {
		facts = append(facts, "with: "+c.Who)
	}
	if len(facts) > 0 {
		b.WriteString("\n" + strings.Join(facts, " | "))
	}
	if c.URL != "" {
		b.WriteString("\nlink: " + c.URL)
	}
	return b.String()
}

// Link renders the one-line form for lists: the address, nothing else.
func Link(url string) string {
	if url == "" {
		return ""
	}
	return "link: " + Clean(url, 2048)
}

// RequestCard describes an issue (REQ).
func (b Builder) RequestCard(d requestdomain.Detail) Card {
	r := d.Request
	open, total := 0, len(d.Criteria)
	var firstOpen *requestdomain.Criterion
	for i := range d.Criteria {
		if d.Criteria[i].State == "open" {
			open++
			if firstOpen == nil {
				firstOpen = &d.Criteria[i]
			}
		}
	}
	waiting := 0
	for _, t := range d.Tasks {
		if t.State == humantask.Open || t.State == humantask.Done {
			waiting++
		}
	}
	c := Card{Kind: "Issue", Ref: r.HumanID(), Title: r.Title, Status: r.State, URL: b.WithProject(r.Scope.Project).Issue(r.ID)}
	switch {
	case r.State != "open":
		c.Next = "none"
	case waiting > 0:
		c.Next = fmt.Sprintf("%d human task(s) waiting; %d of %d criteria open", waiting, open, total)
		c.Who = "human"
	case open > 0:
		c.Next = fmt.Sprintf("%d of %d criteria open, first %s %s", open, total, firstOpen.HumanID(), Clean(firstOpen.Description, 60))
		c.Who = "agent"
	case total > 0:
		c.Next = "all criteria settled, complete the request"
		c.Who = "agent"
	default:
		c.Next = "add criteria"
		c.Who = "agent"
	}
	return c.Sanitized()
}

// CriterionCard describes one criterion of an issue.
func (b Builder) CriterionCard(d requestdomain.Detail, number int) (Card, bool) {
	for _, cr := range d.Criteria {
		if cr.Number != number {
			continue
		}
		c := Card{Kind: "Criterion", Ref: cr.HumanID(), Title: cr.Description, Status: cr.State,
			URL: b.WithProject(d.Request.Scope.Project).Criterion(d.Request.ID, cr.Number)}
		if cr.State == "open" {
			c.Next = "meet it with evidence or waive it"
			c.Who = "agent"
		} else {
			c.Next = "none"
		}
		return c.Sanitized(), true
	}
	return Card{}, false
}

// TaskCard describes a human task.
func (b Builder) TaskCard(t humantask.Task) Card {
	c := Card{Kind: "Task", Ref: t.HumanID(), Title: t.Title, Status: t.State, URL: b.WithProject(t.Project).Task(t.ID)}
	who := t.Assignee
	if who == "" {
		who = "human"
	}
	switch t.State {
	case humantask.Open:
		c.Next = "human does: " + firstStep(t)
		c.Who = who
	case humantask.Done:
		c.Next = "agent checks" + hint(t.CheckHint) + " and confirms"
		c.Who = "agent"
	default:
		c.Next = "none"
	}
	return c.Sanitized()
}

func firstStep(t humantask.Task) string {
	if len(t.Steps) > 0 {
		return "1. " + t.Steps[0].Text
	}
	if t.Location != "" {
		return t.Kind + " at " + t.Location
	}
	return t.Kind
}

func hint(h string) string {
	if h == "" {
		return ""
	}
	return " (" + Clean(h, 60) + ")"
}

// PainCard describes a pain.
func (b Builder) PainCard(p pain.Pain) Card {
	c := Card{Kind: "Pain", Ref: p.HumanID(), Title: p.Title, Status: p.State, URL: b.WithProject(p.Project).Pain(p.ID)}
	switch {
	case p.State == "resolved":
		c.Next = "none"
	case p.State == "accepted":
		c.Next = "none; accepted: " + p.AcceptedReason
	case p.Remaining != "":
		c.Next = "remaining: " + p.Remaining
		c.Who = "agent"
	case len(p.Ideas) > 0:
		c.Next = fmt.Sprintf("solution idea IDEA-%d; tie it to a request or resolve it with evidence", p.Ideas[0].ID)
		c.Who = "agent"
	default:
		c.Next = "tie it to a request or an idea, or resolve it with evidence"
		c.Who = "agent"
	}
	return c.Sanitized()
}

// IdeaCard describes an idea. Once it is being worked out, the status is its
// phase (concept, discussion, approved, building, done) and the next step follows
// from it.
func (b Builder) IdeaCard(i idea.Idea) Card {
	c := Card{Kind: "Idea", Ref: fmt.Sprintf("IDEA-%d", i.ID), Title: i.Title, Status: i.State, URL: b.WithProject(i.Project).Idea(i.ID)}
	if i.Phase != "" && i.Phase != idea.PhaseRaw && i.Phase != idea.PhaseDismissed {
		c.Status = i.Phase
	}
	switch {
	case i.State == "dismissed" || i.Phase == idea.PhaseDone:
		c.Next = "none"
	case i.Phase == idea.PhaseConcept || i.Phase == idea.PhaseDiscussion:
		c.Next, c.Who = "discuss it; its owner or a maintainer approves", "human"
	case i.Phase == idea.PhaseApproved:
		c.Next, c.Who = "create the issues from it", "human"
	case i.Phase == idea.PhaseBuilding:
		c.Next = "its issues are in work"
	case i.State == "open":
		c.Next = "decide: promote to an issue or drop"
		c.Who = "human"
	default:
		c.Next = "none"
	}
	if i.OpenDecisions > 0 {
		c.Next += fmt.Sprintf("; %d open decisions", i.OpenDecisions)
	}
	if len(i.Pains) > 0 {
		c.Next += fmt.Sprintf("; motivated by PAIN-%d", i.Pains[0].ID)
	}
	if i.UncoveredRequirements > 0 {
		c.Next += fmt.Sprintf("; %d requirements not covered", i.UncoveredRequirements)
	}
	return c.Sanitized()
}

// FeatureCard describes a feature: its version is the status, the next step is
// the description an idea still has to carry forward, if there is one.
func (b Builder) FeatureCard(f feature.Feature) Card {
	c := Card{Kind: "Feature", Ref: fmt.Sprintf("FEAT-%d", f.ID), Title: f.Name, Status: fmt.Sprintf("version %d", f.Revision), URL: b.WithProject(f.Project).Feature(f.ID)}
	if f.State == feature.Retired {
		c.Status, c.Next = "retired", "none"
		return c.Sanitized()
	}
	c.Next = "none"
	for _, r := range f.Affects {
		if r.Kind == feature.KindIdea && !r.Advanced {
			c.Next, c.Who = fmt.Sprintf("carry the description forward for IDEA-%d (feature action=update idea=%d)", r.ID, r.ID), "agent"
			break
		}
	}
	return c.Sanitized()
}

// DecisionCard describes an open decision: who has to decide is its next step.
func (b Builder) DecisionCard(d idea.Decision) Card {
	c := Card{Kind: "Decision", Ref: fmt.Sprintf("DEC-%d", d.ID), Title: d.Question, Status: d.State, URL: b.WithProject(d.Project).Decision(d.ID)}
	switch {
	case d.State == idea.DecisionOpen && d.Way == idea.WayHuman:
		c.Next, c.Who = "a person decides", "human"
	case d.State == idea.DecisionOpen:
		c.Next, c.Who = "an agent may decide and says why", "agent"
	case d.State == idea.DecisionDeferred:
		c.Next, c.Who = "when it is due: "+d.Trigger, "human"
	default:
		c.Next = "none"
	}
	return c.Sanitized()
}

// KnowledgeCard describes a knowledge entry.
func (b Builder) KnowledgeCard(k store.Knowledge) Card {
	c := Card{Kind: "Knowledge", Ref: fmt.Sprintf("#%d", k.ID), Title: k.Title, Status: k.Type + "/" + k.Status, Who: k.Person,
		URL: b.WithProject(k.Scope.Project).Knowledge(k.ID)}
	if k.Status == "active" {
		c.Next = "none"
	} else {
		c.Next = "check whether it still holds"
	}
	return c.Sanitized()
}

// Comment is the page of a comment: its object, anchored at the comment.
func (b Builder) Comment(kind string, objectID, id int64) string {
	var u string
	switch kind {
	case "request":
		u = b.Issue(objectID)
	case "idea":
		u = b.Idea(objectID)
	case "pain":
		u = b.Pain(objectID)
	}
	if u == "" {
		return ""
	}
	return u + "#comment-" + num(id)
}

// CommentCard describes a comment: where it hangs, who wrote it, the start of
// its text. A deleted comment shows no text.
func (b Builder) CommentCard(c store.Comment) Card {
	ref := map[string]string{"request": "REQ", "idea": "IDEA", "pain": "PAIN"}[c.ObjectKind]
	card := Card{Kind: "Comment", Ref: "#" + num(c.ID), Title: c.Body, Status: fmt.Sprintf("on %s-%d", ref, c.ObjectID), Who: c.Person, Next: "none",
		URL: b.WithProject(c.Project).Comment(c.ObjectKind, c.ObjectID, c.ID)}
	if c.Deleted {
		card.Title, card.Status = "", card.Status+", deleted"
	}
	return card.Sanitized()
}

// MachineCard describes a machine for the person it concerns: who owns it, how
// the viewer stands to it and with whom it is shared.
func (b Builder) MachineCard(m store.Machine) Card {
	owner := m.Owner
	if m.OwnerOrg != "" {
		owner = m.OwnerOrg
	}
	shared := "not shared"
	if len(m.Shares) > 0 {
		var orgs []string
		for _, sh := range m.Shares {
			orgs = append(orgs, sh.Org+" ("+sh.Role+")")
		}
		shared = "shared with " + strings.Join(orgs, ", ")
	}
	return Card{Kind: "Machine", Ref: m.Name, Status: m.Relation, Next: shared, Who: owner, URL: b.Machine(m.Name)}.Sanitized()
}

// DocumentCard describes a document.
func (b Builder) DocumentCard(d store.Document) Card {
	return Card{Kind: "Document", Ref: d.Slug, Title: d.Title, Status: fmt.Sprintf("%s r%d", d.Status, d.HeadRevision), Who: d.Person,
		Next: "none", URL: b.WithProject(d.Project).Document(d.Slug)}.Sanitized()
}

// ThreadCard describes a room thread, optionally a message in it.
func (b Builder) ThreadCard(t store.Thread, message int64) Card {
	c := Card{Kind: "Room", Ref: fmt.Sprintf("thread %d", t.ID), Title: t.Title, Status: t.State, Who: t.Person,
		URL: b.WithProject(t.Project).Room(t.ID, message)}
	if t.State == "open" {
		c.Next = "answer or resolve"
	} else {
		c.Next = "none"
	}
	return c.Sanitized()
}

// MessageCard describes a message in a room: the start of its text, the room,
// who said it. The link leads to the room.
func (b Builder) MessageCard(m store.MessageHit) Card {
	room := m.RoomLabel
	if room == "" {
		room = m.RoomKey
	}
	return Card{Kind: "Message", Ref: fmt.Sprintf("#%d", m.ID), Title: m.Body, Status: "in " + room, Who: m.Sender,
		Next: "none", URL: b.RoomChat(m.RoomKey)}.Sanitized()
}

// SessionCard describes a session.
func (b Builder) SessionCard(s store.Session) Card {
	id := s.PublicID
	if id == "" {
		id = num(s.ID)
	}
	status := s.Harness
	if s.LastSeenAt != "" {
		status += ", last seen " + s.LastSeenAt
	}
	return Card{Kind: "Session", Ref: id, Title: s.Title, Status: status, Who: s.Owner,
		URL: b.WithProject(s.Scope.Project).Session(id)}.Sanitized()
}

// PackageCard describes a work package ("checklist" in the interface).
func (b Builder) PackageCard(p requestdomain.PackageDetail) Card {
	open := 0
	for _, m := range p.Members {
		if m.Role == requestdomain.RolePflicht && m.State == "open" {
			open++
		}
	}
	pb := b.WithProject(p.Project)
	u := pb.PackageN(p.ID)
	if p.PR != 0 {
		u = pb.PullChecklist(p.PR)
	}
	c := Card{Kind: "Checklist", Ref: p.HumanID(), Title: p.Name, Status: p.State, URL: u}
	if open > 0 {
		c.Next = fmt.Sprintf("%d required issue(s) still open", open)
		c.Who = "agent"
	} else {
		c.Next = "none"
	}
	return c.Sanitized()
}

// RunbookCard describes a runbook.
func (b Builder) RunbookCard(s runbook.Summary) Card {
	c := Card{Kind: "Runbook", Ref: s.Name, Title: s.Purpose, Status: fmt.Sprintf("head v%d, approved v%d", s.Head, s.ApprovedVersion),
		URL: b.WithProject(s.Project).Runbook(s.Name)}
	if s.ApprovedVersion < s.Head {
		c.Next = "a human approves the newest version"
		c.Who = "human"
	} else {
		c.Next = "none"
	}
	return c.Sanitized()
}

// PresetCard describes a saved search.
func (b Builder) PresetCard(p store.SearchPreset, args map[string]string) Card {
	return Card{Kind: "Search", Ref: "preset:" + p.Name, Title: p.Description, Status: p.Scope, Next: "none",
		URL: b.Preset(p.Scope, p.Name, args)}.Sanitized()
}

// SummaryOf reduces a runbook view to the line of the list, for its card.
func SummaryOf(v runbook.View) runbook.Summary {
	return runbook.Summary{ID: v.ID, Name: v.Name, Project: v.Project, Machine: v.Machine, Purpose: v.Spec.Purpose, Head: v.Head, ApprovedVersion: v.ApprovedVersion}
}

// PresetCardFor finds the saved search name among those the account sees in
// project and describes it with the values the run was given. No card when it
// cannot be found: a card needs the scope of the search for its address.
func PresetCardFor(api interface {
	ListPresets(project string) ([]store.SearchPreset, error)
}, b Builder, project, name string, args map[string]string) (Card, bool) {
	list, err := api.ListPresets(project)
	if err != nil {
		return Card{}, false
	}
	for _, p := range list {
		if p.Name == name {
			c := b.WithProject(project).PresetCard(p, args)
			return c, c.URL != ""
		}
	}
	return Card{}, false
}
