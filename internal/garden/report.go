package garden

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

var kindLabel = map[string]string{
	Holds: "gilt noch", Stale: "veraltet", Correction: "Korrekturvorschlag", Unclear: "unklar",
	Skipped: "nicht prüfbar", Failed: "Fehler", NotRun: "nicht geprüft",
}

// WriteReport renders the run as Markdown. Verdicts that propose a change come
// first because they are what a person has to look at.
func WriteReport(w io.Writer, project string, res Result, at time.Time) {
	counts := map[string]int{}
	for _, v := range res.Verdicts {
		counts[v.Kind]++
	}
	mode := "Lauf"
	if res.DryRun {
		mode = "Trockenlauf (kein Modellaufruf)"
	}
	fmt.Fprintf(w, "# Gärtner: Wissen gegen Code, %s\n\n", project)
	fmt.Fprintf(w, "%s am %s. Der Gärtner schlägt vor, er entscheidet nicht.\n\n", mode, at.UTC().Format("2006-01-02 15:04 UTC"))
	if !res.DryRun {
		fmt.Fprintf(w, "%s\n\n", Headline(res.Verdicts))
	}
	fmt.Fprintf(w, "Einträge: %d. ", len(res.Verdicts))
	var parts []string
	for _, k := range []string{Holds, Stale, Correction, Unclear, Skipped, Failed, NotRun} {
		if counts[k] > 0 {
			label := kindLabel[k]
			if res.DryRun && k == NotRun {
				label = "würden geprüft (oder durch Limit/Budget ausgelassen)"
			}
			parts = append(parts, fmt.Sprintf("%s %d", label, counts[k]))
		}
	}
	fmt.Fprintf(w, "%s.\n\n", strings.Join(parts, ", "))
	if sum := res.Rotation.Summary(); sum != "" {
		fmt.Fprintf(w, "%s\n\n", sum)
	}
	var in, out, cacheRead, cacheWrite int
	for _, v := range res.Verdicts {
		in, out, cacheRead, cacheWrite = in+v.In, out+v.Out, cacheRead+v.CacheRead, cacheWrite+v.CacheWrite
	}
	if res.Agent {
		var s SessionStat
		for _, x := range res.Sessions {
			s.Turns += x.Turns
			s.Usage.InputTokens += x.Usage.InputTokens
			s.Usage.OutputTokens += x.Usage.OutputTokens
			s.Usage.CacheReadTokens += x.Usage.CacheReadTokens
			s.Usage.CacheWriteTokens += x.Usage.CacheWriteTokens
		}
		in, out, cacheRead, cacheWrite = s.Usage.InputTokens, s.Usage.OutputTokens, s.Usage.CacheReadTokens, s.Usage.CacheWriteTokens
		fmt.Fprintf(w, "Agent-Lauf: %d Sessions mit %d Turns.", len(res.Sessions), s.Turns)
		if res.Deferred > 0 {
			fmt.Fprintf(w, " Weitere %d fällige Einträge kommen in einem späteren Lauf dran.", res.Deferred)
		}
		fmt.Fprintf(w, "\n\n")
	}
	if res.DryRun && res.Agent {
		fmt.Fprintf(w, "Geschätzt: nur die Aufträge, %d Eingabe-Tokens vor den Turns; was die Sessions im Lauf lesen, lässt sich nicht vorhersagen. Obergrenze ist das Budget von $%.2f.\n\n", in, res.MaxUSD)
	} else if res.DryRun {
		fmt.Fprintf(w, "Geschätzt: %d Eingabe- und %d Ausgabe-Tokens, ca. $%.4f; Obergrenze bei vollen Antworten $%.4f.\n\n", in, out, res.Spent, res.UpperBoundUSD)
	} else {
		fmt.Fprintf(w, "Gemessen: %d Eingabe- und %d Ausgabe-Tokens", in, out)
		if cacheRead+cacheWrite > 0 {
			fmt.Fprintf(w, " (dazu %d aus dem Cache gelesen, %d in den Cache geschrieben)", cacheRead, cacheWrite)
		}
		fmt.Fprintf(w, ", $%.4f von höchstens $%.2f.\n\n", res.Spent, res.MaxUSD)
	}
	switch res.StoppedBy {
	case "budget":
		fmt.Fprintf(w, "**Abgebrochen:** Budgetdeckel erreicht; der Rest ist unten als \"nicht geprüft\" aufgeführt.\n\n")
	case "limit":
		fmt.Fprintf(w, "**Abgebrochen:** %s; der Rest ist unten als \"nicht geprüft\" aufgeführt.\n\n", res.LimitHit)
	case "errors":
		fmt.Fprintf(w, "**Abgebrochen:** wiederholte Fehler beim Modellaufruf.\n\n")
	case "baseline":
		fmt.Fprintf(w, "**Nicht gestartet:** %s; es lief keine Session.\n\n", res.LimitHit)
	case "tamper":
		fmt.Fprintf(w, "**Abgebrochen:** eine Session hat den Checkout verändert, ihr Ergebnis wurde verworfen. Das darf nicht passieren; die Berechtigungsregeln prüfen.\n\n")
	}

	if res.Agent && len(res.Sessions) > 0 {
		fmt.Fprintf(w, "## Sessions\n")
		for _, s := range res.Sessions {
			fmt.Fprintf(w, "- %s: %d Einträge, %d geurteilt, %d Turns, Tokens %d / %d, Cache %d gelesen / %d geschrieben, $%.4f", s.Name, s.Entries, s.Judged, s.Turns,
				s.Usage.InputTokens, s.Usage.OutputTokens, s.Usage.CacheReadTokens, s.Usage.CacheWriteTokens, s.CostUSD)
			if s.ReportedUSD > 0 {
				fmt.Fprintf(w, " (Claude Code rechnet $%.4f)", s.ReportedUSD)
			}
			if s.Denials > 0 {
				fmt.Fprintf(w, ", %d Werkzeugaufrufe verweigert", s.Denials)
			}
			if s.Err != "" {
				fmt.Fprintf(w, ", FEHLER: %s", s.Err)
			}
			fmt.Fprintln(w)
			calls := 0
			for _, n := range s.Tools {
				calls += n
			}
			if s.Err == "" && s.Entries > 0 && calls < s.Entries {
				fmt.Fprintf(w, "  - ACHTUNG flach geprüft: %d Werkzeugaufrufe für %d Einträge.\n", calls, s.Entries)
			}
			if len(s.Tools) > 0 {
				var names []string
				for n := range s.Tools {
					names = append(names, n)
				}
				sort.Strings(names)
				var parts []string
				for _, n := range names {
					parts = append(parts, fmt.Sprintf("%s %d", n, s.Tools[n]))
				}
				fmt.Fprintf(w, "  - Werkzeuge: %s\n", strings.Join(parts, ", "))
			}
			for _, d := range s.DeniedCalls {
				fmt.Fprintf(w, "  - verweigert: %s\n", d)
			}
			for _, r := range s.Rejected {
				fmt.Fprintf(w, "  - verworfen: %s\n", r)
			}
		}
		fmt.Fprintln(w)
	}
	if len(res.Relations) > 0 {
		fmt.Fprintf(w, "## Duplikate und Widersprüche\n")
		for _, r := range res.Relations {
			label := "Duplikat?"
			if r.Kind == Contradiction {
				label = "Widerspruch?"
			}
			var ids []string
			for _, id := range r.IDs {
				ids = append(ids, fmt.Sprintf("#%d", id))
			}
			fmt.Fprintf(w, "- %s %s: %s\n", label, strings.Join(ids, ", "), r.Reason)
			for _, id := range r.IDs {
				fmt.Fprintf(w, "  - #%d: \"%s\"\n", id, r.Quotes[id])
			}
			if len(r.Evidence) > 0 {
				fmt.Fprintf(w, "  - Belege: %s\n", strings.Join(r.Evidence, ", "))
			}
		}
		fmt.Fprintln(w)
	}

	writeCovers(w, res.Covers)
	writeProofs(w, res)
	writeGhosts(w, res)
	writeIdle(w, res)

	order := map[string]int{Stale: 0, Correction: 1, Unclear: 2, Holds: 3, Failed: 4, Skipped: 5, NotRun: 6}
	vs := append([]Verdict(nil), res.Verdicts...)
	sort.SliceStable(vs, func(i, j int) bool { return order[vs[i].Kind] < order[vs[j].Kind] })
	for _, v := range vs {
		kind := kindLabel[v.Kind]
		if res.DryRun && v.Kind == NotRun {
			kind = "Trockenlauf"
		}
		fmt.Fprintf(w, "## #%d %s: %s\n", v.Entry.ID, kind, v.Entry.Title)
		fmt.Fprintf(w, "- Typ %s, beobachtet %s\n", v.Entry.Type, v.Entry.Since())
		if len(v.Files) > 0 {
			fmt.Fprintf(w, "- Dateien: %s\n", strings.Join(v.Files, ", "))
		}
		if len(v.Symbols) > 0 {
			fmt.Fprintf(w, "- Symbole: %s\n", strings.Join(v.Symbols, ", "))
		}
		if v.Reason != "" {
			fmt.Fprintf(w, "- Begründung: %s\n", v.Reason)
		}
		if len(v.Evidence) > 0 {
			fmt.Fprintf(w, "- Belege: %s\n", strings.Join(v.Evidence, ", "))
		}
		if len(v.Unverified) > 0 {
			fmt.Fprintf(w, "- Nicht im Kontext auffindbar (ignoriert): %s\n", strings.Join(v.Unverified, ", "))
		}
		if v.Correction != "" && v.Kind == Correction {
			fmt.Fprintf(w, "- Vorschlag:\n\n  > %s\n", strings.ReplaceAll(v.Correction, "\n", "\n  > "))
		}
		if v.In > 0 || v.CacheRead > 0 || v.CacheWrite > 0 {
			fmt.Fprintf(w, "- Tokens %d / %d, Cache %d gelesen / %d geschrieben, $%.4f\n", v.In, v.Out, v.CacheRead, v.CacheWrite, v.CostUSD)
		}
		fmt.Fprintln(w)
	}
}

