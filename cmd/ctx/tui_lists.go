package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/links"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/Deadweight-Labs/ghosttree/internal/tui"
)

// List answers one list of the TUI's two modes. Every call only reads;
// none of them may put an unknown repository into an organization.
func (b cliBackend) List(ctx context.Context, kind tui.ListKind) ([]tui.Item, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, classifyErr(err)
	}
	c := b.client(cfg)
	project := requestProjectAxes(cfg.Machine).Project
	if kind < tui.ListMyTasks && project == "" {
		return nil, &tui.ProblemError{Kind: tui.ProblemNoProject}
	}
	var items []tui.Item
	switch kind {
	case tui.ListRequests:
		items, err = projectRequests(c, project)
	case tui.ListPains:
		items, err = projectPains(c, project)
	case tui.ListProjectTasks:
		items, err = projectTasks(c, project)
	case tui.ListRoom:
		items, err = roomUnread(c, cfg, store.RoomKeyForProject(project), "")
		if isForbidden(err) {
			// This terminal never joined the room (joining writes); the
			// person may still read it through the search.
			items, err = recentMessages(c, cfg, project)
		}
	case tui.ListMyTasks:
		items, err = inboxItems(c, func(e client.InboxEntry) bool { return e.Kind == store.InboxTask })
	case tui.ListQuestions:
		items, err = inboxItems(c, func(e client.InboxEntry) bool { return e.Kind != store.InboxTask })
	case tui.ListUnread:
		items, err = recentEverywhere(c, cfg)
	case tui.ListSuggestions:
		items, err = suggestions(c)
	}
	if err != nil {
		return nil, classifyErr(err)
	}
	return cleanItems(items), nil
}

// shortProject is the last part of a project id, for the column that says
// where an inbox row belongs.
func shortProject(id string) string {
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		return id[i+1:]
	}
	return id
}

func projectRequests(c *client.Client, project string) ([]tui.Item, error) {
	page, err := c.SearchRequests(requestdomain.SearchFilter{Scope: scope.Axes{Project: project}, State: "open", Limit: 50})
	if err != nil {
		return nil, err
	}
	out := make([]tui.Item, 0, len(page.Results))
	for _, h := range page.Results {
		meta := ""
		if h.OpenCriteria > 0 {
			meta = fmt.Sprintf("%d open", h.OpenCriteria)
		}
		out = append(out, tui.Item{Ref: fmt.Sprintf("REQ-%d", h.Request.ID), Kind: "issue", Title: h.Request.Title, Meta: meta})
	}
	return out, nil
}

func projectPains(c *client.Client, project string) ([]tui.Item, error) {
	list, err := c.SearchPains(project, "", "", 0, 50)
	if err != nil {
		return nil, err
	}
	out := make([]tui.Item, 0, len(list))
	for _, p := range list {
		out = append(out, tui.Item{Ref: fmt.Sprintf("PAIN-%d", p.ID), Kind: "pain", Title: p.Title, Meta: p.State})
	}
	return out, nil
}

func projectTasks(c *client.Client, project string) ([]tui.Item, error) {
	list, err := c.ListHumanTasks(project, "open", false, 0)
	if err != nil {
		return nil, err
	}
	out := make([]tui.Item, 0, len(list))
	for _, t := range list {
		meta := fmt.Sprintf("REQ-%d", t.RequestID)
		if t.Assignee != "" {
			meta = t.Assignee + " · " + meta
		}
		out = append(out, tui.Item{Ref: t.HumanID(), Kind: "task", Title: t.Title, Meta: meta})
	}
	return out, nil
}

// roomUnread is what is unread in one room for this terminal, read without
// joining and without moving the read mark. A room that does not exist has
// no messages. where is put before the sender (the project, in the inbox).
func roomUnread(c *client.Client, cfg config.Config, room, where string) ([]tui.Item, error) {
	me := coordSession(cfg)
	after, _ := c.CoordCursor(me, store.DestinationRoom, room)
	msgs, err := c.CoordInbox(store.DestinationRoom, room, me, after, 30)
	if messagesNotFound(err) {
		return []tui.Item{}, nil
	}
	if err != nil {
		return nil, err
	}
	builder := clientBuilder(c, "")
	out := make([]tui.Item, 0, len(msgs))
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Expired || m.SenderExternalID == me {
			continue
		}
		card := builder.MessageCard(store.MessageHit{ID: m.ID, RoomKey: room, Sequence: m.Sequence, Sender: m.SenderExternalID, Body: m.Body})
		meta := m.SenderExternalID
		if where != "" {
			meta = where + " · " + meta
		}
		out = append(out, tui.Item{Ref: fmt.Sprintf("#%d", m.Sequence), Kind: "message", Title: m.Body, Meta: meta, Card: &card})
	}
	return out, nil
}

