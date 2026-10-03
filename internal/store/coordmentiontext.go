package store

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// ErrCoordAmbiguousMention marks an @name in a message body that fits several
// people or agents of the room. The message is not stored: delivering it to
// one of them would be a guess, and the sender can fix it by typing more.
var ErrCoordAmbiguousMention = errors.New("coordination mention is ambiguous")

// AmbiguousMentionError names the token and the candidates it fits. Only
// members ever get it; a guest cannot see who is in the room, so for a guest an
// ambiguous name drops silently like any other name that does not resolve.
type AmbiguousMentionError struct {
	Token      string
	Candidates []string
}

func (e *AmbiguousMentionError) Error() string {
	return fmt.Sprintf("%v: @%s fits %s; type more of the name", ErrCoordAmbiguousMention, e.Token, strings.Join(e.Candidates, ", "))
}

func (e *AmbiguousMentionError) Is(target error) bool { return target == ErrCoordAmbiguousMention }

const maxTextMentions = 20

// textMentionPattern finds an @name that starts a word. The character in front
// must not belong to a name or address, so ben@example.com is not a mention.
var textMentionPattern = regexp.MustCompile(`(?:^|[^\p{L}\p{N}_.@:-])@([\p{L}\p{N}][\p{L}\p{N}._:-]*)`)

// textMentionTokens returns the normalised @names of a body, in order, without
// repeats and without the punctuation that ends a sentence.
func textMentionTokens(body string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range textMentionPattern.FindAllStringSubmatch(body, -1) {
		token := normalizeHandle(strings.TrimRight(m[1], "._:-"))
		if token == "" || seen[token] {
			continue
		}
		seen[token] = true
		out = append(out, token)
		if len(out) == maxTextMentions {
			break
		}
	}
	return out
}

