package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Tool use in both wire formats (REQ-698). A conversation is a list of turns;
// a user turn carries text or the results of the tool calls of the turn before
// it, an assistant turn carries text and tool calls. ChatTools sends it with
// the tools and returns the next assistant turn. The names of tools are the
// names on the wire: letters, digits, _ and - (both formats demand that).

// Tool is one tool the model may call: its name, what it does and the JSON
// schema of its input.
type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

// ToolCall is one call the model made.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// ToolResult answers one call.
type ToolResult struct {
	CallID  string
	Content string
	IsError bool
}

// Turn is one turn of a conversation with tools.
type Turn struct {
	// Role is "user" or "assistant".
	Role    string
	Text    string
	Calls   []ToolCall
	Results []ToolResult
}

// ToolReply is the next assistant turn.
type ToolReply struct {
	Text  string
	Calls []ToolCall
	// Stop is why the model stopped: end_turn, tool_use, max_tokens (Anthropic
	// words; OpenAI's stop, tool_calls and length are mapped onto them).
	Stop  string
	Usage Usage
}

// ToolClient talks with tools.
type ToolClient interface {
	// ChatTools sends the conversation. force names a tool the model has to
	// call in this turn; empty lets it choose.
	ChatTools(ctx context.Context, system string, turns []Turn, tools []Tool, force string, maxTokens int) (ToolReply, error)
}

// ChatTools implements ToolClient for both formats.
func (c *httpClient) ChatTools(ctx context.Context, system string, turns []Turn, tools []Tool, force string, maxTokens int) (ToolReply, error) {
	if c.anthropic {
		return c.chatToolsAnthropic(ctx, system, turns, tools, force, maxTokens)
	}
	return c.chatToolsOpenAI(ctx, system, turns, tools, force, maxTokens)
}

func schemaOf(t Tool) json.RawMessage {
	if len(t.Schema) == 0 {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return t.Schema
}

func inputOf(raw json.RawMessage) json.RawMessage {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

func (c *httpClient) chatToolsAnthropic(ctx context.Context, system string, turns []Turn, tools []Tool, force string, maxTokens int) (ToolReply, error) {
	messages := make([]map[string]any, 0, len(turns))
	for _, t := range turns {
		var blocks []map[string]any
		for _, r := range t.Results {
			b := map[string]any{"type": "tool_result", "tool_use_id": r.CallID, "content": r.Content}
			if r.IsError {
				b["is_error"] = true
			}
			blocks = append(blocks, b)
		}
		// The API refuses a text block of white space only, and a message
		// without blocks (content null).
		if strings.TrimSpace(t.Text) != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": t.Text})
		}
		for _, call := range t.Calls {
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": call.ID, "name": call.Name, "input": inputOf(call.Input)})
		}
		if len(blocks) == 0 {
			blocks = append(blocks, map[string]any{"type": "text", "text": "(empty)"})
		}
		messages = append(messages, map[string]any{"role": t.Role, "content": blocks})
	}
	wire := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		wire = append(wire, map[string]any{"name": t.Name, "description": t.Description, "input_schema": schemaOf(t)})
	}
	payload := map[string]any{"model": c.cfg.Model, "system": system, "messages": messages, "max_tokens": maxTokens}
	if len(wire) > 0 {
		payload["tools"] = wire
		if force != "" {
			payload["tool_choice"] = map[string]string{"type": "tool", "name": force}
		}
	}
	var out struct {
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		Usage struct {
			InputTokens      int `json:"input_tokens"`
			OutputTokens     int `json:"output_tokens"`
			CacheReadTokens  int `json:"cache_read_input_tokens"`
			CacheWriteTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := c.post(ctx, "/v1/messages", payload, &out); err != nil {
		return ToolReply{}, err
	}
	reply := ToolReply{Stop: out.StopReason, Usage: Usage{InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens,
		CacheReadTokens: out.Usage.CacheReadTokens, CacheWriteTokens: out.Usage.CacheWriteTokens}}
	var text []string
	for _, b := range out.Content {
		switch b.Type {
		case "text":
			text = append(text, b.Text)
		case "tool_use":
			reply.Calls = append(reply.Calls, ToolCall{ID: b.ID, Name: b.Name, Input: inputOf(b.Input)})
		}
	}
	reply.Text = strings.Join(text, "\n")
	return reply, nil
}

func (c *httpClient) chatToolsOpenAI(ctx context.Context, system string, turns []Turn, tools []Tool, force string, maxTokens int) (ToolReply, error) {
	messages := make([]map[string]any, 0, len(turns)+1)
	if system != "" {
		messages = append(messages, map[string]any{"role": "system", "content": system})
	}
	for _, t := range turns {
		// The results of tool calls are messages of their own, one per call.
		for _, r := range t.Results {
			content := r.Content
			if r.IsError {
				content = "error: " + content
			}
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": r.CallID, "content": content})
		}
		if t.Role == "assistant" {
			m := map[string]any{"role": "assistant", "content": t.Text}
			if len(t.Calls) > 0 {
				calls := make([]map[string]any, 0, len(t.Calls))
				for _, call := range t.Calls {
					calls = append(calls, map[string]any{"id": call.ID, "type": "function",
						"function": map[string]string{"name": call.Name, "arguments": string(inputOf(call.Input))}})
				}
				m["tool_calls"] = calls
			}
			messages = append(messages, m)
			continue
		}
		if t.Text != "" {
			messages = append(messages, map[string]any{"role": "user", "content": t.Text})
		}
	}
	wire := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		wire = append(wire, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": schemaOf(t)}})
	}
	payload := map[string]any{"model": c.cfg.Model, "messages": messages, "max_completion_tokens": maxTokens}
	if len(wire) > 0 {
		payload["tools"] = wire
		if force != "" {
			payload["tool_choice"] = map[string]any{"type": "function", "function": map[string]string{"name": force}}
		}
	}
	var out struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := c.post(ctx, "/v1/chat/completions", payload, &out); err != nil {
		return ToolReply{}, err
	}
	if len(out.Choices) == 0 {
		return ToolReply{}, fmt.Errorf("openai response has no choices")
	}
	ch := out.Choices[0]
	reply := ToolReply{Text: ch.Message.Content, Usage: Usage{InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens}}
	switch ch.FinishReason {
	case "tool_calls":
		reply.Stop = "tool_use"
	case "length":
		reply.Stop = "max_tokens"
	default:
		reply.Stop = "end_turn"
	}
	for _, call := range ch.Message.ToolCalls {
		input := json.RawMessage(call.Function.Arguments)
		if !json.Valid(input) {
			// A model that wrote broken arguments gets the error back as the
			// tool's answer; the raw text stays a string so the turn can be sent again.
			b, _ := json.Marshal(call.Function.Arguments)
			input = b
		}
		reply.Calls = append(reply.Calls, ToolCall{ID: call.ID, Name: call.Function.Name, Input: inputOf(input)})
	}
	if len(reply.Calls) > 0 {
		reply.Stop = "tool_use"
	}
	return reply, nil
}
