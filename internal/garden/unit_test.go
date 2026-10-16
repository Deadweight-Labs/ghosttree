package garden

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// serviceSettings reads the [Service] section of a unit as key -> values.
func serviceSettings(t *testing.T, file string) map[string][]string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	in := false
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "[") {
			in = l == "[Service]"
			continue
		}
		if !in || l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if ok {
			out[k] = append(out[k], v)
		}
	}
	return out
}

// REQ-491 (3): the unit of the gardener is sandboxed as the request demands.
func TestGardenUnitIsSandboxed(t *testing.T) {
	s := serviceSettings(t, filepath.Join("..", "..", "deploy", "ghosttree-garden.service"))
	one := func(k string) string {
		if len(s[k]) != 1 {
			t.Errorf("%s: %v", k, s[k])
			return ""
		}
		return s[k][0]
	}
	if u := one("User"); u == "" || u == "root" || u == "ghosttree" {
		t.Errorf("the gardener needs a user of its own, got %q", u)
	}
	for k, want := range map[string]string{
		"ProtectHome": "yes", "ProtectSystem": "strict", "PrivateTmp": "yes", "NoNewPrivileges": "yes", "PrivateDevices": "yes",
		"ProtectProc": "invisible", "IPAddressDeny": "any", "CapabilityBoundingSet": "", "RestrictNamespaces": "yes",
	} {
		if got := one(k); got != want {
			t.Errorf("%s=%q, want %q", k, got, want)
		}
	}
	// What is hidden: everything under /var/lib/ghosttree except what is bound
	// back, and the usual places for other people's data.
	if got := one("TemporaryFileSystem"); !strings.HasPrefix(got, "/var/lib/ghosttree:ro") {
		t.Errorf("TemporaryFileSystem=%q", got)
	}
	inacc := strings.Fields(one("InaccessiblePaths"))
	for _, p := range []string{"/home", "/root", "/var/backups", "/var/lib/ghosttree/data", "/var/lib/ghosttree/private"} {
		if !contains(inacc, "-"+p) && !contains(inacc, p) {
			t.Errorf("%s is not inaccessible: %v", p, inacc)
		}
	}
	// The checkout is read-only and nothing under it is writable.
	checkout := "/var/lib/ghosttree/garden/repo"
	if !contains(strings.Fields(one("BindReadOnlyPaths")), checkout) || !contains(strings.Fields(one("ReadOnlyPaths")), checkout) {
		t.Errorf("checkout is not mounted read-only")
	}
	for _, key := range []string{"BindPaths", "ReadWritePaths"} {
		for _, v := range s[key] {
			for _, p := range strings.Fields(v) {
				if p == checkout || strings.HasPrefix(p, checkout+"/") || strings.HasPrefix(checkout, p+"/") || p == "/var/lib/ghosttree" || strings.Contains(p, "/data") || strings.Contains(p, "private") || strings.Contains(p, "backup") {
					t.Errorf("%s makes %s writable", key, p)
				}
			}
		}
	}
	if strings.Contains(strings.Join(s["EnvironmentFile"], " "), "server.env") {
		t.Error("the gardener must not get the server's environment")
	}
}

// The probe takes its settings from the unit file, so it tests what is shipped.
func TestSandboxProbeUsesTheSettingsOfTheUnit(t *testing.T) {
	root := filepath.Join("..", "..", "deploy")
	out, err := exec.Command("bash", filepath.Join(root, "garden-sandbox-probe.sh"), "--print", filepath.Join(root, "ghosttree-garden.service")).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	line := string(out)
	for _, want := range []string{"systemd-run", "User=ghosttree-garden", "ProtectHome=yes", "TemporaryFileSystem=/var/lib/ghosttree:ro", "InaccessiblePaths=", "NoNewPrivileges=yes", "PrivateTmp=yes", "BindReadOnlyPaths="} {
		if !strings.Contains(strings.ReplaceAll(line, "\\ ", " "), want) {
			t.Errorf("probe command lacks %s:\n%s", want, line)
		}
	}
	if strings.Contains(line, "ExecStart") || strings.Contains(line, "EnvironmentFile") {
		t.Errorf("probe takes over settings that are not sandbox:\n%s", line)
	}
	// And it names what must fail.
	b, _ := os.ReadFile(filepath.Join(root, "garden-sandbox-probe.sh"))
	for _, want := range []string{"/var/lib/ghosttree/data", "/var/lib/ghosttree/private", "/var/backups", "/home", "/root", "$garden/repo/probe-write", "expect_ok   \"Checkout lesen\"", "Client-Konfiguration lesen", "PROBE_ALLOW_MISSING"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("probe does not try %s", want)
		}
	}
	// Run without root it refuses, and does not claim anything.
	if os.Getuid() != 0 {
		cmd := exec.Command("bash", filepath.Join(root, "garden-sandbox-probe.sh"))
		if err := cmd.Run(); err == nil {
			t.Error("the probe ran without root")
		}
	}
}
