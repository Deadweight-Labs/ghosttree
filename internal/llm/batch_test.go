package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeBatchAPI implements just enough of the OpenAI batch endpoints to drive
// the client through a full submit-poll-collect cycle.
type fakeBatchAPI struct {
	uploaded    string
	uploadedFor string
	status      string
	requestBody string
	results     string
}

func (f *fakeBatchAPI) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/files", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.uploaded = string(body)
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "multipart/form-data") {
			t.Errorf("upload Content-Type = %q, want multipart/form-data", ct)
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "file-in-1"})
	})
	mux.HandleFunc("/v1/batches", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.requestBody = string(body)
		json.NewEncoder(w).Encode(map[string]any{"id": "batch-1", "status": "validating"})
	})
	mux.HandleFunc("/v1/batches/batch-1", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": "batch-1", "status": f.status, "output_file_id": "file-out-1",
			"request_counts": map[string]int{"total": 2, "completed": 2, "failed": 0}})
	})
	mux.HandleFunc("/v1/files/file-out-1/content", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, f.results)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func batchClient(t *testing.T, url string) BatchClient {
	t.Helper()
	c, err := New(Config{Format: "openai", BaseURL: url, APIKey: "k", Model: "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	bc, ok := c.(BatchClient)
	if !ok {
		t.Fatalf("%T does not implement BatchClient", c)
	}
	return bc
}

func TestSubmitBatchUploadsJSONLAndReturnsID(t *testing.T) {
	fake := &fakeBatchAPI{}
	bc := batchClient(t, fake.server(t).URL)

	id, err := bc.SubmitBatch(context.Background(), []BatchRequest{
		{CustomID: "session-1", System: "extract", User: "transcript one", MaxTokens: 2500},
		{CustomID: "session-2", System: "extract", User: "transcript two", MaxTokens: 2500},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "batch-1" {
		t.Errorf("batch id = %q, want batch-1", id)
	}
	lines := 0
	for _, line := range strings.Split(fake.uploaded, "\n") {
		if !strings.Contains(line, `"custom_id"`) {
			continue
		}
		lines++
		var req struct {
			CustomID string `json:"custom_id"`
			Method   string `json:"method"`
			URL      string `json:"url"`
			Body     struct {
				Model string `json:"model"`
			} `json:"body"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			t.Fatalf("upload line is not valid JSON: %v", err)
		}
		if req.Method != "POST" || req.URL != "/v1/chat/completions" {
			t.Errorf("line targets %s %s, want POST /v1/chat/completions", req.Method, req.URL)
		}
		if req.Body.Model != "test-model" {
			t.Errorf("model = %q, want the configured model", req.Body.Model)
		}
	}
	if lines != 2 {
		t.Errorf("uploaded %d request lines, want 2", lines)
	}
	// A 24-hour window is what buys the 50% discount.
	if !strings.Contains(fake.requestBody, `"completion_window":"24h"`) {
		t.Errorf("batch creation did not request the 24h window: %s", fake.requestBody)
	}
}

func TestBatchStatusReportsProgress(t *testing.T) {
	fake := &fakeBatchAPI{status: "in_progress"}
	bc := batchClient(t, fake.server(t).URL)

	status, err := bc.BatchStatus(context.Background(), "batch-1")
	if err != nil {
		t.Fatal(err)
	}
	if status.Done {
		t.Error("in_progress reported as done")
	}
	if status.Total != 2 || status.Completed != 2 {
		t.Errorf("counts = %+v, want the reported totals", status)
	}
}

// Terminal states other than completed must not read as "still running", or a
// collector waits forever on a batch that already failed.
func TestBatchStatusTreatsFailureAsTerminal(t *testing.T) {
	for _, state := range []string{"failed", "expired", "cancelled"} {
		fake := &fakeBatchAPI{status: state}
		bc := batchClient(t, fake.server(t).URL)
		status, err := bc.BatchStatus(context.Background(), "batch-1")
		if err != nil {
			t.Fatal(err)
		}
		if !status.Done {
			t.Errorf("%s did not report as done", state)
		}
		if status.Failed == "" {
			t.Errorf("%s reported no failure reason", state)
		}
	}
}

func TestCollectBatchReturnsResultsByCustomID(t *testing.T) {
	fake := &fakeBatchAPI{
		status: "completed",
		results: `{"custom_id":"session-1","response":{"status_code":200,"body":{"choices":[{"message":{"content":"{\"items\":[]}"}}],"usage":{"prompt_tokens":1200,"completion_tokens":30}}}}
{"custom_id":"session-2","response":{"status_code":200,"body":{"choices":[{"message":{"content":"{\"items\":[1]}"}}],"usage":{"prompt_tokens":800,"completion_tokens":10}}}}`,
	}
	bc := batchClient(t, fake.server(t).URL)

	results, err := bc.CollectBatch(context.Background(), "batch-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	first := results["session-1"]
	if first.Content != `{"items":[]}` {
		t.Errorf("content = %q", first.Content)
	}
	// usage is the only exact token count available; the local estimate exists
	// solely to decide what to send.
	if first.PromptTokens != 1200 || first.CompletionTokens != 30 {
		t.Errorf("usage = %d/%d, want 1200/30", first.PromptTokens, first.CompletionTokens)
	}
}

// One failed request in a batch must not discard the rest.
func TestCollectBatchKeepsGoodResultsAlongsideFailures(t *testing.T) {
	fake := &fakeBatchAPI{
		status: "completed",
		results: `{"custom_id":"session-1","response":{"status_code":400,"body":{"error":{"message":"context length exceeded"}}}}
{"custom_id":"session-2","response":{"status_code":200,"body":{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":5}}}}`,
	}
	bc := batchClient(t, fake.server(t).URL)

	results, err := bc.CollectBatch(context.Background(), "batch-1")
	if err != nil {
		t.Fatal(err)
	}
	if results["session-2"].Content != "ok" {
		t.Error("a sibling failure discarded a good result")
	}
	if results["session-1"].Error == "" {
		t.Error("failed request reported no error")
	}
}
