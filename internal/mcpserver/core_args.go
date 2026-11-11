package mcpserver

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/coretools"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// guessedFields maps a field name an agent guessed to the fields it most
// likely meant, most likely first. The names come from failed calls in session
// transcripts: one tool says request_id where its neighbour says id, body where
// the neighbour says description. A guess is only followed when the action
// does not take the guessed name, takes the meant one, and that one is not
// given as well.
var guessedFields = map[string][]string{
	"id":           {"request_id", "knowledge_id", "package_id", "session_id"},
	"request":      {"request_id"},
	"req":          {"request_id"},
	"request_id":   {"id"},
	"knowledge_id": {"id"},
	"pain_id":      {"id"},
	"idea_id":      {"id", "idea"},
	"task_id":      {"id"},
	"feature_id":   {"id"},
	"thread_id":    {"id"},
	"package":      {"package_id"},
	"criterion":    {"criterion_id"},
	"content":      {"body", "description"},
	"text":         {"body", "description"},
	"message":      {"body"},
	"description":  {"body"},
	"body":         {"description"},
	"kind":         {"type"},
	"type":         {"kind"},
	"check":        {"check_hint"},
	"where":        {"location"},
	"scope":        {"scope_hint"},
	"name":         {"title"},
	"title":        {"name"},
	"q":            {"query"},
	"search":       {"query"},
	"file":         {"path"},
	"file_path":    {"path"},
	"evidence":     {"evidence_ref"},
	"why":          {"reason"},
	"format":       {"response_format"},
}

// detailFlags are the ways an agent asks for the long form of an answer where
// the field is response_format.
var detailFlags = map[string]bool{"detailed": true, "detail": true, "full": true, "verbose": true}

var numberedRef = regexp.MustCompile(`^(?:[A-Za-z]+-|#)?(\d+)$`)

// adaptFields turns what the agent sent into what the action takes, where the
// intent is not in doubt: a guessed field name, a flag for the long form, and
// a reference like REQ-12 where the number is asked for.
func adaptFields(s *jsonschema.Schema, args map[string]any) {
	if s == nil || s.Properties == nil {
		return
	}
	names := make([]string, 0, len(args))
	for k := range args {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		v := args[k]
		if _, ok := s.Properties[k]; ok || emptyValue(v) {
			continue
		}
		if detailFlags[k] {
			if _, ok := s.Properties["response_format"]; ok && emptyValue(args["response_format"]) && asksForDetail(v) {
				args["response_format"] = "detailed"
				delete(args, k)
			}
			continue
		}
		for _, meant := range guessedFields[k] {
			if _, ok := s.Properties[meant]; ok && emptyValue(args[meant]) {
				args[meant] = v
				delete(args, k)
				break
			}
		}
	}
	for k, v := range args {
		text, ok := v.(string)
		if !ok || !takesInteger(s.Properties[k]) {
			continue
		}
		if m := numberedRef.FindStringSubmatch(strings.TrimSpace(text)); m != nil {
			if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
				args[k] = n
			}
		}
	}
}

func asksForDetail(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true", "yes", "1", "detailed", "full":
			return true
		}
	}
	return false
}

func takesInteger(p *jsonschema.Schema) bool {
	if p == nil {
		return false
	}
	if p.Type == "integer" {
		return true
	}
	for _, t := range p.Types {
		if t == "string" {
			return false
		}
	}
	for _, t := range p.Types {
		if t == "integer" {
			return true
		}
	}
	return false
}

// fieldProblems drops empty values the target does not take (a model often
// fills every field of the shared schema), names the non-empty ones together
// with the fields the action does take, and names the required fields that are
// missing with what each is for. The registered tool would refuse the same
// call, but with the wording of a schema validator.
func (r *coreRouter) fieldProblems(core string, route coretools.Action, args map[string]any) string {
	s := r.schema(route.Tool)
	if s == nil || s.Properties == nil {
		return ""
	}
	adaptFields(s, args)
	var foreign []string
	for k, v := range args {
		if _, ok := s.Properties[k]; ok {
			continue
		}
		if emptyValue(v) {
			delete(args, k)
			continue
		}
		foreign = append(foreign, k)
	}
	var missing []string
	for _, k := range s.Required {
		if _, given := args[k]; !given && k != "action" {
			missing = append(missing, k)
		}
	}
	if len(foreign) == 0 && len(missing) == 0 {
		return ""
	}
	sort.Strings(foreign)
	call := core + " action=" + route.Name
	var parts []string
	if len(foreign) > 0 {
		var takes []string
		for k := range s.Properties {
			if k != "action" {
				takes = append(takes, k)
			}
		}
		sort.Strings(takes)
		parts = append(parts, fmt.Sprintf("%s does not take %s; it takes %s", call, strings.Join(foreign, ", "), strings.Join(takes, ", ")))
	}
	if len(missing) > 0 {
		for i, k := range missing {
			if d := cutDescription(RewriteToolNames(s.Properties[k].Description), propertyDescriptionRunes); d != "" {
				missing[i] = k + " (" + d + ")"
			}
		}
		parts = append(parts, fmt.Sprintf("%s needs %s", call, strings.Join(missing, ", ")))
	}
	return strings.Join(parts, ". ")
}

// textOnly drops the structured copy of an answer that also carries text. A
// core tool declares no output schema, and a harness that is handed both shows
// its model the structured one alone: the rendered text and the card with the
// web link never arrived there, the raw records with every timestamp did.
func textOnly(res mcp.Result) {
	out, ok := res.(*mcp.CallToolResult)
	if !ok || out == nil || out.StructuredContent == nil {
		return
	}
	for _, c := range out.Content {
		if t, ok := c.(*mcp.TextContent); ok && strings.TrimSpace(t.Text) != "" {
			out.StructuredContent = nil
			return
		}
	}
}
