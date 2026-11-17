package web

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/secretbox"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The settings "AI connections" (REQ-694): the connections of an
// organization or profile with their aliases, caps and costs, the loans of a
// profile, and for the instance admin the server's connections and their
// release. A key goes in through a password field, is sealed at once and is
// never written to a page again; only its last four characters are.

// WithAIConnections gives the settings the box that seals keys; nil means the
// server has no secret key and no key can be stored.
func WithAIConnections(box *secretbox.Box) Option { return func(a *app) { a.aiBox = box } }

type aiConnRow struct {
	ID                           int64
	Name, Format, BaseURL, Last4 string
	Via                          string
	FromFile                     bool
	Models                       []store.AIModel
	ModelsText, Cap, Spent       string
	Loans                        []aiLoanRow
	Lendable                     bool
}

type aiLoanRow struct {
	ID         int64
	Org, Spent string
}

type aiAliasRow struct {
	ID                              int64
	Alias, Where, Connection, Model string
	Editable                        bool
}

type aiChoice struct {
	Value, Label string
	On           bool
}

type aiCostTable struct {
	Head string
	Rows []aiCostLine
}

type aiCostLine struct {
	Key, Href, Sub string
	Calls, Tokens  int
	Cost           string
}

type aiCapRow struct {
	Workflow, Month, Run string
}

// aiView is the section of one namespace.
type aiView struct {
	Owner, IsUser, CanSeal bool
	Conns                  []aiConnRow
	Usable                 []aiConnRow
	ModelNames             []string
	Aliases                []aiAliasRow
	Wheres                 []aiChoice
	OwnerMonth, OwnerRun   string
	WorkflowCaps           []aiCapRow
	Costs                  []aiCostTable
	CostTotal              string
	LendTo                 []store.Org
}

type aiGrantRow struct {
	OrgID      int64
	Slug, Name string
	Cap, Spent string
}

// aiServerView is the admin's section.
type aiServerView struct {
	CanSeal    bool
	Conns      []aiConnRow
	Modes      []aiChoice
	Selected   bool
	Grants     []aiGrantRow
	Orgs       []store.Org
	Aliases    []aiAliasRow
	ModelNames []string
}

// aiUSD shows an amount; below a cent it keeps four places, so a cheap call
// does not read as free.
func aiUSD(usd float64) string {
	if usd > 0 && usd < 0.01 {
		return fmt.Sprintf("%.4f $", usd)
	}
	return fmt.Sprintf("%.2f $", usd)
}

func aiCapText(usd float64) string {
	if usd <= 0 {
		return ""
	}
	return strconv.FormatFloat(usd, 'f', -1, 64)
}

func aiSpentLine(l lang, spent, capUSD float64) string {
	if capUSD > 0 {
		return l.msg("ai.spent", aiUSD(spent), aiUSD(capUSD))
	}
	return l.msg("ai.spent_nocap", aiUSD(spent))
}

func aiModelsText(ms []store.AIModel) string {
	var b strings.Builder
	for _, m := range ms {
		fmt.Fprintf(&b, "%s %s %s\n", m.Model, strconv.FormatFloat(m.InUSD, 'f', -1, 64), strconv.FormatFloat(m.OutUSD, 'f', -1, 64))
	}
	return b.String()
}

func aiRow(l lang, c store.AIConnection, details bool) aiConnRow {
	row := aiConnRow{ID: c.ID, Name: c.Name, Via: c.Via, FromFile: c.FromFile}
	switch c.Via {
	case store.AIViaLoan:
		row.Spent = aiSpentLine(l, c.SpentMonth, c.LoanCapUSD)
		return row
	case store.AIViaServer:
		row.Models = c.Models
		row.Spent = aiSpentLine(l, c.SpentMonth, 0)
		if !details {
			return row
		}
	}
	row.Format, row.BaseURL, row.Last4, row.Models = c.Format, c.BaseURL, c.KeyLast4, c.Models
	row.ModelsText, row.Cap = aiModelsText(c.Models), aiCapText(c.MonthlyCapUSD)
	row.Spent = aiSpentLine(l, c.SpentMonth, c.MonthlyCapUSD)
	return row
}

func modelNames(conns []aiConnRow) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range conns {
		for _, m := range c.Models {
			if !seen[m.Model] {
				seen[m.Model] = true
				out = append(out, m.Model)
			}
		}
	}
	sort.Strings(out)
	return out
}

