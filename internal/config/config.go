// Package config holds the client-side settings (server URL, token, machine).
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Config struct {
	ServerURL string `json:"server_url"`
	Token     string `json:"token"`
	Machine   string `json:"machine"` // default: os.Hostname()
}

func Path() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "ghosttree", "config.json")
	}
	return filepath.Join(os.Getenv("HOME"), ".config", "ghosttree", "config.json")
}

func Load() (Config, error) {
	var c Config
	b, err := os.ReadFile(Path())
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	if c.Machine == "" {
		c.Machine, _ = os.Hostname()
	}
	return c, nil
}

func Save(c Config) error {
	p := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// The token is a credential: keep the file owner-only.
	return os.WriteFile(p, append(b, '\n'), 0o600)
}
