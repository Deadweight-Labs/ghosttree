package mcpserver

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/links"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// builder makes the web addresses of this server for the session's project.
// An object of another project carries its own project into its card.
func (s *Server) builder() links.Builder {
	return links.New(s.client.ServerURL(), s.ctxAxes.Project)
}

// withCard appends the card of a single object (title, state, next step, who,
// link) to a tool result as a text block of its own. The structured output of
// the tool stays as it was. Without a server address there is no link and no
// card: a card without its link is not worth the tokens.
func (s *Server) withCard(res *mcp.CallToolResult, c links.Card) *mcp.CallToolResult {
	if res == nil || c.URL == "" {
		return res
	}
	res.Content = append(res.Content, &mcp.TextContent{Text: c.String()})
	return res
}

// withRequestLink gives work replies the address of the issue they are about.
// Work carries no title; the link is what the human needs.
func (s *Server) withRequestLink(res *mcp.CallToolResult, requestID int64, err error) *mcp.CallToolResult {
	if err != nil || requestID == 0 {
		return res
	}
	return s.withCard(res, links.Card{Kind: "Issue", Ref: fmt.Sprintf("REQ-%d", requestID), URL: s.builder().Issue(requestID)})
}

// hitLink puts the web address at the end of a one-line search hit.
func hitLink(line, url string) string {
	if url == "" {
		return line
	}
	return strings.TrimSuffix(line, "\n") + " " + url + "\n"
}

// sessionURL is the address of a session hit: its public address when it has
// one, else its number.
func (s *Server) sessionURL(sess store.Session) string {
	id := sess.PublicID
	if id == "" {
		id = strconv.FormatInt(sess.ID, 10)
	}
	return s.builder().WithProject(sess.Scope.Project).Session(id)
}

// withSessionCard gives the answer that reads one session its card. The card
// comes from the same lookup as open_link, so a session the caller may not read
// gets none (the reading itself would have failed first).
func (s *Server) withSessionCard(res *mcp.CallToolResult, in SessionsInput) *mcp.CallToolResult {
	id := in.SessionRef
	if id == "" {
		id = strconv.FormatInt(in.SessionID, 10)
	}
	ref, err := links.ParseRef("session:" + id)
	if err != nil {
		return res
	}
	card, err := s.linkEnv().Resolve(ref)
	if err != nil {
		return res
	}
	return s.withCard(res, card)
}

// OpenLinkInput is the open_link tool.
type OpenLinkInput struct {
	Ref string `json:"ref" jsonschema:"what to link: REQ-n, AC-n.m or REQ-n#m (a criterion), TASK-n, PAIN-n, IDEA-n, PKG-n, #id (a knowledge entry), pr:n, commit:sha, room:n, session:id, doc:slug, runbook:name, branch:name (its checklist), preset:name followed by its values (preset:pr-ergebnis 155), a repository path, a search line like is:open label:bug, or inbox"`
}

// OpenLinkOutput is the link and the card to hand to the human.
type OpenLinkOutput struct {
	URL  string     `json:"url"`
	Card links.Card `json:"card"`
}

func (s *Server) linkEnv() links.Env {
	return links.Env{Builder: s.builder(), API: s.client, Project: s.ctxAxes.Project, Branch: s.ctxAxes.Branch, RepoRoot: s.repoRoot, Agent: s.coordRef()}
}

// handleOpenLink answers with the web address and the card of one object. It
// opens nothing: the agent passes the link to the human, who clicks it. The
// address carries no login; a browser that is not signed in is led back to
// this page after signing in.
func (s *Server) handleOpenLink(_ context.Context, _ *mcp.CallToolRequest, in OpenLinkInput) (*mcp.CallToolResult, OpenLinkOutput, error) {
	ref, err := links.ParseRef(in.Ref)
	if err != nil {
		return nil, OpenLinkOutput{}, err
	}
	card, err := s.linkEnv().Resolve(ref)
	if err != nil {
		return nil, OpenLinkOutput{}, err
	}
	return textResult(card.String()), OpenLinkOutput{URL: card.URL, Card: card}, nil
}

func (s *Server) registerLinkTools(srv *mcp.Server) {
	closed := false
	mcp.AddTool(srv, &mcp.Tool{Name: "open_link", Description: "Get the web link and a compact card (title, state, next step, who) for one object, to hand to the human: \"open this requirement in the browser\". It opens nothing itself and the link carries no login. For an object you may not read you get the same answer as for one that does not exist.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handleOpenLink)
}
