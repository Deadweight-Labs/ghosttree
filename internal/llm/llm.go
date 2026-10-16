// Package llm provides the small model-provider surface used by migration.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Format  string `json:"format"`
	BaseURL string `json:"base_url"`
	APIKey  string `json:"-"`
	// APIKeyFile is a path, absolute or relative to the configuration file.
	APIKeyFile string `json:"api_key_file"`
	// Credential names an entry in $CREDENTIALS_DIRECTORY, which is how a
	// systemd unit receives a secret without it living in the state directory.
	Credential string `json:"credential"`
	Model      string `json:"model"`
	// Prices (USD per million tokens) for a model without a built-in list
	// price; zero fields fall back to DefaultPrice and the cache ratios.
	InputUSDPerMTok      float64 `json:"input_usd_per_mtok"`
	OutputUSDPerMTok     float64 `json:"output_usd_per_mtok"`
	CacheReadUSDPerMTok  float64 `json:"cache_read_usd_per_mtok"`
	CacheWriteUSDPerMTok float64 `json:"cache_write_usd_per_mtok"`
}

// Price is what the configured model costs; false when it is not known, in
// which case dollar figures cannot be computed.
func (c Config) Price() (Price, bool) {
	if c.InputUSDPerMTok > 0 && c.OutputUSDPerMTok > 0 {
		return Price{InUSD: c.InputUSDPerMTok, OutUSD: c.OutputUSDPerMTok, CacheReadUSD: c.CacheReadUSDPerMTok, CacheWriteUSD: c.CacheWriteUSDPerMTok}, true
	}
	return DefaultPrice(c.Model)
}

type Message struct{ Role, Content string }

type Client interface {
	Complete(ctx context.Context, system string, msgs []Message, maxTokens int) (string, error)
}

type JSONClient interface {
	CompleteJSON(ctx context.Context, system string, msgs []Message, maxTokens int) (string, error)
}

func New(cfg Config) (Client, error) {
	switch cfg.Format {
	case "openai":
		return &httpClient{cfg: cfg, anthropic: false}, nil
	case "anthropic":
		return &httpClient{cfg: cfg, anthropic: true}, nil
	default:
		return nil, fmt.Errorf("unknown LLM wire format %q", cfg.Format)
	}
}

// LoadConfig reads the provider configuration. The path defaults to the
// user's config directory, which is where an interactive run finds it, but a
// systemd unit has no home: DynamicUser services get no usable
// os.UserConfigDir, so GHOSTTREE_LLM_CONFIG names the file directly.
func LoadConfig() (Config, error) {
	path := os.Getenv("GHOSTTREE_LLM_CONFIG")
	if path == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return Config{}, err
		}
		path = filepath.Join(dir, "ghosttree", "llm.json")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, err
	}
	key, err := loadAPIKey(cfg, filepath.Dir(path))
	if err != nil {
		return Config{}, fmt.Errorf("read LLM API key: %w", err)
	}
	cfg.APIKey = key
	return cfg, nil
}

// loadAPIKey prefers a systemd credential, which keeps the secret out of both
// the unit file and the state directory, and falls back to a file next to the
// configuration for interactive use.
//
// Outside a unit there is no $CREDENTIALS_DIRECTORY, and an operator running
// the same command by hand needs the same configuration to work. The fallback
// is only taken when the file is named explicitly: guessing a path is how the
// wrong key gets used without anyone noticing.
func loadAPIKey(cfg Config, configDir string) (string, error) {
	if cfg.Credential != "" {
		dir := os.Getenv("CREDENTIALS_DIRECTORY")
		if dir == "" && cfg.APIKeyFile == "" {
			return "", fmt.Errorf("credential %q requested but $CREDENTIALS_DIRECTORY is unset and no api_key_file is named", cfg.Credential)
		}
		if dir != "" {
			raw, err := os.ReadFile(filepath.Join(dir, cfg.Credential))
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(string(raw)), nil
		}
	}
	keyPath := cfg.APIKeyFile
	if keyPath == "" {
		keyPath = cfg.Format + "-key"
	}
	if !filepath.IsAbs(keyPath) {
		keyPath = filepath.Join(configDir, keyPath)
	}
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}
