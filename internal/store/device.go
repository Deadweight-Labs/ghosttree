package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
	"strings"
	"sync"
	"time"
)

// Grenzen des Geräte-Logins. Der Ablauf liegt im Speicher des Servers, nicht
// in der Datenbank: ein unangemeldeter Client kann ihn anstoßen, und ein Start
// soll weder Plattenplatz noch unbegrenzt Speicher belegen. Ein Neustart
// verwirft laufende Abläufe; der Nutzer führt `ctx login` dann erneut aus.
const (
	DeviceFlowTTL      = 10 * time.Minute
	DeviceInterval     = 5 * time.Second
	deviceIntervalStep = 5 * time.Second

	maxDeviceFlows     = 1000 // offene Abläufe insgesamt
	maxDevicePerClient = 5    // offene Abläufe je Absenderadresse

	maxDeviceFailures       = 5   // falsche Codes je Konto im Fenster
	maxDeviceFailuresGlobal = 100 // falsche Codes aller Konten im Fenster
	deviceFailureWindow     = 10 * time.Minute

	// UserCodeAlphabet hat keine Vokale (keine Wörter) und lässt verwechselbare
	// Zeichen (0/O, 1/I/L, 2/Z, 5/S, 8/B) weg, soweit sie nicht eindeutig
	// bleiben; 22^8 Möglichkeiten.
	UserCodeAlphabet = "BCDFGHJKMNPQRTVWX34679"
	userCodeLen      = 8
)

var (
	// ErrDeviceBusy: zu viele offene Abläufe von dieser Adresse oder insgesamt.
	ErrDeviceBusy = errors.New("too many device logins in progress")
	// ErrDeviceUnknown deckt unbekannt, abgelaufen und schon entschieden ab.
	ErrDeviceUnknown = errors.New("device code is invalid or expired")
	// ErrDeviceLocked: zu viele falsche User-Codes; das Konto ist kurz gesperrt.
	ErrDeviceLocked = errors.New("too many wrong codes, try again later")

	ErrDevicePending  = errors.New("authorization pending")
	ErrDeviceSlowDown = errors.New("slow down")
	ErrDeviceDenied   = errors.New("access denied")
)

// DeviceStart ist, was der Start dem Client mitgibt. Die Klartext-Codes
// existieren nur hier.
type DeviceStart struct {
	DeviceCode, UserCode string
	ExpiresIn, Interval  time.Duration
}

// DeviceRequest beschreibt einen offenen Ablauf für die Bestätigungsseite.
type DeviceRequest struct {
	Machine, Remote string
	StartedAt       time.Time
}

// DeviceApproval ist das Ergebnis eines bestätigten Ablaufs; daraus stellt der
// Aufrufer das Token aus (CreateDeviceToken).
type DeviceApproval struct {
	AccountID, Machine string
}

type deviceFlow struct {
	deviceHash, userHash string
	client, machine      string
	remote               string
	created, expires     time.Time
	lastPoll             time.Time
	interval             time.Duration
	state                string // pending, approved, denied
	account              string
	join                 bool // Join-Paarung: ohne User-Code, nur über deviceHash ansprechbar
}

// DeviceFlows hält die offenen Geräte-Abläufe.
type DeviceFlows struct {
	mu        sync.Mutex
	now       func() time.Time
	byDevice  map[string]*deviceFlow
	byUser    map[string]*deviceFlow
	perClient map[string]int
	failures  map[string][]time.Time
	allFails  []time.Time
}

func NewDeviceFlows() *DeviceFlows {
	return &DeviceFlows{now: time.Now, byDevice: map[string]*deviceFlow{}, byUser: map[string]*deviceFlow{},
		perClient: map[string]int{}, failures: map[string][]time.Time{}}
}

// Device gibt die Abläufe dieses Stores zurück.
func (s *Store) Device() *DeviceFlows {
	s.deviceOnce.Do(func() { s.device = NewDeviceFlows() })
	return s.device
}

// SetClock ersetzt die Uhr; für Tests.
func (d *DeviceFlows) SetClock(now func() time.Time) {
	d.mu.Lock()
	d.now = now
	d.mu.Unlock()
}

func hashCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// NormalizeUserCode macht aus der Eingabe die kanonische Form oder "".
func NormalizeUserCode(in string) string {
	in = strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(in)))
	if len(in) != userCodeLen {
		return ""
	}
	for _, c := range in {
		if !strings.ContainsRune(UserCodeAlphabet, c) {
			return ""
		}
	}
	return in
}

// FormatUserCode gibt XXXX-XXXX zurück.
func FormatUserCode(code string) string {
	if len(code) != userCodeLen {
		return code
	}
	return code[:4] + "-" + code[4:]
}

