package main

import (
	"io"
	"strings"
	"testing"
)

func TestProxyConfigWarning(t *testing.T) {
	t.Setenv(envPublicURL, "https://gt.example.test")
	t.Setenv(envTrustedProxies, "")
	cfg, err := parseServeConfig(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if w := proxyConfigWarning(cfg); !strings.Contains(w, envTrustedProxies) {
		t.Fatalf("no warning for a public URL without trusted proxies: %q", w)
	}
	t.Setenv(envTrustedProxies, "192.0.2.0/24")
	if cfg, err = parseServeConfig(nil, io.Discard); err != nil || proxyConfigWarning(cfg) != "" {
		t.Fatalf("warning with trusted proxies configured: %v", err)
	}
	t.Setenv(envPublicURL, "")
	t.Setenv(envTrustedProxies, "")
	if cfg, err = parseServeConfig(nil, io.Discard); err != nil || proxyConfigWarning(cfg) != "" {
		t.Fatalf("warning without a public URL: %v", err)
	}
}
