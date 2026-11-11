package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/knref"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The Gardener tab of an organization or a person (REQ-624). It exists where
// the gardener is switched on and only for people who may review its
// proposals: the open proposals grouped by verdict, the latest runs with the
// next one, the automatic approvals with their undo, and a way to the
// settings. Guests and services never see it; the inbox has no gardener row
// any more.

// The catalog keys of the run states, rules and frequencies, spelled out so
// the catalog test finds them.
var (
	gardenRunKeys  = map[string]string{"running": "gardener.run.running", "done": "gardener.run.done", "failed": "gardener.run.failed", "budget": "gardener.run.budget"}
	gardenRuleKeys = map[string]string{store.GardenRuleProof: "gardener.rule.proof", store.GardenRuleDescription: "gardener.rule.description", store.GardenRuleTest: "gardener.rule.test"}
	gardenHintKeys = map[string]string{store.GardenRuleProof: "gardener.rule_hint.proof", store.GardenRuleDescription: "gardener.rule_hint.description", store.GardenRuleTest: "gardener.rule_hint.test"}
	gardenFreqKeys = map[string]string{store.GardenDaily: "gardener.freq.daily", store.GardenWeekdays: "gardener.freq.weekdays", store.GardenWeekly: "gardener.freq.weekly"}
)

// gardenTabView is the content of the tab.
type gardenTabView struct {
	Rows     []rvRow
	Verdicts []gardenVerdictPill
	Repos    []gardenVerdictPill
	All      int
	Runs     []gardenRunRow
	Next     string
	Spent    string
	Budget   string
	Percent  int
	Auto     []gardenAutoRow
	Settings string
	Token    string
	Org      string
}

type gardenVerdictPill struct {
	Label, Href string
	Count       int
	Current     bool
}

type gardenRunRow struct {
	When, Repo, State, Cost string
	Proposals               int
	Tone                    string
}

type gardenAutoRow struct {
	ID                 int64
	Rule, What, Before string
	After, When, Href  string
	Undone, CanUndo    bool
}

// gardenReviewer says whether the viewer may review the gardener's proposals
// in this namespace: its owner, or someone with lead, owner or the review
// right in one of its projects. Guests never.
func (a *app) gardenReviewer(r *http.Request, org store.Org) bool {
	if org.Role == store.OrgOwner {
		return true
	}
	pa := a.access(r)
	for _, p := range a.orgProjects(r, org) {
		if reviews(pa.Role(p.Remote)) {
			return true
		}
	}
	return false
}

// reviews: the role may decide the gardener's proposals in its project.
func reviews(role store.RoleInfo) bool {
	return role.CanReview || store.RoleRank(role.Role) >= store.RoleRank(store.RoleLead)
}

// gardenTabFor says whether the tab exists for this viewer, with the settings.
func (a *app) gardenTabFor(r *http.Request, org store.Org) (store.GardenSettings, bool) {
	g, _ := a.store.GardenSettingsFor(org.ID)
	if !g.Enabled || !a.gardenReviewer(r, org) {
		return g, false
	}
	return g, true
}

// gardenNotes are the open proposals of the gardener in the namespace's
// repositories the viewer may see.
func (a *app) gardenNotes(r *http.Request, org store.Org) []store.Knowledge {
	pa := a.access(r)
	var notes []store.Knowledge
	for _, p := range a.orgProjects(r, org) {
		pending, err := a.store.PendingKnowledge(p.Remote, a.overfetch(reviewShown), pa.Viewer())
		if err != nil {
			continue
		}
		for _, k := range pending {
			if k.Harness == gardenerHarness && pa.CanSeeKnowledge(k) {
				notes = append(notes, k)
			}
		}
	}
	return notes
}

