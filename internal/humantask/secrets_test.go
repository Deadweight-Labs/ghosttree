package humantask

import (
	"strings"
	"testing"
)

func TestCheckTextRejectsSecretsAndKeepsPaths(t *testing.T) {
	rep := func(s string, n int) string { return strings.Repeat(s, n) }
	positives := map[string]string{
		"sk prefix":        "key is sk-" + rep("a1", 12),
		"sk-ant":           "sk-ant-" + rep("x9", 8),
		"ghp":              "ghp_" + rep("Ab1", 5),
		"gho":              "gho_" + rep("Ab1", 5),
		"ghs":              "ghs_" + rep("Ab1", 5),
		"github_pat":       "github_pat_" + rep("Ab1", 5),
		"glpat":            "glpat-" + rep("Ab1", 5),
		"slack":            "xoxb-" + rep("12", 8),
		"aws akia":         "AKIA" + rep("AB12", 4),
		"aws asia":         "ASIA" + rep("AB12", 4),
		"google":           "AIza" + rep("Ab1_", 8),
		"sendgrid":         "SG." + rep("Ab1_", 4),
		"stripe pk":        "pk_live_" + rep("Ab1", 4),
		"stripe sk":        "sk_live_" + rep("Ab1", 4),
		"stripe rk":        "rk_live_" + rep("Ab1", 4),
		"jwt":              "eyJ" + rep("a1", 6) + "." + rep("b2", 6) + "." + rep("c3", 6),
		"private key":      "-----BEGIN RSA PRIVATE KEY-----",
		"bearer":           "Authorization header Bearer " + rep("Ab1.", 6),
		"url credentials":  "postgres://admin:hunter22@db.example.invalid/app",
		"assignment":       "password: correcthorsebattery",
		"assignment equal": "API_KEY=" + "abcdefgh" + "1234",
		"entropy":          "value " + rep("aZ3", 12),
	}
	for name, text := range positives {
		if err := CheckText("note", text); err == nil {
			t.Errorf("%s was not detected: %q", name, text)
		} else {
			if err.ErrorCode != "secret_detected" {
				t.Errorf("%s: code %s", name, err.ErrorCode)
			}
			if strings.Contains(err.Message+err.Resolution, text) || strings.Contains(err.Message, "aZ3aZ3") {
				t.Errorf("%s: the error repeats the value", name)
			}
		}
	}
	for name, text := range map[string]string{
		"base64 secret": "key is wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"bare 64 hex":   "hmac " + rep("0f", 32),
	} {
		if err := CheckText("note", text); err == nil {
			t.Errorf("%s was not detected: %q", name, text)
		}
	}
	negatives := map[string]string{
		"git sha":   "commit " + rep("a", 40),
		"sha256":    "digest " + rep("0f", 32),
		"uuid":      "run 123e4567-e89b-12d3-a456-426614174000 done",
		"path":      "~/.config/stripe/live-key.env",
		"vault":     "Agent Vault: stripe/prod",
		"sentence":  "Bitte das Passwort im Tresor anlegen und Bescheid geben.",
		"short":     "token rotation next week",
		"file path": "./db/schema.sql",
		"long path": "/home/main/Projects/ghosttree/internal/server/some_really_long_file_name_here.go",
	}
	for name, text := range negatives {
		if err := CheckText("note", text); err != nil {
			t.Errorf("%s is a false alarm: %q (%s)", name, text, err.Message)
		}
	}
}