func aliasWhere(l lang, a store.AIAlias, orgName string) string {
	switch a.Level {
	case store.AILevelServer:
		return l.msg("ai.level.server")
	case store.AILevelRepo:
		return lastSegment(a.Project)
	}
	return orgName
}

// aiViewFor builds the section of a namespace for its members; owners get
// the forms and the details of their own connections.
func (a *app) aiViewFor(r *http.Request, org store.Org) *aiView {
	if org.ID == 0 || org.Guest {
		return nil
	}
	l := a.langFor(r)
	owner := org.Role == store.OrgOwner
	v := &aiView{Owner: owner && interactive(r), IsUser: org.Kind == store.OrgKindUser, CanSeal: a.aiBox != nil}
	conns, _ := a.store.AIConnectionsFor(org.ID)
	for _, c := range conns {
		row := aiRow(l, c, false)
		if c.Via == store.AIViaOwn && !owner {
			// Members see what there is and what it cost, not where it goes.
			row.BaseURL, row.Last4, row.ModelsText = "", "", ""
		}
		if c.Via == store.AIViaOwn && v.IsUser && owner {
			row.Lendable = true
			loans, _ := a.store.AILoansOf(c.ID)
			for _, ln := range loans {
				row.Loans = append(row.Loans, aiLoanRow{ID: ln.ID, Org: ln.OrgName, Spent: aiSpentLine(l, ln.SpentMonth, ln.CapUSD)})
			}
		}
		v.Conns = append(v.Conns, row)
	}
	v.Usable = v.Conns
	var named []aiConnRow
	for _, c := range v.Conns {
		if c.Via != store.AIViaLoan {
			named = append(named, c)
		}
	}
	v.ModelNames = modelNames(named)
	aliases, _ := a.store.AIAliasesFor(org.ID)
	for _, al := range aliases {
		v.Aliases = append(v.Aliases, aiAliasRow{ID: al.ID, Alias: al.Alias, Where: aliasWhere(l, al, org.Name), Connection: al.Connection, Model: al.Model,
			Editable: owner && al.Level != store.AILevelServer})
	}
	v.Wheres = append(v.Wheres, aiChoice{Value: store.AILevelOwner, Label: org.Name, On: true})
	for _, p := range a.orgProjects(r, org) {
		v.Wheres = append(v.Wheres, aiChoice{Value: "repo:" + p.Remote, Label: lastSegment(p.Remote)})
	}
	caps, _ := a.store.AICapsOf(org.ID)
	for i, c := range caps {
		if i == 0 {
			v.OwnerMonth, v.OwnerRun = aiCapText(c.MonthUSD), aiCapText(c.RunUSD)
			continue
		}
		v.WorkflowCaps = append(v.WorkflowCaps, aiCapRow{Workflow: c.Workflow, Month: aiCapText(c.MonthUSD), Run: aiCapText(c.RunUSD)})
	}
	if rep, err := a.store.AICostReportOf(org.ID, store.AIMonthStart()); err == nil && rep.Calls > 0 {
		v.CostTotal = l.msg("ai.costs.total", aiUSD(rep.TotalUSD), rep.Calls)
		if rep.Calls == 1 {
			v.CostTotal = l.msg("ai.costs.total_one", aiUSD(rep.TotalUSD))
		}
		line := func(row store.AICostRow) aiCostLine {
			return aiCostLine{Key: row.Key, Calls: row.Calls, Tokens: row.Tokens, Cost: aiUSD(row.CostUSD)}
		}
		var runs []aiCostLine
		for _, row := range rep.ByRun {
			c := line(row)
			c.Key = "#" + strconv.FormatInt(row.RunID, 10)
			c.Sub = row.Key
			c.Href = workflowRunHref(repoHref(row.Project), row.RunID)
			runs = append(runs, c)
		}
		for _, t := range []struct {
			head string
			rows []store.AICostRow
		}{{"ai.costs.workflow", rep.ByWorkflow}, {"ai.costs.connection", rep.ByConnection}, {"ai.costs.model", rep.ByModel}} {
			tab := aiCostTable{Head: l.msg(t.head)}
			for _, row := range t.rows {
				tab.Rows = append(tab.Rows, line(row))
			}
			v.Costs = append(v.Costs, tab)
		}
		v.Costs = append(v.Costs, aiCostTable{Head: l.msg("ai.costs.run"), Rows: runs})
	}
	if v.IsUser && owner {
		orgs, _ := a.store.ListOrgs(browserPrincipal(r).ID)
		for _, o := range orgs {
			if o.Kind != store.OrgKindUser && !o.Guest {
				v.LendTo = append(v.LendTo, o)
			}
		}
	}
	return v
}

