package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// ---------------------------------------------------------------- Helfer

func jl(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func uLine(text string) string {
	return jl(map[string]any{"type": "user", "timestamp": "2026-10-01T10:00:00Z", "message": map[string]any{"role": "user", "content": text}})
}

func aLine(blocks ...map[string]any) string {
	return jl(map[string]any{"type": "assistant", "timestamp": "2026-10-01T10:00:01Z", "message": map[string]any{"role": "assistant", "content": blocks}})
}

func tb(s string) map[string]any    { return map[string]any{"type": "text", "text": s} }
func think(s string) map[string]any { return map[string]any{"type": "thinking", "thinking": s} }
func bash(id, cmd string) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": cmd}}
}
func edit(id, path, old, new string) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": "Edit", "input": map[string]any{"file_path": path, "old_string": old, "new_string": new}}
}
func rLine(id, out string, isErr bool) string {
	block := map[string]any{"type": "tool_result", "tool_use_id": id, "content": out}
	if isErr {
		block["is_error"] = true
	}
	return jl(map[string]any{"type": "user", "timestamp": "2026-10-01T10:00:02Z", "message": map[string]any{"role": "user", "content": []map[string]any{block}}})
}

func chunks(lines ...string) []store.Chunk {
	out := make([]store.Chunk, len(lines))
	for i, l := range lines {
		out[i] = store.Chunk{Seq: i, Raw: l}
	}
	return out
}

// work builds a short realistic session about word.
func work(word string) []store.Chunk {
	var long []string
	for i := 1; i <= 30; i++ {
		long = append(long, fmt.Sprintf("line %d of the listing mentioning %s", i, word))
	}
	return chunks(
		uLine("Please look at "+word+" in the login handler"),
		aLine(think("The handler compares origins, "+word+" is suspicious")),
		aLine(tb("I will search the code for it."), bash("t1", "grep -rn "+word+" internal/")),
		rLine("t1", strings.Join(long, "\n"), false),
		aLine(edit("t2", "internal/web/login.go", "if origin != want {", "if origin != want && origin != \"null\" {")),
		rLine("t2", "updated", false),
		aLine(bash("t3", "go test ./...")),
		rLine("t3", "FAIL internal/web\nexit status 1", true),
		aLine(tb("The test fails, I will look into it.")),
		jl(map[string]any{"type": "ai-title", "aiTitle": "Fix the " + word + " login"}),
	)
}

type sessEnv struct {
	shellEnv
	srv *httptest.Server
	pid map[string]string // Name -> öffentliche Adresse
	id  map[string]int64
}

// seedSessions legt je Rolle Sessions im Projekt an. alice=1 Owner, anna=2
// Member, gina=3 Guest, lars=4 Lead, rita=5 Member.
func seedSessions(t *testing.T) sessEnv {
	t.Helper()
	env := shellWebAll(t)
	srv := &httptest.Server{URL: env.Base}
	e := sessEnv{shellEnv: env, srv: srv, pid: map[string]string{}, id: map[string]int64{}}
	st := env.St
	st.SetAccessMode(store.AccessMode{Enforce: true})
	add := func(name string, account int64, project, machine, visibility, word string, harness string) {
		id, err := st.UpsertSession(store.Session{Harness: harness, ExternalID: "ext-" + name, AccountID: account,
			Scope: scope.Axes{Project: project, Machine: machine, Branch: "feat/" + name}})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.AppendChunks(id, work(word)); err != nil {
			t.Fatal(err)
		}
		sess, _ := st.SessionByID(id)
		if visibility != store.VisPrivate {
			owner := fmt.Sprintf("person:%d", account)
			if err := st.SetSessionVisibility(id, st.Access(store.Principal{ID: owner, Label: name}), visibility); err != nil {
				t.Fatal(err)
			}
		}
		e.pid[name], e.id[name] = sess.PublicID, id
	}
	add("alice-private", 1, shellProject, "mainex", store.VisPrivate, "zebrafish", "claude-code")
	add("anna-private", 2, shellProject, "laptop", store.VisPrivate, "zebrafish", "claude-code")
	add("anna-project", 2, shellProject, "laptop", store.VisProject, "zebrafish", "claude-code")
	add("anna-guests", 2, shellProject, "laptop", store.VisGuests, "zebrafish", "claude-code")
	add("hidden-project", 1, shellHiddenProject, "mainex", store.VisGuests, "zebrafish", "claude-code")
	return e
}

