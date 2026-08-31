package agentbench

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

type ExposureClass string

const (
	ExposureRetrospective ExposureClass = "retrospective"
	ExposureTransfer      ExposureClass = "transfer"
	ExposureProspective   ExposureClass = "prospective"
)

func (e Exposure) Class() ExposureClass {
	switch {
	case e.ExactPromptSeen || e.EquivalentSeen:
		return ExposureRetrospective
	case e.AnswerFactSeen:
		return ExposureTransfer
	default:
		return ExposureProspective
	}
}

type GroupSummary struct {
	Group          string  `json:"group"`
	Arm            ArmName `json:"arm"`
	Runs           int     `json:"runs"`
	FactRecall     float64 `json:"fact_recall"`
	ClaimPrecision float64 `json:"claim_precision"`
	ContradictRate float64 `json:"contradiction_rate"`
	AbstentionRate float64 `json:"abstention_rate"`
}

type Report struct {
	Campaign   Campaign        `json:"campaign"`
	Records    []RunRecord     `json:"-"`
	ByExposure []GroupSummary  `json:"by_exposure"`
	ByCategory []GroupSummary  `json:"by_category"`
	Effects    []PairedEffect  `json:"effects"`
	Failures   map[Failure]int `json:"failures"`
}

func BuildReport(campaign Campaign, records []RunRecord) Report {
	report := Report{Campaign: campaign, Records: records, Failures: map[Failure]int{}}
	for _, record := range records {
		if record.Failure != FailureNone {
			report.Failures[record.Failure]++
		}
	}
	report.ByExposure = summarise(records, func(r RunRecord) string { return string(r.Exposure.Class()) })
	report.ByCategory = summarise(records, func(r RunRecord) string { return string(r.Category) })
	return report
}

func summarise(records []RunRecord, key func(RunRecord) string) []GroupSummary {
	type bucket struct {
		runs                                   int
		recall, precision, contradict, abstain float64
	}
	buckets := map[string]map[ArmName]*bucket{}
	for _, record := range records {
		if record.Failure != FailureNone {
			continue
		}
		k := key(record)
		if buckets[k] == nil {
			buckets[k] = map[ArmName]*bucket{}
		}
		if buckets[k][record.Arm] == nil {
			buckets[k][record.Arm] = &bucket{}
		}
		b := buckets[k][record.Arm]
		b.runs++
		b.recall += record.Score.FactRecall
		b.precision += record.Score.ClaimPrecision
		b.contradict += record.Score.ContradictRate
		b.abstain += record.Score.AbstentionRate
	}
	var out []GroupSummary
	for group, arms := range buckets {
		for arm, b := range arms {
			n := float64(b.runs)
			out = append(out, GroupSummary{
				Group: group, Arm: arm, Runs: b.runs,
				FactRecall: b.recall / n, ClaimPrecision: b.precision / n,
				ContradictRate: b.contradict / n, AbstentionRate: b.abstain / n,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Arm < out[j].Arm
	})
	return out
}

// writeProvenance names, for every arm, the exact state that was measured.
// Without it the report answers "ghosttree won" but not "which tree" — and
// the second question is the one a sceptic asks first.
func (r Report) writeProvenance(w io.Writer) error {
	if len(r.Campaign.Arms) == 0 {
		return nil
	}
	fmt.Fprint(w, "## Gemessene Zustände\n\n")
	fmt.Fprintln(w, "| Arm | Snapshot | Datenbank-Hash | Sessions | Werkzeug | Verdichtungsmodell |")
	fmt.Fprintln(w, "|---|---|---|---|---|---|")
	for _, arm := range r.Campaign.Arms {
		build, ok := r.Campaign.Builds[arm]
		if !ok {
			fmt.Fprintf(w, "| %s | — | — | — | — | — |\n", arm)
			continue
		}
		fmt.Fprintf(w, "| %s | %s | %s | %d | %s | %s |\n",
			arm, orDash(build.SnapshotName), orDash(build.DatabaseSHA256),
			build.SourceSessionCount, orDash(build.ToolVersion), orDash(build.SummarizerModel))
	}
	fmt.Fprintln(w)
	return nil
}

func orDash(value string) string {
	if value == "" {
		return "—"
	}
	return value
}

func (r Report) WriteJSONL(w io.Writer) error {
	encoder := json.NewEncoder(w)
	for _, record := range r.Records {
		if err := encoder.Encode(record); err != nil {
			return err
		}
	}
	return nil
}

func (r Report) WriteMarkdown(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "# agentbench: %s\n\n", r.Campaign.Name); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Commit `%s`, Cutoff %s, Modell `%s`.\n\n",
		r.Campaign.RepoCommit, r.Campaign.KnowledgeCutoff.Format("2006-01-02T15:04:05Z"),
		r.Campaign.Agent.ModelID); err != nil {
		return err
	}

	if err := r.writeProvenance(w); err != nil {
		return err
	}

	fmt.Fprint(w, "## Gepaarte Effekte\n\n")
	fmt.Fprintln(w, "| Kontrast | Aufgaben | Effekt | 95 % KI |")
	fmt.Fprintln(w, "|---|---|---|---|")
	for _, e := range r.Effects {
		fmt.Fprintf(w, "| %s − %s | %d | %+.3f | [%.2f, %.2f] |\n",
			e.Treatment, e.Control, e.Tasks, e.Mean, e.LowerCI, e.UpperCI)
	}

	writeGroups := func(title string, groups []GroupSummary) {
		fmt.Fprintf(w, "\n## %s\n\n", title)
		fmt.Fprintln(w, "| Gruppe | Arm | Läufe | Treffer | Präzision | Widerspruch | Enthaltung |")
		fmt.Fprintln(w, "|---|---|---|---|---|---|---|")
		for _, g := range groups {
			fmt.Fprintf(w, "| %s | %s | %d | %.3f | %.3f | %.3f | %.3f |\n",
				g.Group, g.Arm, g.Runs, g.FactRecall, g.ClaimPrecision, g.ContradictRate, g.AbstentionRate)
		}
	}
	writeGroups("Nach Expositionsklasse", r.ByExposure)
	writeGroups("Nach Kategorie", r.ByCategory)

	if len(r.Failures) > 0 {
		fmt.Fprint(w, "\n## Fehler\n\n")
		kinds := make([]string, 0, len(r.Failures))
		for kind := range r.Failures {
			kinds = append(kinds, string(kind))
		}
		sort.Strings(kinds)
		for _, kind := range kinds {
			fmt.Fprintf(w, "- %s: %d\n", kind, r.Failures[Failure(kind)])
		}
	}
	return nil
}
