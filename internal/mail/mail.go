// Package mail sends the few plain-text mails of the server: sign-in codes,
// confirmation of an address, password reset.
//
// There are two ways out. SMTP (net/smtp, with STARTTLS or implicit TLS) is for
// a real instance. The file mailbox writes every mail as an .eml file into a
// directory; it is for tests and for local instances without a mail server.
// A mail carries no remote resource: plain text only, no images, no tracking.
package mail

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Message is one outgoing mail. Text is plain text with \n line ends.
type Message struct {
	To, Subject, Text string
}

// Sender delivers a message. Mode names the way for the start-up line and tests.
type Sender interface {
	Send(Message) error
	Mode() string
}

// Security of the SMTP connection.
const (
	StartTLS = "starttls" // plain connection upgraded with STARTTLS; the default
	TLS      = "tls"      // TLS from the first byte (port 465)
	None     = "none"     // no encryption; only for a relay on the same host
)

// Config is the setup of the mail sending. Empty Host and empty Dir mean no
// mail sending; Host and Dir together are a mistake.
type Config struct {
	Host         string
	Port         int
	Security     string
	From         string
	User         string
	PasswordFile string
	// Dir is the file mailbox.
	Dir string
}

// Enabled says whether any way out is configured.
func (c Config) Enabled() bool { return c.Host != "" || c.Dir != "" }

// Validate checks the setup without touching the network.
func (c Config) Validate() error {
	if c.Host != "" && c.Dir != "" {
		return errors.New("an SMTP host and a mail directory are exclusive: use one")
	}
	if c.Host == "" && c.Dir == "" {
		if c.User != "" || c.PasswordFile != "" || c.From != "" || c.Port != 0 {
			return errors.New("SMTP settings without an SMTP host")
		}
		return nil
	}
	if c.From == "" {
		return errors.New("a sender address is required (smtp from)")
	}
	if _, err := mail.ParseAddress(c.From); err != nil {
		return fmt.Errorf("sender address %q: %w", c.From, err)
	}
	if c.Dir != "" {
		return nil
	}
	switch c.Security {
	case "", StartTLS, TLS, None:
	default:
		return fmt.Errorf("smtp security must be starttls, tls or none, got %q", c.Security)
	}
	if c.Port < 0 || c.Port > 65535 {
		return fmt.Errorf("smtp port %d out of range", c.Port)
	}
	if (c.User == "") != (c.PasswordFile == "") {
		return errors.New("smtp user and password file belong together")
	}
	// A password over an unencrypted connection would cross the network in the
	// clear; only a relay on this very host may be reached that way.
	if c.Security == None && c.User != "" && !loopbackHost(c.Host) {
		return errors.New("smtp credentials over security none are only allowed for a host on this machine")
	}
	return nil
}

func loopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// New builds the sender of a configuration, or nil when none is configured.
func New(c Config) (Sender, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	switch {
	case c.Dir != "":
		if err := os.MkdirAll(c.Dir, 0o700); err != nil {
			return nil, fmt.Errorf("mail directory: %w", err)
		}
		return &fileSender{dir: c.Dir, from: c.From}, nil
	case c.Host != "":
		s := &smtpSender{host: c.Host, port: c.Port, security: c.Security, from: c.From, user: c.User, passwordFile: c.PasswordFile}
		if s.security == "" {
			s.security = StartTLS
		}
		if s.port == 0 {
			s.port = map[string]int{StartTLS: 587, TLS: 465, None: 25}[s.security]
		}
		// The password is read at the start, so a wrong path stops the server
		// there and not at the first mail.
		if s.passwordFile != "" {
			if _, err := s.password(); err != nil {
				return nil, err
			}
		}
		return s, nil
	}
	return nil, nil
}

// safeHeader rejects what could end a header line and start another one.
func safeHeader(v string) error {
	if strings.ContainsAny(v, "\r\n\x00") {
		return errors.New("line break in a mail header")
	}
	return nil
}

