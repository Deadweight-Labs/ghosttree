// Package runbook ist das Modell der Abläufe (REQ-604): benannte Anleitungen
// mit Parametern, Vorbedingungen, Schritten, Prüfungen, Rückweg, Auslösern und
// optional ausführbaren Zielen. Das Paket kennt weder Speicher noch Server; es
// prüft, hasht, setzt Parameter ein und gleicht Auslöser ab.
//
// Zwei Linien halten die Sicherheit zusammen. Erstens sind alle Texte einzeilig
// ohne Steuerzeichen und alle Parameterwerte auf einen engen Zeichensatz
// beschränkt, sodass ein Wert weder eine Shell noch eine Markdown-Zeile noch
// eine Option verwechseln kann. Zweitens sind ausführbare Schritte argv-Listen:
// ein Parameter ersetzt einen Platzhalter innerhalb genau eines Arguments, es
// gibt keine Shell, die etwas neu zerlegen könnte.
package runbook

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Grenzen. Sie sind klein, weil ein Ablauf eine Anleitung ist und kein
// Dokument; ein Ablauf, der sie sprengt, gehört in ein Dokument.
const (
	MaxSpecBytes   = 64 << 10
	MaxSteps       = 60
	MaxParams      = 20
	MaxTargets     = 20
	MaxTargetSteps = 30
	MaxArgv        = 50
	MaxTriggers    = 20
	MaxLines       = 40
	maxPurpose     = 500
	maxTitle       = 200
	maxCommand     = 1000
	maxArg         = 500
	maxValue       = 200
)

var (
	nameRE        = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	paramNameRE   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	placeholderRE = regexp.MustCompile(`\{\{([a-z][a-z0-9_]{0,31})\}\}`)
	// safeValueRE ist der gesamte Zeichensatz eines Parameterwerts: nichts, was
	// eine Shell (auch die entfernte Fischshell hinter ssh), eine Markdown-Zeile
	// oder ein Glob deuten würde. Kein Leerzeichen, kein Anführungszeichen, kein
	// $, kein Backtick, kein Semikolon, keine Tilde.
	safeValueRE = regexp.MustCompile(`^[A-Za-z0-9._/:@+,=-]{1,200}$`)
	intRE       = regexp.MustCompile(`^[0-9]{1,18}$`)
	shaRE       = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
)

// Parameterarten.
const (
	TypeString = "string"
	TypeInt    = "int"
	TypeSHA    = "sha"
	TypeBool   = "bool"
	TypeEnum   = "enum"
	TypePath   = "path"
)

// Param ist ein Parameter des Ablaufs. Required ist als Gegenteil gespeichert
// (Optional), damit ein weggelassenes Feld die sichere Lesart bekommt: Pflicht.
type Param struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Optional    bool     `json:"optional,omitempty"`
	Default     string   `json:"default,omitempty"`
	Values      []string `json:"values,omitempty"`
}

// Step ist ein Schritt der Anleitung. Command ist der exakte Befehl als Text für
// den Menschen oder Agenten; er wird nie von ghosttree ausgeführt. Was
// ausführbar sein soll, steht in Target.
type Step struct {
	Title   string `json:"title"`
	Command string `json:"command,omitempty"`
	Check   string `json:"check,omitempty"`
}

// ExecStep ist ein ausführbarer Schritt: eine argv-Liste, ohne Shell.
type ExecStep struct {
	Argv []string `json:"argv"`
}

// Target ist ein ausführbares Ziel wie bei make: ein Kommentar in einer Zeile
// und feste Befehle.
type Target struct {
	Name    string     `json:"name"`
	Comment string     `json:"comment"`
	Steps   []ExecStep `json:"steps"`
}

// Triggers sind die Auslöser: Stichwörter im Prompt, Befehlsmuster in einem
// Bash-Aufruf, Pfadmuster der berührten Dateien.
type Triggers struct {
	Keywords []string `json:"keywords,omitempty"`
	Commands []string `json:"commands,omitempty"`
	Paths    []string `json:"paths,omitempty"`
}

