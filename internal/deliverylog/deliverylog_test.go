package deliverylog

import (
	"strings"
	"testing"
)

func TestHeaderRoundTripAndForgiveness(t *testing.T) {
	parts := []Part{
		{Via: ViaBootstrap, Kind: KindKnowledge, ID: 12, Chars: 380},
		{Via: ViaBootstrap, Kind: KindLedger, Chars: 200},
		{Via: ViaHint, Kind: KindHint, Chars: 90},
	}
	got := ParseHeader(Header(parts))
	if len(got) != len(parts) {
		t.Fatalf("round trip: %v", got)
	}
	for i := range parts {
		if got[i] != parts[i] {
			t.Errorf("part %d: %v != %v", i, got[i], parts[i])
		}
	}
	// A header is an annotation: whatever is wrong with a part, the rest counts.
	mixed := "bootstrap:knowledge:1:10,nonsense,bogus:knowledge:2:5,bootstrap:knowledge:x:5,bootstrap:knowledge:3:-4,relevance:knowledge:4:7"
	got = ParseHeader(mixed)
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 4 {
		t.Errorf("tolerant parse: %v", got)
	}
	if ParseHeader("") != nil || ParseHeader(strings.Repeat("a", 20<<10)) != nil {
		t.Error("empty and oversize headers carry no parts")
	}
}

func TestFitMakesPartsAddUpToTheBudgetCount(t *testing.T) {
	parts := []Part{
		{Via: ViaBootstrap, Kind: KindKnowledge, ID: 1, Chars: 100},
		{Via: ViaBootstrap, Kind: KindKnowledge, ID: 2, Chars: 50},
		{Via: ViaBootstrap, Kind: KindKnowledge, ID: 3, Chars: 70},
	}
	// Text around the parts is the frame.
	got := Fit(parts, 300, ViaBootstrap)
	if Sum(got) != 300 || got[len(got)-1].Kind != KindFrame || got[len(got)-1].Chars != 80 {
		t.Errorf("frame: %v", got)
	}
	// The budget cut the delivery inside the second part: later parts are gone,
	// the cut one keeps what was delivered, and nothing is left over.
	got = Fit(parts, 130, ViaBootstrap)
	if Sum(got) != 130 || len(got) != 2 || got[1].Chars != 30 {
		t.Errorf("cut: %v", got)
	}
	// No parts at all (an older server): one frame part with everything.
	got = Fit(nil, 42, ViaPath)
	if len(got) != 1 || got[0].Kind != KindFrame || got[0].Chars != 42 || got[0].Via != ViaPath {
		t.Errorf("no parts: %v", got)
	}
	if Fit(parts, 0, ViaPath) != nil {
		t.Error("an empty delivery has no parts")
	}
	// A server that claims more than was sent cannot make the sum exceed it.
	if Sum(Fit([]Part{{Via: ViaPath, Kind: KindGhost, Path: "a.go", Chars: 1 << 20}}, 10, ViaPath)) != 10 {
		t.Error("sum must equal the total")
	}
}

func TestPartCleanKeepsPathsOnlyForGhostsAndBoundsThem(t *testing.T) {
	p, ok := Part{Via: ViaPath, Kind: KindGhost, Path: strings.Repeat("ä", 500), Chars: 5}.Clean()
	if !ok || len([]rune(p.Path)) != MaxPathRunes {
		t.Errorf("long path: %v %d", ok, len([]rune(p.Path)))
	}
	p, ok = Part{Via: ViaBootstrap, Kind: KindKnowledge, Path: "secret/path", ID: 1, Chars: 5}.Clean()
	if !ok || p.Path != "" {
		t.Errorf("only a ghost carries a path: %v", p)
	}
	for _, bad := range []Part{
		{Via: "x", Kind: KindGhost, Chars: 1},
		{Via: ViaPath, Kind: "x", Chars: 1},
		{Via: ViaPath, Kind: KindGhost, Path: "a\nb", Chars: 1},
		{Via: ViaPath, Kind: KindGhost, Path: "\xff", Chars: 1},
		{Via: ViaPath, Kind: KindGhost, Chars: -1},
	} {
		if _, ok := bad.Clean(); ok {
			t.Errorf("accepted %v", bad)
		}
	}
}
