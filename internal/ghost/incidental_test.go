package ghost

import "testing"

func TestIsIncidentalCatchesTheUsualFloodWithoutEatingRealSource(t *testing.T) {
	incidental := []string{
		"internal/store/ghost_test.go",
		"internal/migrate/testdata/session.jsonl",
		"vendor/github.com/x/y.go",
		"web/node_modules/react/index.js",
		"internal/api/service.pb.go",
		"internal/store/schema_gen.go",
		"web/static/app.min.js",
		"go.sum",
	}
	for _, p := range incidental {
		if !IsIncidental(p) {
			t.Errorf("%q sollte als beilaeufig gelten", p)
		}
	}

	// Die Gegenprobe ist die wichtigere Haelfte: ein Muster, das echte
	// Quelldateien mitnimmt, versteckt sie im Baum, und niemand merkt es.
	real := []string{
		"internal/store/ghost.go",
		"cmd/ctx/hook.go",
		"go.mod",
		"README.md",
		"internal/testing.go",
		"internal/vendoring/policy.go",
		"docs/generated-tokens.md",
	}
	for _, p := range real {
		if IsIncidental(p) {
			t.Errorf("%q ist echte Quelle und darf nicht verschwinden", p)
		}
	}
}
