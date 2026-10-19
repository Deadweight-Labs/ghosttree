package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Usage is the provider's own token count for one call, the only exact figure
// there is. Complete returns text alone, so spend that goes through it cannot
// be metered.
//
// InputTokens counts only what the provider did not serve from its prompt
// cache. Cache reads and writes are reported separately and billed at their own
// prices, so a cost taken from InputTokens alone is too low behind a caching
// proxy.
type Usage struct{ InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens int }

// UsageClient is a Client that also reports what a call consumed.
type UsageClient interface {
	CompleteWithUsage(ctx context.Context, system string, msgs []Message, maxTokens int) (string, Usage, error)
}

// CompleteWithUsage is the Anthropic Messages call with the usage block kept.
func (c *httpClient) CompleteWithUsage(ctx context.Context, system string, msgs []Message, maxTokens int) (string, Usage, error) {
	if !c.anthropic {
		return "", Usage{}, fmt.Errorf("token metering needs the anthropic wire format")
	}
	return c.completeAnthropicUsage(ctx, system, msgs, maxTokens)
}

// GardenConfig is the opt-in for the gardener. It is deliberately separate
// from the distiller's provider: the gardener is paid from the API credits of
// one console organisation, and an OpenAI key left over from the distiller
// must never be picked up for it by accident.
//
// Prices are the embedded Config's InputUSDPerMTok and friends, read from the
// "garden" entry. Cache prices are optional: left out they are derived from the
// input price (reads a tenth, writes 1.25 times).
type GardenConfig struct {
	Config
}

// DefaultGardenModel and its prices (USD per million tokens, synchronous).
const (
	DefaultGardenModel   = "claude-sonnet-5-5"
	defaultGardenBaseURL = "https://api.anthropic.com"
)

// ErrGardenDisabled means no key was configured. It is the normal state, not
// a failure: the gardener is an option and stays off without one.
var ErrGardenDisabled = errors.New("gardener is off: no API key configured (set GHOSTTREE_GARDEN_KEY or a \"garden\" entry with api_key_file/credential in llm.json)")

// resolveHost looks a hostname up for checkBaseURL. It is a variable so tests
// can stand in for DNS and /etc/hosts.
var resolveHost = func(host string) ([]net.IP, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		ips = append(ips, a.IP)
	}
	return ips, nil
}

// onlyLoopback reports whether host is, or resolves exclusively to, loopback
// addresses. A failed lookup or any non-loopback answer is false (fail closed).
func onlyLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	ips, err := resolveHost(host)
	if err != nil || len(ips) == 0 {
		return false
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return false
		}
	}
	return true
}

// checkBaseURL keeps the API key off the wire in the clear: https only, with
// plain http allowed for a server on this machine. That is localhost, a
// loopback address, or a hostname that resolves only to loopback addresses
// (a tunnel to a shared proxy that routes by Host header, named in /etc/hosts).
func checkBaseURL(raw string) error { return checkBaseURLFor("gardener", raw) }

func checkBaseURLFor(who, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%s base_url %q is not a URL", who, raw)
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && onlyLoopback(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("%s base_url must be https (http only for hosts that resolve to loopback), got %q", who, u.Scheme+"://"+u.Host)
}

type gardenFile struct {
	Garden *struct {
		BaseURL              string  `json:"base_url"`
		Model                string  `json:"model"`
		APIKeyFile           string  `json:"api_key_file"`
		Credential           string  `json:"credential"`
		InputUSDPerMTok      float64 `json:"input_usd_per_mtok"`
		OutputUSDPerMTok     float64 `json:"output_usd_per_mtok"`
		CacheReadUSDPerMTok  float64 `json:"cache_read_usd_per_mtok"`
		CacheWriteUSDPerMTok float64 `json:"cache_write_usd_per_mtok"`
	} `json:"garden"`
}

// LoadGardenConfig returns ErrGardenDisabled when no gardener key exists. The
// key comes from GHOSTTREE_GARDEN_KEY or from a "garden" entry in llm.json; a
// key file is only read when named explicitly, never guessed.
func LoadGardenConfig() (GardenConfig, error) {
	out := GardenConfig{Config: Config{Format: "anthropic", BaseURL: defaultGardenBaseURL, Model: DefaultGardenModel}}
	path := os.Getenv("GHOSTTREE_LLM_CONFIG")
	if path == "" {
		if dir, err := os.UserConfigDir(); err == nil {
			path = filepath.Join(dir, "ghosttree", "llm.json")
		}
	}
	var file gardenFile
	if path != "" {
		if raw, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(raw, &file); err != nil {
				return out, fmt.Errorf("read %s: %w", path, err)
			}
		}
	}
	if g := file.Garden; g != nil {
		if g.BaseURL != "" {
			out.BaseURL = g.BaseURL
		}
		if g.Model != "" {
			out.Model = g.Model
		}
		out.APIKeyFile, out.Credential = g.APIKeyFile, g.Credential
		out.InputUSDPerMTok, out.OutputUSDPerMTok = g.InputUSDPerMTok, g.OutputUSDPerMTok
		out.CacheReadUSDPerMTok, out.CacheWriteUSDPerMTok = g.CacheReadUSDPerMTok, g.CacheWriteUSDPerMTok
	}
	if m := os.Getenv("GHOSTTREE_GARDEN_MODEL"); m != "" {
		out.Model = m
	}
	if err := checkBaseURL(out.BaseURL); err != nil {
		return out, err
	}
	if out.InputUSDPerMTok == 0 && out.OutputUSDPerMTok == 0 {
		if p, ok := DefaultPrice(out.Model); ok {
			out.InputUSDPerMTok, out.OutputUSDPerMTok = p.InUSD, p.OutUSD
		}
	}
	if key := strings.TrimSpace(os.Getenv("GHOSTTREE_GARDEN_KEY")); key != "" {
		out.APIKey = key
		return out, nil
	}
	if out.APIKeyFile == "" && out.Credential == "" {
		return out, ErrGardenDisabled
	}
	key, err := loadAPIKey(out.Config, filepath.Dir(path))
	if err != nil {
		return out, fmt.Errorf("read gardener API key: %w", err)
	}
	if key == "" {
		return out, ErrGardenDisabled
	}
	out.APIKey = key
	return out, nil
}
