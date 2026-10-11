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
	var in, out int
	for _, v := range res.Verdicts {
		in, out = in+v.In, out+v.Out
	}
	if res.DryRun {
		fmt.Fprintf(w, "Geschätzt: %d Eingabe- und %d Ausgabe-Tokens, ca. $%.4f; Obergrenze bei vollen Antworten $%.4f.\n\n", in, out, res.Spent, res.UpperBoundUSD)
	} else {
		fmt.Fprintf(w, "Gemessen: %d Eingabe- und %d Ausgabe-Tokens, $%.4f von höchstens $%.2f.\n\n", in, out, res.Spent, res.MaxUSD)
	}
	switch res.StoppedBy {
	case "budget":
		fmt.Fprintf(w, "**Abgebrochen:** Budgetdeckel erreicht; der Rest ist unten als \"nicht geprüft\" aufgeführt.\n\n")
	case "errors":
		fmt.Fprintf(w, "**Abgebrochen:** wiederholte Fehler beim Modellaufruf.\n\n")
	}

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
		if v.In > 0 {
			fmt.Fprintf(w, "- Tokens %d / %d, $%.4f\n", v.In, v.Out, v.CostUSD)
		}
		fmt.Fprintln(w)
	}
}
