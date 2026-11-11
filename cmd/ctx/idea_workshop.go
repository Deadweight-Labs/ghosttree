package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Ideen-Werkstatt auf der Kommandozeile (REQ-611). Ein Aufruf mit dem Token der
// Konfiguration gilt dem Server als Agent: Freigeben, menschliche Entscheidungen,
// Erlassen und die Policy gehören einem Menschen in der Weboberfläche.

const ideaWorkshopUsage = `
  raw <n> [--file F|-] [--source S]                   attach the raw text unchanged (stdin with -); without text, print the newest version (--rev N)
  summary <n> [--file F|-] [--message M]               attach a short version; without text, print it
  concept <n> [--about T] [--what T] [--how T] [--not T] [--questions T] [--requirement R ...] [--message M] [--rev N]
                                                       write sections of the concept, or print it when none is given
  phase <n> <concept|discussion|done> [--rev N]  move it along (approving, with the concept version seen, is a person's step in the web interface)
  discuss <n>                                          mark it as under discussion (the thread: ctx ... thread_open with link_kind idea)
  issues <n> [--issue "Title|1,2"] ... [--branch B] [--package-name N]   create the issues of an approved idea; "1,2" are the requirement numbers
  waive <n> --requirement N --reason R                 waive a requirement (a person only)
  attach <n> <file> [--decision DEC-n --variant A | --thread N] [--alt A]    attach an image (PNG, JPEG, WebP up to 5 MB)
  import <file.json> [--project P] [--force] [--apply]  record ideas with their texts, concept and open decisions from a file; without --apply only a dry run that lists the local files it would read`

var ideaWorkshopSubs = map[string]bool{"raw": true, "summary": true, "concept": true, "phase": true, "discuss": true, "issues": true, "waive": true, "attach": true, "import": true}

// readTextArg liest einen Text aus einer Datei oder von der Standardeingabe ("-"),
// höchstens max+1 Bytes: wer größer ist, bekommt den Fehler des Servers, ohne dass
// der Client die Datei ganz einliest.
func readTextArg(file string, stdin io.Reader, max int) (string, error) {
	var r io.Reader = stdin
	if file == "-" && stdin == nil {
		return "", fmt.Errorf("standard input is not available here")
	}
	if file != "-" {
		f, err := os.Open(file)
		if err != nil {
			return "", err
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return "", err
		}
		if !st.Mode().IsRegular() {
			return "", fmt.Errorf("%s is not a regular file", file)
		}
		r = f
	}
	b, err := io.ReadAll(io.LimitReader(r, int64(max)+1))
	if err != nil {
		return "", err
	}
	if len(b) > max {
		return "", fmt.Errorf("the text is longer than %d bytes", max)
	}
	return string(b), nil
}

func workshopLines(stdout io.Writer, ws store.IdeaWorkshop) {
	i := ws.Idea
	fmt.Fprintf(stdout, "phase: %s\n", i.Phase)
	for _, p := range i.Pains {
		fmt.Fprintf(stdout, "motivated by PAIN-%d %s\n", p.ID, p.Title)
	}
	for _, r := range ws.Raw {
		by := r.Person
		if r.ByAgent {
			by += " (agent)"
		}
		src := r.Source
		if src == "" {
			src = "-"
		}
		fmt.Fprintf(stdout, "raw r%d %s, %d bytes, %s\n", r.Revision, src, r.Bytes, by)
	}
	if ws.Summary != nil {
		fmt.Fprintf(stdout, "\nsummary (r%d)\n%s\n", ws.Summary.Revision, ws.Summary.Body)
	}
	if ws.Concept != nil {
		c := ws.Concept.Concept
		fmt.Fprintf(stdout, "\nconcept (r%d)\n", ws.Concept.Revision)
		for _, sec := range []struct{ title, text string }{{"Worum", c.About}, {"Was", c.What}, {"Wie", c.How}, {"Was nicht", c.Not}, {"Offene Fragen", c.Questions}} {
			if sec.text != "" {
				fmt.Fprintf(stdout, "%s: %s\n", sec.title, sec.text)
			}
		}
		for n, r := range c.Requirements {
			fmt.Fprintf(stdout, "Anforderung %d: %s\n", n+1, r)
		}
	}
	for _, c := range ws.Coverage {
		if c.State != idea.CoverNone || i.Phase == idea.PhaseBuilding || i.Phase == idea.PhaseDone {
			fmt.Fprintf(stdout, "requirement %d is %s\n", c.No, c.State)
		}
	}
	for _, d := range ws.Decisions {
		decisionLines(stdout, d)
	}
	for _, a := range ws.Attachments {
		fmt.Fprintf(stdout, "ATT-%d %s %dx%d (%s) %s\n", a.ID, a.Mime, a.Width, a.Height, a.Owner, a.URL)
	}
}