// normalizeHandle lowercases and joins words with a hyphen, so "Docs
// reviewer" and "docs-reviewer" are the same handle.
func normalizeHandle(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == ':' || r == '_' || r == '-':
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// mentionCandidate is one person or agent a body can address, with the names it
// answers to. handles are ordered from friendliest to most technical.
type mentionCandidate struct {
	id      string
	label   string
	handles []string
}

func (c mentionCandidate) has(handle string) bool {
	for _, h := range c.handles {
		if h == handle {
			return true
		}
	}
	return false
}

func (c mentionCandidate) hasPrefix(prefix string) bool {
	for _, h := range c.handles {
		if strings.HasPrefix(h, prefix) {
			return true
		}
	}
	return false
}

func addHandle(list []string, handle string) []string {
	handle = normalizeHandle(handle)
	if handle == "" {
		return list
	}
	for _, h := range list {
		if h == handle {
			return list
		}
	}
	return append(list, handle)
}

// mentionCandidatesTx are the principals of a room that a mention may reach,
// except the sender, with their handles.
func (a CoordAccess) mentionCandidatesTx(tx *sql.Tx, actor, roomKey string) ([]mentionCandidate, error) {
	allowed, err := a.mentionTargetsTx(tx, actor, roomKey)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(allowed))
	for id := range allowed {
		if id != actor && id != a.Principal.ID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := make([]mentionCandidate, 0, len(ids))
	for _, id := range ids {
		c := mentionCandidate{id: id, label: id}
		if strings.HasPrefix(id, "person:") {
			name := personNameTx(tx, id)
			c.handles = addHandle(c.handles, name)
			if first, _, found := strings.Cut(strings.TrimSpace(name), " "); found {
				c.handles = addHandle(c.handles, first)
			}
			if name != "" {
				c.label = name
			}
			c.handles = addHandle(c.handles, id)
			out = append(out, c)
			continue
		}
		external := strings.TrimPrefix(id, "agent:")
		var display, provider, owner string
		err := tx.QueryRow(`SELECT display_name, provider, COALESCE(principal_id,'') FROM coord_agents WHERE external_id=?`, external).
			Scan(&display, &provider, &owner)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		ownerName := ""
		if owner != "" {
			ownerName = personNameTx(tx, owner)
		}
		if display != "" && display != external {
			c.label = display
			c.handles = addHandle(c.handles, display)
			if first, _, found := strings.Cut(strings.TrimSpace(display), " "); found {
				c.handles = addHandle(c.handles, first)
			}
		}
		if provider != "" && ownerName != "" {
			c.handles = addHandle(c.handles, provider+"-"+ownerName)
			if host := agentHost(external); host != "" {
				c.handles = addHandle(c.handles, provider+"-"+ownerName+"-"+host)
			}
			if short := agentShortID(external); short != "" {
				c.handles = addHandle(c.handles, provider+"-"+ownerName+"-"+short)
			}
		}
		if provider != "" && provider != "unknown" && provider != "unidentified-harness" {
			c.handles = addHandle(c.handles, provider)
		}
		c.handles = addHandle(c.handles, external)
		if c.label == id && ownerName != "" {
			c.label = id + " (" + ownerName + ")"
		}
		out = append(out, c)
	}
	return out, nil
}

func personNameTx(tx *sql.Tx, principal string) string {
	personID, err := parsePersonPrincipalID(principal)
	if err != nil {
		return ""
	}
	var name string
	if tx.QueryRow(`SELECT name FROM persons WHERE id=?`, personID).Scan(&name) != nil {
		return ""
	}
	return strings.TrimSpace(name)
}

// agentHost is the machine part of a harness id such as claude:host:uuid.
func agentHost(external string) string {
	parts := strings.Split(external, ":")
	if len(parts) >= 3 {
		return normalizeHandle(parts[1])
	}
	return ""
}

// agentShortID is the first eight characters of the last part of the id: what
// tells two sessions of the same person on the same machine apart.
func agentShortID(external string) string {
	parts := strings.Split(external, ":")
	last := normalizeHandle(parts[len(parts)-1])
	if len(last) > 8 {
		last = last[:8]
	}
	return last
}

// resolveMentionToken fits one token to the candidates: an exact handle wins
// over a prefix, and anything that is not unique is ambiguous.
func resolveMentionToken(candidates []mentionCandidate, token string) (hit *mentionCandidate, ambiguous []mentionCandidate) {
	var exact, prefix []mentionCandidate
	for _, c := range candidates {
		switch {
		case c.has(token):
			exact = append(exact, c)
		case c.hasPrefix(token):
			prefix = append(prefix, c)
		}
	}
	pool := exact
	if len(pool) == 0 {
		pool = prefix
	}
	switch len(pool) {
	case 0:
		return nil, nil
	case 1:
		return &pool[0], nil
	}
	return nil, pool
}

// textMentionsTx resolves the @names of a body against the room. Members get
// the resolved principals and an error for an ambiguous name. Guests get the
// principals that resolve, no error, and the typed tokens for their read-back,
// so nothing they can observe depends on who is in the room (#2447).
func (a CoordAccess) textMentionsTx(tx *sql.Tx, actor, roomKey, body string, guest bool) (ids, typed []string, err error) {
	tokens := textMentionTokens(body)
	if len(tokens) == 0 || roomKey == "" {
		return nil, nil, nil
	}
	candidates, err := a.mentionCandidatesTx(tx, actor, roomKey)
	if err != nil {
		return nil, nil, err
	}
	for _, token := range tokens {
		if guest {
			typed = append(typed, "@"+token)
		}
		hit, ambiguous := resolveMentionToken(candidates, token)
		switch {
		case hit != nil:
			ids = append(ids, hit.id)
		case len(ambiguous) > 0 && !guest:
			labels := make([]string, 0, 6)
			for i, c := range ambiguous {
				if i == 5 {
					labels = append(labels, "…")
					break
				}
				labels = append(labels, c.label)
			}
			return nil, nil, &AmbiguousMentionError{Token: token, Candidates: labels}
		}
	}
	return ids, typed, nil
}

// MentionHandles gives each principal of the room the shortest handle that
// resolves to it alone, for the composer to offer while someone types @. A
// guest gets nothing: the member list is not theirs to see.
func (a CoordAccess) MentionHandles(roomKey string) (map[string]string, error) {
	reader := a.Store
	if reader.reader != nil {
		reader = reader.reader
	}
	tx, err := reader.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	actor, err := a.actorTx(tx)
	if err != nil {
		return nil, err
	}
	if actor == "" {
		return nil, ErrCoordForbidden
	}
	if err := a.canAccessTx(tx, actor, DestinationRoom, roomKey, viaMembership); err != nil {
		return nil, err
	}
	if a.projectRoomGate(roomKindOf(roomKey), roomKey, ResAgents, tx) != nil {
		return map[string]string{}, nil
	}
	candidates, err := a.mentionCandidatesTx(tx, actor, roomKey)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(candidates))
	for _, c := range candidates {
		for _, h := range c.handles {
			if hit, _ := resolveMentionToken(candidates, h); hit != nil && hit.id == c.id {
				out[c.id] = h
				break
			}
		}
	}
	return out, nil
}

// mentionRoomKeyTx is the room whose members an @name in a message to this
// destination refers to. A legacy thread without a home has no such room and
// keeps only explicit mentions.
func mentionRoomKeyTx(tx *sql.Tx, kind, id string) string {
	if kind == DestinationRoom {
		return id
	}
	threadID, err := strconv.ParseInt(id, 10, 64)
	if err != nil || threadID <= 0 {
		return ""
	}
	if home, found, err := threadHomeTx(tx, threadID); err == nil && found {
		return home.RoomKey
	}
	return ""
}

// markAddressedToAgent sets ToYou on the messages of other senders that
// mention the agent or lie in a private room. Only the reader's own mention
// rows are read, so nothing about other recipients can show.
func markAddressedToAgent(q rowsQuerier, msgs []CoordMessage, agent string) {
	if len(msgs) == 0 {
		return
	}
	mentioned := map[int64]bool{}
	args := []any{agent}
	marks := make([]string, len(msgs))
	for i, m := range msgs {
		args = append(args, m.ID)
		marks[i] = "?"
	}
	rows, err := q.Query(`SELECT message_id FROM coord_message_mentions WHERE mentioned_external_id=? AND message_id IN (`+strings.Join(marks, ",")+`)`, args...)
	if err == nil {
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				mentioned[id] = true
			}
		}
		rows.Close()
	}
	for i := range msgs {
		m := &msgs[i]
		switch {
		case m.SenderExternalID == agent:
		case mentioned[m.ID]:
			m.ToYou = ToYouMention
		case m.DestinationKind == DestinationRoom && (strings.HasPrefix(m.DestinationID, "direct:") || strings.HasPrefix(m.DestinationID, "group:")):
			m.ToYou = ToYouDirect
		}
	}
}
