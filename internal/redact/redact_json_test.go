package redact

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"
)

// checkJSONAgainstText encodes text the way a transcript does, redacts the
// encoded line and checks three things: the line is still valid JSON, what it
// says is what the plain text would have become, and a second pass changes
// nothing.
func checkJSONAgainstText(t *testing.T, text string, escapeHTML bool) {
	t.Helper()
	line := jsonLine(t, text, escapeHTML)
	got := RedactJSON(line)
	if !json.Valid([]byte(got)) {
		t.Fatalf("invalid JSON for %q\n in  %s\n out %s", text, line, got)
	}
	if content, want := contentOf(t, got), Redact(text); content != want {
		t.Fatalf("JSON and text disagree for %q\n json %q\n text %q", text, content, want)
	}
	if again := RedactJSON(got); again != got {
		t.Fatalf("not idempotent for %q\n 1st %s\n 2nd %s", text, got, again)
	}
	if again := Redact(Redact(text)); again != Redact(text) {
		t.Fatalf("text not idempotent for %q", text)
	}
}

var (
	fragLabels = []string{
		"Passwort", "passwort", "PASSWORD", "password", "Password", "pw", "PW", "pwd", "pass", "PASS", "passphrase",
		"Kennwort", "secret", "Secret", "client secret", "client_secret", "api key", "API_KEY", "apikey", "token",
		"access_token", "Token", "PIN", "pin", "Zugangsdaten", "credentials", "Bearer", "DB_PASSWORD", "dbPassword",
		"Mailpasswort", "WLAN-Passwort", "/etc/passwd", "secrets/token", "--password",
	}
	fragSeps = []string{
		": ", ":", "=", " = ", " ", "  ", " ist ", " is ", " -> ", " => ", ":= ", "\t", ": \"", "=\"", " '", ": `", ": „", " “",
		"\": \"", "\":\"", "' = '", ": **", ":** ", "**: ", "\n", "\r\n", ": \n", "\\", ": \\",
	}
	fragValues = []string{
		"Hx7!kQ2pz", "QDzt#fakeWRD%7kTest!", "Sommer2026!", "s3cr3t-value", "k7k7k7k7m2m2m2m2", "5n5n5n5n7p7p7p7p9q9q",
		"a\"b", "x\\y", "q<w>e&r", "ü😀ä", "correcthorsebatterystaple", "123456789012", "4711", "kx9-Tm2-qw7",
		"zurücksetzen", "Zurücksetzen", "zu", "kurz", "changeme", "${DB_PASSWORD}", "$DB_PASSWORD", "<password>", "********",
		`r.FormValue("password")`, `os.Getenv("X")`, "process.env.DB_PASSWORD", "/etc/passwd", "./secret.txt",
		"https://example.com/reset", "https://u:Hx7!kQ2pz@host/x", "TestRuntimeWriterCrashRestartMatrix", "user_token_v2",
		"ghp_" + "AbCdEfGhIjKlMnOpQrStUvWxYz1234567890", "[REDACTED:labeled]", "",
	}
	fragFiller = []string{
		" ", " ", "\n", "\r\n", "\t", ". ", ", ", "! ", "? ", ") ", "(", "\"", "'", "`", "„", "“", "”", "\\", "/", "<", ">", "&",
		"das ", "und ", "mein ", "Mail", "Konto", "x", "7", "ä", "日本", "😀", " ", " ", "\u0001", "*", "**", "_", "-", ".",
	}
)

func randomText(rng *rand.Rand) string {
	var sb strings.Builder
	for n := 1 + rng.Intn(14); n > 0; n-- {
		switch rng.Intn(5) {
		case 0, 1:
			sb.WriteString(fragLabels[rng.Intn(len(fragLabels))])
			sb.WriteString(fragSeps[rng.Intn(len(fragSeps))])
			sb.WriteString(fragValues[rng.Intn(len(fragValues))])
		case 2:
			sb.WriteString(fragLabels[rng.Intn(len(fragLabels))])
		case 3:
			sb.WriteString(fragValues[rng.Intn(len(fragValues))])
		default:
			sb.WriteString(fragFiller[rng.Intn(len(fragFiller))])
		}
	}
	return sb.String()
}

func TestRedactJSONAgreesWithTextOnRandomInput(t *testing.T) {
	rng := rand.New(rand.NewSource(526))
	for i := 0; i < 30000; i++ {
		checkJSONAgainstText(t, randomText(rng), i%2 == 0)
	}
}

