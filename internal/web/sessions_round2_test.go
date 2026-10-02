package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestAPageCutNeverSplitsTheBlocksOfOneLine(t *testing.T) {
	// One stored line carries more blocks than a page may render. The cut has
	// to fall between lines, or the rest of that line is lost without a trace.
	var blocks []map[string]any
	for i := 0; i < maxRenderedRows+500; i++ {
		blocks = append(blocks, tb("part"))
	}
	cs := chunks(uLine("first"), aLine(blocks...), uLine("after the big line"))
	sess := store.Session{Harness: "claude-code"}
	out, cut := buildBlocks(sess, cs, newHighlighter(""), 0, false, true, false, nil)
	parts := 0
	for _, b := range out {
		if b.Kind == "assistant" {
			parts++
		}
	}
	if parts != len(blocks) {
		t.Errorf("the line shows %d of its %d blocks", parts, len(blocks))
	}
	if cut != 0 && cut != 2 {
		t.Errorf("cut = %d, want the start of the next line (2) or no cut", cut)
	}
	if cut == 2 {
		for _, b := range out {
			if b.Seq >= cut {
				t.Errorf("block of line %d shown before the cut at %d", b.Seq, cut)
			}
		}
	}
}

func TestTimesAreShownInOneZone(t *testing.T) {
	old := time.Local
	time.Local = time.FixedZone("test", 2*3600)
	defer func() { time.Local = old }()
	if got := clock("2026-10-01T22:30:00Z"); got != "00:30" {
		t.Errorf("clock = %q, want the local 00:30", got)
	}
	now := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC) // 11:00 local
	if got := dayAndClock("2026-10-01T22:30:00Z", now); !strings.HasPrefix(got, "Today") || !strings.HasSuffix(got, "00:30") {
		t.Errorf("dayAndClock = %q, want today 00:30 (the stamp is on the local day of now)", got)
	}
	meta := detailMeta(store.Session{StartedAt: "2026-10-01T22:30:00Z", LastSeenAt: "2026-10-01T22:30:00Z"})
	if !strings.Contains(meta, "00:30") {
		t.Errorf("detail meta = %q", meta)
	}
}

func TestShareConfirmationIsAPageOfItsOwnNotAPostResponse(t *testing.T) {
	e := seedSessions(t)
	id, err := e.St.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "leaky2", AccountID: 2, Scope: scope.Axes{Project: shellProject, Machine: "laptop"}})
	if err != nil {
		t.Fatal(err)
	}
	tok := "ghp_" + strings.Repeat("a1B2", 9)
	if err := e.St.AppendChunks(id, chunks(uLine("key "+tok))); err != nil {
		t.Fatal(err)
	}
	sess, _ := e.St.SessionByID(id)
	token := renderedCSRFToken(t, e.Member, e.Base+"/ui/sessions")
	resp := sameOriginPostForm(t, e.Member, e.Base+"/ui/sessions/"+sess.PublicID+"/share", url.Values{"csrf_token": {token}, "level": {store.VisGuests}})
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/ui/sessions/"+sess.PublicID) || !strings.Contains(loc, "confirm=guests") {
		t.Fatalf("POST answered %d %q, want a redirect to the confirmation page", resp.StatusCode, loc)
	}
	code, page := e.get(t, e.Member, loc)
	if code != 200 || !strings.Contains(page, "1 possible secret") || !strings.Contains(page, `name="confirm" value="1"`) {
		t.Fatalf("confirmation page: %d %.300s", code, page)
	}
	// Reloading is harmless and changes nothing.
	e.get(t, e.Member, loc)
	if got, _ := e.St.SessionByID(id); got.Visibility == store.VisGuests {
		t.Error("level changed by looking at the question")
	}
	// Somebody who may not share sees no question.
	_, other := e.get(t, e.Reviewer, loc)
	if strings.Contains(other, `name="confirm"`) {
		t.Error("a viewer who may not share is asked")
	}
	// A clean session or an unknown value shows the plain page.
	_, plain := e.get(t, e.Member, "/ui/sessions/"+sess.PublicID+"?confirm=bogus")
	if strings.Contains(plain, `name="confirm"`) {
		t.Error("unknown confirm value shows a question")
	}
}
