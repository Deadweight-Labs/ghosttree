package mcpserver

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// defaultThreadPosts begrenzt, wie viel Verlauf ein Abruf ohne ausdrückliche
// Bitte mitbringt. Spec §B3 nennt als Startwert rund 3.000 Tokens für den
// gesamten Abruf einschließlich Karte; zwanzig Beiträge liegen in derselben
// Größenordnung und sind eine Zahl, die man beim Lesen versteht.
//
// Die Grenze ist ein Startwert für Tests, keine gemessene Optimalzahl.
const defaultThreadPosts = 20

type ThreadOpenInput struct {
	Title string `json:"title" jsonschema:"the question this thread is about, as a title someone will search for in six months. 'Does pitfall #846 still hold after the parser change?' — not 'parser'"`
	// Alle weiteren Felder optional, deshalb omitempty (#846).
	Question   string `json:"question,omitempty" jsonschema:"the leading question in full, if the title is too short to carry it"`
	First      string `json:"first_post,omitempty" jsonschema:"your opening contribution — what you already know or suspect"`
	LinkKind   string `json:"link_kind,omitempty" jsonschema:"attach this thread to an existing object: knowledge, request or document"`
	LinkID     string `json:"link_id,omitempty" jsonschema:"id of that object, for example 846 for a knowledge entry"`
	LinkRev    string `json:"link_revision,omitempty" jsonschema:"revision of that object, if you mean one exact version rather than whatever it says today"`
	DiscussNow bool   `json:"announce,omitempty" jsonschema:"also announce this thread in the project room so other agents see it exists"`
}

type ThreadReadInput struct {
	ID int64 `json:"id" jsonschema:"thread id"`
	// After ist der Cursor. Ohne ihn setzt der Abruf beim gespeicherten Stand
	// fort — das ist fast immer das Gewollte.
	After int64 `json:"after,omitempty" jsonschema:"only return posts after this sequence. Omit to continue where you left off"`
	Full  bool  `json:"full,omitempty" jsonschema:"return the whole history instead of the working state. Use it when you need to check a claim against what was actually written"`
	Limit int   `json:"limit,omitempty" jsonschema:"maximum posts to return"`
}

type ThreadReplyInput struct {
	ID   int64  `json:"id" jsonschema:"thread id"`
	Body string `json:"body" jsonschema:"your contribution"`
}

type ThreadFindInput struct {
	Query string `json:"query,omitempty" jsonschema:"words from the title or question. Omit to list what is open"`
	// Ein Objektbezug ist die zuverlässigere Suche: wer wissen will, ob an
	// einem Pitfall schon diskutiert wurde, sucht nicht nach Wörtern.
	ObjectKind string `json:"object_kind,omitempty" jsonschema:"find threads attached to this kind of object: knowledge, request or document"`
	ObjectID   string `json:"object_id,omitempty" jsonschema:"id of that object"`
	Archived   bool   `json:"include_archived,omitempty" jsonschema:"also list archived threads"`
}

type ThreadResolveInput struct {
	ID int64 `json:"id" jsonschema:"thread id"`
	// State ist optional und fällt auf resolved. Ein Werkzeug, das einen
	// Thread schließen soll, darf dafür kein Pflichtfeld verlangen.
	State string `json:"state,omitempty" jsonschema:"resolved (default — the question is answered), deferred (parked on purpose) or open (reopen it)"`
	Note  string `json:"note,omitempty" jsonschema:"what the answer was, as a closing post"`
}

type ThreadProposeInput struct {
	ID   int64  `json:"id" jsonschema:"thread id"`
	Kind string `json:"kind" jsonschema:"what you propose should come out of this: decision, pitfall or request"`
	Note string `json:"note" jsonschema:"the proposal itself"`
	Ref  string `json:"ref_id,omitempty" jsonschema:"id of the object, if it already exists"`
}

func (s *Server) threadProject() (string, error) {
	if s.ctxAxes.Project == "" {
		return "", fmt.Errorf("this session is not bound to a repository, so it has no project to file a thread under")
	}
	return s.ctxAxes.Project, nil
}

func (s *Server) joinThreadProject() error {
	key, err := s.roomKeyFor("project")
	if err != nil {
		return err
	}
	return s.joinRoom(key)
}

