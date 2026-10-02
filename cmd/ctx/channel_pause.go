package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/agentpause"
	"github.com/Deadweight-Labs/ghosttree/internal/claudechannel"
	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// pauseInterval is how often the channel asks the server for a control. It is
// the delay between a click and the flag file, so it is short; the request is
// one small GET.
const pauseInterval = 2 * time.Second

// pauseSource is what the syncer needs from the server.
type pauseSource interface {
	// ControlState returns the active control and, when there is none, the
	// latest one that was lifted (it names who resumed).
	ControlState(agent string) (active, resumed *store.AgentControl, err error)
	RecordEvent(controlID int64, ev store.ControlEvent) (bool, error)
}

type clientPauseSource struct{ c *client.Client }

func (s clientPauseSource) ControlState(agent string) (*store.AgentControl, *store.AgentControl, error) {
	return s.c.AgentControlState(agent)
}

func (s clientPauseSource) RecordEvent(id int64, ev store.ControlEvent) (bool, error) {
	return s.c.RecordControlEvent(id, ev)
}

// pauseSyncer mirrors the server's control state of one agent into the local
// flag file and reports the hook's acks back. The channel is the one process
// per Claude session that already knows its agent identity and talks to the
// server. If it is not running, nothing is mirrored and the control stays
// "requested", which is the honest state.
//
// When a control it has seen active is lifted, the syncer tells the session so
// through the channel. The hook's stopReason is the last thing the agent saw;
// without a positive signal it keeps believing it is paused. A channel
// notification wakes an idle session (measured, see package claudechannel), and
// a resume is an explicit human action, so the wake is wanted.
type pauseSyncer struct {
	agent     string
	src       pauseSource
	ackOffset int64

	// notifier delivers the resume notice; nil means no channel to tell.
	notifier claudechannel.Notifier
	// seen holds the controls this process has watched while they were active.
	// Only those are announced when lifted, so a restarted channel, which finds
	// an old lifted control in the server's answer, stays silent. announced
	// keeps one notice per control.
	seen      map[int64]bool
	announced map[int64]bool
}

// sync does one round. A server error leaves the flag as it is: an
// unreachable server must neither start nor silently lift a pause.
func (p *pauseSyncer) sync() error {
	c, resumed, err := p.src.ControlState(p.agent)
	if err != nil {
		return err
	}
	if c != nil {
		if p.seen == nil {
			p.seen = map[int64]bool{}
		}
		p.seen[c.ID] = true
		if cur, set := agentpause.ReadFlag(p.agent); !set || cur.ControlID != c.ID {
			if err := agentpause.WriteFlag(p.agent, agentpause.Flag{
				ControlID: c.ID, Action: c.Action, By: c.RequestedByLabel, Reason: c.Reason,
			}); err != nil {
				return err
			}
		}
	}
	reportErr := p.reportAcks()
	if c == nil {
		// Control over: lift the flag and drop the ack file even if reporting
		// failed. Acks of a finished control are only evidence for something
		// already resumed, and keeping them could wedge the next control.
		agentpause.RemoveFlag(p.agent)
		agentpause.ClearAcks(p.agent)
		p.ackOffset = 0
		if err := p.announceResume(resumed); err != nil && reportErr == nil {
			reportErr = err
		}
	}
	return reportErr
}

// announceResume sends the resume notice once for a control this process saw
// active. A transport that is not ready or a failed write leaves the control
// unannounced; the server keeps returning it, so the next round retries.
func (p *pauseSyncer) announceResume(c *store.AgentControl) error {
	if p.notifier == nil || c == nil || c.ResumedAt == "" || !p.seen[c.ID] || p.announced[c.ID] {
		return nil
	}
	if !p.notifier.Ready() {
		return nil
	}
	by := store.NormalizeAccountName(c.ResumedByLabel)
	if by == "" {
		by = "einem Menschen"
	}
	what := "Pause"
	if c.Action == store.ControlInterrupt {
		what = "Unterbrechung"
	}
	n := claudechannel.Notification{
		Content: fmt.Sprintf("%s aufgehoben durch %s (control #%d) — du kannst weiterarbeiten. Deine ursprüngliche Aufgabe läuft weiter: mach dort weiter, wo du angehalten wurdest. Das ist keine neue Aufgabe.", what, by, c.ID),
		Meta: map[string]string{
			"event": "pause_resumed", "control_id": strconv.FormatInt(c.ID, 10), "resumed_by": by,
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.notifier.Notify(ctx, n); err != nil {
		return err
	}
	if p.announced == nil {
		p.announced = map[int64]bool{}
	}
	p.announced[c.ID] = true
	return nil
}

func (p *pauseSyncer) reportAcks() error {
	acks, next := agentpause.ReadAcks(p.agent, p.ackOffset)
	for _, a := range acks {
		if a.ControlID <= 0 {
			// The hook found a flag it could not read and so knew no control.
			// The server would answer 400 for ever; there is nothing to report.
			p.ackOffset = a.End
			continue
		}
		if _, err := p.src.RecordEvent(a.ControlID, store.ControlEvent{
			Kind: store.ControlEventAck, ToolUseID: a.ToolUseID, ToolName: a.ToolName,
			AgentID: a.AgentID, SessionID: a.SessionID,
		}); err != nil {
			if client.IsPermanent(err) {
				// A rejection that will not change: log it and move on instead
				// of retrying the same line for ever.
				fmt.Fprintf(os.Stderr, "channel: pause ack dropped: %v\n", err)
				p.ackOffset = a.End
				continue
			}
			return err // the offset stays behind the last reported line
		}
		p.ackOffset = a.End
	}
	p.ackOffset = next
	return nil
}

// run syncs until ctx ends. Errors are logged when they change, not every round.
func (p *pauseSyncer) run(ctx context.Context) {
	last := ""
	t := time.NewTicker(pauseInterval)
	defer t.Stop()
	for {
		if err := p.sync(); err != nil {
			if msg := err.Error(); msg != last {
				fmt.Fprintf(os.Stderr, "channel: pause sync: %v\n", err)
				last = msg
			}
		} else {
			last = ""
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// close removes the flag when the channel ends: the session it belongs to is
// over, and a flag left behind would pause the next session with this identity.
func (p *pauseSyncer) close() { agentpause.RemoveFlag(p.agent) }

// pauseEligible: only a launcher identity can be found by the hook.
func pauseEligible(self string) bool { return strings.HasPrefix(self, agentIDPrefix) }
