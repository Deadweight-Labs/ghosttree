package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Config is how to reach the embedding server.
type Config struct {
	// URL is the base URL of the server, for example http://127.0.0.1:8475. The
	// client calls POST {URL}/v1/embeddings and GET {URL}/health.
	URL string
	// Model is sent as "model" and stored with every vector. Empty means
	// DefaultModel.
	Model string
	// HTTPClient overrides the default client, for tests.
	HTTPClient *http.Client
}

// Client talks to an OpenAI-compatible embeddings endpoint, such as the
// llama-server of llama.cpp. It is safe for concurrent use.
type Client struct {
	base  string
	model string
	hc    *http.Client
}

// ParseURL checks the configured base URL and returns it without a trailing
// slash or a trailing /v1/embeddings.
func ParseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", fmt.Errorf("embedding server URL must be http(s)://host[:port], got %q", raw)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("embedding server URL must not carry credentials, a query or a fragment")
	}
	p := strings.TrimRight(u.Path, "/")
	p = strings.TrimSuffix(p, "/v1/embeddings")
	return u.Scheme + "://" + u.Host + p, nil
}

// Loopback reports whether a configured URL points at this machine. The texts
// of knowledge entries go to the server, so a remote one deserves a warning.
func Loopback(base string) bool {
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// New returns a client for the server in cfg.
func New(cfg Config) (*Client, error) {
	base, err := ParseURL(cfg.URL)
	if err != nil {
		return nil, err
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = DefaultModel
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Transport: &http.Transport{
			Proxy:               nil, // the server is local; never route its texts through a proxy
			MaxIdleConnsPerHost: 2,
			IdleConnTimeout:     time.Minute,
		}}
	}
	return &Client{base: base, model: model, hc: hc}, nil
}

// URL is the normalised base URL.
func (c *Client) URL() string { return c.base }

// Model is the model id this client asks for.
func (c *Client) Model() string { return c.model }

// PrefixVersion is the version of the text formats in this package.
func (c *Client) PrefixVersion() string { return PrefixVersion }

// StatusError is an HTTP answer other than 200.
type StatusError struct {
	Code int
	// Detail is a short, sanitised piece of the answer. It never contains the
	// request, which the server does not echo.
	Detail string
}

func (e *StatusError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("embedding server answered %d", e.Code)
	}
	return fmt.Sprintf("embedding server answered %d: %s", e.Code, e.Detail)
}

// Permanent says that the server understood the request and refuses this very
// text, so repeating it will not help (a document larger than its context, say):
// 400, 413 and 422. Everything else is about the server or its configuration
// and may pass: 5xx, a timeout, a refused connection, but also 401, 403, 404 and
// 429. A wrong URL answering 404 must not make the job write off every entry.
func (e *StatusError) Permanent() bool {
	switch e.Code {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

// Permanent reports whether err is a refusal that a retry cannot cure.
func Permanent(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Permanent()
}

type embeddingsRequest struct {
	Input []string `json:"input"`
	Model string   `json:"model"`
}

type embeddingsResponse struct {
	Data []struct {
		Index     *int      `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

const maxResponseBytes = 16 << 20

// Query embeds one search query and returns a unit vector.
func (c *Client) Query(ctx context.Context, q string) ([]float32, error) {
	out, err := c.embed(ctx, []string{QueryText(q)})
	if err != nil {
		return nil, err
	}
	return out[0], nil
}

// Documents embeds finished document texts (see DocumentText) in one request
// and returns unit vectors in the order of the input.
func (c *Client) Documents(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultBatchTimeout)
		defer cancel()
	}
	return c.embed(ctx, texts)
}

func (c *Client) embed(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(embeddingsRequest{Input: texts, Model: c.model})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, unwrapURLError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{Code: resp.StatusCode, Detail: errorDetail(resp.Body)}
	}
	var out embeddingsResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&out); err != nil {
		return nil, fmt.Errorf("embedding server answer is not valid: %w", unwrapURLError(err))
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("embedding server returned %d vectors for %d texts", len(out.Data), len(texts))
	}
	// The endpoint may reorder; "index" says which text a vector belongs to.
	sort.SliceStable(out.Data, func(a, b int) bool {
		ia, ib := out.Data[a].Index, out.Data[b].Index
		return ia != nil && ib != nil && *ia < *ib
	})
	vecs := make([][]float32, len(texts))
	dim := 0
	for i, d := range out.Data {
		if d.Index != nil && *d.Index != i {
			return nil, errors.New("embedding server returned duplicate or missing indexes")
		}
		if dim == 0 {
			dim = len(d.Embedding)
		}
		if len(d.Embedding) != dim {
			return nil, errors.New("embedding server returned vectors of different lengths")
		}
		if err := Normalize(d.Embedding); err != nil {
			return nil, fmt.Errorf("embedding server returned an unusable vector (%d values)", len(d.Embedding))
		}
		vecs[i] = d.Embedding
	}
	return vecs, nil
}

// Health asks the server whether its model is loaded and ready.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return unwrapURLError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &StatusError{Code: resp.StatusCode, Detail: errorDetail(resp.Body)}
	}
	return nil
}

// unwrapURLError drops the URL from a transport error; it carries nothing
// useful and keeps log lines short.
func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// errorDetail returns the message of an error answer, cut short and without
// control characters.
func errorDetail(r io.Reader) string {
	raw, _ := io.ReadAll(io.LimitReader(r, 4096))
	var shaped struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	msg := strings.TrimSpace(string(raw))
	if json.Unmarshal(raw, &shaped) == nil && shaped.Error.Message != "" {
		msg = shaped.Error.Message
	}
	msg = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, msg)
	return truncRunes(strings.Join(strings.Fields(msg), " "), 200)
}
