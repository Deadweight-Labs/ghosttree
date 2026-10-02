package store

import (
	"errors"
	"sync"
	"time"
)

// Join-Sitzungen (REQ-434, Paket P2): der Treffpunkt von Browser und Installer.
//
// Eine Sitzung gehört einem Konto, das die Einladung angenommen hat oder
// angemeldet ist, und trägt einen kurzen Paarungscode. Der Installer meldet sich
// damit (Claim); daraus entsteht ein gewöhnlicher Geräte-Ablauf, dessen
// device_code nur der Installer erhält. Das Token entsteht erst, wenn dasselbe
// Konto im Browser freigibt, und wird vom bestehenden Poll genau einmal
// ausgeliefert.
//
// Der Zustand liegt im Speicher, wie bei DeviceFlows: nach einem Neustart sind
// Sitzungen weg, und jeder alte Paarungscode wird wie ein unbekannter behandelt.
// Der Einladungscode kommt hier nie vor.
const (
	JoinSessionTTL = 10 * time.Minute

	maxJoinSessions     = 1000
	maxJoinSessionTries = 3 // Treffer auf einen Code, danach ist er tot
	maxJoinFailures     = 8 // falsche Codes je Adresse im Fenster
	joinFailureWindow   = 10 * time.Minute
	maxJoinFailureKeys  = 10000
)

// Zustände einer Sitzung, wie die Seite sie sieht.
const (
	JoinNone      = "none"      // keine (mehr)
	JoinWaiting   = "waiting"   // Code ausgegeben, noch kein Gerät
	JoinClaimed   = "claimed"   // Gerät hat sich gemeldet, Freigabe offen
	JoinApproved  = "approved"  // freigegeben, Token noch nicht abgeholt
	JoinDenied    = "denied"    // abgelehnt
	JoinConnected = "connected" // Token abgeholt
)

var (
	// ErrJoinInvalid deckt unbekannt, abgelaufen, schon beansprucht und
	// fehlerhaft geformt ab. Es ist immer dieselbe Antwort.
	ErrJoinInvalid = errors.New("pairing code is invalid or expired")
	// ErrJoinLocked: von dieser Adresse kamen zu viele falsche Codes.
	ErrJoinLocked = errors.New("too many wrong pairing codes, try again later")
	// ErrJoinNotReady: es gibt nichts freizugeben (keine Sitzung, kein Gerät).
	ErrJoinNotReady = errors.New("no device is waiting")
)

type joinSession struct {
	account   string
	pair      string
	created   time.Time
	expires   time.Time
	tries     int
	conflicts int
	claimed   bool
	userCode  string // Code des Geräte-Ablaufs; verlässt den Server nie
	machine   string
	remote    string
	state     string
}

// JoinView ist, was die Seite des Kontos über seine Sitzung erfährt.
type JoinView struct {
	State     string
	Pair      string // XXXX-XXXX; leer ohne Sitzung
	Machine   string
	Remote    string
	Conflicts int
}

// JoinClaim ist die Antwort an den Installer.
type JoinClaim struct {
	DeviceCode          string
	ExpiresIn, Interval time.Duration
}

// JoinSessions hält die offenen Sitzungen.
type JoinSessions struct {
	mu        sync.Mutex
	now       func() time.Time
	device    *DeviceFlows
	byAccount map[string]*joinSession
	byPair    map[string]*joinSession
	failures  map[string][]time.Time
}