// gardenTab fills the tab.
func (a *app) gardenTab(w http.ResponseWriter, r *http.Request, org store.Org) {
	g, ok := a.gardenTabFor(r, org)
	if !ok {
		http.NotFound(w, r)
		return
	}
	l := a.langFor(r)
	pa := a.access(r)
	base := "/ui/" + url.PathEscape(org.Slug) + "/gardener"
	v := &gardenTabView{Token: csrfOf(r), Org: org.Slug}
	if org.Role == store.OrgOwner {
		v.Settings = "/ui/orgs?org=" + url.QueryEscape(org.Slug) + "#gardener"
	}
	notes := a.gardenNotes(r, org)
	block := a.gardenBlock(r, pa, "", notes)
	rows := reviewRows(l, block, nil)
	wantVerdict, wantRepo := r.URL.Query().Get("v"), r.URL.Query().Get("repo")
	verdicts, repos := map[string]int{}, map[string]int{}
	var order, repoOrder []string
	for i := range rows {
		rows[i].Next = "gardener:" + org.Slug
		if rows[i].Act != nil {
			rows[i].Act.Filter = ""
		}
		if verdicts[rows[i].Label] == 0 {
			order = append(order, rows[i].Label)
		}
		verdicts[rows[i].Label]++
		repo := lastSegment(notes[i].Scope.Project)
		if repos[repo] == 0 {
			repoOrder = append(repoOrder, repo)
		}
		repos[repo]++
	}
	v.All = len(rows)
	for i, row := range rows {
		if (wantVerdict == "" || row.Label == wantVerdict) && (wantRepo == "" || lastSegment(notes[i].Scope.Project) == wantRepo) {
			v.Rows = append(v.Rows, row)
		}
	}
	slices.SortStableFunc(order, func(x, y string) int { return verdicts[y] - verdicts[x] })
	for _, label := range order {
		q := url.Values{"v": {label}}
		if wantRepo != "" {
			q.Set("repo", wantRepo)
		}
		v.Verdicts = append(v.Verdicts, gardenVerdictPill{Label: label, Count: verdicts[label], Href: base + "?" + q.Encode(), Current: label == wantVerdict})
	}
	slices.Sort(repoOrder)
	for _, repo := range repoOrder {
		q := url.Values{"repo": {repo}}
		if wantVerdict != "" {
			q.Set("v", wantVerdict)
		}
		v.Repos = append(v.Repos, gardenVerdictPill{Label: repo, Count: repos[repo], Href: base + "?" + q.Encode(), Current: repo == wantRepo})
	}
	now := overviewNow().UTC()
	if next := store.NextGardenRun(g, now); !next.IsZero() {
		v.Next = l.dateTime(next.Local())
	}
	spent := a.store.GardenSpent(org.ID, now)
	v.Spent, v.Budget = fmt.Sprintf("%.2f $", spent), fmt.Sprintf("%.2f $", g.BudgetMon)
	if g.BudgetMon > 0 {
		v.Percent = min(100, int(spent*100/g.BudgetMon))
	}
	runs, _ := a.store.GardenRuns(org.ID, 30)
	for _, run := range runs {
		if pa.Role(run.Project).Role == "" {
			continue // a restricted repository stays unnamed
		}
		row := gardenRunRow{When: shortAge(l, now, parseTime(run.StartedAt)), Repo: lastSegment(run.Project), State: l.msg(gardenRunKeys[run.State]),
			Cost: fmt.Sprintf("%.2f $", run.CostUSD), Proposals: run.Proposals}
		if run.State == "failed" || run.State == "budget" {
			row.Tone = "warn"
		}
		v.Runs = append(v.Runs, row)
	}
	log, _ := a.store.GardenAutoLog(org.ID, 30)
	for _, e := range log {
		role := pa.Role(e.Project)
		if role.Role == "" {
			continue
		}
		v.Auto = append(v.Auto, gardenAutoRow{ID: e.ID, Rule: l.msg(gardenRuleKeys[e.Rule]), What: lastSegment(e.Project) + " · " + e.TargetID,
			Before: e.Before, After: e.After, When: shortAge(l, now, parseTime(e.CreatedAt)), Href: "/ui/knowledge/" + strconv.FormatInt(e.NoteID, 10),
			Undone: e.UndoneAt != "", CanUndo: e.UndoneAt == "" && interactive(r) && reviews(role)})
	}
	a.renderBrowser(w, r, "gardentab", pageData{Title: l.msg("ui.org.tab.gardener"), OrgName: org.Name, OrgBand: a.orgBandFor(r, org, "gardener"), GardenTabV: v})
}

// gardenOpenCount is the number on the tab: the open proposals the viewer may
// see; -1 when the tab does not exist for the viewer.
func (a *app) gardenOpenCount(r *http.Request, org store.Org) int {
	if _, ok := a.gardenTabFor(r, org); !ok {
		return -1
	}
	return len(a.gardenNotes(r, org))
}

// gardenNextRe is the form of the "next" value of the rows on the tab.
var gardenNextRe = regexp.MustCompile(`^gardener:([a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?)$`)

// gardenBack is where a decision on the tab leads back to, "" when it came
// from elsewhere.
func gardenBack(r *http.Request, q url.Values) string {
	m := gardenNextRe.FindStringSubmatch(r.FormValue("next"))
	if m == nil {
		return ""
	}
	return "/ui/" + m[1] + "/gardener?" + q.Encode()
}

