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
	// ClaimTimeout begrenzt den Claim. Er läuft unter dem Datei-Lock des
	// Budgets; ohne eigene Frist hielte ein hängender Server den Lock bis zum
	// Client-Timeout.
	ClaimTimeout = 5 * time.Second
)

// Source ist alles, was der Poller vom Server braucht. Ein Interface, damit der
// Poller ohne HTTP prüfbar ist; ClientSource ist die echte Seite.
type Source interface {
	// Rooms nennt die Räume, die beobachtet werden.
	Rooms(self string) ([]store.CoordRoom, error)
	Inbox(self string, room store.CoordRoom, after int64, limit int) ([]store.CoordMessage, error)
	Mentions(self string, messageID int64) ([]string, error)
	// Message lädt eine Nachricht des Raums. ok ist false, wenn es sie dort
	// nicht gibt.
	Message(self string, room store.CoordRoom, id int64) (m store.CoordMessage, ok bool, err error)
	// Claim ist atomar: je Nachricht und Empfänger bekommt genau ein Aufrufer true.
	Claim(ctx context.Context, self string, messageID int64) (bool, error)
	Cursor(self string, room store.CoordRoom) (int64, error)
	SetCursor(self string, room store.CoordRoom, id int64) error
}

// Budget begrenzt die Zustellung je Session. Die echte Seite ist
// hookbudget.DeliverChannel auf dem Koordinationskanal.
//
// Deliver reserviert das Budget UND ruft emit, in einem Schritt. Der Poller
// claimt deshalb erst innerhalb von emit: ist das Budget erschöpft (emit("")),
// oder scheitert das Lesen des Kontos (emit wird nie gerufen), ist nichts
// geclaimt und nichts verloren.
type Budget interface {
	Deliver(session, text string, emit func(string) error) error
}

type hookBudget struct{}

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
	// ClaimTimeout überschreibt die Frist des Claims; null heißt ClaimTimeout.
	ClaimTimeout time.Duration
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
	if !p.Notifier.Ready() {
		return false, nil
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

// pollRoom gibt zurück, ob etwas vorangekommen ist (Nachricht zugestellt).
// Ungeweckter Verkehr, Fehler und Stillstand zählen nicht: sie gehen in den
// Backoff, statt im Takt von 2 s denselben Fehler zu wiederholen.
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
	progress := false
	// contiguous: bis hierher ist alles zugestellt oder eigene Post. Nur dann
	// darf der gemeinsame Cursor vorrücken, sonst verschwänden ungeweckte
	// Nachrichten aus coord_inbox.
	contiguous := true
	for _, m := range msgs {
		if m.ID <= pos {
			continue
		}
		delivered, err := p.handle(ctx, room, m)
		if errors.Is(err, errStalled) {
			return progress, nil // Position bleibt stehen, nichts ist geclaimt
		}
		if err != nil {
			return progress, err
		}
		pos = m.ID
		p.pos[room.Key] = pos
		progress = progress || delivered
		switch {
		case delivered:
			if contiguous && m.ID > cursor {
				if err := p.Source.SetCursor(p.Self, room, m.ID); err != nil {
					return progress, err
				}
				cursor = m.ID
			}
		case isOwn(p.Self, m):
		default:
			contiguous = false
		}
	}
	return progress, nil
}

