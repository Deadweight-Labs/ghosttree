package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/Deadweight-Labs/ghosttree/internal/coretools"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// alwaysLoadMeta asks Claude Code to load a tool with its description instead
// of deferring it behind tool search.
const alwaysLoadMeta = "anthropic/alwaysLoad"

// coreRouter shows only the core tools in tools/list and routes a call of a
// core tool with its action to the registered tool that serves it. The
// registered tools stay callable under their own names, they are only no
// longer listed.
type coreRouter struct {
	srv *Server

	mu      sync.Mutex
	listed  []*mcp.Tool
	schemas map[string]*jsonschema.Schema
}

func (s *Server) coreMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	r := &coreRouter{srv: s}
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		switch method {
		case "tools/list":
			res, err := next(ctx, method, req)
			if err != nil {
				return res, err
			}
			list, ok := res.(*mcp.ListToolsResult)
			if !ok {
				return res, nil
			}
			r.learn(list.Tools)
			if listsRegistered(req) {
				return res, nil
			}
			out := *list
			out.Tools = r.list()
			return &out, nil
		case "tools/call":
			call, ok := req.(*mcp.CallToolRequest)
			if !ok || call.Params == nil {
				return next(ctx, method, req)
			}
			return r.call(ctx, next, call)
		}
		return next(ctx, method, req)
	}
}

// registeredMeta in the _meta of a tools/list request asks for every
// registered tool instead of the core list: for tests and diagnosis, never
// shown to an agent by a harness.
const registeredMeta = "ghosttree/registered"

func listsRegistered(req mcp.Request) bool {
	l, ok := req.(*mcp.ListToolsRequest)
	if !ok || l.Params == nil {
		return false
	}
	v, _ := l.Params.Meta[registeredMeta].(bool)
	return v
}

// learn keeps the input schemas of the registered tools and builds the core
// list once.
func (r *coreRouter) learn(tools []*mcp.Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.schemas != nil {
		return
	}
	r.schemas = map[string]*jsonschema.Schema{}
	for _, t := range tools {
		if t == nil {
			continue
		}
		if s := asSchema(t.InputSchema); s != nil {
			r.schemas[t.Name] = s
		}
	}
	for _, c := range coretools.Table() {
		r.listed = append(r.listed, r.coreTool(c, tools))
	}
}

func (r *coreRouter) list() []*mcp.Tool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*mcp.Tool(nil), r.listed...)
}

func (r *coreRouter) schema(tool string) *jsonschema.Schema {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.schemas[tool]
}

// ensure loads the registered tools when a call comes before any tools/list
// (a client that knows the names already).
func (r *coreRouter) ensure(ctx context.Context, next mcp.MethodHandler, call *mcp.CallToolRequest) {
	r.mu.Lock()
	loaded := r.schemas != nil
	r.mu.Unlock()
	if loaded {
		return
	}
	res, err := next(ctx, "tools/list", &mcp.ListToolsRequest{Session: call.Session, Params: &mcp.ListToolsParams{}})
	if err != nil {
		return
	}
	if list, ok := res.(*mcp.ListToolsResult); ok {
		r.learn(list.Tools)
	}
}

func (r *coreRouter) call(ctx context.Context, next mcp.MethodHandler, call *mcp.CallToolRequest) (mcp.Result, error) {
	name := call.Params.Name
	if !coretools.IsCore(name) {
		res, err := next(ctx, "tools/call", call)
		r.srv.noteToolCall(call, name, inputAction(call.Params.Arguments), res, err)
		return res, err
	}
	r.ensure(ctx, next, call)
	args := map[string]any{}
	if len(call.Params.Arguments) > 0 && string(call.Params.Arguments) != "null" {
		if err := json.Unmarshal(call.Params.Arguments, &args); err != nil {
			return toolError("arguments must be a JSON object"), nil
		}
	}
	action, _ := args["action"].(string)
	if raw, present := args["action"]; present && action == "" && raw != nil {
		return toolError("action must be a string"), nil
	}
	route, ok := coretools.Route(name, strings.TrimSpace(action))
	if !ok {
		if action == "" {
			return toolError(fmt.Sprintf("%s needs an action: %s", name, strings.Join(coretools.ActionNames(name), ", "))), nil
		}
		return toolError(fmt.Sprintf("%s has no action %q; it knows %s", name, action, strings.Join(coretools.ActionNames(name), ", "))), nil
	}
	delete(args, "action")
	if route.Inner != "" {
		args["action"] = route.Inner
	}
	if msg := r.fieldProblems(name, route, args); msg != "" {
		return toolError(msg), nil
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return toolError(err.Error()), nil
	}
	inner := *call.Params
	inner.Name = route.Tool
	inner.Arguments = raw
	routed := *call
	routed.Params = &inner
	res, err := next(ctx, "tools/call", &routed)
	textOnly(res)
	r.srv.noteToolCall(call, name, route.Name, res, err)
	return res, err
}

