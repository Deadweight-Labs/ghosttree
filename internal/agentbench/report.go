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
	// Aufwand je Lauf. Ein Gedaechtnis, das die Trefferquote wenig hebt und
	// die Rechnung stark, ist ein anderes Produkt als eines, das beides hebt.
	Turns   float64 `json:"turns"`
	CostUSD float64 `json:"cost_usd"`
}

// SourceSummary splits an arm's recall by where the answer could be found.
//
// It exists because the two halves answer different questions and a single
// number that adds them up answers neither. On facts that stand in the
// repository, every arm can find the answer and a difference says how well a
// memory guides the search. On facts that stand only in the memory, an arm
// without one scores zero by construction, and the difference says what it was
// worth to write the thing down. A sceptic who suspects the second is being
// sold as the first is right to look, and this table is where they look.
type SourceSummary struct {
	Arm ArmName `json:"arm"`
	// Runs zaehlt nur Laeufe, in denen die jeweilige Klasse ueberhaupt
	// vorkommt — eine Aufgabe ohne Gedaechtnis-Fakt hat keine
	// Gedaechtnis-Trefferquote, auch keine von null.
	RepoRuns     int     `json:"repo_runs"`
	RepoRecall   float64 `json:"repo_recall"`
	MemoryRuns   int     `json:"memory_runs"`
	MemoryRecall float64 `json:"memory_recall"`
}

func summariseBySource(records []RunRecord, arms []ArmName) []SourceSummary {
	byArm := map[ArmName]*SourceSummary{}
	for _, record := range records {
		if record.Failure != FailureNone {
			continue
		}
		s := byArm[record.Arm]
		if s == nil {
			s = &SourceSummary{Arm: record.Arm}
			byArm[record.Arm] = s
		}
		if record.Score.RepoWeight > 0 {
			s.RepoRuns++
			s.RepoRecall += record.Score.RepoRecall
		}
		if record.Score.MemoryWeight > 0 {
			s.MemoryRuns++
			s.MemoryRecall += record.Score.MemoryRecall
		}
	}
	var out []SourceSummary
	for _, arm := range arms {
		s, ok := byArm[arm]
		if !ok {
			continue
		}
		if s.RepoRuns > 0 {
			s.RepoRecall /= float64(s.RepoRuns)
		}
		if s.MemoryRuns > 0 {
			s.MemoryRecall /= float64(s.MemoryRuns)
		}
		out = append(out, *s)
	}
	return out
}

// Isolation records how tightly the runs were fenced in. It belongs in the
// report because a number produced without a seal means something different
// from the same number produced with one, and the difference is not visible
// in the number.
type Isolation struct {
	Runtime        string   `json:"runtime"`
	Image          string   `json:"image,omitempty"`
	Network        string   `json:"network,omitempty"`
	AllowedDomains []string `json:"allowed_domains,omitempty"`
	Sealed         bool     `json:"sealed"`
}

type Report struct {
	Campaign   Campaign        `json:"campaign"`
	Isolation  Isolation       `json:"isolation"`
	Records    []RunRecord     `json:"-"`
	ByExposure []GroupSummary  `json:"by_exposure"`
	ByCategory []GroupSummary  `json:"by_category"`
	BySource   []SourceSummary `json:"by_source"`
	// ByProvenance trennt Aufgaben, deren Ground Truth nach dem Anblick der
	// Antworten korrigiert wurde, von denen, bei denen das nicht passiert ist.
	ByProvenance []GroupSummary  `json:"by_provenance"`
	Effects      []PairedEffect  `json:"effects"`
	Failures     map[Failure]int `json:"failures"`
	Suspect      []TaskSuspicion `json:"suspect_tasks,omitempty"`
}

