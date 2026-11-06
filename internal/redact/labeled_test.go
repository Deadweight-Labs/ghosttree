package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

const mark = "[REDACTED:labeled]"

// Alle Werte hier sind erfunden.
func TestLabeledRedactsTheValueAndKeepsTheLabel(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"de colon", "Passwort: Hx7!kQ2pz", "Passwort: " + mark},
		{"de bare", "Mailkonto Passwort QDzt#fakeWRD%7kTest! und weiter", "Mailkonto Passwort " + mark + " und weiter"},
		{"de ist", "Das Passwort ist Hx7!kQ2pz.", "Das Passwort ist " + mark},
		{"de kennwort", "Kennwort = Gr0ssb4um#7", "Kennwort = " + mark},
		{"de compound", "Mailpasswort: Sommer2026!", "Mailpasswort: " + mark},
		{"de hyphen compound", "WLAN-Passwort: kx9-Tm2-qw7", "WLAN-Passwort: " + mark},
		{"pw equals", "pw=Sommer2026!", "pw=" + mark},
		{"pw colon", "pw: Sommer2026!", "pw: " + mark},
		{"password quoted", `password = "s3cr3t-value"`, `password = "` + mark + `"`},
		{"password single quotes", `password = 's3cr3t-value'`, `password = '` + mark + `'`},
		{"password typographic", "Passwort: „Hx7!kQ2pz“ danach", "Passwort: „" + mark + "“ danach"},
		{"api key", "API key: k7k7k7k7m2m2m2m2", "API key: " + mark},
		{"api_key", "api_key=k7k7k7k7m2m2m2m2", "api_key=" + mark},
		{"apikey json key", `{"apikey": "k7k7k7k7m2m2m2m2"}`, `{"apikey": "` + mark + `"}`},
		{"x-api-key header", "X-API-Key: k7k7k7k7m2m2m2m2", "X-API-Key: " + mark},
		{"client secret", "client secret: Ab3Ab3Ab3Ab3", "client secret: " + mark},
		{"client_secret env", "CLIENT_SECRET=Ab3Ab3Ab3Ab3", "CLIENT_SECRET=" + mark},
		{"aws secret key", "AWS_SECRET_ACCESS_KEY=Zq7/Zq7/Zq7/Zq7/Zq7/Zq7", "AWS_SECRET_ACCESS_KEY=" + mark},
		{"db password env", "export DB_PASSWORD=\"Hx7!kQ2pz\"", "export DB_PASSWORD=\"" + mark + "\""},
		{"token", "token: 5n5n5n5n7p7p7p7p9q9q", "token: " + mark},
		{"access token", "access_token = 5n5n5n5n7p7p7p7p9q9q", "access_token = " + mark},
		{"bearer", "Authorization: Bearer 5n5n5n5n7p7p7p7p9q9q", "Authorization: Bearer " + mark},
		{"passphrase", "passphrase: Tr0ub4dor&3", "passphrase: " + mark},
		{"passwd", "passwd: Tr0ub4dor&3", "passwd: " + mark},
		{"pwd label", "pwd: Tr0ub4dor&3", "pwd: " + mark},
		{"pass", "pass: Tr0ub4dor&3", "pass: " + mark},
		{"zugangsdaten", "Zugangsdaten: Tr0ub4dor&3", "Zugangsdaten: " + mark},
		{"credentials", "credentials = Tr0ub4dor&3", "credentials = " + mark},
		{"arrow", "Passwort -> Hx7!kQ2pz", "Passwort -> " + mark},
		{"fat arrow", "password => Hx7!kQ2pz", "password => " + mark},
		{"colon equals", "password := \"Hx7!kQ2pz\"", "password := \"" + mark + "\""},
		{"markdown bold label", "**Passwort:** Hx7!kQ2pz", "**Passwort:** " + mark},
		{"markdown bold label 2", "**Passwort**: Hx7!kQ2pz", "**Passwort**: " + mark},
		{"backticks", "Passwort: `Hx7!kQ2pz`", "Passwort: `" + mark + "`"},
		{"camelCase label", "dbPassword: Hx7!kQ2pz", "dbPassword: " + mark},
		{"cli flag equals", "mysql --password=Hx7!kQ2pz -u root", "mysql --password=" + mark + " -u root"},
		{"cli flag space", "mysql --password Hx7!kQ2pz -u root", "mysql --password " + mark + " -u root"},
		{"sql", "CREATE USER app WITH PASSWORD 'secret123';", "CREATE USER app WITH PASSWORD '" + mark + "';"},
		{"digit only 12", "password: 123456789012", "password: " + mark},
		{"lower only 12", "password: correcthorsebatterystaple", "password: " + mark},
		{"pin", "PIN: 4711", "PIN: " + mark},
		{"pin ist", "Die PIN ist 482913", "Die PIN ist " + mark},
		{"docker env, lower case words", "docker run -e POSTGRES_PASSWORD=mysecretpassword postgres", "docker run -e POSTGRES_PASSWORD=" + mark + " postgres"},
		{"quoted lower case words", `password = "correcthorsebatterystaple"`, `password = "` + mark + `"`},
		{"bare with digits", "Passwort Sommer2026", "Passwort " + mark},
		{"bare in a sentence", "ich habe das Passwort Hx7!kQ2pz geändert", "ich habe das Passwort " + mark + " geändert"},
		{"go literal", `const dbPassword = "s3cr3t-value"`, `const dbPassword = "` + mark + `"`},
		{"struct literal", `Password: "a1a1a1a1a1a1a1a1a1a1b2b2b2b2b2b2b2b2b2b2",`, `Password: "` + mark + `",`},
		{"yaml", "db:\n  password: Hx7!kQ2pz\n  host: localhost", "db:\n  password: " + mark + "\n  host: localhost"},
		{"env file", "DB_HOST=localhost\nDB_PASSWORD=Hx7!kQ2pz\nDB_PORT=5432", "DB_HOST=localhost\nDB_PASSWORD=" + mark + "\nDB_PORT=5432"},
		{"json text", `{"password": "Hx7!kQ2pz", "user": "app"}`, `{"password": "` + mark + `", "user": "app"}`},
		{"curl user", `curl --user admin --password Hx7!kQ2pz https://example.com`, `curl --user admin --password ` + mark + ` https://example.com`},
		{"two in a line", "pw=Sommer2026! und token=5n5n5n5n7p7p7p7p9q9q", "pw=" + mark + " und token=" + mark},
		{"line start", "ok\nPasswort: Hx7!kQ2pz\nnext", "ok\nPasswort: " + mark + "\nnext"},
		{"upper label", "PASSWORD=Hx7!kQ2pz", "PASSWORD=" + mark},
		{"label after bullet", "- Passwort: Hx7!kQ2pz", "- Passwort: " + mark},
		{"paren", "(Passwort: Hx7!kQ2pz)", "(Passwort: " + mark},
		{"already redacted", "Passwort: " + mark, "Passwort: " + mark},
		{"prefix token wins", "token: ghp_" + strings.Repeat("A", 36), "token: [REDACTED:github]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Redact(c.in); got != c.want {
				t.Errorf("Redact(%q)\n got  %q\n want %q", c.in, got, c.want)
			}
			if again := Redact(Redact(c.in)); again != Redact(c.in) {
				t.Errorf("not idempotent: %q -> %q", Redact(c.in), again)
			}
		})
	}
}

