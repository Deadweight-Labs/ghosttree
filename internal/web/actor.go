package web

import (
	"html"
	"html/template"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// browserActor is who acts in the web interface: a person when the request
// comes from an interactive web session, which is the only way a person is
// recognized (REQ-620, the rule of store.IdeaActorFor). Pages reached with a
// token are an agent without session.
func browserActor(r *http.Request) actor.Actor {
	p := browserPrincipal(r)
	a := actor.Actor{Kind: actor.Agent, AccountID: store.AccountIDOf(p.ID), Person: p.Label, Machine: p.Machine}
	if p.TokenKind == store.WebSessionKind {
		a.Kind, a.Machine, a.Via = actor.Human, "", actor.ViaWeb
	}
	return a
}

// stampActor records who wrote an entry from a web form, after it is stored.
// A failure leaves the entry unknown and is logged; the form has succeeded.
func (a *app) stampActor(r *http.Request, kind string, id int64) {
	if id <= 0 {
		return
	}
	if err := a.store.RecordActor(kind, id, browserActor(r)); err != nil {
		slog.Warn("actor not recorded", "kind", kind, "id", id, "err", err)
	}
}

// actorView is who wrote an entry as a page shows it: a person by name, an
// agent as "Robin’s Claude" with the mark of its harness. Detail (model and
// machine) and Href (the session) are only filled on a detail page and only
// as far as the viewer may see them.
type actorView struct {
	// Person is the account: the person, or the person the agent belongs to.
	Person string
	Agent  bool
	// Label is what the page writes: the person, or "Robin’s Claude".
	Label string
	// Kind is the avatar kind (avatarSpec.Kind).
	Kind string
	// Terminal: a person who wrote through ctx in a terminal, by the client's
	// own word (actor.ViaTerminal); shown as such, never as verified.
	Terminal bool
	Detail   string
	Href     string
}

// Owner is the person shown in the corner of an agent's avatar.
func (v actorView) Owner() string {
	if v.Agent {
		return v.Person
	}
	return ""
}

// possessive is "Robin’s" or "Robins": the English apostrophe s, the German
// genitive s, and for a name that ends in a hissing sound only the apostrophe.
func possessive(l lang, name string) string {
	low := strings.ToLower(name)
	if l == langDE {
		for _, end := range []string{"s", "ß", "x", "z", "ce"} {
			if strings.HasSuffix(low, end) {
				return name + "’"
			}
		}
		return name + "s"
	}
	if strings.HasSuffix(low, "s") {
		return name + "’"
	}
	return name + "’s"
}

// actorViewOf builds the view of one entry for the person the page shows.
// Without a stamp the entry shows its person as before (unknown is never
// guessed into an agent). detail asks for model, machine and session, which
// follow the viewer's rights on the session behind the stamp
// (ProjectAccess.ActorView); the session link only for whoever may read its
// transcript.
func (a *app) actorViewOf(l lang, pa *store.ProjectAccess, kind string, id int64, person string, detail bool) actorView {
	v := actorView{Person: person, Label: person, Kind: avHuman}
	// The name comes from the page's own view of the entry, never from the
	// stamp: where the view hides the person, nothing is shown.
	if person == "" {
		return v
	}
	ea, ok, err := a.store.ActorOf(kind, id)
	if err != nil || !ok {
		return v
	}
	if !ea.IsAgent() {
		v.Terminal = ea.Via == actor.ViaTerminal
		return v
	}
	v.Agent, v.Kind = true, harnessKind(ea.Harness)
	v.Label = possessive(l, person) + " " + actor.Family(ea.Harness)
	if !detail {
		return v
	}
	seen, sess, withSession := pa.ActorView(ea)
	var parts []string
	if seen.Model != "" {
		parts = append(parts, actor.ModelLabel(seen.Model))
	}
	if seen.Machine != "" {
		parts = append(parts, seen.Machine)
	}
	v.Detail = strings.Join(parts, " · ")
	if withSession && sess.PublicID != "" && pa.CanSeeTranscript(sess) {
		v.Href = "/ui/sessions/" + sess.PublicID
	}
	return v
}

// actorHTML writes the actor in a meta line: avatar, name, the quiet agent
// mark and, on a detail page, model and machine.
func actorHTML(l lang, v actorView) template.HTML {
	if v.Label == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<span class="person actor">`)
	b.WriteString(string(avatarMarkup(l, avatarSpec{Kind: v.Kind, Name: v.Label, Title: v.Label})))
	if v.Href != "" {
		b.WriteString(`<a href="` + html.EscapeString(safeRefHref(v.Href)) + `">` + html.EscapeString(v.Label) + `</a>`)
	} else {
		b.WriteString(`<span>` + html.EscapeString(v.Label) + `</span>`)
	}
	if v.Agent {
		b.WriteString(`<span class="lb out botmark">` + html.EscapeString(l.msg("agent.mark")) + `</span>`)
	}
	if v.Terminal {
		b.WriteString(`<span class="lb out botmark" title="` + html.EscapeString(l.msg("agent.mark_terminal_title")) + `">` + html.EscapeString(l.msg("agent.mark_terminal")) + `</span>`)
	}
	if v.Detail != "" {
		b.WriteString(`<span class="actor-d">` + html.EscapeString(v.Detail) + `</span>`)
	}
	b.WriteString(`</span>`)
	return template.HTML(b.String())
}

// rowActors marks the rows of a list that agents wrote: one lookup per kind
// for the whole list, names only (no model or machine in a list).
func (a *app) rowActors(l lang, rows []issueRow) {
	ids := map[string][]int64{}
	for _, r := range rows {
		ids[r.Kind] = append(ids[r.Kind], r.N)
	}
	found := map[string]map[int64]store.EntryActor{}
	for kind, list := range ids {
		if m, err := a.store.ActorsOf(kind, list); err == nil {
			found[kind] = m
		}
	}
	for i, r := range rows {
		ea, ok := found[r.Kind][r.N]
		if !ok || !ea.IsAgent() || r.Person == "" {
			continue
		}
		rows[i].Actor = actorView{Person: r.Person, Agent: true, Kind: harnessKind(ea.Harness),
			Label: possessive(l, r.Person) + " " + actor.Family(ea.Harness)}
	}
}

// stampedActors reads the stamps of entries of one kind and answers who made
// each: a person by name, an agent as "Robin’s Claude" with its mark. persons
// holds the name each page already shows for the entry; where the page names
// nobody, nobody is shown. An entry without a stamp stays the person.
func (a *app) stampedActors(l lang, kind string, persons map[int64]string) map[int64]actorView {
	out := map[int64]actorView{}
	if len(persons) == 0 {
		return out
	}
	ids := make([]int64, 0, len(persons))
	for id := range persons {
		ids = append(ids, id)
	}
	found, err := a.store.ActorsOf(kind, ids)
	if err != nil {
		found = nil
	}
	for id, person := range persons {
		v := actorView{Person: person, Label: person, Kind: avHuman}
		if ea, ok := found[id]; ok && person != "" {
			switch {
			case ea.IsAgent():
				v.Agent, v.Kind = true, harnessKind(ea.Harness)
				v.Label = possessive(l, person) + " " + actor.Family(ea.Harness)
			default:
				v.Terminal = ea.Via == actor.ViaTerminal
			}
		}
		out[id] = v
	}
	return out
}
