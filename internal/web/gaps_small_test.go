package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

var echoedLinkRE = regexp.MustCompile(`value="[^"]*"`)

// /ui/register prüft den Link wie /join/{code}, bevor es dorthin leitet; alles, was nicht
// taugt, antwortet gleich, Echo der Eingabe ausgenommen (REQ-599).
func TestRegisterChecksTheInvitationLikeTheJoinPageAndAnswersTheSameForEveryBadOne(t *testing.T) {
	srv, st, _, org, _ := joinWeb(t)
	if _, err := st.AddPerson("anna"); err != nil {
		t.Fatal(err)
	}
	good := projectInvite(t, st, org, store.RoleMember)
	short, _, err := st.CreateProjectInvitation("person:1", org.ID, joinProject, store.RoleMember, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	used := projectInvite(t, st, org, store.RoleMember)
	if _, err := st.AcceptInvitation("person:2", used); err != nil {
		t.Fatal(err)
	}
	revoked, inv, _ := st.CreateProjectInvitation("person:1", org.ID, joinProject, store.RoleMember, 0)
	if err := st.RevokeInvitation("person:1", org.ID, inv.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	c := anonClient()
	post := func(link string) (int, string, string) {
		resp := sameOriginPostForm(t, c, srv.URL+"/ui/register", url.Values{"link": {link}})
		loc := resp.Header.Get("Location")
		return resp.StatusCode, loc, body(t, resp)
	}
	// A valid link, as a whole link or as a bare code, leads to the invitation page.
	for _, link := range []string{"https://ghosttree.example/join/" + good + "?x=1", good, strings.ToUpper(good)} {
		if code, loc, _ := post(link); code != http.StatusSeeOther || loc != "/join/"+good {
			t.Errorf("a valid link %.30q -> %d %q", link, code, loc)
		}
	}
	// Unknown, expired, used and revoked codes get one answer, and it says nothing of which.
	var want string
	for name, link := range map[string]string{"unknown": strings.Repeat("ab", joinCodeLen/2), "expired": short, "used": used, "revoked": revoked,
		"unknown in a link": "https://ghosttree.example/join/" + strings.Repeat("cd", joinCodeLen/2)} {
		code, loc, page := post(link)
		if code != http.StatusUnprocessableEntity || loc != "" || !strings.Contains(page, "does not work") {
			t.Errorf("%s -> %d %q, want 422 with the one message", name, code, loc)
		}
		for _, leak := range []string{"expired", "revoked", "used up", "Invited by", "alpha"} {
			if strings.Contains(strings.ToLower(page), strings.ToLower(leak)) && leak != "expired" {
				t.Errorf("%s: the answer mentions %q", name, leak)
			}
		}
		normal := echoedLinkRE.ReplaceAllString(page, `value=""`)
		if want == "" {
			want = normal
		} else if normal != want {
			t.Errorf("%s answers differently from the others", name)
		}
	}
	// A string that is no link at all keeps its own message and never reaches the store.
	if code, _, page := post("hello"); code != http.StatusUnprocessableEntity || !strings.Contains(page, "That is not an invitation link.") {
		t.Errorf("a wrong link -> %d", code)
	}
	// Guessing is held back by the limit of the join page.
	limited := false
	for i := 0; i < 200 && !limited; i++ {
		if code, _, _ := post(strings.Repeat("0", joinCodeLen-3) + "abc"); code == http.StatusTooManyRequests {
			limited = true
		}
	}
	if !limited {
		t.Error("guessing codes through /ui/register is not limited")
	}
}

// Der Fuß der Anmelde-, Registrier- und Einladungsseiten hat keine einzelnen
// Trennpunkte, die am Zeilenende stranden: Datenschutz, Hilfe und Version stehen
// mit Abstand nebeneinander (Mock), mit und ohne Version.
func TestTheAuthFooterHasNoSeparatorDotThatCanBeLeftAlone(t *testing.T) {
	for _, ver := range []string{"", "0.1.0-dev+abc1234"} {
		st, err := store.Open(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		opts := append(append([]Option{}, testWebOptions...), WithBuildVersion(ver))
		srv := httptest.NewServer(New(st, opts...))
		t.Cleanup(srv.Close)
		t.Cleanup(func() { st.Close() })
		for _, path := range []string{"/ui/login", "/ui/register", "/join/" + strings.Repeat("ab", joinCodeLen/2)} {
			resp, err := http.Get(srv.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			page := body(t, resp)
			i := strings.Index(page, `<footer class="gt-foot">`)
			if i < 0 {
				t.Fatalf("%s (version %q) has no footer", path, ver)
			}
			foot := page[i : i+strings.Index(page[i:], "</footer>")]
			if strings.Contains(foot, "sep") || strings.Contains(foot, "·") {
				t.Errorf("%s (version %q): the footer carries a separator dot: %s", path, ver, foot)
			}
			if strings.Contains(foot, "ghosttree ") != (ver != "") {
				t.Errorf("%s (version %q): the version line is wrong: %s", path, ver, foot)
			}
			if !strings.Contains(foot, "Privacy") || !strings.Contains(foot, "Help") {
				t.Errorf("%s: privacy or help is missing", path)
			}
		}
	}
}

// Jede Art von Verlauf, die der Store schreibt, hat Worte in beiden Sprachen: nichts davon
// erscheint als Rohwert („pr.merged“, „request.reopened“).
func TestEveryHistoryKindTheStoreWritesIsPutInWordsInBothLanguages(t *testing.T) {
	kinds := []string{"request.created", "request.corrected", "request.moved", "request.done", "request.dropped", "request.reopened", "request.migrated",
		"criterion.added", "criterion.met", "criterion.waived", "criterion.reopened", "relation.added", "relation.removed", "relation.taken_over",
		"work.started", "work.resumed", "work.finished", "evidence.migrated", "package.added", "package.removed", "package.role",
		"pain.linked", "pain.unlinked", "task.created", "task.done", "task.confirmed", "task.reopened", "task.dropped", "pr.merged"}
	data := map[string]string{"pr.merged": "PR #12 Pull requests tab merged into main: no branch knowledge; 0 ghost descriptions lifted",
		"criterion.reopened": "AC-5.2 (was met) — the proof was wrong", "request.reopened": "AC-5.2 reopened",
		"relation.taken_over": "external branch:x — übernommen in PKG-3, Relation bleibt stehen"}
	for _, l := range []lang{langEN, langDE} {
		for _, kind := range kinds {
			acts := []requestdomain.Activity{{ID: 1, Kind: kind, Data: data[kind], Person: "alice", CreatedAt: "2026-11-08T10:00:00Z"}}
			evs := issueEvents(l, acts, nil, nil, "", func(string) string { return "now" })
			if len(evs) != 1 {
				t.Fatalf("%s: %d events", kind, len(evs))
			}
			s := strings.ToLower(string(evs[0].Sentence))
			if strings.Contains(s, strings.ToLower(kind)) || strings.TrimSpace(s) == "" || strings.Contains(s, "issue.ev.") || strings.Contains(s, "requests.act.") {
				t.Errorf("%v %s reads as %q", l, kind, s)
			}
			label := l.msg(activityKinds[kind])
			if activityKinds[kind] == "" || label == activityKinds[kind] {
				t.Errorf("%v %s has no label for the list of the old page", l, kind)
			}
		}
	}
	// The pull request number is named, the English note of the store is not shown.
	evs := issueEvents(langDE, []requestdomain.Activity{{ID: 1, Kind: "pr.merged", Data: data["pr.merged"], CreatedAt: "2026-11-08T10:00:00Z"}}, nil, nil, "", func(string) string { return "" })
	if got := string(evs[0].Sentence); !strings.Contains(got, "#12") || strings.Contains(got, "merged into") || evs[0].Quote != "" {
		t.Errorf("merged pull request: %q (quote %q)", got, evs[0].Quote)
	}
}

// Jede Relationsart, die der Store kennt, hat einen Text in en und de, auch derived.
func TestEveryRelationKindHasALabelInBothLanguages(t *testing.T) {
	for _, kind := range []string{"parent", "related", "blocks", "duplicates", "supersedes", "knowledge", "external", "derived"} {
		for _, l := range []lang{langEN, langDE} {
			key, ok := relationKeys[kind]
			if !ok {
				t.Errorf("relation kind %q has no label key", kind)
				continue
			}
			if got := label(l, relationKeys, kind); got == kind || got == key || strings.TrimSpace(got) == "" {
				t.Errorf("%v relation kind %q reads as %q", l, kind, got)
			}
			// An external link reads like "related" in the history: no sentence of its own.
			if _, ok := relationEventKeys[kind]; !ok && kind != "external" {
				t.Errorf("relation kind %q has no sentence for its history entry", kind)
			}
		}
	}
	if label(langDE, relationKeys, "derived") != "Entstanden aus" || label(langEN, relationKeys, "derived") != "Derived from" {
		t.Error("derived has the wrong words")
	}
}