func decisionLines(stdout io.Writer, d idea.Decision) {
	subject := fmt.Sprintf("IDEA-%d", d.SubjectID)
	if d.Subject == idea.SubjectRequest {
		subject = fmt.Sprintf("REQ-%d", d.SubjectID)
	}
	fmt.Fprintf(stdout, "%s [%s|%s] %s (%s)\n", d.HumanID(), d.State, d.Way, d.Question, subject)
	for _, w := range d.WayWhy {
		fmt.Fprintf(stdout, "  who decides: %s\n", w)
	}
	for _, v := range d.Variants {
		mark := " "
		if v.Chosen {
			mark = "*"
		}
		fmt.Fprintf(stdout, "  %s %s %s", mark, v.Label, v.Title)
		if v.Pro != "" {
			fmt.Fprintf(stdout, " | pro: %s", v.Pro)
		}
		if v.Contra != "" {
			fmt.Fprintf(stdout, " | contra: %s", v.Contra)
		}
		for _, a := range v.Attachments {
			fmt.Fprintf(stdout, " [ATT-%d]", a.ID)
		}
		fmt.Fprintln(stdout)
	}
	for _, v := range d.Votes {
		who := v.Voter
		if v.ByAgent {
			who += " (agent)"
		}
		fmt.Fprintf(stdout, "  vote %s: %s, %s\n", who, v.Variant, v.Reason)
	}
	if d.Answer != "" {
		fmt.Fprintf(stdout, "  decision: %s\n", d.Answer)
	}
	if d.Reason != "" {
		fmt.Fprintf(stdout, "  reason: %s\n", d.Reason)
	}
	if d.Trigger != "" {
		fmt.Fprintf(stdout, "  comes back when: %s\n", d.Trigger)
	}
	if d.ByAgent {
		fmt.Fprintln(stdout, "  decided by an agent; a person can overturn it")
	}
}

