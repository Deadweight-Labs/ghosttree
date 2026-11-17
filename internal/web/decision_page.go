package web

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
)

// The page of one decision, /ui/{org}/{repo}/decisions/{n} (REQ-684). Every decision has this
// address, the one the server and the tools name for DEC-n. A decision of an idea lives in the
// workshop of the idea: the address leads there, anchored at the decision. A decision of an issue
// has no workshop, so it shows here: the question, the variants, the votes and the forms that
// vote, decide or reopen it, the same article as in the workshop (workshop.html, "wsdec").

type decisionPageView struct {
	ID                  int64
	Question            string
	Num                 string
	Asked, Age          string
	Decision            workshopDecision
	Token, Viewer, Base string
	Result              *requestResult
	// Subject is the issue the question was asked of.
	Subject *ideaFact
}

// decisionPageHref is the address of the page of a decision.
func decisionPageHref(d idea.Decision) string {
	return repoHref(d.Project) + "/decisions/" + strconv.FormatInt(d.ID, 10)
}

// decisionInRepo is /ui/{org}/{repo}/decisions/{n}. A decision of another project is as unknown
// here as one the viewer may not read or one that does not exist.
func (a *app) decisionInRepo(w http.ResponseWriter, r *http.Request, remote, id string) {
	l := a.langFor(r)
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	pa := a.access(r)
	d, err := pa.DecisionView(n)
	if err != nil || d.Project != remote {
		http.NotFound(w, r)
		return
	}
	if d.Subject == idea.SubjectIdea {
		target := repoHref(remote) + "/ideas/" + strconv.FormatInt(d.SubjectID, 10)
		if rq := r.URL.Query(); rq.Get("wdone") != "" || rq.Get("wfail") != "" {
			target += "?" + rq.Encode()
		}
		http.Redirect(w, r, target+"#decision-"+id, http.StatusFound)
		return
	}
	q := r.URL.Query()
	q.Set("project", remote)
	r2 := r.Clone(r.Context())
	r2.URL.RawQuery = q.Encode()
	view := a.repoViewFor(r2, pa, a.shellBaseFor(r2).who.kind, remote, "issues")
	r2 = r2.WithContext(context.WithValue(r2.Context(), repoCtxKey{}, view))
	now := overviewNow().UTC()
	human := interactive(r)
	wd := decisionViewOf(l, r, d, now, human && pa.CanWorkDecision(d), human && pa.CanCloseDecision(d))
	v := &decisionPageView{ID: d.ID, Question: d.Question, Num: d.HumanID(), Decision: wd, Token: csrfOf(r), Viewer: browserPrincipal(r).Label,
		Base: view.Href, Asked: d.Person, Age: shortAgo(l, now, parseTime(d.CreatedAt))}
	if d.Subject == idea.SubjectRequest {
		sub := &ideaFact{Num: "#" + strconv.FormatInt(d.SubjectID, 10), Brief: l.msg("ref.kind.issue"), Href: view.Href + "/issues/" + strconv.FormatInt(d.SubjectID, 10)}
		if det, err := a.store.RequestByID(d.SubjectID); err == nil {
			sub.Title = det.Request.Title
		}
		v.Subject = sub
	}
	switch rq := r.URL.Query(); {
	case rq.Get("wfail") != "":
		key, ok := workshopFailKeys[rq.Get("wfail")]
		if !ok {
			key = "workshop.fail.failed"
		}
		v.Result = &requestResult{Text: l.msg(key), Kind: "failed", Fail: true}
	case rq.Get("wdone") != "":
		if key, ok := workshopDoneKeys[rq.Get("wdone")]; ok {
			v.Result = &requestResult{Text: l.msg(key), Kind: "done"}
		}
	}
	a.renderBrowser(w, r2, "decision", pageData{Title: d.Question, Project: remote, DecisionV: v})
}
