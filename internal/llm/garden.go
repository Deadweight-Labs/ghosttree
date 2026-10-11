package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Usage is the provider's own token count for one call, the only exact figure
// there is. Complete returns text alone, so spend that goes through it cannot
// be metered.
type Usage struct{ InputTokens, OutputTokens int }

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
type GardenConfig struct {
	Config
	InputUSDPerMTok  float64
	OutputUSDPerMTok float64
}

// DefaultGardenModel and its prices (USD per million tokens, synchronous).
const (
	DefaultGardenModel   = "claude-sonnet-5-5"
	defaultGardenBaseURL = "https://api.anthropic.com"
	defaultGardenInUSD   = 2.0
	defaultGardenOutUSD  = 10.0
)

// ErrGardenDisabled means no key was configured. It is the normal state, not
// a failure: the gardener is an option and stays off without one.
var ErrGardenDisabled = errors.New("gardener is off: no API key configured (set GHOSTTREE_GARDEN_KEY or a \"garden\" entry with api_key_file/credential in llm.json)")

type gardenFile struct {
	Garden *struct {
		BaseURL          string  `json:"base_url"`
		Model            string  `json:"model"`
		APIKeyFile       string  `json:"api_key_file"`
		Credential       string  `json:"credential"`
		InputUSDPerMTok  float64 `json:"input_usd_per_mtok"`
		OutputUSDPerMTok float64 `json:"output_usd_per_mtok"`
	} `json:"garden"`
}

// LoadGardenConfig returns ErrGardenDisabled when no gardener key exists. The
// key comes from GHOSTTREE_GARDEN_KEY or from a "garden" entry in llm.json; a
// key file is only read when named explicitly, never guessed.
func LoadGardenConfig() (GardenConfig, error) {
	out := GardenConfig{Config: Config{Format: "anthropic", BaseURL: defaultGardenBaseURL, Model: DefaultGardenModel}}
	out.InputUSDPerMTok, out.OutputUSDPerMTok = 0, 0
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
	}
	if m := os.Getenv("GHOSTTREE_GARDEN_MODEL"); m != "" {
		out.Model = m
	}
	if out.InputUSDPerMTok == 0 && out.OutputUSDPerMTok == 0 && out.Model == DefaultGardenModel {
		out.InputUSDPerMTok, out.OutputUSDPerMTok = defaultGardenInUSD, defaultGardenOutUSD
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
