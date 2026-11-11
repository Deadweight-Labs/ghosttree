package client

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// ReactionsOut sind die Reaktionen eines Objekts mit der festen Liste der erlaubten.
type ReactionsOut struct {
	store.ReactionSummary
	Choices []store.ReactionKind `json:"choices,omitempty"`
}

// ReactionTarget löst die Angabe eines Objekts auf (REQ-n, IDEA-n oder comment:n)
// und liefert Art und Nummer, wie die API sie nimmt.
func ReactionTarget(ref string) (string, int64, error) {
	ref = strings.TrimSpace(ref)
	if rest, ok := strings.CutPrefix(strings.ToLower(ref), "comment:"); ok {
		n, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
		if err != nil || n <= 0 {
			return "", 0, fmt.Errorf("%q is not REQ-n, IDEA-n or comment:n", ref)
		}
		return store.ReactOnComment, n, nil
	}
	r, err := requestdomain.ParseOriginRef(ref)
	if err != nil || (r.Kind != requestdomain.OriginRequest && r.Kind != requestdomain.OriginIdea) {
		return "", 0, fmt.Errorf("%q is not REQ-n, IDEA-n or comment:n", ref)
	}
	return r.Kind, r.ID, nil
}

// React setzt die Reaktion des Aufrufers an einem Kommentar, einer Idee oder einem Issue.
func (c *Client) React(kind string, id int64, content string) (store.ReactionSummary, error) {
	var out store.ReactionSummary
	err := c.do("PUT", "/api/reactions", nil, map[string]any{"kind": kind, "id": id, "content": content}, &out)
	return out, err
}

// Unreact nimmt sie zurück.
func (c *Client) Unreact(kind string, id int64, content string) (store.ReactionSummary, error) {
	var out store.ReactionSummary
	err := c.do("DELETE", "/api/reactions", url.Values{"kind": {kind}, "id": {strconv.FormatInt(id, 10)}, "content": {content}}, nil, &out)
	return out, err
}

// Reactions liefert die Reaktionen eines Objekts.
func (c *Client) Reactions(kind string, id int64) (ReactionsOut, error) {
	var out ReactionsOut
	err := c.do("GET", "/api/reactions", url.Values{"kind": {kind}, "id": {strconv.FormatInt(id, 10)}}, nil, &out)
	return out, err
}

// ReactionLine beschreibt die Reaktionen eines Objekts in einer Zeile (ein Hinweis,
// wenn der Aufrufer nicht reagieren darf, in der nächsten).
func ReactionLine(ref string, s store.ReactionSummary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: ", ref)
	if len(s.Items) == 0 {
		b.WriteString("no reactions")
	}
	for i, it := range s.Items {
		if i > 0 {
			b.WriteString(", ")
		}
		mine := ""
		if it.Mine {
			mine = " (yours)"
		}
		fmt.Fprintf(&b, "%s %s x%d%s", it.Emoji, it.Content, it.Count, mine)
	}
	if !s.CanReact {
		b.WriteString("\nyou may read this but not react to it")
	}
	return b.String()
}
