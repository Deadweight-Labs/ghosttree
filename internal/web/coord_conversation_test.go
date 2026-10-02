package web

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// cssBodies liefert die Deklarationen aller Blattregeln, deren Selektorliste
// (kommagetrennt) genau sel enthält.
func cssBodies(t *testing.T, sel string) []string {
	t.Helper()
	var out []string
	for _, r := range parseCSSRules(string(mustReadEmbedded(t, "static/app.css"))) {
		if strings.Join(strings.Fields(r.selector), " ") == sel {
			out = append(out, r.body)
			continue
		}
		if strings.Contains(r.selector, ":where(") {
			continue
		}
		for _, part := range strings.Split(r.selector, ",") {
			if strings.Join(strings.Fields(part), " ") == sel {
				out = append(out, r.body)
				break
			}
		}
	}
	return out
}

func requireCSS(t *testing.T, sel string, wants ...string) {
	t.Helper()
	bodies := cssBodies(t, sel)
	if len(bodies) == 0 {
		t.Fatalf("CSS rule %q missing", sel)
	}
	all := strings.Join(bodies, "\n")
	for _, want := range wants {
		if !strings.Contains(all, want) {
			t.Errorf("%s: missing %q in %s", sel, want, all)
		}
	}
}

func coordTemplate(t *testing.T) string { return string(mustReadEmbedded(t, "templates/coord.html")) }

func TestCoordExpiryAcceptsDatetimeLocalAndRFC3339(t *testing.T) {
	server := time.FixedZone("CEST", 2*60*60)
	for _, tc := range []struct{ in, offset, want string }{
		{"", "", ""},
		{"", "120", ""},
		{"2026-09-18T18:30", "", "2026-09-18T16:30:00Z"},
		{"2026-09-18T18:30:15", "", "2026-09-18T16:30:15Z"},
		{"2026-09-18T18:30:00Z", "", "2026-09-18T18:30:00Z"},
		{"2026-09-18T18:30:00+02:00", "", "2026-09-18T16:30:00Z"},
		// The browser offset (minutes east of UTC) beats the server zone.
		{"2026-07-01T18:30", "120", "2026-07-01T16:30:00Z"},
		{"2026-01-15T18:30", "60", "2026-01-15T17:30:00Z"},
		{"2026-01-15T18:30", "-300", "2026-01-15T23:30:00Z"},
		{"2026-01-15T18:30", "", "2026-01-15T16:30:00Z"},
	} {
		got, err := parseCoordExpiry(tc.in, tc.offset, server)
		if err != nil || got != tc.want {
			t.Errorf("parseCoordExpiry(%q,%q)=%q,%v want %q", tc.in, tc.offset, got, err, tc.want)
		}
	}
}

func TestCoordExpiryRejectsInvalidValues(t *testing.T) {
	server := time.FixedZone("CEST", 2*60*60)
	for _, tc := range []struct{ in, offset string }{
		{"not a date", ""}, {"2026-09-18T18", ""}, {"2026-09-18", ""}, {"2026-13-40T10:00", ""},
		{"2026-09-18T18:30", "abc"}, {"2026-09-18T18:30", "99999"},
	} {
		if got, err := parseCoordExpiry(tc.in, tc.offset, server); err == nil {
			t.Errorf("parseCoordExpiry(%q,%q)=%q, want error", tc.in, tc.offset, got)
		}
	}
}

func TestCoordSendRejectsInvalidExpiryAndStandingDoes(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/expiry")
	materializeWebRoom(t, st, room)
	for path, form := range map[string]url.Values{
		"/ui/coord/send":            {"room": {room}, "body": {"hi"}, "expires_at": {"not a date"}},
		"/ui/coord/standing/create": {"room": {room}, "body": {"rule"}, "confirm_scope": {"1"}, "expires_at": {"2026-09"}},
	} {
		res := authenticatedPostForm(t, client, srv.URL+path, form)
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "Invalid expiry") {
			t.Errorf("%s status=%d body=%q, want 400 with an expiry message", path, res.StatusCode, body)
		}
	}
	page := coordPageBody(t, client, srv.URL+"/ui/coord?room="+url.QueryEscape(room))
	if strings.Contains(page, "hi</") {
		t.Error("a rejected message must not be stored")
	}
}

