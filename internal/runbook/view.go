package runbook

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/knref"
	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
)

// Ergebnisse eines Laufs.
const (
	ResultOK      = "ok"
	ResultFailed  = "failed"
	ResultAborted = "aborted"
)

// Arten eines Laufs: eine Checkliste, die ein Mensch oder Agent selbst abarbeitet
// und zurückmeldet, oder ein ausführbares Ziel, das ctx run im Vordergrund
// ausgeführt hat.
const (
	ModeChecklist = "checklist"
	ModeExec      = "exec"
)

// Abweichungsarten.
const (
	DevAdd    = "add"
	DevChange = "change"
	DevRemove = "remove"
)

// Deviation ist eine Abweichung eines Laufs vom Ablauf: ein zusätzlicher, ein
// geänderter oder ein entfallener Schritt der Anleitung. Sie betrifft nur
// Schritte der Anleitung, nie die ausführbaren Ziele.
type Deviation struct {
	Kind    string `json:"kind"`
	Step    int    `json:"step,omitempty"`
	Title   string `json:"title,omitempty"`
	Command string `json:"command,omitempty"`
	Note    string `json:"note,omitempty"`
}

// Run ist die Rückmeldung zu einem Lauf.
type Run struct {
	ID         int64             `json:"id"`
	Version    int               `json:"version"`
	Target     string            `json:"target,omitempty"`
	Mode       string            `json:"mode"`
	Result     string            `json:"result"`
	Params     map[string]string `json:"params,omitempty"`
	Note       string            `json:"note,omitempty"`
	Machine    string            `json:"machine,omitempty"`
	Person     string            `json:"person"`
	Deviations []Deviation       `json:"deviations,omitempty"`
	CreatedAt  string            `json:"created_at"`
	GitAnchor  *gitanchor.Anchor `json:"git_anchor,omitempty"`
}

// PitfallRef ist eine verlinkte Falle, soweit der Betrachter sie sehen darf.
type PitfallRef struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

// View ist ein Ablauf in einer Fassung, wie ihn API, CLI und MCP liefern.
type View struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Project string `json:"project,omitempty"`
	Machine string `json:"machine,omitempty"`
	Version int    `json:"version"`
	// Versions ist die Zahl der Fassungen, Head die neueste.
	Versions int    `json:"versions"`
	Head     int    `json:"head"`
	Hash     string `json:"hash"`
	Spec     Spec   `json:"spec"`
	Note     string `json:"note,omitempty"`
	Person   string `json:"person"`
	// Approved sagt, dass ein Owner oder Maintainer diese Fassung geschrieben
	// oder freigegeben hat und noch Owner oder Maintainer ist. Nur eine solche
	// Fassung ist ausführbar.
	Approved   bool   `json:"approved"`
	ApprovedBy string `json:"approved_by,omitempty"`
	// ApprovedVersion ist die neueste freigegebene Fassung, 0 ohne.
	ApprovedVersion  int                `json:"approved_version"`
	CreatedAt        string             `json:"created_at"`
	LastSuccess      *Run               `json:"last_success,omitempty"`
	PendingProposals int                `json:"pending_proposals,omitempty"`
	Pitfalls         []PitfallRef       `json:"pitfalls,omitempty"`
	GitAnchor        *gitanchor.Anchor  `json:"git_anchor,omitempty"`
	OriginContext    *originctx.Context `json:"origin_context,omitempty"`
}

// Summary ist eine Zeile der Liste.
type Summary struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	Project         string `json:"project,omitempty"`
	Machine         string `json:"machine,omitempty"`
	Purpose         string `json:"purpose"`
	Head            int    `json:"head"`
	ApprovedVersion int    `json:"approved_version"`
	Targets         int    `json:"targets,omitempty"`
	LastSuccessAt   string `json:"last_success_at,omitempty"`
	LastCommit      string `json:"last_success_commit,omitempty"`
}

// Proposal ist ein Vorschlag zur Aktualisierung eines Ablaufs, der aus einer
// Abweichung entstand. Er ist nie ausführbar und ändert nichts, bis ihn ein
// Owner oder Maintainer freigibt.
type Proposal struct {
	ID          int64       `json:"id"`
	RunbookID   int64       `json:"runbook_id"`
	BaseVersion int         `json:"base_version"`
	RunID       int64       `json:"run_id,omitempty"`
	State       string      `json:"state"`
	Note        string      `json:"note,omitempty"`
	Deviations  []Deviation `json:"deviations"`
	Person      string      `json:"person"`
	DecidedBy   string      `json:"decided_by,omitempty"`
	ResultVer   int         `json:"result_version,omitempty"`
	CreatedAt   string      `json:"created_at"`
}