func newUserCode() (string, error) {
	max := big.NewInt(int64(len(UserCodeAlphabet)))
	out := make([]byte, userCodeLen)
	for i := range out {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		out[i] = UserCodeAlphabet[n.Int64()]
	}
	return string(out), nil
}

// purge entfernt abgelaufene Abläufe. Der Aufrufer hält d.mu.
func (d *DeviceFlows) purge(now time.Time) {
	for _, f := range d.byDevice {
		if !now.Before(f.expires) {
			d.remove(f)
		}
	}
}

func (d *DeviceFlows) remove(f *deviceFlow) {
	delete(d.byDevice, f.deviceHash)
	if !f.join {
		delete(d.byUser, f.userHash)
	}
	if d.perClient[f.client]--; d.perClient[f.client] <= 0 {
		delete(d.perClient, f.client)
	}
}

// Start legt einen Ablauf an. client ist die Absenderadresse; sie begrenzt, wie
// viele Abläufe ein einzelner unangemeldeter Absender offen halten kann. Ist
// der Speicher insgesamt voll, verdrängt der Start den ältesten Ablauf des
// Absenders mit den meisten offenen Abläufen, nie den eines Absenders mit
// weniger als der Start selbst; ein einzelner Absender kann so fremde Abläufe
// nicht verdrängen.
func (d *DeviceFlows) Start(client, machine, remote string) (DeviceStart, error) {
	return d.start(client, machine, remote, false)
}

// StartJoin legt einen Ablauf für die Join-Paarung an. Er hat keinen User-Code,
// ist also über /ui/device weder zu finden noch zu entscheiden; die Join-Sitzung
// spricht ihn über den Hash des device_code an.
func (d *DeviceFlows) StartJoin(client, machine, remote string) (DeviceStart, error) {
	return d.start(client, machine, remote, true)
}

func (d *DeviceFlows) start(client, machine, remote string, join bool) (DeviceStart, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	d.purge(now)
	if d.perClient[client] >= maxDevicePerClient {
		return DeviceStart{}, ErrDeviceBusy
	}
	if len(d.byDevice) >= maxDeviceFlows {
		heaviest, most := "", 0
		for c, n := range d.perClient {
			if n > most {
				heaviest, most = c, n
			}
		}
		if heaviest == client || most <= d.perClient[client] {
			return DeviceStart{}, ErrDeviceBusy
		}
		var oldest *deviceFlow
		for _, f := range d.byDevice {
			if f.client == heaviest && (oldest == nil || f.created.Before(oldest.created)) {
				oldest = f
			}
		}
		if oldest != nil {
			d.remove(oldest)
		}
	}
	rawDevice := make([]byte, 32)
	if _, err := rand.Read(rawDevice); err != nil {
		return DeviceStart{}, err
	}
	deviceCode := hex.EncodeToString(rawDevice)
	var userCode, userHash string
	for tries := 0; !join; tries++ {
		c, err := newUserCode()
		if err != nil {
			return DeviceStart{}, err
		}
		if _, taken := d.byUser[hashCode(c)]; !taken {
			userCode, userHash = c, hashCode(c)
			break
		}
		if tries > 20 {
			return DeviceStart{}, ErrDeviceBusy
		}
	}
	f := &deviceFlow{deviceHash: hashCode(deviceCode), userHash: userHash, client: client, machine: machine, remote: remote,
		created: now, expires: now.Add(DeviceFlowTTL), lastPoll: now, interval: DeviceInterval, state: "pending"}
	f.join = join
	d.byDevice[f.deviceHash] = f
	if !join {
		d.byUser[userHash] = f
	}
	d.perClient[client]++
	return DeviceStart{DeviceCode: deviceCode, UserCode: userCode, ExpiresIn: DeviceFlowTTL, Interval: DeviceInterval}, nil
}

// Poll beantwortet die Abfrage des Clients. Ein bestätigter oder abgelehnter
// Ablauf wird dabei verbraucht: das Token gibt es genau einmal. Fragt der
// Client schneller als das Intervall, erhält er ErrDeviceSlowDown und das neue,
// größere Intervall.
func (d *DeviceFlows) Poll(deviceCode string) (DeviceApproval, time.Duration, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	d.purge(now)
	f := d.byDevice[hashCode(deviceCode)]
	if f == nil {
		return DeviceApproval{}, 0, ErrDeviceUnknown
	}
	if now.Sub(f.lastPoll) < f.interval {
		f.interval += deviceIntervalStep
		f.lastPoll = now
		return DeviceApproval{}, f.interval, ErrDeviceSlowDown
	}
	f.lastPoll = now
	switch f.state {
	case "approved":
		d.remove(f)
		return DeviceApproval{AccountID: f.account, Machine: f.machine}, f.interval, nil
	case "denied":
		d.remove(f)
		return DeviceApproval{}, f.interval, ErrDeviceDenied
	}
	return DeviceApproval{}, f.interval, ErrDevicePending
}

