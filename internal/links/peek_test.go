package links

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/feature"
	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// fakeReader kennt genau einen Auftrag (REQ-7) und einen PR (12); alles andere
// ist sql.ErrNoRows.
type fakeReader struct{ pulls bool }

func (fakeReader) GetRequest(id int64) (requestdomain.Detail, error) {
	if id != 7 {
		return requestdomain.Detail{}, sql.ErrNoRows
	}
	d := requestdomain.Detail{}
	d.Request = requestdomain.Request{ID: 7, Title: "Titel\nzweizeilig", Description: "Anfang des Textes.\nMehr.", State: "open", Person: "rex", CreatedAt: "2026-11-01T10:00:00Z"}
	d.Request.Scope.Project = "github.com/dw/p"
	return d, nil
}
func (fakeReader) GetHumanTask(int64) (humantask.Task, error) { return humantask.Task{}, sql.ErrNoRows }
func (fakeReader) GetPain(int64) (pain.Detail, error)         { return pain.Detail{}, sql.ErrNoRows }
func (fakeReader) GetIdea(int64) (idea.Idea, error)           { return idea.Idea{}, sql.ErrNoRows }
func (fakeReader) GetFeature(int64) (feature.Feature, error)  { return feature.Feature{}, sql.ErrNoRows }
func (fakeReader) GetDecision(int64) (idea.Decision, error)   { return idea.Decision{}, sql.ErrNoRows }
func (fakeReader) KnowledgeByID(int64) (store.Knowledge, error) {
	return store.Knowledge{}, sql.ErrNoRows
}

type fakePulls struct{ fakeReader }

func (fakePulls) PullRequest(project string, n int64) (store.PullRequestInfo, error) {
	if n != 12 {
		return store.PullRequestInfo{}, sql.ErrNoRows
	}
	return store.PullRequestInfo{Project: project, Number: 12, Title: "PR", State: "merged", Head: "a", Base: "main", OpenedAt: "2026-11-02T10:00:00Z"}, nil
}

func TestPeekBuildsTheHoverCardFromTheSameCardAsOpenLink(t *testing.T) {
	b := New("https://gt.example.org", "github.com/dw/p")
	got, err := Peek(b, fakeReader{}, Ref{Kind: KindRequest, ID: 7}, "")
	if err != nil {
		t.Fatal(err)
	}
	card := b.RequestCard(fakeRequest())
	if got.Link != card.URL || got.Zustand != card.Status || got.Ref != "REQ-7" || got.Kind != "request" || got.Nummer != 7 {
		t.Fatalf("info %+v, card %+v", got, card)
	}
	if got.Titel != "Titel zweizeilig" || got.Kurzfassung != "Anfang des Textes. Mehr." || got.Autor != "rex" || got.Zeit != "2026-11-01T10:00:00Z" || !got.Gefunden {
		t.Fatalf("info %+v", got)
	}
}

func fakeRequest() requestdomain.Detail {
	d, _ := fakeReader{}.GetRequest(7)
	return d
}

func TestPeekAnswersEveryFailureTheSame(t *testing.T) {
	b := New("https://gt.example.org", "github.com/dw/p")
	var msgs []string
	for _, ref := range []Ref{
		{Kind: KindRequest, ID: 8}, {Kind: KindPain, ID: 1}, {Kind: KindKnowledge, ID: 1},
		{Kind: KindCommit, Text: "abcdef1"}, {Kind: KindPull, ID: 12}, {Kind: KindPull, ID: 12, Text: ""},
	} {
		if _, err := Peek(b, fakeReader{}, ref, "github.com/dw/p"); err == nil {
			t.Errorf("%+v: no error", ref)
		} else if strings.Contains(err.Error(), "forbidden") {
			t.Errorf("%+v: %v", ref, err)
		} else {
			msgs = append(msgs, err.Error())
		}
	}
	if len(msgs) != 6 {
		t.Fatalf("messages: %v", msgs)
	}
}

func TestPeekPullNeedsAProjectAndAReaderThatKnowsPulls(t *testing.T) {
	b := New("https://gt.example.org", "")
	if _, err := Peek(b, fakePulls{}, Ref{Kind: KindPull, ID: 12}, ""); err == nil {
		t.Fatal("a pull request without a project was resolved")
	}
	got, err := Peek(b, fakePulls{}, Ref{Kind: KindPull, ID: 12}, "github.com/dw/p")
	if err != nil || got.Zustand != "merged" || got.Link != "https://gt.example.org/ui/dw/p/pulls/12" || got.Kurzfassung != "a → main" {
		t.Fatalf("pull: %+v %v", got, err)
	}
}

func TestLookupKeepsOrderMarksMissingAndCapsTheCount(t *testing.T) {
	b := New("https://gt.example.org", "github.com/dw/p")
	list, err := Lookup(b, fakeReader{}, []string{"REQ-8", "REQ-7", "commit:abcdef1"}, "")
	if err != nil || len(list) != 3 || list[0].Gefunden || !list[1].Gefunden || list[2].Gefunden || list[0].Ref != "REQ-8" {
		t.Fatalf("lookup: %+v %v", list, err)
	}
	if list[0] != Missing("REQ-8") {
		t.Fatalf("missing entry carries more than the reference: %+v", list[0])
	}
	if _, err := Lookup(b, fakeReader{}, []string{"nonsense"}, ""); err == nil {
		t.Fatal("an unreadable reference was accepted")
	}
	var many []string
	for i := 0; i <= MaxPeekRefs; i++ {
		many = append(many, fmt.Sprintf("REQ-%d", i+1))
	}
	if _, err := Lookup(b, fakeReader{}, many, ""); err == nil {
		t.Fatal("more than the maximum was accepted")
	}
}
