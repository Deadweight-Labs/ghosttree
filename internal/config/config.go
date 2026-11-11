// Package config holds the client-side settings (server URL, token, machine).
package config

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/Deadweight-Labs/ghosttree/internal/privatefile"
)

type Config struct {
	ServerURL string `json:"server_url"`
	Token     string `json:"token"`
	Machine   string `json:"machine"` // default: os.Hostname()
	// Channel is the update channel, "stable" or "dev"; empty means stable.
	// REQ-610 (several servers): it becomes a per-profile field then, and this
	// top-level value stays the fallback for the default server, so an existing
	// config.json keeps its meaning.
	Channel string `json:"channel,omitempty"`
}

const (
	ChannelStable = "stable"
	ChannelDev    = "dev"
)

// UpdateChannel is the channel with the default applied.
func (c Config) UpdateChannel() string {
	if c.Channel == ChannelDev {
		return ChannelDev
	}
	return ChannelStable
}

// ValidChannel reports whether s names an update channel.
func ValidChannel(s string) bool { return s == ChannelStable || s == ChannelDev }

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
	return privatefile.Write(p, append(b, '\n'))
}