// writeCovers lists the answers to the regression question (job 5). The test
// sketches have a section of their own, whole: they are the part of the answer
// somebody has to write. The two other kinds are one line each.
func writeCovers(w io.Writer, covers []Cover) {
	var sketches, others []Cover
	for _, c := range covers {
		if c.Kind == CoverSketch {
			sketches = append(sketches, c)
		} else {
			others = append(others, c)
		}
	}
	if len(sketches) > 0 {
		fmt.Fprintf(w, "## Testskizzen (Regressionsschutz)\nZu diesen Pitfalls gibt es keinen Test, der ihre Wiederkehr bemerken würde. Der Gärtner skizziert einen; geschrieben wird er von einem Menschen.\n")
		for _, c := range sketches {
			fmt.Fprintf(w, "- #%d %s\n  - Test: %s\n", c.Entry.ID, c.Entry.Title, c.TestID())
			for _, l := range strings.Split(c.Steps, "\n") {
				fmt.Fprintf(w, "    %s\n", l)
			}
			if c.Reason != "" {
				fmt.Fprintf(w, "  - Begründung: %s\n", c.Reason)
			}
		}
		fmt.Fprintln(w)
	}
	if len(others) > 0 {
		fmt.Fprintf(w, "## Regressionsschutz: vorhandene Tests und nicht Testbares\n")
		for _, c := range others {
			if c.Kind == CoverExisting {
				fmt.Fprintf(w, "- #%d %s: %s deckt ihn ab", c.Entry.ID, c.Entry.Title, c.TestID())
			} else {
				fmt.Fprintf(w, "- #%d %s: nicht testbar", c.Entry.ID, c.Entry.Title)
			}
			if c.Reason != "" {
				fmt.Fprintf(w, " (%s)", c.Reason)
			}
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w)
	}
}