func NewJoinSessions(d *DeviceFlows) *JoinSessions {
	return &JoinSessions{now: time.Now, device: d, byAccount: map[string]*joinSession{},
		byPair: map[string]*joinSession{}, failures: map[string][]time.Time{}}
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

func (j *JoinSessions) drop(sess *joinSession) {
	delete(j.byAccount, sess.account)
	delete(j.byPair, hashCode(sess.pair))
	if sess.claimed {
		j.device.Drop(sess.userCode)
	}
}

func (j *JoinSessions) purge(now time.Time) {
	for _, sess := range j.byAccount {
		if !now.Before(sess.expires) {
			j.drop(sess)
		}
	}
}

// Create legt für das Konto eine neue Sitzung an und gibt den Paarungscode
// (XXXX-XXXX) zurück. Eine bestehende Sitzung des Kontos endet damit, samt dem
// Gerät, das sich darauf gemeldet hatte.
func (j *JoinSessions) Create(account string) (string, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := j.now()
	j.purge(now)
	if old := j.byAccount[account]; old != nil {
		j.drop(old)
	}
	if len(j.byAccount) >= maxJoinSessions {
		var oldest *joinSession
		for _, sess := range j.byAccount {
			if oldest == nil || sess.created.Before(oldest.created) {
				oldest = sess
			}
		}
		j.drop(oldest)
	}
	var code string
	for tries := 0; ; tries++ {
		c, err := newUserCode()
		if err != nil {
			return "", err
		}
		if _, taken := j.byPair[hashCode(c)]; !taken {
			code = c
			break
		}
		if tries > 20 {
			return "", ErrDeviceBusy
		}
	}
	sess := &joinSession{account: account, pair: code, created: now, expires: now.Add(JoinSessionTTL), state: JoinWaiting}
	j.byAccount[account] = sess
	j.byPair[hashCode(code)] = sess
	return FormatUserCode(code), nil
}

func (j *JoinSessions) fail(client string, now time.Time) {
	if _, ok := j.failures[client]; !ok && len(j.failures) >= maxJoinFailureKeys {
		for k, v := range j.failures {
			if len(trimBefore(v, now.Add(-joinFailureWindow))) == 0 {
				delete(j.failures, k)
			}
		}
		if len(j.failures) >= maxJoinFailureKeys {
			for k := range j.failures {
				delete(j.failures, k)
				break
			}
		}
	}
	j.failures[client] = append(j.failures[client], now)
}

// Claim meldet ein Gerät mit dem Paarungscode an. Fehler: ErrJoinLocked (zu
// viele falsche Codes von dieser Adresse), ErrDeviceBusy (zu viele offene
// Geräte-Abläufe), sonst für jeden Grund ErrJoinInvalid. Sperre und Auslastung
// werden vor der Prüfung des Codes beantwortet, damit sie nichts über Codes
// verraten.
func (j *JoinSessions) Claim(client, pair, machine, remote string) (JoinClaim, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := j.now()
	j.purge(now)
	cutoff := now.Add(-joinFailureWindow)
	j.failures[client] = trimBefore(j.failures[client], cutoff)
	if len(j.failures[client]) == 0 {
		delete(j.failures, client)
	}
	if len(j.failures[client]) >= maxJoinFailures {
		return JoinClaim{}, ErrJoinLocked
	}
	if j.device.Busy(client) {
		return JoinClaim{}, ErrDeviceBusy
	}
	var sess *joinSession
	if code := NormalizeUserCode(pair); code != "" {
		sess = j.byPair[hashCode(code)]
	}
	if sess == nil {
		j.fail(client, now)
		return JoinClaim{}, ErrJoinInvalid
	}
	sess.tries++
	if sess.tries >= maxJoinSessionTries {
		delete(j.byPair, hashCode(sess.pair))
	}
	if sess.claimed {
		sess.conflicts++
		j.fail(client, now)
		return JoinClaim{}, ErrJoinInvalid
	}
	start, err := j.device.Start(client, machine, remote)
	if err != nil {
		return JoinClaim{}, err
	}
	sess.claimed, sess.userCode, sess.machine, sess.remote, sess.state = true, start.UserCode, machine, remote, JoinClaimed
	if end := now.Add(start.ExpiresIn); end.After(sess.expires) {
		sess.expires = end
	}
	return JoinClaim{DeviceCode: start.DeviceCode, ExpiresIn: start.ExpiresIn, Interval: start.Interval}, nil
}

// View beschreibt die Sitzung des Kontos.
func (j *JoinSessions) View(account string) JoinView {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.purge(j.now())
	sess := j.byAccount[account]
	if sess == nil {
		return JoinView{State: JoinNone}
	}
	state := sess.state
	if state == JoinClaimed || state == JoinApproved {
		switch status := j.device.Status(sess.userCode); {
		case status == "" && state == JoinApproved:
			state = JoinConnected
		case status == "":
			state = JoinNone // der Geräte-Ablauf ist weg
		}
	}
	return JoinView{State: state, Pair: FormatUserCode(sess.pair), Machine: sess.machine, Remote: sess.remote, Conflicts: sess.conflicts}
}

// Decide bestätigt oder verweigert das gemeldete Gerät im Namen des Kontos.
func (j *JoinSessions) Decide(account string, approve bool) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.purge(j.now())
	sess := j.byAccount[account]
	if sess == nil || sess.state != JoinClaimed {
		return ErrJoinNotReady
	}
	if err := j.device.DecideFlow(sess.userCode, account, approve); err != nil {
		return ErrJoinNotReady
	}
	if approve {
		sess.state = JoinApproved
	} else {
		sess.state = JoinDenied
		delete(j.byPair, hashCode(sess.pair))
	}
	return nil
}

// Open zählt die Sitzungen; für Tests.
func (j *JoinSessions) Open() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.purge(j.now())
	return len(j.byAccount)
}