func (e sessEnv) get(t *testing.T, c *http.Client, path string) (int, string) {
	t.Helper()
	return fetchPage(t, c, e.Base+path)
}

// ------------------------------------------------------------------ Liste

func TestSessionListShowsTitlesWithRandomAddressesToTheOwner(t *testing.T) {
	e := seedSessions(t)
	code, page := e.get(t, e.Owner, "/ui/sessions")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	for _, name := range []string{"alice-private", "anna-private", "anna-project", "anna-guests", "hidden-project"} {
		if !strings.Contains(page, `href="/ui/sessions/`+e.pid[name]+`"`) {
			t.Errorf("owner list lacks %s", name)
		}
	}
	if !strings.Contains(page, "Fix the zebrafish login") {
		t.Error("title missing")
	}
	if regexp.MustCompile(`/ui/sessions/\d+["?#]`).MatchString(page) {
		t.Error("a numeric session address is rendered")
	}
}

func TestSessionListShowsMembersPrivateRowsWithoutLinkOrTitle(t *testing.T) {
	e := seedSessions(t)
	_, page := e.get(t, e.Member, "/ui/sessions")
	for _, name := range []string{"anna-private", "anna-project", "anna-guests"} {
		if !strings.Contains(page, `href="/ui/sessions/`+e.pid[name]+`"`) {
			t.Errorf("member lacks own/shared %s", name)
		}
	}
	if strings.Contains(page, e.pid["alice-private"]) {
		t.Error("the foreign private session's address is rendered")
	}
	if n := strings.Count(page, `class="srow is-private"`); n != 1 {
		t.Errorf("%d private rows, want 1", n)
	}
	// Das Hidden-Projekt hat der Member nicht.
	if strings.Contains(page, e.pid["hidden-project"]) || strings.Contains(page, "feat/hidden-project") {
		t.Error("session of a project without a role is listed")
	}
}

func TestSessionListForAGuestHasOnlyGuestSharedSessionsAndNoHostDetails(t *testing.T) {
	e := seedSessions(t)
	code, page := e.get(t, e.Guest, "/ui/sessions")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if !strings.Contains(page, `href="/ui/sessions/`+e.pid["anna-guests"]+`"`) {
		t.Error("guest-shared session missing")
	}
	for _, name := range []string{"alice-private", "anna-private", "anna-project", "hidden-project"} {
		if strings.Contains(page, e.pid[name]) {
			t.Errorf("guest sees %s", name)
		}
	}
	for _, leak := range []string{"laptop", "mainex", "feat/anna", "anna", "Everyone", "Mine", "All machines", "ext-"} {
		if strings.Contains(page, leak) {
			t.Errorf("guest page leaks %q", leak)
		}
	}
	if !strings.Contains(page, "Claude Code") {
		t.Error("guest should see the platform name")
	}
}

func TestSessionEmptyStatesAreNeutral(t *testing.T) {
	env := shellWebAll(t)
	_, owner := fetchPage(t, env.Owner, env.Base+"/ui/sessions")
	_, guest := fetchPage(t, env.Guest, env.Base+"/ui/sessions")
	if !strings.Contains(owner, "No sessions yet.") || !strings.Contains(owner, "Connect an agent") {
		t.Errorf("empty owner page: %s", owner)
	}
	if !strings.Contains(guest, "Nothing here yet.") || strings.Contains(guest, "Connect an agent") {
		t.Error("empty guest page must be neutral without an action")
	}
	// Mit nur privaten fremden Sessions sieht ein Gast dasselbe wie bei einer leeren Instanz.
	e := seedSessions(t)
	e.St.SetAccessMode(store.AccessMode{Enforce: true})
	if err := e.St.SetSessionVisibility(e.id["anna-guests"], e.St.Access(store.Principal{ID: "person:2", Label: "anna"}), store.VisPrivate); err != nil {
		t.Fatal(err)
	}
	_, hidden := e.get(t, e.Guest, "/ui/sessions")
	if !strings.Contains(hidden, "Nothing here yet.") {
		t.Error("guest with only hidden sessions must see the neutral empty state")
	}
}

func TestSessionNavEntryIsThereForGuestsToo(t *testing.T) {
	env := shellWebAll(t)
	_, page := fetchPage(t, env.Guest, env.Base+"/ui/sessions")
	if !navKeys(page)["sessions"] {
		t.Error("guest has no Sessions entry")
	}
}

