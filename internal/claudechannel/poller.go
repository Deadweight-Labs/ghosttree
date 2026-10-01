package claudechannel

import (
	"context"
	"errors"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/hookbudget"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Takt des Pollers. Nach Aktivität gilt MinInterval; im Leerlauf verdoppelt
// sich die Wartezeit bis MaxInterval. Die coord-SSE-Route ist an
// Browser-Cookies gebunden und liefert keinen Inhalt, deshalb Polling.
const (
	MinInterval = 2 * time.Second
	MaxInterval = 10 * time.Second
	inboxLimit  = 100
)

// Source ist alles, was der Poller vom Server braucht. Ein Interface, damit der
// Poller ohne HTTP prüfbar ist; ClientSource ist die echte Seite.
type Source interface {
	// Rooms nennt die Räume, die beobachtet werden.
	Rooms(self string) ([]store.CoordRoom, error)
	Inbox(self string, room store.CoordRoom, after int64, limit int) ([]store.CoordMessage, error)
	Mentions(self string, messageID int64) ([]string, error)
	// Claim ist atomar: je Nachricht und Empfänger bekommt genau ein Aufrufer true.
	Claim(self string, messageID int64) (bool, error)
	Cursor(self string, room store.CoordRoom) (int64, error)
	SetCursor(self string, room store.CoordRoom, id int64) error
}

// Budget begrenzt die Zustellung je Session. Die echte Seite ist
// hookbudget.DeliverChannel auf dem Koordinationskanal.
type Budget interface {
	// Exhausted sagt, ob gerade nichts mehr durchgeht. Der Poller claimt dann
	// nicht: eine geclaimte, aber verschluckte Nachricht wäre auch im Pull-Pfad
	// verborgen.
	Exhausted(session string) bool
	Deliver(session, text string, emit func(string) error) error
}

type hookBudget struct{}

func (hookBudget) Exhausted(session string) bool {
	r, err := hookbudget.ChannelUsage(session, hookbudget.ChannelCoord)
	if err != nil || !r.Exhausted {
		return false
	}
	// Das rollende Fenster erholt sich; ein altes Konto sperrt nicht mehr.
	return time.Since(r.StartedAt) <= hookbudget.ChannelWindow(hookbudget.ChannelCoord)
}

func (hookBudget) Deliver(session, text string, emit func(string) error) error {
	return hookbudget.DeliverChannel(session, hookbudget.ChannelCoord, text, emit)
}

// Poller holt neue Nachrichten und stellt sie über Notifier zu.
type Poller struct {
	Self     string
	Source   Source
	Notifier Notifier
	// Budget ist optional; ohne Angabe gilt hookbudget.DeliverChannel.
	Budget Budget
	// Now und Sleep sind für Tests austauschbar. Sleep wartet d oder bis ctx
	// endet und gibt dann ctx.Err() zurück.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
	// OnError sieht Fehler, die einen Durchlauf abbrechen. Der Poller läuft
	// danach mit Backoff weiter.
	OnError func(error)

	pos map[string]int64 // Abrufstand je Raum, unabhängig vom gemeinsamen Cursor
}

func (p *Poller) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Poller) budget() Budget {
	if p.Budget != nil {
		return p.Budget
	}
	return hookBudget{}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Run pollt bis ctx endet.
func (p *Poller) Run(ctx context.Context) error {
	sleep := p.Sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	wait := MinInterval
	for {
		active, err := p.Poll(ctx)
		if err != nil && p.OnError != nil && ctx.Err() == nil {
			p.OnError(err)
		}
		if active {
			wait = MinInterval
		}
		if err := sleep(ctx, wait); err != nil {
			return err
		}
		if !active {
			if wait *= 2; wait > MaxInterval {
				wait = MaxInterval
			}
		}
	}
}

// Poll macht einen Durchlauf über alle Räume. active ist true, wenn neue
// Nachrichten gesehen wurden, auch ungeweckte: Backoff gilt dem Leerlauf.
func (p *Poller) Poll(ctx context.Context) (active bool, err error) {
	if p.pos == nil {
		p.pos = map[string]int64{}
	}
	rooms, err := p.Source.Rooms(p.Self)
	if err != nil {
		return false, err
	}
	var firstErr error
	for _, room := range rooms {
		if ctx.Err() != nil {
			return active, ctx.Err()
		}
		seen, err := p.pollRoom(ctx, room)
		active = active || seen
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return active, firstErr
}

func (p *Poller) pollRoom(ctx context.Context, room store.CoordRoom) (bool, error) {
	cursor, err := p.Source.Cursor(p.Self, room)
	if err != nil {
		return false, err
	}
	pos, known := p.pos[room.Key]
	if !known {
		pos = cursor
	}
	msgs, err := p.Source.Inbox(p.Self, room, pos, inboxLimit)
	if err != nil {
		return false, err
	}
	// contiguous: bis hierher ist alles zugestellt oder eigene Post. Nur dann
	// darf der gemeinsame Cursor vorrücken, sonst verschwänden ungeweckte
	// Nachrichten aus coord_inbox.
	contiguous := true
	for _, m := range msgs {
		if m.ID <= pos {
			continue
		}
		delivered, err := p.handle(ctx, room, m)
		if errors.Is(err, errBudget) {
			return true, nil // erschöpftes Budget ist kein Fehler; die Post bleibt im Pull-Pfad lesbar
		}
		if err != nil {
			return true, err
		}
		pos = m.ID
		p.pos[room.Key] = pos
		switch {
		case delivered:
			if contiguous && m.ID > cursor {
				if err := p.Source.SetCursor(p.Self, room, m.ID); err != nil {
					return true, err
				}
				cursor = m.ID
			}
		case isOwn(p.Self, m):
		default:
			contiguous = false
		}
	}
	return len(msgs) > 0, nil
}

// handle entscheidet über eine Nachricht. Reihenfolge: Claim, dann
// Notification. Ein Fehler VOR dem Claim lässt die Nachricht für den nächsten
// Durchlauf stehen; nach dem Claim ist sie at-most-once (siehe doc.go).
func (p *Poller) handle(ctx context.Context, room store.CoordRoom, m store.CoordMessage) (bool, error) {
	now := p.now()
	if !wakeCandidate(p.Self, room.Kind, m, now) {
		return false, nil
	}
	if needsMentions(room.Kind) {
		mentions, err := p.Source.Mentions(p.Self, m.ID)
		if err != nil {
			return false, err
		}
		if !ShouldWake(p.Self, room.Kind, m, mentions, now) {
			return false, nil
		}
	}
	if p.budget().Exhausted(p.Self) {
		return false, errBudget
	}
	won, err := p.Source.Claim(p.Self, m.ID)
	if err != nil {
		return false, err
	}
	if !won {
		return false, nil // ein anderer Poller hat sie; nie erneut zustellen
	}
	sent := false
	err = p.budget().Deliver(p.Self, m.Body, func(text string) error {
		if text == "" {
			return nil // Budget erschöpft; nichts geht raus
		}
		if err := p.Notifier.Notify(ctx, NewNotification(room, m, text)); err != nil {
			return err
		}
		sent = true
		return nil
	})
	return sent, err
}

type budgetError struct{}

func (budgetError) Error() string { return "claude channel: coordination budget exhausted" }

var errBudget error = budgetError{}

// ClientSource ist die Source gegen den ghosttree-Server. Extra nennt Räume,
// die der Server nicht je Teilnehmer auflistet, also den Projekt- und
// Maschinenraum; Direkt- und Gruppenräume kommen von CoordRoomsFor.
type ClientSource struct {
	Client *client.Client
	Extra  []store.CoordRoom
}

var _ Source = ClientSource{}

func (s ClientSource) Rooms(self string) ([]store.CoordRoom, error) {
	rooms, err := s.Client.CoordRoomsFor(self)
	if err != nil {
		return nil, err
	}
	return append(append([]store.CoordRoom{}, s.Extra...), rooms...), nil
}

func (s ClientSource) Inbox(self string, room store.CoordRoom, after int64, limit int) ([]store.CoordMessage, error) {
	return s.Client.CoordInbox(store.DestinationRoom, room.Key, self, after, limit)
}

func (s ClientSource) Mentions(self string, id int64) ([]string, error) {
	return s.Client.CoordMessageMentions(id, self)
}

func (s ClientSource) Claim(self string, id int64) (bool, error) {
	return s.Client.ClaimCoordDelivery(id, self)
}

func (s ClientSource) Cursor(self string, room store.CoordRoom) (int64, error) {
	return s.Client.CoordCursor(self, store.DestinationRoom, room.Key)
}

func (s ClientSource) SetCursor(self string, room store.CoordRoom, id int64) error {
	return s.Client.SetCoordCursor(self, store.DestinationRoom, room.Key, id)
}
