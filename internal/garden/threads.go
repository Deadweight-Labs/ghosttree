package garden

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// ThreadSink is the slice of the server client that opening a thread for a
// contradiction needs: look for threads on the entries, register as the agent
// that speaks for the gardener in the project room, open a thread, link it to
// the other entries, and write its first post. There is deliberately no way to
// set a thread's state, archive it, answer in a thread somebody else opened or
// record an outcome: a thread the gardener opened is closed by a person.
type ThreadSink interface {
	RegisterCoordAgent(a store.CoordAgent) (int64, error)
	ThreadsForObject(kind, id, agentExternalID string) ([]store.Thread, error)
	ThreadLinks(id int64, agentExternalID string) ([]store.ThreadLink, error)
	CreateThread(t store.Thread, agentExternalID string) (int64, error)
	LinkThread(l store.ThreadLink, agentExternalID string) error
	SendCoordMessage(m store.CoordMessage) (int64, error)
}

// What became of one contradiction.
const (
	// ThreadOpened: a new thread was opened.
	ThreadOpened = "opened"
	// ThreadExists: an open thread for these entries exists already; nothing was opened.
	ThreadExists = "open"
	// ThreadDecided: somebody dealt with this before (a note of the gardener on
	// it, or a thread they closed while the entries stayed as they were); nothing
	// was opened, and nothing was reopened.
	ThreadDecided = "decided"
	// ThreadFailed: the thread could not be opened or looked for; the caller falls
	// back to a staged note.
	ThreadFailed = "failed"
)

// ThreadOutcome is the result for one contradiction.
type ThreadOutcome struct {
	Relation Relation
	State    string
	// ThreadID is the thread opened or the one that stood in the way.
	ThreadID int64
	// Warn names a part that did not work although the thread was opened.
	Warn string
	Err  error
}

const (
	// standLabel starts the line of a thread's question that records which wording
	// of the entries the thread is about, so that a closed thread stays closed
	// while they are unchanged and a changed entry may ask again.
	standLabel = "Stand der Einträge beim Fund:"
	// maxThreadTitle keeps the question title far below the store's limit.
	maxThreadTitle = 220
	maxQuoteRunes  = 800
)

var standRe = regexp.MustCompile(`#(\d+) \(([0-9a-f]{8,64})\)`)

// ThreadAgentID is the coordination id under which the gardener speaks in the
// room of a project.
func ThreadAgentID(project string) string { return "gardener:" + project }

