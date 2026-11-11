package web

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/runbook"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Abläufe auf der neuen Oberfläche (REQ-599, Phase 2 B): die Liste der Abläufe
// eines Repositorys und die Seite eines Ablaufs mit den Befehlen, die man kopiert.
// Die Daten kommen aus dem Store wie bei /api/runbooks; jeder Ablauf wird gegen
// die Rechte des Betrachters geprüft (CanSeeRunbook).

// runbookRow is one line of the list.
type runbookRow struct {
	Name, Href, Purpose, Machine string
	Approved                     bool
	Sha, LastAge                 string
	Targets                      int
}

type runbooksView struct {
	Rows  []runbookRow
	Empty bool
	// NewHref is the button "New runbook", empty for who may not create one.
	NewHref string
}

// runbookCmd is a command to copy with its caption.
type runbookCmd struct {
	Label string
	Block cmdBlock
}

type runbookStep struct {
	N              int
	Title, Command string
	Check          string
}

type runbookPageView struct {
	Name, Purpose, Machine string
	Version                int
	Approved               bool
	ApprovedBy             string
	Sha                    string
	LastRun                string
	LastResult             string
	LastAge                string
	Params                 []runbook.Param
	Preconditions          []string
	Steps                  []runbookStep
	Rollback               []runbookStep
	Cmds                   []runbookCmd
	GiveHref               string
	Touched                []string
	Pitfalls               []runbook.PitfallRef
	// EditHref is the button "Edit", empty for who may not change the runbook;
	// Result says what the last save did.
	EditHref string
	Result   *requestResult
}

func (a *app) accountNumber(r *http.Request) int64 {
	id, _ := store.AccountNumericID(browserPrincipal(r).ID)
	return id
}

// runbooksPage lists the runbooks of the project of the repository path.
func (a *app) runbooksPage(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	project := a.projectParam(r)
	if rv := repoFrom(r); rv != nil {
		// The repository in the address is the one project of the list.
		project = rv.Project
	}
	pa := a.access(r)
	hidden, stop := a.gateList(w, r, pa, project, store.ResRunbook)
	if stop {
		return
	}
	v := &runbooksView{}
	if !hidden {
		refs, err := a.store.RunbookRefsIn(scope.Axes{Project: project}, a.accountNumber(r))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		now := overviewNow().UTC()
		for _, ref := range refs {
			if !pa.CanSeeRunbook(ref) {
				continue
			}
			sum, err := a.store.RunbookSummary(ref)
			if err != nil {
				continue
			}
			row := runbookRow{Name: sum.Name, Purpose: sum.Purpose, Machine: sum.Machine, Targets: sum.Targets, Approved: sum.ApprovedVersion > 0,
				Href: runbookHref(r, project, sum.Name)}
			if pa.HidesMachines(project) {
				row.Machine = ""
			}
			if sum.LastCommit != "" {
				row.Sha = shortSha(sum.LastCommit)
			}
			if t := parseTime(sum.LastSuccessAt); !t.IsZero() {
				row.LastAge = shortAgo(l, now, t)
			}
			v.Rows = append(v.Rows, row)
		}
	}
	sort.SliceStable(v.Rows, func(i, j int) bool { return v.Rows[i].Name < v.Rows[j].Name })
	v.Empty = len(v.Rows) == 0
	if interactive(r) && project != "" && pa.CheckRunbookCreate(project, "") == nil {
		v.NewHref = runbookHrefIn(project, runbookNew)
	}
	a.renderBrowser(w, r, "runbooks", pageData{Title: l.msg("repo.tab.runbooks"), Project: project, RunbooksV: v})
}

func runbookHref(r *http.Request, project, name string) string {
	if rv := repoFrom(r); rv != nil {
		return rv.Href + "/runbooks/" + url.PathEscape(name)
	}
	return repoHref(project) + "/runbooks/" + url.PathEscape(name)
}

