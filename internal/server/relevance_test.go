package server

import (
	"fmt"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const relevantHeading = "## Possibly relevant to what you just said (ghosttree)"

func relevantURL(q, session string) string {
	u := "/api/context/relevant?project=" + accProject + "&q=" + q
	if session != "" {
		u += "&session=" + session
	}
	return u
}

func addPitfall(t *testing.T, f *accessAPIFixture, title string) int64 {
	t.Helper()
	id, err := f.st.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: title, Body: "body of " + title,
		Scope: scope.Axes{Project: accProject}, Person: "mia", Confidence: "trusted"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// The reader sees which word brought each entry, and the heading the hook and
// the model recognise stays as it was.
func TestRelevantNamesTheWordBehindEachEntry(t *testing.T) {
	f := accessAPI(t, true)
	addPitfall(t, f, "quillfeather zerbricht beim Neustart")

	out := f.expect(t, "mia", 200, "GET", relevantURL("was+ist+mit+quillfeather", ""), nil)
	if !strings.HasPrefix(out, relevantHeading+"\n\n") {
		t.Errorf("the heading changed: %q", out)
	}
	if !strings.Contains(out, "quillfeather zerbricht beim Neustart") || !strings.Contains(out, `(triggered by "quillfeather")`) {
		t.Errorf("the entry does not name its trigger:\n%s", out)
	}
}

// Once per session, over HTTP: the second mention delivers nothing, another
// session gets the entry again, and so does a caller that sends no session id.
func TestRelevantDeliversAnEntryOncePerSession(t *testing.T) {
	f := accessAPI(t, true)
	addPitfall(t, f, "quillfeather zerbricht beim Neustart")
	ask := func(who, session string) string {
		t.Helper()
		return f.expect(t, who, 200, "GET", relevantURL("was+ist+mit+quillfeather", session), nil)
	}

	if out := ask("mia", "sess-1"); !strings.Contains(out, "quillfeather zerbricht") {
		t.Fatalf("first mention: %q", out)
	}
	if out := ask("mia", "sess-1"); out != "" {
		t.Errorf("second mention in the same session delivered again: %q", out)
	}
	if out := ask("mia", "sess-2"); !strings.Contains(out, "quillfeather zerbricht") {
		t.Errorf("another session must receive it again: %q", out)
	}
	// The memory is per account: lena in the same session key is not mia.
	if out := ask("lena", "sess-1"); !strings.Contains(out, "quillfeather zerbricht") {
		t.Errorf("another account with the same session id must receive it: %q", out)
	}
	for range 2 {
		if out := ask("mia", ""); !strings.Contains(out, "quillfeather zerbricht") {
			t.Errorf("without a session id nothing is held back: %q", out)
		}
	}
}

// What the start-up briefing delivered in a session counts: a pitfall that is
// already in front of the model does not arrive a second time because a prompt
// names it. The key is the same session id the session-start hook sends.
func TestRelevantHonoursWhatTheBootstrapDelivered(t *testing.T) {
	f := accessAPI(t, true)
	addPitfall(t, f, "quillfeather zerbricht beim Neustart")

	boot := f.expect(t, "mia", 200, "GET", "/api/context/bootstrap?project="+accProject+"&session=sess-boot", nil)
	if !strings.Contains(boot, "quillfeather zerbricht") {
		t.Fatalf("the pitfall is not in the bootstrap, the test proves nothing:\n%s", boot)
	}
	if out := f.expect(t, "mia", 200, "GET", relevantURL("quillfeather", "sess-boot"), nil); out != "" {
		t.Errorf("an entry from the bootstrap was delivered again: %q", out)
	}
	if out := f.expect(t, "mia", 200, "GET", relevantURL("quillfeather", "sess-other"), nil); !strings.Contains(out, "quillfeather zerbricht") {
		t.Errorf("a session whose bootstrap did not carry it must get it: %q", out)
	}
	// A bootstrap without a session id (the MCP tool context_get) remembers nothing.
	f.expect(t, "mia", 200, "GET", "/api/context/bootstrap?project="+accProject, nil)
	if out := f.expect(t, "mia", 200, "GET", relevantURL("quillfeather", "sess-nobody"), nil); !strings.Contains(out, "quillfeather zerbricht") {
		t.Errorf("a bootstrap without session id suppressed an entry: %q", out)
	}
}

// An entry the budget cut off of the bootstrap was not delivered by it, and
// relevance must still be able to deliver it.
func TestRelevantStillDeliversWhatTheBootstrapBudgetCutOff(t *testing.T) {
	f := accessAPI(t, true)
	long := strings.Repeat("x", 190)
	for _, title := range []string{"quillfeather eins", "marzipanic zwei"} {
		if _, err := f.st.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: title, Body: long,
			Scope: scope.Axes{Project: accProject}, Person: "mia", Confidence: "trusted"}); err != nil {
			t.Fatal(err)
		}
	}
	// The budget is the character allowance after the header; find one that lets
	// exactly one of the two entries through rather than depend on how long a
	// scope label happens to be.
	var shown, cut, session string
	for budget := 100; budget <= 700 && cut == ""; budget += 25 {
		session = fmt.Sprintf("sess-cut-%d", budget)
		boot := f.expect(t, "robin", 200, "GET", fmt.Sprintf("/api/context/bootstrap?project=%s&session=%s&budget=%d", accProject, session, budget), nil)
		one, two := strings.Contains(boot, "quillfeather eins"), strings.Contains(boot, "marzipanic zwei")
		switch {
		case one && !two:
			shown, cut = "quillfeather eins", "marzipanic zwei"
		case two && !one:
			shown, cut = "marzipanic zwei", "quillfeather eins"
		}
	}
	if shown == "" || cut == "" {
		t.Fatal("no budget lets exactly one of the two entries through, the test proves nothing")
	}
	out := f.expect(t, "robin", 200, "GET", relevantURL("quillfeather+marzipanic", session), nil)
	if strings.Contains(out, shown) {
		t.Errorf("%q was in the bootstrap and arrived again: %s", shown, out)
	}
	if !strings.Contains(out, cut) {
		t.Errorf("%q was cut off the bootstrap and must still be delivered: %s", cut, out)
	}
}