// canonicalLinkID writes a request as REQ-<n>, the form a thread in a room links
// to; agents often pass the bare number.
func canonicalLinkID(kind, id string) string {
	id = strings.TrimSpace(id)
	if kind != "request" || id == "" {
		return id
	}
	if _, err := strconv.ParseInt(id, 10, 64); err == nil {
		return "REQ-" + id
	}
	return id
}

func (s *Server) handleThreadOpen(ctx context.Context, _ *mcp.CallToolRequest, in ThreadOpenInput) (*mcp.CallToolResult, any, error) {
	if strings.TrimSpace(in.Title) == "" {
		return nil, nil, fmt.Errorf("title is required — it is what someone searches for later")
	}
	project, err := s.threadProject()
	if err != nil {
		return nil, nil, err
	}
	if err := s.joinThreadProject(); err != nil {
		return nil, nil, err
	}
	// Vorhandene Threads am selben Objekt vorschlagen, bevor ein zweiter
	// entsteht. Spec §B5: vorschlagen ja, automatisch zusammenführen nein.
	var existing []store.Thread
	in.LinkID = canonicalLinkID(in.LinkKind, in.LinkID)
	if in.LinkKind != "" && in.LinkID != "" {
		existing, _ = s.client.ThreadsForObject(in.LinkKind, in.LinkID, s.coordRef())
	}

	id, err := s.client.CreateThread(store.Thread{
		Project: project, Title: in.Title, Question: in.Question}, s.coordRef())
	if err != nil {
		return nil, nil, err
	}
	if in.LinkKind != "" && in.LinkID != "" {
		if err := s.client.LinkThread(store.ThreadLink{ThreadID: id,
			Kind: in.LinkKind, ID: in.LinkID, Revision: in.LinkRev}, s.coordRef()); err != nil {
			return nil, nil, err
		}
	}
	if strings.TrimSpace(in.First) != "" {
		if err := s.postToThread(id, in.First); err != nil {
			return nil, nil, err
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "thread %d opened: %s\n", id, in.Title)
	if len(existing) > 0 {
		b.WriteString("\nThreads already attached to that object — check whether yours belongs in one of them:\n")
		for _, t := range existing {
			fmt.Fprintf(&b, "  [%d] %s (%s)\n", t.ID, t.Title, t.State)
		}
	}
	if in.DiscussNow {
		if key, err := s.roomKeyFor("project"); err == nil {
			clientID, err := newCoordClientID()
			if err == nil {
				_, _ = s.client.SendCoordMessage(store.CoordMessage{
					DestinationKind: store.DestinationRoom, DestinationID: key,
					SenderExternalID: s.coordRef(), ClientID: clientID,
					Body: fmt.Sprintf("Thread %d offen: %s", id, in.Title),
					Refs: []store.CoordRef{{Kind: "thread", ID: store.ThreadDestinationID(id)}},
				})
				b.WriteString("\nAnnounced in the project room.\n")
			}
		}
	}
	return coordText(b.String()), nil, nil
}

func (s *Server) postToThread(id int64, body string) error {
	clientID, err := newCoordClientID()
	if err != nil {
		return err
	}
	_, err = s.client.SendCoordMessage(store.CoordMessage{
		DestinationKind:  store.DestinationDiscussion,
		DestinationID:    store.ThreadDestinationID(id),
		SenderExternalID: s.coordRef(), ClientID: clientID, Body: body,
	})
	if err != nil {
		return err
	}
	return nil
}

func (s *Server) handleThreadReply(ctx context.Context, _ *mcp.CallToolRequest, in ThreadReplyInput) (*mcp.CallToolResult, any, error) {
	if in.ID == 0 || strings.TrimSpace(in.Body) == "" {
		return nil, nil, fmt.Errorf("id and body are required")
	}
	if err := s.joinThreadProject(); err != nil {
		return nil, nil, err
	}
	if err := s.postToThread(in.ID, in.Body); err != nil {
		return nil, nil, err
	}
	return coordText(fmt.Sprintf("posted to thread %d", in.ID)), nil, nil
}

// handleThreadRead ist das Werkzeug, an dem sich entscheidet, ob Threads
// benutzbar bleiben. Es liefert standardmäßig den ARBEITSSTAND — Karte,
// offene Fragen, Ergebnisse und die Beiträge seit dem Quellenstand — statt
// des ganzen Verlaufs.
//
// Und es lügt nicht über die Abdeckung: wenn etwas fehlt, steht das dabei,
// samt dem Weg, es zu holen. Spec §B3: "Lange Inhalte werden nicht an der
// Grenze unsichtbar abgeschnitten."
func (s *Server) handleThreadRead(ctx context.Context, _ *mcp.CallToolRequest, in ThreadReadInput) (*mcp.CallToolResult, any, error) {
	if in.ID == 0 {
		return nil, nil, fmt.Errorf("id is required")
	}
	if err := s.joinThreadProject(); err != nil {
		return nil, nil, err
	}
	t, err := s.client.ThreadByID(in.ID, s.coordRef())
	if err != nil {
		return nil, nil, err
	}
	dest := store.ThreadDestinationID(in.ID)
	limit := in.Limit
	if limit <= 0 {
		limit = defaultThreadPosts
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Thread %d — %s\nState: %s", t.ID, t.Title, t.State)
	if t.Dormant {
		// Ruhend ist nicht gelöst, und ein Leser soll das nicht verwechseln.
		b.WriteString(" (dormant — no activity for a while, but the question is still open)")
	}
	if t.Archived {
		b.WriteString(" (archived)")
	}
	b.WriteString("\n")
	if t.Question != "" {
		fmt.Fprintf(&b, "Question: %s\n", t.Question)
	}

	if links, err := s.client.ThreadLinks(in.ID, s.coordRef()); err == nil && len(links) > 0 {
		b.WriteString("Attached to: ")
		for i, l := range links {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%s %s", l.Kind, l.ID)
			if l.Revision != "" {
				fmt.Fprintf(&b, " rev %s", l.Revision)
			} else {
				// Derselbe Grundsatz wie bei CoordRef: ein Verweis auf einen
				// veränderlichen Head ist kein Beleg des damaligen Wortlauts.
				b.WriteString(" (current head)")
			}
		}
		b.WriteString("\n")
	}

	after := in.After
	sum, sumErr := s.client.ThreadSummary(in.ID, s.coordRef())
	hasSummary := sumErr == nil && sum.Body != ""
	if hasSummary && !in.Full {
		fmt.Fprintf(&b, "\nWorking state (summary rev %d, covers through post %d):\n%s\n",
			sum.Revision, sum.CoversThrough, sum.Body)
		if sum.OpenQuestions != "" {
			fmt.Fprintf(&b, "Open: %s\n", sum.OpenQuestions)
		}
		if after == 0 {
			after = sum.CoversThrough
		}
	}

	posts, err := s.client.CoordInbox(store.DestinationDiscussion, dest, s.coordRef(), 0, 200)
	if err != nil {
		return nil, nil, err
	}
	var shown []store.CoordMessage
	for _, p := range posts {
		if in.Full || p.Sequence > after {
			shown = append(shown, p)
		}
	}
	omitted := len(shown) - limit
	if omitted > 0 {
		// Von hinten kürzen wäre falsch: die neuesten Beiträge sind die, die
		// der Karte fehlen. Also die ältesten weglassen und sagen, wie viele.
		shown = shown[len(shown)-limit:]
	}

	if len(shown) == 0 {
		if hasSummary && !in.Full {
			b.WriteString("\nNo posts since the summary.\n")
		} else {
			b.WriteString("\nNo posts yet.\n")
		}
	} else {
		if omitted > 0 {
			fmt.Fprintf(&b, "\n%d earlier posts omitted — read them with full=true or after=<sequence>.\n", omitted)
		}
		b.WriteString("\nPosts:\n")
		for _, p := range shown {
			fmt.Fprintf(&b, "  #%d %s: %s\n", p.Sequence, senderLabel(p), bodyBlock(p.Body))
		}
	}

	if outcomes, err := s.client.ThreadOutcomes(in.ID, s.coordRef()); err == nil && len(outcomes) > 0 {
		b.WriteString("\nOutcomes:\n")
		for _, o := range outcomes {
			fmt.Fprintf(&b, "  %s %s [%s]", o.Kind, o.RefID, o.State)
			if o.Note != "" {
				fmt.Fprintf(&b, " — %s", o.Note)
			}
			b.WriteString("\n")
		}
		// Der Satz verhindert die Verwechslung, gegen die §B6 antritt.
		b.WriteString("A proposal is not an accepted rule, and a resolved thread is not implemented work.\n")
	}
	if !hasSummary && !in.Full && len(posts) > limit {
		b.WriteString("\nThere is no summary yet, so this is raw history.\n")
	}
	return coordText(b.String()), nil, nil
}

func (s *Server) handleThreadFind(ctx context.Context, _ *mcp.CallToolRequest, in ThreadFindInput) (*mcp.CallToolResult, any, error) {
	if err := s.joinThreadProject(); err != nil {
		return nil, nil, err
	}
	var found []store.Thread
	var err error
	if in.ObjectKind != "" && in.ObjectID != "" {
		found, err = s.client.ThreadsForObject(in.ObjectKind, canonicalLinkID(in.ObjectKind, in.ObjectID), s.coordRef())
		if err == nil && len(found) == 0 && canonicalLinkID(in.ObjectKind, in.ObjectID) != in.ObjectID {
			// Links written before the canonical form carry the bare number.
			found, err = s.client.ThreadsForObject(in.ObjectKind, in.ObjectID, s.coordRef())
		}
	} else {
		project, perr := s.threadProject()
		if perr != nil {
			return nil, nil, perr
		}
		found, err = s.client.SearchThreads(project, in.Query, in.Archived, 0, s.coordRef())
	}
	if err != nil {
		return nil, nil, err
	}
	if len(found) == 0 {
		return coordText("no threads match"), nil, nil
	}
	var b strings.Builder
	for _, t := range found {
		fmt.Fprintf(&b, "[%d] %s — %s", t.ID, t.Title, t.State)
		if t.Dormant {
			b.WriteString(", dormant")
		}
		if t.Archived {
			b.WriteString(", archived")
		}
		fmt.Fprintf(&b, " (last activity %s)\n", t.UpdatedAt)
	}
	return coordText(b.String()), nil, nil
}

func (s *Server) handleThreadResolve(ctx context.Context, _ *mcp.CallToolRequest, in ThreadResolveInput) (*mcp.CallToolResult, any, error) {
	if in.ID == 0 {
		return nil, nil, fmt.Errorf("id is required")
	}
	if err := s.joinThreadProject(); err != nil {
		return nil, nil, err
	}
	state := in.State
	if state == "" {
		state = store.ThreadResolved
	}
	if strings.TrimSpace(in.Note) != "" {
		if err := s.postToThread(in.ID, in.Note); err != nil {
			return nil, nil, err
		}
	}
	if err := s.client.SetThreadState(in.ID, state, s.coordRef()); err != nil {
		return nil, nil, err
	}
	// Der Nachsatz ist der Punkt aus §B6: eine beendete Diskussion ist keine
	// erledigte Arbeit, und kein verknüpfter Request wird dadurch grün.
	return coordText(fmt.Sprintf("thread %d is now %s. This closes the discussion, not any work: "+
		"linked requests keep their own criteria and evidence.", in.ID, state)), nil, nil
}

func (s *Server) handleThreadPropose(ctx context.Context, _ *mcp.CallToolRequest, in ThreadProposeInput) (*mcp.CallToolResult, any, error) {
	if in.ID == 0 || in.Kind == "" || strings.TrimSpace(in.Note) == "" {
		return nil, nil, fmt.Errorf("id, kind and note are required")
	}
	if err := s.joinThreadProject(); err != nil {
		return nil, nil, err
	}
	ref := in.Ref
	if ref == "" {
		ref = "proposal-" + store.ThreadDestinationID(in.ID) + "-" + in.Kind
	}
	if err := s.client.PutThreadOutcome(store.ThreadOutcome{
		ThreadID: in.ID, Kind: in.Kind, RefID: ref,
		State: store.OutcomeProposed, Note: in.Note}, s.coordRef()); err != nil {
		return nil, nil, err
	}
	return coordText(fmt.Sprintf("recorded as a proposal on thread %d. It is not knowledge yet: "+
		"promoting it needs its own write through context_remember and the usual review.", in.ID)), nil, nil
}
