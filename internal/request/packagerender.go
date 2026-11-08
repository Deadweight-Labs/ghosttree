package request

import (
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
)

// Die Textfassungen eines Pakets, geteilt von MCP und CLI: beide sollen dem
// Leser dasselbe Bild zeigen.

func packageHeader(d PackageDetail) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %q [%s] — project %s, branch %s", d.HumanID(), d.Name, d.State, d.Project, d.Branch)
	if d.PR > 0 {
		fmt.Fprintf(&b, ", PR #%d", d.PR)
	}
	if d.InheritedFrom != "" {
		fmt.Fprintf(&b, " (inherited: this branch was cut from %s)", d.InheritedFrom)
	}
	return b.String()
}

// RenderPackage ist die Antwort von package_get: je Request eine Zeile mit den
// Zahlen, darunter was auf einen Menschen wartet.
func RenderPackage(d PackageDetail) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", packageHeader(d))
	if len(d.Members) == 0 {
		b.WriteString("\nNo requests in this package yet (package_update action=add).\n")
		return b.String()
	}
	for _, role := range []string{RolePflicht, RoleOptional} {
		wrote := false
		for _, m := range d.Members {
			if m.Role != role {
				continue
			}
			if !wrote {
				fmt.Fprintf(&b, "\n%s\n", role)
				wrote = true
			}
			fmt.Fprintf(&b, "- REQ-%d [%s] %s — %d open, %d met, %d waived\n", m.RequestID, m.State, m.Title, m.Open, m.Met, m.Waived)
		}
	}
	writeWaitingTasks(&b, d)
	return b.String()
}

func writeWaitingTasks(b *strings.Builder, d PackageDetail) {
	wrote := false
	for _, m := range d.Members {
		for _, t := range m.Tasks {
			if !wrote {
				b.WriteString("\nHuman tasks still waiting\n")
				wrote = true
			}
			fmt.Fprintf(b, "- TASK-%d [%s|%s] %s (REQ-%d)", t.ID, t.Kind, t.State, t.Title, m.RequestID)
			if t.Location != "" {
				fmt.Fprintf(b, " -> %s", t.Location)
			}
			b.WriteByte('\n')
		}
	}
}

// anchorText sagt, wo ein Nachweis geschrieben wurde: Commit, Branch, PR.
// Der Prüfer schlägt damit den Stand des Codes nach, an dem der Agent ihn
// behauptet hat. Ein Nachweis ohne Anker steht ohne Stand da, und das sagt
// auch der Text, statt eine Herkunft zu erfinden.
func anchorText(a *gitanchor.Anchor) string {
	if a == nil {
		return "no git anchor"
	}
	commit := a.Commit
	if len(commit) > 10 {
		commit = commit[:10]
	}
	text := "@ " + commit
	if a.Branch != "" {
		text += " on " + a.Branch
	}
	if a.PR != nil {
		text += fmt.Sprintf(", PR #%d", *a.PR)
	}
	if a.Dirty {
		text += ", uncommitted changes"
	}
	return text
}

// RenderPackageReview ist das Lastenheft für den Prüfer. Der Verdict steht
// oben und ist aus den Zahlen abgeleitet, nicht geschätzt: bereit nur, wenn
// jedes Pflichtkriterium erfüllt oder mit Begründung verzichtet ist und kein
// Human-Task der Pflicht-Requests mehr wartet. Erfüllt heißt hier "es gibt einen
// Nachweis" — ob er stimmt, prüft der Leser gegen den Code.
func RenderPackageReview(d PackageDetail) string {
	var b strings.Builder
	open, met, waived, tasks, pflicht := 0, 0, 0, 0, 0
	for _, m := range d.Members {
		if m.Role != RolePflicht {
			continue
		}
		pflicht++
		open += m.Open
		met += m.Met
		waived += m.Waived
		tasks += len(m.Tasks)
	}
	verdict := "READY: every mandatory criterion is met or waived with a reason"
	if open > 0 || tasks > 0 || len(d.Uncovered) > 0 {
		verdict = fmt.Sprintf("NOT READY: %d mandatory criteria open, %d human tasks waiting", open, tasks)
		if len(d.Uncovered) > 0 {
			verdict += fmt.Sprintf(", %d requirements of the ideas not covered", len(d.Uncovered))
		}
	}
	if pflicht == 0 {
		verdict = "EMPTY: no mandatory request in this package"
	}
	fmt.Fprintf(&b, "# Requirements list — %s\n", packageHeader(d))
	fmt.Fprintf(&b, "%s\nMandatory: %d requests, %d criteria met, %d waived, %d open.\n", verdict, pflicht, met, waived, open)
	b.WriteString("Met means evidence exists, not that it is right: check each criterion against the code at the commit its evidence names. Waived means nobody built it: read the reason.\n")
	for _, role := range []string{RolePflicht, RoleOptional} {
		wrote := false
		for _, m := range d.Members {
			if m.Role != role {
				continue
			}
			if !wrote {
				fmt.Fprintf(&b, "\n## %s requests\n", role)
				wrote = true
			}
			fmt.Fprintf(&b, "\n### REQ-%d [%s] %s\n", m.RequestID, m.State, m.Title)
			for _, c := range m.Criteria {
				fmt.Fprintf(&b, "- %s [%s] %s\n", c.HumanID(), c.State, c.Description)
				if len(c.Evidence) == 0 {
					if c.State != "open" {
						b.WriteString("  - no evidence recorded\n")
					}
					continue
				}
				for _, e := range c.Evidence {
					stale := ""
					if c.State == "open" {
						stale = " [from before it was reopened]"
					}
					fmt.Fprintf(&b, "  - evidence %s: %s (%s)%s\n", e.Kind, e.Ref, anchorText(e.GitAnchor), stale)
				}
			}
			for _, t := range m.Tasks {
				fmt.Fprintf(&b, "- TASK-%d [%s|%s] %s\n", t.ID, t.Kind, t.State, t.Title)
			}
		}
	}
	if len(d.Uncovered) > 0 {
		b.WriteString("\n## Requirements of the ideas that no met criterion covers\n")
		for _, u := range d.Uncovered {
			how := "no criterion yet"
			if u.State == "open" {
				how = "criteria exist, none is met"
			}
			fmt.Fprintf(&b, "- IDEA-%d#%d %s (%s)\n", u.Idea, u.No, u.Text, how)
		}
		b.WriteString("An idea counts as done only when each of its requirements has a met criterion or is waived with a reason (idea_update requirement_waive).\n")
	}
	b.WriteString("\nConfirm: request_record_progress action=criterion_met with evidence_kind and evidence_ref. Reopen what the code does not show: action=criterion_reopen with criterion_id and reason.\n")
	return b.String()
}