func TestSessionListFiltersAndFacetCounts(t *testing.T) {
	e := seedSessions(t)
	cx, err := e.St.UpsertSession(store.Session{Harness: "codex", ExternalID: "cx-list", AccountID: 2, Scope: scope.Axes{Project: shellProject, Machine: "laptop"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = e.St.AppendChunks(cx, chunks(uLine("a codex session about parsing")))
	cxSess, _ := e.St.SessionByID(cx)
	_, page := e.get(t, e.Owner, "/ui/sessions?agent=codex")
	if !strings.Contains(page, cxSess.PublicID) || strings.Contains(page, e.pid["anna-guests"]) {
		t.Error("agent filter does not narrow the list")
	}
	_, page = e.get(t, e.Owner, "/ui/sessions?owner=mine")
	if !strings.Contains(page, e.pid["alice-private"]) || strings.Contains(page, e.pid["anna-project"]) {
		t.Error("Mine filter does not narrow the list")
	}
	_, page = e.get(t, e.Owner, "/ui/sessions?machine=laptop")
	if strings.Contains(page, e.pid["alice-private"]) {
		t.Error("machine filter does not narrow the list")
	}
	if !strings.Contains(page, "laptop (4)") || !strings.Contains(page, "mainex (2)") {
		t.Errorf("facet counts missing or wrong: %s", regexp.MustCompile(`<select[^>]*name="machine".*?</select>`).FindString(page))
	}
}

func TestSessionListPagesWithAnOlderLink(t *testing.T) {
	e := seedSessions(t)
	for i := 0; i < 60; i++ {
		id, err := e.St.UpsertSession(store.Session{Harness: "claude-code", ExternalID: fmt.Sprintf("many-%d", i), AccountID: 1, Scope: scope.Axes{Project: shellProject}})
		if err != nil {
			t.Fatal(err)
		}
		_ = e.St.AppendChunks(id, chunks(uLine(fmt.Sprintf("bulk session number %d", i))))
	}
	_, page := e.get(t, e.Owner, "/ui/sessions")
	if strings.Count(page, `class="srow`) != 50 {
		t.Errorf("first page has %d rows", strings.Count(page, `class="srow`))
	}
	m := regexp.MustCompile(`href="(/ui/sessions\?[^"]*cursor=[^"]+)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no Show older link")
	}
	next := strings.ReplaceAll(m[1], "&amp;", "&")
	_, page2 := e.get(t, e.Owner, next)
	if strings.Count(page2, `class="srow`) != 15 || strings.Contains(page2, "cursor=") {
		t.Errorf("second page: rows=%d", strings.Count(page2, `class="srow`))
	}
}

// ----------------------------------------------------------------- Suche

func TestSessionSearchShowsKindedHitsWithMarkedSnippets(t *testing.T) {
	e := seedSessions(t)
	_, page := e.get(t, e.Owner, "/ui/sessions?q=zebrafish")
	for _, kind := range []string{"user", "command", "output"} {
		if !strings.Contains(page, `data-kind="`+kind+`"`) {
			t.Errorf("no %s hit", kind)
		}
	}
	if !strings.Contains(page, "<mark>zebrafish</mark>") {
		t.Error("match not marked")
	}
	if strings.Contains(page, `data-kind="thinking"`) {
		t.Error("thinking in Everything")
	}
	m := regexp.MustCompile(`(\d+) matches in (\d+) sessions`).FindStringSubmatch(page)
	if m == nil || m[2] != "5" {
		t.Errorf("found line = %v", m)
	}
	// Treffer springen in den Kontext.
	if !regexp.MustCompile(`href="/ui/sessions/` + e.pid["anna-project"] + `\?[^"]*at=\d+[^"]*q=zebrafish[^"]*#c\d+"`).MatchString(page) {
		t.Error("hit link lacks at, q and anchor")
	}
	_, th := e.get(t, e.Owner, "/ui/sessions?q=suspicious&kind=thinking")
	if !strings.Contains(th, `data-kind="thinking"`) {
		t.Error("Thinking filter finds no thinking block")
	}
	_, none := e.get(t, e.Owner, "/ui/sessions?q=suspicious")
	if strings.Contains(none, `data-kind=`) {
		t.Error("thinking text found without the Thinking filter")
	}
}

func TestSessionSearchOnlyUsesWhatTheViewerMayRead(t *testing.T) {
	e := seedSessions(t)
	for _, c := range []struct {
		name   string
		client *http.Client
		want   []string
		not    []string
	}{
		{"member", e.Member, []string{"anna-private", "anna-project", "anna-guests"}, []string{"alice-private", "hidden-project"}},
		{"guest", e.Guest, []string{"anna-guests"}, []string{"alice-private", "anna-private", "anna-project", "hidden-project"}},
		{"lead", e.Lead, []string{"alice-private", "anna-private", "anna-project", "anna-guests"}, []string{"hidden-project"}},
	} {
		_, page := e.get(t, c.client, "/ui/sessions?q=zebrafish")
		for _, w := range c.want {
			if !strings.Contains(page, `data-session="`+e.pid[w]+`"`) {
				t.Errorf("%s: %s missing from the hits", c.name, w)
			}
		}
		for _, n := range c.not {
			if strings.Contains(page, e.pid[n]) {
				t.Errorf("%s: %s present", c.name, n)
			}
		}
		m := regexp.MustCompile(`(\d+) matches in (\d+) sessions?`).FindStringSubmatch(page)
		if m == nil || m[2] != fmt.Sprint(len(c.want)) {
			t.Errorf("%s: found line %v, want %d sessions", c.name, m, len(c.want))
		}
	}
	// Ein Wort, das nur im verborgenen Transkript vorkommt: kein Treffer, kein Hinweis.
	id := e.id["alice-private"]
	if err := e.St.AppendChunks(id, []store.Chunk{{Seq: 99, Raw: uLine("the quokkaword is only here")}}); err != nil {
		t.Fatal(err)
	}
	_, page := e.get(t, e.Member, "/ui/sessions?q=quokkaword")
	if strings.Contains(page, "quokkaword is") || !strings.Contains(page, "No results for") {
		t.Errorf("hidden content leaks: %s", page)
	}
	_, owner := e.get(t, e.Owner, "/ui/sessions?q=quokkaword")
	if !strings.Contains(owner, "<mark>quokkaword</mark>") {
		t.Error("owner should find it")
	}
}

func TestSessionSearchWithoutResultsOffersToClear(t *testing.T) {
	e := seedSessions(t)
	_, page := e.get(t, e.Owner, "/ui/sessions?q=kafkaabsent")
	if !strings.Contains(page, "No results for “kafkaabsent”.") || !strings.Contains(page, "Clear search") {
		t.Errorf("no-result state: %s", page)
	}
}

// ---------------------------------------------------------------- Detail

func TestSessionDetailRendersBlocksReadably(t *testing.T) {
	e := seedSessions(t)
	code, page := e.get(t, e.Owner, "/ui/sessions/"+e.pid["anna-project"])
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	for _, want := range []string{
		"Fix the zebrafish login",                       // Titel
		`class="blk blk-user"`,                          // Nutzerprompt als Block
		"Please look at zebrafish in the login handler", // Text
		`<details class="think"`,                        // Denkblock eingeklappt
		`<details class="tool`,                          // Werkzeugaufruf eingeklappt
		"Show all 30 lines",                             // Ergebnis gekürzt
		`class="diff"`,                                  // Diff
		`class="dl dl-del"`,
		`class="dl dl-add"`,
		`class="tr is-bad">error`, // Fehler fällt im Ergebnis auf
		`class="tool is-failed"`,
		"Prompts",
		"3 tool calls",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("detail lacks %q", want)
		}
	}
	if strings.Contains(page, `<details class="think" open`) || strings.Contains(page, `<details class="tool" open`) {
		t.Error("blocks are open in the resting state")
	}
	// Von den 30 Zeilen stehen 12 offen, der Rest hinter dem Schalter.
	if strings.Count(page, `class="ol"`) < 12 {
		t.Error("first lines of the output are missing")
	}
}

func TestSessionDetailEscapesTranscriptText(t *testing.T) {
	e := seedSessions(t)
	id := e.id["alice-private"]
	_ = e.St.AppendChunks(id, chunks()) // no-op
	if err := e.St.AppendChunks(id, []store.Chunk{
		{Seq: 50, Raw: uLine(`<script>alert(1)</script> & "quotes" zebrafish`)},
		{Seq: 51, Raw: aLine(bash("tx", `echo '<img src=x onerror=alert(2)>'`))},
		{Seq: 52, Raw: rLine("tx", `<b>bold</b>`, false)},
	}); err != nil {
		t.Fatal(err)
	}
	_, page := e.get(t, e.Owner, "/ui/sessions/"+e.pid["alice-private"]+"?q=zebrafish")
	for _, bad := range []string{"<script>alert(1)", "<img src=x", "<b>bold</b>"} {
		if strings.Contains(page, bad) {
			t.Errorf("unescaped %q", bad)
		}
	}
	if !strings.Contains(page, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Error("script text not shown escaped")
	}
}

func TestSessionDetailJumpsToAHitAndOpensIt(t *testing.T) {
	e := seedSessions(t)
	_, page := e.get(t, e.Owner, "/ui/sessions/"+e.pid["anna-project"]+"?at=3&q=listing&back=1")
	if !strings.Contains(page, `id="c3"`) {
		t.Fatalf("anchor for the hit's chunk missing")
	}
	re := regexp.MustCompile(`<details class="tool[^"]*is-target[^"]*"[^>]*>`)
	if m := re.FindString(page); !strings.Contains(m, "open") {
		t.Errorf("target is not open and marked: %q", m)
	}
	if !strings.Contains(page, "Back to results for “listing”") {
		t.Error("back link missing")
	}
	if !strings.Contains(page, "<mark") {
		t.Error("no highlight in the target")
	}
}

func TestSessionFindShowsPositionAndLinksToNeighbours(t *testing.T) {
	e := seedSessions(t)
	base := "/ui/sessions/" + e.pid["anna-project"]
	_, page := e.get(t, e.Owner, base+"?q=zebrafish&at=3")
	if !regexp.MustCompile(`class="find-pos">\s*\d+ / \d+`).MatchString(page) {
		t.Fatalf("no position: %s", regexp.MustCompile(`find[^<]*<[^>]*>[^<]*`).FindString(page))
	}
	if !strings.Contains(page, `rel="next"`) || !strings.Contains(page, `rel="prev"`) {
		t.Error("no prev/next links")
	}
	// Trefferzahl je Prompt in der Liste links.
	if !strings.Contains(page, `class="pr-hits"`) {
		t.Error("prompt list lacks hit counts")
	}
}

func TestSessionLongTranscriptIsShownInWindows(t *testing.T) {
	e := seedSessions(t)
	id, _ := e.St.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "long", AccountID: 1, Scope: scope.Axes{Project: shellProject}})
	var batch []store.Chunk
	for i := 0; i < 700; i++ {
		batch = append(batch, store.Chunk{Seq: i, Raw: uLine(fmt.Sprintf("prompt number %d about giraffes", i))})
	}
	if err := e.St.AppendChunks(id, batch); err != nil {
		t.Fatal(err)
	}
	sess, _ := e.St.SessionByID(id)
	_, tail := e.get(t, e.Owner, "/ui/sessions/"+sess.PublicID)
	if !strings.Contains(tail, "prompt number 699 about") || strings.Contains(tail, "prompt number 100 about") {
		t.Error("default window is not the latest 200")
	}
	if !strings.Contains(tail, "Show 200 earlier") || strings.Contains(tail, "Show 200 later") {
		t.Error("window links wrong at the tail")
	}
	if !strings.Contains(tail, "Show all 700 prompts") {
		t.Error("prompt list not shortened")
	}
	_, around := e.get(t, e.Owner, "/ui/sessions/"+sess.PublicID+"?at=300")
	if !strings.Contains(around, "prompt number 300 about") || !strings.Contains(around, "prompt number 250 about") || strings.Contains(around, "prompt number 600 about") {
		t.Error("around window wrong")
	}
	if !strings.Contains(around, "Show 200 earlier") || !strings.Contains(around, "Show 200 later") {
		t.Error("window links missing in the middle")
	}
}

