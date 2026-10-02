package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestServeConfigReadsOIDCFromEnvAndFlags(t *testing.T) {
	t.Setenv(envOIDCIssuer, "https://id.example.test")
	t.Setenv(envOIDCClientID, "from-env")
	t.Setenv(envOIDCClientSecret, "shh")
	t.Setenv(envOIDCRedirectURL, "https://gt.example.test/ui/login/oidc/callback")
	cfg, err := parseServeConfig([]string{"--oidc-client-id=from-flag"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OIDC.Issuer != "https://id.example.test" || cfg.OIDC.ClientID != "from-flag" ||
		cfg.OIDC.ClientSecret != "shh" || !strings.HasSuffix(cfg.OIDC.RedirectURL, "/ui/login/oidc/callback") {
		t.Fatalf("oidc=%+v", cfg.OIDC)
	}
}

func TestServeConfigWithoutOIDCStaysDisabledAndPartialConfigFails(t *testing.T) {
	t.Setenv(envOIDCIssuer, "")
	cfg, err := parseServeConfig(nil, io.Discard)
	if err != nil || cfg.OIDC.Enabled() {
		t.Fatalf("cfg=%+v err=%v", cfg.OIDC, err)
	}
	if _, err := parseServeConfig([]string{"--oidc-issuer=https://id.example.test"}, io.Discard); err == nil {
		t.Fatal("issuer alone accepted")
	}
}

func TestBootstrapCodeFileOnlyOnEmptyInstance(t *testing.T) {
	db := filepath.Join(t.TempDir(), "ghosttree.db")
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var out bytes.Buffer
	if err := prepareBootstrapCode(st, db, &out); err != nil {
		t.Fatal(err)
	}
	path := bootstrapCodePath(db)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("stat=%v err=%v", info, err)
	}
	raw, _ := os.ReadFile(path)
	code := strings.TrimSpace(string(raw))
	if code == "" || strings.Contains(out.String(), code) || !strings.Contains(out.String(), path) {
		t.Fatalf("code=%q output=%q", code, out.String())
	}
	if st.CodeKindFor(code) != store.CodeBootstrap {
		t.Fatal("written code is not a valid bootstrap code")
	}
	// Mit Konto: keine neue Datei, die alte verschwindet.
	if _, err := st.AddPerson("robin"); err != nil {
		t.Fatal(err)
	}
	if err := prepareBootstrapCode(st, db, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stale bootstrap file: %v", err)
	}
}

func TestServeProxyConfiguration(t *testing.T) {
	t.Setenv(envPublicURL, "")
	t.Setenv(envTrustedProxies, "")
	cfg, err := parseServeConfig(nil, io.Discard)
	if err != nil || cfg.PublicURL != "" || cfg.TrustedProxies.Configured() {
		t.Fatalf("default must trust no proxy and set no public url: %+v %v", cfg, err)
	}
	cfg, err = parseServeConfig([]string{"--public-url", "https://gt.example.test/", "--trusted-proxies", "192.0.2.0/24,198.51.100.1"}, io.Discard)
	if err != nil || cfg.PublicURL != "https://gt.example.test" || !cfg.TrustedProxies.Trusts("192.0.2.9:1") || cfg.TrustedProxies.Trusts("203.0.113.1:1") {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
	t.Setenv(envTrustedProxies, "198.51.100.0/24")
	cfg, err = parseServeConfig(nil, io.Discard)
	if err != nil || !cfg.TrustedProxies.Trusts("198.51.100.7:1") {
		t.Fatalf("env not read: %v", err)
	}
	cfg, err = parseServeConfig([]string{"--public-url", "https://gt.example.test:443"}, io.Discard)
	if err != nil || cfg.PublicURL != "https://gt.example.test" {
		t.Fatalf("default port not dropped: %+v %v", cfg, err)
	}
	cfg, err = parseServeConfig([]string{"--public-url", "https://gt.example.test:8443"}, io.Discard)
	if err != nil || cfg.PublicURL != "https://gt.example.test:8443" {
		t.Fatalf("custom port lost: %+v %v", cfg, err)
	}
	for _, bad := range [][]string{
		{"--public-url", "gt.example.test"}, {"--trusted-proxies", "0.0.0.0/0"}, {"--public-url", "ftp://x"}, {"--public-url", "https://x/path"},
		{"--public-url", "https://u:p@x"}, {"--trusted-proxies", "nonsense"},
	} {
		if _, err := parseServeConfig(bad, io.Discard); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}