// cmdIdeaWorkshop führt die Unterbefehle der Werkstatt aus; false, wenn sub keiner ist.
func cmdIdeaWorkshop(sub string, rest []string, stdout io.Writer, jsonMode bool, cfg config.Config, c *client.Client) (int, bool) {
	if !ideaWorkshopSubs[sub] {
		return 0, false
	}
	usageErr := func() (int, bool) { fmt.Fprintln(stdout, ideaUsage+ideaWorkshopUsage); return 2, true }
	fail := func(err error) (int, bool) { return writeRequestCLIError(stdout, jsonMode, 1, err), true }
	fs := flag.NewFlagSet("idea "+sub, flag.ContinueOnError)
	fs.SetOutput(stdout)
	file := fs.String("file", "", "file with the text, - for standard input")
	source := fs.String("source", "", "where the raw text comes from, one line")
	message := fs.String("message", "", "what changed in this version, one line")
	rev := fs.Int("rev", 0, "version to print, default the newest")
	about := fs.String("about", "", "concept: Worum")
	what := fs.String("what", "", "concept: Was")
	how := fs.String("how", "", "concept: Wie")
	not := fs.String("not", "", "concept: Was nicht")
	questions := fs.String("questions", "", "concept: Offene Fragen")
	var requirements, issueSpecs repeatedString
	fs.Var(&requirements, "requirement", "concept: one requirement (a criterion draft); repeatable. With waive: the number")
	fs.Var(&issueSpecs, "issue", `an issue to create: "Title|1,2" (requirement numbers); repeatable`)
	branch := fs.String("branch", "", "work package branch")
	packageName := fs.String("package-name", "", "name of a new work package")
	reason := fs.String("reason", "", "why")
	decision := fs.String("decision", "", "attach: the decision, DEC-n")
	variant := fs.String("variant", "", "attach: the letter of the variant")
	thread := fs.Int64("thread", 0, "attach: the discussion of the idea, a reply image")
	alt := fs.String("alt", "", "attach: what the image shows")
	project := fs.String("project", "", "import: the project")
	force := fs.Bool("force", false, "import: record ideas although a similar open one exists")
	apply := fs.Bool("apply", false, "import: upload; without it the command only lists the local files it would read (dry run)")
	positional, err := parseInterspersed(fs, rest)
	if err != nil || len(positional) == 0 {
		return usageErr()
	}
	if sub == "import" {
		return cmdIdeaImport(positional[0], *project, *force, *apply, stdout, jsonMode, cfg, c)
	}
	id, ok := parseIdeaRef(positional[0])
	if !ok {
		return usageErr()
	}
	session := cliSessionID(c, cfg)
	switch sub {
	case "raw", "summary":
		part := idea.PartRaw
		if sub == "summary" {
			part = idea.PartSummary
		}
		if *file == "" {
			t, err := c.IdeaText(id, part, *rev)
			if err != nil {
				return fail(err)
			}
			if jsonMode {
				return writeRequestCLIResult(stdout, true, t, nil, nil), true
			}
			fmt.Fprint(stdout, t.Body)
			return 0, true
		}
		max := idea.MaxRawBytes
		if part == idea.PartSummary {
			max = idea.MaxSummary * 4
		}
		body, err := readTextArg(*file, os.Stdin, max)
		if err != nil {
			return fail(err)
		}
		r, err := c.PutIdeaText(id, part, body, *source, *message, 0, session)
		if err != nil {
			return fail(err)
		}
		if !jsonMode {
			note := ""
			if r.Unchanged {
				note = " (unchanged, no new version)"
			}
			fmt.Fprintf(stdout, "%s r%d of IDEA-%d, %d bytes%s\n", part, r.Version.Revision, id, r.Version.Bytes, note)
			return 0, true
		}
		return writeRequestCLIResult(stdout, true, r, nil, nil), true
	case "concept":
		patch := idea.ConceptPatch{}
		any := false
		for dst, v := range map[**string]string{&patch.About: *about, &patch.What: *what, &patch.How: *how, &patch.Not: *not, &patch.Questions: *questions} {
			if v != "" {
				s := v
				*dst = &s
				any = true
			}
		}
		if len(requirements) > 0 {
			r := []string(requirements)
			patch.Requirements = &r
			any = true
		}
		if !any {
			v, err := c.IdeaConcept(id, *rev)
			if err != nil {
				return fail(err)
			}
			if jsonMode {
				return writeRequestCLIResult(stdout, true, v, nil, nil), true
			}
			ws := store.IdeaWorkshop{Idea: idea.Idea{Phase: ""}, Concept: &v}
			workshopLines(stdout, ws)
			return 0, true
		}
		r, err := c.PutIdeaConcept(id, patch, *message, 0, session)
		if err != nil {
			return fail(err)
		}
		if !jsonMode {
			fmt.Fprintf(stdout, "concept r%d of IDEA-%d\n", r.Version.Revision, id)
			return 0, true
		}
		return writeRequestCLIResult(stdout, true, r, nil, nil), true
	case "phase":
		if len(positional) != 2 {
			return usageErr()
		}
		i, err := c.SetIdeaPhase(id, positional[1], *rev)
		return ideaResultCLI(stdout, jsonMode, cfg, i, err), true
	case "discuss":
		i, err := c.StartIdeaDiscussion(id)
		return ideaResultCLI(stdout, jsonMode, cfg, i, err), true
	case "issues":
		var specs []store.IssueSpec
		for _, raw := range issueSpecs {
			title, nums, _ := strings.Cut(raw, "|")
			spec := store.IssueSpec{Title: strings.TrimSpace(title)}
			for _, n := range strings.Split(nums, ",") {
				if n = strings.TrimSpace(n); n != "" {
					v, err := strconv.Atoi(n)
					if err != nil {
						return usageErr()
					}
					spec.Requirements = append(spec.Requirements, v)
				}
			}
			specs = append(specs, spec)
		}
		res, err := c.CreateIssuesFromIdea(id, specs, *branch, *packageName, session)
		if err != nil || jsonMode {
			return writeRequestCLIResult(stdout, jsonMode, res, nil, err), true
		}
		for _, r := range res.Requests {
			fmt.Fprintf(stdout, "created REQ-%d\n", r)
		}
		if res.Package != nil {
			fmt.Fprintf(stdout, "work package %s on branch %s\n", res.Package.HumanID(), res.Package.Branch)
		}
		return 0, true
	case "waive":
		n := 0
		if len(requirements) == 1 {
			n, _ = strconv.Atoi(requirements[0])
		}
		if n == 0 {
			return usageErr()
		}
		cov, err := c.WaiveIdeaRequirement(id, n, *reason)
		return writeRequestCLIResult(stdout, jsonMode, cov, nil, err), true
	case "attach":
		if len(positional) != 2 {
			return usageErr()
		}
		data, err := readImageArg(positional[1])
		if err != nil {
			return fail(err)
		}
		req := client.AttachRequest{Subject: idea.SubjectIdea, ID: id, Owner: idea.OwnerIdea, Data: data, Alt: *alt, SessionID: session}
		switch {
		case *decision != "":
			n, ok := parseDecisionRef(*decision)
			if !ok || *variant == "" {
				return usageErr()
			}
			req.Owner, req.DecisionID, req.Variant = idea.OwnerVariant, n, *variant
		case *thread != 0:
			req.Owner, req.ThreadID = idea.OwnerReply, *thread
		}
		m, err := c.Attach(req)
		if err != nil || jsonMode {
			return writeRequestCLIResult(stdout, jsonMode, m, nil, err), true
		}
		fmt.Fprintf(stdout, "ATT-%d %s %dx%d %s\n", m.ID, m.Mime, m.Width, m.Height, m.URL)
		return 0, true
	}
	return usageErr()
}