func TestSessionCodexTranscriptRenders(t *testing.T) {
	e := seedSessions(t)
	id, _ := e.St.UpsertSession(store.Session{Harness: "codex", ExternalID: "cx", AccountID: 1, Scope: scope.Axes{Project: shellProject}})
	line := func(typ string, payload map[string]any) string {
		return jl(map[string]any{"timestamp": "2026-10-01T11:00:00Z", "type": typ, "payload": payload})
	}
	if err := e.St.AppendChunks(id, chunks(
		line("event_msg", map[string]any{"type": "user_message", "message": "why does the kafka consumer lag"}),
		line("response_item", map[string]any{"type": "reasoning", "summary": []map[string]any{{"type": "summary_text", "text": "looking at offsets"}}}),
		line("response_item", map[string]any{"type": "function_call", "name": "exec_command", "arguments": `{"cmd":"kafka-consumer-groups --describe"}`, "call_id": "c1"}),
		line("response_item", map[string]any{"type": "function_call_output", "call_id": "c1", "output": "Process exited with code 1\nOutput:\nboom"}),
		line("response_item", map[string]any{"type": "custom_tool_call", "name": "apply_patch", "call_id": "c2", "input": "*** Begin Patch\n*** Update File: a.go\n@@\n-old\n+new\n*** End Patch\n"}),
		line("response_item", map[string]any{"type": "custom_tool_call_output", "call_id": "c2", "output": "Exit code: 0\nOutput:\nSuccess"}),
		line("response_item", map[string]any{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "commit offsets on close"}}}),
	)); err != nil {
		t.Fatal(err)
	}
	sess, _ := e.St.SessionByID(id)
	code, page := e.get(t, e.Owner, "/ui/sessions/"+sess.PublicID)
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	for _, want := range []string{"why does the kafka consumer lag", "looking at offsets", "exec_command", "kafka-consumer-groups --describe", "exit 1", "apply_patch", `class="dl dl-add"`, "commit offsets on close", "Codex"} {
		if !strings.Contains(page, want) {
			t.Errorf("codex page lacks %q", want)
		}
	}
}

