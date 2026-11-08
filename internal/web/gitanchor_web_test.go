package web

import (
	"net/http"
	"net/url"
	"strconv"
	"testing"
)

// Was in der Weboberfläche geschrieben wird, trägt keinen Git-Anker (es gibt
// dort kein Arbeitsverzeichnis), aber die Kennzeichnung "Web" (REQ-597).
func TestWritesFromTheWebInterfaceAreMarkedWeb(t *testing.T) {
	e := ovEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "Web marked", Criteria: []string{"Existing"}})
	id := d.Request.ID

	if resp := rqPost(t, e, e.Lead, id, "/criteria", url.Values{"description": {"Added in the browser"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("adding a criterion = %d", resp.StatusCode)
	}
	det, _ := e.St.RequestByID(id)
	cid := det.Criteria[0].ID
	if resp := rqPost(t, e, e.Lead, id, "/criteria/"+strconv.FormatInt(cid, 10), url.Values{"evidence_kind": {"test"}, "evidence_ref": {"TestX"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("resolving a criterion = %d", resp.StatusCode)
	}
	det, _ = e.St.RequestByID(id)
	var added bool
	for _, a := range det.Activity {
		if a.Kind == "criterion.added" && a.Data == "Added in the browser" {
			added = true
			if a.Via != "web" || a.GitAnchor != nil {
				t.Errorf("criterion added in the browser: via %q anchor %+v", a.Via, a.GitAnchor)
			}
		}
	}
	if !added {
		t.Fatalf("no criterion.added activity: %+v", det.Activity)
	}
	var marked int
	for _, c := range det.Criteria {
		for _, ev := range c.Evidence {
			if ev.Via == "web" && ev.GitAnchor == nil {
				marked++
			}
		}
	}
	if marked != 1 {
		t.Errorf("evidence given in the browser marked Web: %d of %+v", marked, det.Criteria)
	}
}
