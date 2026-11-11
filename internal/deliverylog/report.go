package deliverylog

// The shapes of the usage reports as they cross the wire between server and
// ctx. They hold sums only.

// DeliveryLine is one route and kind: how many parts were delivered, how many
// characters, and in how many sessions (0 in the folded lines, which come from
// monthly sums that no longer know sessions).
type DeliveryLine struct {
	Via      string `json:"via"`
	Kind     string `json:"kind"`
	Parts    int    `json:"parts"`
	Chars    int    `json:"chars"`
	Sessions int    `json:"sessions,omitempty"`
}

// DeliveryUsage is the report over the delivery log since a day.
type DeliveryUsage struct {
	Since            string         `json:"since"`
	Sessions         int            `json:"sessions"`
	Chars            int            `json:"chars"`
	SubagentChars    int            `json:"subagent_chars"`
	SubagentSessions int            `json:"subagent_sessions"`
	Lines            []DeliveryLine `json:"lines"`
	Folded           []DeliveryLine `json:"folded"`
}

// SignalLine is one value of one session signal. Measured sessions are those
// that are over and whose transcript is stored; Short of them had at most three
// prompts.
type SignalLine struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Sessions int    `json:"sessions"`
	Measured int    `json:"measured"`
	Short    int    `json:"short"`
}

// SignalUsage is the report over the session signals since a day.
type SignalUsage struct {
	Since string       `json:"since"`
	Lines []SignalLine `json:"lines"`
}