// aiServerFor is the admin's section; nil for anyone else.
func (a *app) aiServerFor(r *http.Request) *aiServerView {
	if !interactive(r) || !a.isAdmin(browserPrincipal(r)) {
		return nil
	}
	l := a.langFor(r)
	v := &aiServerView{CanSeal: a.aiBox != nil}
	conns, _ := a.store.ServerAIConnections()
	for _, c := range conns {
		v.Conns = append(v.Conns, aiRow(l, c, true))
	}
	v.ModelNames = modelNames(v.Conns)
	mode, grants, _ := a.store.AIRelease()
	for _, m := range []struct{ value, key string }{{store.AIReleaseAll, "ai.release.all"}, {store.AIReleaseSelected, "ai.release.selected"}, {store.AIReleaseNone, "ai.release.none"}} {
		v.Modes = append(v.Modes, aiChoice{Value: m.value, Label: l.msg(m.key), On: m.value == mode})
	}
	v.Selected = mode == store.AIReleaseSelected
	for _, g := range grants {
		v.Grants = append(v.Grants, aiGrantRow{OrgID: g.OrgID, Slug: g.Slug, Name: g.Name, Cap: aiCapText(g.CapUSD), Spent: aiSpentLine(l, g.SpentMonth, g.CapUSD)})
	}
	v.Orgs, _ = a.store.AllOwners()
	aliases, _ := a.store.ServerAIAliases()
	for _, al := range aliases {
		v.Aliases = append(v.Aliases, aiAliasRow{ID: al.ID, Alias: al.Alias, Where: l.msg("ai.level.server"), Connection: al.Connection, Model: al.Model, Editable: true})
	}
	return v
}

// ---------------------------------------------------------------- forms

var errAIModels = fmt.Errorf("%w: models", store.ErrInvalidInput)
var errAIAddress = fmt.Errorf("%w: address", store.ErrInvalidInput)

// parseAIModels reads one model per line: name, input price, output price
// (USD per million tokens), separated by spaces, tabs or semicolons; a comma
// may be the decimal mark.
func parseAIModels(text string) ([]store.AIModel, error) {
	var out []store.AIModel
	for _, line := range strings.Split(text, "\n") {
		f := strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == '\t' || r == ';' || r == '\r' })
		if len(f) == 0 {
			continue
		}
		if len(f) != 3 {
			return nil, errAIModels
		}
		in, err1 := strconv.ParseFloat(strings.ReplaceAll(f[1], ",", "."), 64)
		outp, err2 := strconv.ParseFloat(strings.ReplaceAll(f[2], ",", "."), 64)
		if err1 != nil || err2 != nil {
			return nil, errAIModels
		}
		out = append(out, store.AIModel{Model: f[0], InUSD: in, OutUSD: outp})
	}
	if len(out) == 0 {
		return nil, errAIModels
	}
	return out, nil
}

func formUSD(r *http.Request, name string) (float64, error) {
	s := strings.ReplaceAll(strings.TrimSpace(r.FormValue(name)), ",", ".")
	if s == "" {
		return 0, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0, store.ErrAICap
	}
	return v, nil
}

func formID(r *http.Request, name string) int64 {
	id, _ := strconv.ParseInt(r.FormValue(name), 10, 64)
	return id
}

// aiForm resolves the namespace of a form, and whether it is about the
// server's connections (scope=server, admin only; the namespace is then only
// where the page returns to).
func (a *app) aiForm(w http.ResponseWriter, r *http.Request) (store.Org, bool, bool) {
	o, err := a.formOrg(r)
	if err != nil {
		a.orgFailure(w, r, err)
		return o, false, false
	}
	server := r.FormValue("scope") == "server"
	if server && !a.isAdmin(browserPrincipal(r)) {
		a.orgFailure(w, r, store.ErrNotServerAdmin)
		return o, false, false
	}
	return o, server, true
}

func aiNotice(server bool) string {
	if server {
		return "ai_server"
	}
	return "ai"
}