// locked sagt, ob das Konto oder die Instanz im Fenster zu viele falsche Codes
// hatte. Der Aufrufer hält d.mu.
func (d *DeviceFlows) locked(account string, now time.Time) bool {
	cutoff := now.Add(-deviceFailureWindow)
	d.failures[account] = trimBefore(d.failures[account], cutoff)
	if len(d.failures[account]) == 0 {
		delete(d.failures, account)
	}
	d.allFails = trimBefore(d.allFails, cutoff)
	return len(d.failures[account]) >= maxDeviceFailures || len(d.allFails) >= maxDeviceFailuresGlobal
}

func trimBefore(in []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(in) && in[i].Before(cutoff) {
		i++
	}
	return in[i:]
}

// find sucht den offenen Ablauf zu einem User-Code und zählt jeden Fehlschlag
// gegen das Konto. Der Aufrufer hält d.mu.
func (d *DeviceFlows) find(userCode, account string) (*deviceFlow, error) {
	now := d.now()
	d.purge(now)
	if d.locked(account, now) {
		return nil, ErrDeviceLocked
	}
	var f *deviceFlow
	if code := NormalizeUserCode(userCode); code != "" {
		f = d.byUser[hashCode(code)]
	}
	if f == nil || f.state != "pending" {
		d.failures[account] = append(d.failures[account], now)
		d.allFails = append(d.allFails, now)
		return nil, ErrDeviceUnknown
	}
	return f, nil
}

// Lookup liefert den offenen Ablauf zu einem User-Code, ohne ihn zu entscheiden.
func (d *DeviceFlows) Lookup(userCode, account string) (DeviceRequest, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f, err := d.find(userCode, account)
	if err != nil {
		return DeviceRequest{}, err
	}
	return DeviceRequest{Machine: f.machine, Remote: f.remote, StartedAt: f.created}, nil
}

// Decide bestätigt oder verweigert den Ablauf im Namen des Kontos.
func (d *DeviceFlows) Decide(userCode, account string, approve bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	f, err := d.find(userCode, account)
	if err != nil {
		return err
	}
	if approve {
		f.state, f.account = "approved", account
	} else {
		f.state = "denied"
	}
	return nil
}

// Open zählt die offenen Abläufe; für Tests und Metriken.
func (d *DeviceFlows) Open() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.purge(d.now())
	return len(d.byDevice)
}

// Busy sagt, ob ein Start von dieser Adresse an einer Grenze scheitern würde.
// Die Join-Paarung fragt das vor der Prüfung des Paarungscodes, damit "zu viele
// offene Abläufe" für gültige und ungültige Codes gleich ausfällt.
func (d *DeviceFlows) Busy(client string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.purge(d.now())
	return d.perClient[client] >= maxDevicePerClient || len(d.byDevice) >= maxDeviceFlows
}

// DeviceHash ist die Kennung, unter der eine Join-Sitzung ihren Ablauf führt.
func DeviceHash(deviceCode string) string { return hashCode(deviceCode) }

// DropJoin verwirft den Join-Ablauf mit diesem Hash, falls es ihn noch gibt.
func (d *DeviceFlows) DropJoin(deviceHash string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if f := d.byDevice[deviceHash]; f != nil && f.join {
		d.remove(f)
	}
}

// JoinStatus nennt den Zustand eines Join-Ablaufs (pending, approved, denied)
// oder "", wenn er abgeholt wurde oder abgelaufen ist.
func (d *DeviceFlows) JoinStatus(deviceHash string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.purge(d.now())
	if f := d.byDevice[deviceHash]; f != nil && f.join {
		return f.state
	}
	return ""
}

// DecideJoin entscheidet einen Join-Ablauf im Namen des Kontos. Die Join-Sitzung
// hat den Ablauf selbst erzeugt, also zählt hier kein Fehlversuch.
func (d *DeviceFlows) DecideJoin(deviceHash, account string, approve bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.purge(d.now())
	f := d.byDevice[deviceHash]
	if f == nil || !f.join || f.state != "pending" {
		return ErrDeviceUnknown
	}
	if approve {
		f.state, f.account = "approved", account
	} else {
		f.state = "denied"
	}
	return nil
}
