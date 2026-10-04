package store

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Join-Sitzungen (REQ-434, Paket P2): der Treffpunkt von Browser und Installer.
//
// Die Sitzung entsteht erst, wenn ein Konto feststeht: nach Annahme der
// Einladung (Bind-Weg der Webseite) oder auf Wunsch eines angemeldeten Kontos
// ("Connect this machine"). Beides ruft Create; die Sitzung gehört dem Konto, der
// Paarungscode steht nur auf dessen Seite. Der Installer meldet sich mit dem Code
// (Claim) und wartet; nur dieses Konto kann das Gerät freigeben (Decide).
//
// Zwei Wege zum Token, beide ohne dass der Paarungscode allein genügt:
//   - Loopback (Regelfall, RFC 8252 mit PKCE): der Claim trägt code_challenge,
//     einen Loopback-Port und einen state. Nach der Freigabe leitet der Browser
//     auf http://127.0.0.1:<port>/callback?code=..&state=.. , und nur wer den
//     code_verifier kennt, tauscht den Code gegen das Token (Exchange). Ein Dieb
//     des Paarungscodes bekommt den Code nie, er landet auf dem Loopback des
//     Browsers.
//   - Code (Rückfall, etwa CLI auf einer anderen Maschine): der Claim erzeugt
//     einen Bestätigungscode, der nur in der Claim-Antwort steht. Die Freigabe
//     verlangt, dass er eingetippt wird; das Token kommt dann über den
//     Geräte-Ablauf (/api/auth/device/token).
//
// Ein zweiter Claim auf einen gemeldeten Code verwirft die Sitzung: wer den Code
// abfängt, kann die Sitzung nicht still übernehmen, und das Konto sieht die
// Warnung. Die einzige Ausnahme ist derselbe Installer nach einem Abbruch
// (Strg-C): Die Claim-Antwort trägt ein zufälliges Wiederaufnahme-Token (resume),
// das nur der Installer kennt. Ein erneuter Claim mit dem passenden Token ersetzt
// die eigene frühere Anfrage (gleicher Weg, Fehlversuche bei der Bestätigung
// bleiben gezählt); ohne es gilt er wie der eines Fremden. Name und Netz des
// Geräts taugen nicht als Beweis, beides kann ein Fremder nachahmen.
//
// Der Zustand liegt im Speicher; ein Neustart verwirft alles, und jeder alte Code
// wird wie ein unbekannter behandelt.
const (
	JoinSessionTTL = 15 * time.Minute
	// JoinMaxLifetime ist das Login-Fenster: so lange darf eine Sitzung von
	// ihrer Entstehung an höchstens leben, auch wenn ein Gerät sie beansprucht
	// hat. Ohne die Grenze verlängerte jeder Claim Sitzung und Cookie.
	JoinMaxLifetime = 30 * time.Minute

	// JoinClaimTTL: so lange darf ein gemeldetes Gerät auf die Freigabe warten.
	// Danach läuft die Anfrage aus (JoinExpired) und der Code ist frei für einen
	// neuen; ein abgebrochener Installer blockiert die Seite also nicht.
	JoinClaimTTL = 5 * time.Minute

	joinAuthTTL            = 2 * time.Minute
	maxJoinSessions        = 1000
	maxJoinConfirmFails    = 3
	maxJoinNameConflicts   = 3 // vergebene Maschinennamen je Konto und Fenster, dann ist der Code verbrannt
	joinNameConflictWindow = JoinMaxLifetime
	maxJoinFailures        = 8 // falsche Codes je /64 im Fenster
	joinFailureWindow      = 10 * time.Minute
	maxJoinFailureKeys     = 10000
	joinConfirmLen         = 4
	joinWiderFactor        = 4 // je /48 (IPv6) sind es so viele Mal mehr
)

// Zustände einer Sitzung, wie die Seite sie sieht.
const (
	JoinNone        = "none"        // keine (mehr)
	JoinWaiting     = "waiting"     // Code ausgegeben, noch kein Gerät
	JoinClaimed     = "claimed"     // Gerät hat sich gemeldet, Freigabe offen
	JoinApproved    = "approved"    // freigegeben, Token noch nicht abgeholt
	JoinDenied      = "denied"      // abgelehnt
	JoinConnected   = "connected"   // Token ausgestellt
	JoinCompromised = "compromised" // zweiter Claim oder zu viele falsche Bestätigungen: neuer Code nötig
	JoinExpired     = "expired"     // gemeldet, aber nicht rechtzeitig freigegeben: neuer Code nötig
)

