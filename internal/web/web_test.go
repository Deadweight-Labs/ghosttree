package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func testWeb(t *testing.T) (*httptest.Server, *store.Store, string) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/web.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	token, err := st.AddPerson("robin")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)
	return srv, st, token
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func login(t *testing.T, srv *httptest.Server, token string) *http.Client {
	t.Helper()
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.PostForm(srv.URL+"/ui/login", url.Values{"token": {token}, "next": {"/ui/requests"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login status=%d body=%s", resp.StatusCode, body(t, resp))
	}
	cookies := resp.Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie=%+v", cookies)
	}
	client.Jar = cookieJar{cookies: cookies}
	return client
}

type cookieJar struct{ cookies []*http.Cookie }

func (j cookieJar) SetCookies(*url.URL, []*http.Cookie) {}
func (j cookieJar) Cookies(*url.URL) []*http.Cookie     { return j.cookies }

func TestShellAuthAndNavigation(t *testing.T) {
	srv, _, token := testWeb(t)
	anon := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, _ := anon.Get(srv.URL + "/ui/requests")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/ui/login") {
		t.Fatalf("anonymous response=%d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	client := login(t, srv, token)
	resp, _ = client.Get(srv.URL + "/ui/requests")
	html := body(t, resp)
	for _, want := range []string{"Requests", "Knowledge", "Review", "Sessions", "Agent Context", `data-clay-theme="ghosttree"`} {
		if !strings.Contains(html, want) {
			t.Errorf("page missing %q", want)
		}
	}
}

func TestRequestsRenderCriteriaEvidenceAndEscapeHTML(t *testing.T) {
	srv, st, token := testWeb(t)
	detail, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: `<script>alert(1)</script>`, Description: "Useful ledger", Scope: scope.Axes{Project: "p"}}, Criteria: []string{"observable result"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetCriterionState(detail.Criteria[0].ID, "met", requestdomain.Evidence{Kind: "test", Ref: "go test ./...", Person: "robin"}); err != nil {
		t.Fatal(err)
	}
	client := login(t, srv, token)
	resp, _ := client.Get(srv.URL + "/ui/requests/" + detail.Request.HumanID()[4:])
	html := body(t, resp)
	for _, want := range []string{"observable result", "go test ./...", "Useful ledger", "&lt;script&gt;alert(1)&lt;/script&gt;"} {
		if !strings.Contains(html, want) {
			t.Errorf("detail missing %q: %s", want, html)
		}
	}
	if strings.Contains(html, "<script>alert(1)</script>") {
		t.Fatal("request title was not escaped")
	}
}

func TestOperatorSectionsUseStoredData(t *testing.T) {
	srv, st, token := testWeb(t)
	_, err := st.InsertKnowledge(store.Knowledge{Type: "decision", Title: "SQLite stays", Body: "Single writer", Scope: scope.Axes{Project: "p"}, Origin: "distilled", Confidence: "quarantined"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertKnowledge(store.Knowledge{Type: "note", Title: "SQLite runtime", Body: "Visible agent context", Scope: scope.Axes{Project: "p"}, Confidence: "trusted"}); err != nil {
		t.Fatal(err)
	}
	sessionID, err := st.UpsertSession(store.Session{Harness: "codex", ExternalID: "run-1", Scope: scope.Axes{Project: "p"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendChunks(sessionID, []store.Chunk{{Seq: 1, Role: "user", Text: "ledger context", Raw: `{}`}}); err != nil {
		t.Fatal(err)
	}
	client := login(t, srv, token)
	for path, want := range map[string]string{
		"/ui/knowledge?q=SQLite": "SQLite stays",
		"/ui/review":             "SQLite stays",
		"/ui/sessions":           "run-1",
		"/ui/sessions/" + strconv.FormatInt(sessionID, 10): "ledger context",
		"/ui/context?project=p":                            "SQLite runtime",
	} {
		resp, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		got := body(t, resp)
		if resp.StatusCode != 200 || !strings.Contains(got, want) {
			t.Errorf("%s: status=%d missing %q in %s", path, resp.StatusCode, want, got)
		}
	}
}