// Scope nennt die Geltung für Meldungen.
func (v View) Scope() string { return ScopeLabel(v.Project, v.Machine) }

// ScopeLabel beschreibt die Geltung in einem Wort.
func ScopeLabel(project, machine string) string {
	switch {
	case project != "" && machine != "":
		return "project " + project + " on " + machine
	case project != "":
		return "project " + project
	case machine != "":
		return "machine " + machine
	}
	return "global"
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func day(ts string) string {
	if len(ts) >= 10 {
		return ts[:10]
	}
	return ts
}

// LastSuccessLine ist die Zeile "zuletzt erfolgreich am ... auf Commit ...".
func (r *Run) LastSuccessLine() string {
	if r == nil {
		return "last success: none reported yet"
	}
	line := "last success: " + day(r.CreatedAt)
	if r.GitAnchor != nil {
		line += " on commit " + shortSHA(r.GitAnchor.Commit)
		if r.GitAnchor.Branch != "" {
			line += " (" + r.GitAnchor.Branch + ")"
		}
		if r.GitAnchor.Dirty {
			line += " with uncommitted changes"
		}
	}
	if r.Person != "" {
		line += ", by " + r.Person
	}
	if r.Version > 0 {
		line += ", version " + strconv.Itoa(r.Version)
	}
	return line
}

// Checklist druckt die Anleitung mit eingesetzten Parametern als Markdown-
// Checkliste. Parameter ohne Wert bleiben als {{name}} stehen. Alle Texte sind
// einzeilig, die Werte stammen aus dem engen Zeichensatz: nichts davon kann die
// Struktur der Liste verlassen.
func Checklist(v View, values map[string]string) string {
	var b strings.Builder
	s := v.Spec
	fmt.Fprintf(&b, "# Runbook %s (version %d of %d, %s)\n", v.Name, v.Version, v.Head, v.Scope())
	if v.Approved {
		fmt.Fprintf(&b, "Status: approved (%s)\n", v.ApprovedBy)
	} else {
		b.WriteString("Status: NOT APPROVED. Nobody with owner or lead rights has written or approved this version; treat the commands as a proposal and never run them unseen.\n")
		if v.ApprovedVersion > 0 {
			fmt.Fprintf(&b, "The newest approved version is %d; read it with --version %d.\n", v.ApprovedVersion, v.ApprovedVersion)
		}
	}
	fmt.Fprintf(&b, "Purpose: %s\n%s\n", s.Purpose, v.LastSuccess.LastSuccessLine())
	if len(s.Params) > 0 {
		b.WriteString("\nParameters:\n")
		for _, p := range s.Params {
			val := "{{" + p.Name + "}}"
			if x, ok := values[p.Name]; ok {
				val = x
			}
			req := "required"
			if p.Optional || p.Default != "" {
				req = "optional"
			}
			line := fmt.Sprintf("- %s = %s (%s, %s)", p.Name, val, p.Type, req)
			if p.Description != "" {
				line += ": " + p.Description
			}
			b.WriteString(line + "\n")
		}
	}
	if len(s.Preconditions) > 0 {
		b.WriteString("\nPreconditions:\n")
		for _, p := range s.Preconditions {
			fmt.Fprintf(&b, "- [ ] %s\n", Expand(p, values))
		}
	}
	writeSteps(&b, "Steps", s.Steps, values)
	writeSteps(&b, "Rollback", s.Rollback, values)
	if len(v.Pitfalls) > 0 {
		b.WriteString("\nPitfalls:\n")
		for _, p := range v.Pitfalls {
			fmt.Fprintf(&b, "- %s %s\n", knref.Format(p.ID), p.Title)
		}
	}
	if len(s.Targets) > 0 {
		b.WriteString("\nExecutable targets (ctx run " + v.Name + " --target NAME, dry run unless --run):\n")
		for _, t := range s.Targets {
			fmt.Fprintf(&b, "- %s: %s\n", t.Name, t.Comment)
		}
	}
	b.WriteString("\n" + reportLine(v.Name))
	return b.String()
}

func writeSteps(b *strings.Builder, head string, steps []Step, values map[string]string) {
	if len(steps) == 0 {
		return
	}
	b.WriteString("\n" + head + ":\n")
	for i, st := range steps {
		fmt.Fprintf(b, "%d. [ ] %s\n", i+1, Expand(st.Title, values))
		if st.Command != "" {
			fmt.Fprintf(b, "   $ %s\n", Expand(st.Command, values))
		}
		if st.Check != "" {
			fmt.Fprintf(b, "   check: %s\n", Expand(st.Check, values))
		}
	}
}

// Briefing ist die Fassung für die automatische Auslieferung: dieselbe
// Checkliste, bei Überlänge auf die Schritttitel gekürzt, mit dem Grund des
// Treffers. limit ist die Zeichengrenze.
func Briefing(v View, reason string, limit int) string {
	full := Checklist(v, nil)
	head := fmt.Sprintf("[runbook %s, triggered by %s]\n", v.Name, reason)
	if len(head)+len(full) <= limit {
		return head + full
	}
	// The two closing lines are reserved before any step is written: the
	// pointer to the full checklist and the line that asks for the report. A
	// shortened runbook that lost the second would never be reported.
	var b strings.Builder
	b.WriteString(head)
	fmt.Fprintf(&b, "# Runbook %s (version %d)\nPurpose: %s\n%s\n", v.Name, v.Version, v.Spec.Purpose, v.LastSuccess.LastSuccessLine())
	pointer := fmt.Sprintf("Full checklist with parameters: ctx run %s, or the MCP tool runbook action=get name=%s.\n", v.Name, v.Name)
	report := reportLine(v.Name)
	reserved := len(pointer) + len(report) + len("...\n")
	for i, st := range v.Spec.Steps {
		line := fmt.Sprintf("%d. %s\n", i+1, st.Title)
		if b.Len()+len(line)+reserved > limit {
			b.WriteString("...\n")
			break
		}
		b.WriteString(line)
	}
	b.WriteString(pointer)
	b.WriteString(report)
	return b.String()
}

// reportLine is the line that closes a runbook the server delivers, in full or
// shortened. The hook budget tells a runbook that is whole by it
// (internal/hookbudget): do not reword its first words.
func reportLine(name string) string {
	return fmt.Sprintf("When done, report the outcome: ctx runbook report %s ok|failed [--note TEXT] [--deviation 'add:N:title:command'].\n", name)
}

// Lines schreibt eine Fassung als Zeilen für den Vergleich zweier Fassungen.
func Lines(s Spec) []string {
	var l []string
	// Jede Zeile des Vergleichs läuft durch Escape: Zeichen außerhalb von
	// druckbarem ASCII erscheinen als \uXXXX, nichts sieht anders aus, als es ist.
	add := func(f string, a ...any) { l = append(l, Escape(fmt.Sprintf(f, a...))) }
	add("purpose: %s", s.Purpose)
	for _, p := range s.Params {
		add("param %s: %s, optional=%t, default=%q, values=%s, %s", p.Name, p.Type, p.Optional, p.Default, strings.Join(p.Values, "|"), p.Description)
	}
	for _, p := range s.Preconditions {
		add("precondition: %s", p)
	}
	for i, st := range s.Steps {
		add("step %d: %s", i+1, st.Title)
		if st.Command != "" {
			add("step %d command: %s", i+1, st.Command)
		}
		if st.Check != "" {
			add("step %d check: %s", i+1, st.Check)
		}
	}
	for i, st := range s.Rollback {
		add("rollback %d: %s", i+1, st.Title)
		if st.Command != "" {
			add("rollback %d command: %s", i+1, st.Command)
		}
	}
	for _, id := range s.Pitfalls {
		add("pitfall: %s", knref.Format(id))
	}
	for _, k := range s.Triggers.Keywords {
		add("trigger keyword: %s", k)
	}
	for _, c := range s.Triggers.Commands {
		add("trigger command: %s", c)
	}
	for _, p := range s.Triggers.Paths {
		add("trigger path: %s", p)
	}
	for _, t := range s.Targets {
		add("target %s: %s", t.Name, t.Comment)
		for i, st := range t.Steps {
			add("target %s step %d: %s", t.Name, i+1, QuoteArgv(st.Argv))
		}
	}
	return l
}

// Diff zeigt, was eine Fassung gegenüber einer anderen ändert, zeilenweise mit
// + und -. Ohne frühere Fassung (old == nil) ist alles hinzugefügt. Gleiche
// Zeilen erscheinen nicht, außer als Zähler; wer freigibt, soll das Neue sehen,
// nicht den Rest.
func Diff(old *Spec, new Spec) string {
	var a []string
	if old != nil {
		a = Lines(*old)
	}
	b := Lines(new)
	// Längste gemeinsame Teilfolge; die Zeilenzahl ist durch die Grenzen klein.
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var out strings.Builder
	same := 0
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			same++
			i++
			j++
		case j < m && (i == n || lcs[i][j+1] >= lcs[i+1][j]):
			out.WriteString("+ " + b[j] + "\n")
			j++
		default:
			out.WriteString("- " + a[i] + "\n")
			i++
		}
	}
	if out.Len() == 0 {
		return "(no change)\n"
	}
	if same > 0 {
		fmt.Fprintf(&out, "(%d unchanged lines)\n", same)
	}
	return out.String()
}

