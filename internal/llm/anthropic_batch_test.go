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

// fakeAnthropicBatches speaks just enough of /v1/messages/batches to drive a
// submit, poll and collect cycle.
type fakeAnthropicBatches struct {
	submitted []byte
	status    string
	results   string
	headers   http.Header
}

func (f *fakeAnthropicBatches) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages/batches", func(w http.ResponseWriter, r *http.Request) {
		f.headers = r.Header.Clone()
		f.submitted, _ = io.ReadAll(r.Body)
		json.NewEncoder(w).Encode(map[string]any{"id": "msgbatch_1", "processing_status": "in_progress"})
	})
	mux.HandleFunc("/v1/messages/batches/msgbatch_1", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"id": "msgbatch_1", "processing_status": f.status,
			"request_counts": map[string]int{"processing": 0, "succeeded": 2, "errored": 1, "canceled": 0, "expired": 1}})
	})
	mux.HandleFunc("/v1/messages/batches/msgbatch_1/results", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, f.results)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func anthropicBatchClient(t *testing.T, url string) BatchClient {
	t.Helper()
	c, err := New(Config{Format: "anthropic", BaseURL: url, APIKey: "k-secret", Model: "claude-haiku-5-5"})
	if err != nil {
		t.Fatal(err)
	}
	return c.(BatchClient)
}

func TestAnthropicSubmitSendsMessageParamsAndAuth(t *testing.T) {
	f := &fakeAnthropicBatches{}
	bc := anthropicBatchClient(t, f.server(t).URL)
	id, err := bc.SubmitBatch(context.Background(), []BatchRequest{
		{CustomID: "knowledge-1", System: "sys", User: "u1", MaxTokens: 4096},
		{CustomID: "request-2", User: "u2", MaxTokens: 4096},
	})
	if err != nil || id != "msgbatch_1" {
		t.Fatalf("id %q err %v", id, err)
	}
	if f.headers.Get("x-api-key") != "k-secret" || f.headers.Get("anthropic-version") == "" {
		t.Errorf("headers: %v", f.headers)
	}
	var body struct {
		Requests []struct {
			CustomID string `json:"custom_id"`
			Params   struct {
				Model     string `json:"model"`
				MaxTokens int    `json:"max_tokens"`
				System    string `json:"system"`
				Messages  []struct{ Role, Content string }
			} `json:"params"`
		} `json:"requests"`
	}
	if err := json.Unmarshal(f.submitted, &body); err != nil || len(body.Requests) != 2 {
		t.Fatalf("body %s: %v", f.submitted, err)
	}
	r := body.Requests[0]
	if r.CustomID != "knowledge-1" || r.Params.Model != "claude-haiku-5-5" || r.Params.MaxTokens != 4096 || r.Params.System != "sys" ||
		len(r.Params.Messages) != 1 || r.Params.Messages[0].Role != "user" || r.Params.Messages[0].Content != "u1" {
		t.Errorf("params: %+v", r)
	}
	if strings.Contains(string(f.submitted), `"system":""`) {
		t.Error("an empty system prompt was sent")
	}
}

func TestAnthropicSubmitRefusesBadIDsAndEmptyBatches(t *testing.T) {
	bc := anthropicBatchClient(t, "http://127.0.0.1:1")
	if _, err := bc.SubmitBatch(context.Background(), nil); err == nil {
		t.Error("empty batch accepted")
	}
	if _, err := bc.SubmitBatch(context.Background(), []BatchRequest{{CustomID: "a b", User: "x", MaxTokens: 1}}); err == nil {
		t.Error("custom_id with a space accepted")
	}
}

