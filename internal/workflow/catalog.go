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
	ResultKnowledge = "knowledge"
	ResultIssue     = "issue"
	ResultCriterion = "criterion"
	// ResultRequirement is a requirement added to the concept of an idea
	// (REQ-696): accepted, it becomes a new version of the concept.
	ResultRequirement = "requirement"
	ResultRelation    = "relation"
	// ResultComment is a comment on the idea a run is about (REQ-696).
	ResultComment     = "comment"
	ResultDescription = "description"
)

// ResultKinds lists every kind of result, in the order the pages show them.
var ResultKinds = []string{ResultKnowledge, ResultIssue, ResultCriterion, ResultRequirement, ResultRelation, ResultComment, ResultDescription}

// InputSpec is one input the machine puts together for a run: a fixed piece of
// the event or of the project. Events lists the events that provide it; empty
// means every event.
type InputSpec struct {
	Name    string
	Events  []string
	Summary string
	// Assembled: the machine reads it from the project, not from the event.
	Assembled bool
	// Foreign: the text is written by someone who need not be a member of the
	// project (the author of a pull request, of a commit, the internet). A
	// result that rests on it never applies by itself.
	Foreign bool
}

// Inputs is the catalogue of inputs of stage E2: what the event carries. Later
// stages add assembled inputs (the diff, the sessions of a pull request, the
// related ideas).
var Inputs = []InputSpec{
	{Name: "event", Summary: "the event as JSON", Foreign: true},
	{Name: "pr.number", Events: []string{store.EventPRMerged}, Summary: "number of the pull request"},
	{Name: "pr.title", Events: []string{store.EventPRMerged}, Summary: "title of the pull request", Foreign: true},
	{Name: "pr.base", Events: []string{store.EventPRMerged}, Summary: "branch it was merged into", Foreign: true},
	{Name: "pr.head", Events: []string{store.EventPRMerged}, Summary: "branch it came from", Foreign: true},
	{Name: "pr.merge_sha", Events: []string{store.EventPRMerged}, Summary: "commit of the merge"},
	{Name: "idea.number", Events: ideaEvents, Summary: "number of the idea"},
	{Name: "idea.title", Events: ideaEvents, Summary: "title of the idea"},
	{Name: "schedule.slot", Events: []string{store.EventSchedule}, Summary: "the period of the schedule"},
	// Assembled from the project (stage E3), bounded in size, with the rights
	// of the account the run acts for.
	{Name: "pr.text", Events: []string{store.EventPRMerged}, Summary: "title and description of the pull request", Assembled: true, Foreign: true},
	{Name: "pr.commits", Events: []string{store.EventPRMerged}, Summary: "its commits with their messages", Assembled: true, Foreign: true},
	{Name: "pr.files", Events: []string{store.EventPRMerged}, Summary: "the files it changed", Assembled: true, Foreign: true},
	{Name: "pr.diff", Events: []string{store.EventPRMerged}, Summary: "its diff where the server can read it, else the changed files", Assembled: true, Foreign: true},
	{Name: "pr.knowledge", Events: []string{store.EventPRMerged}, Summary: "the knowledge entries written on its commits", Assembled: true},
	{Name: "pr.sessions.user_messages", Events: []string{store.EventPRMerged}, Summary: "what people typed in the sessions of its commits, nothing the agents wrote", Assembled: true},
	{Name: "knowledge.related", Events: []string{store.EventPRMerged}, Summary: "knowledge of the project its title finds", Assembled: true},
	{Name: "requests.open", Summary: "the open issues of the project", Assembled: true},
	{Name: "idea.text", Events: ideaEvents, Summary: "title, text and summary of the idea", Assembled: true},
	{Name: "idea.concept", Events: ideaEvents, Summary: "the concept of the idea with its requirements", Assembled: true},
	{Name: "idea.decisions", Events: ideaEvents, Summary: "its decisions with outcome and reason", Assembled: true},
	{Name: "idea.related", Events: ideaEvents, Summary: "other ideas of the project with words of its title", Assembled: true},
	{Name: "features.related", Events: ideaEvents, Summary: "features of the project with words of its title", Assembled: true},
}

