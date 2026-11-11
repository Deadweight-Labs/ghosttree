package hookbudget

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/profile"
)

// cutNotice is the text that closes a response the budget cut short.
func cutNotice(channel string, limit int) string {
	if channel == ChannelCoord {
		return coordNotice
	}
	return noticeFor(limit)
}

// Remaining says how many characters the next delivery on the channel may carry
// whole: what the profile's budget has left, the notice of a cut excepted. A
// text of at most that many characters is delivered as it is. ok is false when
// the budget cannot be read; a caller then does what it did before it knew.
//
// It reads the receipt without the lock, so it is an answer for now: a hook that
// runs at the same moment can use some of it up. DeliverChannel does not rely
// on it (cutAtLine), it only makes the cut rare.
func Remaining(sessionID, channel string) (n int, ok bool) {
	if strings.TrimSpace(sessionID) == "" || len(sessionID) > 4096 {
		return 0, false
	}
	path, err := channelPath(sessionID, channel)
	if err != nil {
		return 0, false
	}
	limit := LimitFor(channel, profile.FromEnv())
	room := limit - len([]rune(cutNotice(channel, limit)))
	r, err := readReceipt(path, receiptKey(sessionID, channel))
	if errors.Is(err, os.ErrNotExist) {
		return max(room, 0), true
	}
	if err != nil {
		return 0, false
	}
	if window := ChannelWindow(channel); window > 0 && !r.StartedAt.IsZero() && time.Since(r.StartedAt) > window {
		return max(room, 0), true
	}
	if r.Exhausted {
		return 0, true
	}
	return max(room-r.ReservedChars, 0), true
}

// A runbook that the server delivers by itself starts with a header line and
// ends with the line that asks for the report (runbook.Briefing). Between the
// two it is one piece: cut in the middle, it would be remembered as delivered
// and never come again.
var (
	runbookHeader = regexp.MustCompile(`(?m)^\[runbook [^\n]*, triggered by `)
	runbookEndTag = "\nWhen done, report"
)

// cutAtLine shortens text to at most max characters. It cuts after the last
// complete line that fits, not in the middle of one, and it does not leave half
// a runbook: a runbook the cut would split goes out whole or not at all. A text
// with no line break in the part that fits is cut at max.
func cutAtLine(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	boundary := max
	if runes[max] != '\n' {
		for i := max - 1; i >= 0; i-- {
			if runes[i] == '\n' {
				boundary = i + 1
				break
			}
		}
	}
	at := len(string(runes[:boundary]))
	var last []int
	for _, m := range runbookHeader.FindAllStringIndex(text[:at], -1) {
		last = m
	}
	if last == nil {
		return text[:at]
	}
	end := len(text)
	if i := strings.Index(text[last[0]:], runbookEndTag); i >= 0 {
		end = last[0] + i + 1
		if j := strings.IndexByte(text[end:], '\n'); j >= 0 {
			end += j
		} else {
			end = len(text)
		}
	}
	if end > at {
		return text[:last[0]]
	}
	return text[:at]
}