func ideaResultCLI(stdout io.Writer, jsonMode bool, cfg config.Config, i idea.Idea, err error) int {
	card := cliBuilder(cfg, i.Project).IdeaCard(i)
	if err == nil && !jsonMode {
		ideaLines(stdout, i)
		cliCard(stdout, card)
		return 0
	}
	return writeRequestCLIResultCard(stdout, jsonMode, i, &card, err)
}

// readImageArg liest eine Bilddatei bis zur Höchstgröße.
func readImageArg(path string) ([]byte, error) {
	s, err := readTextArg(path, nil, idea.MaxAttachmentBytes)
	if err != nil {
		return nil, err
	}
	return []byte(s), nil
}

func parseDecisionRef(s string) (int64, bool) {
	s = strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(s)), "DEC-")
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil && n > 0
}

// ---- Import

// ideaImportFile ist das Dateiformat von ctx idea import: Ideen mit
// Rohfassung, Kurzfassung, Konzept und offenen Entscheidungen. Beispiel:
// docs/examples/idea-werkstatt-import.json.
type ideaImportFile struct {
	Version int    `json:"version"`
	Project string `json:"project,omitempty"`
	// AttachmentsDir ist der eine Unterordner neben der Importdatei, aus dem Dateien
	// gelesen werden dürfen; sonst nur Dateien direkt neben der Importdatei.
	AttachmentsDir string            `json:"attachments_dir,omitempty"`
	Ideas          []ideaImportEntry `json:"ideas"`
}

type ideaImportEntry struct {
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	Raw   *struct {
		Source string `json:"source,omitempty"`
		// Genau eines: Text, eine Datei (relativ zur Importdatei) oder ein
		// Dokument des Projekts (Slug), dessen jüngste Fassung die Rohfassung ist.
		Text     string `json:"text,omitempty"`
		File     string `json:"file,omitempty"`
		Document string `json:"document,omitempty"`
	} `json:"raw,omitempty"`
	Summary         string        `json:"summary,omitempty"`
	Concept         *idea.Concept `json:"concept,omitempty"`
	MotivatedByPain int64         `json:"motivated_by_pain,omitempty"`
	Decisions       []struct {
		Question string              `json:"question"`
		Detail   string              `json:"detail,omitempty"`
		Way      string              `json:"way,omitempty"`
		Aspects  []string            `json:"aspects,omitempty"`
		Variants []ideaImportVariant `json:"variants,omitempty"`
	} `json:"decisions,omitempty"`
}

type ideaImportVariant struct {
	idea.VariantInput
	// Image ist ein Bild (relativ zur Importdatei) an der Variante.
	Image string `json:"image,omitempty"`
	Alt   string `json:"alt,omitempty"`
}

const maxImportFile = 2 << 20