// ---------------------------------------------------------------- settings

// orgGardenSave stores the gardener settings from the settings page (owner,
// interactive session; the store checks the owner again).
func (a *app) orgGardenSave(w http.ResponseWriter, r *http.Request) {
	o, err := a.formOrg(r)
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	g, _ := a.store.GardenSettingsFor(o.ID)
	g.OrgID = o.ID
	g.Enabled = r.FormValue("enabled") == "1"
	g.Frequency = r.FormValue("frequency")
	if m, ok := store.ParseGardenTime(r.FormValue("at")); ok {
		g.AtMinute = m
	} else {
		a.orgFailure(w, r, store.ErrGardenSettings)
		return
	}
	g.Weekday, _ = strconv.Atoi(r.FormValue("weekday"))
	g.BudgetRun, err = strconv.ParseFloat(strings.TrimSpace(r.FormValue("budget_run")), 64)
	if err != nil {
		a.orgFailure(w, r, store.ErrGardenSettings)
		return
	}
	if g.BudgetMon, err = strconv.ParseFloat(strings.TrimSpace(r.FormValue("budget_month")), 64); err != nil {
		a.orgFailure(w, r, store.ErrGardenSettings)
		return
	}
	g.Repos, g.Rules = nil, nil
	if r.FormValue("repos_all") != "1" {
		g.Repos = r.Form["repo"]
	}
	for _, rule := range r.Form["rule"] {
		g.Rules = append(g.Rules, rule)
	}
	if err := a.store.SetGardenSettings(browserPrincipal(r).ID, g); err != nil {
		a.orgFailure(w, r, err)
		return
	}
	http.Redirect(w, r, "/ui/orgs?"+url.Values{"org": {o.Slug}, "notice": {"garden"}}.Encode()+"#gardener", http.StatusSeeOther)
}

// gardenSettingsView is the settings section on the settings page.
type gardenSettingsView struct {
	G                   store.GardenSettings
	At                  string
	Repos               []gardenRepoChoice
	AllRepos            bool
	Rules               []gardenRuleChoice
	Frequencies         []gardenChoice
	Weekdays            []gardenChoice
	Budget, Spent, Next string
}

type gardenRepoChoice struct {
	Remote, Name string
	On           bool
}

type gardenRuleChoice struct {
	Key, Label, Hint string
	On               bool
}

type gardenChoice struct {
	Value, Label string
	On           bool
}

// gardenSettingsFor builds the settings section for the owner of a namespace.
func (a *app) gardenSettingsFor(r *http.Request, org store.Org) *gardenSettingsView {
	if org.Role != store.OrgOwner {
		return nil
	}
	l := a.langFor(r)
	g, _ := a.store.GardenSettingsFor(org.ID)
	v := &gardenSettingsView{G: g, At: store.FormatGardenTime(g.AtMinute), AllRepos: len(g.Repos) == 0}
	for _, p := range a.orgProjects(r, org) {
		v.Repos = append(v.Repos, gardenRepoChoice{Remote: p.Remote, Name: lastSegment(p.Remote), On: slices.Contains(g.Repos, p.Remote)})
	}
	for _, rule := range store.GardenRules {
		v.Rules = append(v.Rules, gardenRuleChoice{Key: rule, Label: l.msg(gardenRuleKeys[rule]), Hint: l.msg(gardenHintKeys[rule]), On: slices.Contains(g.Rules, rule)})
	}
	for _, f := range []string{store.GardenDaily, store.GardenWeekdays, store.GardenWeekly} {
		v.Frequencies = append(v.Frequencies, gardenChoice{Value: f, Label: l.msg(gardenFreqKeys[f]), On: g.Frequency == f})
	}
	for d := 1; d <= 7; d++ {
		wd := d % 7
		v.Weekdays = append(v.Weekdays, gardenChoice{Value: strconv.Itoa(wd), Label: l.weekdayName(time.Weekday(wd)), On: g.Weekday == wd})
	}
	now := overviewNow().UTC()
	v.Spent = fmt.Sprintf("%.2f $", a.store.GardenSpent(org.ID, now))
	if next := store.NextGardenRun(g, now); !next.IsZero() {
		v.Next = l.dateTime(next.Local())
	}
	return v
}

// ---------------------------------------------------------------- automatic approvals

