package llm

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func explainEnv(t *testing.T, llmJSON string) string {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "llm.json")
	if llmJSON != "" {
		if err := os.WriteFile(cfg, []byte(llmJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GHOSTTREE_LLM_CONFIG", cfg)
	t.Setenv("GHOSTTREE_EXPLAIN_KEY", "")
	return dir
}

func TestExplainConfigNeverFallsBackToTheOtherProviders(t *testing.T) {
	dir := explainEnv(t, `{"format":"anthropic","base_url":"http://allgemein:18317","model":"m","api_key_file":"anthropic-key",
		"garden":{"base_url":"http://allgemein:18317","api_key_file":"garden-key"}}`)
	os.WriteFile(filepath.Join(dir, "anthropic-key"), []byte("proxy\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "garden-key"), []byte("garden\n"), 0o600)
	cfg, err := LoadExplainConfig()
	if !errors.Is(err, ErrExplainDisabled) || cfg.APIKey != "" {
		t.Fatalf("key %q err %v: the proxy or garden key was used", cfg.APIKey, err)
	}
	if cfg.BaseURL != "https://api.anthropic.com" {
		t.Errorf("base url %q", cfg.BaseURL)
	}
}

func TestExplainConfigReadsItsOwnEntryWithDefaults(t *testing.T) {
	dir := explainEnv(t, `{"explain":{"api_key_file":"ek"}}`)
	os.WriteFile(filepath.Join(dir, "ek"), []byte("sk-test\n"), 0o600)
	cfg, err := LoadExplainConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "sk-test" || cfg.Model != "claude-haiku-5-5" || cfg.Format != "anthropic" || cfg.BaseURL != "https://api.anthropic.com" {
		t.Errorf("%+v", cfg.Config)
	}
	if p, ok := cfg.Price(); !ok || p.InUSD != 0.10 {
		t.Errorf("price %+v %v", p, ok)
	}
}

func TestExplainConfigWithoutAKeyFileSendsNothingAndSaysWhy(t *testing.T) {
	explainEnv(t, `{"explain":{"api_key_file":"/nonexistent/anthropic-api-key"}}`)
	cfg, err := LoadExplainConfig()
	if !errors.Is(err, ErrExplainDisabled) || cfg.APIKey != "" || !strings.Contains(err.Error(), "does not exist yet") {
		t.Fatalf("missing file: %q %v", cfg.APIKey, err)
	}
	dir := explainEnv(t, `{"explain":{"api_key_file":"ek"}}`)
	os.WriteFile(filepath.Join(dir, "ek"), []byte(" \n"), 0o600)
	if _, err := LoadExplainConfig(); !errors.Is(err, ErrExplainDisabled) {
		t.Errorf("empty file: %v", err)
	}
	explainEnv(t, "")
	if _, err := LoadExplainConfig(); !errors.Is(err, ErrExplainDisabled) {
		t.Errorf("no entry: %v", err)
	}
}

func TestExplainConfigKeyFromEnvironmentAndBadSettings(t *testing.T) {
	explainEnv(t, "")
	t.Setenv("GHOSTTREE_EXPLAIN_KEY", " sk-env ")
	if cfg, err := LoadExplainConfig(); err != nil || cfg.APIKey != "sk-env" {
		t.Errorf("env key: %q %v", cfg.APIKey, err)
	}
	explainEnv(t, `{"explain":{"format":"openai","api_key_file":"k"}}`)
	if _, err := LoadExplainConfig(); err == nil || errors.Is(err, ErrExplainDisabled) {
		t.Errorf("openai format: %v", err)
	}
	explainEnv(t, `{"explain":{"base_url":"http://example.com","api_key_file":"k"}}`)
	if _, err := LoadExplainConfig(); err == nil || !strings.Contains(err.Error(), "explain base_url must be https") {
		t.Errorf("plain http to a remote host: %v", err)
	}
	explainEnv(t, `{"explain":`)
	if _, err := LoadExplainConfig(); err == nil {
		t.Error("broken json accepted")
	}
}
