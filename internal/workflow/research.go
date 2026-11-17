package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/netguard"
)

// Research on the net (REQ-698). The server ships no search service: an
// operator names one with ResearchURLEnv, an address that answers like
// SearXNG (GET ?q=…&format=json with a list "results" of url, title,
// content). Without it no workflow searches, and the check says so. Pages are
// read as text only, from public addresses only, also after a redirect.

// ResearchURLEnv names the search address of the server.
const ResearchURLEnv = "GHOSTTREE_RESEARCH_URL"

// Limits of research.
const (
	maxResearchQuery = 200
	maxPageBytes     = 2 << 20
	maxPageChars     = 15_000
)

var errResearchOff = errors.New("this server has no search service")

// WebHit is one result of a search.
type WebHit struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

// WebResearch searches with the operator's search service and reads pages.
type WebResearch struct {
	SearchURL string
	// Searcher reaches the search service: the operator's address, https or
	// plain http on this machine (a local SearXNG).
	Searcher *http.Client
	// Fetcher reads pages: public addresses only (netguard.PublicClient).
	Fetcher *http.Client
}

// ResearchFromEnv is the research of the server, or nil when the operator set
// no search address (or one that is neither https nor on this machine).
func ResearchFromEnv() (*WebResearch, error) {
	raw := strings.TrimSpace(os.Getenv(ResearchURLEnv))
	if raw == "" {
		return nil, nil
	}
	return NewWebResearch(raw)
}

// ResearchConfigured says whether the server has a search service.
func ResearchConfigured() bool { return strings.TrimSpace(os.Getenv(ResearchURLEnv)) != "" }

// NewWebResearch checks a search address and makes the research for it.
func NewWebResearch(raw string) (*WebResearch, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("%s: not an address", ResearchURLEnv)
	}
	if err := llm.CheckBaseURL(ResearchURLEnv, u.Scheme+"://"+u.Host); err != nil {
		return nil, err
	}
	searcher := llm.NewHTTPClient(u, 20*time.Second)
	searcher.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &WebResearch{SearchURL: raw, Searcher: searcher, Fetcher: netguard.PublicClient(20*time.Second, 3)}, nil
}

// Search asks the search service; at most max hits with an http(s) address.
func (w *WebResearch) Search(ctx context.Context, query string, max int) ([]WebHit, error) {
	u, err := url.Parse(w.SearchURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("format", "json")
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := w.Searcher.Do(req)
	if err != nil {
		return nil, errors.New("the search service did not answer")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the search service answered %d", resp.StatusCode)
	}
	var out struct {
		Results []WebHit `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return nil, errors.New("the search service did not answer with JSON")
	}
	var hits []WebHit
	for _, h := range out.Results {
		if pu, err := url.Parse(h.URL); err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" || len(h.URL) > 2000 {
			continue
		}
		hits = append(hits, h)
		if len(hits) == max {
			break
		}
	}
	return hits, nil
}

// Fetch reads a page as text: html and plain text only, at most maxPageBytes,
// from a public address only.
func (w *WebResearch) Fetch(ctx context.Context, raw string) (title, text string, err error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "", "", errors.New("not an http(s) address")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "text/html, text/plain;q=0.9")
	resp, err := w.Fetcher.Do(req)
	if err != nil {
		if errors.Is(err, netguard.ErrNotPublic) {
			return "", "", errors.New("the address is not public")
		}
		return "", "", errors.New("the page could not be read")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("the page answered %d", resp.StatusCode)
	}
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if ct != "text/html" && ct != "text/plain" && ct != "application/xhtml+xml" {
		return "", "", fmt.Errorf("the page is %s, not text", ct)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes))
	if err != nil {
		return "", "", errors.New("the page could not be read")
	}
	if ct == "text/plain" {
		return "", cut(strings.TrimSpace(string(body)), maxPageChars), nil
	}
	title, text = htmlText(string(body))
	return title, cut(text, maxPageChars), nil
}

// Researcher is the research step of a block (stage 1 and 2): a search with
// the query the block allows, the hits as text.
func (w *WebResearch) Researcher() Researcher {
	return func(ctx context.Context, query string) (string, error) {
		hits, err := w.Search(ctx, cut(query, maxResearchQuery), 6)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		for _, h := range hits {
			fmt.Fprintf(&b, "%s\n%s\n%s\n\n", oneLine(h.Title), h.URL, cut(oneLine(h.Content), 400))
		}
		return b.String(), nil
	}
}

var (
	reDrop  = regexp.MustCompile(`(?is)<(script|style|noscript|template|svg|head)\b.*?</(script|style|noscript|template|svg|head)\s*>`)
	reTitle = regexp.MustCompile(`(?is)<title\b[^>]*>(.*?)</title\s*>`)
	reBlock = regexp.MustCompile(`(?i)</?(p|div|br|li|ul|ol|h[1-6]|tr|table|section|article|header|footer|pre|blockquote)\b[^>]*>`)
	reTag   = regexp.MustCompile(`(?s)<[^>]*>`)
	reBlank = regexp.MustCompile(`\n[ \t]*\n(?:[ \t]*\n)+`)
	reSpace = regexp.MustCompile(`[ \t\r\f\v]+`)
)

// htmlText makes text of a page: no scripts, styles or markup, entities
// resolved, paragraphs kept as lines.
func htmlText(page string) (title, text string) {
	if m := reTitle.FindStringSubmatch(page); m != nil {
		title = oneLine(html.UnescapeString(reTag.ReplaceAllString(m[1], "")))
	}
	page = reDrop.ReplaceAllString(page, " ")
	page = reBlock.ReplaceAllString(page, "\n")
	page = reTag.ReplaceAllString(page, " ")
	page = html.UnescapeString(page)
	page = reSpace.ReplaceAllString(page, " ")
	lines := strings.Split(page, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	page = reBlank.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	return title, strings.TrimSpace(page)
}