func TestSessionDetailDegradesToARetryLineWhenTheTranscriptCannotBeLoaded(t *testing.T) {
	e := seedSessions(t)
	if _, err := e.St.DB().Exec(`ALTER TABLE session_chunks RENAME TO session_chunks_gone`); err != nil {
		t.Skipf("cannot break the table: %v", err)
	}
	code, page := e.get(t, e.Owner, "/ui/sessions/"+e.pid["anna-project"])
	if code != 200 || !strings.Contains(page, "Retry") || !strings.Contains(page, "Fix the zebrafish login") {
		t.Errorf("status %d, error state: %.300s", code, page)
	}
}

// -------------------------------------------------- Zugriff und Adressen

func TestSessionDetailAccessPerRole(t *testing.T) {
	e := seedSessions(t)
	for _, c := range []struct {
		who    string
		client *http.Client
		name   string
		want   int
	}{
		{"owner", e.Owner, "anna-private", 200},
		{"lead", e.Lead, "anna-private", 200},
		{"member own", e.Member, "anna-private", 200},
		{"member foreign private", e.Member, "alice-private", 404},
		{"member shared", e.Reviewer, "anna-project", 200},
		{"member foreign private 2", e.Reviewer, "anna-private", 404},
		{"guest project-level", e.Guest, "anna-project", 404},
		{"guest guest-level", e.Guest, "anna-guests", 200},
		{"guest private", e.Guest, "alice-private", 404},
		{"member no role", e.Member, "hidden-project", 404},
	} {
		code, _ := e.get(t, c.client, "/ui/sessions/"+e.pid[c.name])
		if code != c.want {
			t.Errorf("%s: %s -> %d, want %d", c.who, c.name, code, c.want)
		}
	}
}

