package claudechannel

// Fähigkeiten nach Spec §A5, mit denselben Namen wie im codexadapter, damit
// beide Adapter in einer Übersicht nebeneinander stehen können.
const (
	CapReceiveAtSafePoint = "receive_at_safe_point"
	CapWakeIdleSession    = "wake_idle_session"
	CapReceiveForSubagent = "receive_for_named_subagent"
	CapHumanSteer         = "human_steer"
	CapHumanInterrupt     = "human_interrupt"
	CapActivityObserve    = "activity_observation"
)

// OptInAtStartOnly ist der Hinweis, der jede Fähigkeit hier einschränkt: der
// Channel muss beim Start der Session per Flag geladen werden. Eine bereits
// laufende Session lässt sich nicht nachträglich anbinden, und ein eigener
// Server braucht --dangerously-load-development-channels samt
// Bestätigungsdialog bei jedem Start.
const OptInAtStartOnly = "OptInAtStartOnly"

// Capabilities nennt, was am 2026-09-30 gemessen wurde: eine idle Session
// wacht auf, und eine arbeitende bekommt die Nachricht am nächsten
// Tool-Ergebnis, ohne dass der laufende Aufruf abbricht.
func Capabilities() []string {
	return []string{CapReceiveAtSafePoint, CapWakeIdleSession}
}

// MissingCapabilities nennt die Lücken mit Grund. Eine benannte Lücke ist für
// eine Produktentscheidung mehr wert als ein pauschales "kann live".
func MissingCapabilities() map[string]string {
	return map[string]string{
		CapReceiveForSubagent: "a channel notification reaches the session, not a named subagent inside it; not measured",
		CapHumanSteer:         "no measurement of a human typing into a session that received a channel message",
		CapHumanInterrupt:     "a channel message never interrupts a running call; it waits for the next tool result",
		CapActivityObserve:    "the channel is one-way; it observes nothing about what the session does",
	}
}

// Notes nennt die Einschränkungen, die für alle Fähigkeiten gelten.
func Notes() []string {
	return []string{OptInAtStartOnly}
}
