package main

import (
	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// seedComments adds what the pages of an issue, an idea and a pain show beyond
// the history: comments (one edited, one deleted), an issue that came out of an
// idea and out of another issue, and a finished task for "Show done".
func seedComments(st *store.Store, project string) {
	robin := st.Access(store.Principal{ID: "person:1", Label: "Robin", TokenKind: store.WebSessionKind})
	lena := st.Access(store.Principal{ID: "person:2", Label: "Lena Berger", TokenKind: store.WebSessionKind})
	as := func(name string) store.CommentActor {
		return store.CommentActor{Person: name, GitAnchor: gitanchor.Web()}
	}
	hits, err := st.SearchRequestsWith(robin.RequestFilter(requestdomain.SearchFilter{Scope: scope.Axes{Project: project}, State: "open", Limit: 10}), store.SearchAuto)
	must(err)
	parent := hits.Results[len(hits.Results)-1].Request.ID
	ideas, err := robin.SearchIdeas(idea.Filter{Project: project, State: "open", Limit: 5})
	must(err)
	// The idea with a workshop is not approved and cannot be derived from; take the plain one.
	var ideaID int64
	for _, i := range ideas {
		if i.Title == "Voice control in the inbox" {
			ideaID = i.ID
		}
	}
	derived := []string{}
	if ideaID != 0 {
		derived = append(derived, "IDEA-"+strconv.FormatInt(ideaID, 10))
	}
	links, err := robin.ResolveRequestLinks(project, "REQ-"+strconv.FormatInt(parent, 10), derived)
	must(err)
	d, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Answer inbox questions by voice", Scope: scope.Axes{Project: project}, Person: "Robin"},
		Criteria: []string{"A question can be answered by voice", "The answer is shown as text before it is sent"}, Links: links})
	must(err)

	c, err := lena.AddComment("request", d.Request.ID, "Short yes/no questions would profit most. Should this also cover answers to mentions?", as("Lena Berger"))
	must(err)
	_, err = lena.EditComment(c.ID, "Short yes/no questions would profit most. Should this also cover **answers to mentions**?", as("Lena Berger"))
	must(err)
	_, err = robin.AddComment("request", d.Request.ID, "Yes, mentions too. The text stays editable before it goes out.", as("Robin"))
	must(err)
	gone, err := lena.AddComment("request", d.Request.ID, "Never mind, wrong issue.", as("Lena Berger"))
	must(err)
	_, err = lena.DeleteComment(gone.ID, as("Lena Berger"))
	must(err)

	_, err = robin.MarkTasksDone([]int64{2}, "dump is in place", humantask.Actor{AccountID: 1, Person: "Robin", Web: true})
	must(err)
	if ideaID != 0 {
		_, err = lena.AddComment("idea", ideaID, "Works best for short answers; long ones are easier to type.", as("Lena Berger"))
		must(err)
	}
	painList, err := robin.SearchPains(pain.Filter{Project: project, State: "active", Limit: 3})
	must(err)
	if len(painList) > 0 {
		_, err = lena.AddComment("pain", painList[0].ID, "Seen it too: after a restart the first read takes minutes.", as("Lena Berger"))
		must(err)
	}
}