// The unit under the handler: exactly what is written is what is reported.
func TestRenderBootstrapShownReportsOnlyWhatIsWritten(t *testing.T) {
	entries := []store.Knowledge{
		{ID: 1, Type: "instruction", Title: "rule", Body: "b", Confidence: "verified"},
		{ID: 2, Type: "pitfall", Title: "first", Body: strings.Repeat("x", 150), Confidence: "trusted"},
		{ID: 3, Type: "pitfall", Title: "second", Body: strings.Repeat("y", 150), Confidence: "trusted"},
		{ID: 4, Type: "pitfall", Title: "staged", Body: "b", Confidence: "staged"},
		{ID: 5, Type: "decision", Title: "held back", Body: "b", Confidence: "trusted"},
	}
	out, shown := renderBootstrapShown(entries, 330, false)
	got := map[int64]bool{}
	for _, id := range shown {
		got[id] = true
	}
	for id, title := range map[int64]string{1: "rule", 2: "first", 3: "second", 4: "staged", 5: "held back"} {
		if written := strings.Contains(out, title+" —"); got[id] != written {
			t.Errorf("entry %d (%s): reported shown = %v, written = %v\n%s", id, title, got[id], written, out)
		}
	}
	if got[4] || got[5] {
		t.Errorf("staged and held entries are not delivered by the bootstrap: %v", shown)
	}
	if !got[1] || !got[2] || got[3] {
		t.Errorf("shown = %v, want the instruction and the first pitfall but not the one the budget cut", shown)
	}
}

// Unconfirmed entries and the gardener's suggestions never come this way, even
// when their title is exactly the word that was said.
func TestRelevantLeavesOutStagedAndGardenerEntries(t *testing.T) {
	f := accessAPI(t, true)
	for _, k := range []store.Knowledge{
		{Type: "pitfall", Title: "quelzor hängt", Body: "b", Confidence: "staged"},
		{Type: "note", Title: "[Gärtner] Veraltet? vexilla", Body: "b", Confidence: "staged", Harness: "gardener", Origin: "distilled"},
	} {
		k.Scope, k.Person = scope.Axes{Project: accProject}, "mia"
		if _, err := f.st.InsertKnowledge(k); err != nil {
			t.Fatal(err)
		}
	}
	for _, word := range []string{"quelzor", "vexilla", "gärtner"} {
		if out := f.expect(t, "robin", 200, "GET", relevantURL(word, "sess-staged"), nil); strings.Contains(out, "quelzor hängt") || strings.Contains(out, "vexilla") {
			t.Errorf("%s: %s", word, out)
		}
	}
}

