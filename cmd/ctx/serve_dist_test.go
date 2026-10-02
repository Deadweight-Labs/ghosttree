package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestServeDistDirFlagAndEnv(t *testing.T) {
	dir := t.TempDir()
	if _, err := parseServeConfig([]string{"--dist-dir", dir}, &bytes.Buffer{}); err == nil {
		t.Fatal("dist dir without checksums.txt accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := parseServeConfig([]string{"--dist-dir", dir}, &bytes.Buffer{})
	if err != nil || cfg.DistDir != dir {
		t.Fatalf("flag: %v %q", err, cfg.DistDir)
	}
	t.Setenv("GHOSTTREE_DIST_DIR", dir)
	cfg, err = parseServeConfig(nil, &bytes.Buffer{})
	if err != nil || cfg.DistDir != dir {
		t.Fatalf("env: %v %q", err, cfg.DistDir)
	}
}
