package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func oidcEnv(t *testing.T) {
	t.Helper()
	t.Setenv(envOIDCIssuer, "https://id.example.test")
	t.Setenv(envOIDCClientID, "id")
	t.Setenv(envOIDCRedirectURL, "https://gt.example.test/ui/login/oidc/callback")
	t.Setenv(envOIDCClientSecret, "")
	t.Setenv(envOIDCClientSecretFile, "")
}

// The client secret can come from a file (systemd LoadCredential hands one over),
// so it need not stand in an environment file or in /proc/<pid>/environ.
func TestServeConfigReadsTheOIDCClientSecretFromAFile(t *testing.T) {
	oidcEnv(t)
	path := filepath.Join(t.TempDir(), "oidc-secret")
	if err := os.WriteFile(path, []byte("from-file\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envOIDCClientSecretFile, path)
	cfg, err := parseServeConfig(nil, io.Discard)
	if err != nil || cfg.OIDC.ClientSecret != "from-file" {
		t.Fatalf("secret %q err %v", cfg.OIDC.ClientSecret, err)
	}
}

func TestOIDCClientSecretFileProblemsStopTheServer(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for name, c := range map[string]struct {
		file, env, want string
	}{
		"both given":     {file: write("a", "x\n", 0o400), env: "y", want: "only one"},
		"missing":        {file: filepath.Join(dir, "none"), want: "oidc"},
		"empty":          {file: write("b", "\n", 0o400), want: "empty"},
		"world readable": {file: write("c", "x\n", 0o644), want: "readable by other users"},
		"directory":      {file: dir, want: "regular file"},
		"huge":           {file: write("d", strings.Repeat("x", 5000), 0o400), want: "too large"},
		"several lines":  {file: write("e", "a\nb\n", 0o400), want: "one line"},
	} {
		t.Run(name, func(t *testing.T) {
			oidcEnv(t)
			t.Setenv(envOIDCClientSecretFile, c.file)
			t.Setenv(envOIDCClientSecret, c.env)
			if _, err := parseServeConfig(nil, io.Discard); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}
