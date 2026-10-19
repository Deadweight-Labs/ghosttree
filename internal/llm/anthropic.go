package llm

import (
	"context"
	"fmt"
	"strings"
)

func (c *httpClient) completeAnthropic(ctx context.Context, system string, msgs []Message, maxTokens int) (string, error) {
	text, _, err := c.completeAnthropicUsage(ctx, system, msgs, maxTokens)
	return text, err
}

func (c *httpClient) completeAnthropicUsage(ctx context.Context, system string, msgs []Message, maxTokens int) (string, Usage, error) {
	messages := make([]map[string]string, 0, len(msgs))
	for _, m := range msgs {
		messages = append(messages, map[string]string{"role": m.Role, "content": m.Content})
	}
	payload := map[string]any{"model": c.cfg.Model, "system": system, "messages": messages, "max_tokens": maxTokens}
	var out struct {
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens      int `json:"input_tokens"`
			OutputTokens     int `json:"output_tokens"`
			CacheReadTokens  int `json:"cache_read_input_tokens"`
			CacheWriteTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := c.post(ctx, "/v1/messages", payload, &out); err != nil {
		return "", Usage{}, err
	}
	usage := Usage{InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens,
		CacheReadTokens: out.Usage.CacheReadTokens, CacheWriteTokens: out.Usage.CacheWriteTokens}
	for _, block := range out.Content {
		if block.Type == "text" {
			return block.Text, usage, nil
		}
	}
	types := make([]string, 0, len(out.Content))
	for _, block := range out.Content {
		types = append(types, block.Type)
	}
	return "", usage, &NoTextError{StopReason: out.StopReason, ContentTypes: types, OutputTokens: out.Usage.OutputTokens, MaxTokens: maxTokens}
}

// NoTextError says an Anthropic reply carried no text block. It names the stop
// reason, the block types and the token counts, never the content.
type NoTextError struct {
	StopReason   string
	ContentTypes []string
	OutputTokens int
	MaxTokens    int
}

func (e *NoTextError) Error() string {
	return fmt.Sprintf("anthropic response has no text content (stop_reason=%q, content=[%s], output_tokens=%d, max_tokens=%d)",
		e.StopReason, strings.Join(e.ContentTypes, ","), e.OutputTokens, e.MaxTokens)
}
