package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Limits of one Message Batch at Anthropic: 100,000 requests or 256 MB,
// whichever is reached first. SplitBatch keeps below them with a margin.
const (
	AnthropicBatchMaxRequests = 100000
	AnthropicBatchMaxBytes    = 200 << 20
)

var anthropicCustomID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// anthropicBatchLine is one entry of the "requests" array of POST
// /v1/messages/batches.
func anthropicBatchLine(model string, r BatchRequest) map[string]any {
	params := map[string]any{
		"model":      model,
		"max_tokens": r.MaxTokens,
		"messages":   []map[string]string{{"role": "user", "content": r.User}},
	}
	if r.System != "" {
		params["system"] = r.System
	}
	return map[string]any{"custom_id": r.CustomID, "params": params}
}

// SplitBatch cuts requests into groups that each fit one batch: at most
// maxRequests entries and maxBytes of JSON. A zero limit means the Anthropic
// limit. A single request that is larger than maxBytes gets a group of its own
// and is refused by SubmitBatch.
func SplitBatch(reqs []BatchRequest, model string, maxRequests, maxBytes int) [][]BatchRequest {
	if maxRequests <= 0 {
		maxRequests = AnthropicBatchMaxRequests
	}
	if maxBytes <= 0 {
		maxBytes = AnthropicBatchMaxBytes
	}
	var out [][]BatchRequest
	var cur []BatchRequest
	size := 0
	for _, r := range reqs {
		line, _ := json.Marshal(anthropicBatchLine(model, r))
		n := len(line) + 1
		if len(cur) > 0 && (len(cur) >= maxRequests || size+n > maxBytes) {
			out = append(out, cur)
			cur, size = nil, 0
		}
		cur = append(cur, r)
		size += n
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

func (c *httpClient) anthropicDo(ctx context.Context, method, path string, body []byte, timeout time.Duration) ([]byte, error) {
	var rd *bytes.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	var req *http.Request
	var err error
	url := strings.TrimRight(c.cfg.BaseURL, "/") + path
	if rd != nil {
		req, err = http.NewRequestWithContext(ctx, method, url, rd)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, url, nil)
	}
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", c.cfg.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return doAndRead(req, timeout)
}

func (c *httpClient) submitAnthropicBatch(ctx context.Context, reqs []BatchRequest) (string, error) {
	if len(reqs) == 0 {
		return "", fmt.Errorf("batch is empty")
	}
	if len(reqs) > AnthropicBatchMaxRequests {
		return "", fmt.Errorf("batch has %d requests, the limit is %d (use SplitBatch)", len(reqs), AnthropicBatchMaxRequests)
	}
	lines := make([]map[string]any, 0, len(reqs))
	for _, r := range reqs {
		if !anthropicCustomID.MatchString(r.CustomID) {
			return "", fmt.Errorf("batch custom_id %q is not 1 to 64 letters, digits, - or _", r.CustomID)
		}
		lines = append(lines, anthropicBatchLine(c.cfg.Model, r))
	}
	body, err := json.Marshal(map[string]any{"requests": lines})
	if err != nil {
		return "", err
	}
	if len(body) > 256<<20 {
		return "", fmt.Errorf("batch is %d bytes, the limit is 256 MB (use SplitBatch)", len(body))
	}
	raw, err := c.anthropicDo(ctx, http.MethodPost, "/v1/messages/batches", body, 10*time.Minute)
	if err != nil {
		return "", err
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		return "", fmt.Errorf("decode batch creation: %w", err)
	}
	if created.ID == "" {
		return "", fmt.Errorf("batch creation returned no id")
	}
	return created.ID, nil
}

type anthropicBatchEnvelope struct {
	ID               string `json:"id"`
	ProcessingStatus string `json:"processing_status"`
	RequestCounts    struct {
		Processing int `json:"processing"`
		Succeeded  int `json:"succeeded"`
		Errored    int `json:"errored"`
		Canceled   int `json:"canceled"`
		Expired    int `json:"expired"`
	} `json:"request_counts"`
}

func (c *httpClient) fetchAnthropicBatch(ctx context.Context, id string) (anthropicBatchEnvelope, error) {
	raw, err := c.anthropicDo(ctx, http.MethodGet, "/v1/messages/batches/"+id, nil, 2*time.Minute)
	if err != nil {
		return anthropicBatchEnvelope{}, err
	}
	var out anthropicBatchEnvelope
	if err := json.Unmarshal(raw, &out); err != nil {
		return anthropicBatchEnvelope{}, fmt.Errorf("decode batch: %w", err)
	}
	return out, nil
}

// anthropicBatchStatus: "ended" is the only terminal status. Requests of an
// ended batch that errored, expired or were canceled are reported per entry
// when collecting, not as a failure of the batch.
func (c *httpClient) anthropicBatchStatus(ctx context.Context, id string) (BatchStatusReport, error) {
	b, err := c.fetchAnthropicBatch(ctx, id)
	if err != nil {
		return BatchStatusReport{}, err
	}
	n := b.RequestCounts
	return BatchStatusReport{
		Done:      b.ProcessingStatus == "ended",
		Total:     n.Processing + n.Succeeded + n.Errored + n.Canceled + n.Expired,
		Completed: n.Succeeded,
		FailedN:   n.Errored + n.Canceled + n.Expired,
	}, nil
}

func (c *httpClient) collectAnthropicBatch(ctx context.Context, id string) (map[string]BatchResult, error) {
	b, err := c.fetchAnthropicBatch(ctx, id)
	if err != nil {
		return nil, err
	}
	if b.ProcessingStatus != "ended" {
		return nil, fmt.Errorf("batch %s is not finished (status %s)", id, b.ProcessingStatus)
	}
	raw, err := c.anthropicDo(ctx, http.MethodGet, "/v1/messages/batches/"+id+"/results", nil, 10*time.Minute)
	if err != nil {
		return nil, err
	}
	out := map[string]BatchResult{}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e struct {
			CustomID string `json:"custom_id"`
			Result   struct {
				Type    string `json:"type"`
				Message struct {
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
				} `json:"message"`
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
					Error   struct {
						Type    string `json:"type"`
						Message string `json:"message"`
					} `json:"error"`
				} `json:"error"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("decode batch result line: %w", err)
		}
		// A failed entry is recorded, never returned as an error: its siblings
		// were billed and their text must not be thrown away with it.
		var res BatchResult
		switch e.Result.Type {
		case "succeeded":
			m := e.Result.Message
			res.PromptTokens, res.CompletionTokens = m.Usage.InputTokens, m.Usage.OutputTokens
			res.CacheReadTokens, res.CacheWriteTokens = m.Usage.CacheReadTokens, m.Usage.CacheWriteTokens
			res.Truncated = m.StopReason == "max_tokens"
			types := make([]string, 0, len(m.Content))
			for _, blk := range m.Content {
				types = append(types, blk.Type)
				if blk.Type == "text" && res.Content == "" {
					res.Content = blk.Text
				}
			}
			if res.Content == "" {
				res.Error = fmt.Sprintf("no text content (stop_reason=%q, content=[%s], output_tokens=%d)", m.StopReason, strings.Join(types, ","), m.Usage.OutputTokens)
			}
		case "errored":
			er := e.Result.Error
			res.Error = firstNonEmpty(er.Error.Message, er.Message, er.Error.Type, er.Type, "errored")
		case "canceled", "expired":
			res.Error = "request " + e.Result.Type
		default:
			res.Error = fmt.Sprintf("unknown result type %q", e.Result.Type)
		}
		out[e.CustomID] = res
	}
	return out, sc.Err()
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