// Spec ist der Inhalt einer Fassung.
type Spec struct {
	Purpose       string   `json:"purpose"`
	Params        []Param  `json:"params,omitempty"`
	Preconditions []string `json:"preconditions,omitempty"`
	Steps         []Step   `json:"steps,omitempty"`
	Rollback      []Step   `json:"rollback,omitempty"`
	// Pitfalls sind Nummern von Wissenseinträgen (Fallen), die zum Ablauf gehören.
	Pitfalls []int64  `json:"pitfalls,omitempty"`
	Triggers Triggers `json:"triggers,omitempty"`
	Targets  []Target `json:"targets,omitempty"`
}

// ValidName sagt, ob name ein Ablaufname ist: Kleinbuchstaben, Ziffern, Punkt,
// Binde- und Unterstrich, einzeilig, höchstens 64 Zeichen.
func ValidName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("runbook name %q: use lowercase letters, digits, '.', '_' or '-' (at most 64, starting with a letter or digit)", truncate(name, 40))
	}
	return nil
}

// ValidTargetName gilt für Ziele und folgt denselben Regeln wie Ablaufnamen.
func ValidTargetName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("target name %q: use lowercase letters, digits, '.', '_' or '-'", truncate(name, 40))
	}
	return nil
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return sanitizeForError(s)
	}
	r := []rune(sanitizeForError(s))
	return string(r[:n]) + "..."
}

// BadRune sagt, ob ein Zeichen in keinem Text eines Ablaufs vorkommen darf:
// Steuerzeichen, Zeilen- und Absatztrenner, Privatgebrauch und alle
// Format-Zeichen (Kategorie Cf). Zu Cf gehören die Bidi-Steuerzeichen
// (U+200E/F, U+202A-E, U+2066-9) und die Nullbreiten-Zeichen (U+200B-D,
// U+FEFF): sie lassen einen Befehl im Vergleich vor der Freigabe anders
// aussehen, als er ausgeführt wird.
func BadRune(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Co, r) || r == '\u2028' || r == '\u2029'
}

func sanitizeForError(s string) string {
	return strings.Map(func(r rune) rune {
		if BadRune(r) {
			return '?'
		}
		return r
	}, s)
}

// Escape schreibt einen Text so, dass jedes Zeichen außer druckbarem ASCII
// sichtbar als \uXXXX erscheint. Der Vergleich vor einer Freigabe und der
// Probelauf zeigen Texte so: was ein Mensch dort liest, ist genau das, was
// ausgeführt wird, auch bei Zeichen, die gleich aussehen (kyrillisches a) oder
// gar nicht.
func Escape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 0x20 && r < 0x7f:
			b.WriteRune(r)
		case r > 0xffff:
			fmt.Fprintf(&b, "\\U%08x", r)
		default:
			fmt.Fprintf(&b, "\\u%04x", r)
		}
	}
	return b.String()
}

// checkLine prüft einen Text: gültiges UTF-8, keine Steuerzeichen (also auch
// kein Zeilenumbruch), nicht länger als max Zeichen.
func checkLine(what, s string, max int, required bool) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		if required {
			return "", fmt.Errorf("%s is required", what)
		}
		return "", nil
	}
	if !utf8.ValidString(s) {
		return "", fmt.Errorf("%s is not valid UTF-8", what)
	}
	for _, r := range s {
		if BadRune(r) {
			return "", fmt.Errorf("%s must be a single line without control or invisible formatting characters", what)
		}
	}
	if utf8.RuneCountInString(s) > max {
		return "", fmt.Errorf("%s is longer than %d characters", what, max)
	}
	return s, nil
}

