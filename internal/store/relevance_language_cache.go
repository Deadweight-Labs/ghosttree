package store

import (
	"strings"
	"sync"
	"time"
)

// The language gate (relevance_language.go) answers the same questions again and
// again: which sessions make up the reader's sample, and is this word unusual in
// it. Both change slowly — a sample is the account's last few weeks of sessions,
// and a word's share in it moves by a chunk or two per prompt — while asking
// costs index lookups over millions of chunks for every word of every prompt.
// Measured on an archive of three million chunks, this was the largest part of a
// prompt's time on the server (PAIN-76). So the answers are kept for a short
// while, per account.
//
// The cache answers exactly what the gate would have answered a moment ago, for
// the same account and nothing else: nothing in it is shared between accounts,
// and what it holds is a verdict and a set of ranges, never text or a count. A
// word's verdict may be up to languageVerdictTTL old and a sample up to
// languageSampleTTL old; chunks written since are not in the ranges until the
// sample is drawn again, which only lets the newest few minutes of the reader's
// own text go uncounted.
//
// What ends a verdict early is the sample changing: when a draw yields other
// sessions or another ceiling, the verdicts of the account are dropped (see
// storeSample). The age limits are chosen on purpose for everything else. Chunks
// that arrive in a sampled session move a word's share by a chunk or two, and
// dropping the verdicts at every upload would empty the cache while the reader is
// typing, the one time it is read. A maintenance job that rewrites text (redact,
// slim) runs in its own process and cannot reach this cache, so its effect shows
// within languageVerdictTTL at the latest; ResetRelevanceCache is for code that
// shares the process.
const (
	languageSampleTTL  = time.Minute
	languageVerdictTTL = 10 * time.Minute
	// languageVerdictMax bounds the words kept per account. A prompt brings a
	// handful of new words; a full cache is cleared rather than trimmed, which is
	// one recount of the words in use.
	languageVerdictMax = 8192
)

// languageCache holds, per account, the gate's sample and its verdicts.
type languageCache struct {
	mu       sync.Mutex
	accounts map[int64]*accountLanguage
}

type accountLanguage struct {
	drawn   time.Time
	owner   int64
	on      bool
	ceiling int
	spans   []sessionSpan
	// verdicts are by word. They are kept across a new draw of the sample, with
	// their own age.
	verdicts map[string]cachedVerdict
}

type cachedVerdict struct {
	at      time.Time
	unusual bool
}

// sample returns the drawn sample of the account if it is still fresh.
func (c *languageCache) sample(account int64, now time.Time) (owner int64, on bool, ceiling int, spans []sessionSpan, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.accounts[account]
	if a == nil || a.drawn.IsZero() || now.Sub(a.drawn) > languageSampleTTL {
		return 0, false, 0, nil, false
	}
	return a.owner, a.on, a.ceiling, a.spans, true
}

// storeSample keeps a freshly drawn sample. The spans are read-only from here on.
func (c *languageCache) storeSample(account int64, now time.Time, owner int64, on bool, ceiling int, spans []sessionSpan) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.account(account)
	// A verdict is made against a sample: the sessions it is drawn from and the
	// ceiling those give. A new draw that differs in either is the event that can
	// change a verdict, so the verdicts of the older sample go with it. A draw that
	// is the same sample keeps them, with their own age.
	if a.on != on || a.ceiling != ceiling || !sameSpans(a.spans, spans) {
		a.verdicts = nil
	}
	a.drawn, a.owner, a.on, a.ceiling, a.spans = now, owner, on, ceiling, spans
}

// sameSpans says whether two samples are made of the same sessions, in the same
// order. The length alone would miss a new session that pushes the oldest one out
// of a full sample. The ranges are not compared: the session being written grows
// its range with every upload, and that is the few new chunks a verdict is
// allowed to miss (see the top of the file), not a different sample.
func sameSpans(a, b []sessionSpan) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].session != b[i].session {
			return false
		}
	}
	return true
}

func (c *languageCache) verdict(account int64, term string, now time.Time) (unusual, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.accounts[account]
	if a == nil {
		return false, false
	}
	v, found := a.verdicts[term]
	if !found || now.Sub(v.at) > languageVerdictTTL {
		return false, false
	}
	return v.unusual, true
}

func (c *languageCache) storeVerdict(account int64, term string, now time.Time, unusual bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.account(account)
	if a.verdicts == nil || len(a.verdicts) >= languageVerdictMax {
		a.verdicts = make(map[string]cachedVerdict)
	}
	// term is a piece of the prompt (strings.FieldsFunc cuts words out of it
	// without copying), and a map key that is a substring keeps the whole prompt
	// alive for as long as the verdict lives.
	a.verdicts[strings.Clone(term)] = cachedVerdict{at: now, unusual: unusual}
}

func (c *languageCache) account(account int64) *accountLanguage {
	if c.accounts == nil {
		c.accounts = make(map[int64]*accountLanguage)
	}
	a := c.accounts[account]
	if a == nil {
		a = &accountLanguage{}
		c.accounts[account] = a
	}
	return a
}

// reset forgets everything. Tests that change the sessions of an account and
// ask again at once need it; the server does not.
func (c *languageCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.accounts = nil
}

// ResetRelevanceCache forgets what the relevance delivery remembers about the
// language of the accounts: their samples and the verdicts on words. A caller
// that changed the text of sessions, such as a test or a maintenance job, calls
// it to be answered from the data as it is now.
func (s *Store) ResetRelevanceCache() {
	s.embedShared().language.reset()
}
