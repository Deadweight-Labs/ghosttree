package workflow

import (
	"slices"
	"sort"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Kinds of results a run may create (REQ-695). A workflow declares which of
// them it creates and how many per run; nothing else gets written.
const (
	ResultKnowledge   = "knowledge"
	ResultIssue       = "issue"
	ResultCriterion   = "criterion"
	ResultRelation    = "relation"
	ResultDescription = "description"
)

// ResultKinds lists every kind of result, in the order the pages show them.
var ResultKinds = []string{ResultKnowledge, ResultIssue, ResultCriterion, ResultRelation, ResultDescription}

// InputSpec is one input the machine puts together for a run: a fixed piece of
// the event or of the project. Events lists the events that provide it; empty
// means every event.
type InputSpec struct {
	Name    string
	Events  []string
	Summary string
}

// Inputs is the catalogue of inputs of stage E2: what the event carries. Later
// stages add assembled inputs (the diff, the sessions of a pull request, the
// related ideas).
var Inputs = []InputSpec{
	{Name: "event", Summary: "the event as JSON"},
	{Name: "pr.number", Events: []string{store.EventPRMerged}, Summary: "number of the pull request"},
	{Name: "pr.title", Events: []string{store.EventPRMerged}, Summary: "title of the pull request"},
	{Name: "pr.base", Events: []string{store.EventPRMerged}, Summary: "branch it was merged into"},
	{Name: "pr.head", Events: []string{store.EventPRMerged}, Summary: "branch it came from"},
	{Name: "pr.merge_sha", Events: []string{store.EventPRMerged}, Summary: "commit of the merge"},
	{Name: "idea.number", Events: []string{store.EventIdeaCreated}, Summary: "number of the idea"},
	{Name: "idea.title", Events: []string{store.EventIdeaCreated}, Summary: "title of the idea"},
	{Name: "schedule.slot", Events: []string{store.EventSchedule}, Summary: "the period of the schedule"},
}

// InputByName finds an input of the catalogue.
func InputByName(name string) (InputSpec, bool) {
	i := slices.IndexFunc(Inputs, func(s InputSpec) bool { return s.Name == name })
	if i < 0 {
		return InputSpec{}, false
	}
	return Inputs[i], true
}

// inputValue is the value of an input for an event; ok is false when the event
// does not provide it.
func inputValue(name string, ev store.WorkflowEvent, eventJSON string) (string, bool) {
	spec, ok := InputByName(name)
	if !ok || (len(spec.Events) > 0 && !slices.Contains(spec.Events, ev.Kind)) {
		return "", false
	}
	switch name {
	case "event":
		return eventJSON, true
	case "pr.number", "idea.number":
		return ev.SubjectID, true
	case "pr.title", "idea.title":
		return ev.Data["title"], true
	case "pr.base":
		return ev.Data["base"], true
	case "pr.head":
		return ev.Data["head"], true
	case "pr.merge_sha":
		return ev.Data["merge_sha"], true
	case "schedule.slot":
		return ev.Data["slot"], true
	}
	return "", false
}

// BlockSpec describes a block of the catalogue: what it serves, reads and may
// write, and whether it calls a model. A block with Run nil runs outside the
// engine (the gardener's sandboxed unit) and only reports its runs.
type BlockSpec struct {
	Name    string
	Version string
	Summary string
	// Events the block can be triggered by; empty means every event.
	Events []string
	// Reads names what the block reads beyond its declared inputs.
	Reads []string
	// Results are the kinds of result the block can produce.
	Results []string
	// Model: the block calls a model and needs a model alias.
	Model bool
	// Instruction: the block takes an instruction of the team (stage 2).
	Instruction bool
	// External: the block runs outside the engine and only reports its runs.
	External bool
	Run      Block
}

// BlockPrefix is the namespace of the blocks ghosttree ships.
const BlockPrefix = "ghosttree/"

// Catalog is the set of blocks a server knows, by name.
type Catalog map[string]BlockSpec

// DefaultCatalog is what every server ships with in stage E2.
func DefaultCatalog() Catalog {
	c := Catalog{}
	for _, b := range []BlockSpec{
		{Name: EchoName, Version: "v1", Summary: "echoes its event; needs no model and costs nothing", Run: Echo},
		{Name: AskName, Version: "v1", Summary: "one model call that describes the event in a sentence", Model: true, Instruction: true, Run: Ask},
	} {
		c[b.Name] = b
	}
	return c
}

// Lookup finds a block by the name a definition uses: "ghosttree/ask" or
// "ghosttree/ask@v1".
func (c Catalog) Lookup(uses string) (BlockSpec, bool) {
	name, version, _ := strings.Cut(strings.TrimSpace(uses), "@")
	b, ok := c[name]
	if !ok || (version != "" && version != b.Version) {
		return BlockSpec{}, false
	}
	return b, true
}

// Names lists the blocks in alphabetical order.
func (c Catalog) Names() []string {
	out := make([]string, 0, len(c))
	for n := range c {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Builtins are the blocks of the catalogue that run in the engine, by name;
// the resolver of E0 still takes them for triggers that name a block directly.
func (c Catalog) Builtins() Builtins {
	b := Builtins{}
	for n, s := range c {
		if s.Run != nil {
			b[n] = s.Run
		}
	}
	return b
}
