package web

import (
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// msg ist der englische Text zu einem Schlüssel; die Tests laufen auf Englisch.
func msg(key string, args ...any) string { return langEN.msg(key, args...) }

var verbRE = regexp.MustCompile(`%(?:\[\d+\])?[a-zA-Z%]`)

// verbs liefert die Platzhalter eines Textes, sortiert, ohne %%.
func verbs(s string) []string {
	var out []string
	for _, v := range verbRE.FindAllString(s, -1) {
		if v != "%%" {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func TestGermanCatalogMatchesEnglish(t *testing.T) {
	for key, en := range messages {
		de, ok := messagesDE[key]
		if !ok {
			t.Errorf("key %q has no German text", key)
			continue
		}
		if strings.TrimSpace(de) == "" {
			t.Errorf("key %q has an empty German text", key)
		}
		if a, b := strings.Join(verbs(en), " "), strings.Join(verbs(de), " "); a != b {
			t.Errorf("key %q: placeholders differ: en %q, de %q", key, a, b)
		}
	}
	for key := range messagesDE {
		if _, ok := messages[key]; !ok {
			t.Errorf("German key %q does not exist in English", key)
		}
	}
}

func TestEveryEnglishTextIsNotEmpty(t *testing.T) {
	for key, en := range messages {
		if strings.TrimSpace(en) == "" {
			t.Errorf("key %q has an empty English text", key)
		}
	}
}

func testWebLang(t *testing.T, language string) (*httptest.Server, string) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/web.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	token, err := st.AddPerson("alice")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st, WithLanguage(language)))
	t.Cleanup(srv.Close)
	return srv, token
}

func TestGermanServerShowsGermanPagesAndEnglishStaysDefault(t *testing.T) {
	de, token := testWebLang(t, "de")
	login := body(t, getURL(t, http.DefaultClient, de.URL+"/ui/login"))
	for _, want := range []string{`<html lang="de"`, `<h1>Anmelden</h1>`, `Code oder Login-Link`} {
		if !strings.Contains(login, want) {
			t.Errorf("German login page lacks %q", want)
		}
	}
	if strings.Contains(login, "Sign in") {
		t.Error("German login page still shows English text")
	}
	client := login(t, de, token)
	for path, want := range map[string]string{
		"/ui/overview": "Start",
		"/ui/requests": "Requests",
		"/ui/coord":    "Räume",
		"/ui/orgs":     "Organisation",
	} {
		page := body(t, getURL(t, client, de.URL+path))
		if !strings.Contains(page, `<html lang="de"`) || !strings.Contains(page, want) {
			t.Errorf("%s on a German server: html lang or %q missing", path, want)
		}
		if strings.Contains(page, ">Sign out<") || strings.Contains(page, ">Main<") {
			t.Errorf("%s on a German server still shows English shell text", path)
		}
	}
	en, _ := testWebLang(t, "")
	page := body(t, getURL(t, http.DefaultClient, en.URL+"/ui/login"))
	if !strings.Contains(page, `<html lang="en"`) || !strings.Contains(page, `<h1>Sign in</h1>`) {
		t.Error("the default server is not English")
	}
}

func TestLanguageIsResolvedPerRequestNotGlobally(t *testing.T) {
	de, _ := testWebLang(t, "de")
	en, _ := testWebLang(t, "en")
	a := body(t, getURL(t, http.DefaultClient, de.URL+"/ui/login"))
	b := body(t, getURL(t, http.DefaultClient, en.URL+"/ui/login"))
	c := body(t, getURL(t, http.DefaultClient, de.URL+"/ui/login"))
	if a != c || a == b {
		t.Error("two servers with different languages must not influence each other")
	}
}

func TestParseLanguage(t *testing.T) {
	for in, want := range map[string]string{"": "en", "en": "en", " DE ": "de", "de": "de"} {
		if got, err := ParseLanguage(in); err != nil || got != want {
			t.Errorf("ParseLanguage(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseLanguage("fr"); err == nil {
		t.Error("an unknown language must be refused")
	}
}

func TestGermanDatesAndAges(t *testing.T) {
	at := time.Date(2026, 3, 4, 15, 30, 0, 0, time.UTC) // Mittwoch
	for _, c := range []struct{ got, want string }{
		{langDE.date(at, false), "4. Mär"},
		{langDE.date(at, true), "4. Mär 2026"},
		{langDE.weekdayDate(at), "Mi 4. Mär"},
		{langDE.dateTime(at), "4. Mär 15:30"},
		{langEN.weekdayDate(at), "Wed 4 Mar"},
		{langEN.date(at, true), "4 Mar 2026"},
		{shortAge(langDE, at.Add(90*time.Minute), at), "1 Std."},
		{shortAge(langEN, at.Add(90*time.Minute), at), "1h"},
		{langDE.msg("time.ago", "5 Min."), "vor 5 Min."},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
	if got := string(timeTag(langDE, "2026-03-04T15:30:00Z")); !strings.Contains(got, "vor ") {
		t.Errorf("German time tag lacks %q: %s", "vor ", got)
	}
}

func getURL(t *testing.T, c *http.Client, url string) *http.Response {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