// ApplyDeviations wendet Abweichungen auf die Schritte der Anleitung an und
// liefert die neue, geprüfte Fassung. Ausführbare Ziele bleiben unverändert:
// ein Vorschlag aus einem Lauf ändert nie, was ghosttree ausführbar macht.
func ApplyDeviations(s Spec, devs []Deviation) (Spec, error) {
	steps := append([]Step(nil), s.Steps...)
	for _, d := range devs {
		switch d.Kind {
		case DevAdd:
			if strings.TrimSpace(d.Title) == "" && strings.TrimSpace(d.Command) == "" {
				return Spec{}, fmt.Errorf("an added step needs a title or a command")
			}
			title := d.Title
			if strings.TrimSpace(title) == "" {
				title = d.Command
			}
			st := Step{Title: title, Command: d.Command}
			pos := d.Step - 1
			if d.Step <= 0 || pos > len(steps) {
				pos = len(steps)
			}
			steps = append(steps[:pos], append([]Step{st}, steps[pos:]...)...)
		case DevChange:
			if d.Step < 1 || d.Step > len(steps) {
				return Spec{}, fmt.Errorf("deviation: no step %d to change", d.Step)
			}
			if d.Command != "" {
				steps[d.Step-1].Command = d.Command
			}
			if d.Title != "" {
				steps[d.Step-1].Title = d.Title
			}
		case DevRemove:
			if d.Step < 1 || d.Step > len(steps) {
				return Spec{}, fmt.Errorf("deviation: no step %d to remove", d.Step)
			}
			steps = append(steps[:d.Step-1], steps[d.Step:]...)
		default:
			return Spec{}, fmt.Errorf("deviation kind %q: use add, change or remove", truncate(d.Kind, 20))
		}
	}
	out := s
	out.Steps = steps
	return Normalize(out)
}

