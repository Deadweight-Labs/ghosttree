package web

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

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
	// Release is, on a connection the server released to the organization, the
	// cap of that release.
	Release string
	// Form is the form that edits the connection (or the refusal of it); Test the
	// last test, for who may test.
	Form aiFormView
	Test *aiTestView
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
	// NewForm is the form for a new connection; ServerRelease says what the
	// server released to the organization, with its cap.
	NewForm       aiFormView
	ServerRelease string
	// CanTest: the server can test a connection (the button is offered).
	CanTest bool
}

type aiGrantRow struct {
	OrgID      int64
	Slug, Name string
	// Cap is the cap the row has of its own (the field); Effective the cap that
	// applies, the default included; Spent the month so far against it.
	Cap, Effective, Spent string
}

// aiOrgChoice is an organization the release can still be given to.
type aiOrgChoice struct{ Slug, Label string }

// aiServerView is the admin's section.
type aiServerView struct {
	CanSeal    bool
	Conns      []aiConnRow
	Modes      []aiChoice
	Mode       string
	Selected   bool
	DefaultCap string
	Grants     []aiGrantRow
	Orgs       []aiOrgChoice
	Aliases    []aiAliasRow
	ModelNames []string
	NewForm    aiFormView
	CanTest    bool
}

func aiCapText(usd float64) string {
	if usd <= 0 {
		return ""
	}
	return strconv.FormatFloat(usd, 'f', -1, 64)
}