func TestLabeledLeavesProseAndCodeAlone(t *testing.T) {
	for _, in := range []string{
		// Prosa
		"Passwort zurücksetzen",
		"Passwort zurücksetzen!",
		"Das Passwort ist zu kurz.",
		"das Passwort ist zurückgesetzt worden",
		"Passwort vergessen?",
		"Passwort: mindestens 8 Zeichen",
		"Passwort: Pflichtfeld",
		"Your password has been changed.",
		"Password must be at least 12 characters long",
		"Password: required",
		"Enter your password to continue",
		"Reset your password",
		"password reset email sent",
		"Neues Passwort setzen",
		"Bitte Passwort eingeben",
		"This is a secret santa gift",
		"nothing secret. ghost tree eyJustText",
		"The token expires in 3600 seconds",
		"token usage: 1234",
		"Token: Authentication",
		"Kennwort: Zurücksetzen",
		"API key is stored in the settings",
		"Bearer of bad news",
		"the pin is loose",
		"pin 12 is connected to the relay",
		"PIN: 12",
		"passwords are hashed with bcrypt",
		"Passwortmanager: Bitwarden",
		"compass: north",
		"bypass: yes",
		"Tokenizer: BPE",
		"secret_santa: true",
		"token_count: 1234567",
		"max_tokens: 4096",
		"password_hash: abcdef",
		"password-file path",
		// Platzhalter
		"password: ${DB_PASSWORD}",
		"password: $DB_PASSWORD",
		"password: $1",
		"PASSWORD=%PASSWORD%",
		"password: <password>",
		"--password <pw>",
		"--password=<pw>",
		"password: {{ .Values.password }}",
		"password: \"{{ vault_db_password }}\"",
		"password: ********",
		"password: ••••••••",
		"password = \"xxxxxxxx\"",
		"password: XXXX-XXXX-XXXX",
		"password: ......",
		"password: changeme",
		"password: changeme123",
		"password: your-password-here",
		"password: <your password>",
		"password: [hidden]",
		"token: YOUR_API_TOKEN",
		"token: example-token",
		"secret: placeholder",
		"password: null",
		"password: undefined",
		"GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}",
		"token: \"${{ secrets.GITHUB_TOKEN }}\"",
		// Code
		`password := r.FormValue("password")`,
		`pw := r.PostFormValue("pw")`,
		`password = os.Getenv("DB_PASSWORD")`,
		`pass := os.Getenv("DB_PASS")`,
		`password: process.env.DB_PASSWORD`,
		`const token = localStorage.getItem('token')`,
		`token = req.headers.authorization`,
		`password = request.POST['password']`,
		`password = request.form["password"]`,
		`password = os.environ["DB_PASSWORD"]`,
		`password = getpass.getpass()`,
		`password = input("Password: ")`,
		`secret = secrets.token_hex(32)`,
		`token = jwt.sign(payload, secret)`,
		`if (!password) {`,
		`password: Optional[str] = None`,
		`password: str`,
		`password: string,`,
		`Password string ` + "`json:\"password\"`",
		`type Config struct { Token string }`,
		`func (s *Store) VerifyToken(token string) error {`,
		`token, err := parse(raw)`,
		`password = config.password`,
		`password: PasswordField`,
		`passwordInput: document.getElementById('x')`,
		`if token == nil {`,
		`api_key = os.environ.get("API_KEY")`,
		`curl -H "Authorization: Bearer $TOKEN" https://api.example.com`,
		`curl -u user:$PASSWORD https://example.com`,
		`echo $PASSWORD | docker login --password-stdin`,
		`ssh-keygen -N "" -f ~/.ssh/id_ed25519`,
		// Pfade und URLs
		"cat /etc/passwd",
		"/etc/passwd:root:x:0:0:root:/root:/bin/bash",
		"grep root /etc/passwd",
		"passwd: /etc/passwd",
		"secrets/token",
		"/var/run/secrets/kubernetes.io/serviceaccount/token",
		"token: /var/run/secrets/token",
		"password_file: ./secrets/db-password.txt",
		"secret: ../secrets/db.json",
		"token: C:\\Users\\x\\token.txt",
		"https://example.com/reset-password",
		"https://example.com/password/reset",
		"token: https://example.com/oauth/token",
		"PWD=/home/user/project",
		"pwd: /home/user/project",
		"$ pwd\n/home/user/project",
		"export OLDPWD=/home/user",
		"secret: usr/local/share/secrets",
		// Zuweisung eines Namens: mit Leerzeichen ist es Code, ein langes Wort kein Geheimnis
		`c.migrationtoken = migrationtoken`,
		`c.userworkloadssecret = userworkloadssecret`,
		`password = correcthorsebatterystaple`,
		// Go-, SQL-, HTML-, CSS- und Shell-Zeilen aus echten Quelltexten
		`req.Header.Set("Authorization", "Bearer "+token)`,
		`req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)`,
		`cfg := config.Config{ServerURL: *serverURL, Token: *token, Machine: *machine}`,
		`const coordBroadcastToken = "@agents"`,
		`func (c *oidcClient) mergeUserInfo(ctx context.Context, token *oauth2.Token, subject string) {`,
		`AuthToken *AuthToken ` + "`json:\"authToken,omitempty\"`",
		`Token string ` + "`json:\"token\" api:\"required\"`",
		`//sys LogonUser(username *uint16, password *uint16, flags uint32) (err error)`,
		`SIOCSLIFTOKEN = -0x7f879679`,
		`pass := &analysis.Pass{`,
		`Credential: &syscall.Credential{Uid: uint32(uid)},`,
		`t.Fatalf("%s: token %d:\n\tinput: %s", tt.Where, i, tt.json)`,
		`curl -H "Authorization: Bearer $BOB_TOKEN" localhost:8080/get`,
		`NL80211_ATTR_SAE_PASSWORD = C.NL80211_ATTR_SAE_PASSWORD`,
		`// Password: User's password`,
		`fmt.Errorf("no machine identity; pass machine=<name>")`,
		`(pass --agent)`,
		`VALUES('feature','MACHINE SECRET TITLE','','open',?,'box')`,
		`LoadCredential=llm-key:/etc/ghosttree/llm-key`,
		`{{lbl "token" .Status}}"></span>`,
		`tok=$(sed -n 's/.*"token": *"\([^"]*\)".*/\1/p' config.json)`,
		`.login-token summary::-webkit-details-marker { display: none; }`,
		`// Ohne das Token (Datei weg, anderer Rechner) gilt derselbe Name`,
		"with your token (`/api/orgs`, `/api/projects`).",
		`Vertraulich: Staging-Token rotieren?`,
		// Test- und Statuszeilen, Bezeichner, Zahlen
		"--- PASS: TestRuntimeWriterCrashRestartMatrix (0.02s)",
		"PASS: TestRuntimeWriterCrashRestartMatrix",
		"PASS",
		"ok  \tgithub.com/example/pkg\t0.012s",
		"pass: TestFoo",
		"secret: kubernetes-secret-name",
		"secret: db-password-v2",
		"token: user_token_v2",
		"token: access_token",
		"secretName: my-app-secrets",
		"password: 2026-10-19",
		"token: 192.168.100.200",
		"token: 1.2.3.4",
		"password: 12345",
		"pin: 13",
		"password: abcdef",
		"Secret: Hello",
		"Password: Sommer",
	} {
		if got := Redact(in); got != in {
			t.Errorf("false positive:\n in   %q\n got  %q", in, got)
		}
		if got := FindSecrets(in); len(got) != 0 {
			t.Errorf("FindSecrets reports %v for %q", got, in)
		}
	}
}