func TestCoordExpiryFieldIsSharedAndHonestAboutTheZone(t *testing.T) {
	tpl := coordTemplate(t)
	if strings.Count(tpl, `{{template "coord-expiry-field"`) != 1 {
		t.Error("the composer carries the shared expiry field partial")
	}
	field := tpl[strings.Index(tpl, `{{define "coord-expiry-field"}}`):]
	field = field[:strings.Index(field, "{{end}}")]
	for _, want := range []string{`type="datetime-local"`, `name="expires_offset"`, `coord-zone-server`, `"coord.zone_server"`, `coord-zone-local`, `"coord.zone_local"`} {
		if !strings.Contains(field, want) {
			t.Errorf("expiry field partial missing %q", want)
		}
	}
	if strings.Contains(tpl, "(RFC3339)") {
		t.Error("no free-text RFC3339 field may remain")
	}
	requireCSS(t, ".coord-enhanced .coord-zone-server", "display: none;")
}

func TestCoordExpiryOffsetStampingRunsInNode(t *testing.T) {
	source := string(mustReadEmbedded(t, "static/app.js"))
	start := strings.Index(source, "  const progressiveFormPaths =")
	end := strings.Index(source, "  const liveStatus =")
	if start < 0 || end <= start || !strings.Contains(source[start:end], "coordExpiryOffset") {
		t.Fatal("expiry offset helper must live in the independently testable block")
	}
	program := source[start:end] + `
if (coordExpiryOffset("2026-07-01T18:30") !== "120") throw new Error("summer offset " + coordExpiryOffset("2026-07-01T18:30"));
if (coordExpiryOffset("2026-01-15T18:30") !== "60") throw new Error("winter offset " + coordExpiryOffset("2026-01-15T18:30"));
if (coordExpiryOffset("") !== "") throw new Error("empty value needs no offset");
if (coordExpiryOffset("garbage") !== "") throw new Error("invalid value needs no offset");
const field = {value:"2026-07-01T18:30"}, hidden = {value:""};
const form = {querySelector: (s) => s.includes("expires_at") ? field : s.includes("expires_offset") ? hidden : null};
stampCoordExpiryOffset(form);
if (hidden.value !== "120") throw new Error("hidden offset not stamped: " + hidden.value);
`
	command := exec.Command("node", "-e", program)
	command.Env = append(os.Environ(), "TZ=Europe/Berlin")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("expiry offset stamping failed: %v\n%s", err, output)
	}
}

func TestCoordRoutineDeliveryStateIsNotShown(t *testing.T) {
	build := func(d store.CoordDeliverySummary) string {
		got := buildCoordMessageViews([]store.CoordMessagePresentation{{
			Message: store.CoordMessage{ID: 1, Sequence: 1, AuthorKind: store.AuthorAgent, Body: "x"}, Delivery: d,
		}}, "project:p", nil)
		return got[0].Delivery
	}
	for name, tc := range map[string]struct {
		in   store.CoordDeliverySummary
		want string
	}{
		"stored only is routine":  {store.CoordDeliverySummary{Stored: 1}, ""},
		"nothing":                 {store.CoordDeliverySummary{}, ""},
		"acked is progress":       {store.CoordDeliverySummary{Acked: 1}, "1 acknowledged"},
		"lagging recipient shown": {store.CoordDeliverySummary{Acked: 1, Stored: 1}, "1 acknowledged · 1 stored"},
		"fetched is progress":     {store.CoordDeliverySummary{Fetched: 2}, "2 fetched"},
	} {
		if got := build(tc.in); got != tc.want {
			t.Errorf("%s: delivery=%q want %q", name, got, tc.want)
		}
	}
}

func TestMarkViewerMentions(t *testing.T) {
	presentations := []store.CoordMessagePresentation{
		{Message: store.CoordMessage{ID: 1}, Mentions: []string{"person:1", "agent-a"}},
		{Message: store.CoordMessage{ID: 2}, Mentions: []string{"agent-a"}},
		{Message: store.CoordMessage{ID: 3}},
	}
	views := make([]coordMessageView, 3)
	markViewerMentions(views, presentations, "person:1")
	if !views[0].MentionsViewer || views[1].MentionsViewer || views[2].MentionsViewer {
		t.Fatalf("mention marks=%+v", views)
	}
	markViewerMentions(views, presentations, "")
	if !views[0].MentionsViewer {
		t.Fatal("empty viewer must not clear or crash")
	}
}