func emptyValue(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case bool:
		return !x
	case float64:
		return x == 0
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}

func toolError(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}
}

func inputAction(raw json.RawMessage) string {
	var in struct {
		Action string `json:"action"`
	}
	_ = json.Unmarshal(raw, &in)
	return in.Action
}

// coreTool builds the listed tool: the lead, one line per action, and an input
// schema that is the union of the fields of the tools behind the actions.
func (r *coreRouter) coreTool(c coretools.Core, tools []*mcp.Tool) *mcp.Tool {
	byName := map[string]*mcp.Tool{}
	for _, t := range tools {
		if t != nil {
			byName[t.Name] = t
		}
	}
	var desc strings.Builder
	desc.WriteString(c.Lead)
	if c.Name == "context_search" {
		desc.WriteString(" " + searchLanguageHelp)
	}
	desc.WriteString("\nActions")
	if c.Default != "" {
		desc.WriteString(" (default " + c.Default + ")")
	}
	desc.WriteString(":\n")
	props := map[string]*jsonschema.Schema{}
	var order []string
	readOnly := true
	for _, a := range c.Actions {
		fmt.Fprintf(&desc, "- %s: %s\n", a.Name, a.Line)
		t := byName[a.Tool]
		if t == nil {
			continue
		}
		if t.Annotations == nil || !t.Annotations.ReadOnlyHint {
			readOnly = false
		}
		s := asSchema(t.InputSchema)
		if s == nil || a.Rare {
			continue
		}
		names := make([]string, 0, len(s.Properties))
		for k := range s.Properties {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			if k == "action" {
				continue
			}
			p := s.Properties[k]
			if have, ok := props[k]; ok {
				mergeType(have, p)
				continue
			}
			n := slimProperty(p)
			if !fullDescriptions[k] {
				n.Description = cutDescription(n.Description, propertyDescriptionRunes)
			}
			props[k] = n
			order = append(order, k)
		}
	}
	actionProp := &jsonschema.Schema{Type: "string", Enum: enumOf(coretools.ActionNames(c.Name))}
	required := []string{"action"}
	if c.Default != "" {
		actionProp.Description = "default " + c.Default
		required = nil
	}
	schema := &jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{"action": actionProp}, Required: required}
	schema.PropertyOrder = append([]string{"action"}, order...)
	shapes := map[string]string{}
	for _, k := range order {
		schema.Properties[k] = sayShapeOnce(shapes, k, props[k])
	}
	closed := false
	tool := &mcp.Tool{
		Name:        c.Name,
		Description: strings.TrimRight(desc.String(), "\n"),
		InputSchema: schema,
		Annotations: &mcp.ToolAnnotations{OpenWorldHint: &closed, ReadOnlyHint: readOnly},
	}
	// A tool the profile does not load up front is still listed, with its name,
	// and a harness that defers tools loads it when the agent looks for it
	// (Claude Code through tool search, Codex through tool_search). Dropping the
	// tool from the list instead would leave a harness that only calls listed
	// tools no way to reach it.
	if r.srv.profile.Preloads(c.Tier) {
		tool.Meta = mcp.Meta{alwaysLoadMeta: true}
	}
	return tool
}

// instructions is the text the server hands the harness at the start of the
// session. Claude Code puts it into the first turn next to the names of the
// deferred tools, which is what tells an agent that a search for a tool is
// worth making: nothing else in the first turn says that the tools exist.
func (s *Server) instructions() string {
	deferred := s.profile.Deferred()
	if len(deferred) == 0 {
		return ""
	}
	return "ghosttree tools not loaded up front in this session: " + strings.Join(deferred, ", ") +
		". Each is listed by name and takes an `action`; look it up with tool search when you need it."
}

func enumOf(names []string) []any {
	out := make([]any, len(names))
	for i, n := range names {
		out[i] = n
	}
	return out
}

// slimProperty copies a property with the description rewritten so that it
// names core tools, not the registered ones an agent no longer sees.
func slimProperty(p *jsonschema.Schema) *jsonschema.Schema {
	return slimSchema(p)
}

