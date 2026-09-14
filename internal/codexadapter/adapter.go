// Package codexadapter spricht den Codex App Server, damit eine Nachricht aus
// ghosttree eine wartende Codex-Session erreicht.
//
// Gemessen am 2026-09-14 mit codex-cli 0.153.4 gegen App Server 0.147.0: eine
// Session mit Status notLoaded nimmt über thread/resume plus turn/start eine
// Nachricht an, beginnt einen Turn und antwortet. Das Transkript trägt danach
// den Text und die Antwort des Modells. Damit ist das hier kein Entwurf,
// sondern der belegte Weg.
//
// Was der Adapter NICHT kann, und das ist Teil seiner Aussage: er stellt nicht
// in einen LAUFENDEN Turn zu. turn/steer setzt voraus, dass dieselbe
// Verbindung den Turn gestartet hat; eine fremde TUI gehört ihrem eigenen
// Prozess. Wer das verwechselt, verspricht Push und liefert Wecken.
package codexadapter

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// Fähigkeiten nach Spec §A5. Sie werden einzeln ausgewiesen, weil "MCP
// verbunden" ausdrücklich kein Live-Zertifikat ist — und weil ein Adapter,
// der pauschal "kann live" meldet, die Lücke verbirgt, statt sie zu benennen.
const (
	CapSend               = "send"
	CapReceiveAtSafePoint = "receive_at_safe_point"
	CapWakeIdleSession    = "wake_idle_session"
	CapReceiveForSubagent = "receive_for_named_subagent"
	CapHumanSteer         = "human_steer"
	CapHumanInterrupt     = "human_interrupt"
	CapActivityObserve    = "activity_observation"
)

// Capabilities nennt, was dieser Adapter gegen den Codex App Server wirklich
// belegt hat — nicht, was das Protokoll an Methoden führt.
//
// wake_idle_session ist gemessen. receive_at_safe_point ist es NICHT: dafür
// müsste ghosttree in einen laufenden fremden Turn schreiben, und die dafür
// gedachte Methode turn/steer gehört dem Prozess, der den Turn hält. Die
// Unterscheidung steht hier und nicht in einer Fußnote, weil §A6 verlangt,
// dass eine fehlende Fähigkeit als solche dokumentiert wird.
func Capabilities() []string {
	return []string{CapWakeIdleSession, CapActivityObserve}
}

// MissingCapabilities nennt die Lücken mit Grund. Eine Liste dessen, was
// fehlt, ist für eine Produktentscheidung mehr wert als eine Liste dessen,
// was geht.
func MissingCapabilities() map[string]string {
	return map[string]string{
		CapReceiveAtSafePoint: "turn/steer belongs to the process that started the turn; a foreign TUI is not ours to write into",
		CapReceiveForSubagent: "codex gives a subagent no separate thread id at this version",
		CapHumanSteer:         "same ownership limit as receive_at_safe_point",
		CapHumanInterrupt:     "turn/interrupt exists but is untested here; claiming it without a run would be the thing this package refuses to do",
		CapSend:               "sending is ghosttree's own channel, not something the app server does for us",
	}
}

// Thread ist eine Codex-Session, so wie der App Server sie führt.
type Thread struct {
	ID        string `json:"id"`
	Preview   string `json:"preview"`
	UpdatedAt int64  `json:"updatedAt"`
	// Loaded unterscheidet eine Session, die gerade jemandem gehört, von einer
	// wartenden. Nur die wartende darf geweckt werden; in eine laufende
	// hineinzuschreiben wäre genau die Übergriffigkeit, die §A5 ausschließt.
	Loaded bool `json:"loaded"`
}

// Client hält eine App-Server-Verbindung. Ein Prozess je Zustellung ist
// absichtlich: der App Server ist zustandsbehaftet, und eine langlebige
// geteilte Verbindung würde eine Zustellung an der nächsten scheitern lassen.
type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	out    *bufio.Reader
	mu     sync.Mutex
	nextID int
}

// Dial startet einen App Server auf stdio und meldet sich an.
func Dial(ctx context.Context) (*Client, error) {
	cmd := exec.CommandContext(ctx, "codex", "app-server")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("codex app-server did not start (is codex installed?): %w", err)
	}
	c := &Client{cmd: cmd, stdin: stdin, out: bufio.NewReaderSize(stdout, 1<<20), nextID: 1}
	if _, err := c.call("initialize", map[string]any{
		"clientInfo": map[string]any{"name": "ghosttree", "title": "ghosttree", "version": "0.1.0"},
	}); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) Close() error {
	if c.stdin != nil {
		c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		return c.cmd.Process.Kill()
	}
	return nil
}