// build writes the message as RFC 5322 text with a quoted-printable body.
func build(from string, m Message, at time.Time) ([]byte, error) {
	for _, v := range []string{from, m.To, m.Subject} {
		if err := safeHeader(v); err != nil {
			return nil, err
		}
	}
	to, err := mail.ParseAddress(m.To)
	if err != nil {
		return nil, fmt.Errorf("recipient: %w", err)
	}
	var id [12]byte
	_, _ = rand.Read(id[:])
	domain := "localhost"
	if f, err := mail.ParseAddress(from); err == nil {
		if i := strings.LastIndex(f.Address, "@"); i >= 0 {
			domain = f.Address[i+1:]
		}
	}
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to.Address + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", m.Subject) + "\r\n")
	b.WriteString("Date: " + at.Format(time.RFC1123Z) + "\r\n")
	b.WriteString("Message-ID: <" + hex.EncodeToString(id[:]) + "@" + domain + ">\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: quoted-printable\r\n")
	b.WriteString("Auto-Submitted: auto-generated\r\n\r\n")
	w := quotedprintable.NewWriter(&b)
	if _, err := w.Write([]byte(strings.ReplaceAll(m.Text, "\n", "\r\n"))); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

// ---------------------------------------------------------------- file mailbox

type fileSender struct {
	dir, from string
	mu        sync.Mutex
	n         int
}

func (f *fileSender) Mode() string { return "file mailbox " + f.dir }

// Local says that the mails stay on this machine: nobody but the operator reads
// them, so a link in them may be built from the address the request came in on.
func (f *fileSender) Local() bool { return true }

// recipientName turns an address into something fit for a file name.
func recipientName(addr string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(addr) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '@', r == '.', r == '-', r == '+', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name := strings.TrimLeft(b.String(), ".")
	if len(name) > 80 {
		name = name[:80]
	}
	return name
}

// Send writes <dir>/<time>-<n>-<recipient>.eml. The file is private to the
// user of the server; the name carries the recipient and the time, so a test or
// a person finds the mail of an address by listing the directory.
func (f *fileSender) Send(m Message) error {
	at := time.Now().UTC()
	raw, err := build(f.from, m, at)
	if err != nil {
		return err
	}
	to, _ := mail.ParseAddress(m.To)
	f.mu.Lock()
	f.n++
	n := f.n
	f.mu.Unlock()
	name := fmt.Sprintf("%s-%03d-%s.eml", at.Format("20060102T150405.000Z"), n, recipientName(to.Address))
	return os.WriteFile(filepath.Join(f.dir, name), raw, 0o600)
}

// ---------------------------------------------------------------- SMTP

type smtpSender struct {
	host         string
	port         int
	security     string
	from         string
	user         string
	passwordFile string
}

func (s *smtpSender) Mode() string {
	return fmt.Sprintf("SMTP %s:%d (%s)", s.host, s.port, s.security)
}

func (s *smtpSender) password() (string, error) {
	raw, err := os.ReadFile(s.passwordFile)
	if err != nil {
		return "", fmt.Errorf("smtp password file: %w", err)
	}
	pw := strings.TrimRight(string(raw), "\r\n")
	if pw == "" {
		return "", errors.New("smtp password file is empty")
	}
	return pw, nil
}

// smtpTimeout bounds the whole conversation; a dead relay must not hold a
// request or a goroutine for long.
const smtpTimeout = 30 * time.Second

func (s *smtpSender) Send(m Message) error {
	raw, err := build(s.from, m, time.Now())
	if err != nil {
		return err
	}
	from, _ := mail.ParseAddress(s.from)
	to, _ := mail.ParseAddress(m.To)
	addr := net.JoinHostPort(s.host, fmt.Sprint(s.port))
	tlsCfg := &tls.Config{ServerName: s.host, MinVersion: tls.VersionTLS12}
	dialer := &net.Dialer{Timeout: smtpTimeout}
	var conn net.Conn
	if s.security == TLS {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
	} else {
		conn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(smtpTimeout))
	c, err := smtp.NewClient(conn, s.host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if s.security == StartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("smtp server offers no STARTTLS")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return err
		}
	}
	if s.user != "" {
		pw, err := s.password()
		if err != nil {
			return err
		}
		if err := c.Auth(plainAuth{user: s.user, password: pw}); err != nil {
			return err
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return err
	}
	if err := c.Rcpt(to.Address); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(raw); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// plainAuth is AUTH PLAIN without the check of net/smtp that refuses it on an
// unencrypted connection to anything but localhost: the setup chose "none" on
// purpose, and the error then would say less than the operator already knows.
// Over STARTTLS and TLS the channel is encrypted by the time this runs.
type plainAuth struct{ user, password string }

func (a plainAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.user + "\x00" + a.password), nil
}

func (a plainAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, errors.New("unexpected server challenge")
	}
	return nil, nil
}