// messageDays is how far back the message lists read when there is no
// read mark to go by.
const messageDays = 7

// recentMessages are the room messages of the last days the person may
// read, newest first: of one project, or of every project with project "".
// It goes through the search, which reads as the account and needs no room
// membership; so there is no "unread", only "recent".
func recentMessages(c *client.Client, cfg config.Config, project string) ([]tui.Item, error) {
	line := fmt.Sprintf("is:message created:>=%dd", messageDays)
	res, err := c.SearchAnchored(line, "messages", scope.Axes{Project: project}, 40, false, client.AnchorFilter{})
	if err != nil {
		return nil, err
	}
	msgs := res.Messages
	sort.SliceStable(msgs, func(i, j int) bool { return msgs[i].CreatedAt > msgs[j].CreatedAt })
	me := coordSession(cfg)
	out := []tui.Item{}
	for _, m := range msgs {
		if m.Sender == me {
			continue
		}
		card := clientBuilder(c, m.Project).MessageCard(m)
		meta := m.Sender
		out = append(out, tui.Item{Ref: fmt.Sprintf("#%d", m.ID), Kind: "message", Title: m.Body, Meta: meta, Card: &card})
	}
	return out, nil
}

// messageProjectLimit bounds how many projects the inbox reads messages of.
const messageProjectLimit = 25

// recentEverywhere is recentMessages over the visible projects, a few at a
// time; the search answers per project only.
func recentEverywhere(c *client.Client, cfg config.Config) ([]tui.Item, error) {
	projects, err := visibleProjects(c)
	if err != nil {
		return nil, err
	}
	if len(projects) > messageProjectLimit {
		projects = projects[:messageProjectLimit]
	}
	results := make([][]tui.Item, len(projects))
	errs := make([]error, len(projects))
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for i, p := range projects {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			items, err := recentMessages(c, cfg, id)
			for j := range items {
				items[j].Meta = shortProject(id) + " · " + items[j].Meta
			}
			results[i], errs[i] = items, err
		}(i, p.ID)
	}
	wg.Wait()
	out := []tui.Item{}
	var first error
	for i := range results {
		if errs[i] != nil && first == nil {
			first = errs[i]
		}
		out = append(out, results[i]...)
	}
	if len(out) == 0 && first != nil {
		return nil, first
	}
	return out, nil
}

func isForbidden(err error) bool {
	var se *client.StatusError
	var ae *client.APIError
	return (errors.As(err, &se) && se.Status == http.StatusForbidden) || (errors.As(err, &ae) && ae.Status == http.StatusForbidden)
}

// inboxItems are the open inbox entries for the caller that keep matches,
// each with its own card and its project.
func inboxItems(c *client.Client, keep func(client.InboxEntry) bool) ([]tui.Item, error) {
	entries, err := c.Inbox("", "open", true, 100)
	if err != nil {
		return nil, err
	}
	out := []tui.Item{}
	for _, e := range entries {
		if !keep(e) {
			continue
		}
		kind := map[string]string{store.InboxTask: "Task", store.InboxDecision: "Decision", store.InboxAttention: "Question"}[e.Kind]
		status := e.Reason
		if status == "" {
			status = e.State
		}
		card := links.Card{Kind: kind, Ref: e.Ref, Title: e.Title, Status: status, Who: e.ProposedBy, Next: shortProject(e.Project), URL: e.Link}.Sanitized()
		out = append(out, tui.Item{Ref: e.Ref, Kind: e.Kind, Title: e.Title, Meta: shortProject(e.Project), Card: &card})
	}
	return out, nil
}

// suggestions are the gardener's and the distiller's entries waiting for
// review. An account that may not review gets none, not an error.
func suggestions(c *client.Client) ([]tui.Item, error) {
	list, err := c.Pending("", 50)
	if err != nil {
		return []tui.Item{}, nil
	}
	out := make([]tui.Item, 0, len(list))
	for _, p := range list {
		k := p.Knowledge
		out = append(out, tui.Item{Ref: fmt.Sprintf("#%d", k.ID), Kind: k.Type, Title: k.Title, Meta: shortProject(k.Scope.Project)})
	}
	return out, nil
}
