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

func TestCoordLinksUseDesignedColorAndUnderlineOnlyOnHoverAndFocus(t *testing.T) {
	requireCSS(t, ".coord-workspace", "--coord-link:", "--coord-link-hover:")
	requireCSS(t, ":where(.coord-conversation, .coord-context) a", "color: var(--coord-link);", "text-decoration: none;")
	requireCSS(t, ":where(.coord-conversation, .coord-context) a:hover, :where(.coord-conversation, .coord-context) a:focus-visible", "text-decoration: underline;")
	requireCSS(t, ".coord-shell :focus-visible", "outline: 3px solid")
	requireCSS(t, ".coord-threads li a", "color: var(--coord-ink);")
	requireCSS(t, ".coord-threads li a:hover", "text-decoration: underline;")
}

func TestCoordReadActionsAreQuietTextActionsWithRealForms(t *testing.T) {
	tpl := coordTemplate(t)
	start := strings.Index(tpl, `data-coord-read-actions`)
	if start < 0 {
		t.Fatal("read actions surface missing")
	}
	end := strings.Index(tpl[start:], `</details>`)
	if end < 0 {
		t.Fatal("read actions must sit in a native disclosure")
	}
	block := tpl[start : start+end]
	for _, want := range []string{`action="/ui/coord/read"`, `action="/ui/coord/unread"`, `class="coord-text-action"`, `Bis hier gelesen`, `Ab hier ungelesen`} {
		if !strings.Contains(block, want) {
			t.Errorf("read actions missing %q", want)
		}
	}
	if strings.Contains(block, "clay-btn") {
		t.Error("read actions must not render as default buttons")
	}
	if start > strings.Index(tpl, `<ol class="coord-messages">`) {
		t.Error("read actions belong to the feed header, not between feed and composer")
	}
	requireCSS(t, ".coord-text-action", "border: 0;", "background: transparent;", "min-height: 2.75rem;")
}

func TestCoordComposerOptionsAreAScrollingLabelledGrid(t *testing.T) {
	tpl := coordTemplate(t)
	start := strings.Index(tpl, `<details class="coord-compose-more coord-compose-options">`)
	if start < 0 {
		t.Fatal("room composer options missing")
	}
	block := tpl[start : start+strings.Index(tpl[start:], `</details>`)]
	for _, want := range []string{`class="coord-option-grid"`, `class="coord-option-field"`, `{{template "coord-expiry-field"`, `class="coord-mention-row"`} {
		if !strings.Contains(block, want) {
			t.Errorf("composer options missing %q", want)
		}
	}
	if strings.Contains(block, `placeholder="2026-09-18T18:30:00Z"`) {
		t.Error("expiry no longer asks for a raw RFC3339 string")
	}
	requireCSS(t, ".coord-option-grid", "display: grid;")
	requireCSS(t, ".coord-option-field", "display: grid;")
	requireCSS(t, ".coord-compose-options[open] .coord-option-panel", "max-height:", "overflow-y: auto;")
	requireCSS(t, ".coord-mention-row", "min-height:")
	for _, body := range cssBodies(t, ".coord-mention-row") {
		if strings.Contains(body, "border-radius") || strings.Contains(body, "border:") {
			t.Errorf("mention rows are plain rows, not pills: %s", body)
		}
	}
}

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
		if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "Ablaufdatum") {
			t.Errorf("%s status=%d body=%q, want 400 with a German expiry message", path, res.StatusCode, body)
		}
	}
	page := coordPageBody(t, client, srv.URL+"/ui/coord?room="+url.QueryEscape(room))
	if strings.Contains(page, "hi</") {
		t.Error("a rejected message must not be stored")
	}
}