func cmdIdeaImport(path, project string, force, apply bool, stdout io.Writer, jsonMode bool, cfg config.Config, c *client.Client) (int, bool) {
	fail := func(err error) (int, bool) { return writeRequestCLIError(stdout, jsonMode, 1, err), true }
	text, err := readTextArg(path, nil, maxImportFile)
	if err != nil {
		return fail(err)
	}
	var in ideaImportFile
	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return fail(fmt.Errorf("%s: %w", path, err))
	}
	if in.Version != 1 || len(in.Ideas) == 0 {
		return fail(fmt.Errorf("%s: version 1 with at least one idea is expected", path))
	}
	if project == "" {
		project = in.Project
	}
	target, err := c.WriteTarget(project, requestProjectAxes(cfg.Machine).Project)
	if err != nil {
		return fail(err)
	}
	base := filepath.Dir(path)
	if err := checkAttachmentsDir(in.AttachmentsDir); err != nil {
		return fail(err)
	}
	// Erst alle lokalen Dateien auflösen und prüfen, dann zeigen, dann (nur mit
	// --apply) hochladen: eine Importdatei, die jemand schickt, lässt nichts lesen,
	// was der Benutzer nicht vorher gesehen hat.
	files, err := planImportFiles(base, in)
	if err != nil {
		return fail(err)
	}
	if !jsonMode || !apply {
		for _, f := range files {
			fmt.Fprintf(stdout, "reads %s %s (%d bytes)\n", f.Kind, f.Rel, f.Size)
		}
	}
	if !apply {
		if jsonMode {
			return writeRequestCLIResult(stdout, true, map[string]any{"dry_run": true, "files": files, "ideas": len(in.Ideas)}, nil, nil), true
		}
		fmt.Fprintf(stdout, "dry run: %d ideas would be recorded, %d local files read, nothing was uploaded; run again with --apply\n", len(in.Ideas), len(files))
		return 0, true
	}
	session := cliSessionID(c, cfg)
	type done struct {
		Ref     string   `json:"ref"`
		Title   string   `json:"title"`
		Steps   []string `json:"steps"`
		Skipped string   `json:"skipped,omitempty"`
	}
	var report []done
	for _, e := range in.Ideas {
		d := done{Title: e.Title}
		i, err := c.CreateIdea(target, e.Title, e.Body, force, session)
		if err != nil {
			if strings.Contains(err.Error(), "idea_duplicate") || strings.Contains(err.Error(), "similar open idea") {
				d.Skipped = err.Error()
				report = append(report, d)
				continue
			}
			return fail(fmt.Errorf("%q: %w", e.Title, err))
		}
		d.Ref = i.HumanID()
		step := func(s string, err error) error {
			if err != nil {
				return fmt.Errorf("%s was recorded, then %s failed: %w", i.HumanID(), s, err)
			}
			d.Steps = append(d.Steps, s)
			return nil
		}
		if e.Raw != nil {
			body, err := importRawText(c, target, base, in.AttachmentsDir, e.Raw.Text, e.Raw.File, e.Raw.Document)
			if err != nil {
				return fail(fmt.Errorf("%s was recorded, then the raw text failed: %w", i.HumanID(), err))
			}
			_, err = c.PutIdeaText(i.ID, idea.PartRaw, body, e.Raw.Source, "", 0, session)
			if err := step("raw", err); err != nil {
				return fail(err)
			}
		}
		if strings.TrimSpace(e.Summary) != "" {
			_, err := c.PutIdeaText(i.ID, idea.PartSummary, e.Summary, "", "", 0, session)
			if err := step("summary", err); err != nil {
				return fail(err)
			}
		}
		if e.Concept != nil {
			cc := *e.Concept
			patch := idea.ConceptPatch{About: &cc.About, What: &cc.What, How: &cc.How, Not: &cc.Not, Questions: &cc.Questions, Requirements: &cc.Requirements}
			_, err := c.PutIdeaConcept(i.ID, patch, "import", 0, session)
			if err := step("concept", err); err != nil {
				return fail(err)
			}
		}
		if e.MotivatedByPain != 0 {
			_, err := c.AddPainLink(e.MotivatedByPain, painTargetOfIdea(i.ID), session)
			if err := step("pain link", err); err != nil {
				return fail(err)
			}
		}
		for _, q := range e.Decisions {
			var vs []idea.VariantInput
			for _, v := range q.Variants {
				vs = append(vs, v.VariantInput)
			}
			opened, err := c.OpenDecision(client.OpenDecisionRequest{Subject: idea.SubjectIdea, ID: i.ID, Question: q.Question, Detail: q.Detail, Way: q.Way, Aspects: q.Aspects, Variants: vs, SessionID: session})
			if err := step("decision "+q.Question, err); err != nil {
				return fail(err)
			}
			for n, v := range q.Variants {
				if v.Image == "" {
					continue
				}
				data, err := readImportImage(base, v.Image, in.AttachmentsDir)
				if err != nil {
					return fail(fmt.Errorf("%s: %w", v.Image, err))
				}
				_, err = c.Attach(client.AttachRequest{Subject: idea.SubjectIdea, ID: i.ID, Owner: idea.OwnerVariant, DecisionID: opened.ID, Variant: idea.VariantLabel(n), Data: data, Alt: v.Alt, SessionID: session})
				if err := step("image of variant "+idea.VariantLabel(n), err); err != nil {
					return fail(err)
				}
			}
		}
		report = append(report, d)
	}
	if jsonMode {
		return writeRequestCLIResult(stdout, true, report, nil, nil), true
	}
	for _, d := range report {
		if d.Skipped != "" {
			fmt.Fprintf(stdout, "skipped %q: %s\n", d.Title, d.Skipped)
			continue
		}
		fmt.Fprintf(stdout, "%s %s (%s)\n", d.Ref, d.Title, strings.Join(d.Steps, ", "))
	}
	return 0, true
}

