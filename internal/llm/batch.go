package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// BatchRequest is one unit of work in a batch. CustomID is how the result is
// found again after the 24-hour window, so it has to identify the subject
// without needing any state kept on this side.
type BatchRequest struct {
	CustomID  string
	System    string
	User      string
	MaxTokens int
	JSONMode  bool
}

// BatchResult carries what came back for one request. PromptTokens and
// CompletionTokens are the provider's own count, which is the only exact
// figure available; the local character estimate exists only to decide what
// to send.
type BatchResult struct {
	Content          string
	Error            string
	PromptTokens     int
	CompletionTokens int
	// Truncated means the model ran into the output cap. The content is then a
	// fragment, and a fragment of JSON does not decode — which is
	// indistinguishable from a bad answer unless it is said outright.
	Truncated bool
}

// BatchStatusReport describes a batch in flight. Done covers every terminal
// state, not just success: a collector that treats failure as "still running"
// waits forever.
type BatchStatusReport struct {
	Done      bool
	Total     int
	Completed int
	FailedN   int
	Failed    string
}

// BatchClient is the asynchronous path. It costs half as much as the
// synchronous API in exchange for a completion window of up to 24 hours,
// which is the right trade for a backlog nobody is waiting on.
type BatchClient interface {
	SubmitBatch(ctx context.Context, reqs []BatchRequest) (string, error)
	BatchStatus(ctx context.Context, batchID string) (BatchStatusReport, error)
	CollectBatch(ctx context.Context, batchID string) (map[string]BatchResult, error)
}

func (c *httpClient) SubmitBatch(ctx context.Context, reqs []BatchRequest) (string, error) {
	if c.anthropic {
		return "", fmt.Errorf("batch submission is only implemented for the OpenAI wire format")
	}
	if len(reqs) == 0 {
		return "", fmt.Errorf("batch is empty")
	}
	var jsonl bytes.Buffer
	for _, r := range reqs {
		messages := []map[string]string{}
		if r.System != "" {
			messages = append(messages, map[string]string{"role": "system", "content": r.System})
		}
		messages = append(messages, map[string]string{"role": "user", "content": r.User})
		body := map[string]any{"model": c.cfg.Model, "messages": messages, "max_completion_tokens": r.MaxTokens}
		if r.JSONMode {
			body["response_format"] = map[string]string{"type": "json_object"}
		}
		line, err := json.Marshal(map[string]any{
			"custom_id": r.CustomID, "method": "POST", "url": "/v1/chat/completions", "body": body})
		if err != nil {
			return "", err
		}
		jsonl.Write(line)
		jsonl.WriteByte('\n')
	}

	fileID, err := c.uploadBatchFile(ctx, jsonl.Bytes())
	if err != nil {
		return "", err
	}
	var created struct {
		ID string `json:"id"`
	}
	// The 24-hour completion window is what the discount is paid for.
	if err := c.post(ctx, "/v1/batches", map[string]any{
		"input_file_id": fileID, "endpoint": "/v1/chat/completions", "completion_window": "24h",
	}, &created); err != nil {
		return "", err
	}
	if created.ID == "" {
		return "", fmt.Errorf("batch creation returned no id")
	}
	return created.ID, nil
}

func (c *httpClient) uploadBatchFile(ctx context.Context, jsonl []byte) (string, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("purpose", "batch"); err != nil {
		return "", err
	}
	part, err := w.CreateFormFile("file", "batch.jsonl")
	if err != nil {
		return "", err
	}
	if _, err := part.Write(jsonl); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.cfg.BaseURL, "/")+"/v1/files", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	raw, err := doAndRead(req, 10*time.Minute)
	if err != nil {
		return "", err
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decode file upload: %w", err)
	}
	if out.ID == "" {
		return "", fmt.Errorf("file upload returned no id")
	}
	return out.ID, nil
}

func (c *httpClient) BatchStatus(ctx context.Context, batchID string) (BatchStatusReport, error) {
	batch, err := c.fetchBatch(ctx, batchID)
	if err != nil {
		return BatchStatusReport{}, err
	}
	report := BatchStatusReport{
		Total:     batch.RequestCounts.Total,
		Completed: batch.RequestCounts.Completed,
		FailedN:   batch.RequestCounts.Failed,
	}
	switch batch.Status {
	case "completed":
		report.Done = true
	case "failed", "expired", "cancelled":
		report.Done = true
		report.Failed = batch.Status
	}
	return report, nil
}

func (c *httpClient) CollectBatch(ctx context.Context, batchID string) (map[string]BatchResult, error) {
	batch, err := c.fetchBatch(ctx, batchID)
	if err != nil {
		return nil, err
	}
	if batch.OutputFileID == "" {
		return nil, fmt.Errorf("batch %s has no output file (status %s)", batchID, batch.Status)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(c.cfg.BaseURL, "/")+"/v1/files/"+batch.OutputFileID+"/content", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	raw, err := doAndRead(req, 10*time.Minute)
	if err != nil {
		return nil, err
	}

	out := map[string]BatchResult{}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	// Result lines carry a whole model response, which can exceed the default
	// 64 KiB scanner buffer.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry struct {
			CustomID string `json:"custom_id"`
			Error    any    `json:"error"`
			Response struct {
				StatusCode int `json:"status_code"`
				Body       struct {
					Choices []struct {
						Message struct {
							Content string `json:"content"`
						} `json:"message"`
						FinishReason string `json:"finish_reason"`
					} `json:"choices"`
					Usage struct {
						PromptTokens     int `json:"prompt_tokens"`
						CompletionTokens int `json:"completion_tokens"`
					} `json:"usage"`
					Error struct {
						Message string `json:"message"`
					} `json:"error"`
				} `json:"body"`
			} `json:"response"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, fmt.Errorf("decode batch result line: %w", err)
		}
		// One failed request must not discard its siblings, so failures are
		// recorded per entry rather than returned as an error for the batch.
		result := BatchResult{
			PromptTokens:     entry.Response.Body.Usage.PromptTokens,
			CompletionTokens: entry.Response.Body.Usage.CompletionTokens,
		}
		switch {
		case entry.Response.StatusCode < 200 || entry.Response.StatusCode > 299:
			result.Error = entry.Response.Body.Error.Message
			if result.Error == "" {
				result.Error = fmt.Sprintf("HTTP %d", entry.Response.StatusCode)
			}
		case len(entry.Response.Body.Choices) == 0:
			result.Error = "response has no choices"
		default:
			result.Content = entry.Response.Body.Choices[0].Message.Content
			result.Truncated = entry.Response.Body.Choices[0].FinishReason == "length"
		}
		out[entry.CustomID] = result
	}
	return out, scanner.Err()
}

type batchEnvelope struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	OutputFileID  string `json:"output_file_id"`
	RequestCounts struct {
		Total     int `json:"total"`
		Completed int `json:"completed"`
		Failed    int `json:"failed"`
	} `json:"request_counts"`
}

func (c *httpClient) fetchBatch(ctx context.Context, batchID string) (batchEnvelope, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(c.cfg.BaseURL, "/")+"/v1/batches/"+batchID, nil)
	if err != nil {
		return batchEnvelope{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	raw, err := doAndRead(req, 2*time.Minute)
	if err != nil {
		return batchEnvelope{}, err
	}
	var out batchEnvelope
	if err := json.Unmarshal(raw, &out); err != nil {
		return batchEnvelope{}, fmt.Errorf("decode batch: %w", err)
	}
	return out, nil
}

func doAndRead(req *http.Request, timeout time.Duration) ([]byte, error) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// Batch output files hold one response per session and can be large.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("LLM HTTP %d: %.500s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}
