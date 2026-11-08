package store

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Der Posteingang eines Menschen sind drei Quellen: Aufgaben, die Agenten ihm
// stellen (human_tasks), Entscheidungen, die auf einen Menschen warten
// (open_decisions mit decider human), und Fragen, Freigaben, Blocker und
// Übergaben an ihn (coord_attention). Keine dieser Quellen löscht Erledigtes:
// eine bestätigte oder verworfene Aufgabe, eine geschlossene Entscheidung und
// ein beantwortetes Attention-Item (dessen Quellnachricht die Aufbewahrung des
// Chats festhält) bleiben in der Datenbank. Verschwunden waren sie nur aus den
// Ansichten, die nach "offen" filtern. Inbox ist die eine Lesefunktion, die
// beide Seiten zeigt.

// InboxDoneDays ist, wie weit zurück die Liste der Erledigten reicht, wenn der
// Aufrufer nichts anderes sagt; InboxMaxDays ist die obere Grenze. Das ist ein
// Fenster der Ansicht: gelöscht wird hier nichts.
const (
	InboxDoneDays = 30
	InboxMaxDays  = 365
)

// Arten von Posteingangs-Einträgen.
const (
	InboxTask      = "task"
	InboxDecision  = "decision"
	InboxAttention = "attention"
)

// Zustände eines Eintrags: offen oder erledigt.
const (
	InboxOpen = "open"
	InboxDone = "done"
)

// InboxEntry ist ein Eintrag des Posteingangs, gleich aus welcher Quelle. Ref
// nennt das Objekt (TASK-n, DEC-n; bei einer Frage attention:n), die Felder
// darunter, was die Oberfläche zum Verlinken braucht.
type InboxEntry struct {
	Kind    string `json:"kind"`
	ID      int64  `json:"id"`
	Ref     string `json:"ref"`
	Project string `json:"project,omitempty"`
	Title   string `json:"title"`
	// Reason ist die Art innerhalb der Quelle: bei einer Aufgabe secret, file,
	// input oder action, bei einer Entscheidung human, bei einer Frage question,
	// approval, blocker oder handoff.
	Reason string `json:"reason,omitempty"`
	State  string `json:"state"`
	// How sagt bei einem erledigten Eintrag, wie er erledigt wurde: done,
	// confirmed, dropped (Aufgabe), decided, deferred, dropped (Entscheidung),
	// resolved, dismissed, expired (Frage).
	How       string `json:"how,omitempty"`
	CreatedAt string `json:"created_at"`
	// At ist die Zeit, nach der sortiert wird: bei offenen die Zeit des Wartens
	// (seit wann), bei erledigten die der Erledigung.
	At     string `json:"at"`
	DoneBy string `json:"done_by,omitempty"`
	// Verweise zum Verlinken, je nach Quelle.
	RequestID       int64  `json:"request_id,omitempty"`
	SubjectKind     string `json:"subject_kind,omitempty"`
	SubjectID       int64  `json:"subject_id,omitempty"`
	MessageID       int64  `json:"message_id,omitempty"`
	HomeRoomKey     string `json:"home_room_key,omitempty"`
	DestinationKind string `json:"destination_kind,omitempty"`
	DestinationID   string `json:"destination_id,omitempty"`
}

// InboxFilter wählt aus dem Posteingang.
type InboxFilter struct {
	// State: open (Standard), done oder all.
	State string
	// Project begrenzt auf ein Projekt; leer heißt alle, die der Betrachter sieht.
	Project string
	// Days ist das Fenster der Erledigten in Tagen (Standard InboxDoneDays, höchstens
	// InboxMaxDays). Offene Einträge haben kein Fenster.
	Days int
	// Mine begrenzt Aufgaben auf die, die dem Betrachter zugewiesen sind (oder
	// niemandem, solange offen), bei Erledigten auf die, die er erledigt hat.
	Mine bool
	// Limit je Quelle und Zustand, höchstens 200 (Standard 100).
	Limit int
	// Now ist für Tests; leer heißt jetzt.
	Now time.Time
}

func oneLine(s string, max int) string {
	var b strings.Builder
	space := true
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
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
	if r := []rune(out); max > 0 && len(r) > max {
		out = strings.TrimSpace(string(r[:max-1])) + "…"
	}
	return out
}

