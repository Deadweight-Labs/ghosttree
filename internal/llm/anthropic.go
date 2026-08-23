package llm

import (
	"context"
	"fmt"
)

func (c *httpClient) completeAnthropic(ctx context.Context, system string, msgs []Message, maxTokens int) (string, error) {
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
	}
	if err := c.post(ctx, "/v1/messages", payload, &out); err != nil {
		return "", err
	}
	for _, block := range out.Content {
		if block.Type == "text" {
			return block.Text, nil
		}
	}
	return "", fmt.Errorf("anthropic response has no text content")
}
