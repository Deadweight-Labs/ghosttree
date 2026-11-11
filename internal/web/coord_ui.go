package web

import (
	"html/template"
	"regexp"
	"strings"
	"time"

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

// HM is the time of day alone, for the narrow column beside a message that
// continues a group: the day is in the head of the group.
func (m coordMessageView) HM() string {
	parsed, err := time.Parse(time.RFC3339, m.Timestamp)
	if err != nil {
		return m.Clock
	}
	return parsed.In(time.Local).Format("15:04")
}

// TimeTitle is the tooltip of the time: the exact moment, and how far the
// message got when it is on its way (delivered, fetched).
func (m coordMessageView) TimeTitle() string {
	if m.Delivery == "" {
		return m.Timestamp
	}
	return m.Timestamp + " · " + m.Delivery
}

// coordStateOption is one status of a thread in the composer's row.
type coordStateOption struct {
	Value, Label, Button, ButtonText string
	Current                          bool
}

// coordComposer is the one input of a room or a thread: the composer of the new
// look with two selects (to whom, what kind) beside the buttons. In a thread the
// row of the status comes with it: a reply and a change of state in one send.
type coordComposer struct {
	composerView
	People    []coordParticipantView
	To, Kind  *composerSelect
	More      bool
	Thread    bool
	CanDirect bool
	Zone      string
	// SendLabel is the main button: "Send" in a room, "Reply" in a thread.
	SendLabel string
	// HiddenTo is the one addressee of a conversation of two; there is no choice to offer.
	HiddenTo             string
	States               []coordStateOption
	StateLabel, StateWas string
}

// coordComposerFor builds the composer for the active room.
func coordComposerFor(l lang, d *coordRoomDetailView, id string, thread bool) coordComposer {
	placeholder := l.msg("coord.placeholder", d.Room.Name)
	if thread {
		placeholder = l.msg("coord.thread_reply")
	}
	c := coordComposer{composerView: composerView{ID: id, Placeholder: placeholder, Value: d.Draft}, People: d.Participants, Thread: thread, CanDirect: d.CanDirect, Zone: d.Zone, More: !thread, SendLabel: l.msg("coord.send")}
	everyone := l.msg("room.compose.all")
	if thread {
		everyone = l.msg("room.compose.all_thread")
		c.SendLabel = l.msg("coord.reply")
	}
	to := &composerSelect{Name: "mentions", Label: l.msg("room.compose.to")}
	to.Options = append(to.Options, composerOption{Value: "", Label: everyone, Selected: d.ToDefault == ""})
	if d.Broadcast {
		to.Options = append(to.Options, composerOption{Value: "all-agents", Label: l.msg("coord.broadcast")})
	}
	for _, p := range d.Participants {
		if !p.Current && !p.ListOnly {
			to.Options = append(to.Options, composerOption{Value: p.ID, Label: p.Label, Selected: p.ID == d.ToDefault})
		}
	}
	c.To = to
	if d.Room.Kind == store.RoomDirect && !thread {
		// A conversation of two has one addressee: there is nothing to choose, and a request goes to them.
		var peers []string
		for _, p := range d.Participants {
			if !p.Current && !p.ListOnly {
				peers = append(peers, p.ID)
			}
		}
		if len(peers) == 1 {
			c.To, c.HiddenTo = nil, peers[0]
		}
	}
	if thread && d.Thread != nil && d.Thread.CanSetState {
		c.StateLabel, c.StateWas = l.msg("coord.thread_state"), d.Thread.State
		for _, st := range []struct{ value, label, button, buttonText string }{
			{"open", "coord.state.open", "coord.state_btn.open", "coord.state_btn_text.open"},
			{"resolved", "coord.state.resolved", "coord.state_btn.resolved", "coord.state_btn_text.resolved"},
			{"deferred", "coord.state.deferred", "coord.state_btn.deferred", "coord.state_btn_text.deferred"},
		} {
			c.States = append(c.States, coordStateOption{Value: st.value, Label: l.msg(st.label), Current: st.value == d.Thread.State,
				Button: l.msg(st.button), ButtonText: l.msg(st.buttonText)})
		}
	}
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

// coordSystemPrefixes maps the English text the store writes for system lines to
// the message that says the same in the language of the page.
var coordSystemPrefixes = []struct{ prefix, key string }{
	{"Mutual wait:", "coord.system.mutual_wait"},
}

// coordSystemText is the line of a system message in the language of the page.
func coordSystemText(l lang, body string) string {
	for _, p := range coordSystemPrefixes {
		if strings.HasPrefix(body, p.prefix) {
			return l.msg(p.key)
		}
	}
	return body
}

// MetaTitle is what the head of a message keeps quiet: role, machine, branch
// and what an agent says it works on, as the tooltip of the name.
func (m coordMessageView) MetaTitle() string {
	var parts []string
	if m.SenderRole != "" && !m.Own {
		parts = append(parts, m.SenderRoleLabel)
	}
	if m.AuthorKind == "agent" {
		_, owner := m.splitAuthor()
		parts = append(parts, strings.Split(m.Name, " · ")[1:]...)
		if host := strings.TrimPrefix(m.Machine, "@"); host != "" && owner == "" {
			parts = append(parts, host)
		}
	}
	if m.WorkNote != "" {
		// What an agent says about itself is marked as such, also in a tooltip.
		parts = append(parts, m.WorkNote+" ("+m.WorkNoteTitle+")")
	}
	return strings.Join(parts, " · ")
}

// coordRequestAction is one button or menu entry of a request card. IDs are the
// attention items it acts on: a card with several addressees has one item each,
// and one button acts on every item that offers it.
type coordRequestAction struct {
	Value, Label string
	IDs          []int64
}

// coordRequestActions is what the foot of a request card shows: the one main
// action, and everything else in one menu.
type coordRequestActions struct {
	Main               *coordRequestAction
	Menu               []coordRequestAction
	RoomKey, CSRFToken string
	// Note is set for an approval: it only keeps the answer in the room.
	Note bool
}

// Merged joins the actions of all items of the card, each action once. The first
// verdict or answer is the main action; closing and withdrawing come last in the menu.
func (r coordRequestView) Merged() *coordRequestActions {
	if len(r.Items) == 0 {
		return nil
	}
	out := &coordRequestActions{RoomKey: r.Items[0].RoomKey, CSRFToken: r.Items[0].CSRFToken}
	var order []string
	byValue := map[string]*coordRequestAction{}
	add := func(id int64, a coordAttentionActionView) {
		got, ok := byValue[a.Value]
		if !ok {
			got = &coordRequestAction{Value: a.Value, Label: a.Label}
			byValue[a.Value] = got
			order = append(order, a.Value)
		}
		got.IDs = append(got.IDs, id)
	}
	for _, item := range r.Items {
		if item.CoordinationOnlyApproval && item.Incoming {
			out.Note = true
		}
		if item.Primary != nil {
			add(item.ID, *item.Primary)
		}
		for _, a := range item.Inline {
			add(item.ID, a)
		}
		for _, a := range item.More {
			add(item.ID, a)
		}
		if item.CanWithdraw {
			add(item.ID, coordAttentionActionView{Value: store.AttentionActionWithdraw, Label: r.WithdrawLabel})
		}
	}
	var last []coordRequestAction
	for _, value := range order {
		a := *byValue[value]
		if value == store.AttentionActionDismiss || value == store.AttentionActionWithdraw {
			last = append(last, a)
			continue
		}
		if out.Main == nil {
			out.Main = &a
			continue
		}
		out.Menu = append(out.Menu, a)
	}
	out.Menu = append(out.Menu, last...)
	return out
}