// Wege zum Token.
const (
	JoinModeLoopback = "loopback"
	JoinModeCode     = "code"
)

var (
	// ErrJoinInvalid deckt unbekannt, abgelaufen, schon beansprucht und
	// fehlerhaft geformt ab. Es ist immer dieselbe Antwort.
	ErrJoinInvalid = errors.New("pairing code is invalid or expired")
	// ErrJoinLocked: von dieser Adresse kamen zu viele falsche Codes.
	ErrJoinLocked = errors.New("too many wrong pairing codes, try again later")
	// ErrJoinNotReady: es gibt nichts freizugeben (keine Sitzung, kein Gerät,
	// oder die Anfrage gehört zu einem früheren Gerät).
	ErrJoinNotReady = errors.New("no device is waiting")
	// ErrJoinBurned: zu viele vergebene Maschinennamen; der Code ist verbraucht.
	ErrJoinBurned = errors.New("too many machine names were refused, the pairing code is used up")
	// ErrJoinConfirm: der eingegebene Bestätigungscode stimmt nicht.
	ErrJoinConfirm = errors.New("the confirmation code does not match")
)

type joinSession struct {
	sid     string
	account string // leer bis zur Bindung
	pair    string
	created time.Time
	expires time.Time
	state   string

	// Gerät
	mode         string
	machine      string
	remote       string
	net          string // Netz (/64) des Claim, zum Vergleich mit dem Browser
	wide         string // Netz (/48) des Claim, leer bei IPv4
	nonce        string
	deviceHash   string // Code-Weg: Geräte-Ablauf
	confirmHash  string
	confirmFail  int
	challenge    string // Loopback-Weg
	cbHost       string
	cbPort       int
	cbState      string
	authHash     string
	authExpires  time.Time
	claimExpires time.Time // Ende der Wartezeit auf die Freigabe
	resumeHash   string    // Wiederaufnahme-Token des Installers (nur Hash)
	auto         bool      // der Installer hat den Namen selbst gewählt (Hostname), nicht der Mensch
	resolve      func(account, machine string, auto bool) (string, error)
}

// JoinView ist, was die Seite über die Sitzung des Kontos erfährt.
type JoinView struct {
	State   string
	Pair    string // XXXX-XXXX; leer ohne Sitzung
	Machine string
	Remote  string
	Net     string
	Nonce   string
	Mode    string
	// Callback: Loopback-Weg, wenn die Sitzung kompromittiert ist; Ziel, auf das
	// der Browser den wartenden Installer abbrechen lassen kann.
	Callback string
}

// JoinClaimRequest trägt, was der Installer beim Claim schickt.
type JoinClaimRequest struct {
	Addr, Pair, Machine string
	// Loopback-Weg: alle drei oder keins.
	Challenge, State string
	Host             string
	Port             int
	// Resume ist das Token aus der Antwort auf den ersten Claim; nur damit darf
	// derselbe Installer nach einem Abbruch erneut claimen.
	Resume string
	// Auto sagt, dass Machine der Hostname ist und kein Wunsch: ist er
	// vergeben, darf Resolve einen anderen wählen.
	Auto bool
	// Resolve macht aus dem Wunschnamen den Namen, unter dem das Konto der
	// Sitzung die Maschine anmelden kann (ErrMachineTaken, wenn keiner geht). Es
	// läuft erst, nachdem der Code geprüft ist, damit ein Fremder ohne gültigen
	// Code nichts über Maschinennamen erfährt.
	Resolve func(account, machine string, auto bool) (string, error)
}

// JoinClaim ist die Antwort an den Installer.
type JoinClaim struct {
	Mode                string
	DeviceCode          string // nur Code-Weg
	Confirm             string // nur Code-Weg
	ExpiresIn, Interval time.Duration
	Resume              string // Wiederaufnahme-Token; der Installer legt es lokal ab
	Machine             string // der Name, unter dem das Gerät angemeldet wird
}