func TestSessionMissingPageIsTheSameForAbsentAndHidden(t *testing.T) {
	e := seedSessions(t)
	codeA, absent := e.get(t, e.Member, "/ui/sessions/zzzzzzzzzzzz")
	codeB, hidden := e.get(t, e.Member, "/ui/sessions/"+e.pid["alice-private"])
	if codeA != 404 || codeB != 404 {
		t.Fatalf("codes %d %d", codeA, codeB)
	}
	if !strings.Contains(absent, "Session not found.") || !strings.Contains(absent, "All sessions") {
		t.Error("missing page text")
	}
	strip := func(s string) string {
		return regexp.MustCompile(`name="csrf_token" value="[^"]*"`).ReplaceAllString(s, "")
	}
	if strip(absent) != strip(hidden) {
		t.Error("hidden and absent sessions answer differently")
	}
}

func TestLegacyNumericAddressRedirectsOnlyForReaders(t *testing.T) {
	e := seedSessions(t)
	num := func(name string) string { return fmt.Sprintf("/ui/sessions/%d", e.id[name]) }
	resp, err := e.Member.Get(e.Base + num("anna-private") + "?q=zebrafish")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/ui/sessions/"+e.pid["anna-private"]) {
		t.Errorf("owner of the session: %d -> %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if !strings.Contains(resp.Header.Get("Location"), "q=zebrafish") {
		t.Error("query lost in the redirect")
	}
	// Nicht lesbar und nicht vorhanden sind nicht zu unterscheiden.
	codeHidden, hidden := e.get(t, e.Member, num("alice-private"))
	codeAbsent, absent := e.get(t, e.Member, "/ui/sessions/999999")
	if codeHidden != 404 || codeAbsent != 404 {
		t.Errorf("hidden %d absent %d", codeHidden, codeAbsent)
	}
	strip := func(s string) string {
		return regexp.MustCompile(`name="csrf_token" value="[^"]*"`).ReplaceAllString(s, "")
	}
	if strip(hidden) != strip(absent) {
		t.Error("numeric hidden and absent differ")
	}
	if code, _ := e.get(t, e.Guest, num("anna-project")); code != 404 {
		t.Errorf("guest follows a numeric address to a members-only session: %d", code)
	}
}