// aiConnectionSave creates or changes a connection (POST /ui/orgs/ai/connection).
func (a *app) aiConnectionSave(w http.ResponseWriter, r *http.Request) {
	o, server, ok := a.aiForm(w, r)
	if !ok {
		return
	}
	models, err := parseAIModels(r.FormValue("models"))
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	capUSD, err := formUSD(r, "cap")
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	in := store.AIConnectionInput{ID: formID(r, "id"), OrgID: o.ID, Name: r.FormValue("name"), Format: r.FormValue("format"),
		BaseURL: strings.TrimSpace(r.FormValue("base_url")), MonthlyCapUSD: capUSD, Models: models}
	if server {
		in.OrgID = 0
		if in.BaseURL != "" {
			if err := llm.CheckBaseURL("connection", in.BaseURL); err != nil {
				a.orgFailure(w, r, errAIAddress)
				return
			}
		}
	}
	if key := strings.TrimSpace(r.FormValue("key")); key != "" {
		if a.aiBox == nil {
			a.orgFailure(w, r, secretbox.ErrNoKey)
			return
		}
		sealed, err := a.aiBox.Seal(key, store.AIKeyPurpose)
		if err != nil {
			a.orgFailure(w, r, err)
			return
		}
		in.KeySealed, in.KeyLast4 = sealed, secretbox.Last4(key)
	}
	if in.ID != 0 && !a.aiConnectionIn(in.ID, in.OrgID) {
		a.orgFailure(w, r, store.ErrAIUnavailable)
		return
	}
	if _, err := a.store.SaveAIConnection(browserPrincipal(r).ID, in); err != nil {
		a.orgFailure(w, r, err)
		return
	}
	orgRedirect(w, r, o.Slug, aiNotice(server))
}

// aiConnectionIn says whether a connection belongs to the namespace (0: the
// server) the form names, so that a form of one namespace cannot change
// another's connection.
func (a *app) aiConnectionIn(id, orgID int64) bool {
	if orgID == 0 {
		conns, _ := a.store.ServerAIConnections()
		for _, c := range conns {
			if c.ID == id {
				return true
			}
		}
		return false
	}
	conns, _ := a.store.AIConnectionsFor(orgID)
	for _, c := range conns {
		if c.ID == id && c.Via == store.AIViaOwn {
			return true
		}
	}
	return false
}

func (a *app) aiConnectionDelete(w http.ResponseWriter, r *http.Request) {
	o, server, ok := a.aiForm(w, r)
	if !ok {
		return
	}
	id, orgID := formID(r, "id"), o.ID
	if server {
		orgID = 0
	}
	if !a.aiConnectionIn(id, orgID) {
		a.orgFailure(w, r, store.ErrAIUnavailable)
		return
	}
	if err := a.store.DeleteAIConnection(browserPrincipal(r).ID, id); err != nil {
		a.orgFailure(w, r, err)
		return
	}
	orgRedirect(w, r, o.Slug, aiNotice(server))
}

func (a *app) aiAliasSave(w http.ResponseWriter, r *http.Request) {
	o, server, ok := a.aiForm(w, r)
	if !ok {
		return
	}
	in := store.AIAlias{Level: store.AILevelOwner, OrgID: o.ID, Alias: r.FormValue("alias"), ConnectionID: formID(r, "connection"), Model: r.FormValue("model")}
	if server {
		in.Level, in.OrgID = store.AILevelServer, 0
	} else if repo, ok := strings.CutPrefix(r.FormValue("where"), "repo:"); ok {
		in.Level, in.Project = store.AILevelRepo, repo
	}
	if _, err := a.store.SetAIAlias(browserPrincipal(r).ID, in); err != nil {
		a.orgFailure(w, r, err)
		return
	}
	orgRedirect(w, r, o.Slug, aiNotice(server))
}

func (a *app) aiAliasDelete(w http.ResponseWriter, r *http.Request) {
	o, server, ok := a.aiForm(w, r)
	if !ok {
		return
	}
	id := formID(r, "id")
	var aliases []store.AIAlias
	if server {
		aliases, _ = a.store.ServerAIAliases()
	} else {
		aliases, _ = a.store.AIAliasesFor(o.ID)
	}
	found := false
	for _, al := range aliases {
		found = found || (al.ID == id && (server || al.Level != store.AILevelServer))
	}
	if !found {
		a.orgFailure(w, r, store.ErrAIUnavailable)
		return
	}
	if err := a.store.DeleteAIAlias(browserPrincipal(r).ID, id); err != nil {
		a.orgFailure(w, r, err)
		return
	}
	orgRedirect(w, r, o.Slug, aiNotice(server))
}

func (a *app) aiCapSave(w http.ResponseWriter, r *http.Request) {
	o, _, ok := a.aiForm(w, r)
	if !ok {
		return
	}
	month, err := formUSD(r, "month")
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	run, err := formUSD(r, "run")
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	if err := a.store.SetAICap(browserPrincipal(r).ID, store.AICap{OrgID: o.ID, Workflow: r.FormValue("workflow"), MonthUSD: month, RunUSD: run}); err != nil {
		a.orgFailure(w, r, err)
		return
	}
	orgRedirect(w, r, o.Slug, "ai")
}

