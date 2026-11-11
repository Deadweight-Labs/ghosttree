package links

import (
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/feature"
	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/knref"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Reader ist, was Peek liest: ein Objekt je Verweisart, im Namen des Aufrufers.
// Ein Objekt, das er nicht lesen darf, antwortet wie ein unbekanntes. *Client
// (über HTTP) und AccessReader (serverseitig über ProjectAccess) erfüllen es;
// es ist der Teil von API, den auch die Hover-Karten brauchen.
type Reader interface {
	GetRequest(id int64) (requestdomain.Detail, error)
	GetHumanTask(id int64) (humantask.Task, error)
	GetPain(id int64) (pain.Detail, error)
	GetIdea(id int64) (idea.Idea, error)
	GetFeature(id int64) (feature.Feature, error)
	GetDecision(id int64) (idea.Decision, error)
	KnowledgeByID(id int64) (store.Knowledge, error)
}

// PullReader liest einen PR eines Projekts. Er ist ein eigener Teil, weil nur
// der Server PRs kennt; ein Reader, der ihn nicht erfüllt, kennt keine PRs.
type PullReader interface {
	PullRequest(project string, n int64) (store.PullRequestInfo, error)
}

// Info ist die Hover-Karte eines Verweises: was ein Mensch sehen will, wenn er
// mit der Maus auf REQ-12 steht. Die Feldnamen sind die der Oberfläche.
type Info struct {
	Kind        string `json:"kind"`
	Gefunden    bool   `json:"gefunden"`
	Ref         string `json:"ref"`
	Nummer      int64  `json:"nummer,omitempty"`
	Titel       string `json:"titel,omitempty"`
	Zustand     string `json:"zustand,omitempty"`
	Kurzfassung string `json:"kurzfassung,omitempty"`
	Autor       string `json:"autor,omitempty"`
	Zeit        string `json:"zeit,omitempty"`
	Link        string `json:"link,omitempty"`
}

// MaxPeekRefs begrenzt, wie viele Verweise ein Aufruf nachschlägt.
const MaxPeekRefs = 50

const (
	maxPeekTitle   = 200
	maxPeekSummary = 240
)

// Missing ist der eine Eintrag für einen Verweis, den es nicht gibt, den der
// Aufrufer nicht lesen darf oder der keine Hover-Karte hat. Er trägt bewusst nur
// den Verweis, wie ihn der Aufrufer schrieb.
func Missing(ref string) Info {
	return Info{Kind: "", Gefunden: false, Ref: Clean(ref, 60)}
}

// Peekable sagt, ob ein Verweis eine Hover-Karte hat: Aufträge, Pains, Aufgaben,
// Ideen, Features, Entscheidungen, Wissen und PRs.
func Peekable(ref Ref) bool {
	switch ref.Kind {
	case KindRequest, KindPain, KindTask, KindIdea, KindFeature, KindDecision, KindKnowledge, KindPull:
		return ref.ID > 0
	}
	return false
}

// Peek schlägt einen Verweis nach und baut daraus die Hover-Karte. Karte, Zustand
// und Link kommen aus denselben Bausteinen wie bei open_link (card.go), damit
// beide nie auseinanderlaufen. project ist der Rahmen für PRs (die Nummer gilt
// nur in einem Projekt) und für Wissen ohne eigenes Projekt. Was fehlt, was der
// Aufrufer nicht lesen darf und was keine Karte hat, gibt Unknown zurück, immer
// denselben Fehler.
func Peek(b Builder, r Reader, ref Ref, project string) (Info, error) {
	if !Peekable(ref) {
		return Info{}, Unknown("reference")
	}
	b = b.WithProject(project)
	label := func(prefix string) string { return fmt.Sprintf("%s-%d", prefix, ref.ID) }
	switch ref.Kind {
	case KindRequest:
		d, err := r.GetRequest(ref.ID)
		if err != nil {
			return Info{}, Unknown(label("REQ"))
		}
		return infoOf(b.RequestCard(d), ref.ID, d.Request.Description, d.Request.Person, d.Request.CreatedAt), nil
	case KindTask:
		t, err := r.GetHumanTask(ref.ID)
		if err != nil {
			return Info{}, Unknown(label("TASK"))
		}
		return infoOf(b.TaskCard(t), ref.ID, t.Description, t.CreatedBy, t.CreatedAt), nil
	case KindPain:
		d, err := r.GetPain(ref.ID)
		if err != nil {
			return Info{}, Unknown(label("PAIN"))
		}
		return infoOf(b.PainCard(d.Pain), ref.ID, d.Pain.Body, d.Pain.Person, d.Pain.CreatedAt), nil
	case KindIdea:
		i, err := r.GetIdea(ref.ID)
		if err != nil {
			return Info{}, Unknown(label("IDEA"))
		}
		return infoOf(b.IdeaCard(i), ref.ID, i.Body, i.Person, i.CreatedAt), nil
	case KindFeature:
		f, err := r.GetFeature(ref.ID)
		if err != nil {
			return Info{}, Unknown(label("FEAT"))
		}
		return infoOf(b.FeatureCard(f), ref.ID, f.Summary, f.Person, f.CreatedAt), nil
	case KindDecision:
		d, err := r.GetDecision(ref.ID)
		if err != nil {
			return Info{}, Unknown(label("DEC"))
		}
		return infoOf(b.DecisionCard(d), ref.ID, d.Detail, d.Person, d.CreatedAt), nil
	case KindKnowledge:
		k, err := r.KnowledgeByID(ref.ID)
		if err != nil {
			return Info{}, Unknown(knref.Format(ref.ID))
		}
		return infoOf(b.KnowledgeCard(k), ref.ID, k.Body, k.Person, k.CreatedAt), nil
	case KindPull:
		pr, ok := r.(PullReader)
		if !ok || strings.TrimSpace(project) == "" {
			return Info{}, Unknown(fmt.Sprintf("pr:%d", ref.ID))
		}
		p, err := pr.PullRequest(project, ref.ID)
		if err != nil {
			return Info{}, Unknown(fmt.Sprintf("pr:%d", ref.ID))
		}
		c := Card{Kind: "Pull request", Ref: "#" + num(p.Number), Title: p.Title, Status: p.State, URL: b.Pull(p.Number)}.Sanitized()
		summary := p.Head
		switch {
		case p.Head == "":
			summary = p.Base
		case p.Base != "":
			summary += " → " + p.Base
		}
		return infoOf(c, p.Number, summary, p.Author, p.OpenedAt), nil
	}
	return Info{}, Unknown("reference")
}

// infoOf macht aus der Karte die Hover-Karte: der Titel wird nicht auf die 100
// Zeichen der Agentenkarte gekürzt, die Kurzfassung ist der Anfang des Textes
// einzeilig und gekürzt.
func infoOf(c Card, id int64, body, author, at string) Info {
	return Info{
		Kind:        kindName(c.Kind),
		Gefunden:    true,
		Ref:         c.Ref,
		Nummer:      id,
		Titel:       c.Title,
		Zustand:     c.Status,
		Kurzfassung: Clean(body, maxPeekSummary),
		Autor:       Clean(author, 60),
		Zeit:        Clean(at, 40),
		Link:        c.URL,
	}
}

// kindName ist die Art in der Schreibweise der API.
func kindName(cardKind string) string {
	switch cardKind {
	case "Issue":
		return KindRequest
	case "Pull request":
		return KindPull
	}
	return strings.ToLower(cardKind)
}

// Lookup schlägt mehrere Verweise nach, in der Reihenfolge der Eingabe, und gibt
// zu jedem genau einen Eintrag zurück. Ein Verweis, der sich nicht lesen lässt,
// ist Missing, gleich ob es ihn nicht gibt oder der Aufrufer ihn nicht lesen
// darf. Ein Verweis, den ParseRef nicht versteht, ist ein Fehler des Aufrufers.
func Lookup(b Builder, r Reader, refs []string, project string) ([]Info, error) {
	if len(refs) > MaxPeekRefs {
		return nil, fmt.Errorf("at most %d references per call", MaxPeekRefs)
	}
	out := make([]Info, 0, len(refs))
	for _, raw := range refs {
		ref, err := ParseRef(raw)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", Clean(raw, 60), err)
		}
		info, err := Peek(b, r, ref, project)
		if err != nil {
			out = append(out, Missing(raw))
			continue
		}
		out = append(out, info)
	}
	return out, nil
}
