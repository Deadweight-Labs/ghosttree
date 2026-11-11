package server

import (
	"fmt"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func TestAsksAboutEarlierWork(t *testing.T) {
	asks := []string{
		"Wie hatten wir das damals mit dem Router gelöst?",
		"was haben wir letzte Woche zu hlpvpngw entschieden",
		"Hast du das schon mal gebaut?",
		"wir haben das doch schon besprochen",
		"Wann hatten wir das Thema Deploy",
		"erinnerst du dich an den Fehler",
		"das war gestern noch anders",
		"vor zwei Tagen lief es noch",
		"in der letzten Session haben wir das umgebaut",
		"what did we decide about the router earlier?",
		"did we try this before",
		"as we discussed in the previous session",
		"Weißt du noch, wie das ging?",
		"schon besprochen",
		"das haben wir bereits entschieden",
		"Ist das schon gebaut?",
		"was ist mit quillfeather",
		"was ist hlpvpngw",
		"what is hlpvpngw",
	}
	for _, p := range asks {
		if !asksAboutEarlierWork(p) {
			t.Errorf("%q asks about earlier work, the cue missed it", p)
		}
	}
	// The sentences of PAIN-70 and prompts that merely contain a rare word or an
	// everyday word that points at nothing.
	plain := []string{
		"wohin soll die Datei?",
		"du hattest recht, das passt",
		"Hattest du Zeit?",
		"bitte prüf hlpvpngw noch einmal",
		"mach das bereits vorhandene Skript schneller",
		"run the tests before you push",
		"kannst du das bitte noch einmal prüfen",
		"die alte Version bleibt bestehen",
		"",
	}
	for _, p := range plain {
		if asksAboutEarlierWork(p) {
			t.Errorf("%q does not ask about earlier work, the cue fired", p)
		}
	}
}

// The hint searches the sessions, so it looks there only for a prompt that asks
// about earlier work (PAIN-70, PAIN-76). The same codename, said without such a
// question, gets no session hint, and the sessions are not searched for it.
func TestRelevantHintComesOnlyWithAQuestionAboutEarlierWork(t *testing.T) {
	f := accessAPI(t, true)
	if out := f.expect(t, "robin", 200, "GET", relevantURL("was+ist+secretterm", "cue-1"), nil); !strings.Contains(out, `"secretterm" also came up in session #`) {
		t.Fatalf("a question about the word gets no hint: %q", out)
	}
	for _, q := range []string{"bitte+pr%C3%BCf+secretterm+noch+einmal", "secretterm", "wohin+mit+secretterm+hattest+du"} {
		if out := f.expect(t, "robin", 200, "GET", relevantURL(q, "cue-2-"+q), nil); strings.Contains(out, "[hint]") {
			t.Errorf("%s: a hint for a prompt that asks nothing about earlier work: %q", q, out)
		}
	}
}

// The pointer to a request is cheap and stays for a prompt that asks nothing about
// earlier work; the sessions are left out. A word that only a session names gets no
// line, and a reader who may not see the request gets none either.
func TestRelevantHintNamesARequestWithoutAQuestionAboutEarlierWork(t *testing.T) {
	f := accessAPI(t, true)
	d, err := f.st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature",
		Title: "blorptastic Migration planen", Scope: scope.Axes{Project: accProject}, Person: "mia"}})
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("REQ-%d", d.Request.ID)
	if asksAboutEarlierWork("bitte prüf blorptastic noch einmal") {
		t.Fatal("the prompt of the test asks about earlier work, it proves nothing")
	}
	out := f.expect(t, "mia", 200, "GET", relevantURL("bitte+pr%C3%BCf+blorptastic+noch+einmal", "nc-mia"), nil)
	if !strings.Contains(out, want) || strings.Contains(out, "session") {
		t.Errorf("the request is not named without a cue, or a session is: %q", out)
	}
	if out := f.expect(t, "nora", 200, "GET", relevantURL("bitte+pr%C3%BCf+blorptastic+noch+einmal", "nc-nora"), nil); out != "" {
		t.Errorf("a stranger is pointed at a request they cannot see: %q", out)
	}
	if out := f.expect(t, "robin", 200, "GET", relevantURL("bitte+pr%C3%BCf+secretterm+noch+einmal", "nc-robin"), nil); strings.Contains(out, "[hint]") {
		t.Errorf("a word only a session names got a hint without a cue: %q", out)
	}
}
