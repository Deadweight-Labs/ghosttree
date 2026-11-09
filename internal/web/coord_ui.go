package web

import (
	"html/template"
	"regexp"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Räume auf der neuen Oberfläche (REQ-599, Phase 2 B): Hilfen, die die Vorlagen
// aus coord.html brauchen. Die Ansichtsdaten bleiben die aus coord_views.go.

// Badge is the number beside a room in the list: what needs the viewer, else
// what is unread.
func (r coordRoomView) Badge() int64 {
	if r.NeedsYou > 0 {
		return r.NeedsYou
	}
	if r.Mentions > 0 {
		return r.Mentions
	}
	if r.Attention > 0 {
		return r.Attention
	}
	return r.Unread
}

// Groups are the team rooms of the sidebar, Directs the conversations with one
// person or agent: both live in Private.
func (s coordSidebarView) Groups() []coordRoomView  { return s.private(store.RoomGroup) }
func (s coordSidebarView) Directs() []coordRoomView { return s.private(store.RoomDirect) }

func (s coordSidebarView) private(kind string) []coordRoomView {
	var out []coordRoomView
	for _, r := range s.Private {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

// mentionPattern finds @name in a message; the name is what the composer completes.
var mentionPattern = regexp.MustCompile(`(^|[\s(])(@[\p{L}\p{N}][\p{L}\p{N}._:-]*)`)

// BodyHTML is the text of the message for the one Markdown renderer of the page
// (ui-md.js), like a comment: the source in data-md, the blocks as plain text
// until the script has drawn them.
func (m coordMessageView) BodyHTML() template.HTML { return mdText(m.Body) }

// coordComposer is the one input of a room or a thread: the composer of the new
// look with two selects (to whom, what kind) beside the buttons.
type coordComposer struct {
	composerView
	People    []coordParticipantView
	To, Kind  *composerSelect
	More      bool
	Thread    bool
	CanDirect bool
	Zone      string
}

// coordComposerFor builds the composer for the active room.
func coordComposerFor(l lang, d *coordRoomDetailView, id string, thread bool) coordComposer {
	placeholder := l.msg("coord.placeholder", d.Room.Name)
	if thread {
		placeholder = l.msg("coord.thread_reply")
	}
	c := coordComposer{composerView: composerView{ID: id, Placeholder: placeholder, Value: d.Draft}, People: d.Participants, Thread: thread, CanDirect: d.CanDirect, Zone: d.Zone, More: !thread}
	to := &composerSelect{Name: "mentions", Label: l.msg("room.compose.to")}
	to.Options = append(to.Options, composerOption{Value: "", Label: l.msg("room.compose.all"), Selected: d.ToDefault == ""})
	if d.Broadcast {
		to.Options = append(to.Options, composerOption{Value: "all-agents", Label: l.msg("coord.broadcast")})
	}
	for _, p := range d.Participants {
		if !p.Current && !p.ListOnly {
			to.Options = append(to.Options, composerOption{Value: p.ID, Label: p.Label, Selected: p.ID == d.ToDefault})
		}
	}
	c.To = to
	kind := &composerSelect{Name: "intent", Label: l.msg("coord.kind")}
	kind.Options = []composerOption{{Value: "", Label: l.msg("coord.mode.message"), Selected: true}}
	for _, k := range []composerOption{
		{Value: "question", Label: l.msg("coord.reason.question")}, {Value: "approval", Label: l.msg("coord.reason.approval")},
		{Value: "blocker", Label: l.msg("coord.reason.blocker")}, {Value: "handoff", Label: l.msg("coord.reason.handoff")},
	} {
		kind.Options = append(kind.Options, k)
	}
	c.Kind = kind
	return c
}

// splitAuthor splits "Claude · Robin" (an agent and the person it belongs to).
func (m coordMessageView) splitAuthor() (name, owner string) {
	name, owner, _ = strings.Cut(m.Name, " · ")
	return name, owner
}

// ShortName is the name an agent goes by, without the owner behind it.
func (m coordMessageView) ShortName() string {
	if m.AuthorKind != "agent" {
		return m.Name
	}
	n, _ := m.splitAuthor()
	return n
}

// msgAvatar is the avatar of the author: the mark of the agent's harness with
// the owner's picture in the corner, or the initials of a person.
func msgAvatar(l lang, m coordMessageView, size string) template.HTML {
	if m.AuthorKind != "agent" {
		return avatarMarkup(l, avatarSpec{Kind: avHuman, Name: m.Name, Size: size})
	}
	name, owner := m.splitAuthor()
	kind := actorKind(strings.ReplaceAll(strings.ToLower(name), " ", "-"))
	if kind == avHuman {
		kind = avSystem
	}
	return avatarMarkup(l, avatarSpec{Kind: kind, Name: name, Size: size, Owner: owner})
}
