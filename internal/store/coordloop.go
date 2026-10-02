package store

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Loop guard (REQ-360, criterion 1223). It slows a low-content repetition loop
// between agents and lets a discussion with new content go on. It counts
// rounds without new content, never messages: any message that brings
// something new resets the streak, so a long productive review never trips it.
//
// A message is low content only if it brings ZERO new content tokens compared
// to the last LoopWindow messages of the room (after stop words and ack
// filler, digits normalised to "#"), or repeats the sender's own previous
// message (token Jaccard >= 0.8). A false hold on a short productive dialogue
// is worse than a missed loop, so a single new word is new content; padding
// with fresh words evades the guard on purpose and the send limit stays the
// backstop. Plain counters ("step 14 of 20") are not new content; a number
// only counts as part of an identifier (hash, REQ/AC/PR, path, link).
//
// The rule is deterministic, with no model call, and pure: LoopTracker is fed
// the messages of ONE room in order. Time only enters through the messages'
// own CreatedAt, so a replay gives the same streaks. Thresholds are starting
// values, not measured quantities.
const (
	// LoopWindow is how many earlier messages novelty is measured against.
	LoopWindow = 6
	// LoopWarnStreak adds loop_streak=N to the delivery meta.
	LoopWarnStreak = 3
	// LoopHoldStreak is where enforce withholds the wake. Observe only logs
	// that it would have.
	LoopHoldStreak = 5
	// LoopDecay is the quiet time after which a streak is forgotten.
	LoopDecay = 10 * time.Minute
	// LoopMaxBody is how much of a message the tracker reads. The server does
	// not cap bodies, and the rule must stay cheap.
	LoopMaxBody = 16 << 10

	loopJaccardRepeat = 0.80 // token overlap with the sender's own previous message
)

// LoopMode is the GHOSTTREE_LOOP_GUARD switch.
type LoopMode string

const (
	// LoopObserve computes, warns in the meta and logs "would hold". Default.
	LoopObserve LoopMode = "observe"
	// LoopEnforce additionally withholds the wake at LoopHoldStreak.
	LoopEnforce LoopMode = "enforce"
)

// LoopModeFromEnv reads GHOSTTREE_LOOP_GUARD. Anything but "enforce" is observe.
func LoopModeFromEnv() LoopMode {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("GHOSTTREE_LOOP_GUARD")), string(LoopEnforce)) {
		return LoopEnforce
	}
	return LoopObserve
}

// LoopState is the verdict on the latest message.
type LoopState struct {
	// Streak is the number of low-content rounds in a row, counting this message.
	Streak int
	// Low says this message itself carried nothing new.
	Low bool
	// Signal names what made the message new content ("" when Low).
	Signal string
}

// Warn says the delivery should carry loop_streak.
func (s LoopState) Warn() bool { return s.Streak >= LoopWarnStreak }

// Hold says the streak reached the point where enforce withholds the wake.
func (s LoopState) Hold() bool { return s.Streak >= LoopHoldStreak }

// LoopTracker follows one room. The zero value is ready. It is not safe for
// concurrent use.
type LoopTracker struct {
	window []loopEntry
	streak int
	last   string // sender of the last low-content message of the run
	prevAt time.Time
}

type loopEntry struct {
	sender string
	tokens map[string]bool
	idents map[string]bool // "kind:value"
}

// Add feeds the next message of the room and returns the state after it. The
// guard's own notices are ordinary messages (low content) to it.
//
// A round is a change of sender among consecutive low-content messages, so a
// third participant joining an ack loop does not end it. The first low message
// of a run only starts it; the same sender talking on adds nothing. New
// content, a human sender, or LoopDecay of silence reset the streak to zero.
func (t *LoopTracker) Add(m CoordMessage) LoopState {
	at, _ := time.Parse(time.RFC3339, m.CreatedAt)
	if !at.IsZero() && !t.prevAt.IsZero() && at.Sub(t.prevAt) > LoopDecay {
		t.reset()
	}
	if !at.IsZero() {
		t.prevAt = at
	}
	entry, signal := t.assess(m)
	t.window = append(t.window, entry)
	if len(t.window) > LoopWindow {
		t.window = t.window[len(t.window)-LoopWindow:]
	}
	if signal != "" {
		t.reset()
		return LoopState{Signal: signal}
	}
	if t.last != "" && m.SenderExternalID != t.last {
		t.streak++
	}
	t.last = m.SenderExternalID
	return LoopState{Streak: t.streak, Low: true}
}

func (t *LoopTracker) reset() { t.streak, t.last = 0, "" }

