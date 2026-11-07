package gitanchor

import (
	"encoding/base64"
	"strings"
	"testing"
)

const full = "0123456789abcdef0123456789abcdef01234567"

func TestEncodeDecodeRoundTrip(t *testing.T) {
	pr := int64(7)
	in := Anchor{Commit: full, Branch: "feat/ümlaut/ä b", MergeBase: strings.ToUpper(full), Dirty: true, PR: &pr, CapturedAt: "x"}
	got := Decode(Encode(in))
	if got.Commit != full || got.Branch != in.Branch || got.MergeBase != full || !got.Dirty {
		t.Fatalf("round trip lost data: %+v", got)
	}
	// Die PR-Nummer ist nicht Sache des Clients, der Zeitpunkt nicht des Headers.
	if got.PR != nil || got.CapturedAt != "" {
		t.Fatalf("pr/captured_at survived the header: %+v", got)
	}
}

func TestEmptyAnchorHasNoHeader(t *testing.T) {
	if v := Encode(Anchor{Branch: "main"}); v != "" {
		t.Fatalf("an anchor without commit must not travel, got %q", v)
	}
	if !Decode("").IsZero() {
		t.Fatal("empty header must decode to the empty anchor")
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	for name, v := range map[string]string{
		"not base64":   "%%%",
		"not json":     enc("hello"),
		"short commit": enc(`{"commit":"abc"}`),
		"non hex":      enc(`{"commit":"zzzzzzzzzzzz"}`),
		"huge":         strings.Repeat("A", maxHeaderBytes+1),
	} {
		if a := Decode(v); !a.IsZero() {
			t.Errorf("%s: want empty anchor, got %+v", name, a)
		}
	}
}

func TestValidPrefix(t *testing.T) {
	for p, want := range map[string]bool{
		"":                      false,
		"abc123":                false,
		"abc1234":               true,
		"ABC1234":               false, // erst normalisieren
		"abc123g":               false,
		full:                    true,
		strings.Repeat("a", 65): false,
	} {
		if got := ValidPrefix(p); got != want {
			t.Errorf("ValidPrefix(%q) = %v, want %v", p, got, want)
		}
	}
	if Normalize(" ABC1234 ") != "abc1234" {
		t.Error("Normalize must lower-case and trim")
	}
	if ShortCommit(full) != full[:12] || ShortCommit("abc1234") != "abc1234" {
		t.Error("ShortCommit")
	}
}
