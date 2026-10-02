package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
)

// Austauschbar für Tests: Terminal, Browser, Erkennung und Installation.
var (
	joinTTY         = openDevTTY
	joinOpenBrowser = openBrowser
	joinHasDisplay  = hasDisplay
	joinCanOpen     = canOpenBrowser
	joinDetect      = detectHarnesses
	joinInstall     = cmdInstall
	joinTimeout     = 10 * time.Minute

	startCallbackFunc = startCallback
)

const (
	errInterrupted = "Setup was interrupted. Run the command again."
	joinPairPath   = "/join/pair"
)

var (
	pairRE        = regexp.MustCompile(`^[A-Za-z0-9]{4}-?[A-Za-z0-9]{4}$`)
	confirmCodeRE = regexp.MustCompile(`^[A-Z0-9]{4}$`)
)

var machineNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// terminal ist die Rückfragestelle. Sie muss /dev/tty sein, weil stdin bei
// `curl ... | sh` die Pipe ist.
type terminal interface {
	Ask(prompt string) (string, error)
	Close() error
}

type devTTY struct {
	f *os.File
	r *bufio.Reader
}

func openDevTTY() (terminal, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	return &devTTY{f: f, r: bufio.NewReader(f)}, nil
}

func (t *devTTY) Ask(prompt string) (string, error) {
	if _, err := io.WriteString(t.f, prompt); err != nil {
		return "", err
	}
	line, err := t.r.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func (t *devTTY) Close() error { return t.f.Close() }

// ---- PKCE und State ----

// newPKCE liefert einen Verifier nach RFC 7636 (43 Zeichen aus [A-Za-z0-9-_])
// und seine S256-Challenge.
func newPKCE() (verifier, challenge string, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func newState() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// normalizeMachine macht aus einem Hostnamen einen Gerätenamen, den der Server
// annimmt: nur [A-Za-z0-9._-], höchstens 64 Zeichen.
func normalizeMachine(in string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(in) {
		if r < 128 && (r == '.' || r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := b.String()
	if len(out) > 64 {
		out = out[:64]
	}
	if out == "" {
		return "machine"
	}
	return out
}

// ---- Loopback-Listener ----

var errDenied = errors.New("denied in the browser")

type callbackResult struct {
	code string
	err  error
}

// callback ist der kurzlebige Listener auf 127.0.0.1, der nur für die Dauer
// des Befehls läuft. Er nimmt genau einen gültigen GET /callback an.
type callback struct {
	ln     net.Listener
	srv    *http.Server
	state  string
	port   int
	used   atomic.Bool
	got    chan callbackResult
	result chan error
}

func startCallback(state string) (*callback, error) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	c := &callback{ln: ln, state: state, got: make(chan callbackResult, 1), result: make(chan error, 1)}
	c.port = ln.Addr().(*net.TCPAddr).Port
	c.srv = &http.Server{Handler: http.HandlerFunc(c.handle), ReadHeaderTimeout: 10 * time.Second}
	go c.srv.Serve(ln)
	return c, nil
}

func (c *callback) Addr() string { return c.ln.Addr().String() }
func (c *callback) Port() int    { return c.port }

func (c *callback) hostOK(host string) bool {
	p := strconv.Itoa(c.port)
	return host == "127.0.0.1:"+p || host == "localhost:"+p
}

const (
	pageDone   = "Connected. You can close this tab."
	pageFailed = "Setup failed. See your terminal."
)

func (c *callback) handle(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	h.Set("X-Content-Type-Options", "nosniff")
	if !c.hostOK(r.Host) {
		http.Error(w, "bad host", http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodGet {
		h.Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	switch r.URL.Path {
	case "/callback":
		c.callback(w, r)
	case "/done":
		writePage(w, pageDone)
	case "/failed":
		writePage(w, pageFailed)
	default:
		http.NotFound(w, r)
	}
}

func (c *callback) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(c.state)) != 1 {
		http.Error(w, "bad state", http.StatusBadRequest)
		return
	}
	var res callbackResult
	switch {
	case q.Get("error") == "access_denied":
		res.err = errDenied
	case q.Get("error") != "":
		res.err = errors.New("browser reported an error")
	default:
		res.code = q.Get("code")
		if res.code == "" || len(res.code) > 512 {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
	}
	if !c.used.CompareAndSwap(false, true) {
		http.NotFound(w, r)
		return
	}
	c.got <- res
	target := "/failed"
	select {
	case err := <-c.result:
		if err == nil {
			target = "/done"
		}
	case <-time.After(60 * time.Second):
	case <-r.Context().Done():
	}
	// Der Code steht nur in dieser einen Adresse; danach zeigt der Browser eine saubere.
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func writePage(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>ghosttree</title><p style=\"font:16px system-ui;margin:3em\">%s</p>\n", msg)
}

// Wait blockiert bis zum gültigen Callback, zum Abbruch des Kontexts oder zur
// Ablehnung im Browser.
func (c *callback) Wait(ctx context.Context) (string, error) {
	select {
	case res := <-c.got:
		return res.code, res.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Finish meldet dem wartenden Browser-Request, wie der Tausch ausging.
func (c *callback) Finish(err error) {
	select {
	case c.result <- err:
	default:
	}
}

func (c *callback) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if c.srv.Shutdown(ctx) != nil {
		c.srv.Close()
	}
	c.ln.Close()
}

// ---- Umgebung ----

func hasDisplay() bool {
	if runtime.GOOS != "linux" && runtime.GOOS != "freebsd" {
		return true
	}
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

// canOpenBrowser sagt, ob es ein Programm zum Öffnen einer Adresse gibt.
func canOpenBrowser() bool {
	name := "xdg-open"
	switch runtime.GOOS {
	case "darwin":
		name = "open"
	case "windows":
		name = "rundll32"
	}
	_, err := exec.LookPath(name)
	return err == nil
}

func openBrowser(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

func detectHarnesses() []string {
	home, _ := os.UserHomeDir()
	var found []string
	for _, h := range []string{"claude", "codex"} {
		if st, err := os.Stat(home + "/." + h); err == nil && st.IsDir() {
			found = append(found, h)
		} else if _, err := exec.LookPath(h); err == nil {
			found = append(found, h)
		}
	}
	return found
}

// ---- Befehl ----

func cmdJoin(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	fs.SetOutput(stdout)
	serverURL := fs.String("server", "", "ghosttree server URL")
	pair := fs.String("pair", "", "pairing code from the invitation page (XXXX-XXXX)")
	name := fs.String("name", "", "machine name (default: hostname)")
	noBrowser := fs.Bool("no-browser", false, "do not use the browser on this machine; type a code in any browser instead")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	fs.Usage = func() {
		fmt.Fprint(stdout, `usage: ctx join --server <url> --pair XXXX-XXXX [--name <machine>] [--no-browser] [--yes]

  --server      ghosttree server URL
  --pair        pairing code from the invitation page
  --name        machine name (A-Z a-z 0-9 . _ -, default: hostname)
  --no-browser  type a code in any browser instead of using this machine's
  --yes         do not ask for confirmation
`)
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	usage := func(msg string) int {
		fmt.Fprintln(stdout, msg)
		fmt.Fprintln(stdout, "usage: ctx join --server <url> --pair XXXX-XXXX [--name <machine>] [--no-browser] [--yes]")
		return 2
	}
	server := strings.TrimRight(strings.TrimSpace(*serverURL), "/")
	u, err := url.Parse(server)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return usage("--server must be an http(s) URL")
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return usage("--server must use https (plain http only for localhost)")
	}
	if fs.NArg() != 0 || !pairRE.MatchString(strings.TrimSpace(*pair)) {
		return usage("--pair must look like XXXX-XXXX")
	}
	machine := *name
	if machine == "" {
		host, _ := os.Hostname()
		machine = normalizeMachine(host)
	} else if !machineNameRE.MatchString(machine) {
		return usage("--name may only contain A-Z a-z 0-9 . _ - (at most 64)")
	}

	// Ein Signal gilt überall als Abbruch; nach der Ausstellung des Tokens
	// führt Abbruch zum Widerruf.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	var tty terminal
	if !*yes {
		t, err := joinTTY()
		if err != nil {
			fmt.Fprintln(stdout, "No terminal to ask on; pass --yes to continue without questions.")
			return 1
		}
		defer t.Close()
		tty = t
	}
	existing, loadErr := config.Load()
	switch {
	case loadErr != nil && !errors.Is(loadErr, os.ErrNotExist):
		if tty != nil && !confirm(ctx, tty, fmt.Sprintf("The config at %s is unreadable. Overwrite it? [y/N] ", config.Path()), false) {
			fmt.Fprintln(stdout, "Cancelled. Config unchanged.")
			return 1
		}
	case existing.Token != "" && existing.ServerURL != "" && strings.TrimRight(existing.ServerURL, "/") != server && tty != nil:
		if !confirm(ctx, tty, fmt.Sprintf("Replace the config for %s with %s? [y/N] ", safe(existing.ServerURL), server), false) {
			fmt.Fprintln(stdout, "Cancelled. Config unchanged.")
			return 1
		}
	}
	// Ein erneuter Join derselben Maschine widerruft das alte Geräte-Token schon
	// bei der Ausstellung des neuen: vorher fragen.
	replaced := false
	if loadErr == nil && existing.Token != "" && strings.TrimRight(existing.ServerURL, "/") == server {
		old := client.New(config.Config{ServerURL: server, Token: existing.Token, Machine: existing.Machine})
		if who, err := old.WhoAmIContext(ctx); err == nil {
			same := strings.EqualFold(existing.Machine, machine)
			q := fmt.Sprintf("This machine is already connected as %s. Joining replaces that connection. Continue? [y/N] ", safe(who.Label))
			if !same {
				q = fmt.Sprintf("This machine is already connected as %s (machine %s). Joining replaces the local config. Continue? [y/N] ", safe(who.Label), safe(existing.Machine))
			}
			if tty != nil && !confirm(ctx, tty, q, false) {
				fmt.Fprintln(stdout, "Cancelled. Config and connection unchanged.")
				return 1
			}
			replaced = same
		}
	}

	c := client.New(config.Config{ServerURL: server})
	remote := os.Getenv("SSH_CONNECTION") != ""
	var tok client.DeviceToken
	var cb *callback
	if !*noBrowser && !remote && joinHasDisplay() && joinCanOpen() {
		cb = prepareLoopback()
	}
	if cb != nil {
		defer cb.Close()
		tok, err = joinLoopback(ctx, c, cb, strings.TrimSpace(*pair), machine, server, stdout)
	} else {
		tok, err = joinCode(ctx, c, strings.TrimSpace(*pair), machine, server, stdout)
	}
	if err != nil {
		fmt.Fprintln(stdout, interruptedOr(err))
		return 1
	}

	cfg := config.Config{ServerURL: server, Token: tok.AccessToken, Machine: safe(tok.Machine)}
	if cfg.Machine == "" {
		cfg.Machine = machine
	}
	issued := client.New(cfg)
	// Ab hier gibt es ein gültiges, noch nicht gespeichertes Token: jeder Weg
	// ohne Speichern widerruft es.
	abort := func(msg string) int {
		fmt.Fprintln(stdout, msg)
		revokeIssued(issued, stdout)
		if replaced {
			fmt.Fprintln(stdout, "The previous connection of this machine is no longer valid; run ctx join or ctx login to connect again.")
		}
		return 1
	}
	who, err := issued.WhoAmIContext(ctx)
	if err != nil {
		return abort("Could not check the new token. Nothing written.")
	}
	org := ""
	if orgs, err := issued.ListOrgsContext(ctx); err == nil {
		var names []string
		for _, o := range orgs {
			if o.Default {
				names = append([]string{safe(o.Name)}, names...)
			} else {
				names = append(names, safe(o.Name))
			}
		}
		if len(names) > 0 {
			org = ", org " + strings.Join(names, ", ")
		}
	}
	label := safe(who.Label)
	fmt.Fprintf(stdout, "Account %s%s, machine %s\n", label, org, cfg.Machine)
	if tty != nil && !confirm(ctx, tty, fmt.Sprintf("Connect this machine as %s? [y/N] ", label), false) {
		return abort("Cancelled. Nothing written.")
	}
	if err := config.Save(cfg); err != nil {
		return abort(fmt.Sprintf("save config: %v", err))
	}
	fmt.Fprintf(stdout, "Wrote %s\n", config.Path())
	for _, h := range joinDetect() {
		if ctx.Err() != nil {
			fmt.Fprintln(stdout, "Interrupted. Connected, but not installed for your agents; run ctx install claude|codex.")
			return 1
		}
		if tty != nil && !confirm(ctx, tty, fmt.Sprintf("Install ghosttree for %s? [Y/n] ", h), true) {
			continue
		}
		if code := joinInstall([]string{h}, stdout); code != 0 {
			fmt.Fprintf(stdout, "install %s failed (exit %d)\n", h, code)
		}
	}
	var st bytes.Buffer
	cmdStatus(nil, &st)
	for _, line := range strings.Split(strings.TrimSpace(st.String()), "\n") {
		if line != "" && !strings.HasPrefix(line, "transcripts") {
			fmt.Fprintln(stdout, safe(line))
		}
	}
	return 0
}

// interruptedOr macht aus einem Abbruch des Kontexts die übliche Meldung.
func interruptedOr(err error) string {
	if errors.Is(err, context.Canceled) {
		return errInterrupted
	}
	return safe(err.Error())
}

func isLoopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// safe entfernt Steuerzeichen (C0, DEL, C1, auch ESC) und Formatzeichen (Bidi, Zero-Width) aus Text, den der Server
// geliefert hat, damit er das Terminal nicht umschreiben und eine Rückfrage
// nicht fälschen kann.
func safe(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}

// revokeIssued widerruft das gerade ausgestellte, nicht gespeicherte Token, damit
// kein ungenutztes gültiges Token zurückbleibt.
func revokeIssued(c *client.Client, stdout io.Writer) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := c.RevokeSelf(ctx); err != nil {
		fmt.Fprintf(stdout, "Could not revoke the new token (%s); release machine %s on the server.\n", interruptedOr(err), safe(c.Machine()))
		return
	}
	fmt.Fprintln(stdout, "New token revoked.")
}

// confirm fragt auf dem Terminal. Ein Abbruch des Kontexts (Signal) während der
// Frage zählt als Ablehnung; die blockierte Leseroutine endet mit dem Prozess.
func confirm(ctx context.Context, t terminal, prompt string, def bool) bool {
	// Nach einem Abbruch keine weitere Leseroutine auf demselben Terminal starten.
	if ctx.Err() != nil {
		return false
	}
	type answer struct {
		text string
		err  error
	}
	ch := make(chan answer, 1)
	go func() {
		a, err := t.Ask(prompt)
		ch <- answer{a, err}
	}()
	var a answer
	select {
	case a = <-ch:
	case <-ctx.Done():
		return false
	}
	if a.err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(a.text)) {
	case "y", "yes":
		return true
	case "":
		return def
	}
	return false
}

// prepareLoopback startet den Listener; scheitert etwas, gilt der Rückfall (nil).
func prepareLoopback() *callback {
	state, err := newState()
	if err != nil {
		return nil
	}
	cb, err := startCallbackFunc(state)
	if err != nil {
		return nil
	}
	return cb
}

func claimError(err error) error {
	switch client.ErrorCode(err) {
	case "invalid_pair":
		return errors.New("Pairing code is not valid, expired or already used. Open the invitation page for a new one.")
	case "too_many_requests":
		return errors.New("Too many attempts. Try again in a minute.")
	}
	return failure(err)
}

func exchangeError(err error) error {
	switch client.ErrorCode(err) {
	case "invalid_grant":
		return errors.New(errInterrupted)
	case "machine_name_taken":
		return errors.New("That machine name belongs to another account. Run again with --name <other>.")
	case "too_many_requests":
		return errors.New("Too many attempts. Try again in a minute.")
	}
	return failure(err)
}

// failure beschreibt einen Fehler ohne den Körper der Serverantwort.
func failure(err error) error {
	var se *client.StatusError
	if errors.As(err, &se) {
		return fmt.Errorf("Pairing failed (server answered %d).", se.Status)
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	return fmt.Errorf("Pairing failed: %s", safe(err.Error()))
}

func joinLoopback(ctx context.Context, c *client.Client, cb *callback, pair, machine, server string, stdout io.Writer) (client.DeviceToken, error) {
	verifier, challenge, err := newPKCE()
	if err != nil {
		return client.DeviceToken{}, err
	}
	claim, err := c.JoinClaim(ctx, client.JoinClaimRequest{
		Pair: pair, Machine: machine,
		CodeChallenge: challenge, CodeChallengeMethod: "S256",
		LoopbackPort: cb.Port(), LoopbackHost: "127.0.0.1", State: cb.state,
	})
	if err != nil {
		return client.DeviceToken{}, claimError(err)
	}
	if claim.Mode != "loopback" {
		return client.DeviceToken{}, errors.New(errInterrupted)
	}
	link := server + joinPairPath
	if joinHasDisplay() && joinOpenBrowser(link) == nil {
		fmt.Fprintf(stdout, "Approve %s in your browser: %s\n", machine, link)
	} else {
		fmt.Fprintf(stdout, "Open this link in your browser to approve %s: %s\n", machine, link)
	}
	wait := joinTimeout
	if d := time.Duration(claim.ExpiresIn) * time.Second; claim.ExpiresIn > 0 && d < wait {
		wait = d
	}
	wctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	code, err := cb.Wait(wctx)
	switch {
	case errors.Is(err, errDenied):
		cb.Finish(err)
		return client.DeviceToken{}, errors.New("Denied in the browser.")
	case errors.Is(err, context.DeadlineExceeded):
		return client.DeviceToken{}, errors.New(errInterrupted)
	case err != nil:
		return client.DeviceToken{}, err
	}
	// Der Tausch läuft unabhängig vom Signal-Kontext, damit ein Abbruch mitten
	// im Tausch kein Token ausstellt, das niemand mehr sieht.
	xctx, xcancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer xcancel()
	tok, err := c.JoinExchange(xctx, code, verifier)
	cb.Finish(err)
	if err != nil {
		return client.DeviceToken{}, exchangeError(err)
	}
	return tok, nil
}

// joinCode ist der Rückfall ohne Loopback: der Bestätigungscode steht im
// Terminal, das Token kommt über den Geräte-Ablauf.
func joinCode(ctx context.Context, c *client.Client, pair, machine, server string, stdout io.Writer) (client.DeviceToken, error) {
	claim, err := c.JoinClaim(ctx, client.JoinClaimRequest{Pair: pair, Machine: machine})
	if err != nil {
		return client.DeviceToken{}, claimError(err)
	}
	if claim.Mode != "code" || claim.DeviceCode == "" {
		return client.DeviceToken{}, errors.New(errInterrupted)
	}
	if !confirmCodeRE.MatchString(claim.ConfirmCode) {
		return client.DeviceToken{}, errors.New(errInterrupted)
	}
	fmt.Fprintf(stdout, "Approve %s at %s - Type this code in your browser: %s\n", machine, server+joinPairPath, claim.ConfirmCode)
	interval := time.Duration(claim.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	wait := joinTimeout
	if d := time.Duration(claim.ExpiresIn) * time.Second; claim.ExpiresIn > 0 && d < wait {
		wait = d
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if err := loginSleep(ctx, interval); err != nil {
			return client.DeviceToken{}, err
		}
		pctx, pcancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		tok, err := c.PollDeviceLogin(pctx, claim.DeviceCode)
		pcancel()
		if err == nil {
			return tok, nil
		}
		var pe *client.DevicePollError
		if !errors.As(err, &pe) {
			return client.DeviceToken{}, exchangeError(err)
		}
		switch pe.Code {
		case "authorization_pending":
		case "slow_down":
			if pe.Interval > 0 {
				interval = time.Duration(pe.Interval) * time.Second
			} else {
				interval += 5 * time.Second
			}
		case "access_denied":
			return client.DeviceToken{}, errors.New("Denied in the browser.")
		default: // expired_token und alles Unbekannte
			return client.DeviceToken{}, errors.New(errInterrupted)
		}
	}
	return client.DeviceToken{}, errors.New(errInterrupted)
}