// JoinDecision ist das Ergebnis einer Freigabe.
type JoinDecision struct {
	Redirect string // Loopback-Weg: Ziel für den Browser (bei Ablehnung mit error=access_denied)
}

// JoinGrant ist das Ergebnis eines gültigen Tausches.
type JoinGrant struct {
	Account, Machine, sid string
}

// JoinSessions hält die offenen Sitzungen.
type JoinSessions struct {
	mu        sync.Mutex
	now       func() time.Time
	device    *DeviceFlows
	startFlow func(client, machine, remote string, ttl time.Duration) (DeviceStart, error)
	all       map[string]*joinSession
	byAccount map[string]*joinSession
	byPair    map[string]*joinSession
	byAuth    map[string]*joinSession
	failures  map[string][]time.Time
	// nameConflicts zählt je Konto (nicht je Code, sonst setzte jeder neue Code
	// den Zähler zurück) die Claims, bei denen der Wunschname vergeben war.
	nameConflicts map[string][]time.Time
}

func NewJoinSessions(d *DeviceFlows) *JoinSessions {
	return &JoinSessions{now: time.Now, device: d, startFlow: d.StartJoin, all: map[string]*joinSession{}, byAccount: map[string]*joinSession{}, byPair: map[string]*joinSession{}, byAuth: map[string]*joinSession{},
		failures: map[string][]time.Time{}, nameConflicts: map[string][]time.Time{}}
}

// Join gibt die Sitzungen dieses Stores zurück.
func (s *Store) Join() *JoinSessions {
	s.joinOnce.Do(func() { s.join = NewJoinSessions(s.Device()) })
	return s.join
}

// SetClock ersetzt die Uhr; für Tests.
func (j *JoinSessions) SetClock(now func() time.Time) {
	j.mu.Lock()
	j.now = now
	j.mu.Unlock()
}

// NetworkKey bündelt IPv6-Adressen auf ihr /64, sonst reichte ein Wechsel
// innerhalb des Anschlusses, um Grenzen zu umgehen.
func NetworkKey(addr string) string {
	if ip, err := netip.ParseAddr(addr); err == nil && ip.Is6() && !ip.Is4In6() {
		if p, err := ip.Prefix(64); err == nil {
			return p.String()
		}
	}
	return addr
}

func widerKey(addr string) string {
	if ip, err := netip.ParseAddr(addr); err == nil && ip.Is6() && !ip.Is4In6() {
		if p, err := ip.Prefix(48); err == nil {
			return "w:" + p.String()
		}
	}
	return ""
}

// joinRandomHex ist die Zufallsquelle der Claims; Tests ersetzen sie, um den
// Fehlerweg zu prüfen.
var joinRandomHex = randomHex