func TestCoordExpiryFieldIsSharedAndHonestAboutTheZone(t *testing.T) {
	tpl := coordTemplate(t)
	if strings.Count(tpl, `{{template "coord-expiry-field"`) != 2 {
		t.Error("composer and standing form must share the expiry field partial")
	}
	field := tpl[strings.Index(tpl, `{{define "coord-expiry-field"}}`):]
	field = field[:strings.Index(field, "{{end}}")]
	for _, want := range []string{`type="datetime-local"`, `name="expires_offset"`, `coord-zone-server`, "Serverzeit", `coord-zone-local`, "deiner Ortszeit"} {
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

func TestCoordThreadPanelGivesMessagesTheSpaceAndKeepsMetaOnOneLine(t *testing.T) {
	tpl := coordTemplate(t)
	start := strings.Index(tpl, `id="coord-thread"`)
	end := strings.Index(tpl, `coord-context-default`)
	panel := tpl[start:end]
	stateAt := strings.Index(panel, `action="/ui/coord/thread/state"`)
	msgsAt := strings.Index(panel, `<ol class="coord-thread-messages">`)
	if stateAt < 0 || stateAt > msgsAt {
		t.Error("status control belongs into the thread header, before the message list")
	}
	if !strings.Contains(panel, `<details class="coord-thread-state">`) {
		t.Error("status control should be a disclosure in the header")
	}
	if !strings.Contains(panel, `<textarea id="coord-thread-body" name="body" rows="2" required placeholder="Im Thread antworten">`) {
		t.Error("thread reply field should be compact with a placeholder instead of a label row")
	}
	if !strings.Contains(panel, `class="coord-message-seq"`) {
		t.Error("sequence and kind meta must be separable")
	}
	requireCSS(t, ".coord-context-thread", "grid-template-rows: auto auto minmax(0, 1fr) auto;")
	requireCSS(t, ".coord-thread-composer textarea:focus", "min-height:")
	requireCSS(t, ".coord-message article > header strong", "text-overflow: ellipsis;", "white-space: nowrap;")
	requireCSS(t, ".coord-thread-messages .coord-message-seq", "display: none;")
	requireCSS(t, ".coord-workspace select", "appearance: none;", "background-image:")
	requireCSS(t, ".coord-thread-state", "position: relative;")
	mobile := string(mustReadEmbedded(t, "static/app.css"))
	mobile = mobile[strings.Index(mobile, "@media (max-width: 700px)"):]
	if !strings.Contains(coordCSSRule(t, mobile, ".coord-message-seq"), "display: none;") {
		t.Error("mobile hides sequence and kind")
	}
}

func TestCoordLiveStatusSitsInLayoutFlowAndOnlyTheMobileDotFloats(t *testing.T) {
	requireCSS(t, ".coord-live-status", "grid-row: 2;")
	for _, body := range cssBodies(t, ".coord-live-status") {
		if strings.Contains(body, "position: fixed") {
			t.Errorf("desktop live status must not overlay content: %s", body)
		}
	}
	requireCSS(t, ".coord-live-status:not(.coord-nojs-status)",
		"position: fixed;", "right: max(.7rem, env(safe-area-inset-right))",
		"bottom: max(.7rem, env(safe-area-inset-bottom))", "pointer-events: none")
}

func TestCoordDrawerCloseKeepsA44pxTarget(t *testing.T) {
	requireCSS(t, ".coord-drawer-close", "min-width: 2.75rem;", "min-height: 2.75rem;")
}

func TestCoordStandingDisclosureHasNoActiveBar(t *testing.T) {
	for _, body := range cssBodies(t, ".coord-standing-create") {
		if strings.Contains(body, "border-left") {
			t.Errorf("a closed disclosure must not look like an active standing rule: %s", body)
		}
	}
	requireCSS(t, ".coord-standing", "border-left: 2px solid var(--coord-signal);")
}

func TestCoordMentionedMessagesAreMarkedAndKeepToolsVisible(t *testing.T) {
	tpl := coordTemplate(t)
	if strings.Count(tpl, `coord-message-mentioned`) < 1 || !strings.Contains(tpl, `erwähnt dich`) {
		t.Error("template must mark messages that mention the viewer")
	}
	requireCSS(t, ".coord-message-mentioned > article", "border-left: 2px solid var(--coord-signal);")
	requireCSS(t, ".coord-message-mentioned .coord-message-tools", "opacity: 1;", "pointer-events: auto;")
	requireCSS(t, ".coord-mention-you", "color: var(--coord-signal-dark);")
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
		"acked is progress":       {store.CoordDeliverySummary{Acked: 1}, "1 bestätigt"},
		"lagging recipient shown": {store.CoordDeliverySummary{Acked: 1, Stored: 1}, "1 bestätigt · 1 gespeichert"},
		"fetched is progress":     {store.CoordDeliverySummary{Fetched: 2}, "2 abgeholt"},
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