// gardenAutoUndo takes an automatic approval back (POST /ui/gardener/undo):
// whoever may review the gardener's proposals of that namespace, from an
// interactive session. What a person changed since is left alone.
func (a *app) gardenAutoUndo(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	e, err := a.store.GardenAutoByID(id)
	if err != nil || e.UndoneAt != "" {
		http.NotFound(w, r)
		return
	}
	org, ok := a.orgByID(r, e.OrgID)
	if !ok || !reviews(a.access(r).Role(e.Project)) {
		http.NotFound(w, r)
		return
	}
	switch e.TargetKind {
	case "criterion":
		// The rule only added a proof; the undo takes exactly that one back.
		c, ev, _ := strings.Cut(e.TargetID, ":")
		cid, _ := strconv.ParseInt(c, 10, 64)
		eid, _ := strconv.ParseInt(ev, 10, 64)
		err = a.store.RemoveAutoEvidence(eid, cid, e.After)
	case "ghost":
		_, err = a.store.UndoGardenGhost(e.Project, e.TargetID, gardenGhostWrittenRef+strconv.FormatInt(e.NoteID, 10))
	case "regression":
		// Only while the pitfall still carries what the rule set.
		kid, _ := strconv.ParseInt(e.TargetID, 10, 64)
		if k, kerr := a.store.KnowledgeByID(kid); kerr != nil || k.RegressionState != "covered" || k.RegressionTest != e.After {
			err = store.ErrGardenAutoGone
			break
		}
		state, test, _ := strings.Cut(e.Before, " ")
		err = a.store.SetRegressionCover(kid, state, test)
	}
	if err != nil {
		http.Error(w, "could not undo", http.StatusConflict)
		return
	}
	if err := a.store.MarkGardenAutoUndone(e.ID, browserPrincipal(r).ID); err != nil {
		http.Error(w, "could not undo", http.StatusConflict)
		return
	}
	http.Redirect(w, r, "/ui/"+url.PathEscape(org.Slug)+"/gardener#auto", http.StatusSeeOther)
}

// orgByID finds an organization of the viewer by its number.
func (a *app) orgByID(r *http.Request, id int64) (store.Org, bool) {
	orgs, err := a.store.ListOrgs(browserPrincipal(r).ID)
	if err != nil {
		return store.Org{}, false
	}
	for _, o := range orgs {
		if o.ID == id && !o.Guest {
			return o, true
		}
	}
	return store.Org{}, false
}

// applyGardenAuto approves, for one namespace, the open proposals of the
// gardener that a switched-on rule covers. It acts on the authority of the
// owner who switched the rules on, with that person's rights as they are now
// (a demoted owner's rules approve nothing), through the same plans the review
// page uses, and logs every change with what it was before.
func (a *app) applyGardenAuto(g store.GardenSettings) int {
	// The rules act only on repositories an owner picked by name; "all
	// repositories" approves nothing on its own.
	if !g.Enabled || len(g.Rules) == 0 || g.UpdatedBy <= 0 || len(g.Repos) == 0 {
		return 0
	}
	enabler := "person:" + strconv.FormatInt(g.UpdatedBy, 10)
	if a.store.OrgRole(g.OrgID, enabler) != store.OrgOwner {
		return 0
	}
	acct, err := a.store.AccountByPrincipalID(enabler)
	if err != nil {
		return 0
	}
	pa := a.store.Access(store.Principal{ID: enabler, Label: acct.Name, TokenKind: store.WebSessionKind})
	by := acct.Name + " (Regel, automatisch)"
	projects, _ := a.store.ListProjects(enabler, g.OrgID)
	applied := 0
	for _, p := range projects {
		pending, err := a.store.PendingKnowledge(p.Remote, 500)
		if err != nil {
			continue
		}
		// Only the repositories the settings name, and only proposals filed
		// after the rules were last set (security review 3).
		if !slices.Contains(g.Repos, p.Remote) {
			continue
		}
		for _, note := range pending {
			if note.Harness != gardenerHarness || a.store.GardenAutoNoted(note.ID) || note.CreatedAt < g.UpdatedAt {
				continue
			}
			if e, ok := a.autoApprove(pa, g, note, by); ok {
				e.OrgID, e.NoteID, e.Project, e.EnabledBy = g.OrgID, note.ID, note.Scope.Project, g.UpdatedBy
				if _, err := a.store.RecordGardenAuto(e); err == nil {
					applied++
				}
			}
		}
	}
	return applied
}