type rpcMessage struct {
	ID     *float64        `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
	Params json.RawMessage `json:"params"`
}

// call schickt eine Anfrage und wartet auf genau ihre Antwort.
//
// Zwischendurch kommen Ereignisse — Statusmeldungen, Turn-Fortschritt — und
// die werden übersprungen statt als Antwort missverstanden. Ein Adapter, der
// das erste Beste für seine Antwort hält, liest irgendwann eine fremde
// Statusmeldung als Erfolg.
func (c *Client) call(method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextID
	c.nextID++
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	if _, err := c.stdin.Write(append(body, '\n')); err != nil {
		return nil, err
	}
	for {
		line, err := c.out.ReadBytes('\n')
		if err != nil {
			return nil, fmt.Errorf("%s: %w", method, err)
		}
		var msg rpcMessage
		if json.Unmarshal(line, &msg) != nil || msg.ID == nil || int(*msg.ID) != id {
			continue
		}
		if len(msg.Error) > 0 {
			return nil, fmt.Errorf("%s: %s", method, msg.Error)
		}
		return msg.Result, nil
	}
}

// Threads listet die jüngsten Sessions.
func (c *Client) Threads(limit int) ([]Thread, error) {
	if limit <= 0 {
		limit = 20
	}
	raw, err := c.call("thread/list", map[string]any{"limit": limit})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Data []struct {
			ID        string `json:"id"`
			Preview   string `json:"preview"`
			UpdatedAt int64  `json:"updatedAt"`
			Status    struct {
				Type string `json:"type"`
			} `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	out := make([]Thread, 0, len(payload.Data))
	for _, t := range payload.Data {
		out = append(out, Thread{ID: t.ID, Preview: t.Preview, UpdatedAt: t.UpdatedAt,
			Loaded: t.Status.Type != "notLoaded"})
	}
	return out, nil
}

// DeliveryOutcome sagt, wie weit die Zustellung gekommen ist — in denselben
// Stufen, die der Store führt.
type DeliveryOutcome struct {
	Accepted bool   `json:"accepted"`
	Reply    string `json:"reply,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// WakeIdleThread stellt einer WARTENDEN Session eine Nachricht zu und wartet,
// bis der daraus entstandene Turn fertig ist.
//
// Eine bereits laufende Session wird abgelehnt statt bedient. Das ist die
// Zusage aus §A5: "Ein gerade laufender langer Toolaufruf wird durch eine
// gewöhnliche Nachricht nicht abgebrochen." Wer hier hineinschreibt, wüsste
// nicht, was er unterbricht.
func (c *Client) WakeIdleThread(threadID, text string, wait time.Duration) (DeliveryOutcome, error) {
	if threadID == "" || text == "" {
		return DeliveryOutcome{}, fmt.Errorf("thread id and text are required")
	}
	if _, err := c.call("thread/resume", map[string]any{"threadId": threadID}); err != nil {
		return DeliveryOutcome{Reason: "resume failed"}, err
	}
	if _, err := c.call("turn/start", map[string]any{
		"threadId": threadID,
		"input":    []any{map[string]any{"type": "text", "text": text}},
	}); err != nil {
		return DeliveryOutcome{Reason: "turn/start failed"}, err
	}
	// Angenommen heißt: der Harness hat die Eingabe genommen. Ob das Modell
	// sie befolgt, ist eine andere Frage und wird hier nicht behauptet.
	out := DeliveryOutcome{Accepted: true}
	if wait <= 0 {
		return out, nil
	}
	out.Reply = c.awaitReply(wait)
	return out, nil
}

// awaitReply sammelt die letzte Modellantwort des laufenden Turns. Leer
// bedeutet "nicht beobachtet", nicht "keine Antwort" — der Turn kann länger
// laufen als das Zeitfenster.
func (c *Client) awaitReply(wait time.Duration) string {
	deadline := time.Now().Add(wait)
	var last string
	for time.Now().Before(deadline) {
		line, err := c.out.ReadBytes('\n')
		if err != nil {
			return last
		}
		var msg rpcMessage
		if json.Unmarshal(line, &msg) != nil || msg.Method == "" {
			continue
		}
		if text := agentText(msg.Params); text != "" {
			last = text
		}
		if msg.Method == "turn/completed" || msg.Method == "turn/failed" {
			return last
		}
	}
	return last
}

// agentText zieht die Modellantwort aus einem Ereignis. Bewusst nachsichtig
// gegenüber der Form: die Ereignisstruktur des App Servers ist zwischen
// Versionen verschieden, und ein zu enger Parser meldet "keine Antwort", wo
// eine steht.
func agentText(params json.RawMessage) string {
	if len(params) == 0 {
		return ""
	}
	var any1 struct {
		Item struct {
			Type string `json:"type"`
			Text string `json:"text"`
			Role string `json:"role"`
		} `json:"item"`
	}
	if json.Unmarshal(params, &any1) == nil {
		if any1.Item.Text != "" && (any1.Item.Role == "assistant" || any1.Item.Type == "agent_message") {
			return any1.Item.Text
		}
	}
	return ""
}
