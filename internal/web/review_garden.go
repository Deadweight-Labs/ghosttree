package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The gardener's morning report: the notes it filed overnight (harness
// "gardener", staged, never verified) as one block at the top of the review
// page. The entry a note is about stays as it is until somebody decides; the
// gardener itself has no way to change it. Everything here is built from the
// entries the review page already kept for the viewer, so a hidden project
// never reaches the block, and the buttons only offer what the viewer may do.

const (
	gardenReasonRunes = 280
	// gardenRunWindow: checks within this long of the newest one count as the
	// same run, "checked overnight".
	gardenRunWindow = 6 * time.Hour
)

// gardenRow is one note of the gardener: verdict, the entry it is about, why.
type gardenRow struct {
	ID, Href, Project, Verdict, Subject, Reason, Age string
	Old                                              bool
	Explain                                          *explainView
	CanApprove, CanReject                            bool
	Token, Next, Filter                              string
	// Lines are what the note holds beyond its reason (the test, the sketch);
	// Refs the entries a merge keeps and replaces; Act the one action besides
	// rejecting, for a note that has one (see review_garden_actions.go).
	Lines []string
	Refs  []gardenRef
	Act   *gardenAct
}

// Selectable: the row has a tick box, because at least one bulk action is open.
func (g gardenRow) Selectable() bool { return g.CanApprove || g.CanReject }

type gardenView struct {
	Rows []gardenRow
	// Title says how many notes there are, Source how much the gardener
	// looked at and when.
	Title, Source string
	// ApproveIDs and RejectIDs are the notes the viewer may act on in bulk.
	ApproveIDs, RejectIDs []string
	Token, Filter         string
	Any                   bool
	// AllReject and AllApprove are the two buttons of the report card; nil
	// where the viewer may act on none of the notes.
	AllReject, AllApprove *gardenAll
}

// gardenAll is the form behind a button of the report card: one verdict for a
// fixed set of notes.
type gardenAll struct {
	Token, Filter, Verdict, Label string
	IDs                           []string
	Primary                       bool
}

// gardenRows builds the block from the pending entries of the gardener.
func (a *app) gardenBlock(r *http.Request, pa *store.ProjectAccess, project string, notes []store.Knowledge) *gardenView {
	if len(notes) == 0 {
		return nil
	}
	l := a.langFor(r)
	now := overviewNow().UTC()
	v := &gardenView{Token: csrfOf(r), Filter: project}
	for _, k := range notes {
		view := pa.KnowledgeView(k)
		card := a.cardFor(r, pa, k, project == "", reviewBodyRunes, "review", project)
		verdict, subject := parseGardenTitle(l, view.Title)
		created := firstNonEmpty(k.ObservedAt, k.CreatedAt)
		row := gardenRow{
			ID: card.ID, Href: card.Href, Project: lastSegment(k.Scope.Project), Verdict: verdict, Subject: subject,
			Reason: ellipsize(gardenReason(view.Body), gardenReasonRunes), Age: card.Age, Old: olderThan(created, now, reviewOldAfter),
			Explain: card.Explain, CanApprove: card.CanApprove, CanReject: card.CanReject,
			Token: v.Token, Next: "review", Filter: project,
		}
		a.decorateGardenRow(l, pa, k, &row)
		if row.CanApprove {
			v.ApproveIDs = append(v.ApproveIDs, row.ID)
		}
		if row.CanReject {
			v.RejectIDs = append(v.RejectIDs, row.ID)
		}
		v.Rows = append(v.Rows, row)
	}
	v.Any = len(v.ApproveIDs) > 0 || len(v.RejectIDs) > 0
	if len(v.RejectIDs) > 0 {
		v.AllReject = &gardenAll{Token: v.Token, Filter: project, Verdict: "reject", IDs: v.RejectIDs, Label: l.msg("review.garden.reject_all")}
	}
	if len(v.ApproveIDs) > 0 {
		v.AllApprove = &gardenAll{Token: v.Token, Filter: project, Verdict: "approve", IDs: v.ApproveIDs, Label: l.msg("review.garden.approve_all", len(v.ApproveIDs)), Primary: true}
	}
	v.Title = l.msg("review.garden.title", len(v.Rows))
	if len(v.Rows) == 1 {
		v.Title = l.msg("review.garden.title_one")
	}
	if n, newest := a.gardenChecked(pa, project); n > 0 {
		key := "review.garden.checked"
		if n == 1 {
			key = "review.garden.checked_one"
		}
		v.Source = l.msg(key, n, shortAgo(l, now, newest))
	}
	return v
}

// gardenChecked counts the entries the gardener looked at in its latest run,
// in the projects the viewer may see, and when that was.
func (a *app) gardenChecked(pa *store.ProjectAccess, project string) (int, time.Time) {
	var times []time.Time
	var newest time.Time
	for _, p := range visibleProjects(pa, project) {
		checks, err := a.store.GardenChecks(p)
		if err != nil {
			continue
		}
		for _, c := range checks {
			if t := parseTime(c.CheckedAt); !t.IsZero() {
				times = append(times, t)
				if t.After(newest) {
					newest = t
				}
			}
		}
	}
	n := 0
	for _, t := range times {
		if newest.Sub(t) <= gardenRunWindow {
			n++
		}
	}
	return n, newest
}

// parseGardenTitle splits "[Gärtner] Veraltet? #12: Titel" into the verdict
// (in the language of the page) and the title of the entry it is about. A title
// in another shape is shown whole.
func parseGardenTitle(l lang, title string) (verdict, subject string) {
	rest := strings.TrimSpace(strings.TrimPrefix(title, "[Gärtner]"))
	label, after, ok := strings.Cut(rest, " #")
	if !ok {
		return l.msg("review.garden.verdict"), rest
	}
	_, subject, ok = strings.Cut(after, ": ")
	if !ok {
		return l.msg("review.garden.verdict"), rest
	}
	switch strings.TrimSpace(label) {
	case "Veraltet?":
		return l.msg("review.garden.stale"), subject
	case "Korrektur?":
		return l.msg("review.garden.correction"), subject
	case "Duplikat?":
		return l.msg("review.garden.duplicate"), subject
	case "Widerspruch?":
		return l.msg("review.garden.contradiction"), subject
	case "Test vorhanden?":
		return l.msg("review.garden.cover_existing"), subject
	case "Test fehlt?":
		return l.msg("review.garden.cover_sketch"), subject
	case "Nicht testbar?":
		return l.msg("review.garden.cover_untestable"), subject
	}
	return strings.TrimSpace(label), subject
}

// gardenReason is the gardener's reason: the text after "Begründung:" up to the
// next paragraph, in one line.
func gardenReason(body string) string {
	_, after, ok := strings.Cut(body, "Begründung:")
	if !ok {
		return ""
	}
	if i := strings.Index(after, "\n\n"); i >= 0 {
		after = after[:i]
	}
	return oneLineText(after)
}

// bulkResult is the line after a bulk decision.
func (a *app) bulkResult(r *http.Request) *resultLine {
	l := a.langFor(r)
	kind := r.URL.Query().Get("done")
	n, err := strconv.Atoi(r.URL.Query().Get("n"))
	if err != nil || n < 1 || n > bulkMax {
		return nil
	}
	switch kind {
	case "bulk_approve":
		return &resultLine{Kind: "approved", Text: l.msg("review.bulk.approved", n)}
	case "bulk_reject":
		return &resultLine{Kind: "rejected", Text: l.msg("review.bulk.rejected", n)}
	}
	return nil
}