func randomHex(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func (j *JoinSessions) unlink(s *joinSession) {
	delete(j.all, s.sid)
	if j.byAccount[s.account] == s {
		delete(j.byAccount, s.account)
	}
	delete(j.byPair, hashCode(s.pair))
	if s.authHash != "" {
		delete(j.byAuth, s.authHash)
	}
	if s.deviceHash != "" {
		j.device.DropJoin(s.deviceHash)
	}
}

// compromise verwirft Gerät und Codes der Sitzung, behält sie aber, damit die
// Seite warnen kann. Ein neuer Code ersetzt sie.
func (j *JoinSessions) compromise(s *joinSession) {
	if s.deviceHash != "" {
		j.device.DropJoin(s.deviceHash)
	}
	if s.authHash != "" {
		delete(j.byAuth, s.authHash)
	}
	delete(j.byPair, hashCode(s.pair))
	s.state, s.authHash, s.deviceHash, s.resumeHash = JoinCompromised, "", "", ""
}

// lapse beendet eine gemeldete, nie freigegebene Anfrage: Gerät und Codes
// verfallen, die Sitzung bleibt, damit die Seite es sagen kann.
func (j *JoinSessions) lapse(s *joinSession) {
	j.compromise(s)
	s.state = JoinExpired
}

func (j *JoinSessions) purge(now time.Time) {
	for _, s := range j.all {
		if !now.Before(s.expires) {
			j.unlink(s)
		}
	}
}

// evictOldest macht Platz: zuerst Sitzungen ohne Gerät, dann die ältesten.
func (j *JoinSessions) evictOldest() {
	var oldest *joinSession
	for _, s := range j.all {
		if oldest == nil || (oldest.state != JoinWaiting && s.state == JoinWaiting) ||
			((oldest.state == JoinWaiting) == (s.state == JoinWaiting) && s.created.Before(oldest.created)) {
			oldest = s
		}
	}
	if oldest != nil {
		j.unlink(oldest)
	}
}

// extend verlängert die Sitzung bis end, nie über das Login-Fenster hinaus.
func (s *joinSession) extend(end time.Time) {
	if limit := s.created.Add(JoinMaxLifetime); end.After(limit) {
		end = limit
	}
	if end.After(s.expires) {
		s.expires = end
	}
}

// callback baut das Ziel auf dem Loopback des Installers (RFC 6749 4.1.2).
func (s *joinSession) callback(q url.Values) string {
	q.Set("state", s.cbState)
	return "http://" + s.cbHost + ":" + strconv.Itoa(s.cbPort) + "/callback?" + q.Encode()
}

func (j *JoinSessions) newPair() (string, error) {
	for tries := 0; tries < 20; tries++ {
		c, err := newUserCode()
		if err != nil {
			return "", err
		}
		if _, taken := j.byPair[hashCode(c)]; !taken {
			return c, nil
		}
	}
	return "", ErrDeviceBusy
}

func (j *JoinSessions) add(now time.Time, account string) (*joinSession, error) {
	pair, err := j.newPair()
	if err != nil {
		return nil, err
	}
	sid, err := randomHex(8)
	if err != nil {
		return nil, err
	}
	if len(j.all) >= maxJoinSessions {
		j.evictOldest()
	}
	s := &joinSession{sid: sid, account: account, pair: pair, created: now, expires: now.Add(JoinSessionTTL), state: JoinWaiting}
	j.all[sid] = s
	j.byPair[hashCode(pair)] = s
	j.byAccount[account] = s
	return s, nil
}

// Create legt für das Konto eine neue Sitzung an (nach der Annahme einer
// Einladung oder bei "Connect this machine"). Eine bestehende Sitzung des Kontos
// endet damit, samt Gerät.
func (j *JoinSessions) Create(account string) (string, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := j.now()
	j.purge(now)
	if old := j.byAccount[account]; old != nil {
		j.unlink(old)
	}
	s, err := j.add(now, account)
	if err != nil {
		return "", err
	}
	return FormatUserCode(s.pair), nil
}

func (j *JoinSessions) failedKeys(addr string) []string {
	keys := []string{NetworkKey(addr)}
	if w := widerKey(addr); w != "" {
		keys = append(keys, w)
	}
	return keys
}

func (j *JoinSessions) lockedOut(addr string, now time.Time) bool {
	cutoff := now.Add(-joinFailureWindow)
	locked := false
	for i, k := range j.failedKeys(addr) {
		j.failures[k] = trimBefore(j.failures[k], cutoff)
		if len(j.failures[k]) == 0 {
			delete(j.failures, k)
		}
		limit := maxJoinFailures
		if i > 0 {
			limit *= joinWiderFactor
		}
		if len(j.failures[k]) >= limit {
			locked = true
		}
	}
	return locked
}

func (j *JoinSessions) fail(addr string, now time.Time) {
	for i, k := range j.failedKeys(addr) {
		if _, ok := j.failures[k]; !ok && len(j.failures) >= maxJoinFailureKeys {
			for old, v := range j.failures {
				if len(trimBefore(v, now.Add(-joinFailureWindow))) == 0 {
					delete(j.failures, old)
				}
			}
			if len(j.failures) >= maxJoinFailureKeys {
				for old := range j.failures {
					delete(j.failures, old)
					break
				}
			}
		}
		j.failures[k] = append(j.failures[k], now)
		limit := maxJoinFailures
		if i > 0 {
			limit *= joinWiderFactor
		}
		if len(j.failures[k]) == limit {
			slog.Warn("join: many wrong pairing codes from one network", "network", k, "window", joinFailureWindow.String())
		}
	}
}

var (
	challengeChars = func(c byte) bool {
		return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_'
	}
	stateChars = func(c byte) bool { return challengeChars(c) || c == '.' || c == '~' }
)

func allBytes(s string, ok func(byte) bool) bool {
	for i := 0; i < len(s); i++ {
		if !ok(s[i]) {
			return false
		}
	}
	return true
}

// ValidLoopback prüft die Felder des Loopback-Wegs; Aufrufer melden Fehler als
// kaputte Anfrage, unabhängig vom Code.
func (r JoinClaimRequest) ValidLoopback() bool {
	return len(r.Challenge) == 43 && allBytes(r.Challenge, challengeChars) &&
		len(r.State) >= 8 && len(r.State) <= 128 && allBytes(r.State, stateChars) &&
		(r.Host == "" || r.Host == "127.0.0.1" || r.Host == "[::1]") && r.Port >= 1024 && r.Port <= 65535
}

// Loopback sagt, ob der Claim den Loopback-Weg verlangt.
func (r JoinClaimRequest) Loopback() bool {
	return r.Challenge != "" || r.State != "" || r.Port != 0 || r.Host != ""
}

// Claim meldet ein Gerät mit dem Paarungscode an. Fehler: ErrJoinLocked (zu
// viele falsche Codes aus diesem Netz), ErrDeviceBusy (zu viele offene
// Geräte-Abläufe), sonst für jeden Grund ErrJoinInvalid. Sperre und Auslastung
// werden vor der Prüfung des Codes beantwortet, damit sie nichts über Codes
// verraten. Der Aufrufer hat die Felder des Loopback-Wegs geprüft.
func (j *JoinSessions) Claim(req JoinClaimRequest) (JoinClaim, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := j.now()
	j.purge(now)
	netKey := NetworkKey(req.Addr)
	loop := req.Loopback()
	if j.lockedOut(req.Addr, now) {
		return JoinClaim{}, ErrJoinLocked
	}
	if !loop && j.device.Busy(netKey) {
		return JoinClaim{}, ErrDeviceBusy
	}
	wide := widerKey(req.Addr)
	if loop && j.loopbackBusy(netKey, wide, now) {
		return JoinClaim{}, ErrDeviceBusy
	}
	var s *joinSession
	if code := NormalizeUserCode(req.Pair); code != "" {
		s = j.byPair[hashCode(code)]
	}
	if s == nil {
		j.fail(req.Addr, now)
		return JoinClaim{}, ErrJoinInvalid
	}
	if s.state == JoinClaimed && !now.Before(s.claimExpires) {
		// Die frühere Anfrage ist schon abgelaufen: das ist ein Timeout, kein
		// zweites Gerät, und die Seite soll es auch so sagen.
		j.lapse(s)
	}
	resumed := s.state == JoinClaimed && s.resumedBy(req, loop)
	if s.state != JoinWaiting && !resumed {
		if s.state == JoinClaimed {
			j.compromise(s)
		}
		j.fail(req.Addr, now)
		return JoinClaim{}, ErrJoinInvalid
	}
	if req.Resolve != nil {
		machine, err := req.Resolve(s.account, req.Machine, req.Auto)
		// Ein vergebener Wunschname zählt, auch wenn der Server ihn selbst
		// ersetzt: sonst verriete die Antwort "<name>-<suffix>" ohne Preis, dass
		// er vergeben ist.
		if errors.Is(err, ErrMachineTaken) || (err == nil && machine != req.Machine) {
			if j.noteNameConflict(s.account, now) {
				j.compromise(s)
				return JoinClaim{}, ErrJoinBurned
			}
		}
		if err != nil {
			return JoinClaim{}, err
		}
		req.Machine = machine
	}
	resume, err := joinRandomHex(16)
	if err != nil {
		return JoinClaim{}, err
	}
	nonce, err := joinRandomHex(16)
	if err != nil {
		return JoinClaim{}, err
	}
	if resumed {
		// Derselbe Installer, wieder aufgerufen (etwa nach Strg-C), bevor jemand
		// freigegeben hat: die frühere Anfrage weicht, der Code wird nicht
		// verbrannt. Fehlversuche bei der Bestätigung (confirmFail) bleiben
		// gezählt, und der Weg (Loopback oder Code) kann nicht wechseln. Erst
		// nach dem Erzeugen der Zufallswerte wird die frühere Anfrage ersetzt.
		if s.deviceHash != "" {
			j.device.DropJoin(s.deviceHash)
		}
		s.deviceHash, s.confirmHash = "", ""
		s.state = JoinWaiting
	}
	// Ein Claim hält Sitzung (und im Code-Weg den Geräte-Ablauf) bis zum Ende
	// des Login-Fensters, damit eine späte Anmeldung das Gerät nicht verliert.
	window := s.created.Add(JoinMaxLifetime).Sub(now)
	wait := min(window, JoinClaimTTL)
	out := JoinClaim{Mode: JoinModeCode, ExpiresIn: wait, Interval: DeviceInterval, Resume: resume, Machine: req.Machine}
	if loop {
		out.Mode = JoinModeLoopback
		host := req.Host
		if host == "" {
			host = "127.0.0.1"
		}
		s.challenge, s.cbHost, s.cbPort, s.cbState = req.Challenge, host, req.Port, req.State
	} else {
		fail := func(err error) (JoinClaim, error) {
			if resumed {
				j.lapse(s)
			}
			return JoinClaim{}, err
		}
		start, err := j.startFlow(netKey, req.Machine, req.Addr, window)
		if err != nil {
			return fail(err)
		}
		confirm, err := newUserCode()
		if err != nil {
			j.device.DropJoin(hashCode(start.DeviceCode))
			return fail(err)
		}
		confirm = confirm[:joinConfirmLen]
		s.deviceHash, s.confirmHash = hashCode(start.DeviceCode), hashCode(confirm)
		out.DeviceCode, out.Confirm = start.DeviceCode, confirm
		out.Interval = start.Interval
	}
	s.mode, s.machine, s.remote, s.net, s.wide, s.nonce, s.state = out.Mode, req.Machine, req.Addr, netKey, wide, nonce, JoinClaimed
	s.extend(s.created.Add(JoinMaxLifetime))
	s.claimExpires = now.Add(JoinClaimTTL)
	s.resumeHash = hashCode(resume)
	s.auto, s.resolve = req.Auto, req.Resolve
	return out, nil
}

// noteNameConflict merkt einen vergebenen Namen für das Konto und sagt, ob die
// Grenze im Fenster erreicht ist.
func (j *JoinSessions) noteNameConflict(account string, now time.Time) bool {
	cutoff := now.Add(-joinNameConflictWindow)
	if len(j.nameConflicts) >= maxJoinSessions {
		for k, v := range j.nameConflicts {
			if len(trimBefore(v, cutoff)) == 0 {
				delete(j.nameConflicts, k)
			}
		}
	}
	list := append(trimBefore(j.nameConflicts[account], cutoff), now)
	j.nameConflicts[account] = list
	return len(list) >= maxJoinNameConflicts
}

// resumedBy sagt, ob der Claim vom Installer kommt, der diese Anfrage gestellt
// hat: er kennt das Wiederaufnahme-Token und bleibt auf demselben Weg.
func (s *joinSession) resumedBy(req JoinClaimRequest, loop bool) bool {
	return req.Resume != "" && s.resumeHash != "" && loop == (s.mode == JoinModeLoopback) &&
		subtle.ConstantTimeCompare([]byte(hashCode(req.Resume)), []byte(s.resumeHash)) == 1
}

// loopbackBusy zählt die offenen, nicht abgelaufenen Loopback-Claims eines Netzes (/64, im /48
// entsprechend mehr), wie Busy es für Geräte-Abläufe tut.
func (j *JoinSessions) loopbackBusy(netKey, wide string, now time.Time) bool {
	n, nw := 0, 0
	for _, s := range j.all {
		if s.mode != JoinModeLoopback || (s.state != JoinClaimed && s.state != JoinApproved) {
			continue
		}
		if s.state == JoinClaimed && !now.Before(s.claimExpires) {
			continue
		}
		if s.net == netKey {
			n++
		}
		if wide != "" && s.wide == wide {
			nw++
		}
	}
	return n >= maxDevicePerClient || (wide != "" && nw >= maxDevicePerClient*joinWiderFactor)
}

// live gibt die Sitzung des Kontos und, falls das Gerät verschwunden ist, den
// ehrlichen Zustand zurück. Der Aufrufer hält j.mu.
func (j *JoinSessions) live(account string, now time.Time) (*joinSession, string) {
	j.purge(now)
	s := j.byAccount[account]
	if s == nil {
		return nil, JoinNone
	}
	state := s.state
	if state == JoinClaimed && !now.Before(s.claimExpires) {
		j.lapse(s)
		return s, JoinExpired
	}
	if s.mode == JoinModeCode && (state == JoinClaimed || state == JoinApproved) && j.device.JoinStatus(s.deviceHash) == "" {
		state = JoinNone // Ablauf weg, ohne dass ein Token ausgestellt wurde
	}
	if s.mode == JoinModeLoopback && state == JoinApproved && !now.Before(s.authExpires) {
		state = JoinNone
	}
	return s, state
}

// View beschreibt die Sitzung des Kontos.
func (j *JoinSessions) View(account string) JoinView {
	j.mu.Lock()
	defer j.mu.Unlock()
	s, state := j.live(account, j.now())
	if s == nil {
		return JoinView{State: JoinNone}
	}
	v := JoinView{State: state, Pair: FormatUserCode(s.pair), Machine: s.machine, Remote: s.remote, Net: s.net,
		Nonce: s.nonce, Mode: s.mode}
	if (state == JoinCompromised || state == JoinExpired) && s.mode == JoinModeLoopback && s.cbPort != 0 {
		v.Callback = s.callback(url.Values{"error": {"access_denied"}})
	}
	return v
}

// Decide bestätigt oder verweigert das gemeldete Gerät im Namen des Kontos. nonce
// ist die Kennung der Anfrage, die die Seite zeigte; gehört sie zu einem anderen
// Gerät, passiert nichts. check läuft unter derselben Sperre (Maschinenname).
// Beim Loopback-Weg liefert die Entscheidung das Ziel für den Browser: bei Freigabe mit Code, bei Ablehnung mit error=access_denied.
func (j *JoinSessions) Decide(account string, approve bool, nonce, confirm string, check func(machine string) error) (JoinDecision, error) {
	j.mu.Lock()
	s, state := j.live(account, j.now())
	if s == nil || state != JoinClaimed || nonce == "" || subtle.ConstantTimeCompare([]byte(nonce), []byte(s.nonce)) != 1 {
		j.mu.Unlock()
		return JoinDecision{}, ErrJoinNotReady
	}
	if !approve {
		defer j.mu.Unlock()
		if s.mode == JoinModeCode {
			if err := j.device.DecideJoin(s.deviceHash, account, false); err != nil {
				return JoinDecision{}, ErrJoinNotReady
			}
		}
		s.state = JoinDenied
		delete(j.byPair, hashCode(s.pair))
		if s.mode == JoinModeLoopback {
			return JoinDecision{Redirect: s.callback(url.Values{"error": {"access_denied"}})}, nil
		}
		return JoinDecision{}, nil
	}
	if check != nil {
		// Die Prüfung fragt die Datenbank und läuft ohne Sperre; danach gilt
		// nur, was zum aktuellen Zustand derselben Anfrage noch passt.
		machine, auto, resolve := s.machine, s.auto, s.resolve
		loopback := s.mode == JoinModeLoopback
		j.mu.Unlock()
		err := check(machine)
		renamed := ""
		if errors.Is(err, ErrMachineTaken) && auto && loopback && resolve != nil {
			// Der Name war bei der Anmeldung frei und ist es nicht mehr; hat der
			// Installer ihn selbst gewählt, weicht er aus, statt den Menschen
			// vor einem Fehler stehen zu lassen. Im Code-Weg steht der Name im
			// Geräte-Ablauf und bleibt ein Fehler.
			if renamed, err = resolve(account, machine, true); err != nil {
				renamed = ""
			}
		}
		j.mu.Lock()
		if err != nil {
			j.mu.Unlock()
			return JoinDecision{}, err
		}
		s, state = j.live(account, j.now())
		if s == nil || state != JoinClaimed || s.machine != machine || subtle.ConstantTimeCompare([]byte(nonce), []byte(s.nonce)) != 1 {
			j.mu.Unlock()
			return JoinDecision{}, ErrJoinNotReady
		}
		if renamed != "" {
			s.machine = renamed
		}
	}
	defer j.mu.Unlock()
	now := j.now()
	if s.mode == JoinModeCode {
		given := normalizeConfirm(confirm)
		if given == "" || hashCode(given) != s.confirmHash {
			if s.confirmFail++; s.confirmFail >= maxJoinConfirmFails {
				j.compromise(s)
			}
			return JoinDecision{}, ErrJoinConfirm
		}
		if err := j.device.DecideJoin(s.deviceHash, account, true); err != nil {
			return JoinDecision{}, ErrJoinNotReady
		}
		s.state = JoinApproved
		return JoinDecision{}, nil
	}
	code, err := randomHex(32)
	if err != nil {
		return JoinDecision{}, err
	}
	s.authHash, s.authExpires, s.state = hashCode(code), now.Add(joinAuthTTL), JoinApproved
	j.byAuth[s.authHash] = s
	s.extend(s.authExpires.Add(time.Minute))
	return JoinDecision{Redirect: s.callback(url.Values{"code": {code}})}, nil
}

// normalizeConfirm macht aus der Eingabe die kanonische Form des Bestätigungscodes
// (vier Zeichen des User-Code-Alphabets) oder "".
func normalizeConfirm(in string) string {
	in = strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(in)))
	if len(in) != joinConfirmLen {
		return ""
	}
	for _, c := range in {
		if !strings.ContainsRune(UserCodeAlphabet, c) {
			return ""
		}
	}
	return in
}

