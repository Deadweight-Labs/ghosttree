package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Deadweight-Labs/ghosttree/internal/links"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// CommentAddInput is the comment_add tool.
type CommentAddInput struct {
	Ref  string `json:"ref" jsonschema:"what the comment is about: REQ-n, IDEA-n or PAIN-n (a bare number is an issue)"`
	Body string `json:"body" jsonschema:"the comment, plain text. Say what you found or decided, not that you are working on it"`
}

// CommentListInput is the comment_list tool.
type CommentListInput struct {
	Ref string `json:"ref" jsonschema:"REQ-n, IDEA-n or PAIN-n"`
}

// CommentOutput is one comment as the tools show it.
type CommentOutput struct {
	ID        int64  `json:"id"`
	Author    string `json:"author,omitempty"`
	At        string `json:"at"`
	EditedAt  string `json:"edited_at,omitempty"`
	Via       string `json:"via,omitempty"`
	Deleted   bool   `json:"deleted,omitempty"`
	Body      string `json:"body,omitempty"`
	URL       string `json:"url,omitempty"`
	CanEdit   bool   `json:"can_edit"`
	CanDelete bool   `json:"can_delete"`
}

// CommentListOutput lists the comments of one object.
type CommentListOutput struct {
	Ref      string          `json:"ref"`
	Comments []CommentOutput `json:"comments"`
}

func commentTarget(ref string) (string, int64, error) {
	r, err := requestdomain.ParseOriginRef(ref)
	if err != nil || (r.Kind != requestdomain.OriginRequest && r.Kind != requestdomain.OriginIdea && r.Kind != requestdomain.OriginPain) {
		return "", 0, fmt.Errorf("%q is not REQ-n, IDEA-n or PAIN-n", strings.TrimSpace(ref))
	}
	return r.Kind, r.ID, nil
}

func commentOutput(b links.Builder, c store.Comment) CommentOutput {
	out := CommentOutput{ID: c.ID, Author: c.Person, At: c.CreatedAt, EditedAt: c.EditedAt, Via: c.Via, Deleted: c.Deleted, Body: c.Body,
		CanEdit: c.CanEdit, CanDelete: c.CanDelete}
	pb := b.WithProject(c.Project)
	switch c.ObjectKind {
	case store.CommentOnRequest:
		out.URL = pb.Issue(c.ObjectID)
	case store.CommentOnIdea:
		out.URL = pb.Idea(c.ObjectID)
	case store.CommentOnPain:
		out.URL = pb.Pain(c.ObjectID)
	}
	if out.URL != "" {
		out.URL += fmt.Sprintf("#comment-%d", c.ID)
	}
	return out
}

func (s *Server) handleCommentAdd(_ context.Context, _ *mcp.CallToolRequest, in CommentAddInput) (*mcp.CallToolResult, CommentOutput, error) {
	kind, id, err := commentTarget(in.Ref)
	if err != nil {
		return nil, CommentOutput{}, err
	}
	c, err := s.client.AddComment(kind, id, in.Body, 0)
	if err != nil {
		return nil, CommentOutput{}, err
	}
	out := commentOutput(s.builder(), c)
	return s.withCard(requestResult(out), s.builder().CommentCard(c)), out, nil
}

func (s *Server) handleCommentList(_ context.Context, _ *mcp.CallToolRequest, in CommentListInput) (*mcp.CallToolResult, CommentListOutput, error) {
	kind, id, err := commentTarget(in.Ref)
	if err != nil {
		return nil, CommentListOutput{}, err
	}
	list, err := s.client.Comments(kind, id)
	if err != nil {
		return nil, CommentListOutput{}, err
	}
	out := CommentListOutput{Ref: strings.ToUpper(strings.TrimSpace(in.Ref)), Comments: []CommentOutput{}}
	for _, c := range list {
		out.Comments = append(out.Comments, commentOutput(s.builder(), c))
	}
	return requestResult(out), out, nil
}
