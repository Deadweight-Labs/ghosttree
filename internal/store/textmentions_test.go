package store

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// An @name in a comment, an issue or a pain reaches the person in their inbox
// (REQ-677); a name that reaches nobody comes back to the sender.
func TestMentionInATextReachesThePersonAndTellsTheSenderWhoWasMissed(t *testing.T) {
	w := newPainWorld(t)
	reqID := w.request(t, "Spool deckeln")
	robin, lena, nora := w.pa["robin"], w.pa["lena"], w.pa["nora"]

	// Comment: lena is a lead of the project, nora is not part of the org.
	body := "@lena kannst du das ansehen? Und @nora?"
	if _, err := robin.AddComment(CommentOnRequest, reqID, body, CommentActor{Person: "robin"}); err != nil {
		t.Fatal(err)
	}
	got, err := lena.TextMentions(roleProject)
	if err != nil || len(got) != 1 {
		t.Fatalf("lena's mentions = %+v, %v", got, err)
	}
	m := got[0]
	if m.SourceKind != MentionInComment || m.ObjectKind != CommentOnRequest || m.ObjectID != reqID || m.Author != "robin" || !strings.Contains(m.Snippet, "kannst du das ansehen") {
		t.Fatalf("mention = %+v", m)
	}
	if got, _ := nora.TextMentions(roleProject); len(got) != 0 {
		t.Fatalf("nora has no access and must not be told: %+v", got)
	}
	if un := robin.UnreachedTextMentions(roleProject, body); len(un) != 1 || un[0] != "nora" {
		t.Fatalf("the sender is told who was missed: %v", un)
	}
	if got, _ := robin.TextMentions(roleProject); len(got) != 0 {
		t.Fatalf("nobody is told about their own text: %+v", got)
	}

	// Issue: the description of a new request mentions lena.
	if _, err := w.st.CreateRequest(newRequestWithText("@lena bitte prüfen")); err != nil {
		t.Fatal(err)
	}
	// Pain: the text of a pain mentions lena too.
	if _, err := robin.CreatePain(CreatePainInput{Project: roleProject, Title: "Langsam", Body: "Merkt @lena auch", Actor: pain.Actor{Person: "robin"}, AuthorID: 1}); err != nil {
		t.Fatal(err)
	}
	got, _ = lena.TextMentions(roleProject)
	kinds := map[string]bool{}
	for _, m := range got {
		kinds[m.SourceKind] = true
	}
	if len(got) != 3 || !kinds[MentionInComment] || !kinds[MentionInRequest] || !kinds[MentionInPain] {
		t.Fatalf("lena should be told in a comment, an issue and a pain: %+v", got)
	}

	// Editing the comment does not tell lena again, and marking read empties the inbox.
	if err := lena.MarkTextMentionRead(got[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := robin.MarkTextMentionRead(got[1].ID); err != ErrTextMentionNotFound {
		t.Fatalf("someone else's mention is not theirs to mark: %v", err)
	}
	left, _ := lena.TextMentions(roleProject)
	if len(left) != 2 {
		t.Fatalf("after reading one: %+v", left)
	}
}

// A mention is no way to see what one may not read: the row stays hidden while
// the person has no access to the object.
func TestMentionIsHiddenWhenTheRecipientCanNoLongerReadTheText(t *testing.T) {
	w := newPainWorld(t)
	reqID := w.request(t, "Spool deckeln")
	if _, err := w.pa["robin"].AddComment(CommentOnRequest, reqID, "@mia schau mal", CommentActor{Person: "robin"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.pa["mia"].TextMentions(roleProject); len(got) != 1 {
		t.Fatalf("mia: %+v", got)
	}
	if err := w.st.RemoveProjectRole("person:1", roleProject, "person:3", RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	fresh := w.st.Access(Principal{ID: "person:3", Label: "mia"})
	if got, _ := fresh.TextMentions(roleProject); len(got) != 0 {
		t.Fatalf("mia lost access but still sees: %+v", got)
	}
}

func newRequestWithText(text string) requestdomain.CreateInput {
	return requestdomain.CreateInput{
		Request:  requestdomain.Request{Type: "feature", Title: "Neu", Description: text, Scope: scope.Axes{Project: roleProject}, Person: "robin"},
		Criteria: []string{"it works"}, AuthorID: 1}
}
