package redact

import (
	"strings"
	"testing"
)

// Alle Werte sind erfundene Muster; die Teile sind getrennt geschrieben, damit
// kein Scanner den Testquelltext selbst meldet.
func TestRedactMoreProviderTokens(t *testing.T) {
	cases := []struct{ in, label string }{
		{"key " + "AIza" + "SyA-1234567890abcdefghijklmnopqrstu", "google"},
		{"oauth " + "ya29." + "a0AfH6SMBx1234567890abcdefghij", "google"},
		{"secret " + "GOCSPX-" + "abcdefghijklmnopqrstuvwxyz12", "google"},
		{"STRIPE_KEY=" + "sk_" + "live_" + "51Habcdefghijklmnopqrstuv", "stripe"},
		{"rk_" + "test_" + "abcdefghijklmnopqrstuvwx", "stripe"},
		{"whsec_" + "abcdefghijklmnopqrstuvwxyz123456", "stripe"},
		{"glpat-" + "abcdefghij0123456789", "gitlab"},
		{"//registry.npmjs.org/:_authToken=npm_" + "abcdefghijklmnopqrstuvwxyz0123456789", "npm"},
		{"pypi-" + "AgEIcHlwaS5vcmcCJGFiY2RlZmdoaWprbG1ub3BxcnN0dXZ3eHl6MDEyMzQ1Njc4OQ", "pypi"},
		{"HF_TOKEN=hf_" + "abcdefghijklmnopqrstuvwxyzABCDEF", "huggingface"},
		{"SG." + "abcdefghijklmnop12" + ".abcdefghijklmnop34", "sendgrid"},
		{"dop_v1_" + strings.Repeat("a1", 32), "digitalocean"},
		{"bot 123456789:" + "AAH" + "abcdefghijklmnopqrstuvwxyz012345", "telegram"},
		{"AccountKey=" + strings.Repeat("Ab1+", 11) + "==", "azure"},
		{"token gtk_" + strings.Repeat("0123456789abcdef", 4), "ghosttree"},
		{"run gtr_" + strings.Repeat("0123456789abcdef", 4), "ghosttree"},
	}
	for _, c := range cases {
		got := Redact(c.in)
		if !strings.Contains(got, "[REDACTED:"+c.label+"]") {
			t.Errorf("Redact(%q) = %q, want label %q", c.in, got, c.label)
		}
	}
}

// Text, der den Mustern ähnelt, aber kein Schlüssel ist, bleibt unverändert.
func TestRedactMorePatternsDoNotHitOrdinaryText(t *testing.T) {
	for _, in := range []string{
		"task_stripe_live_mode and sk_live ok",
		"the SG. prefix is short: SG.abc.def",
		"npm_config and npm_install are env names",
		"hf_ is the prefix; see hf_hub",
		"AIza is a prefix, not a key",
		"the ya29. prefix",
		"2026-11-18:AAAA timestamps 20261118:12 are fine",
		"glpat- alone",
		"gtk_ is the prefix of a token",
		"commit " + strings.Repeat("0123456789abcdef", 4),
		"AccountKey= is a name",
	} {
		if got := Redact(in); got != in {
			t.Errorf("false positive: %q -> %q", in, got)
		}
	}
}

// Ein Passwort mit / ? # in einer URL wird geschwärzt; ein Host mit Port und
// Pfad nicht.
func TestRedactURLPasswordWithSlash(t *testing.T) {
	in := "postgres://admin:p4ss/w0rd?x#y@db.example.com/app"
	got := Redact(in)
	if strings.Contains(got, "p4ss") || !strings.Contains(got, "[REDACTED:url]") {
		t.Fatalf("password with a slash not redacted: %q", got)
	}
	for _, safe := range []string{
		"https://registry.example.com:443/@scope/pkg",
		"http://localhost:8080/@foo/bar",
		"https://github.com/org/repo/@file",
		"ssh://git@github.com:22/org/repo.git",
		"https://host.example.com:8443?next=/a@b",
	} {
		if got := Redact(safe); got != safe {
			t.Errorf("false positive: %q -> %q", safe, got)
		}
	}
}
