package web

import (
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestRefKeyOfReadsTheReferenceOfAnAddress(t *testing.T) {
	for href, want := range map[string]string{
		"/ui/x/shell/issues/12":      "REQ-12",
		"/ui/requests/7":             "REQ-7",
		"/ui/x/shell/pains/5":        "PAIN-5",
		"/ui/x/shell/ideas/3?x=1":    "IDEA-3",
		"#task-4":                    "TASK-4",
		"/ui/x/shell/issues":         "",
		"/ui/x/shell/issues?q=a/1":   "",
		"/ui/knowledge/9":            "",
		"/ui/x/shell/pulls/9":        "",
		"https://example.org/pains/": "",
	} {
		if got := refKeyOf(href); got != want {
			t.Errorf("refKeyOf(%q) = %q, want %q", href, got, want)
		}
	}
}

// The card of a reference carries what the viewer may read and nothing else:
// an object of a project the viewer cannot see is as unknown as one that does
// not exist, for requests, pains, tasks and ideas alike.
func TestRefCardsOnlyForWhatTheViewerMayRead(t *testing.T) {
	e := ovEnv(t)
	a := newApp(e.St).(*appHandler).app
	owner := tpOwner(e)
	vis := rqMake(t, e.St, rqSpec{Title: "Sichtbarer Auftrag", Desc: "Erste Zeile\nZweite Zeile", Criteria: []string{"eins", "zwei"}})
	hid := rqMake(t, e.St, rqSpec{Title: "VERSTECKTER-AUFTRAG", Project: shellHiddenProject})
	visPain, err := owner.CreatePain(store.CreatePainInput{Project: shellProject, Title: "Sichtbarer Pain", Body: "b", Actor: pain.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	hidPain, err := owner.CreatePain(store.CreatePainInput{Project: shellHiddenProject, Title: "VERSTECKTER-PAIN", Body: "b", Actor: pain.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	visIdea, err := owner.CreateIdea(store.CreateIdeaInput{Project: shellProject, Title: "Sichtbare Idee", Body: "i", Actor: idea.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	hidIdea, err := owner.CreateIdea(store.CreateIdeaInput{Project: shellHiddenProject, Title: "VERSTECKTE-IDEE", Body: "i", Actor: idea.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	visTask := tpTask(t, e, shellProject, vis.Request.ID, humantask.CreateInput{Title: "Sichtbare Aufgabe"})
	hidTask := tpTask(t, e, shellHiddenProject, hid.Request.ID, humantask.CreateInput{Title: "VERSTECKTE-AUFGABE"})

	pa := e.St.Access(store.Principal{ID: "person:2", Label: "anna"})
	l := lang("de")
	now := overviewNow().UTC()
	for _, c := range []struct {
		prefix  string
		id      int64
		visible bool
		title   string
	}{
		{"REQ", vis.Request.ID, true, "Sichtbarer Auftrag"}, {"REQ", hid.Request.ID, false, ""},
		{"PAIN", visPain.Pain.ID, true, "Sichtbarer Pain"}, {"PAIN", hidPain.Pain.ID, false, ""},
		{"IDEA", visIdea.ID, true, "Sichtbare Idee"}, {"IDEA", hidIdea.ID, false, ""},
		{"TASK", visTask.ID, true, "Sichtbare Aufgabe"}, {"TASK", hidTask.ID, false, ""},
		{"REQ", 99999, false, ""},
	} {
		card, href, ok := a.resolveRefCard(l, pa, now, c.prefix, c.id)
		if ok != c.visible {
			t.Errorf("%s-%d: card = %v, want %v", c.prefix, c.id, ok, c.visible)
			continue
		}
		if ok && (card.Title != c.title || card.Key != c.prefix+"-"+strconv.FormatInt(c.id, 10)) {
			t.Errorf("%s-%d: card %+v", c.prefix, c.id, card)
		}
		if ok && c.prefix != "TASK" && !strings.HasPrefix(href, "/ui/") {
			t.Errorf("%s-%d: href %q", c.prefix, c.id, href)
		}
	}
	card, _, _ := a.resolveRefCard(l, pa, now, "REQ", vis.Request.ID)
	if card.Total != 2 || card.State != "Offen" || card.Who != "alice" || !strings.HasPrefix(card.Brief, "Erste Zeile") {
		t.Errorf("request card: %+v", card)
	}
}

// A page that names a reference carries the card of it as a template, and a
// reference the viewer cannot read has none.
func TestPageCarriesTheCardsOfItsReferences(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Teilweise lesen", Desc: "Nur den Anfang laden.", Criteria: []string{"a"}})
	d, err := tpOwner(e).CreatePain(store.CreatePainInput{Project: shellProject, Title: "Lesen geht nur ganz", Body: "b",
		Motivates: []pain.Target{{RequestID: req.Request.ID}}, Actor: pain.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	key := "REQ-" + strconv.FormatInt(req.Request.ID, 10)
	_, page := fetchPage(t, e.Member, e.Base+"/ui/x/shell/pains/"+strconv.FormatInt(d.Pain.ID, 10))
	for _, want := range []string{`data-ref="` + key + `"`, `<template data-ref-card="` + key + `"`, "Teilweise lesen", "Nur den Anfang laden.", `class="pc-t"`} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if !strings.Contains(page, `/static/ui-refs.js`) || !strings.Contains(page, `/static/ui-md.js`) {
		t.Error("page does not load the scripts of the cards and the preview")
	}
	// The card carries no script and no inline style.
	if strings.Contains(page, "<template data-ref-card") && strings.Contains(page[strings.Index(page, "<template data-ref-card"):], " style=") {
		t.Error("a card carries a style attribute")
	}
}
