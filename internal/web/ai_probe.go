package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/aiproxy"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// "Test connection" (AC-707.3): one minimal call through the same way a run's
// calls take (aiproxy.Proxy: the key is put in there, the address checked as
// it is for a run, the call booked with its cost and the caps that apply).
// Only who keeps the connection may test it: the admin for the server's, the
// owner of the namespace for its own. The outcome is worded for the person
// and shown on the connection's card.

// AITester makes the test call; aiproxy.Proxy is one.
type AITester interface {
	TestConnection(ctx context.Context, actor string, connID int64) (aiproxy.TestResult, error)
}

// WithAITester gives the settings the means to test a connection; without it
// the button is not offered.
func WithAITester(t AITester) Option { return func(a *app) { a.aiTester = t } }

// aiTestShown is how long the page keeps the outcome of a test.
const aiTestShown = 15 * time.Minute

type aiTestEntry struct {
	res  aiproxy.TestResult
	wait time.Duration // > 0: the test was refused for coming too fast
	at   time.Time
}

// aiTestLog holds the last outcome per connection, in memory.
type aiTestLog struct {
	mu sync.Mutex
	m  map[int64]aiTestEntry
}

func (t *aiTestLog) put(id int64, e aiTestEntry) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m == nil {
		t.m = map[int64]aiTestEntry{}
	}
	for k, v := range t.m {
		if time.Since(v.at) > aiTestShown {
			delete(t.m, k)
		}
	}
	t.m[id] = e
}

func (t *aiTestLog) get(id int64) (aiTestEntry, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.m[id]
	return e, ok && time.Since(e.at) <= aiTestShown
}

// aiTestView is the outcome of a test as the card shows it.
type aiTestView struct {
	OK           bool
	Text, Detail string
	At           string
}

// aiTestOf is the last outcome of the connection, for who may test it.
func (a *app) aiTestOf(id int64, l lang) *aiTestView {
	if a.aiTester == nil {
		return nil
	}
	e, ok := a.aiTests.get(id)
	if !ok {
		return nil
	}
	text, detail := aiTestWords(l, e)
	return &aiTestView{OK: e.res.OK, Text: text, Detail: detail, At: e.at.Local().Format("15:04")}
}

// aiTestWords says what a test found, in plain words.
func aiTestWords(l lang, e aiTestEntry) (text, detail string) {
	r := e.res
	host := r.Host
	status := strconv.Itoa(r.Status)
	switch {
	case e.wait > 0:
		return l.msg("ai.test.rate_limit", int(e.wait.Round(time.Second).Seconds())+1), ""
	case r.OK:
		return l.msg("ai.test.ok", r.Model, r.Millis, wfCost(l, r.CostUSD)), ""
	}
	switch r.Kind {
	case aiproxy.TestKey:
		text = l.msg("ai.test.key", status)
	case aiproxy.TestNotFound:
		text = l.msg("ai.test.not_found", host, r.Model)
	case aiproxy.TestRequest:
		text = l.msg("ai.test.request", status, r.Model)
	case aiproxy.TestPayment:
		text = l.msg("ai.test.payment")
	case aiproxy.TestRateLimited:
		text = l.msg("ai.test.rate")
	case aiproxy.TestServer:
		text = l.msg("ai.test.server", status)
	case aiproxy.TestTimeout:
		text = l.msg("ai.test.timeout", host, int(r.Millis/1000))
	case aiproxy.TestUnreachable:
		text = l.msg("ai.test.unreachable", host)
	case aiproxy.TestTLS:
		text = l.msg("ai.test.tls", host)
	case aiproxy.TestAddress:
		text = l.msg("ai.test.address")
	case aiproxy.TestNoKey:
		text = l.msg("ai.test.no_key")
	case aiproxy.TestCap:
		return l.msg("ai.test.cap", l.msg(aiCapReasons[r.Detail])), ""
	default:
		text = l.msg("ai.test.other", status)
	}
	return text, r.Detail
}

// aiCapReasons name the caps a test can run into, in the words runs use.
var aiCapReasons = map[string]string{
	store.AICapRun: "wf.reason.cap.run", store.AICapWorkflow: "wf.reason.cap.workflow", store.AICapOwner: "wf.reason.cap.owner",
	store.AICapConnection: "wf.reason.cap.connection", store.AICapServer: "wf.reason.cap.server", store.AICapLoan: "wf.reason.cap.loan",
}

// aiConnectionTest is POST /ui/orgs/ai/connection/test.
func (a *app) aiConnectionTest(w http.ResponseWriter, r *http.Request) {
	o, server, ok := a.aiForm(w, r)
	if !ok {
		return
	}
	id, orgID := formID(r, "id"), o.ID
	if server {
		orgID = 0
	}
	if !a.aiConnectionIn(id, orgID) {
		a.aiFailure(w, r, server, store.ErrAIUnavailable)
		return
	}
	if a.aiTester == nil {
		a.aiFailure(w, r, server, errAITestOff)
		return
	}
	res, err := a.aiTester.TestConnection(r.Context(), browserPrincipal(r).ID, id)
	var rate *aiproxy.TestRateError
	switch {
	case errors.As(err, &rate):
		a.aiTests.put(id, aiTestEntry{wait: rate.Wait, at: time.Now()})
	case err != nil:
		a.aiFailure(w, r, server, err)
		return
	default:
		a.aiTests.put(id, aiTestEntry{res: res, at: time.Now()})
	}
	q := url.Values{"org": {o.Slug}}
	http.Redirect(w, r, "/ui/orgs?"+q.Encode()+"#"+aiRowID(server, id), http.StatusSeeOther)
}

var errAITestOff = fmt.Errorf("%w: connections cannot be tested here", store.ErrInvalidInput)

// aiRowID is the anchor of a connection's row. A connection the server released
// to an organization stands in the organization's list as well as in the
// server's, under the same number, so the server's row has its own anchor: the
// address of a test of a server connection must open the server's section, not
// the first row of that number (N-B).
func aiRowID(server bool, id int64) string {
	if server {
		return "ai-server-" + strconv.FormatInt(id, 10)
	}
	return "ai-" + strconv.FormatInt(id, 10)
}

// aiSectionRedirect is GET on an address that only takes forms: it leads to the
// AI connections of the organization the address names.
func aiSectionRedirect(w http.ResponseWriter, r *http.Request) {
	q := url.Values{}
	if org := r.URL.Query().Get("org"); org != "" {
		q.Set("org", org)
	}
	target := "/ui/orgs"
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	http.Redirect(w, r, target+"#ai", http.StatusSeeOther)
}