func importRawText(c *client.Client, project, base, attDir, text, file, document string) (string, error) {
	n := 0
	for _, s := range []string{text, file, document} {
		if s != "" {
			n++
		}
	}
	if n != 1 {
		return "", fmt.Errorf("raw needs exactly one of text, file or document")
	}
	switch {
	case text != "":
		return text, nil
	case file != "":
		return readImportText(base, file, attDir)
	}
	docs, err := c.Documents(project, "", true)
	if err != nil {
		return "", err
	}
	for _, d := range docs {
		if d.Slug == document {
			rev, err := c.DocumentRevision(d.ID, d.HeadRevision)
			if err != nil {
				return "", err
			}
			return rev.Body, nil
		}
	}
	return "", fmt.Errorf("no document %q in %s", document, project)
}

// ---- ctx decision

const decisionUsage = `usage: ctx decision <command>

  open (--idea N | --request N | --via-request N) "question" [--detail D] [--way human|agent] [--aspect A ...] [--variant "Title|text|pro|contra" ...]
        aspects that hand it to a person: irreversible, security, cost, external, data_model, product, people, nonlocal, convention
  list [--idea N | --request N] [--state open|decided|deferred|dropped|all] [--waiting] [--project P]   --waiting is the inbox: open decisions that wait for a person
  show <DEC-n>
  vote <DEC-n> <A|B|...> --reason R
  variant <DEC-n> --title T [--text X] [--pro P] [--contra C]
  close <DEC-n> (--variant A | --answer X | --defer TRIGGER | --drop) [--reason R]
  reopen <DEC-n> --reason R
  way <DEC-n> <human|agent>
  policy [rule|human] [--project P]

Closing needs the right to edit the idea or issue. A decision that waits for a person, and the steps way, reopen and policy, are a person's in the web interface:
a token (this command, MCP, the API) counts as an agent. All commands take --json.`