func (a *app) aiLend(w http.ResponseWriter, r *http.Request) {
	o, _, ok := a.aiForm(w, r)
	if !ok {
		return
	}
	to, err := a.store.OrgByRef(r.FormValue("to"))
	if err != nil {
		a.orgFailure(w, r, store.ErrOrgNotFound)
		return
	}
	capUSD, err := formUSD(r, "cap")
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	id := formID(r, "id")
	if !a.aiConnectionIn(id, o.ID) {
		a.orgFailure(w, r, store.ErrAIUnavailable)
		return
	}
	if _, err := a.store.LendAIConnection(browserPrincipal(r).ID, id, to.ID, capUSD); err != nil {
		a.orgFailure(w, r, err)
		return
	}
	orgRedirect(w, r, o.Slug, "ai")
}

func (a *app) aiLoanRevoke(w http.ResponseWriter, r *http.Request) {
	o, _, ok := a.aiForm(w, r)
	if !ok {
		return
	}
	if err := a.store.RevokeAILoan(browserPrincipal(r).ID, formID(r, "id")); err != nil {
		a.orgFailure(w, r, err)
		return
	}
	orgRedirect(w, r, o.Slug, "ai")
}

func (a *app) aiReleaseSave(w http.ResponseWriter, r *http.Request) {
	o, _, ok := a.aiForm(w, r)
	if !ok {
		return
	}
	if err := a.store.SetAIRelease(browserPrincipal(r).ID, r.FormValue("mode")); err != nil {
		a.orgFailure(w, r, err)
		return
	}
	orgRedirect(w, r, o.Slug, "ai_server")
}

func (a *app) aiGrantSave(w http.ResponseWriter, r *http.Request) {
	o, _, ok := a.aiForm(w, r)
	if !ok {
		return
	}
	target, err := a.store.OrgByRef(r.FormValue("target"))
	if err != nil {
		a.orgFailure(w, r, store.ErrOrgNotFound)
		return
	}
	capUSD, err := formUSD(r, "cap")
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	if r.FormValue("remove") == "1" {
		err = a.store.DeleteAIGrant(browserPrincipal(r).ID, target.ID)
	} else {
		err = a.store.SetAIGrant(browserPrincipal(r).ID, target.ID, capUSD)
	}
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	orgRedirect(w, r, o.Slug, "ai_server")
}

// aiError words the refusals of this section; ok is false for other errors.
func aiError(l lang, err error) (int, string, bool) {
	switch {
	case errors.Is(err, secretbox.ErrNoKey):
		return http.StatusConflict, l.msg("ai.err.no_secret"), true
	case errors.Is(err, store.ErrAINameTaken):
		return http.StatusConflict, l.msg("ai.err.name_taken"), true
	case errors.Is(err, store.ErrAIFromFile):
		return http.StatusConflict, l.msg("ai.err.from_file"), true
	case errors.Is(err, store.ErrNotServerAdmin):
		return http.StatusForbidden, l.msg("ai.err.server_admin"), true
	case errors.Is(err, store.ErrAIUnavailable):
		return http.StatusBadRequest, l.msg("ai.err.unavailable"), true
	case errors.Is(err, store.ErrAIKeyMissing):
		return http.StatusBadRequest, l.msg("ai.err.key"), true
	case errors.Is(err, errAIAddress):
		return http.StatusBadRequest, l.msg("ai.err.address"), true
	case errors.Is(err, errAIModels):
		return http.StatusBadRequest, l.msg("ai.err.models"), true
	}
	return 0, "", false
}

// aiPOST lists the forms of the section, for the router.
func (a *app) aiPOST() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"/ui/orgs/ai/connection": a.aiConnectionSave, "/ui/orgs/ai/connection/delete": a.aiConnectionDelete,
		"/ui/orgs/ai/alias": a.aiAliasSave, "/ui/orgs/ai/alias/delete": a.aiAliasDelete,
		"/ui/orgs/ai/cap": a.aiCapSave, "/ui/orgs/ai/lend": a.aiLend, "/ui/orgs/ai/loan/revoke": a.aiLoanRevoke,
		"/ui/orgs/ai/release": a.aiReleaseSave, "/ui/orgs/ai/grant": a.aiGrantSave,
	}
}
