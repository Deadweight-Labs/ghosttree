package web

import (
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// anchorView ist der Git-Anker eines Eintrags als Verweise (REQ-597, Kriterium 5):
// kurzer Commit mit Link auf die Commit-Seite, PR-Nummer mit Link auf die PR-Seite,
// der Branch nur nach der Gast-Regel (ProjectAccess.BranchView) und „Web“ bei einem
// Eintrag aus der Oberfläche. Alle Seiten zeigen ihn mit demselben Template
// „gitanchor“.
type anchorView struct {
	Sha, ShaHref string
	PR           int64
	PRHref       string
	Branch       string
	Web          bool
}

// anchorViewOf macht aus dem Anker eines Eintrags die Ansicht für diesen
// Betrachter; nil, wenn es nichts zu zeigen gibt. via ist die Herkunft des
// Eintrags („web“ bei einem Schreibzugriff aus der Oberfläche, dort gibt es
// keinen Git-Stand). Ohne Projekt gibt es keine Links, nur den kurzen Commit.
func anchorViewOf(pa *store.ProjectAccess, project string, g *gitanchor.Anchor, via string) *anchorView {
	if g == nil || g.IsZero() {
		if via == gitanchor.ViaWeb || g.IsWeb() {
			return &anchorView{Web: true}
		}
		return nil
	}
	v := &anchorView{Sha: shortSha(g.Commit)}
	base := ""
	if project != "" {
		base = repoHref(project)
		v.ShaHref = base + "/commits/" + g.Commit
	}
	if g.PR != nil && *g.PR > 0 {
		v.PR = *g.PR
		if base != "" {
			v.PRHref = base + "/pulls/" + strconv.FormatInt(*g.PR, 10)
		}
	}
	if pa != nil {
		v.Branch = pa.BranchView(project, g.Branch)
	}
	return v
}

// originVia ist die Herkunft „web“ aus dem Entstehungskontext eines Eintrags,
// wenn dort keine andere steht: Wissen, Ideen und Feature-Fassungen halten sie
// dort, nicht am Anker.
func originVia(oc *originctx.Context) string {
	if oc != nil && oc.Via == originctx.ViaWeb {
		return gitanchor.ViaWeb
	}
	return ""
}