func cmdDecision(args []string, stdout io.Writer) int {
	args, jsonMode := removeBoolArg(args, "--json")
	usageErr := func() int { fmt.Fprintln(stdout, decisionUsage); return 2 }
	if len(args) == 0 {
		return usageErr()
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "open", "list", "show", "vote", "variant", "close", "reopen", "way", "policy":
	default:
		return usageErr()
	}
	cfg, err := config.Load()
	if err != nil {
		return writeRequestCLIError(stdout, jsonMode, 1, err)
	}
	c := client.New(cfg)
	one := func(d idea.Decision, err error) int {
		card := cliBuilder(cfg, d.Project).DecisionCard(d)
		if err == nil && !jsonMode {
			decisionLines(stdout, d)
			cliCard(stdout, card)
			return 0
		}
		return writeRequestCLIResultCard(stdout, jsonMode, d, &card, err)
	}
	fs := flag.NewFlagSet("decision "+sub, flag.ContinueOnError)
	fs.SetOutput(stdout)
	ideaNo := fs.Int64("idea", 0, "idea number")
	requestNo := fs.Int64("request", 0, "request number")
	via := fs.Int64("via-request", 0, "a question that came up while working on this request: it lands on its idea")
	detail := fs.String("detail", "", "background")
	way := fs.String("way", "", "human or agent")
	var aspects, variants repeatedString
	fs.Var(&aspects, "aspect", "aspect that hands it to a person; repeatable")
	fs.Var(&variants, "variant", `an option "Title|text|pro|contra" (open); with close and vote: the letter`)
	state := fs.String("state", "", "list: open (default), decided, deferred, dropped or all")
	waiting := fs.Bool("waiting", false, "list: only what waits for a person")
	project := fs.String("project", "", "project")
	reason := fs.String("reason", "", "why")
	answer := fs.String("answer", "", "close: the decision in words")
	deferTrigger := fs.String("defer", "", "close: defer it until this trigger")
	drop := fs.Bool("drop", false, "close: drop it, with a reason")
	title := fs.String("title", "", "variant: title")
	text := fs.String("text", "", "variant: text")
	pro := fs.String("pro", "", "variant: pro")
	contra := fs.String("contra", "", "variant: contra")
	positional, err := parseInterspersed(fs, rest)
	if err != nil {
		return usageErr()
	}
	session := cliSessionID(c, cfg)
	switch sub {
	case "open":
		if len(positional) != 1 {
			return usageErr()
		}
		req := client.OpenDecisionRequest{Question: positional[0], Detail: *detail, Way: *way, Aspects: aspects, SessionID: session}
		switch {
		case *via != 0:
			req.ViaReq = *via
		case *ideaNo != 0:
			req.Subject, req.ID = idea.SubjectIdea, *ideaNo
		case *requestNo != 0:
			req.Subject, req.ID = idea.SubjectRequest, *requestNo
		default:
			return usageErr()
		}
		for _, v := range variants {
			parts := strings.SplitN(v, "|", 4)
			for len(parts) < 4 {
				parts = append(parts, "")
			}
			req.Variants = append(req.Variants, idea.VariantInput{Title: parts[0], Text: parts[1], Pro: parts[2], Contra: parts[3]})
		}
		return one(c.OpenDecision(req))
	case "list":
		var list []idea.Decision
		var err error
		switch {
		case *ideaNo != 0:
			list, err = c.DecisionsOf(idea.SubjectIdea, *ideaNo)
		case *requestNo != 0:
			list, err = c.DecisionsOf(idea.SubjectRequest, *requestNo)
		default:
			target := requestProjectAxes(cfg.Machine).Project
			if *project != "" {
				if target, err = c.WriteTarget(*project, ""); err != nil {
					return writeRequestCLIError(stdout, jsonMode, 1, err)
				}
			}
			list, err = c.ListDecisions(target, *state, "", *waiting, 100)
		}
		if err != nil || jsonMode {
			return writeRequestCLIResult(stdout, jsonMode, list, nil, err)
		}
		if len(list) == 0 {
			fmt.Fprintln(stdout, "no matching decisions")
			return 0
		}
		for _, d := range list {
			fmt.Fprintf(stdout, "%-8s %-9s %-8s %s\n", d.HumanID(), d.State, d.Way, d.Question)
		}
		return 0
	case "policy":
		target := requestProjectAxes(cfg.Machine).Project
		if *project != "" {
			if target, err = c.WriteTarget(*project, ""); err != nil {
				return writeRequestCLIError(stdout, jsonMode, 1, err)
			}
		}
		if len(positional) == 1 {
			if err := c.SetDecisionPolicy(target, positional[0]); err != nil {
				return writeRequestCLIError(stdout, jsonMode, 1, err)
			}
		}
		mode, err := c.DecisionPolicy(target)
		return writeRequestCLIResult(stdout, jsonMode, map[string]string{"project": target, "mode": mode}, nil, err)
	}
	if len(positional) == 0 {
		return usageErr()
	}
	id, ok := parseDecisionRef(positional[0])
	if !ok {
		return usageErr()
	}
	switch sub {
	case "show":
		return one(c.GetDecision(id))
	case "vote":
		if len(positional) != 2 {
			return usageErr()
		}
		return one(c.VoteDecision(id, positional[1], *reason, session))
	case "variant":
		return one(c.AddVariant(id, idea.VariantInput{Title: *title, Text: *text, Pro: *pro, Contra: *contra}, session))
	case "reopen":
		return one(c.ReopenDecision(id, *reason, session))
	case "way":
		if len(positional) != 2 {
			return usageErr()
		}
		return one(c.SetDecisionWay(id, positional[1], session))
	case "close":
		req := client.CloseDecisionRequest{Reason: *reason, SessionID: session}
		switch {
		case *drop:
			req.Outcome = idea.DecisionDropped
		case *deferTrigger != "":
			req.Outcome, req.Trigger = idea.DecisionDeferred, *deferTrigger
		case len(variants) == 1 || *answer != "":
			req.Outcome, req.Answer = idea.DecisionDecided, *answer
			if len(variants) == 1 {
				req.Variant = variants[0]
			}
		default:
			return usageErr()
		}
		return one(c.CloseDecision(id, req))
	}
	return usageErr()
}

func painTargetOfIdea(id int64) pain.Target { return pain.Target{IdeaID: id} }