// NormalizeDeviations prüft Abweichungen: Art, Schrittnummer, einzeilige Texte.
func NormalizeDeviations(in []Deviation) ([]Deviation, error) {
	if len(in) > MaxLines {
		return nil, fmt.Errorf("more than %d deviations", MaxLines)
	}
	var out []Deviation
	for _, d := range in {
		var err error
		switch d.Kind {
		case DevAdd, DevChange, DevRemove:
		default:
			return nil, fmt.Errorf("deviation kind %q: use add, change or remove", truncate(d.Kind, 20))
		}
		if d.Step < 0 || d.Step > MaxSteps+1 {
			return nil, fmt.Errorf("deviation step %d is out of range", d.Step)
		}
		if d.Title, err = checkLine("deviation title", d.Title, maxTitle, false); err != nil {
			return nil, err
		}
		if d.Command, err = checkLine("deviation command", d.Command, maxCommand, false); err != nil {
			return nil, err
		}
		if d.Note, err = checkLine("deviation note", d.Note, maxTitle, false); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// CheckNote prüft eine freie Notiz einer Rückmeldung.
func CheckNote(note string) (string, error) { return checkLine("note", note, maxTitle*2, false) }

// ParseDeviation liest die Kurzschreibweise der Kommandozeile
// "art:schritt:titel:befehl", z. B. "add:3:Cache leeren:make clean". Titel und
// Befehl dürfen fehlen; der Befehl darf Doppelpunkte enthalten.
func ParseDeviation(s string) (Deviation, error) {
	parts := strings.SplitN(s, ":", 4)
	if len(parts) < 2 {
		return Deviation{}, fmt.Errorf("deviation %q: write kind:step[:title[:command]], e.g. add:3:Clear cache:make clean", truncate(s, 40))
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil {
		return Deviation{}, fmt.Errorf("deviation %q: the step must be a number", truncate(s, 40))
	}
	d := Deviation{Kind: parts[0], Step: n}
	if len(parts) > 2 {
		d.Title = parts[2]
	}
	if len(parts) > 3 {
		d.Command = parts[3]
	}
	return d, nil
}
