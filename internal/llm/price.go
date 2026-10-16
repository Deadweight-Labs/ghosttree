package llm

// Price is what a model costs, in USD per million tokens. Cache reads and
// writes have their own prices; when they are 0 they follow the usual ratios
// (reads a tenth of the input price, five-minute writes 1.25 times).
type Price struct {
	InUSD, OutUSD, CacheReadUSD, CacheWriteUSD float64
}

// Cost is the cost of one call: every token class at its own price.
func (p Price) Cost(u Usage) float64 {
	read, write := p.CacheReadUSD, p.CacheWriteUSD
	if read == 0 {
		read = p.InUSD * 0.1
	}
	if write == 0 {
		write = p.InUSD * 1.25
	}
	return (float64(u.InputTokens)*p.InUSD + float64(u.OutputTokens)*p.OutUSD +
		float64(u.CacheReadTokens)*read + float64(u.CacheWriteTokens)*write) / 1e6
}

// DefaultPrice returns the list price (synchronous, prompts up to 100k tokens)
// of the models the jobs are meant for. Any other model has no default: its
// prices must be set in llm.json, or a dollar limit cannot be enforced.
func DefaultPrice(model string) (Price, bool) {
	switch model {
	case "claude-sonnet-5-5", "claude-sonnet-5":
		return Price{InUSD: 2, OutUSD: 10}, true
	case "claude-haiku-5-5":
		return Price{InUSD: 0.10, OutUSD: 0.50}, true
	}
	return Price{}, false
}
