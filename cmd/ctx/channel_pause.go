package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/agentpause"
	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// pauseInterval is how often the channel asks the server for a control. It is
// the delay between a click and the flag file, so it is short; the request is
// one small GET.
const pauseInterval = 2 * time.Second

// pauseSource is what the syncer needs from the server.
type pauseSource interface {
	ActiveControl(agent string) (*store.AgentControl, error)
	RecordEvent(controlID int64, ev store.ControlEvent) (bool, error)
}

type clientPauseSource struct{ c *client.Client }

func (s clientPauseSource) ActiveControl(agent string) (*store.AgentControl, error) {
	return s.c.AgentControl(agent)
}

func (s clientPauseSource) RecordEvent(id int64, ev store.ControlEvent) (bool, error) {
	return s.c.RecordControlEvent(id, ev)
}

// pauseSyncer mirrors the server's control state of one agent into the local
// flag file and reports the hook's acks back. The channel is the one process
// per Claude session that already knows its agent identity and talks to the
// server. If it is not running, nothing is mirrored and the control stays
// "requested", which is the honest state.
type pauseSyncer struct {
	agent     string
	src       pauseSource
	ackOffset int64
}

// sync does one round. A server error leaves the flag as it is: an
// unreachable server must neither start nor silently lift a pause.
func (p *pauseSyncer) sync() error {
	c, err := p.src.ActiveControl(p.agent)
	if err != nil {
		return err
	}
	if c != nil {
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
		// Lift only after the last acks are reported; a failed report keeps the
		// acks for the next round, the flag is gone either way.
		agentpause.RemoveFlag(p.agent)
		if reportErr == nil {
			agentpause.ClearAcks(p.agent)
			p.ackOffset = 0
		}
	}
	return reportErr
}

func (p *pauseSyncer) reportAcks() error {
	acks, next := agentpause.ReadAcks(p.agent, p.ackOffset)
	for _, a := range acks {
		if _, err := p.src.RecordEvent(a.ControlID, store.ControlEvent{
			Kind: store.ControlEventAck, ToolUseID: a.ToolUseID, ToolName: a.ToolName,
			AgentID: a.AgentID, SessionID: a.SessionID,
		}); err != nil {
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