func TestAnthropicStatusIsDoneOnlyWhenEnded(t *testing.T) {
	f := &fakeAnthropicBatches{status: "in_progress"}
	bc := anthropicBatchClient(t, f.server(t).URL)
	st, err := bc.BatchStatus(context.Background(), "msgbatch_1")
	if err != nil || st.Done {
		t.Fatalf("in progress: %+v %v", st, err)
	}
	if _, err := bc.CollectBatch(context.Background(), "msgbatch_1"); err == nil {
		t.Error("results of an unfinished batch were collected")
	}
	f.status = "ended"
	st, _ = bc.BatchStatus(context.Background(), "msgbatch_1")
	if !st.Done || st.Total != 4 || st.Completed != 2 || st.FailedN != 2 {
		t.Errorf("ended: %+v", st)
	}
}

func TestAnthropicCollectKeepsSiblingsOfAFailedEntry(t *testing.T) {
	f := &fakeAnthropicBatches{status: "ended"}
	f.results = strings.Join([]string{
		`{"custom_id":"a","result":{"type":"succeeded","message":{"stop_reason":"end_turn","content":[{"type":"thinking","thinking":"x"},{"type":"text","text":"Text A"}],"usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":7,"cache_creation_input_tokens":3}}}}`,
		`{"custom_id":"b","result":{"type":"errored","error":{"type":"error","error":{"type":"invalid_request_error","message":"bad input"}}}}`,
		`{"custom_id":"c","result":{"type":"expired"}}`,
		`{"custom_id":"d","result":{"type":"succeeded","message":{"stop_reason":"max_tokens","content":[{"type":"thinking","thinking":"x"}],"usage":{"input_tokens":50,"output_tokens":4096}}}}`,
		`{"custom_id":"e","result":{"type":"succeeded","message":{"stop_reason":"max_tokens","content":[{"type":"text","text":"Halb"}],"usage":{"input_tokens":5,"output_tokens":9}}}}`,
		"",
	}, "\n")
	bc := anthropicBatchClient(t, f.server(t).URL)
	got, err := bc.CollectBatch(context.Background(), "msgbatch_1")
	if err != nil {
		t.Fatal(err)
	}
	if a := got["a"]; a.Content != "Text A" || a.Error != "" || a.PromptTokens != 100 || a.CompletionTokens != 20 || a.CacheReadTokens != 7 || a.CacheWriteTokens != 3 {
		t.Errorf("a: %+v", a)
	}
	if b := got["b"]; b.Error != "bad input" || b.Content != "" {
		t.Errorf("b: %+v", b)
	}
	if c := got["c"]; c.Error != "request expired" {
		t.Errorf("c: %+v", c)
	}
	// Billed although it has no text: the tokens must survive.
	if d := got["d"]; d.Error == "" || !d.Truncated || d.CompletionTokens != 4096 || !strings.Contains(d.Error, "max_tokens") {
		t.Errorf("d: %+v", d)
	}
	if e := got["e"]; !e.Truncated || e.Content != "Halb" {
		t.Errorf("e: %+v", e)
	}
}

func TestSplitBatchKeepsEveryGroupWithinBothLimits(t *testing.T) {
	var reqs []BatchRequest
	for i := 0; i < 7; i++ {
		reqs = append(reqs, BatchRequest{CustomID: "x", User: strings.Repeat("a", 100), MaxTokens: 1})
	}
	groups := SplitBatch(reqs, "m", 3, 0)
	if len(groups) != 3 || len(groups[0]) != 3 || len(groups[2]) != 1 {
		t.Errorf("by count: %d groups", len(groups))
	}
	line, _ := json.Marshal(anthropicBatchLine("m", reqs[0]))
	groups = SplitBatch(reqs, "m", 0, 2*(len(line)+1)+5)
	if len(groups) != 4 || len(groups[0]) != 2 {
		t.Errorf("by bytes: %d groups", len(groups))
	}
	// One request above the byte limit still gets its own group, never lost.
	groups = SplitBatch(reqs[:2], "m", 0, 10)
	if len(groups) != 2 {
		t.Errorf("oversized: %d groups", len(groups))
	}
	total := 0
	for _, g := range SplitBatch(reqs, "m", 2, 0) {
		total += len(g)
	}
	if total != 7 {
		t.Errorf("requests lost: %d", total)
	}
}
