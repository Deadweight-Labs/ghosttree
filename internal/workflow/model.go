package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// ModelAccess is how a block reaches a model: the address of the model proxy
// and the token of its run. The proxy resolves the model (an alias such as
// "klein", "connection/model" or a model name), puts in the key and books the
// call on the run.
type ModelAccess struct {
	BaseURL string
	Token   string
}

// Resolved is what a model name stands for in this run.
type Resolved struct {
	Format     string `json:"format"`
	Connection string `json:"connection"`
	Model      string `json:"model"`
}

// Resolve asks the proxy what model stands for.
func (m *ModelAccess) Resolve(ctx context.Context, model string) (Resolved, error) {
	if m == nil {
		return Resolved{}, errors.New("this run has no model access")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(m.BaseURL, "/")+"/ghosttree/v1/resolve?model="+url.QueryEscape(model), nil)
	if err != nil {
		return Resolved{}, err
	}
	req.Header.Set("Authorization", "Bearer "+m.Token)
	u, _ := url.Parse(m.BaseURL)
	resp, err := llm.NewHTTPClient(u, 30*time.Second).Do(req)
	if err != nil {
		return Resolved{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return Resolved{}, fmt.Errorf("model %s: %d %s", model, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var r Resolved
	if err := json.Unmarshal(raw, &r); err != nil {
		return Resolved{}, err
	}
	return r, nil
}

// Client is a client for model in the format of its connection, talking to
// the proxy with the run token.
func (m *ModelAccess) Client(ctx context.Context, model string) (llm.Client, Resolved, error) {
	r, err := m.Resolve(ctx, model)
	if err != nil {
		return nil, r, err
	}
	c, err := llm.New(llm.Config{Format: r.Format, BaseURL: m.BaseURL, APIKey: m.Token, Model: model})
	return c, r, err
}

// AskName is the built-in test block of stage E1: one model call through the
// proxy. The event's data may name the model ("model"); the default is the
// alias "klein".
const AskName = "ghosttree/ask"

// Ask is the block behind AskName.
var Ask = BlockFunc(func(ctx context.Context, in Input) (store.WorkflowRunOutcome, error) {
	model := in.Event.Data["model"]
	if model == "" {
		model = "klein"
	}
	event, _ := json.MarshalIndent(in.Event, "", "  ")
	prompt := "Describe this ghosttree event in one short sentence.\n\n" + string(event)
	client, _, err := in.AI.Client(ctx, model)
	if err != nil {
		return store.WorkflowRunOutcome{}, err
	}
	answer, err := client.Complete(ctx, "You summarise events of a software project.", []llm.Message{{Role: "user", Content: prompt}}, 200)
	if err != nil {
		return store.WorkflowRunOutcome{Inputs: prompt}, err
	}
	return store.WorkflowRunOutcome{State: store.RunDone, Result: strings.TrimSpace(answer), Inputs: prompt, Output: answer}, nil
})
