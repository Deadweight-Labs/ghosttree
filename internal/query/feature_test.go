package query

import "testing"

// Features in der Suchsprache (REQ-612): is:feature, kind:feature und feature:N.
func TestFeatureKindAndStates(t *testing.T) {
	feat := Facts{Kind: KindFeature, State: "active", Number: 3}
	idea := Facts{Kind: KindIdea, State: "open", Number: 3}
	if !match(t, "is:feature", feat) || match(t, "is:feature", idea) {
		t.Error("is:feature picks the kind")
	}
	if !match(t, "kind:features", feat) || !match(t, "kind:feat", feat) {
		t.Error("kind:feature and its aliases")
	}
	if !match(t, "is:active", feat) || match(t, "is:retired", feat) || !match(t, "is:retired", Facts{Kind: KindFeature, State: "retired"}) {
		t.Error("is:active and is:retired are the states")
	}
	// Ein Feature hat keine Klasse offen/erledigt: wie Wissen und Dateien.
	if match(t, "is:open", feat) || match(t, "is:done", feat) || StateClass(KindFeature, "active") != "" {
		t.Error("a feature is neither open nor done")
	}
	if got := mustParse(t, "is:feature").Kinds(); len(got) != 1 || got[0] != KindFeature {
		t.Errorf("Kinds: %v", got)
	}
	if mustParse(t, "is:feature").WantsKind(KindIdea) || !mustParse(t, "is:feature").WantsKind(KindFeature) || mustParse(t, "-is:feature").WantsKind(KindFeature) {
		t.Error("WantsKind follows is:feature")
	}
}

func TestFeatureNumberFindsTheFeatureAndWhatRelatesToIt(t *testing.T) {
	feat := Facts{Kind: KindFeature, Number: 3}
	otherFeat := Facts{Kind: KindFeature, Number: 4}
	affecting := Facts{Kind: KindIdea, Number: 9, Features: []int64{3, 7}}
	unrelated := Facts{Kind: KindIdea, Number: 3}
	req := Facts{Kind: KindRequest, Number: 5, Features: []int64{3}}
	for name, c := range map[string]struct {
		line string
		f    Facts
		want bool
	}{
		"the feature itself":         {"feature:3", feat, true},
		"another feature":            {"feature:3", otherFeat, false},
		"an idea that affects it":    {"feature:3", affecting, true},
		"an issue that relates":      {"feature:3", req, true},
		"an idea with the same no.":  {"feature:3", unrelated, false},
		"alias feat":                 {"feat:3", feat, true},
		"range":                      {"feature:2..4", otherFeat, true},
		"or":                         {"feature:4,7", affecting, true},
		"negated drops the relation": {"-feature:3", affecting, false},
		"negated keeps the others":   {"-feature:3", unrelated, true},
		"with is:idea":               {"is:idea feature:3", affecting, true},
		"with is:idea, not related":  {"is:idea feature:3", unrelated, false},
	} {
		if got := match(t, c.line, c.f); got != c.want {
			t.Errorf("%s: %q = %v, want %v", name, c.line, got, c.want)
		}
	}
	// feature:N schränkt die Arten nicht ein: es trifft Features, Ideen und Issues.
	if got := mustParse(t, "feature:3").Kinds(); got != nil {
		t.Errorf("feature:3 narrows the kinds to %v", got)
	}
	for _, bad := range []string{"feature:", "feature:x", "feature:0"} {
		if _, err := Parse(bad, testNow); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	// Bereiche eines Features sind seine Pfade: area: findet es.
	withAreas := Facts{Kind: KindFeature, Areas: []string{"internal/store", "cmd/ctx/feature.go"}}
	if !match(t, "is:feature area:internal/store/feature.go", withAreas) || !match(t, "area:cmd/ctx", withAreas) || match(t, "area:internal/web", withAreas) {
		t.Error("area: matches the paths of a feature")
	}
	if q := mustParse(t, "is:feature feature:3 area:internal"); mustParse(t, q.String()).String() != q.String() {
		t.Errorf("round trip: %q", q.String())
	}
}