// A codename that no entry names but that a session mentions gets one hint line,
// built from what the reader may open. Robin reads every session of the project,
// mia only her own, nora none.
func TestRelevantHintNamesOnlySessionsTheReaderMayRead(t *testing.T) {
	f := accessAPI(t, true)
	label := func(name string) string { return fmt.Sprintf("session #%d", f.id["s-"+name]) }

	out := f.expect(t, "robin", 200, "GET", relevantURL("was+ist+secretterm", "hint-robin"), nil)
	if !strings.HasPrefix(out, relevantHeading) || !strings.Contains(out, `"secretterm" also came up in session #`) || !strings.Contains(out, "context_search finds more.") {
		t.Fatalf("no hint for the owner: %q", out)
	}
	if strings.Count(out, "- [hint]") != 1 {
		t.Errorf("more than one hint line: %q", out)
	}

	// mia reads her own session and nobody else's: the hint may name hers and
	// must not name robin's or rex's.
	out = f.expect(t, "mia", 200, "GET", relevantURL("was+ist+secretterm", "hint-mia"), nil)
	if !strings.Contains(out, label("mia")) {
		t.Errorf("mia gets no hint to her own session: %q", out)
	}
	for _, other := range []string{"robin", "rex"} {
		if strings.Contains(out, label(other)+" ") || strings.Contains(out, label(other)+"\"") {
			t.Errorf("mia's hint names the private session of %s: %q", other, out)
		}
	}
	// The session the prompt is typed in is left out: it holds the word because
	// it was just typed. Everything else mia could read is hidden, so no hint.
	if out = f.expect(t, "mia", 200, "GET", relevantURL("was+ist+secretterm", "s-mia"), nil); out != "" {
		t.Errorf("the hint points into the session that was typed in: %q", out)
	}
	// nora has no role in the project: the word occurs only in material she
	// cannot read, and the answer is the same silence as for a word that
	// occurs nowhere.
	if out = f.expect(t, "nora", 200, "GET", relevantURL("was+ist+secretterm", "hint-nora"), nil); out != "" {
		t.Errorf("a stranger learns that the word exists: %q", out)
	}
	if nowhere := f.expect(t, "nora", 200, "GET", relevantURL("was+ist+nirgendwoterm", "hint-nora"), nil); nowhere != "" {
		t.Errorf("control: %q", nowhere)
	}
}

// A guest reads no session unless one was shared with guests on purpose, and
// then sees it the way a guest sees every session: by its random address, never
// by its number, and without machine, branch or owner.
func TestRelevantHintForAGuestNamesOnlySessionsSharedWithGuests(t *testing.T) {
	f := accessAPI(t, true)
	if out := f.expect(t, "gus", 200, "GET", relevantURL("was+ist+secretterm", "g1"), nil); out != "" {
		t.Fatalf("a guest is pointed at a session nobody shared: %q", out)
	}
	if err := f.st.SetSessionVisibility(f.id["s-rex"], f.st.Access(store.Principal{ID: "person:4", Label: "rex"}), store.VisGuests); err != nil {
		t.Fatal(err)
	}
	se, err := f.st.SessionByID(f.id["s-rex"])
	if err != nil || se.PublicID == "" {
		t.Fatalf("session %+v %v", se, err)
	}
	out := f.expect(t, "gus", 200, "GET", relevantURL("was+ist+secretterm", "g2"), nil)
	if !strings.Contains(out, "session @"+se.PublicID) {
		t.Errorf("the guest is not pointed at the shared session by its address: %q", out)
	}
	if strings.Contains(out, fmt.Sprintf("#%d", f.id["s-rex"])) {
		t.Errorf("the guest learns the session number: %q", out)
	}
}

// One hint per word and session: the second mention is silent, a new session is
// told again, and no session id means no memory.
func TestRelevantHintComesOncePerWordAndSession(t *testing.T) {
	f := accessAPI(t, true)
	ask := func(session string) string {
		return f.expect(t, "robin", 200, "GET", relevantURL("was+ist+secretterm", session), nil)
	}
	if out := ask("h1"); !strings.Contains(out, "[hint]") {
		t.Fatalf("first mention: %q", out)
	}
	if out := ask("h1"); out != "" {
		t.Errorf("second mention in the same session: %q", out)
	}
	if out := ask("h2"); !strings.Contains(out, "[hint]") {
		t.Errorf("a new session must be told again: %q", out)
	}
	for range 2 {
		if out := ask(""); !strings.Contains(out, "[hint]") {
			t.Errorf("without a session id nothing is held back: %q", out)
		}
	}
}

// A request that names the word is hinted too, by number and title — and only
// to a reader who may see the request.
func TestRelevantHintNamesARequestOnlyToWhoMaySeeIt(t *testing.T) {
	f := accessAPI(t, true)
	d, err := f.st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature",
		Title: "blorptastic Migration planen", Scope: scope.Axes{Project: accProject}, Person: "mia"}})
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("REQ-%d", d.Request.ID)
	if out := f.expect(t, "mia", 200, "GET", relevantURL("was+ist+blorptastic", "r-mia"), nil); !strings.Contains(out, want) || !strings.Contains(out, "blorptastic Migration planen") {
		t.Errorf("mia is not pointed at her request: %q", out)
	}
	if out := f.expect(t, "nora", 200, "GET", relevantURL("was+ist+blorptastic", "r-nora"), nil); out != "" {
		t.Errorf("a stranger is pointed at a request of a project they cannot read: %q", out)
	}
}

