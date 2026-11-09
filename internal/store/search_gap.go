package store

// SearchGap names an area of a search over several areas that gave no answer,
// and why in one word. The hits of the other areas stand as they are; a gap
// says only that this area is missing from them, not what went wrong inside it.
type SearchGap struct {
	Area   string `json:"area"`
	Reason string `json:"reason"`
}

const (
	// GapTimeout: the area did not answer within its time.
	GapTimeout = "timeout"
	// GapFailed: the area failed for another reason.
	GapFailed = "failed"
)
