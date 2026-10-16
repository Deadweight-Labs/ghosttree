package llm

import (
	"context"
	"fmt"
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
		Content []struct {
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
	return "", usage, fmt.Errorf("anthropic response has no text content")
}