// runbookPage is the page of one runbook.
func (a *app) runbookPage(w http.ResponseWriter, r *http.Request, remote, name string) {
	l := a.langFor(r)
	pa := a.access(r)
	refs, err := a.store.RunbookCandidates(name, scope.Axes{Project: remote}, a.accountNumber(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var ref *store.RunbookRef
	for i := range refs {
		if pa.CanSeeRunbook(refs[i]) {
			ref = &refs[i]
			break
		}
	}
	if ref == nil {
		pa.Filtered()
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("edit") == "1" {
		a.runbookEditPage(w, r, remote, *ref)
		return
	}
	view, err := a.store.RunbookView(ref.ID, 0)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	v := &runbookPageView{Name: view.Name, Purpose: view.Spec.Purpose, Machine: view.Machine, Version: view.Version, Approved: view.Approved, ApprovedBy: view.ApprovedBy,
		Params: view.Spec.Params, Preconditions: view.Spec.Preconditions, Pitfalls: pa.RunbookTitles(view.Spec.Pitfalls)}
	if pa.HidesMachines(remote) {
		v.Machine = ""
	}
	if interactive(r) && pa.CheckRunbook(*ref, store.ActEdit) == nil {
		v.EditHref = runbookHrefIn(remote, view.Name) + "?edit=1"
	}
	switch r.URL.Query().Get("done") {
	case "saved":
		v.Result = &requestResult{Text: l.msg("runbooks.result.saved", view.Version), Kind: "done"}
	case "approved":
		v.Result = &requestResult{Text: l.msg("runbooks.result.saved_approved", view.Version), Kind: "done"}
	case "unchanged":
		v.Result = &requestResult{Text: l.msg("runbooks.result.unchanged"), Kind: "done"}
	}
	if view.GitAnchor != nil && !view.GitAnchor.IsZero() {
		v.Sha = shortSha(view.GitAnchor.Commit)
	}
	for i, s := range view.Spec.Steps {
		v.Steps = append(v.Steps, runbookStep{N: i + 1, Title: s.Title, Command: s.Command, Check: s.Check})
	}
	for i, s := range view.Spec.Rollback {
		v.Rollback = append(v.Rollback, runbookStep{N: i + 1, Title: s.Title, Command: s.Command, Check: s.Check})
	}
	if run := view.LastSuccess; run != nil {
		v.LastRun, v.LastResult = "#"+strconv.FormatInt(run.ID, 10), run.Result
		v.LastAge = shortAgo(l, overviewNow().UTC(), parseTime(run.CreatedAt))
	}
	for _, t := range view.Spec.Targets {
		cmd := "ctx run " + view.Name + " --target " + t.Name
		v.Cmds = append(v.Cmds, runbookCmd{Label: l.msg("runbooks.dry_run") + " · " + t.Name, Block: cmdBlock{ID: "rb-dry-" + t.Name, Mac: cmd, Linux: cmd}},
			runbookCmd{Label: l.msg("runbooks.run") + " · " + t.Name, Block: cmdBlock{ID: "rb-run-" + t.Name, Mac: cmd + " --run", Linux: cmd + " --run"}})
	}
	if len(v.Cmds) == 0 {
		cmd := "ctx run " + view.Name
		v.Cmds = append(v.Cmds, runbookCmd{Label: l.msg("runbooks.checklist"), Block: cmdBlock{ID: "rb-list", Mac: cmd, Linux: cmd}})
	}
	// The ask for an agent opens the room of the project with the message filled in.
	if remote != "" {
		draft := l.msg("runbooks.draft", view.Name)
		v.GiveHref = pathWith("/ui/coord", "room", store.RoomKeyForProject(remote), "draft", draft)
	}
	r = a.withRepoBand(r, remote, "runbooks")
	a.renderBrowser(w, r, "runbook", pageData{Title: view.Name, RunbookV: v})
}