func TestLabeledRejectsWhenTheQuotationHoldsMoreThanOneToken(t *testing.T) {
	for _, in := range []string{
		`password = "correct horse battery staple 9"`,
		`password: "My passphrase has spaces 123!"`,
	} {
		if got := Redact(in); got != in {
			t.Errorf("redacted %q -> %q", in, got)
		}
	}
}

func TestURLCredentials(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"git clone https://deploy:Hx7!kQ2pz@git.example.com/org/repo.git", "git clone https://deploy:[REDACTED:url]@git.example.com/org/repo.git"},
		{"postgres://app:s3cr3t-value@db.internal:5432/app", "postgres://app:[REDACTED:url]@db.internal:5432/app"},
		{"redis://:Hx7!kQ2pz@cache:6379", "redis://:[REDACTED:url]@cache:6379"},
		{"url=\"amqps://u:p4ss%2Fw0rd@mq.example.com\"", "url=\"amqps://u:[REDACTED:url]@mq.example.com\""},
		// keine Zugangsdaten oder nur Platzhalter
		{"https://example.com/path", "https://example.com/path"},
		{"https://user@example.com/path", "https://user@example.com/path"},
		{"ssh://git@github.com/org/repo.git", "ssh://git@github.com/org/repo.git"},
		{"http://localhost:8080/health", "http://localhost:8080/health"},
		{"https://example.com:8443/x@y", "https://example.com:8443/x@y"},
		{"postgres://user:password@localhost:5432/db", "postgres://user:password@localhost:5432/db"},
		{"postgres://user:${DB_PASS}@localhost/db", "postgres://user:${DB_PASS}@localhost/db"},
		{"mysql://user:<password>@localhost/db", "mysql://user:<password>@localhost/db"},
		{"mysql://root:****@localhost/db", "mysql://root:****@localhost/db"},
		{"mailto:someone@example.com", "mailto:someone@example.com"},
		{"git@github.com:org/repo.git", "git@github.com:org/repo.git"},
	} {
		if got := Redact(c.in); got != c.want {
			t.Errorf("Redact(%q)\n got  %q\n want %q", c.in, got, c.want)
		}
	}
	if got := Redact("https://x-access-token:ghs_" + strings.Repeat("a", 36) + "@github.com/o/r"); strings.Contains(got, "ghs_") || !strings.Contains(got, "[REDACTED:github]") {
		t.Errorf("token in URL: %q", got)
	}
}