// Exchange tauscht den Autorisierungscode, den der Browser auf den Loopback
// brachte, gegen die Berechtigung zum Token. Der Code gilt für genau einen
// Versuch; ohne den passenden code_verifier, nach Ablauf oder bei unbekanntem
// Code gibt es immer ErrJoinInvalid. Der Aufrufer stellt das Token aus und meldet
// es mit Delivered.
func (j *JoinSessions) Exchange(addr, code, verifier string) (JoinGrant, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := j.now()
	j.purge(now)
	if j.lockedOut(addr, now) {
		return JoinGrant{}, ErrJoinLocked
	}
	s := j.byAuth[hashCode(code)]
	if s != nil {
		delete(j.byAuth, s.authHash)
		s.authHash = ""
	}
	sum := sha256.Sum256([]byte(verifier))
	ok := s != nil && now.Before(s.authExpires) && s.state == JoinApproved && len(verifier) >= 43 && len(verifier) <= 128 && allBytes(verifier, stateChars) &&
		subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(s.challenge)) == 1
	if !ok {
		j.fail(addr, now)
		return JoinGrant{}, ErrJoinInvalid
	}
	return JoinGrant{Account: s.account, Machine: s.machine, sid: s.sid}, nil
}

// Delivered meldet, dass das Token ausgestellt ist (Loopback-Weg).
func (j *JoinSessions) Delivered(g JoinGrant) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if s := j.all[g.sid]; s != nil {
		j.connected(s)
	}
}

