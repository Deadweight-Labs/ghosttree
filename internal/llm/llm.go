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
	Format     string `json:"format"`
	BaseURL    string `json:"base_url"`
	APIKey     string `json:"-"`
	APIKeyFile string `json:"api_key_file"`
	Model      string `json:"model"`
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

func LoadConfig() (Config, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return Config{}, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "ghosttree", "llm.json"))
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, err
	}
	keyPath := cfg.APIKeyFile
	if keyPath == "" {
		keyPath = filepath.Join(dir, "ghosttree", cfg.Format+"-key")
	}
	if !filepath.IsAbs(keyPath) {
		keyPath = filepath.Join(dir, "ghosttree", keyPath)
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return Config{}, fmt.Errorf("read LLM API key: %w", err)
	}
	cfg.APIKey = strings.TrimSpace(string(key))
	return cfg, nil
}