// jsonLine verpackt einen Text so, wie ein Transkript ihn schreibt.
func jsonLine(t *testing.T, text string, escapeHTML bool) string {
	t.Helper()
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(escapeHTML)
	if err := enc.Encode(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": text}}); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(sb.String(), "\n")
}

func contentOf(t *testing.T, line string) string {
	t.Helper()
	var v struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(line), &v); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, line)
	}
	return v.Message.Content
}

func TestRedactJSONKeepsTheLineValidAndRedactsInsideEscapes(t *testing.T) {
	for _, escapeHTML := range []bool{true, false} {
		for _, c := range []struct{ name, text, want string }{
			{"plain", "Passwort QDzt#fakeWRD%7kTest! danach", "Passwort " + mark + " danach"},
			{"ends the string", "Das Passwort ist Hx7!kQ2pz", "Das Passwort ist " + mark},
			{"ends with a quote", `mein Passwort: Hx7!kQ2pz"`, "mein Passwort: " + mark},
			{"quoted", `mein Passwort: "Hx7!kQ2pz" bitte`, `mein Passwort: "` + mark + `" bitte`},
			{"quote inside", `pw=Hx7"kQ2pz weiter`, "pw=" + mark + " weiter"},
			{"backslash inside", `pw=Hx7\kQ2pz weiter`, "pw=" + mark + " weiter"},
			{"ends at newline", "Passwort: Hx7!kQ2pz\nnächste Zeile", "Passwort: " + mark + "\nnächste Zeile"},
			{"ends at crlf", "Passwort: Hx7!kQ2pz\r\nnext", "Passwort: " + mark + "\r\nnext"},
			{"ends at tab", "Passwort: Hx7!kQ2pz\tnext", "Passwort: " + mark + "\tnext"},
			{"label after newline", "ok\nPasswort: Hx7!kQ2pz", "ok\nPasswort: " + mark},
			{"label after crlf", "ok\r\npw=Hx7!kQ2pz", "ok\r\npw=" + mark},
			{"ampersand and angle", "Passwort: Hx7&kQ2<pz>", "Passwort: " + mark},
			{"umlaut and emoji", "Passwort: Hx7ü😀kQ2", "Passwort: " + mark},
			{"token after newline", "ok\nsk-ant-" + strings.Repeat("A", 30), "ok\n[REDACTED:anthropic]"},
			{"private key", "-----BEGIN RSA PRIVATE KEY-----\nMIIabc\n-----END RSA PRIVATE KEY-----\nrest", "[REDACTED:privatekey]\nrest"},
			{"label with quotes", `"password": "Hx7!kQ2pz"`, `"password": "` + mark + `"`},
			{"url credentials", "clone https://u:Hx7!kQ2pz@host/x now", "clone https://u:[REDACTED:url]@host/x now"},
			{"prose", "Passwort zurücksetzen\n\"quoted\"", "Passwort zurücksetzen\n\"quoted\""},
			{"code", `password := r.FormValue("password")`, `password := r.FormValue("password")`},
		} {
			t.Run(c.name, func(t *testing.T) {
				line := jsonLine(t, c.text, escapeHTML)
				got := RedactJSON(line)
				if !json.Valid([]byte(got)) {
					t.Fatalf("invalid JSON:\n in  %s\n out %s", line, got)
				}
				if content := contentOf(t, got); content != c.want {
					t.Errorf("content\n got  %q\n want %q\n line %s", content, c.want, line)
				}
				if RedactJSON(got) != got {
					t.Errorf("not idempotent: %s", got)
				}
				// The same text, redacted as text, gives the same result.
				if viaText := Redact(c.text); viaText != c.want {
					t.Errorf("Redact(text) = %q, want %q", viaText, c.want)
				}
			})
		}
	}
}

