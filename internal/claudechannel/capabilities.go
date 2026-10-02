package claudechannel

// Fähigkeiten nach Spec §A5, mit denselben Namen wie im codexadapter, damit
// beide Adapter in einer Übersicht nebeneinander stehen können.
const (
	CapReceiveAtSafePoint = "receive_at_safe_point"
	CapWakeIdleSession    = "wake_idle_session"
	CapReceiveForSubagent = "receive_for_named_subagent"
	CapHumanSteer         = "human_steer"
	CapHumanInterrupt     = "human_interrupt"
	CapHumanPause         = "human_pause"
	CapActivityObserve    = "activity_observation"
)

// OptInAtStartOnly ist der Hinweis, der jede Fähigkeit hier einschränkt: der
// Channel muss beim Start der Session per Flag geladen werden. Eine bereits
// laufende Session lässt sich nicht nachträglich anbinden, und ein eigener
// Server braucht --dangerously-load-development-channels samt
// Bestätigungsdialog bei jedem Start.
const OptInAtStartOnly = "OptInAtStartOnly"

// Capabilities nennt, was gemessen wurde: am 2026-09-30, dass eine idle Session
// aufwacht und eine arbeitende die Nachricht am nächsten Tool-Ergebnis bekommt,
// ohne dass der laufende Aufruf abbricht; am 2026-10-02 (Claude Code 2.1.287),
// dass ein PreToolUse-Hook mit continue:false plus deny den auslösenden Aufruf
// verhindert und die Schleife anhält (human_pause, Zustellung über den Hook
// `ctx hook pause-gate`, Flag-Spiegel in diesem Channel). Die Fähigkeit heißt
// nicht, dass jede Pause wirkt: wirksam gilt sie erst mit Ack und
// hook_stopped_continuation im Transkript.
func Capabilities() []string {
	return []string{CapReceiveAtSafePoint, CapWakeIdleSession, CapHumanPause}
}

// MissingCapabilities nennt die Lücken mit Grund. Eine benannte Lücke ist für
// eine Produktentscheidung mehr wert als ein pauschales "kann live".
func MissingCapabilities() map[string]string {
	return map[string]string{
		CapReceiveForSubagent: "a channel notification reaches the session, not a named subagent inside it; not measured",
		CapHumanSteer:         "no measurement of a human typing into a session that received a channel message",
		CapHumanInterrupt: "a pause takes effect at the next tool call: a call that is already running is not aborted, " +
			"and nothing stops while the model streams without a tool call; a channel message never interrupts either",
		CapActivityObserve: "the channel is one-way; it observes nothing about what the session does",
	}
}

// Notes nennt die Einschränkungen, die für alle Fähigkeiten gelten.
func Notes() []string {
	return []string{OptInAtStartOnly}
}