// ---------------------------------------------------------------- Teilen

func postShare(t *testing.T, e sessEnv, c *http.Client, name, level string) *http.Response {
	t.Helper()
	token := renderedCSRFToken(t, c, e.Base+"/ui/sessions")
	form := url.Values{"csrf_token": {token}, "level": {level}}
	resp := sameOriginPostForm(t, c, e.Base+"/ui/sessions/"+e.pid[name]+"/share", form)
	return resp
}

func TestShareSwitchIsForTheOwnerAndTheProjectOwnerOnly(t *testing.T) {
	e := seedSessions(t)
	_, own := e.get(t, e.Member, "/ui/sessions/"+e.pid["anna-private"])
	if !strings.Contains(own, `action="/ui/sessions/`+e.pid["anna-private"]+`/share"`) {
		t.Error("owner of the session has no switch")
	}
	_, shared := e.get(t, e.Reviewer, "/ui/sessions/"+e.pid["anna-project"])
	if strings.Contains(shared, "/share") {
		t.Error("a foreign member sees the switch")
	}
	_, lead := e.get(t, e.Lead, "/ui/sessions/"+e.pid["anna-project"])
	if strings.Contains(lead, "/share") {
		t.Error("a lead sees the switch on a foreign session")
	}
	_, owner := e.get(t, e.Owner, "/ui/sessions/"+e.pid["anna-project"])
	if !strings.Contains(owner, "/share") {
		t.Error("the project owner has no switch")
	}
	_, guest := e.get(t, e.Guest, "/ui/sessions/"+e.pid["anna-guests"])
	if strings.Contains(guest, "/share") {
		t.Error("guest sees the switch")
	}
}

func TestSharePostChangesTheLevelWithAuditAndRejectsEveryoneElse(t *testing.T) {
	e := seedSessions(t)
	resp := postShare(t, e, e.Member, "anna-private", store.VisGuests)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/sessions/"+e.pid["anna-private"] {
		t.Fatalf("owner share: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if sess, _ := e.St.SessionByID(e.id["anna-private"]); sess.Visibility != store.VisGuests {
		t.Errorf("level = %q", sess.Visibility)
	}
	if code, _ := e.get(t, e.Guest, "/ui/sessions/"+e.pid["anna-private"]); code != 200 {
		t.Errorf("guest cannot read the freshly shared session: %d", code)
	}
	var events int
	_ = e.St.DB().QueryRow(`SELECT count(*) FROM session_share_events WHERE session_id=? AND new_level=?`, e.id["anna-private"], store.VisGuests).Scan(&events)
	if events != 1 {
		t.Errorf("audit events = %d", events)
	}
	// Fremdes Mitglied, Lead, Gast: nichts ändert sich.
	for _, c := range []struct {
		who    string
		client *http.Client
	}{{"member", e.Reviewer}, {"lead", e.Lead}, {"guest", e.Guest}} {
		r := postShare(t, e, c.client, "anna-project", store.VisPrivate)
		if r.StatusCode == http.StatusSeeOther {
			t.Errorf("%s changed a foreign session", c.who)
		}
		if sess, _ := e.St.SessionByID(e.id["anna-project"]); sess.Visibility != store.VisProject {
			t.Errorf("%s: level became %q", c.who, sess.Visibility)
		}
	}
	// Der Projekt-Owner darf.
	if r := postShare(t, e, e.Owner, "anna-project", store.VisPrivate); r.StatusCode != http.StatusSeeOther {
		t.Errorf("project owner: %d", r.StatusCode)
	}
	if r := postShare(t, e, e.Owner, "anna-project", "everyone"); r.StatusCode == http.StatusSeeOther {
		t.Error("unknown level accepted")
	}
}

