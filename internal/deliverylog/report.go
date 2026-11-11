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
	// FoldedFrom (YYYY-MM) is the first month of Folded: only whole months that
	// lie inside the window are summed, so a window that starts inside a month
	// leaves that month out.
	FoldedFrom string `json:"folded_from,omitempty"`
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

// UseLine is what was delivered of one kind by one route (or in sessions of one
// profile) and what became of it: how many parts the agent picked up or
// followed afterwards, as far as the transcript shows. Sums only.
type UseLine struct {
	Via     string `json:"via,omitempty"`
	Kind    string `json:"kind"`
	Profile string `json:"profile,omitempty"`
	Parts   int    `json:"parts"`
	// Judged are the parts whose use was read from the transcript. Pending are
	// judgeable parts still waiting for that. What is neither was not judged:
	// the kind has no judgement (path descriptions), the session has no
	// transcript, or the entry was gone.
	Judged   int `json:"judged"`
	PickedUp int `json:"picked_up"`
	Followed int `json:"followed"`
	Pending  int `json:"pending"`
	Chars    int `json:"chars"`
	// JudgedChars and UsedChars give the weight: of the characters that were
	// judged, how many belonged to parts that were picked up or followed.
	JudgedChars int `json:"judged_chars"`
	UsedChars   int `json:"used_chars"`
}

// ContextUse is the report on use of the delivered context since a day.
type ContextUse struct {
	Since    string    `json:"since"`
	Lines    []UseLine `json:"lines"`
	Profiles []UseLine `json:"profiles"`
}

// Judgeable says whether the use of a kind of part is read from the transcript.
// It is the list the judging job works through.
func Judgeable(kind string) bool { return kind == KindKnowledge || kind == KindRunbook }