func BuildReport(campaign Campaign, records []RunRecord) Report {
	report := Report{Campaign: campaign, Records: records, Failures: map[Failure]int{}}
	for _, record := range records {
		if record.Failure != FailureNone {
			report.Failures[record.Failure]++
		}
	}
	report.Suspect = SuspectTasks(records, campaign.Arms)
	report.ByExposure = summarise(records, func(r RunRecord) string { return string(r.Exposure.Class()) })
	report.ByCategory = summarise(records, func(r RunRecord) string { return string(r.Category) })
	report.BySource = summariseBySource(records, campaign.Arms)
	report.ByProvenance = summarise(records, func(r RunRecord) string {
		if r.DevelopmentData {
			return "entwickelt"
		}
		return "zurueckgehalten"
	})
	return report
}

func summarise(records []RunRecord, key func(RunRecord) string) []GroupSummary {
	type bucket struct {
		runs                                   int
		recall, precision, contradict, abstain float64
		turns, cost                            float64
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
		b.turns += float64(record.Transcript.Turns)
		b.cost += record.Transcript.CostUSD
	}
	var out []GroupSummary
	for group, arms := range buckets {
		for arm, b := range arms {
			n := float64(b.runs)
			out = append(out, GroupSummary{
				Group: group, Arm: arm, Runs: b.runs,
				FactRecall: b.recall / n, ClaimPrecision: b.precision / n,
				ContradictRate: b.contradict / n, AbstentionRate: b.abstain / n,
				Turns: b.turns / n, CostUSD: b.cost / n,
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
	// "Werkzeug beim Bauen", nicht "Werkzeug im Lauf": hier steht, womit der
	// Zustand hergestellt wurde. Im Container laeuft kein ctx — der Arm liest
	// den Spiegel auf der Platte. Die alte Ueberschrift las sich wie das
	// Gegenteil und behauptete damit ein Werkzeug, das gar nicht dabei war.
	fmt.Fprintln(w, "| Arm | Snapshot | Datenbank-Hash | Sessions | Werkzeug beim Bauen | Verdichtungsmodell |")
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

// writeIsolation says in one paragraph whether the run could reach the open
// internet. An unsealed campaign is not worthless, but every claim from it
// carries the caveat, so the caveat is printed next to the numbers.
func (r Report) writeIsolation(w io.Writer) {
	fmt.Fprint(w, "## Abschottung\n\n")
	// Eine Nachbewertung kennt die Abschottung nicht: sie liest Transkripte,
	// keine Laufzeitumgebung. Das Fehlen als "offenes Netz" zu drucken waere
	// eine falsche Aussage ueber einen Lauf, der abgedichtet war — und zwar
	// eine, die den Bericht schlechter macht, als gar nichts zu sagen.
	if r.Isolation.Runtime == "" && !r.Isolation.Sealed {
		fmt.Fprint(w, "Nicht aufgezeichnet. Dieser Bericht entstand aus vorhandenen Transkripten; "+
			"wie die Läufe abgeschottet waren, steht im Bericht des Laufs, der sie erzeugt hat.\n\n")
		return
	}
	fmt.Fprintf(w, "Laufzeit `%s`", r.Isolation.Runtime)
	if r.Isolation.Image != "" {
		fmt.Fprintf(w, ", Abbild `%s`", r.Isolation.Image)
	}
	if r.Isolation.Sealed {
		fmt.Fprintf(w, ", internes Netz `%s`, erreichbar nur: %v.\n\n",
			r.Isolation.Network, r.Isolation.AllowedDomains)
		return
	}
	fmt.Fprint(w, ", **ohne Netzabdichtung**. Die Läufe konnten das offene Netz erreichen; "+
		"ein Arm kann fehlendes Gedächtnis durch Recherche ersetzt haben. "+
		"Effekte aus diesem Lauf sind Untergrenzen mit unbekanntem Rauschanteil.\n\n")
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

	r.writeIsolation(w)

	if err := r.writeProvenance(w); err != nil {
		return err
	}

	fmt.Fprint(w, "## Gepaarte Effekte\n\n")
	fmt.Fprintln(w, "| Kontrast | Metrik | Aufgaben | Effekt | 95 % KI |")
	fmt.Fprintln(w, "|---|---|---|---|---|")
	for _, e := range r.Effects {
		fmt.Fprintf(w, "| %s − %s | %s | %d | %+.3f | [%+.3f, %+.3f] |\n",
			e.Treatment, e.Control, e.Metric, e.Tasks, e.Mean, e.LowerCI, e.UpperCI)
	}

	writeGroups := func(title string, groups []GroupSummary) {
		fmt.Fprintf(w, "\n## %s\n\n", title)
		fmt.Fprintln(w, "| Gruppe | Arm | Läufe | Treffer | Präzision | Widerspruch | Enthaltung | Züge | USD |")
		fmt.Fprintln(w, "|---|---|---|---|---|---|---|---|---|")
		for _, g := range groups {
			fmt.Fprintf(w, "| %s | %s | %d | %.3f | %.3f | %.3f | %.3f | %.1f | %.3f |\n",
				g.Group, g.Arm, g.Runs, g.FactRecall, g.ClaimPrecision, g.ContradictRate,
				g.AbstentionRate, g.Turns, g.CostUSD)
		}
	}
	writeGroups("Nach Expositionsklasse", r.ByExposure)
	writeGroups("Nach Kategorie", r.ByCategory)
	if len(r.ByProvenance) > 1 {
		fmt.Fprint(w, "\n_\"entwickelt\" heißt: Wortlaut oder Ground Truth wurden korrigiert, "+
			"nachdem die Antworten der Arme vorlagen. Die Korrekturen waren richtig — ein Schlüssel, "+
			"der eine wahre Antwort ablehnt, ist falsch —, aber sie entstanden mit den Antworten vor "+
			"Augen. \"zurueckgehalten\" heißt: das ist hier nicht passiert._\n")
		writeGroups("Nach Datenherkunft", r.ByProvenance)
	}

	if len(r.BySource) > 0 {
		fmt.Fprint(w, "\n## Nach Wissensquelle\n\n")
		fmt.Fprint(w, "Zwei Fragen, die nicht zusammengezählt werden dürfen. Links steht die Antwort im "+
			"Repository und jeder Arm könnte sie finden — gemessen wird, ob ein Gedächtnis die Suche besser "+
			"führt. Rechts steht sie nur im Gedächtnis; ein Arm ohne eines erreicht dort null, und das ist "+
			"keine Schwäche, sondern die Bauart.\n\n")
		fmt.Fprintln(w, "| Arm | Läufe (im Repo) | Treffer (im Repo) | Läufe (nur Gedächtnis) | Treffer (nur Gedächtnis) |")
		fmt.Fprintln(w, "|---|---|---|---|---|")
		for _, s := range r.BySource {
			fmt.Fprintf(w, "| %s | %d | %.3f | %d | %.3f |\n",
				s.Arm, s.RepoRuns, s.RepoRecall, s.MemoryRuns, s.MemoryRecall)
		}
	}

	if len(r.Suspect) > 0 {
		fmt.Fprint(w, "\n## Verdächtige Aufgaben\n\n")
		fmt.Fprint(w, "Übereinstimmung über Arme hinweg, die sich sonst unterscheiden, ist ein Hinweis "+
			"auf die Aufgabe, nicht auf die Arme — sei es, dass alle null erreichen, oder dass mehrere "+
			"dieselbe Antwort geben, die der Schlüssel ablehnt. Die Ground Truth gehört geprüft, bevor "+
			"diese Zeilen in eine Zahl eingehen.\n\n")
		fmt.Fprintln(w, "| Aufgabe | Läufe | Arme | Treffer | Grund |")
		fmt.Fprintln(w, "|---|---|---|---|---|")
		for _, s := range r.Suspect {
			fmt.Fprintf(w, "| %s | %d | %d | %.3f | %s |\n", s.TaskID, s.Runs, s.Arms, s.Recall, s.Reason)
		}
	}

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