// shorten cuts s to at most n runes.
func shorten(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// ThreadTitle is the contradiction as a question, marked as the gardener's.
func ThreadTitle(r Relation, titles map[int64]string) string {
	var parts []string
	for _, id := range r.IDs {
		parts = append(parts, fmt.Sprintf("#%d „%s“", id, shorten(titles[id], 60)))
	}
	return shorten(fmt.Sprintf("%sWidersprechen sich %s?", titlePrefix, strings.Join(parts, " und ")), maxThreadTitle)
}

// threadQuestion is the question of the thread: what is asked, who asks and
// the stand of the entries it was asked about.
func threadQuestion(r Relation, known map[int64]Entry) string {
	var stand []string
	for _, id := range r.IDs {
		stand = append(stand, fmt.Sprintf("#%d (%s)", id, HashOf(known[id])))
	}
	return fmt.Sprintf("Behaupten diese Einträge Unvereinbares, und welcher gilt? Der Gärtner (Herkunft gardener, nicht bestätigt) hat den Widerspruch gefunden; die Begründung und die wörtlichen Zitate stehen im ersten Beitrag. Der Gärtner entscheidet nichts.\n\n%s %s",
		standLabel, strings.Join(stand, ", "))
}

// parseStand reads the stand line of a thread's question: entry id to hash.
func parseStand(question string) map[int64]string {
	for _, line := range strings.Split(question, "\n") {
		if !strings.HasPrefix(line, standLabel) {
			continue
		}
		out := map[int64]string{}
		for _, m := range standRe.FindAllStringSubmatch(line, -1) {
			if id, err := strconv.ParseInt(m[1], 10, 64); err == nil {
				out[id] = m[2]
			}
		}
		return out
	}
	return nil
}

// threadPost is the first post: the reason and the verbatim quotes.
func threadPost(r Relation, titles map[int64]string) string {
	t := r
	t.Quotes = map[int64]string{}
	for id, q := range r.Quotes {
		t.Quotes[id] = shorten(q, maxQuoteRunes)
	}
	return relationText(t, titles) +
		"\nDieser Thread bleibt offen, bis ein Mensch ihn schließt. Er wird nicht wieder geöffnet, solange die Einträge unverändert sind."
}

type threadOpener struct {
	sink    ThreadSink
	project string
	agent   string
	known   map[int64]Entry
	titles  map[int64]string
	joined  bool
}

func (o *threadOpener) join() error {
	if o.joined {
		return nil
	}
	_, err := o.sink.RegisterCoordAgent(store.CoordAgent{
		ExternalID: o.agent, Provider: Harness, RoomKey: store.RoomKeyForProject(o.project),
		DisplayName: "Gärtner", Role: "member",
	})
	o.joined = err == nil
	return err
}

// belongs says whether a thread is about exactly these entries: its stand line
// names them, or it is linked to all of them (a thread people opened has no stand
// line).
func (o *threadOpener) belongs(t store.Thread, r Relation) bool {
	if stand := parseStand(t.Question); stand != nil {
		same := len(stand) == len(r.IDs)
		for _, id := range r.IDs {
			if _, ok := stand[id]; !ok {
				same = false
			}
		}
		if same {
			return true
		}
	}
	links, err := o.sink.ThreadLinks(t.ID, o.agent)
	if err != nil {
		// Could not tell: counting it as one of ours is the safe error, it opens nothing.
		return true
	}
	have := map[string]bool{}
	for _, l := range links {
		if l.Kind == "knowledge" {
			have[l.ID] = true
		}
	}
	for _, id := range r.IDs {
		if !have[strconv.FormatInt(id, 10)] {
			return false
		}
	}
	return true
}

// blocking finds the thread that makes opening another one wrong: an open thread
// for these entries, or a closed one that is not out of date. A closed thread of
// the gardener is out of date once one of its entries changed; a thread people
// opened counts in every state.
func (o *threadOpener) blocking(r Relation, found []store.Thread) (store.Thread, bool) {
	for _, t := range found {
		if !o.belongs(t, r) {
			continue
		}
		if t.State == store.ThreadOpen && !t.Archived {
			return t, true
		}
		stand := parseStand(t.Question)
		if stand == nil {
			return t, true
		}
		unchanged := true
		for _, id := range r.IDs {
			if stand[id] != HashOf(o.known[id]) {
				unchanged = false
			}
		}
		if unchanged {
			return t, true
		}
	}
	return store.Thread{}, false
}

func (o *threadOpener) open(r Relation) ThreadOutcome {
	out := ThreadOutcome{Relation: r}
	if err := o.join(); err != nil {
		out.State, out.Err = ThreadFailed, fmt.Errorf("register in the project room: %w", err)
		return out
	}
	var found []store.Thread
	seen := map[int64]bool{}
	for _, id := range r.IDs {
		ts, err := o.sink.ThreadsForObject("knowledge", strconv.FormatInt(id, 10), o.agent)
		if err != nil {
			// Not knowing whether there is a thread is not a reason to open one.
			out.State, out.Err = ThreadFailed, fmt.Errorf("look for threads on #%d: %w", id, err)
			return out
		}
		for _, t := range ts {
			if !seen[t.ID] {
				seen[t.ID] = true
				found = append(found, t)
			}
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].ID > found[j].ID })
	if t, ok := o.blocking(r, found); ok {
		out.ThreadID = t.ID
		out.State = ThreadDecided
		if t.State == store.ThreadOpen && !t.Archived {
			out.State = ThreadExists
		}
		return out
	}
	id, err := o.sink.CreateThread(store.Thread{
		Project: o.project, Title: ThreadTitle(r, o.titles), Question: threadQuestion(r, o.known),
		Link: &store.ThreadLink{Kind: "knowledge", ID: strconv.FormatInt(r.IDs[0], 10)},
	}, o.agent)
	if err != nil {
		out.State, out.Err = ThreadFailed, fmt.Errorf("open thread: %w", err)
		return out
	}
	out.State, out.ThreadID = ThreadOpened, id
	var warn []string
	for _, other := range r.IDs[1:] {
		if err := o.sink.LinkThread(store.ThreadLink{ThreadID: id, Kind: "knowledge", ID: strconv.FormatInt(other, 10)}, o.agent); err != nil {
			warn = append(warn, fmt.Sprintf("link #%d: %v", other, err))
		}
	}
	if _, err := o.sink.SendCoordMessage(store.CoordMessage{
		DestinationKind: store.DestinationDiscussion, DestinationID: store.ThreadDestinationID(id),
		SenderExternalID: o.agent, ClientID: fmt.Sprintf("gardener-open-%d", id), Body: threadPost(r, o.titles),
	}); err != nil {
		warn = append(warn, fmt.Sprintf("first post: %v", err))
	}
	out.Warn = strings.Join(warn, "; ")
	return out
}

// OpenContradictionThreads opens one thread per validated contradiction, linked
// to every entry it is about, with the gardener's reason and the verbatim quotes
// as the first post and the thread marked as the gardener's. It never reopens or
// closes anything, and it is idempotent:
//
//   - a note of the gardener on the same relation, in any state, means somebody
//     has seen it: no thread;
//   - an open thread on the same entries means the question is being dealt with:
//     no second one;
//   - a thread closed by a person stays closed while the entries are unchanged
//     (the thread's question records their wording); once one changed, a new
//     thread may ask again, the old one is left as it is;
//   - if the existing threads cannot be looked up, nothing is opened.
//
// known holds every entry a relation may name.
func OpenContradictionThreads(sink ThreadSink, project string, existing []store.Knowledge, rels []Relation, known map[int64]Entry) []ThreadOutcome {
	titles := make(map[int64]string, len(known))
	for id, e := range known {
		titles[id] = e.Title
	}
	o := &threadOpener{sink: sink, project: project, agent: ThreadAgentID(project), known: known, titles: titles}
	var out []ThreadOutcome
	done := map[string]bool{}
	for _, r := range rels {
		if r.Kind != Contradiction || len(r.IDs) < 2 {
			continue
		}
		ref := RelationRef(r)
		if done[ref] {
			continue
		}
		done[ref] = true
		if RelationProposed(existing, r) {
			out = append(out, ThreadOutcome{Relation: r, State: ThreadDecided})
			continue
		}
		out = append(out, o.open(r))
	}
	return out
}