func TestRedactJSONHandlesStructuralKeys(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"string value", `{"type":"tool_use","input":{"password":"Hx7!kQ2pz","user":"app"}}`, `{"type":"tool_use","input":{"password":"` + mark + `","user":"app"}}`},
		{"spaced", `{"password" : "Hx7!kQ2pz"}`, `{"password" : "` + mark + `"}`},
		{"number stays", `{"password":123456789012,"n":1}`, `{"password":123456789012,"n":1}`},
		{"true stays", `{"token":true}`, `{"token":true}`},
		{"null stays", `{"token":null}`, `{"token":null}`},
		{"object stays", `{"token":{"a":"b"}}`, `{"token":{"a":"b"}}`},
		{"label as value", `{"label":"Password","value":"Hx7!kQ2pz"}`, `{"label":"Password","value":"Hx7!kQ2pz"}`},
		{"escaped nested json", `{"cmd":"{\"password\":\"Hx7!kQ2pz\",\"u\":1}"}`, `{"cmd":"{\"password\":\"` + mark + `\",\"u\":1}"}`},
		{"string ends at label", `{"cmd":"echo password=","next":"Hx7!kQ2pz"}`, `{"cmd":"echo password=","next":"Hx7!kQ2pz"}`},
		{"unicode escape before", `{"t":"x\u00a0Passwort: Hx7!kQ2pz"}`, `{"t":"x\u00a0Passwort: ` + mark + `"}`},
		{"unicode escape inside", `{"t":"Passwort: Hx7\u00fckQ2pz!"}`, `{"t":"Passwort: ` + mark + `"}`},
		{"surrogate pair inside", `{"t":"pw=Hx7\ud83d\ude00kQ2pz end"}`, `{"t":"pw=` + mark + ` end"}`},
		{"escaped backslash before", `{"t":"C:\\Users\\x\\password: Hx7!kQ2pz"}`, `{"t":"C:\\Users\\x\\password: Hx7!kQ2pz"}`},
		{"escaped backslash is not an escape", `{"t":"a\\npassword: Hx7!kQ2pz"}`, `{"t":"a\\npassword: ` + mark + `"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := RedactJSON(c.in)
			if got != c.want {
				t.Errorf("RedactJSON\n in   %s\n got  %s\n want %s", c.in, got, c.want)
			}
			if !json.Valid([]byte(got)) {
				t.Errorf("invalid JSON: %s", got)
			}
		})
	}
}

func TestStatsCountKindsAndNeverValues(t *testing.T) {
	in := "Passwort: Hx7!kQ2pz token=5n5n5n5n7p7p7p7p9q9q api_key=k7k7k7k7m2m2m2m2 PIN: 4711 https://u:Hx7!kQ2pz@h/x ghp_" + strings.Repeat("B", 36)
	stats := Stats{}
	out := Apply(in, Text, stats)
	want := Stats{"labeled:password": 1, "labeled:token": 1, "labeled:apikey": 1, "labeled:pin": 1, "url": 1, "github": 1}
	for k, v := range want {
		if stats[k] != v {
			t.Errorf("stats[%q] = %d, want %d (all: %v)", k, stats[k], v, stats)
		}
	}
	for _, secret := range []string{"Hx7!kQ2pz", "5n5n5n5n7p7p7p7p9q9q", "k7k7k7k7m2m2m2m2", "4711", strings.Repeat("B", 36)} {
		if strings.Contains(out, secret) {
			t.Errorf("output still holds %q: %s", secret, out)
		}
	}
	for k := range stats {
		if strings.Contains(k, "Hx7") {
			t.Errorf("a stats key holds a value: %q", k)
		}
	}
}

func TestFindSecretsReportsLabeledAndURLWithoutTheValue(t *testing.T) {
	in := "ok\nPasswort: Hx7!kQ2pz\nclone https://u:Hx7!kQ2pz@h/x\n"
	got := FindSecrets(in)
	if len(got) != 2 || got[0].Label != "url" || got[0].Line != 3 || got[1].Label != "labeled" || got[1].Line != 2 {
		t.Fatalf("matches = %+v", got)
	}
	raw := jsonLine(t, in, true)
	gotRaw := FindSecretsIn(raw, JSON)
	if len(gotRaw) != 2 {
		t.Fatalf("raw matches = %+v", gotRaw)
	}
	if len(FindSecretsIn(RedactJSON(raw), JSON)) != 0 {
		t.Fatal("matches left after redaction")
	}
}
