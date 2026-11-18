package web

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// doneNote is the outcome of an administrative action, worded for the person
// who did it: it names the person and the object ("Jonas Brandt is now lead of
// werkstatt-keller/deploy-hook"). Text comes from the handler, never from the
// address, so a link cannot bring its own wording onto the page.
type doneNote struct {
	Text string
	// Row is the id of the row the action changed; the page marks it and the
	// address points at it.
	Row string
	// Open is the project whose roles stay open after the action.
	Open string
}

type storedNote struct {
	doneNote
	account string
	expires time.Time
}

const (
	doneNoteTTL        = 10 * time.Minute
	doneNotesPerPerson = 8
	doneNotesMax       = 512
)

// doneNotes keeps those notes until the page the redirect leads to has shown
// them once. A reload does not show the note again, and a note is for the
// account that caused it only.
type doneNotes struct {
	mu sync.Mutex
	m  map[string]storedNote
}

// adminNotes is shared by all pages with administrative forms (organization
// settings, label settings). Tokens are random and bound to an account.
var adminNotes = &doneNotes{}

// put keeps a note and returns the token for the redirect.
func (d *doneNotes) put(account string, n doneNote) string {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	token := hex.EncodeToString(buf)
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.m == nil {
		d.m = map[string]storedNote{}
	}
	oldest, count := "", 0
	for k, v := range d.m {
		if now.After(v.expires) {
			delete(d.m, k)
			continue
		}
		if v.account == account {
			count++
			if oldest == "" || v.expires.Before(d.m[oldest].expires) {
				oldest = k
			}
		}
	}
	if count >= doneNotesPerPerson {
		delete(d.m, oldest)
	}
	for len(d.m) >= doneNotesMax {
		for k := range d.m {
			delete(d.m, k)
			break
		}
	}
	d.m[token] = storedNote{doneNote: n, account: account, expires: now.Add(doneNoteTTL)}
	return token
}

// take returns the note behind a token for its account, once.
func (d *doneNotes) take(token, account string) (doneNote, bool) {
	if token == "" {
		return doneNote{}, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	v, ok := d.m[token]
	if !ok || v.account != account || time.Now().After(v.expires) {
		return doneNote{}, false
	}
	delete(d.m, token)
	return v.doneNote, true
}

// rowAnchor is the id of a row of the settings: kind and the account or
// project it is about, safe as an id and as a fragment.
func rowAnchor(kind, key string) string {
	return kind + "-" + sanitizeAnchor(key)
}

func sanitizeAnchor(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}

// RowID is the id of the member's row in the member list.
func (m orgMemberRow) RowID() string { return rowAnchor("m", m.AccountID) }

// RowID is the id of the project's box in the project list.
func (v projectRolesView) RowID() string { return rowAnchor("p", v.Remote) }