// CheckValue prüft einen Parameterwert gegen die Art des Parameters und liefert
// die kanonische Form (sha klein geschrieben).
func CheckValue(p Param, v string) (string, error) {
	if !safeValueRE.MatchString(v) || strings.HasPrefix(v, "-") {
		return "", fmt.Errorf("parameter %s: value must consist of letters, digits and . _ / : @ + , = - only, up to %d characters, and must not start with '-'", p.Name, maxValue)
	}
	switch p.Type {
	case TypeString:
	case TypeInt:
		if !intRE.MatchString(v) {
			return "", fmt.Errorf("parameter %s: %q is not a non-negative integer", p.Name, truncate(v, 30))
		}
	case TypeSHA:
		v = strings.ToLower(v)
		if !shaRE.MatchString(v) {
			return "", fmt.Errorf("parameter %s: %q is not a hexadecimal commit id of 7 to 64 characters", p.Name, truncate(v, 30))
		}
	case TypeBool:
		if v != "true" && v != "false" {
			return "", fmt.Errorf("parameter %s: use true or false", p.Name)
		}
	case TypeEnum:
		ok := false
		for _, allowed := range p.Values {
			if v == allowed {
				ok = true
			}
		}
		if !ok {
			return "", fmt.Errorf("parameter %s: %q is none of %s", p.Name, truncate(v, 30), strings.Join(p.Values, ", "))
		}
	case TypePath:
		for _, seg := range strings.Split(v, "/") {
			if seg == ".." {
				return "", fmt.Errorf("parameter %s: a path must not contain '..'", p.Name)
			}
		}
	default:
		return "", fmt.Errorf("parameter %s: unknown type %q", p.Name, p.Type)
	}
	return v, nil
}

