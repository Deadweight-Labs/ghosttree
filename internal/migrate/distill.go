package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/llm"
)

type Item struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Quote  string `json:"quote"`
	Source string `json:"source"`
}

type Result struct {
	Items   []Item   `json:"items"`
	Dropped []string `json:"dropped"`
}

const distillSystem = `You distill agent artifacts from a repository into durable knowledge.
Return one JSON object with arrays "items" and "dropped". Item types are instruction,
decision, note, pitfall, or request. An instruction must not be derivable by reading the
repo: directory layout and visible implementation details are derivable and must be
dropped, while a required build command or a damaging-to-violate architecture rule is
not. When uncertain, omit it and explain why in dropped. Keep all instruction bodies
to at most 1500 characters total. Do not duplicate an existing title. Every item needs
a short title, self-contained body, and one exact contiguous quote copied from
the source. Never use ellipses and never join separate source passages.`

func Distill(ctx context.Context, c llm.Client, a Artifact, content string, existing []string) (Result, error) {
	activationNote := ""
	if len(a.Activation.Paths) > 0 {
		activationNote = fmt.Sprintf("\nProposed activation paths: %s\nThis gate is metadata, not content. Do not repeat it in item bodies; the model must not invent task gates.\n", strings.Join(a.Activation.Paths, ", "))
	}
	user := fmt.Sprintf("Source: %s\nKind: %s%s\nExisting titles (do not duplicate):\n- %s\n\nContent:\n%s",
		a.Rel, a.Kind, activationNote, strings.Join(existing, "\n- "), content)
	msgs := []llm.Message{{Role: "user", Content: user}}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		var raw string
		var err error
		if jc, ok := c.(llm.JSONClient); ok {
			raw, err = jc.CompleteJSON(ctx, distillSystem, msgs, 3000)
		} else {
			raw, err = c.Complete(ctx, distillSystem, msgs, 3000)
		}
		if err != nil {
			return Result{}, err
		}
		result, err := parseDistilled(raw, a, content)
		if err == nil {
			return result, nil
		}
		lastErr = err
		msgs = append(msgs, llm.Message{Role: "assistant", Content: raw}, llm.Message{Role: "user", Content: "Your response was rejected: " + err.Error() + ". Return the complete corrected JSON object. Every quote must be one exact contiguous passage copied from the source, without ellipses or paraphrasing."})
	}
	return Result{}, lastErr
}

func parseDistilled(raw string, a Artifact, content string) (Result, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```") {
		raw = strings.TrimPrefix(raw, "```json")
		raw = strings.TrimPrefix(raw, "```")
		raw = strings.TrimSuffix(strings.TrimSpace(raw), "```")
	}
	var wire struct {
		Items   []Item            `json:"items"`
		Dropped []json.RawMessage `json:"dropped"`
	}
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return Result{}, fmt.Errorf("decode distilled output: %w", err)
	}
	result := Result{Items: wire.Items}
	for _, value := range wire.Dropped {
		var reason string
		if err := json.Unmarshal(value, &reason); err != nil {
			reason = string(value)
		}
		result.Dropped = append(result.Dropped, reason)
	}
	allowed := map[string]bool{"instruction": true, "decision": true, "note": true, "pitfall": true, "request": true}
	for i := range result.Items {
		item := &result.Items[i]
		if !allowed[item.Type] {
			return Result{}, fmt.Errorf("distilled item has invalid type %q", item.Type)
		}
		if strings.TrimSpace(item.Title) == "" || strings.TrimSpace(item.Body) == "" || strings.TrimSpace(item.Quote) == "" {
			return Result{}, fmt.Errorf("distilled item %d is incomplete", i)
		}
		if grounded, ok := groundQuote(content, item.Quote); ok {
			item.Quote = grounded
		} else {
			return Result{}, fmt.Errorf("distilled item %d quote is not present in source: %q", i, item.Quote)
		}
		item.Source = a.Rel
	}
	return result, nil
}

func groundQuote(content, quote string) (string, bool) {
	if strings.Contains(content, quote) {
		return quote, true
	}
	fields := strings.Fields(quote)
	if len(fields) == 0 {
		return "", false
	}
	parts := make([]string, len(fields))
	for i, field := range fields {
		parts[i] = regexp.QuoteMeta(field)
	}
	loc := regexp.MustCompile(strings.Join(parts, `\s+`)).FindStringIndex(content)
	if loc == nil {
		for i, field := range fields {
			plain := strings.Trim(field, "*_`")
			if plain == "" {
				return "", false
			}
			parts[i] = "[*_`]*" + regexp.QuoteMeta(plain) + "[*_`]*"
		}
		loc = regexp.MustCompile(strings.Join(parts, "(?:\\s|[*_`])+")).FindStringIndex(content)
		if loc == nil {
			return "", false
		}
	}
	return content[loc[0]:loc[1]], true
}