// assess returns the message's features and the content signal that makes it
// new ("" if it is low-content), judged against the window before it.
func (t *LoopTracker) assess(m CoordMessage) (loopEntry, string) {
	f := loopFeatures(m.Body)
	f.sender = m.SenderExternalID
	for _, r := range m.Refs {
		f.idents["ref:"+strings.ToLower(r.Kind+"/"+r.ID)] = true
	}
	if m.AuthorKind == AuthorHuman {
		return f, "human"
	}
	seen := func(pick func(loopEntry) map[string]bool, key string) bool {
		for _, w := range t.window {
			if pick(w)[key] {
				return true
			}
		}
		return false
	}
	keys := make([]string, 0, len(f.idents))
	for key := range f.idents {
		keys = append(keys, key)
	}
	sort.Strings(keys) // the signal name must not depend on map order
	for _, key := range keys {
		if !seen(func(e loopEntry) map[string]bool { return e.idents }, key) {
			return f, key[:strings.Index(key, ":")]
		}
	}
	// The sender's own previous message, however long, is a repeat when it is
	// nearly the same words.
	for i := len(t.window) - 1; i >= 0; i-- {
		if w := t.window[i]; w.sender == f.sender {
			if len(f.tokens) > 0 && jaccard(f.tokens, w.tokens) >= loopJaccardRepeat {
				return f, ""
			}
			break
		}
	}
	for tok := range f.tokens {
		if !seen(func(e loopEntry) map[string]bool { return e.tokens }, tok) {
			if m.Intent == IntentQuestion {
				return f, "question"
			}
			return f, "text"
		}
	}
	return f, ""
}

func jaccard(a, b map[string]bool) float64 {
	inter := 0
	for k := range a {
		if b[k] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

var (
	loopCodeBlock = regexp.MustCompile("(?s)```.*?```")
	loopURL       = regexp.MustCompile(`(?i)\bhttps?://[^\s)>\]]+`)
	loopRef       = regexp.MustCompile(`(?i)\b(?:req|ac|thr|pr)[-# ]?\d+\b|#\d+\b`)
	loopHash      = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)
	loopPath      = regexp.MustCompile(`(?:[\w.~-]+/){2,}[\w.-]+|(?:\.{0,2}|~)/[\w.-]+(?:/[\w.-]+)*|\b[\w-]+\.(?:go|md|ts|tsx|js|py|sh|json|ya?ml|toml|html|css|sql|txt|mod|sum)\b`)
	loopDigits    = regexp.MustCompile(`\d+(?:[.,]\d+)?`)
)

// loopFeatures extracts the content markers of a body: identifiers (code
// block, link, REQ/AC/THR/PR reference, commit hash, file path) keyed
// "kind:value", and the content tokens that remain once identifiers and stop
// words are cut. Digits are normalised to "#", and a token that is nothing but
// digits is dropped, so a counter is no content. A hex run only counts as a
// commit hash when it mixes digits and letters. Only the first LoopMaxBody
// bytes are read.
func loopFeatures(body string) loopEntry {
	if len(body) > LoopMaxBody {
		body = strings.ToValidUTF8(body[:LoopMaxBody], "")
	}
	f := loopEntry{tokens: map[string]bool{}, idents: map[string]bool{}}
	rest := body
	cut := func(re *regexp.Regexp, kind string, norm func(string) string) {
		for _, s := range re.FindAllString(rest, -1) {
			f.idents[kind+":"+norm(s)] = true
		}
		rest = re.ReplaceAllString(rest, " ")
	}
	cut(loopCodeBlock, "code", func(s string) string { return strings.Join(strings.Fields(s), " ") })
	cut(loopURL, "link", strings.ToLower)
	cut(loopRef, "ref", func(s string) string {
		return strings.ToLower(strings.NewReplacer(" ", "", "-", "", "#", "").Replace(s))
	})
	rest = loopHash.ReplaceAllStringFunc(rest, func(h string) string {
		if strings.IndexFunc(h, unicode.IsDigit) < 0 || strings.IndexFunc(h, unicode.IsLetter) < 0 {
			return h
		}
		f.idents["commit:"+h] = true
		return " "
	})
	cut(loopPath, "path", func(s string) string { return strings.ToLower(strings.Trim(s, "./")) })
	rest = loopDigits.ReplaceAllString(strings.ToLower(rest), "#")
	for _, tok := range strings.FieldsFunc(rest, func(r rune) bool {
		return r != '#' && !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if tok != "#" && len([]rune(tok)) >= 2 && !loopStop[tok] {
			f.tokens[tok] = true
		}
	}
	return f
}

// loopStop are German and English words that carry no content: the filler of
// acknowledgements, thanks and agreement, and plain function words.
var loopStop = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`
		ok okay geschehen ja jo nein gern gerne danke dank bitte super prima gut alles klar genau
		verstanden passt stimmt richtig erledigt fertig done thanks thank you thx yes yep yeah
		sure fine great good got understood agreed noted ack roger welcome please cool
		der die das den dem des ein eine einen einem einer und oder aber auch noch nur schon
		ich du er sie es wir ihr mir dir mich dich uns euch ist sind war waren wird werden hat haben
		bin bist sein mit von zu zum zur im in an auf aus bei für fuer nach vor über ueber um
		the an and or but also still just already we he she it they is are was were be been
		has have had with of to on at for from by this that these those my your our its
		so dann denn wie was wer wo wenn dass da hier jetzt nun mal`) {
		m[w] = true
	}
	return m
}()
