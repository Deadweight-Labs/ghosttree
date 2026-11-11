package knref

import "testing"

func TestFormat(t *testing.T) {
	if got := Format(4434); got != "KN-4434" {
		t.Fatalf("Format = %q", got)
	}
	if got := List([]int64{12, 15}); got != "KN-12, KN-15" {
		t.Fatalf("List = %q", got)
	}
	if got := List(nil); got != "" {
		t.Fatalf("List(nil) = %q", got)
	}
}

func TestParse(t *testing.T) {
	for in, want := range map[string]int64{"KN-12": 12, "kn-12": 12, " Kn-7 ": 7, "#12": 12, "12": 12} {
		if got, ok := Parse(in); !ok || got != want {
			t.Errorf("Parse(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "KN-", "KN-0", "KN--3", "KN-1x", "#", "#-1", "REQ-5", "pr:5", "KN-+3", "1 2"} {
		if got, ok := Parse(in); ok {
			t.Errorf("Parse(%q) = %d, true; want false", in, got)
		}
	}
}

func TestHas(t *testing.T) {
	if !Has("KN-4") || !Has("kn-4") || Has("#4") || Has("KN-") || Has("4") {
		t.Fatal("Has misreads the prefix")
	}
}