// writeProofs lists the criteria the checkout shows as fulfilled (job 4), each
// with the evidence a person is asked to look at. Nothing in it is recorded until
// a person confirms the note on the review page.
func writeProofs(w io.Writer, res Result) {
	if s := res.LedgerRotation.Summary(); s != "" && res.Agent {
		fmt.Fprintf(w, "Ledger: %s\n\n", strings.Replace(s, "Nicht dran", "Aufträge nicht dran", 1))
	}
	if len(res.Proofs) == 0 {
		return
	}
	fmt.Fprintf(w, "## Ledger: erfüllte Kriterien (Vorschläge)\nDer Gärtner schlägt vor, diese Kriterien als erfüllt zu vermerken. Geschlossen wird nichts; ein Mensch bestätigt jeden Vorschlag auf der Prüfseite.\n")
	for _, p := range res.Proofs {
		fmt.Fprintf(w, "- #%d %s, Kriterium %d: %s\n", p.Request.ID, p.Request.Title, p.Criterion.Number, shorten(p.Criterion.Description, 160))
		for _, it := range p.Items {
			fmt.Fprintf(w, "  - Beleg %s: %s", it.Kind, it.Ref)
			if it.Detail != "" {
				fmt.Fprintf(w, " (%s)", it.Detail)
			}
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "  - Begründung: %s\n", p.Reason)
	}
	fmt.Fprintln(w)
}