// checkAttachmentsDir: der Ordner ist ein einziger Name, ohne Pfadtrenner und ohne
// führenden Punkt.
func checkAttachmentsDir(dir string) error {
	if dir == "" {
		return nil
	}
	if strings.ContainsAny(dir, `/\`) || strings.HasPrefix(dir, ".") || dir != filepath.Clean(dir) {
		return fmt.Errorf("attachments_dir %q must be one folder name beside the import file, not starting with a dot", dir)
	}
	return nil
}

// safeImportPath löst eine Datei aus einer Importdatei auf. Erlaubt sind nur
// Dateien direkt neben der Importdatei oder direkt in dem einen Unterordner, den
// die Importdatei als attachments_dir nennt; kein Pfadbestandteil darf mit einem
// Punkt beginnen (.ssh, .config, ..), kein absoluter Pfad, kein Symlink, der aus
// diesem Bereich hinauszeigt, nur reguläre Dateien. Eine Importdatei, die jemand
// schickt, lässt sonst Schlüssel oder Konfiguration lesen, sobald sie in einem
// großen Ordner (dem Home-Verzeichnis) liegt. kind ist text (.md, .txt) oder image
// (.png, .jpg, .jpeg, .webp); den Inhalt prüfen readImportText und readImportImage.
func safeImportPath(base, rel, attDir, kind string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) || strings.ContainsRune(rel, 0) {
		return "", fmt.Errorf("%q must be a path beside the import file", rel)
	}
	clean := filepath.Clean(rel)
	for _, part := range strings.Split(filepath.ToSlash(clean), "/") {
		if strings.HasPrefix(part, ".") {
			return "", fmt.Errorf("%q has a part that starts with a dot", rel)
		}
	}
	dir := filepath.Dir(clean)
	if dir != "." && (attDir == "" || dir != attDir) {
		return "", fmt.Errorf("%q is not beside the import file%s", rel, attDirHint(attDir))
	}
	switch strings.ToLower(filepath.Ext(clean)) {
	case ".md", ".txt":
		if kind != "text" {
			return "", fmt.Errorf("%q is not an image by its name (.png, .jpg, .jpeg, .webp)", rel)
		}
	case ".png", ".jpg", ".jpeg", ".webp":
		if kind != "image" {
			return "", fmt.Errorf("%q is not a text file by its name (.md, .txt)", rel)
		}
	default:
		return "", fmt.Errorf("%q has an extension that is not allowed here", rel)
	}
	root, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", err
	}
	if root, err = filepath.Abs(root); err != nil {
		return "", err
	}
	full, err := filepath.EvalSymlinks(filepath.Join(root, clean))
	if err != nil {
		return "", err
	}
	back, err := filepath.Rel(root, full)
	if err != nil || back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) || filepath.IsAbs(back) {
		return "", fmt.Errorf("%q leaves the folder of the import file", rel)
	}
	if d := filepath.Dir(back); d != "." && d != attDir {
		return "", fmt.Errorf("%q leads out of the allowed folders", rel)
	}
	st, err := os.Stat(full)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("%q is not a regular file", rel)
	}
	return full, nil
}

func attDirHint(attDir string) string {
	if attDir == "" {
		return " (a subfolder needs attachments_dir in the import file)"
	}
	return " or in " + attDir
}

// readImportText liest eine Rohfassung aus der Importdatei-Umgebung: .md oder .txt,
// gültiges UTF-8.
func readImportText(base, rel, attDir string) (string, error) {
	path, err := safeImportPath(base, rel, attDir, "text")
	if err != nil {
		return "", err
	}
	text, err := readTextArg(path, nil, idea.MaxRawBytes)
	if err != nil {
		return "", err
	}
	if !utf8.ValidString(text) {
		return "", fmt.Errorf("%q is not valid UTF-8 text", rel)
	}
	return text, nil
}

// readImportImage liest ein Bild: Endung, dann Magic Bytes (PNG, JPEG, WebP).
func readImportImage(base, rel, attDir string) ([]byte, error) {
	path, err := safeImportPath(base, rel, attDir, "image")
	if err != nil {
		return nil, err
	}
	data, err := readImageArg(path)
	if err != nil {
		return nil, err
	}
	if _, err := idea.CheckImage(data, ""); err != nil {
		return nil, fmt.Errorf("%q: %w", rel, err)
	}
	return data, nil
}

// importFile ist eine lokale Datei, die der Import lesen würde.
type importFile struct {
	Kind string `json:"kind"`
	Rel  string `json:"file"`
	Size int    `json:"bytes"`
}

// planImportFiles löst alle lokalen Dateien der Importdatei auf und prüft sie,
// ohne etwas hochzuladen.
func planImportFiles(base string, in ideaImportFile) ([]importFile, error) {
	var out []importFile
	for _, e := range in.Ideas {
		if e.Raw != nil && e.Raw.File != "" {
			text, err := readImportText(base, e.Raw.File, in.AttachmentsDir)
			if err != nil {
				return nil, err
			}
			out = append(out, importFile{"text", e.Raw.File, len(text)})
		}
		for _, q := range e.Decisions {
			for _, v := range q.Variants {
				if v.Image == "" {
					continue
				}
				data, err := readImportImage(base, v.Image, in.AttachmentsDir)
				if err != nil {
					return nil, err
				}
				out = append(out, importFile{"image", v.Image, len(data)})
			}
		}
	}
	return out, nil
}
