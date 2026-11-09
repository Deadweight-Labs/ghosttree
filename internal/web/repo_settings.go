package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The settings of a repository (REQ-599, mock settings-repo.html): the labels
// of the project, with create, edit and delete, and where the visibility and
// the roles are managed. What a viewer may do is the store's rule (ResLabel):
// read from guest, create from member, edit and delete from lead. The tab only
// shows for those who may edit. Org labels are listed but belong to the
// organisation's owners.

type labelRow struct {
	ID           int64
	Name, Color  string
	Description  string
	Class        string
	Org, CanEdit bool
}

type repoSettingsView struct {
	Labels      []labelRow
	Project     string
	Token       string
	Back        string
	OrgSettings string
	Notice      string
	Error       string
	CanCreate   bool
	// NewColor is the colour a new label starts with.
	NewColor string
}

func (a *app) repoSettings(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	w.Header().Set("Cache-Control", "no-store")
	view := repoFrom(r)
	pa := a.access(r)
	if view == nil || view.Project == "" {
		http.NotFound(w, r)
		return
	}
	project := view.Project
	if pa.Check(project, store.ResLabel, store.ActEdit, store.Object{}) != nil {
		http.NotFound(w, r)
		return
	}
	labels, err := pa.ListLabels(project)
	if err != nil {
		a.accessDenied(w, r, err)
		return
	}
	v := &repoSettingsView{Project: project, Token: csrfOf(r), Back: repoHref(project) + "/settings", CanCreate: true, NewColor: "#0969da"}
	v.OrgSettings = view.OrgHref + "/settings"
	for _, lb := range labels {
		v.Labels = append(v.Labels, labelRow{ID: lb.ID, Name: lb.Name, Color: "#" + strings.TrimPrefix(lb.Color, "#"), Description: lb.Description, Class: labelClass(lb.Color),
			Org: lb.Scope == "org", CanEdit: lb.Scope != "org"})
	}
	q := r.URL.Query()
	switch {
	case q.Get("fail") != "":
		v.Error = l.msg(labelFailKey(q.Get("fail")))
	case q.Get("done") != "":
		v.Notice = label(l, labelDoneKeys, q.Get("done"))
	}
	a.renderBrowser(w, r, "reposettings", pageData{Title: l.msg("repo.tab.settings"), SettingsV: v})
}

var labelDoneKeys = map[string]string{"created": "settings.done.created", "updated": "settings.done.updated", "deleted": "settings.done.deleted"}

// labelFailKey maps the fail= value of a redirect to its message.
func labelFailKey(code string) string {
	switch code {
	case "label_exists":
		return "settings.fail.label_exists"
	case "forbidden":
		return "settings.fail.forbidden"
	}
	return "settings.fail.label"
}

// labelForm reads the label form and the project it belongs to; the project
// is the one of the page the form came from.
func labelForm(r *http.Request) (project string, in store.LabelInput) {
	project = scope.NormalizeRemote(r.FormValue("project"))
	color := strings.TrimSpace(r.FormValue("color"))
	if color != "" && !strings.HasPrefix(color, "#") {
		color = "#" + color
	}
	return project, store.LabelInput{Project: project, Name: strings.TrimSpace(r.FormValue("name")), Color: color, Description: strings.TrimSpace(r.FormValue("description"))}
}

func (a *app) labelRedirect(w http.ResponseWriter, r *http.Request, project, key, value string) {
	http.Redirect(w, r, repoHref(project)+"/settings?"+key+"="+url.QueryEscape(value)+"#labels", http.StatusSeeOther)
}

func (a *app) labelResult(w http.ResponseWriter, r *http.Request, project string, err error, done string) {
	switch {
	case err == nil:
		a.labelRedirect(w, r, project, "done", done)
	case requestdomain.Code(err) != "":
		a.labelRedirect(w, r, project, "fail", requestdomain.Code(err))
	case err == store.ErrAccessForbidden:
		a.labelRedirect(w, r, project, "fail", "forbidden")
	case err == store.ErrAccessNotFound:
		http.NotFound(w, r)
	default:
		a.labelRedirect(w, r, project, "fail", "label")
	}
}

func (a *app) labelCreate(w http.ResponseWriter, r *http.Request) {
	project, in := labelForm(r)
	if project == "" {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	_, err := a.access(r).CreateLabel(in)
	a.labelResult(w, r, project, err, "created")
}

func (a *app) labelUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	project, in := labelForm(r)
	if err != nil || project == "" {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	_, err = a.access(r).UpdateLabel(id, store.LabelPatch{Name: &in.Name, Color: &in.Color, Description: &in.Description})
	a.labelResult(w, r, project, err, "updated")
}

func (a *app) labelDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	project := scope.NormalizeRemote(r.FormValue("project"))
	if err != nil || project == "" {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	a.labelResult(w, r, project, a.access(r).DeleteLabel(id), "deleted")
}