var shells = map[string]bool{"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true, "ksh": true, "csh": true, "tcsh": true}

// IsShell sagt, ob argv0 ein Shell-Interpreter ist. Mit Platzhaltern ist er in
// einem ausführbaren Ziel verboten: ein Shell-Aufruf zerlegt sein Argument neu.
func IsShell(argv0 string) bool { return shells[path.Base(argv0)] }

// Normalize prüft eine Fassung und liefert sie in der Form, die gespeichert und
// gehasht wird: bereinigte Zeichenketten, geprüfte Verweise auf Parameter.
func Normalize(s Spec) (Spec, error) {
	var err error
	if s.Purpose, err = checkLine("purpose", s.Purpose, maxPurpose, true); err != nil {
		return Spec{}, err
	}
	if len(s.Params) > MaxParams {
		return Spec{}, fmt.Errorf("more than %d parameters", MaxParams)
	}
	declared := map[string]bool{}
	for i := range s.Params {
		p := &s.Params[i]
		p.Name = strings.TrimSpace(p.Name)
		if !paramNameRE.MatchString(p.Name) {
			return Spec{}, fmt.Errorf("parameter name %q: use a lowercase letter, then lowercase letters, digits or '_'", truncate(p.Name, 40))
		}
		if declared[p.Name] {
			return Spec{}, fmt.Errorf("parameter %s is declared twice", p.Name)
		}
		declared[p.Name] = true
		if p.Type == "" {
			p.Type = TypeString
		}
		if p.Description, err = checkLine("description of "+p.Name, p.Description, maxTitle, false); err != nil {
			return Spec{}, err
		}
		if p.Type == TypeEnum {
			if len(p.Values) == 0 || len(p.Values) > 20 {
				return Spec{}, fmt.Errorf("parameter %s: an enum needs 1 to 20 values", p.Name)
			}
			for j, v := range p.Values {
				v = strings.TrimSpace(v)
				if !safeValueRE.MatchString(v) || strings.HasPrefix(v, "-") {
					return Spec{}, fmt.Errorf("parameter %s: enum value %q is not a safe value", p.Name, truncate(v, 30))
				}
				p.Values[j] = v
			}
		} else if len(p.Values) > 0 {
			return Spec{}, fmt.Errorf("parameter %s: values belong to the enum type", p.Name)
		}
		if p.Default != "" {
			if p.Default, err = CheckValue(*p, p.Default); err != nil {
				return Spec{}, fmt.Errorf("default: %w", err)
			}
		}
	}
	if s.Preconditions, err = checkLines("precondition", s.Preconditions, maxPurpose); err != nil {
		return Spec{}, err
	}
	if s.Steps, err = normalizeSteps("step", s.Steps, declared); err != nil {
		return Spec{}, err
	}
	if s.Rollback, err = normalizeSteps("rollback step", s.Rollback, declared); err != nil {
		return Spec{}, err
	}
	for _, id := range s.Pitfalls {
		if id <= 0 {
			return Spec{}, fmt.Errorf("pitfall %d is not a knowledge entry number", id)
		}
	}
	if len(s.Pitfalls) > MaxLines {
		return Spec{}, fmt.Errorf("more than %d linked pitfalls", MaxLines)
	}
	if s.Triggers, err = normalizeTriggers(s.Triggers); err != nil {
		return Spec{}, err
	}
	if s.Targets, err = normalizeTargets(s.Targets, declared); err != nil {
		return Spec{}, err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return Spec{}, err
	}
	if len(raw) > MaxSpecBytes {
		return Spec{}, fmt.Errorf("the runbook is larger than %d bytes", MaxSpecBytes)
	}
	return s, nil
}

func checkLines(what string, in []string, max int) ([]string, error) {
	if len(in) > MaxLines {
		return nil, fmt.Errorf("more than %d %ss", MaxLines, what)
	}
	var out []string
	for _, l := range in {
		v, err := checkLine(what, l, max, true)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func checkPlaceholders(what, s string, declared map[string]bool) error {
	for _, m := range placeholderRE.FindAllStringSubmatch(s, -1) {
		if !declared[m[1]] {
			return fmt.Errorf("%s uses {{%s}}, which is not a declared parameter", what, m[1])
		}
	}
	if strings.Contains(placeholderRE.ReplaceAllString(s, ""), "{{") {
		return fmt.Errorf("%s has a malformed placeholder: write {{name}}", what)
	}
	return nil
}

func normalizeSteps(what string, in []Step, declared map[string]bool) ([]Step, error) {
	if len(in) > MaxSteps {
		return nil, fmt.Errorf("more than %d %ss", MaxSteps, what)
	}
	var out []Step
	for i, st := range in {
		var err error
		label := fmt.Sprintf("%s %d", what, i+1)
		if st.Title, err = checkLine(label+" title", st.Title, maxTitle, true); err != nil {
			return nil, err
		}
		if st.Command, err = checkLine(label+" command", st.Command, maxCommand, false); err != nil {
			return nil, err
		}
		if st.Check, err = checkLine(label+" check", st.Check, maxPurpose, false); err != nil {
			return nil, err
		}
		for _, field := range []struct{ n, v string }{{"title", st.Title}, {"command", st.Command}, {"check", st.Check}} {
			if err := checkPlaceholders(label+" "+field.n, field.v, declared); err != nil {
				return nil, err
			}
		}
		out = append(out, st)
	}
	return out, nil
}

func normalizeTriggers(t Triggers) (Triggers, error) {
	var out Triggers
	if len(t.Keywords)+len(t.Commands)+len(t.Paths) > MaxTriggers {
		return out, fmt.Errorf("more than %d triggers", MaxTriggers)
	}
	for _, k := range t.Keywords {
		k, err := checkLine("keyword", k, 60, true)
		if err != nil {
			return out, err
		}
		k = strings.ToLower(k)
		if utf8.RuneCountInString(k) < 3 {
			return out, fmt.Errorf("keyword %q is shorter than 3 characters", k)
		}
		out.Keywords = append(out.Keywords, k)
	}
	for _, c := range t.Commands {
		c, err := checkLine("command pattern", c, 200, true)
		if err != nil {
			return out, err
		}
		if len(strings.ReplaceAll(c, "*", "")) < 3 {
			return out, fmt.Errorf("command pattern %q is too broad: it needs at least 3 characters besides '*'", c)
		}
		out.Commands = append(out.Commands, c)
	}
	for _, p := range t.Paths {
		p, err := checkLine("path pattern", p, 200, true)
		if err != nil {
			return out, err
		}
		out.Paths = append(out.Paths, p)
	}
	if err := validatePaths(out.Paths); err != nil {
		return out, err
	}
	return out, nil
}

func normalizeTargets(in []Target, declared map[string]bool) ([]Target, error) {
	if len(in) > MaxTargets {
		return nil, fmt.Errorf("more than %d targets", MaxTargets)
	}
	var out []Target
	seen := map[string]bool{}
	for _, t := range in {
		t.Name = strings.TrimSpace(t.Name)
		if err := ValidTargetName(t.Name); err != nil {
			return nil, err
		}
		if seen[t.Name] {
			return nil, fmt.Errorf("target %s is declared twice", t.Name)
		}
		seen[t.Name] = true
		var err error
		if t.Comment, err = checkLine("comment of target "+t.Name, t.Comment, maxTitle, true); err != nil {
			return nil, err
		}
		if len(t.Steps) == 0 || len(t.Steps) > MaxTargetSteps {
			return nil, fmt.Errorf("target %s needs 1 to %d steps", t.Name, MaxTargetSteps)
		}
		for i := range t.Steps {
			argv := t.Steps[i].Argv
			label := fmt.Sprintf("target %s step %d", t.Name, i+1)
			if len(argv) == 0 || len(argv) > MaxArgv {
				return nil, fmt.Errorf("%s needs 1 to %d arguments", label, MaxArgv)
			}
			hasPlaceholder := false
			for j, a := range argv {
				if !utf8.ValidString(a) || strings.ContainsRune(a, 0) || strings.ContainsFunc(a, BadRune) || len(a) > maxArg {
					return nil, fmt.Errorf("%s argument %d has control or invisible formatting characters or is longer than %d bytes", label, j+1, maxArg)
				}
				if err := checkPlaceholders(fmt.Sprintf("%s argument %d", label, j+1), a, declared); err != nil {
					return nil, err
				}
				if placeholderRE.MatchString(a) {
					hasPlaceholder = true
				}
			}
			if strings.TrimSpace(argv[0]) == "" || placeholderRE.MatchString(argv[0]) {
				return nil, fmt.Errorf("%s: the program (first argument) must be fixed text", label)
			}
			if hasPlaceholder && IsShell(argv[0]) {
				return nil, fmt.Errorf("%s: a shell interpreter may not receive parameters; pass the parameter to the program itself", label)
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// ScopeKey ist die Adresse der Geltung, wie sie in den Hash eingeht.
func ScopeKey(project, machine string) string {
	return "project=" + project + ";machine=" + machine
}

// Hash ist der Inhalts-Hash einer Fassung. Er deckt Name, Geltung und Inhalt
// ab: derselbe Text unter anderem Namen oder in anderem Projekt ist eine andere
// Freigabe.
func Hash(project, machine, name string, s Spec) string {
	raw, _ := json.Marshal(s)
	sum := sha256.Sum256([]byte("ghosttree-runbook/1\x00" + ScopeKey(project, machine) + "\x00" + name + "\x00" + string(raw)))
	return hex.EncodeToString(sum[:])
}

// ParseSpec liest eine Fassung aus JSON; unbekannte Felder sind ein Fehler,
// damit ein Tippfehler im Feldnamen nicht still ein Feld wegwirft.
func ParseSpec(raw []byte) (Spec, error) {
	var s Spec
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return Spec{}, err
	}
	return Normalize(s)
}

// ParamByName sucht einen Parameter.
func (s Spec) ParamByName(name string) (Param, bool) {
	for _, p := range s.Params {
		if p.Name == name {
			return p, true
		}
	}
	return Param{}, false
}

// TargetByName sucht ein ausführbares Ziel.
func (s Spec) TargetByName(name string) (Target, bool) {
	for _, t := range s.Targets {
		if t.Name == name {
			return t, true
		}
	}
	return Target{}, false
}

// BindPartial prüft die angegebenen Werte gegen die Parameter und ergänzt
// Vorgaben, verlangt aber keine Pflichtwerte: für die Checkliste, die fehlende
// Werte als {{name}} stehen lässt. Ein unbekannter Name oder ein Wert, der die
// Art verletzt, ist ein Fehler.
func (s Spec) BindPartial(given map[string]string) (map[string]string, error) {
	values := map[string]string{}
	for name := range given {
		if _, ok := s.ParamByName(name); !ok {
			return nil, fmt.Errorf("unknown parameter %q", truncate(name, 40))
		}
	}
	for _, p := range s.Params {
		v, ok := given[p.Name]
		if !ok {
			if p.Default != "" {
				values[p.Name] = p.Default
			}
			continue
		}
		v, err := CheckValue(p, v)
		if err != nil {
			return nil, err
		}
		values[p.Name] = v
	}
	return values, nil
}

// Bind ist BindPartial und verlangt zusätzlich jeden Pflichtparameter: für
// alles, was ausgeführt wird.
func (s Spec) Bind(given map[string]string) (map[string]string, error) {
	values, err := s.BindPartial(given)
	if err != nil {
		return nil, err
	}
	for _, p := range s.Params {
		if _, ok := values[p.Name]; !ok && !p.Optional {
			return nil, fmt.Errorf("parameter %s (%s) is required", p.Name, p.Type)
		}
	}
	return values, nil
}

// Expand setzt Parameter in einen Text ein. Fehlt der Wert eines optionalen
// Parameters, bleibt der Platzhalter sichtbar stehen. Die Werte sind durch
// CheckValue auf den engen Zeichensatz beschränkt; hier muss deshalb nichts
// mehr maskiert werden, und es entsteht nie ein Zeichen, das den Text
// umdeutet.
func Expand(text string, values map[string]string) string {
	return placeholderRE.ReplaceAllStringFunc(text, func(m string) string {
		name := placeholderRE.FindStringSubmatch(m)[1]
		if v, ok := values[name]; ok {
			return v
		}
		return m
	})
}

// Exec ist ein aufgelöster ausführbarer Schritt.
type Exec struct {
	Argv []string `json:"argv"`
}

// Plan löst ein Ziel mit den Werten zu argv-Listen auf. Ein Platzhalter ersetzt
// nur den Teil eines einzelnen Arguments; die Anzahl der Argumente ändert sich
// nie. Ein nicht gesetzter Platzhalter ist ein Fehler, nie ein leerer Wert.
func (s Spec) Plan(target string, values map[string]string) ([]Exec, error) {
	t, ok := s.TargetByName(target)
	if !ok {
		return nil, fmt.Errorf("no target %q in this runbook", truncate(target, 40))
	}
	var out []Exec
	for _, st := range t.Steps {
		argv := make([]string, len(st.Argv))
		for i, a := range st.Argv {
			argv[i] = Expand(a, values)
			if placeholderRE.MatchString(argv[i]) {
				return nil, fmt.Errorf("target %s: a parameter used in %q has no value", target, truncate(a, 60))
			}
		}
		out = append(out, Exec{Argv: argv})
	}
	return out, nil
}

// Quote schreibt ein Argument so, dass es in einer POSIX-Shell wieder dieses
// eine Argument ergäbe. Nur zur Anzeige im Probelauf; ausgeführt wird nie
// durch eine Shell.
func Quote(arg string) string {
	if arg != "" && regexp.MustCompile(`^[A-Za-z0-9._/:@+,=%-]+$`).MatchString(arg) {
		return arg
	}
	return "'" + strings.ReplaceAll(Escape(arg), "'", `'\''`) + "'"
}

// QuoteArgv schreibt eine argv-Liste als eine Zeile zur Anzeige.
func QuoteArgv(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = Quote(a)
	}
	return strings.Join(parts, " ")
}
