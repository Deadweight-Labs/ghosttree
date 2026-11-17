package mail

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileMailboxWritesOneFilePerMail(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "box")
	s, err := New(Config{Dir: dir, From: "ghosttree <noreply@example.test>"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s.Mode(), "file mailbox") {
		t.Fatalf("mode %q", s.Mode())
	}
	if err := s.Send(Message{To: "Jonas.K@Example.test", Subject: "Dein Code für ghosttree", Text: "Code: ABCD-2345\nGrüße\n"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Send(Message{To: "jonas.k@example.test", Subject: "Zweite", Text: "x"}); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*-jonas.k@example.test.eml"))
	if len(files) != 2 {
		t.Fatalf("want 2 files named after the recipient, got %v", files)
	}
	info, _ := os.Stat(files[0])
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mail file is %v, want 0600", info.Mode().Perm())
	}
	raw, _ := os.ReadFile(files[0])
	text := string(raw)
	for _, want := range []string{"To: Jonas.K@Example.test", "Subject: =?utf-8?q?Dein_Code_f=C3=BCr_ghosttree?=", "Content-Type: text/plain; charset=utf-8", "Code: ABCD-2345"} {
		if !strings.Contains(text, want) {
			t.Errorf("mail lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(strings.ToLower(text), "http://") && strings.Contains(strings.ToLower(text), "<img") {
		t.Error("mail must not load remote resources")
	}
}

func TestHeaderInjectionIsRefused(t *testing.T) {
	s, _ := New(Config{Dir: t.TempDir(), From: "noreply@example.test"})
	for _, m := range []Message{
		{To: "a@example.test\r\nBcc: evil@example.test", Subject: "x", Text: "x"},
		{To: "a@example.test", Subject: "x\r\nBcc: evil@example.test", Text: "x"},
		{To: "not an address", Subject: "x", Text: "x"},
	} {
		if err := s.Send(m); err == nil {
			t.Errorf("message %+v was sent", m)
		}
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		c    Config
		ok   bool
	}{
		{"nothing", Config{}, true},
		{"file", Config{Dir: "x", From: "a@b.test"}, true},
		{"smtp", Config{Host: "mail.test", From: "a@b.test", Security: TLS}, true},
		{"both", Config{Host: "mail.test", Dir: "x", From: "a@b.test"}, false},
		{"no sender", Config{Host: "mail.test"}, false},
		{"bad security", Config{Host: "mail.test", From: "a@b.test", Security: "ssl"}, false},
		{"user without password", Config{Host: "mail.test", From: "a@b.test", User: "u"}, false},
		{"settings without host", Config{From: "a@b.test"}, false},
		{"clear password to remote", Config{Host: "mail.test", From: "a@b.test", Security: None, User: "u", PasswordFile: "p"}, false},
		{"clear password to loopback", Config{Host: "127.0.0.1", From: "a@b.test", Security: None, User: "u", PasswordFile: "p"}, true},
	}
	for _, c := range cases {
		if err := c.c.Validate(); (err == nil) != c.ok {
			t.Errorf("%s: err=%v, want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestMissingPasswordFileStopsAtStart(t *testing.T) {
	_, err := New(Config{Host: "127.0.0.1", From: "a@b.test", Security: None, User: "u", PasswordFile: filepath.Join(t.TempDir(), "nope")})
	if err == nil {
		t.Fatal("a missing password file must fail at start")
	}
}

// fakeSMTP is the smallest server that takes one mail.
func fakeSMTP(t *testing.T) (port int, got chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got = make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		say := func(s string) { c.Write([]byte(s + "\r\n")) }
		say("220 fake")
		var data strings.Builder
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if inData {
				if line == "." {
					inData = false
					say("250 ok")
					got <- data.String()
					continue
				}
				data.WriteString(line + "\n")
				continue
			}
			switch cmd := strings.ToUpper(strings.Fields(line + " x")[0]); cmd {
			case "EHLO":
				say("250-fake")
				say("250 AUTH PLAIN")
			case "AUTH":
				say("235 ok")
			case "MAIL", "RCPT":
				say("250 ok")
			case "DATA":
				say("354 go")
				inData = true
			case "QUIT":
				say("221 bye")
				return
			default:
				say("250 ok")
			}
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, got
}

func TestSMTPDeliversOverPlainRelay(t *testing.T) {
	port, got := fakeSMTP(t)
	pw := filepath.Join(t.TempDir(), "pw")
	os.WriteFile(pw, []byte("secret\n"), 0o600)
	s, err := New(Config{Host: "127.0.0.1", Port: port, Security: None, From: "noreply@example.test", User: "u", PasswordFile: pw})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.Mode(), "SMTP 127.0.0.1:") {
		t.Fatalf("mode %q", s.Mode())
	}
	if err := s.Send(Message{To: "pia@example.test", Subject: "Hallo", Text: "Code 1234"}); err != nil {
		t.Fatal(err)
	}
	if data := <-got; !strings.Contains(data, "To: pia@example.test") || !strings.Contains(data, "Code 1234") {
		t.Fatalf("server got:\n%s", data)
	}
}