// writeGhosts lists the descriptions proposed (job 1), whole: they are the text a
// person is asked to read.
func writeGhosts(w io.Writer, res Result) {
	if s := res.GhostRotation.Summary(); s != "" && res.Agent {
		fmt.Fprintf(w, "%s\n\n", s)
	}
	if len(res.GhostNotes) == 0 && len(res.GhostKept) == 0 {
		return
	}
	fmt.Fprintf(w, "## Beschreibungen (Ghosts)\nDer Gärtner schlägt diese Beschreibungen vor. Geschrieben wird erst, wenn ein Mensch den Vorschlag auf der Prüfseite übernimmt.\n")
	for _, g := range res.GhostNotes {
		fmt.Fprintf(w, "- %s (%s)\n", g.Path, GhostStateLabel(g.GhostPath))
		if g.Reason != "" {
			fmt.Fprintf(w, "  - Begründung: %s\n", g.Reason)
		}
		fmt.Fprintf(w, "  > %s\n", strings.ReplaceAll(g.Description, "\n", "\n  > "))
	}
	if len(res.GhostKept) > 0 {
		var ps []string
		for _, g := range res.GhostKept {
			ps = append(ps, g.Path)
		}
		fmt.Fprintf(w, "- Die bisherige Beschreibung stimmt noch (keine Änderung vorgeschlagen): %s\n", strings.Join(ps, ", "))
	}
	fmt.Fprintln(w)
}

// maxIdleListed bounds the list of idle requests in the report.
const maxIdleListed = 25

// writeIdle lists the open requests nobody has touched for a while, from data
// alone: no model was asked.
func writeIdle(w io.Writer, res Result) {
	if res.IdleDays <= 0 || len(res.Idle) == 0 {
		return
	}
	fmt.Fprintf(w, "## Liegengeblieben\nOffene Aufträge ohne sichtbare Aktivität seit mindestens %d Tagen (angelegt, geändert, Kriterium, Beleg oder Arbeit), am längsten zuerst. Gezählt wird, was das Konto des Gärtners sehen darf: Arbeit in einer privaten Session eines anderen Kontos fehlt hier. Das ist ein Hinweis, kein Urteil.\n", res.IdleDays)
	for i, r := range res.Idle {
		if i == maxIdleListed {
			fmt.Fprintf(w, "- … und %d weitere\n", len(res.Idle)-maxIdleListed)
			break
		}
		fmt.Fprintf(w, "- #%d %s: seit %d Tagen, %d offene Kriterien", r.ID, r.Title, r.Days, r.OpenCriteria)
		if r.Priority != "" {
			fmt.Fprintf(w, ", Priorität %s", shorten(r.Priority, 40))
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w)
}

// Headline is the morning sentence: how many entries the model judged and how
// many of those led to a proposal.
func Headline(vs []Verdict) string {
	checked, proposals := 0, 0
	for _, v := range vs {
		switch v.Kind {
		case Holds, Unclear:
			checked++
		case Stale, Correction:
			checked++
			proposals++
		}
	}
	return fmt.Sprintf("Der Gärtner hat %d Einträge geprüft, %d Vorschläge.", checked, proposals)
}