// DeliveredDevice meldet es für den Code-Weg; ein device_code ohne Join-Sitzung
// ist ein gewöhnlicher Geräte-Login und bleibt ohne Wirkung.
func (j *JoinSessions) DeliveredDevice(deviceCode string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	h := hashCode(deviceCode)
	for _, s := range j.all {
		if s.deviceHash == h {
			j.connected(s)
		}
	}
}

func (j *JoinSessions) connected(s *joinSession) {
	s.state = JoinConnected
	delete(j.byPair, hashCode(s.pair))
	s.extend(j.now().Add(time.Minute))
}

// Cancelled meldet, dass der Installer das gerade ausgestellte Token selbst
// widerrufen hat (im Terminal abgelehnt oder die Konfiguration ließ sich nicht
// schreiben). Die Seite sagt dann, dass nichts verbunden wurde, statt
// "verbunden" stehen zu lassen. Nur eine soeben verbundene Sitzung derselben
// Maschine ändert sich.
func (j *JoinSessions) Cancelled(account, machine string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := j.byAccount[account]
	if s != nil && s.state == JoinConnected && strings.EqualFold(s.machine, machine) {
		s.state = JoinDenied
	}
}

// Sessions zählt die Sitzungen; für Tests.
func (j *JoinSessions) Sessions() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.purge(j.now())
	return len(j.all)
}
