package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// A reviewer decides whether a claim is supported by its source. The listing
// showed the title and the source quote but never the claim itself, so the one
// judgement the review exists for could not be made from its output.
func TestPendingEntryShowsTheClaimItAsksAbout(t *testing.T) {
	var out bytes.Buffer
	writePendingEntry(&out, client.PendingEntry{
		Knowledge: store.Knowledge{
			ID: 7, Type: "pitfall", Confidence: "quarantined",
			Title: "Contains-tests miss broken grammar",
			Body:  "A contains assertion passes on the broken sentence too; forbid the concrete pattern instead.",
			Scope: scope.Axes{Project: "github.com/x/y"},
		},
		Evidence: []store.Evidence{{SessionID: 314, ChunkSeq: 110, Quote: "Kasusbruch im Repair-Template"}},
	})
	got := out.String()
	if !strings.Contains(got, "forbid the concrete pattern instead") {
		t.Fatalf("listing omits the claim under review:\n%s", got)
	}
	if !strings.Contains(got, "Kasusbruch im Repair-Template") {
		t.Fatalf("listing omits the evidence:\n%s", got)
	}
}
