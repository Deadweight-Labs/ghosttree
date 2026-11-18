package installscript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The sample units are what people copy. The ones that run code and talk to the
// network on their own keep the directives that take away privileges, kernel
// access and namespaces; a unit that loses one fails here.
func TestSampleUnitsKeepTheirHardening(t *testing.T) {
	common := []string{"NoNewPrivileges=yes", "PrivateDevices=yes", "CapabilityBoundingSet=", "ProtectKernelTunables=yes",
		"ProtectKernelModules=yes", "ProtectKernelLogs=yes", "ProtectControlGroups=yes", "ProtectClock=yes", "ProtectHostname=yes",
		"RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX", "RestrictNamespaces=yes", "RestrictRealtime=yes", "LockPersonality=yes",
		"MemoryDenyWriteExecute=yes", "SystemCallArchitectures=native", "SystemCallFilter=@system-service"}
	jobs := append([]string{"ProtectSystem=strict", "ProtectHome=yes", "PrivateTmp=yes", "RestrictSUIDSGID=yes"}, common...)
	for unit, want := range map[string][]string{
		"ghosttree.service":               append([]string{"UMask=0077"}, common...),
		"ghosttree-explain.service":       jobs,
		"ghosttree-distill.service":       jobs,
		"ghosttree-garden-update.service": append([]string{"ReadWritePaths=/var/lib/ghosttree/garden/repo"}, jobs...),
		"ghosttree-watch.service": {"NoNewPrivileges=yes", "RestrictSUIDSGID=yes", "RestrictNamespaces=yes", "RestrictRealtime=yes",
			"LockPersonality=yes", "MemoryDenyWriteExecute=yes", "SystemCallArchitectures=native", "RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX"},
	} {
		b, err := os.ReadFile(filepath.Join("../../deploy", unit))
		if err != nil {
			t.Fatal(err)
		}
		lines := map[string]bool{}
		for _, l := range strings.Split(string(b), "\n") {
			lines[strings.TrimSpace(l)] = true
		}
		for _, w := range want {
			if !lines[w] {
				t.Errorf("%s lacks %s", unit, w)
			}
		}
	}
}

// The update of the gardener's checkout runs as root: it may write the checkout
// and nothing else.
func TestGardenUpdateUnitMayWriteTheCheckoutOnly(t *testing.T) {
	b, err := os.ReadFile("../../deploy/ghosttree-garden-update.service")
	if err != nil {
		t.Fatal(err)
	}
	var rw []string
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "ReadWritePaths="); ok {
			rw = append(rw, v)
		}
	}
	if len(rw) != 1 || rw[0] != "/var/lib/ghosttree/garden/repo" {
		t.Fatalf("ReadWritePaths = %v", rw)
	}
}
