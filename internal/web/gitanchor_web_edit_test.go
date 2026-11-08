package web

import (
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// REQ-597 K9: auch die Korrektur eines Requests und der Zustandswechsel eines
// Pains aus der Weboberfläche tragen die Kennzeichnung "Web", keinen
// erfundenen Git-Stand.
func TestRequestCorrectionFromTheWebIsMarkedWeb(t *testing.T) {
	e := ovEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "Alter Titel", Criteria: []string{"eins"}})
	id := d.Request.ID
	resp := rqPost(t, e, e.Lead, id, "/correct", url.Values{
		"title": {"Neuer Titel"}, "description": {d.Request.Description}, "type": {d.Request.Type}, "priority": {d.Request.Priority},
		"reason": {"Der Titel war ungenau"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correct = %d", resp.StatusCode)
	}
	det, err := e.St.RequestByID(id)
	if err != nil {
		t.Fatal(err)
	}
	var seen bool
	for _, a := range det.Activity {
		if a.Kind == "request.corrected" {
			seen = true
			if a.Via != "web" || a.GitAnchor != nil {
				t.Errorf("correction from the browser: via %q anchor %+v", a.Via, a.GitAnchor)
			}
		}
	}
	if !seen {
		t.Fatalf("no request.corrected activity: %+v", det.Activity)
	}
}

func TestPainStateChangeFromTheWebIsMarkedWeb(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Teilweise lesen"})
	d, err := tpOwner(e).CreatePain(store.CreatePainInput{Project: shellProject, Title: "Lesen geht nur ganz", Body: "b",
		Motivates: []pain.Target{{RequestID: req.Request.ID}}, Actor: pain.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	resp := tpPost(t, e, e.Member, "/ui/pains/"+strconv.FormatInt(d.Pain.ID, 10)+"/state",
		url.Values{"state": {"obsolete"}, "note": {"hat sich erledigt"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("state change = %d", resp.StatusCode)
	}
	got, err := tpOwner(e).PainDetailView(d.Pain.ID)
	if err != nil {
		t.Fatal(err)
	}
	var seen bool
	for _, ev := range got.Events {
		if ev.Kind == "state" || ev.ToState == "obsolete" {
			seen = true
			if ev.Via != "web" || ev.GitAnchor != nil {
				t.Errorf("state change from the browser: via %q anchor %+v", ev.Via, ev.GitAnchor)
			}
		}
	}
	if !seen {
		t.Fatalf("no state event: %+v", got.Events)
	}
}