// autoApprove applies one note when a rule covers it. Only three kinds ever
// qualify; each through the plan of the review page, so every check a person's
// click passes applies here too.
func (a *app) autoApprove(pa *store.ProjectAccess, g store.GardenSettings, note store.Knowledge, by string) (store.GardenAuto, bool) {
	switch {
	case strings.HasPrefix(note.SessionRef, gardenCriterionRef) && g.HasRule(store.GardenRuleProof):
		// Only a met criterion whose commit proof vanished gets the proposed
		// commit added as proof; its state never changes (security review 3).
		if a.gardenerNote(note) != nil || !openGardenNote(note) || pa.CheckKnowledge(note, store.ActVerify) != nil {
			return store.GardenAuto{}, false
		}
		reqID, criterion, ok := parseCriterionRef(note.SessionRef)
		items, okItems := parseProofItems(note.Body)
		if !ok || !okItems {
			return store.GardenAuto{}, false
		}
		// The same gate as a person's click: the criterion belongs to the
		// request the reference names, in the note's project, and the enabler
		// may read and work on that request.
		cref, err := a.store.RequestRef(store.RefCriterion, criterion)
		if err != nil || cref.ID != reqID || cref.Project == "" || cref.Project != note.Scope.Project {
			return store.GardenAuto{}, false
		}
		obj, visible := pa.RequestObject(cref)
		if !visible || pa.Check(cref.Project, store.ResRequest, store.ActRead, obj) != nil ||
			pa.Check(cref.Project, store.ResRequest, store.ActWork, obj) != nil {
			return store.GardenAuto{}, false
		}
		kind, ref := recordedProof(items)
		if kind != gardenKindCommit {
			return store.GardenAuto{}, false
		}
		evidence := fmt.Sprintf("%s (Gärtner-Vorschlag %s, automatisch freigegeben, Regel von %s)", ref, knref.Format(note.ID), by)
		evID, err := a.store.ReplaceVanishedEvidence(criterion, note.Scope.Project, evidence, by)
		if err != nil {
			return store.GardenAuto{}, false
		}
		_ = a.confirmGardenNote(note, by)
		return store.GardenAuto{Rule: store.GardenRuleProof, TargetKind: "criterion",
			TargetID: strconv.FormatInt(criterion, 10) + ":" + strconv.FormatInt(evID, 10), After: evidence}, true
	case strings.HasPrefix(note.SessionRef, gardenGhostRef) && g.HasRule(store.GardenRuleDescription):
		plan, err := a.planDescribe(pa, note)
		// Only where there was no description at all: one a person wrote is
		// never replaced by a rule.
		if err != nil || plan.hadOld || plan.facts.Was != "keine" {
			return store.GardenAuto{}, false
		}
		if _, err := a.store.PutGhostFile(store.GhostFile{
			Project: plan.project, Path: plan.path, Kind: "file", Description: plan.facts.Description,
			ContentSHA: plan.facts.SHA, GitBlob: plan.facts.Blob, LineCount: plan.facts.Lines,
			Person: by, Harness: gardenerHarness, SessionRef: fmt.Sprintf("%s%d", gardenGhostWrittenRef, note.ID),
			GitAnchor: gitanchor.Web(),
		}); err != nil {
			return store.GardenAuto{}, false
		}
		_ = a.confirmGardenNote(note, by)
		return store.GardenAuto{Rule: store.GardenRuleDescription, TargetKind: "ghost", TargetID: plan.path, After: ellipsize(plan.facts.Description, 200)}, true
	case strings.HasPrefix(note.SessionRef, gardenRegressionRef) && g.HasRule(store.GardenRuleTest):
		plan, err := a.planCover(pa, note)
		if err != nil || plan.state != "covered" {
			return store.GardenAuto{}, false
		}
		before := plan.pitfall.RegressionState + " " + plan.pitfall.RegressionTest
		if err := a.store.SetRegressionCover(plan.pitfall.ID, plan.state, plan.test); err != nil {
			return store.GardenAuto{}, false
		}
		_ = a.confirmGardenNote(note, by)
		return store.GardenAuto{Rule: store.GardenRuleTest, TargetKind: "regression", TargetID: strconv.FormatInt(plan.pitfall.ID, 10),
			Before: strings.TrimSpace(before), After: plan.test}, true
	}
	return store.GardenAuto{}, false
}

// WithGardenAuto runs the automatic approvals every interval until ctx ends.
// The server switches it on; tests call applyGardenAuto themselves.
func WithGardenAuto(ctx context.Context, every time.Duration, logger *slog.Logger) Option {
	return func(a *app) {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-time.After(every):
				}
				all, err := a.store.GardenSettingsAll()
				if err != nil {
					logger.Error("garden_auto", "error", err.Error())
					continue
				}
				for _, g := range all {
					if n := a.applyGardenAuto(g); n > 0 {
						logger.Info("garden_auto", "org", g.OrgID, "approved", n)
					}
				}
			}
		}()
	}
}
