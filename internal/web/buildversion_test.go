package web

import (
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestWithBuildVersionReachesTheApp(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h := newApp(st, WithBuildVersion("0.1.0-dev+b0cd1b9")).(*appHandler)
	if h.app.buildVersion != "0.1.0-dev+b0cd1b9" {
		t.Fatalf("buildVersion = %q", h.app.buildVersion)
	}
}