// A word an entry already answers needs no hint: the entry arrives and the
// session is not also pointed at old sessions.
func TestRelevantNeedsNoHintWhereAnEntryAnswers(t *testing.T) {
	f := accessAPI(t, true)
	addPitfall(t, f, "secretterm im Log bedeutet Alarm")
	out := f.expect(t, "robin", 200, "GET", relevantURL("was+ist+secretterm", "answered"), nil)
	if !strings.Contains(out, "secretterm im Log bedeutet Alarm") || strings.Contains(out, "[hint]") {
		t.Errorf("the entry should answer alone: %q", out)
	}
}

// The language gate measures the account that asks, over HTTP as in the store: a
// word that robin says all the time holds back what robin would be told and not
// what mia is told, and nothing robin or rex write changes mia's answer.
func TestRelevantLanguageGateMeasuresOnlyTheCaller(t *testing.T) {
	f := accessAPI(t, true)
	addPitfall(t, f, "dinge zerbrechen leise")
	fill := func(session string, from, n int, dinge bool) {
		t.Helper()
		chunks := make([]store.Chunk, n)
		for i := range chunks {
			text := fmt.Sprintf("das war ein langes gespräch über den server %d", from+i)
			if dinge && i%3 == 0 {
				text = fmt.Sprintf("die dinge laufen %d", from+i)
			}
			chunks[i] = store.Chunk{Seq: from + i, Role: "user", Text: text, Raw: "{}"}
		}
		if err := f.st.AppendChunks(f.id[session], chunks); err != nil {
			t.Fatal(err)
		}
	}
	// Both have enough text for a gate; only robin's says "dinge".
	fill("s-mia", 1, 6000, false)
	fill("s-robin", 1, 6000, true)
	ask := func(who string) string {
		t.Helper()
		return f.expect(t, who, 200, "GET", relevantURL("was+ist+stand+der+dinge", ""), nil)
	}

	before := ask("mia")
	if !strings.Contains(before, "dinge zerbrechen leise") {
		t.Fatalf("mia, who never says the word, is told nothing: %q", before)
	}
	if out := ask("robin"); strings.Contains(out, "dinge zerbrechen leise") {
		t.Errorf("robin says the word all the time and is still told: %q", out)
	}
	// What the others write, however much of it, does not move mia's answer.
	fill("s-robin", 6001, 6000, true)
	fill("s-rex", 1, 12000, true)
	if after := ask("mia"); after != before {
		t.Errorf("other accounts' text changed mia's answer:\nbefore: %q\nafter:  %q", before, after)
	}
}

// On the store the service really runs — queued writes and a separate read-only
// pool — the memory written by one request is what the next request reads, for
// entries and for hints alike.
func TestRelevantOncePerSessionOnTheRuntimeStore(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "relevance.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	token, err := st.AddPerson("alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: "quillfeather zerbricht beim Neustart", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	id, err := st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "old", Scope: scope.Axes{Project: accProject}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendChunks(id, []store.Chunk{{Seq: 0, Role: "user", Text: "hlpvpngw läuft wieder", Raw: "{}"}}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)

	ask := func(q, session string) string {
		t.Helper()
		resp := req(t, "GET", srv.URL+"/api/context/relevant?q="+q+"&session="+session, token, nil)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("status %d: %s", resp.StatusCode, b)
		}
		return string(b)
	}
	for _, q := range []string{"was+ist+mit+quillfeather", "was+ist+mit+hlpvpngw"} {
		first := ask(q, "run-1")
		if first == "" {
			t.Fatalf("%s: nothing the first time", q)
		}
		if again := ask(q, "run-1"); again != "" {
			t.Errorf("%s: delivered again in the same session: %q", q, again)
		}
		if other := ask(q, "run-2"); other == "" {
			t.Errorf("%s: another session got nothing", q)
		}
	}
}

// The replay of old prompts and a reader without a session id are unaffected by
// the memory; and the heading is the one the hook looks for.
func TestRelevantStaysSilentForOrdinaryProse(t *testing.T) {
	f := accessAPI(t, true)
	out := f.expect(t, "mia", 200, "GET", relevantURL("bitte+mach+den+button+etwas+gr%C3%B6sser", "ordinary"), nil)
	if out != "" {
		t.Errorf("ordinary prose delivered %q", out)
	}
}
