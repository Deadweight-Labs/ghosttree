package web

import (
	"context"
	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The page "New issue" (also "New idea" and "New pain") of a repository, and the
// form behind it (REQ-599). It asks for what the ledger takes: title, text, type,
// requirements (issues), labels and, for an issue, a work package with the role
// of the issue in it. The role (Pflicht or optional) belongs to the issue in the
// package, not to a single requirement: the ledger has no such flag per
// requirement, so none is asked for.

const (
	newKindIssue = "issue"
	newKindIdea  = "idea"
	newKindPain  = "pain"
	newCriteria  = 3
	maxCriteria  = 40
)

type newPackageOption struct {
	ID       int64
	Label    string
	Selected bool
}

type newTypeOption struct {
	Value, Label string
	Selected     bool
}

type newLabelOption struct {
	Name, Class string
	Checked     bool
}

// newIssueView is the page: Kind picks the texts and which parts show.
type newIssueView struct {
	Kind, Base, Project, Token string
	TitleKey, SubmitKey        string
	HasCriteria, HasPackage    bool
	Title, Body                string
	Criteria                   []string
	Types                      []newTypeOption
	Labels                     []newLabelOption
	Packages                   []newPackageOption
	PackageChoice              string
	PackageName, PackageBranch string
	Role                       string
	// Parent and DerivedFrom are the fields "Parent issue" and "Derived from":
	// REQ-n for the parent, IDEA-n, PAIN-n, REQ-n or thread:n (separated by
	// spaces or commas) for where the issue came from.
	Parent, DerivedFrom string
	Again               bool
	Composer            composerView
	// Error is a catalog key ("" none), ErrorDetail the rule code behind it.
	Error, ErrorDetail string
	CanCreate          bool
}

func newKind(r *http.Request) string {
	switch k := r.FormValue("kind"); k {
	case newKindIdea, newKindPain:
		return k
	}
	return newKindIssue
}

// issueNew is GET /ui/{org}/{repo}/issues/new.
func (a *app) issueNew(w http.ResponseWriter, r *http.Request, remote string) {
	q := r.URL.Query()
	q.Set("project", remote)
	r2 := r.Clone(r.Context())
	r2.URL.RawQuery = q.Encode()
	pa := a.access(r2)
	if _, stop := a.gateList(w, r2, pa, remote, store.ResRequest); stop {
		return
	}
	a.renderNewIssue(w, r2, remote, a.newIssueDefaults(r2, pa, remote), http.StatusOK)
}

func (a *app) newIssueDefaults(r *http.Request, pa *store.ProjectAccess, remote string) *newIssueView {
	l := a.langFor(r)
	kind := newKind(r)
	v := &newIssueView{Kind: kind, Project: remote, Base: repoHref(remote), Token: csrfOf(r), Role: requestdomain.RolePflicht, PackageChoice: "none"}
	switch kind {
	case newKindIdea:
		v.TitleKey, v.SubmitKey = "newissue.title_idea", "newissue.submit_idea"
	case newKindPain:
		v.TitleKey, v.SubmitKey = "newissue.title_pain", "newissue.submit_pain"
	default:
		v.TitleKey, v.SubmitKey, v.HasCriteria, v.HasPackage = "newissue.title_issue", "newissue.submit_issue", true, true
	}
	for _, t := range requestTypes {
		v.Types = append(v.Types, newTypeOption{Value: t, Label: requestTypeLabel(l, t), Selected: t == "feature"})
	}
	if labels, err := pa.ListLabels(remote); err == nil {
		for _, lb := range labels {
			v.Labels = append(v.Labels, newLabelOption{Name: lb.Name, Class: labelClass(lb.Color)})
		}
	}
	if v.HasPackage {
		if pkgs, err := pa.PackagesOfProject(remote, requestdomain.PackageOpen); err == nil {
			for _, p := range pkgs {
				v.Packages = append(v.Packages, newPackageOption{ID: p.ID, Label: packageName(l, p.Package)})
			}
		}
	}
	v.Criteria = make([]string, newCriteria)
	if v.HasCriteria {
		// A link from another page ("New sub-issue", "Make an issue of this") fills the origin.
		v.Parent, v.DerivedFrom = strings.TrimSpace(r.FormValue("parent")), strings.TrimSpace(r.FormValue("derived_from"))
	}
	return v
}

func (a *app) renderNewIssue(w http.ResponseWriter, r *http.Request, remote string, v *newIssueView, status int) {
	l := a.langFor(r)
	pa := a.access(r)
	view := a.repoViewFor(r, pa, a.shellBaseFor(r).who.kind, remote, "issues")
	v.Composer = composerView{Name: "body", Placeholder: l.msg("newissue.body_placeholder"), Value: v.Body, NoButtons: true}
	v.CanCreate = interactive(r)
	ctx := r.Context()
	r = r.WithContext(context.WithValue(ctx, repoCtxKey{}, view))
	w.WriteHeader(status)
	a.renderBrowser(w, r, "newissue", pageData{Title: l.msg(v.TitleKey), Project: remote, NewIssueV: v})
}

// issueCreate is POST /ui/issues/create: it makes the issue, idea or pain and
// leads to it, or back to the form for the next one.
func (a *app) issueCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	remote := scope.NormalizeRemote(strings.TrimSpace(r.FormValue("project")))
	pa := a.access(r)
	opts, _ := a.projectOptions(r, pa)
	if remote == "" || !slices.ContainsFunc(opts, func(o projectOption) bool { return o.Remote == remote }) {
		http.NotFound(w, r)
		return
	}
	v := a.newIssueDefaults(r, pa, remote)
	v.Title, _ = cleanText(r.FormValue("title"), 200)
	v.Body = strings.TrimSpace(r.FormValue("body"))
	v.Again = r.FormValue("again") == "1"
	v.PackageChoice = r.FormValue("package")
	v.PackageName, v.PackageBranch = strings.TrimSpace(r.FormValue("package_name")), strings.TrimSpace(r.FormValue("package_branch"))
	if r.FormValue("role") == requestdomain.RoleOptional {
		v.Role = requestdomain.RoleOptional
	}
	picked := map[string]bool{}
	for _, n := range r.Form["label"] {
		picked[n] = true
	}
	for i := range v.Labels {
		v.Labels[i].Checked = picked[v.Labels[i].Name]
	}
	chosenType := r.FormValue("type")
	for i := range v.Types {
		v.Types[i].Selected = v.Types[i].Value == chosenType
	}
	fail := func(key, detail string) {
		v.Error, v.ErrorDetail = key, detail
		a.renderNewIssue(w, r, remote, v, http.StatusUnprocessableEntity)
	}
	var criteria []string
	var links requestdomain.Links
	if v.HasCriteria {
		v.Parent, v.DerivedFrom = strings.TrimSpace(r.FormValue("parent")), strings.TrimSpace(r.FormValue("derived_from"))
		var err error
		if links, err = pa.ResolveRequestLinks(remote, v.Parent, strings.FieldsFunc(v.DerivedFrom, func(c rune) bool { return c == ',' || c == ' ' || c == '\n' || c == '\t' })); err != nil {
			a.newIssueOriginError(r, fail, err)
			return
		}
		for _, c := range r.Form["criterion"] {
			if c = strings.TrimSpace(c); c != "" && len(criteria) < maxCriteria {
				criteria = append(criteria, c)
			}
		}
		v.Criteria = append(slices.Clone(criteria), make([]string, max(0, newCriteria-len(criteria)))...)
	}
	if v.Title == "" {
		fail("newissue.err.title", "")
		return
	}
	if len(v.Body) > 20000 {
		fail("newissue.err.failed", "too long")
		return
	}
	person := personOf(r)
	accountID, _ := store.AccountNumericID(browserPrincipal(r).ID)
	var id int64
	var object, kind string
	switch v.Kind {
	case newKindIdea:
		kind, object = "idea", "ideas"
		i, err := pa.CreateIdea(store.CreateIdeaInput{Project: remote, Title: v.Title, Body: v.Body, Actor: store.IdeaActorFor(browserPrincipal(r), "")})
		if err != nil {
			a.newIssueError(fail, err)
			return
		}
		id = i.ID
		a.stampActor(r, actor.Idea, id)
	case newKindPain:
		kind, object = "pain", "pains"
		d, err := pa.CreatePain(store.CreatePainInput{Project: remote, Title: v.Title, Body: v.Body, Actor: pain.Actor{AccountID: accountID, Person: person}, AuthorID: accountID})
		if err != nil {
			a.newIssueError(fail, err)
			return
		}
		id = d.Pain.ID
		a.stampActor(r, actor.Pain, id)
	default:
		kind, object = "request", "issues"
		typ := chosenType
		if !slices.Contains(requestTypes, typ) {
			typ = "feature"
		}
		if err := pa.Check(remote, store.ResRequest, store.ActCreate, store.Object{Own: true}); err != nil {
			if a.accessDenied(w, r, err) {
				return
			}
		}
		d, err := a.store.CreateRequest(requestdomain.CreateInput{
			Request:  requestdomain.Request{Type: typ, Title: v.Title, Description: v.Body, Scope: scope.Axes{Project: remote}, Person: person},
			Criteria: criteria, AuthorID: accountID, Links: links,
		})
		if err != nil {
			a.newIssueError(fail, err)
			return
		}
		id = d.Request.ID
		a.stampActor(r, actor.Request, id)
		if errKey := a.newIssuePackage(pa, v, id, person); errKey != "" {
			// The issue exists; only the package failed. Lead to the issue and say so.
			http.Redirect(w, r, v.Base+"/issues/"+strconv.FormatInt(id, 10)+"?fail=package", http.StatusSeeOther)
			return
		}
	}
	if len(picked) > 0 {
		var names []string
		for n := range picked {
			names = append(names, n)
		}
		slices.Sort(names)
		_, _ = pa.SetLabels(kind, id, names, nil)
	}
	if v.Again {
		next := v.Base + "/issues/new"
		if v.Kind != newKindIssue {
			next += "?kind=" + url.QueryEscape(v.Kind)
		}
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, v.Base+"/"+object+"/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// newIssueError shows a refusal of the store on the form: an access refusal as
// the missing page it is, a rule as its code.
func (a *app) newIssueError(fail func(key, detail string), err error) {
	if code := requestdomain.Code(err); code != "" {
		fail("newissue.err.rule", code)
		return
	}
	fail("newissue.err.failed", "")
}

// newIssueOriginError shows what is wrong with the parent or the origin: a
// target that is not in this repository or not readable all read the same, a
// wrong spelling or too many sources say so.
func (a *app) newIssueOriginError(r *http.Request, fail func(key, detail string), err error) {
	switch requestdomain.Code(err) {
	case "origin_not_found", "link_target_not_found":
		fail("newissue.err.origin_missing", "")
	case "invalid_link":
		fail("newissue.err.origin_invalid", "")
	case "too_many_links":
		fail("newissue.err.origin_many", "")
	default:
		a.newIssueError(fail, err)
	}
}

// newIssuePackage puts the new issue into the chosen work package, or into a
// new one named on the form. "" is success.
func (a *app) newIssuePackage(pa *store.ProjectAccess, v *newIssueView, id int64, person string) string {
	member := []requestdomain.PackageMember{{RequestID: id, Role: v.Role}}
	switch v.PackageChoice {
	case "", "none":
		return ""
	case "new":
		if _, err := a.store.CreatePackage(requestdomain.PackageInput{Project: v.Project, Branch: v.PackageBranch, Name: v.PackageName, Members: member, Person: person}); err != nil {
			return "failed"
		}
		return ""
	}
	pkgID, err := strconv.ParseInt(v.PackageChoice, 10, 64)
	if err != nil || !slices.ContainsFunc(v.Packages, func(p newPackageOption) bool { return p.ID == pkgID }) {
		return "failed"
	}
	if _, err := a.store.AddPackageRequests(pkgID, member, "", person); err != nil {
		return "failed"
	}
	return ""
}