func TestSharePostNeedsCSRFAndAnInteractiveSession(t *testing.T) {
	e := seedSessions(t)
	resp := sameOriginPostForm(t, e.Member, e.Base+"/ui/sessions/"+e.pid["anna-private"]+"/share", url.Values{"level": {store.VisProject}})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("without a CSRF token: %d", resp.StatusCode)
	}
	if sess, _ := e.St.SessionByID(e.id["anna-private"]); sess.Visibility != store.VisPrivate {
		t.Error("changed without CSRF")
	}
	token, _, err := e.St.CreateToken("anna", store.TokenSpec{Label: "t"})
	if err != nil {
		t.Skipf("no token: %v", err)
	}
	c := login(t, e.srv, token)
	form := url.Values{"csrf_token": {renderedCSRFToken(t, c, e.Base+"/ui/sessions")}, "level": {store.VisProject}}
	r := sameOriginPostForm(t, c, e.Base+"/ui/sessions/"+e.pid["anna-private"]+"/share", form)
	if r.StatusCode == http.StatusSeeOther {
		t.Error("a pasted token (read-only session) changed the level")
	}
}

// ------------------------------------------------- Nachindizieren sichtbar

func TestIndexingProgressShowsForMembersNotForGuests(t *testing.T) {
	e := seedSessions(t)
	db := e.St.DB()
	if _, err := db.Exec(`UPDATE index_state SET val=0 WHERE key='backfill_done'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE index_state SET val=100 WHERE key='backfill_bound'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE index_state SET val=40 WHERE key='backfill_cursor'`); err != nil {
		t.Fatal(err)
	}
	_, member := e.get(t, e.Member, "/ui/sessions")
	if !strings.Contains(member, "Indexing older sessions") || !strings.Contains(member, "40 %") {
		t.Errorf("no progress line: %.200s", member)
	}
	_, guest := e.get(t, e.Guest, "/ui/sessions")
	if strings.Contains(guest, "Indexing") {
		t.Error("guest sees the indexing state")
	}
}

// Pitfall #2447: nothing the guest or member can observe may depend on rows
// they may not read. Adding hidden sessions must leave their pages unchanged.
func TestSessionPagesDoNotChangeWhenHiddenSessionsAreAdded(t *testing.T) {
	e := seedSessions(t)
	strip := func(s string) string {
		return regexp.MustCompile(`name="csrf_token" value="[^"]*"`).ReplaceAllString(s, "")
	}
	urls := []string{
		"/ui/sessions",
		"/ui/sessions?q=zebrafish",
		"/ui/sessions?q=zebrafish&kind=output",
		"/ui/sessions?machine=laptop&agent=codex&owner=mine",
		"/ui/sessions?q=nomatchatall",
	}
	clients := map[string]*http.Client{"guest": e.Guest, "member": e.Member}
	before := map[string]string{}
	for n, c := range clients {
		for _, u := range urls {
			_, p := e.get(t, c, u)
			before[n+u] = strip(p)
		}
	}
	for i := 0; i < 60; i++ {
		id, err := e.St.UpsertSession(store.Session{Harness: "codex", ExternalID: fmt.Sprintf("hid-%d", i), AccountID: 1, Scope: scope.Axes{Project: shellProject, Machine: "secretbox"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := e.St.AppendChunks(id, chunks(uLine("zebrafish hidden "+fmt.Sprint(i)))); err != nil {
			t.Fatal(err)
		}
	}
	for n, c := range clients {
		for _, u := range urls {
			_, p := e.get(t, c, u)
			after := strip(p)
			if after != before[n+u] {
				// A member legitimately sees metadata rows of other sessions; only the guest must be byte-identical.
				if n == "guest" || strings.Contains(after, "secretbox") && strings.Contains(u, "q=") {
					t.Errorf("%s %s changes with hidden sessions", n, u)
				}
			}
		}
	}
}