// handle entscheidet über eine Nachricht. Geclaimt wird erst, wenn die
// Zustellung gesichert ist: Notifier bereit und Budget reserviert. Danach
// bleibt als einziges ein echter Write-Fehler, und der ist at-most-once (siehe
// doc.go). errStalled heißt: nichts geclaimt, später erneut versuchen.
func (p *Poller) handle(ctx context.Context, room store.CoordRoom, m store.CoordMessage) (bool, error) {
	now := p.now()
	if !wakeCandidate(p.Self, room.Kind, m, now) {
		return false, nil
	}
	parentKind := ParentNotOwn
	if m.ReplyTo != 0 && !attentionIntent(m) && m.AuthorKind != store.AuthorHuman {
		parent, ok, err := p.Source.Message(p.Self, room, m.ReplyTo)
		if err != nil {
			return false, err // fail-closed: ohne Antwort kein Claim
		}
		if ok && isOwn(p.Self, parent) {
			var parentMentions []string
			// Erwähnungen nur, wenn sie die Entscheidung tragen.
			if !attentionIntent(parent) && parent.ReplyTo == 0 && needsMentions(room.Kind) {
				if parentMentions, err = p.Source.Mentions(p.Self, parent.ID); err != nil {
					return false, err
				}
			}
			parentKind = ClassifyParent(p.Self, room.Kind, parent, m.SenderExternalID, parentMentions)
		}
	}
	var mentions []string
	if needsMentions(room.Kind) {
		var err error
		if mentions, err = p.Source.Mentions(p.Self, m.ID); err != nil {
			return false, err
		}
	}
	if !ShouldWake(p.Self, room.Kind, m, mentions, parentKind, now) {
		return false, nil
	}
	if !p.Notifier.Ready() {
		return false, errStalled
	}
	sent, lost, stalled := false, false, false
	err := p.budget().Deliver(p.Self, m.Body, func(text string) error {
		if text == "" {
			stalled = true // Budget erschöpft; nichts geclaimt
			return nil
		}
		timeout := p.ClaimTimeout
		if timeout <= 0 {
			timeout = ClaimTimeout
		}
		cctx, cancel := context.WithTimeout(ctx, timeout)
		won, err := p.Source.Claim(cctx, p.Self, m.ID)
		cancel()
		if err != nil {
			// Nichts geschrieben: die Budgetreservierung wird zurückgerollt.
			return errors.Join(hookbudget.ErrNotEmitted, err)
		}
		if !won {
			lost = true // ein anderer Poller hat sie; nie erneut zustellen
			return hookbudget.ErrNotEmitted
		}
		if err := p.Notifier.Notify(ctx, NewNotification(room, m, text)); err != nil {
			return err
		}
		sent = true
		return nil
	})
	switch {
	case sent && err != nil:
		// Zugestellt, nur die Buchführung danach schlug fehl.
		if p.OnError != nil {
			p.OnError(err)
		}
		return true, nil
	case lost:
		return false, nil
	case err != nil:
		return false, err
	case stalled:
		return false, errStalled
	}
	return sent, nil
}

// errStalled: im Moment kann nicht zugestellt werden (Notifier nicht bereit,
// Budget erschöpft). Kein Fehler, aber auch kein Fortschritt.
var errStalled = errors.New("claude channel: delivery stalled")

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

func (s ClientSource) Message(self string, room store.CoordRoom, id int64) (store.CoordMessage, bool, error) {
	// Die Inbox liefert ab einer ID aufwärts; der erste Treffer ist die
	// Nachricht selbst, wenn sie in diesem Raum liegt.
	msgs, err := s.Client.CoordInbox(store.DestinationRoom, room.Key, self, id-1, 1)
	if err != nil || len(msgs) == 0 || msgs[0].ID != id {
		return store.CoordMessage{}, false, err
	}
	return msgs[0], true, nil
}

func (s ClientSource) Mentions(self string, id int64) ([]string, error) {
	return s.Client.CoordMessageMentions(id, self)
}

func (s ClientSource) Claim(ctx context.Context, self string, id int64) (bool, error) {
	return s.Client.ClaimCoordDeliveryContext(ctx, id, self)
}

func (s ClientSource) Cursor(self string, room store.CoordRoom) (int64, error) {
	return s.Client.CoordCursor(self, store.DestinationRoom, room.Key)
}

func (s ClientSource) SetCursor(self string, room store.CoordRoom, id int64) error {
	return s.Client.SetCoordCursor(self, store.DestinationRoom, room.Key, id)
}