// Inbox liefert die Einträge des Posteingangs für den Betrachter, offene zuerst
// (die am längsten wartenden vorn), danach die erledigten, die jüngsten vorn.
// Aufgaben und Entscheidungen gehen durch dieselben Sichtprüfungen wie ihre
// eigenen Listen (ListHumanTasks, SearchDecisions); Fragen liefert ca, das nur
// zeigt, was der Betrachter in seinen Räumen lesen darf, und nur die an ihn
// gerichteten. Ein ca ohne Zugang zu Räumen (ein Konto ohne Agenten) trägt nichts
// bei, ohne dass die anderen Quellen leiden.
func (a *ProjectAccess) Inbox(ca CoordAccess, f InboxFilter) ([]InboxEntry, error) {
	state := f.State
	switch state {
	case "":
		state = InboxOpen
	case InboxOpen, InboxDone, "all":
	default:
		return nil, fmt.Errorf("unknown inbox state %q: use open, done or all", f.State)
	}
	days := f.Days
	if days <= 0 {
		days = InboxDoneDays
	}
	days = min(days, InboxMaxDays)
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	nowT := f.Now
	if nowT.IsZero() {
		nowT = time.Now()
	}
	since := nowT.UTC().Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339)
	wantOpen, wantDone := state == InboxOpen || state == "all", state == InboxDone || state == "all"

	var projects []string
	if p := scope.NormalizeRemote(strings.TrimSpace(f.Project)); p != "" {
		projects = []string{p}
	} else {
		projects = slices.Sorted(slices.Values(a.Projects()))
	}

	var open, done []InboxEntry
	for _, project := range projects {
		if wantOpen {
			tasks, err := a.st.ListHumanTasks(a, TaskFilter{Project: project, State: humantask.Open, Mine: f.Mine, Limit: limit})
			if err != nil {
				return nil, err
			}
			for _, t := range tasks {
				open = append(open, taskEntry(t, InboxOpen))
			}
			decisions, err := a.SearchDecisions(DecisionFilter{Project: project, NeedsHuman: true, Limit: limit})
			if err != nil {
				return nil, err
			}
			for _, d := range decisions {
				open = append(open, decisionEntry(d, InboxOpen))
			}
		}
		if wantDone {
			tasks, err := a.st.ListHumanTasks(a, TaskFilter{Project: project, State: "closed", Since: since, Mine: f.Mine, Limit: limit})
			if err != nil {
				return nil, err
			}
			for _, t := range tasks {
				done = append(done, taskEntry(t, InboxDone))
			}
			// Entscheidungen, die auf einen Menschen warteten und es nicht mehr
			// tun. Eine vertagte kehrt zu ihrer Zeit zurück; bis dahin steht sie hier.
			decisions, err := a.SearchDecisions(DecisionFilter{Project: project, State: "all", Limit: 200})
			if err != nil {
				return nil, err
			}
			for _, d := range decisions {
				if d.State == idea.DecisionOpen || d.Decider != idea.WayHuman {
					continue
				}
				if e := decisionEntry(d, InboxDone); e.At >= since {
					done = append(done, e)
				}
			}
		}
	}

	if !ca.isZero() {
		items, err := ca.Attention()
		switch {
		case errors.Is(err, ErrCoordNotFound), errors.Is(err, ErrCoordForbidden):
			// Kein Zugang zu Räumen: keine Fragen, die anderen Quellen bleiben.
		case err != nil:
			return nil, err
		default:
			for _, item := range items {
				if !item.IsRecipient {
					continue
				}
				if f.Project != "" && item.HomeRoomKey != RoomKeyForProject(scope.NormalizeRemote(f.Project)) {
					continue
				}
				switch {
				case item.State == AttentionOpen && wantOpen:
					open = append(open, attentionEntry(item, InboxOpen))
				case item.State != AttentionOpen && wantDone:
					if e := attentionEntry(item, InboxDone); e.At >= since {
						done = append(done, e)
					}
				}
			}
		}
	}

	// Offene: wer am längsten wartet, zuerst. Erledigte: das Jüngste zuerst.
	slices.SortStableFunc(open, func(x, y InboxEntry) int { return strings.Compare(x.At, y.At) })
	slices.SortStableFunc(done, func(x, y InboxEntry) int { return strings.Compare(y.At, x.At) })
	out := make([]InboxEntry, 0, len(open)+len(done))
	out = append(out, open...)
	out = append(out, done...)
	return out, nil
}

func (c CoordAccess) isZero() bool { return c.Store == nil }

func taskEntry(t humantask.Task, state string) InboxEntry {
	e := InboxEntry{Kind: InboxTask, ID: t.ID, Ref: t.HumanID(), Project: t.Project, Title: oneLine(t.Title, 200), Reason: t.Kind,
		State: state, CreatedAt: t.CreatedAt, At: t.CreatedAt, RequestID: t.RequestID}
	if state == InboxDone {
		e.How, e.DoneBy = t.State, t.DoneBy
		e.At = t.DoneAt
		if e.At == "" {
			e.At = t.UpdatedAt
		}
		if t.State == humantask.Dropped {
			e.DoneBy = ""
		}
	}
	return e
}

func decisionEntry(d idea.Decision, state string) InboxEntry {
	e := InboxEntry{Kind: InboxDecision, ID: d.ID, Ref: fmt.Sprintf("DEC-%d", d.ID), Project: d.Project, Title: oneLine(d.Question, 200),
		Reason: d.Decider, State: state, CreatedAt: d.CreatedAt, At: d.CreatedAt, SubjectKind: d.Subject, SubjectID: d.SubjectID}
	if state == InboxDone {
		e.How, e.DoneBy = d.State, d.DecidedBy
		if e.At = d.DecidedAt; e.At == "" {
			e.At = d.UpdatedAt
		}
	}
	return e
}

func attentionEntry(item AttentionItem, state string) InboxEntry {
	e := InboxEntry{Kind: InboxAttention, ID: item.ID, Ref: fmt.Sprintf("attention:%d", item.ID), Title: oneLine(item.Body, 200), Reason: item.Reason,
		State: state, CreatedAt: item.CreatedAt, At: item.CreatedAt, MessageID: item.MessageID, HomeRoomKey: item.HomeRoomKey,
		DestinationKind: item.DestinationKind, DestinationID: item.DestinationID}
	if rest, ok := strings.CutPrefix(item.HomeRoomKey, "project:"); ok {
		e.Project = rest
	}
	if state == InboxDone {
		e.How = item.State
		if item.ResolvedAt != "" {
			e.At = item.ResolvedAt
		}
	}
	return e
}