// fullDescriptions are the fields whose whole description stays in the list:
// the judgement only the agent can make and that is most often made wrong.
var fullDescriptions = map[string]bool{"scope_hint": true}

// propertyDescriptionRunes is where a field description in the core list is
// cut, at the end of a sentence where there is one. The registered tool keeps
// the whole text and its validation errors name the fields it takes.
const propertyDescriptionRunes = 120

func slimSchema(p *jsonschema.Schema) *jsonschema.Schema {
	if p == nil {
		return &jsonschema.Schema{}
	}
	c := *p
	c.Description = RewriteToolNames(c.Description)
	// Nullable slices and pointers are inferred as ["null", T]; the agent only
	// needs T, and the registered tool still accepts null.
	if len(c.Types) == 2 && (c.Types[0] == "null" || c.Types[1] == "null") {
		c.Type = c.Types[0]
		if c.Type == "null" {
			c.Type = c.Types[1]
		}
		c.Types = nil
	}
	c.AdditionalProperties = nil
	if c.Items != nil {
		c.Items = slimSchema(c.Items)
	}
	if len(c.Properties) > 0 {
		props := make(map[string]*jsonschema.Schema, len(c.Properties))
		for k, v := range c.Properties {
			n := slimSchema(v)
			if !fullDescriptions[k] {
				n.Description = cutDescription(n.Description, propertyDescriptionRunes/2)
			}
			props[k] = n
		}
		c.Properties = props
	}
	return &c
}

// sayShapeOnce describes the fields of a list of objects where the list first
// occurs in a tool. A second field with the same items (what a pain motivates,
// and what is added to that later) names the first instead of repeating it.
func sayShapeOnce(seen map[string]string, name string, p *jsonschema.Schema) *jsonschema.Schema {
	if p.Items == nil || len(p.Items.Properties) == 0 {
		return p
	}
	raw, err := json.Marshal(p.Items)
	if err != nil {
		return p
	}
	first, ok := seen[string(raw)]
	if !ok {
		seen[string(raw)] = name
		return p
	}
	c, items := *p, *p.Items
	items.Properties = make(map[string]*jsonschema.Schema, len(p.Items.Properties))
	for k, v := range p.Items.Properties {
		bare := *v
		bare.Description = ""
		items.Properties[k] = &bare
	}
	c.Items = &items
	c.Description = strings.TrimRight(c.Description, ". ") + " (items as in " + first + ")"
	return &c
}

// cutDescription shortens a description to about limit runes, preferring the
// end of a sentence or clause.
func cutDescription(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	head := string(r[:limit])
	for _, sep := range []string{". ", "; ", ": ", ", "} {
		if i := strings.LastIndex(head, sep); i >= limit/3 {
			out := head[:i]
			if sep == ". " {
				return out + "."
			}
			return out + " …"
		}
	}
	if i := strings.LastIndex(head, " "); i > 0 {
		head = head[:i]
	}
	return head + " …"
}

// mergeType widens a shared property when two actions give it different
// types; the server checks the exact type of the routed tool.
func mergeType(have, p *jsonschema.Schema) {
	if p == nil {
		return
	}
	types := map[string]bool{}
	for _, t := range append(append([]string{have.Type}, have.Types...), append([]string{p.Type}, p.Types...)...) {
		// null only says that the field may be left out, which every field of
		// a core tool may.
		if t != "" && t != "null" {
			types[t] = true
		}
	}
	if len(types) <= 1 {
		return
	}
	var list []string
	for t := range types {
		list = append(list, t)
	}
	sort.Strings(list)
	have.Type, have.Types = "", list
	if have.Items == nil {
		have.Items = p.Items
	}
}

func asSchema(v any) *jsonschema.Schema {
	switch s := v.(type) {
	case *jsonschema.Schema:
		return s
	case nil:
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var s jsonschema.Schema
	if json.Unmarshal(raw, &s) != nil {
		return nil
	}
	return &s
}

var toolNamePattern = regexp.MustCompile(`\b[a-z]+(?:_[a-z]+)+\b`)

// RewriteToolNames replaces the names of registered tools that are no longer
// listed by the core tool and action an agent calls instead.
func RewriteToolNames(text string) string {
	return toolNamePattern.ReplaceAllStringFunc(text, func(w string) string {
		if coretools.IsCore(w) {
			return w
		}
		for _, t := range coretools.Tools() {
			if t == w {
				return coretools.Ref(w)
			}
		}
		return w
	})
}