// ideaEvents are the events about an idea.
var ideaEvents = []string{store.EventIdeaCreated, store.EventIdeaDiscussionClosed}

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
	// Inputs are the inputs of the catalogue the block always reads; the
	// machine assembles them with the declared ones.
	Inputs []string
	// Research: the block can take what a research step found, where the
	// server has a search service (ResearchURLEnv).
	Research bool
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

// DistillSessionsName is the block of the session distiller.
const DistillSessionsName = "ghosttree/distill-sessions"

// BlockPrefix is the namespace of the blocks ghosttree ships.
const BlockPrefix = "ghosttree/"

// Catalog is the set of blocks a server knows, by name.
type Catalog map[string]BlockSpec

// DefaultCatalog is what every server ships with (stage E3).
func DefaultCatalog() Catalog {
	c := Catalog{}
	for _, b := range []BlockSpec{
		{Name: EchoName, Version: "v1", Summary: "echoes its event; needs no model and costs nothing", Run: Echo},
		{Name: AskName, Version: "v1", Summary: "one model call that describes the event in a sentence", Model: true, Instruction: true, Run: Ask},
		// Robin's four examples (REQ-696): one model call each, a structured
		// answer, every result through the gate.
		{Name: PRKnowledgeName, Version: "v1", Summary: "after a merge: is the knowledge of the diff in ghosttree? proposes what is missing", Events: []string{store.EventPRMerged},
			Inputs: []string{"pr.text", "pr.commits", "pr.diff", "pr.knowledge", "knowledge.related"}, Results: []string{ResultKnowledge}, Model: true, Instruction: true, Run: PRKnowledge},
		{Name: PRWishesName, Version: "v1", Summary: "after a merge: what people asked for in its sessions and did not get becomes a proposed issue", Events: []string{store.EventPRMerged},
			Inputs: []string{"pr.sessions.user_messages", "pr.text", "pr.commits", "pr.diff", "requests.open"}, Results: []string{ResultIssue}, Model: true, Instruction: true, Run: PRWishes},
		{Name: IdeaMatchName, Version: "v1", Summary: "a new idea next to the ideas and features there are: a comment and links to features", Events: []string{store.EventIdeaCreated},
			Inputs: []string{"idea.text", "idea.related", "features.related"}, Results: []string{ResultComment, ResultRelation}, Model: true, Instruction: true, Research: true, Run: IdeaMatch},
		{Name: CriteriaName, Version: "v1", Summary: "a closed discussion: requirements for the concept, nothing while a decision is open", Events: []string{store.EventIdeaDiscussionClosed},
			Inputs: []string{"idea.text", "idea.concept", "idea.decisions"}, Results: []string{ResultRequirement}, Model: true, Instruction: true, Run: Criteria},
		// The gardener and the distillers stay Go code and run where they run
		// today (DEC-41): the gardener in its sandboxed unit, the distillers
		// in their jobs. As blocks they have one place in the catalogue; their
		// settings start them, not a workflow file, and the gardener's runs are
		// runs of its block (store.GardenBlock).
		{Name: store.GardenBlock, Version: "v1", Summary: "the gardener: descriptions of files, checks of knowledge against the code", Events: []string{store.EventSchedule},
			Reads: []string{"repository checkout", "knowledge", "requests"}, Results: []string{ResultDescription, ResultKnowledge}, Model: true, External: true},
		{Name: DistillSessionsName, Version: "v1", Summary: "the distiller: knowledge and wishes from finished sessions", Events: []string{store.EventSchedule},
			Reads: []string{"session transcripts"}, Results: []string{ResultKnowledge}, Model: true, External: true},
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