func aiSpentLine(l lang, spent, capUSD float64) string {
	if capUSD > 0 {
		return l.msg("ai.spent", wfCost(l, spent), wfCost(l, capUSD))
	}
	return l.msg("ai.spent_nocap", wfCost(l, spent))
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
		row.BaseURL = c.Host
		return row
	case store.AIViaServer:
		row.Models = c.Models
		row.Spent = aiSpentLine(l, c.SpentMonth, 0)
		if !details {
			row.Release = l.msg("ai.released_nocap")
			if c.ReleaseCapUSD > 0 {
				row.Release = l.msg("ai.released_cap", wfCost(l, c.ReleaseCapUSD))
			}
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
func (a *app) aiViewFor(r *http.Request, org store.Org, st *aiFormView) *aiView {
	if org.ID == 0 || org.Guest {
		return nil
	}
	l := a.langFor(r)
	owner := store.OrgRoleHas(org.Role, store.RightOrgAI)
	v := &aiView{Owner: orgMay(r, org, store.RightOrgAI), IsUser: org.Kind == store.OrgKindUser, CanSeal: a.aiBox != nil, CanTest: a.aiTester != nil}
	conns, _ := a.store.AIConnectionsFor(org.ID)
	for _, c := range conns {
		row := aiRow(l, c, false)
		if (c.Via == store.AIViaOwn || c.Via == store.AIViaLoan) && !owner {
			// Members see what there is and what it cost, not where it goes;
			// owners see the host a lent connection sends the calls to.
			row.BaseURL, row.Last4, row.ModelsText = "", "", ""
		}
		if c.Via == store.AIViaOwn && v.IsUser && owner {
			row.Lendable = true
			loans, _ := a.store.AILoansOf(c.ID)
			for _, ln := range loans {
				row.Loans = append(row.Loans, aiLoanRow{ID: ln.ID, Org: ln.OrgName, Spent: aiSpentLine(l, ln.SpentMonth, ln.CapUSD)})
			}
		}
		if v.Owner && c.Via == store.AIViaOwn {
			row.Form = aiFormOf(row, false, st)
			row.Test = a.aiTestOf(c.ID, l)
		}
		if c.Via == store.AIViaServer && v.ServerRelease == "" {
			v.ServerRelease = l.msg("ai.cap.none")
			if c.ReleaseCapUSD > 0 {
				v.ServerRelease = l.msg("ai.released.per_month", wfCost(l, c.ReleaseCapUSD))
			}
		}
		v.Conns = append(v.Conns, row)
	}
	v.NewForm = aiNewForm(false, st)
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
		v.CostTotal = l.msg("ai.costs.total", wfCost(l, rep.TotalUSD), rep.Calls)
		if rep.Calls == 1 {
			v.CostTotal = l.msg("ai.costs.total_one", wfCost(l, rep.TotalUSD))
		}
		line := func(row store.AICostRow) aiCostLine {
			if row.Key == store.AITestWorkflow {
				row.Key = l.msg("ai.test.workflow")
			}
			return aiCostLine{Key: row.Key, Calls: row.Calls, Tokens: row.Tokens, Cost: wfCost(l, row.CostUSD)}
		}
		var runs []aiCostLine
		for _, row := range rep.ByRun {
			if row.RunID == 0 {
				// Tests of connections have no run to link to.
				continue
			}
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
		if len(runs) > 0 {
			v.Costs = append(v.Costs, aiCostTable{Head: l.msg("ai.costs.run"), Rows: runs})
		}
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
func (a *app) aiServerFor(r *http.Request, st *aiFormView) *aiServerView {
	if !a.access(r).CanInstance(store.RightInstanceAI) {
		return nil
	}
	l := a.langFor(r)
	v := &aiServerView{CanSeal: a.aiBox != nil, CanTest: a.aiTester != nil}
	conns, _ := a.store.ServerAIConnections()
	for _, c := range conns {
		row := aiRow(l, c, true)
		row.Form = aiFormOf(row, true, st)
		row.Test = a.aiTestOf(c.ID, l)
		v.Conns = append(v.Conns, row)
	}
	v.NewForm = aiNewForm(true, st)
	v.ModelNames = modelNames(v.Conns)
	mode, grants, _ := a.store.AIRelease()
	v.Mode = mode
	for _, m := range []struct{ value, key string }{{store.AIReleaseAll, "ai.release.all"}, {store.AIReleaseSelected, "ai.release.selected"}, {store.AIReleaseNone, "ai.release.none"}} {
		v.Modes = append(v.Modes, aiChoice{Value: m.value, Label: l.msg(m.key), On: m.value == mode})
	}
	v.Selected = mode == store.AIReleaseSelected
	def := a.store.AIReleaseDefaultCap()
	v.DefaultCap = aiCapText(def)
	granted := map[int64]bool{}
	for _, g := range grants {
		granted[g.OrgID] = true
		// One cap per organization: its own when it has one, else the default.
		eff := g.CapUSD
		if eff <= 0 {
			eff = def
		}
		v.Grants = append(v.Grants, aiGrantRow{OrgID: g.OrgID, Slug: g.Slug, Name: g.Name, Cap: aiCapText(g.CapUSD), Effective: wfCost(l, eff), Spent: aiSpentLine(l, g.SpentMonth, eff)})
	}
	all, _ := a.store.AllOwners()
	for _, o := range all {
		if !granted[o.ID] {
			label := o.Name
			if !strings.EqualFold(o.Name, o.Slug) {
				label += " (" + o.Slug + ")"
			}
			v.Orgs = append(v.Orgs, aiOrgChoice{Slug: o.Slug, Label: label})
		}
	}
	aliases, _ := a.store.ServerAIAliases()
	for _, al := range aliases {
		v.Aliases = append(v.Aliases, aiAliasRow{ID: al.ID, Alias: al.Alias, Where: l.msg("ai.level.server"), Connection: al.Connection, Model: al.Model, Editable: true})
	}
	return v
}

// ---------------------------------------------------------------- forms

var errAIModels = fmt.Errorf("%w: models", store.ErrInvalidInput)
var errAIAddress = fmt.Errorf("%w: address", store.ErrInvalidInput)
var errAIReleaseCap = fmt.Errorf("%w: release to all needs a cap", store.ErrInvalidInput)

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
		a.aiFailure(w, r, true, store.ErrNotServerAdmin)
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
// A form that does not pass comes back on the same page, open, with what was
// typed (the key excepted) and the reason at each field.
func (a *app) aiConnectionSave(w http.ResponseWriter, r *http.Request) {
	o, server, ok := a.aiForm(w, r)
	if !ok {
		return
	}
	l := a.langFor(r)
	if !server && !a.access(r).CanOrg(o.ID, store.RightOrgAI) {
		a.aiFailure(w, r, server, store.ErrNotOrgOwner)
		return
	}
	f := aiFormView{ID: formID(r, "id"), Server: server, Name: strings.TrimSpace(r.FormValue("name")), Format: r.FormValue("format"),
		BaseURL: strings.TrimSpace(r.FormValue("base_url")), Cap: strings.TrimSpace(r.FormValue("cap")), Errors: map[string]string{}}
	key := strings.TrimSpace(r.FormValue("key"))
	f.KeyAgain = key != ""
	orgID := o.ID
	if server {
		orgID = 0
	}
	var old store.AIConnection
	if f.ID != 0 {
		var found bool
		if old, found = a.aiConnectionOf(f.ID, orgID); !found {
			a.aiFailure(w, r, server, store.ErrAIUnavailable)
			return
		}
		f.FromFile, f.Last4 = old.FromFile, old.KeyLast4
	}
	if old.FromFile {
		// Name, format, address and key are the file's.
		f.Name, f.Format, f.BaseURL = old.Name, old.Format, old.BaseURL
	} else {
		f.Errors["name"] = aiNameProblem(l, f.Name)
		if f.Format != store.AIFormatAnthropic && f.Format != store.AIFormatOpenAI {
			f.Errors["form"] = l.msg("ai.f.format")
		}
		f.Errors["base_url"] = checkAIAddress(l, f.BaseURL, server)
	}
	models, rows, problem := parseAIModelRows(r, l)
	f.Models, f.Errors["models"] = rows, problem
	capUSD, capProblem := aiCapProblem(l, r, "cap")
	f.Errors["cap"] = capProblem
	switch {
	case old.FromFile:
	case f.ID == 0 && key == "":
		f.Errors["key"] = l.msg("ai.f.key.missing")
	case f.ID != 0 && key == "" && (f.BaseURL != old.BaseURL || f.Format != old.Format):
		// The stored key never goes to an address it was not entered for.
		f.Errors["key"] = l.msg("ai.f.key.moved")
	}
	if key != "" && !old.FromFile && a.aiBox == nil {
		f.Errors["form"] = l.msg("ai.err.no_secret")
	}
	if f.KeyAgain && f.Errors["key"] == "" && len(nonEmpty(f.Errors)) > 0 {
		f.Errors["key"] = l.msg("ai.f.key.again")
	}
	if len(nonEmpty(f.Errors)) > 0 {
		a.aiFormFail(w, r, o, f, http.StatusBadRequest)
		return
	}
	in := store.AIConnectionInput{ID: f.ID, OrgID: orgID, Name: f.Name, Format: f.Format, BaseURL: f.BaseURL, MonthlyCapUSD: capUSD, Models: models}
	var seal store.AIKeySealer
	if key != "" && !old.FromFile {
		box := a.aiBox
		seal = func(purpose string) ([]byte, error) { return box.Seal(key, purpose) }
		in.KeyLast4 = secretbox.Last4(key)
	}
	if _, err := a.store.SaveAIConnection(browserPrincipal(r).ID, in, seal); err != nil {
		if field, msg, status, ok := aiStoreFieldError(l, err); ok {
			f.Errors[field] = msg
			if field != "key" && f.KeyAgain {
				f.Errors["key"] = l.msg("ai.f.key.again")
			}
			a.aiFormFail(w, r, o, f, status)
			return
		}
		a.aiFailure(w, r, server, err)
		return
	}
	orgRedirect(w, r, o.Slug, aiNotice(server))
}

// aiFailure shows a refusal of the section as the settings page, opened at
// the section and not at "General", with the reason on top.
func (a *app) aiFailure(w http.ResponseWriter, r *http.Request, server bool, err error) {
	status, msg := orgError(a.langFor(r), err)
	open := "ai"
	if server {
		open = "ai-server"
	}
	a.renderOrgs(w, r, status, orgsView{Open: open}, r.FormValue("org"), msg)
}

// aiConnectionOf finds a connection of the namespace (0: the server) a form
// names, so that a form of one namespace cannot change another's connection.
func (a *app) aiConnectionOf(id, orgID int64) (store.AIConnection, bool) {
	if orgID == 0 {
		conns, _ := a.store.ServerAIConnections()
		for _, c := range conns {
			if c.ID == id {
				return c, true
			}
		}
		return store.AIConnection{}, false
	}
	conns, _ := a.store.AIConnectionsFor(orgID)
	for _, c := range conns {
		if c.ID == id && c.Via == store.AIViaOwn {
			return c, true
		}
	}
	return store.AIConnection{}, false
}

func (a *app) aiConnectionIn(id, orgID int64) bool {
	_, ok := a.aiConnectionOf(id, orgID)
	return ok
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
		a.aiFailure(w, r, server, store.ErrAIUnavailable)
		return
	}
	if err := a.store.DeleteAIConnection(browserPrincipal(r).ID, id); err != nil {
		a.aiFailure(w, r, server, err)
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
		a.aiFailure(w, r, server, err)
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
		a.aiFailure(w, r, server, store.ErrAIUnavailable)
		return
	}
	if err := a.store.DeleteAIAlias(browserPrincipal(r).ID, id); err != nil {
		a.aiFailure(w, r, server, err)
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
		a.aiFailure(w, r, false, err)
		return
	}
	run, err := formUSD(r, "run")
	if err != nil {
		a.aiFailure(w, r, false, err)
		return
	}
	if err := a.store.SetAICap(browserPrincipal(r).ID, store.AICap{OrgID: o.ID, Workflow: r.FormValue("workflow"), MonthUSD: month, RunUSD: run}); err != nil {
		a.aiFailure(w, r, false, err)
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
		a.aiFailure(w, r, false, store.ErrOrgNotFound)
		return
	}
	capUSD, err := formUSD(r, "cap")
	if err != nil {
		a.aiFailure(w, r, false, err)
		return
	}
	id := formID(r, "id")
	if !a.aiConnectionIn(id, o.ID) {
		a.aiFailure(w, r, false, store.ErrAIUnavailable)
		return
	}
	if _, err := a.store.LendAIConnection(browserPrincipal(r).ID, id, to.ID, capUSD); err != nil {
		a.aiFailure(w, r, false, err)
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
		a.aiFailure(w, r, false, err)
		return
	}
	orgRedirect(w, r, o.Slug, "ai")
}

func (a *app) aiReleaseSave(w http.ResponseWriter, r *http.Request) {
	o, _, ok := a.aiForm(w, r)
	if !ok {
		return
	}
	capUSD, err := formUSD(r, "cap")
	if err != nil {
		a.aiFailure(w, r, true, err)
		return
	}
	mode := r.FormValue("mode")
	if mode == store.AIReleaseAll && capUSD <= 0 {
		capUSD = a.store.AIReleaseDefaultCap()
	}
	if err := a.store.SetAIRelease(browserPrincipal(r).ID, mode, capUSD); err != nil {
		if errors.Is(err, store.ErrAICap) && mode == store.AIReleaseAll {
			err = errAIReleaseCap
		}
		a.aiFailure(w, r, true, err)
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
		a.aiFailure(w, r, true, store.ErrOrgNotFound)
		return
	}
	capUSD, err := formUSD(r, "cap")
	if err != nil {
		a.aiFailure(w, r, true, err)
		return
	}
	if r.FormValue("remove") == "1" {
		err = a.store.DeleteAIGrant(browserPrincipal(r).ID, target.ID)
	} else {
		err = a.store.SetAIGrant(browserPrincipal(r).ID, target.ID, capUSD)
	}
	if err != nil {
		a.aiFailure(w, r, true, err)
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
	case errors.Is(err, errAIReleaseCap):
		return http.StatusBadRequest, l.msg("ai.err.release_cap"), true
	case errors.Is(err, errAITestOff):
		return http.StatusConflict, l.msg("ai.err.test_off"), true
	case errors.Is(err, store.ErrAICap):
		return http.StatusBadRequest, l.msg("ai.f.cap"), true
	case errors.Is(err, store.ErrAIAlias):
		return http.StatusBadRequest, l.msg("ai.err.alias"), true
	case errors.Is(err, store.ErrAILoan):
		return http.StatusBadRequest, l.msg("ai.err.loan"), true
	case errors.Is(err, store.ErrAIConnection):
		return http.StatusBadRequest, l.msg("ai.f.form"), true
	}
	return 0, "", false
}

// aiPOST lists the forms of the section, for the router.
func (a *app) aiPOST() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"/ui/orgs/ai/connection": a.aiConnectionSave, "/ui/orgs/ai/connection/delete": a.aiConnectionDelete, "/ui/orgs/ai/connection/test": a.aiConnectionTest,
		"/ui/orgs/ai/alias": a.aiAliasSave, "/ui/orgs/ai/alias/delete": a.aiAliasDelete,
		"/ui/orgs/ai/cap": a.aiCapSave, "/ui/orgs/ai/lend": a.aiLend, "/ui/orgs/ai/loan/revoke": a.aiLoanRevoke,
		"/ui/orgs/ai/release": a.aiReleaseSave, "/ui/orgs/ai/grant": a.aiGrantSave,
	}
}
