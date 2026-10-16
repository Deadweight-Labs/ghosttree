package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ExplainConfig is the provider of "Kurz erklärt". Like the gardener's it is
// its own entry and never the distiller's top-level provider: explanations are
// paid from the API credits of one console organisation through
// api.anthropic.com, and neither an OpenAI key left over from the distiller nor
// the company proxy that carries the gardener (which would put bot traffic on
// subscription accounts) may be picked up for them by accident.
type ExplainConfig struct {
	Config
}

// Defaults of the "explain" entry.
const (
	DefaultExplainModel   = "claude-haiku-5-5"
	defaultExplainBaseURL = "https://api.anthropic.com"
)

// ErrExplainDisabled means no usable key exists. It is the normal state of a
// server that has not been given one: nothing is sent, and it is reported.
var ErrExplainDisabled = errors.New("explanations are off: no API key (set GHOSTTREE_EXPLAIN_KEY or an \"explain\" entry with api_key_file/credential in llm.json)")

type explainFile struct {
	Explain *struct {
		Format               string  `json:"format"`
		BaseURL              string  `json:"base_url"`
		Model                string  `json:"model"`
		APIKeyFile           string  `json:"api_key_file"`
		Credential           string  `json:"credential"`
		InputUSDPerMTok      float64 `json:"input_usd_per_mtok"`
		OutputUSDPerMTok     float64 `json:"output_usd_per_mtok"`
		CacheReadUSDPerMTok  float64 `json:"cache_read_usd_per_mtok"`
		CacheWriteUSDPerMTok float64 `json:"cache_write_usd_per_mtok"`
	} `json:"explain"`
}

// LoadExplainConfig reads the provider of the explanations. The rules, in
// order:
//
//  1. Only the "explain" entry of llm.json and GHOSTTREE_EXPLAIN_KEY count.
//     The top-level provider and the "garden" entry are never a fallback.
//  2. The key is GHOSTTREE_EXPLAIN_KEY, else the entry's credential or
//     api_key_file (named explicitly, never guessed).
//  3. No entry, no key, a key file that does not exist yet or an empty key is
//     ErrExplainDisabled; the returned config is still filled in so a caller can
//     say which file it waits for. A key file that exists but cannot be read, a
//     format other than anthropic and a base_url that is neither https nor
//     loopback are errors.
//  4. base_url defaults to https://api.anthropic.com, the model to
//     claude-haiku-5-5, the prices to the built-in list price of the model.
func LoadExplainConfig() (ExplainConfig, error) {
	out := ExplainConfig{Config: Config{Format: "anthropic", BaseURL: defaultExplainBaseURL, Model: DefaultExplainModel}}
	path := os.Getenv("GHOSTTREE_LLM_CONFIG")
	if path == "" {
		if dir, err := os.UserConfigDir(); err == nil {
			path = filepath.Join(dir, "ghosttree", "llm.json")
		}
	}
	var file explainFile
	if path != "" {
		if raw, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(raw, &file); err != nil {
				return out, fmt.Errorf("read %s: %w", path, err)
			}
		}
	}
	if e := file.Explain; e != nil {
		if e.Format != "" && e.Format != "anthropic" {
			return out, fmt.Errorf("explain format must be anthropic, got %q", e.Format)
		}
		if e.BaseURL != "" {
			out.BaseURL = e.BaseURL
		}
		if e.Model != "" {
			out.Model = e.Model
		}
		out.APIKeyFile, out.Credential = e.APIKeyFile, e.Credential
		out.InputUSDPerMTok, out.OutputUSDPerMTok = e.InputUSDPerMTok, e.OutputUSDPerMTok
		out.CacheReadUSDPerMTok, out.CacheWriteUSDPerMTok = e.CacheReadUSDPerMTok, e.CacheWriteUSDPerMTok
	}
	if err := checkBaseURLFor("explain", out.BaseURL); err != nil {
		return out, err
	}
	if out.InputUSDPerMTok == 0 && out.OutputUSDPerMTok == 0 {
		if p, ok := DefaultPrice(out.Model); ok {
			out.InputUSDPerMTok, out.OutputUSDPerMTok = p.InUSD, p.OutUSD
		}
	}
	if key := strings.TrimSpace(os.Getenv("GHOSTTREE_EXPLAIN_KEY")); key != "" {
		out.APIKey = key
		return out, nil
	}
	if out.APIKeyFile == "" && out.Credential == "" {
		return out, ErrExplainDisabled
	}
	key, err := loadAPIKey(out.Config, filepath.Dir(path))
	if errors.Is(err, fs.ErrNotExist) {
		return out, fmt.Errorf("%w: the key file %s does not exist yet", ErrExplainDisabled, firstNonEmpty(out.APIKeyFile, out.Credential))
	}
	if err != nil {
		return out, fmt.Errorf("read explain API key: %w", err)
	}
	if key == "" {
		return out, fmt.Errorf("%w: the key file is empty", ErrExplainDisabled)
	}
	out.APIKey = key
	return out, nil
}
