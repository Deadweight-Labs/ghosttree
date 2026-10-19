package server

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

var requestVolatile = regexp.MustCompile(`"(created_at|updated_at|started_at|ended_at|at)":"[^"]*"`)

// requestOracleBodies legt einen Auftrag mit einer lesbaren und hidden
// verborgenen Sessions an und liefert, was ein Gast über /api/requests/{id}
// und /api/requests sieht (ohne Zeitstempel und die Adresse der lesbaren Session).
func requestOracleBodies(t *testing.T, hidden int) (detail, list string) {
	t.Helper()
	f := oracleFixture(t, hidden)
	shared := f.id["o-shared"]
	created, err := f.st.CreateRequest(requestdomain.CreateInput{
		Request:  requestdomain.Request{Type: "feature", Title: "oracle", Description: "d", Scope: scope.Axes{Project: accProject}, Person: "rex"},
		Criteria: []string{"one", "two"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rid := created.Request.ID
	work := func(session int64, text string) {
		w, _, err := f.st.StartRequestWork(rid, session, "primary", "rex")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.st.FinishRequestWork(w.ID, "paused", text, "rex"); err != nil {
			t.Fatal(err)
		}
	}
	work(shared, "sichtbare Uebergabe")
	for i := 0; i < hidden; i++ {
		work(shared+int64(i)+1, fmt.Sprintf("GEHEIM-Uebergabe-%d", i))
	}
	c := created.Criteria
	if len(c) == 0 {
		d, _ := f.st.RequestByID(rid)
		c = d.Criteria
	}
	ev := func(ref string) requestdomain.Evidence {
		return requestdomain.Evidence{Kind: "session", Ref: ref, Person: "rex"}
	}
	if err := f.st.SetCriterionState(c[0].ID, "met", ev(fmt.Sprintf("session:%d#chunk:0", shared))); err != nil {
		t.Fatal(err)
	}
	hiddenRef := fmt.Sprintf("session:%d#chunk:7", shared+1)
	if hidden == 0 {
		hiddenRef = "session:999#chunk:7" // existiert nicht: für den Gast nicht von verborgen zu unterscheiden
	}
	if err := f.st.SetCriterionState(c[1].ID, "met", ev(hiddenRef)); err != nil {
		t.Fatal(err)
	}
	if err := f.st.CompleteRequest(rid, ev(hiddenRef)); err != nil {
		t.Fatal(err)
	}
	detail = f.expect(t, "gus", 200, "GET", fmt.Sprintf("/api/requests/%d", rid), nil)
	list = f.expect(t, "gus", 200, "GET", "/api/requests?state=done", nil)
	norm := func(s string) string {
		s = requestVolatile.ReplaceAllString(s, `"$1":""`)
		sess, err := f.st.SessionByID(shared)
		if err != nil {
			t.Fatal(err)
		}
		return strings.ReplaceAll(s, sess.PublicID, "PUB")
	}
	return norm(detail), norm(list)
}

func TestGuestRequestViewDoesNotDependOnHiddenSessions(t *testing.T) {
	d0, l0 := requestOracleBodies(t, 0)
	d3, l3 := requestOracleBodies(t, 3)
	for name, body := range map[string]string{"detail": d3, "list": l3} {
		if regexp.MustCompile(`session:\d`).MatchString(body) {
			t.Errorf("%s names a numeric session ref: %s", name, body)
		}
		if strings.Contains(body, "GEHEIM") {
			t.Errorf("%s carries hidden handoff text: %s", name, body)
		}
	}
	if !strings.Contains(d3, "sichtbare Uebergabe") || !strings.Contains(l3, "sichtbare Uebergabe") {
		t.Errorf("readable handoff missing:\n%s\n%s", d3, l3)
	}
	if d0 != d3 {
		t.Errorf("detail depends on hidden sessions:\n%s\n%s", d0, d3)
	}
	if l0 != l3 {
		t.Errorf("list depends on hidden sessions:\n%s\n%s", l0, l3)
	}
}