func FuzzRedactJSONAgreesWithText(f *testing.F) {
	rng := rand.New(rand.NewSource(17))
	for i := 0; i < 40; i++ {
		f.Add(randomText(rng))
	}
	f.Fuzz(func(t *testing.T, text string) {
		if !utf8.ValidString(text) {
			t.Skip()
		}
		checkJSONAgainstText(t, text, true)
		checkJSONAgainstText(t, text, false)
	})
}

func TestRedactNeverLeavesAnInvalidLineOnTruncatedJSON(t *testing.T) {
	// A line cut in the middle of an escape sequence or a value must not make
	// the scanner run over the end.
	line := jsonLine(t, "ok\nPasswort: Hx7!kQ2pz  und pw=\"Sommer2026!\" ü😀", true)
	for i := 0; i <= len(line); i++ {
		_ = RedactJSON(line[:i])
		_ = Redact(line[:i])
		_ = FindSecretsIn(line[:i], JSON)
	}
}

// 1 MB of a transcript-like text: prose, code and tool output, no secrets.
func cleanMegabyte() string {
	para := "The collector reads the session log, parses each line and uploads it in batches.\n" +
		"func (s *Store) AppendChunks(sessionID int64, chunks []Chunk) error {\n\tchunks = stripChunks(chunks)\n\treturn s.appendChunkBatches(nil)\n}\n" +
		"ok  \tgithub.com/Deadweight-Labs/ghosttree/internal/store\t12.345s\n" +
		"Der Bildschirm bleibt gesperrt, solange kein Agent mehr arbeitet; das Passwort zurücksetzen dauert nur einen Moment.\n" +
		"{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"Done: the token usage is shown in the status line.\"}]}}\n"
	return strings.Repeat(para, (1<<20)/len(para)+1)[:1<<20]
}

// The same size with a label every few lines.
func denseMegabyte() string {
	para := "line one of some output without much in it\n" +
		"Passwort: Hx7!kQ2pz and some more words\n" +
		"export DB_PASSWORD=\"s3cr3t-value\" # comment\n" +
		"the token is shown below: password := r.FormValue(\"password\")\n" +
		"pass pass pw pin secret token api key ...\n"
	return strings.Repeat(para, (1<<20)/len(para)+1)[:1<<20]
}

func TestMegabyteInputsBehave(t *testing.T) {
	if got := Redact(cleanMegabyte()); got != cleanMegabyte() {
		t.Fatal("the clean megabyte changed")
	}
	dense := denseMegabyte()
	out := Redact(dense)
	if strings.Contains(out, "Hx7!kQ2pz") || strings.Contains(out, "s3cr3t-value") {
		t.Fatal("a secret survived in the dense megabyte")
	}
	if !strings.Contains(out, `password := r.FormValue("password")`) {
		t.Fatal("code was changed")
	}
}

func BenchmarkRedactCleanMegabyte(b *testing.B) {
	input := cleanMegabyte()
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for b.Loop() {
		if len(Redact(input)) != len(input) {
			b.Fatal("changed")
		}
	}
}

func BenchmarkRedactJSONCleanMegabyte(b *testing.B) {
	input := jsonLineB(b, cleanMegabyte())
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for b.Loop() {
		if len(RedactJSON(input)) != len(input) {
			b.Fatal("changed")
		}
	}
}

func BenchmarkRedactDenseMegabyte(b *testing.B) {
	input := denseMegabyte()
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for b.Loop() {
		if len(Redact(input)) == len(input) {
			b.Fatal("nothing redacted")
		}
	}
}

func BenchmarkRedactJSONDenseMegabyte(b *testing.B) {
	input := jsonLineB(b, denseMegabyte())
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for b.Loop() {
		if len(RedactJSON(input)) == len(input) {
			b.Fatal("nothing redacted")
		}
	}
}

func BenchmarkFindSecretsCleanMegabyte(b *testing.B) {
	input := cleanMegabyte()
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for b.Loop() {
		if len(FindSecrets(input)) != 0 {
			b.Fatal("unexpected match")
		}
	}
}

func jsonLineB(b *testing.B, text string) string {
	b.Helper()
	raw, err := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": text}})
	if err != nil {
		b.Fatal(err)
	}
	return string(raw)
}
