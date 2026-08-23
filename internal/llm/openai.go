package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type httpClient struct {
	cfg       Config
	anthropic bool
}

func (c *httpClient) Complete(ctx context.Context, system string, msgs []Message, maxTokens int) (string, error) {
	if c.anthropic {
		return c.completeAnthropic(ctx, system, msgs, maxTokens)
	}
	messages := make([]map[string]string, 0, len(msgs)+1)
	if system != "" {
		messages = append(messages, map[string]string{"role": "system", "content": system})
	}
	for _, m := range msgs {
		messages = append(messages, map[string]string{"role": m.Role, "content": m.Content})
	}
	return c.completeOpenAI(ctx, messages, maxTokens, false)
}

func (c *httpClient) CompleteJSON(ctx context.Context, system string, msgs []Message, maxTokens int) (string, error) {
	if c.anthropic {
		return c.Complete(ctx, system, msgs, maxTokens)
	}
	messages := make([]map[string]string, 0, len(msgs)+1)
	if system != "" {
		messages = append(messages, map[string]string{"role": "system", "content": system})
	}
	for _, m := range msgs {
		messages = append(messages, map[string]string{"role": m.Role, "content": m.Content})
	}
	return c.completeOpenAI(ctx, messages, maxTokens, true)
}

func (c *httpClient) completeOpenAI(ctx context.Context, messages []map[string]string, maxTokens int, jsonMode bool) (string, error) {
	payload := map[string]any{"model": c.cfg.Model, "messages": messages, "max_completion_tokens": maxTokens}
	if jsonMode {
		payload["response_format"] = map[string]string{"type": "json_object"}
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := c.post(ctx, "/v1/chat/completions", payload, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("openai response has no choices")
	}
	return out.Choices[0].Message.Content, nil
}

func (c *httpClient) post(ctx context.Context, path string, payload, out any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.cfg.BaseURL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.anthropic {
		req.Header.Set("x-api-key", c.cfg.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("LLM HTTP %d: %.500s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode LLM response: %w", err)
	}
	return nil
}
