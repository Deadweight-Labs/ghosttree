package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/blobstrip"
	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/redact"
	"github.com/Deadweight-Labs/ghosttree/internal/transcript"
)

// ---------------------------------------------------------------- classification

var leadTag = regexp.MustCompile(`^<([A-Za-z][A-Za-z0-9_:-]{0,40})[ >/\n]`)

// injectedTags are the tags that the harnesses put into user lines; a text that
// starts with one of them was not typed by the user.
var injectedTags = map[string]bool{
	"system-reminder": true, "task-notification": true, "local-command-caveat": true,
	"local-command-stdout": true, "local-command-stderr": true, "command-name": true,
	"command-message": true, "command-args": true, "user-prompt-submit-hook": true,
	"environment_context": true, "ide_opened_file": true, "ide_selection": true,
	"bash-input": true, "bash-stdout": true, "bash-stderr": true, "subagent_notification": true,
	"turn_aborted": true, "user_instructions": true, "ghosttree": true, "tool-use-id": true,
	"persisted-output": true, "task-id": true, "command-stdout": true, "system": true,
	"skill-name": true, "new-diagnostics": true, "hook_additional_context": true,
}

// classifyPrompt says what a text of a user line is: "prompt" for something a
// person typed, anything else for what the harness injected. The second result
// is a label that is safe to print (a tag name or a fixed word, never content).
func classifyPrompt(s string) string {
	t := strings.TrimSpace(s)
	switch {
	case t == "":
		return "empty"
	case strings.HasPrefix(t, "[SYSTEM NOTIFICATION"):
		return "system-notification"
	case strings.HasPrefix(t, "[Request interrupted"):
		return "interrupt"
	case strings.HasPrefix(t, "Caveat:"):
		return "caveat"
	case strings.HasPrefix(t, "This session is being continued from a previous conversation"):
		return "compaction-summary"
	case strings.HasPrefix(t, "Stop hook feedback"), strings.HasPrefix(t, "UserPromptSubmit hook"):
		return "hook-feedback"
	}
	if m := leadTag.FindStringSubmatch(t); m != nil {
		if injectedTags[m[1]] {
			return "tag:" + m[1]
		}
		return "unknown-tag:" + m[1]
	}
	return "prompt"
}

func realPrompt(kind string) bool {
	// An unknown leading tag is more often pasted markup than a harness
	// injection; it counts as typed and is reported separately.
	return kind == "prompt" || strings.HasPrefix(kind, "unknown-tag:")
}

// ---------------------------------------------------------------- cleaning

var (
	placeholderRe = regexp.MustCompile(`\[ghosttree: [^\]]{0,300}\]`)
	imageMarkRe   = regexp.MustCompile(`\[Image(?: #\d+)?(?:: [^\]]{0,300})?\]`)
	blankRunRe    = regexp.MustCompile(`\n{3,}`)
	spaceRunRe    = regexp.MustCompile(`[ \t]{2,}`)
	spanTags      = []string{"system-reminder", "ide_opened_file", "ide_selection", "local-command-caveat",
		"local-command-stdout", "command-name", "command-message", "command-args", "user-prompt-submit-hook"}
	spanRes = func() []*regexp.Regexp {
		var out []*regexp.Regexp
		for _, t := range spanTags {
			out = append(out, regexp.MustCompile(`(?s)<`+regexp.QuoteMeta(t)+`>.*?</`+regexp.QuoteMeta(t)+`>`))
		}
		return out
	}()
)

type cleanCounters struct {
	Texts          int `json:"texts"`
	IndexMismatch  int `json:"index_mismatch"`  // reparsed text differs from chunk_index.text
	BlobChanged    int `json:"blob_changed"`    // blobstrip changed the raw line
	RedactJSONRaw  int `json:"redact_json_raw"` // RedactJSON changed the raw line
	RedactText     int `json:"redact_text"`     // Redact changed the extracted text
	RedactAny      int `json:"redact_any"`      // the text after redaction differs from before
	Placeholders   int `json:"placeholders"`    // blobstrip placeholders or image markers removed
	TagSpans       int `json:"tag_spans"`       // injected tag spans removed from inside the text
	EmptyAfter     int `json:"empty_after"`
	TooShort       int `json:"too_short"`
	ChangedAnyWay  int `json:"changed_any_way"` // final text differs from chunk_index.text
	RedactMarkers  int `json:"redact_markers"`  // texts that carry a redaction marker already
	DroppedMarkers int `json:"-"`
}

// cleanText is the production path of a text on its way to the embedding server:
// blobstrip and RedactJSON on the stored line, the transcript parser, Redact on
// the text, then the removal of placeholders and injected spans.
func cleanText(harness string, seq int, raw, indexText string, cc *cleanCounters) string {
	cc.Texts++
	base := transcript.Fields(transcript.Parse(harness, seq, raw)).Text
	if base != indexText {
		cc.IndexMismatch++
	}
	line, saved := blobstrip.Strip(raw)
	if saved > 0 {
		cc.BlobChanged++
	}
	red := redact.RedactJSON(line)
	if red != line {
		cc.RedactJSONRaw++
	}
	t := transcript.Fields(transcript.Parse(harness, seq, red)).Text
	t2 := redact.Redact(t)
	if t2 != t {
		cc.RedactText++
	}
	if t2 != base {
		cc.RedactAny++
	}
	t = t2
	if strings.Contains(t, "[redacted") || strings.Contains(t, "[REDACTED") {
		cc.redactMarkerSeen()
	}
	before := t
	t = placeholderRe.ReplaceAllString(t, "")
	t = imageMarkRe.ReplaceAllString(t, "")
	if t != before {
		cc.Placeholders++
	}
	before = t
	for _, re := range spanRes {
		t = re.ReplaceAllString(t, "")
	}
	if t != before {
		cc.TagSpans++
	}
	t = strings.ReplaceAll(t, "\r\n", "\n")
	t = spaceRunRe.ReplaceAllString(t, " ")
	t = blankRunRe.ReplaceAllString(t, "\n\n")
	t = strings.TrimSpace(strings.ToValidUTF8(t, "�"))
	if t != indexText {
		cc.ChangedAnyWay++
	}
	return t
}

func (cc *cleanCounters) redactMarkerSeen() { cc.RedactMarkers++ }

// ---------------------------------------------------------------- prep-items

type sel struct {
	chunk int64
	seq   int
	text  string
}

type turn struct {
	prompt     *sel
	last       *sel
	toolsAfter bool
}

type prepStats struct {
	Sessions            int            `json:"sessions"`
	SessionsWithItems   int            `json:"sessions_with_items"`
	ChunkRows           int            `json:"chunk_rows_read"`
	PromptChunks        int            `json:"prompt_chunks"`
	PromptKinds         map[string]int `json:"prompt_kinds"`
	UnknownFirstWords   map[string]int `json:"first_words_of_prompts_ge15"`
	AssistantTextChunks int            `json:"assistant_text_chunks"`
	Turns               int            `json:"turns_with_real_prompt"`
	TurnsWithFinal      int            `json:"turns_with_final"`
	Turn0Finals         int            `json:"finals_before_first_prompt"`
	FinalsFollowedTool  int            `json:"finals_followed_by_tool_call"`
	Prompts             int            `json:"prompts_kept"`
	Finals              int            `json:"finals_kept"`
	ByHarness           map[string]int `json:"items_by_harness"`
	Clean               cleanCounters  `json:"clean"`
	Dropped             map[string]int `json:"dropped"`
	IdenticalDocs       int            `json:"identical_docs_within_session"`
}

func cmdPrepItems(args []string) error {
	fs := flag.NewFlagSet("prep-items", flag.ExitOnError)
	dbPath := fs.String("db", "", "database")
	minRunes := fs.Int("min-runes", 20, "shortest text that is embedded")
	only := fs.Int64("session", 0, "only this session (debugging)")
	fs.Parse(args)
	db, err := openRO(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	harness := map[int64]string{}
	rows, err := db.Query(`SELECT id, harness FROM sessions`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var h string
		if err := rows.Scan(&id, &h); err != nil {
			return err
		}
		harness[id] = h
	}
	rows.Close()

	st := prepStats{PromptKinds: map[string]int{}, UnknownFirstWords: map[string]int{}, ByHarness: map[string]int{}, Dropped: map[string]int{}}
	st.Sessions = len(harness)
	rawStmt, err := db.Prepare(`SELECT raw FROM session_chunks WHERE id = ?`)
	if err != nil {
		return err
	}
	defer rawStmt.Close()

	q := `SELECT session_id, chunk_id, seq, role, is_prompt, tool_calls, CASE WHEN role != '' THEN text ELSE '' END
		FROM chunk_index WHERE role != '' OR tool_calls > 0`
	var qargs []any
	if *only != 0 {
		q += ` AND session_id = ?`
		qargs = append(qargs, *only)
	}
	q += ` ORDER BY session_id, seq`
	rows, err = db.Query(q, qargs...)
	if err != nil {
		return err
	}
	defer rows.Close()

	var items []Item
	firstWords := map[string]int{}
	var cur int64 = -1
	var turns []turn
	var open *turn
	flush := func() error {
		if cur < 0 {
			return nil
		}
		if open != nil {
			turns = append(turns, *open)
			open = nil
		}
		h := harness[cur]
		seen := map[string]bool{}
		produced := 0
		emit := func(kind string, tn int, s *sel, tools bool) error {
			var raw string
			if err := rawStmt.QueryRow(s.chunk).Scan(&raw); err != nil {
				return fmt.Errorf("raw of chunk %d: %w", s.chunk, err)
			}
			text := cleanText(h, s.seq, raw, s.text, &st.Clean)
			if text == "" {
				st.Clean.EmptyAfter++
				st.Dropped[kind+"_empty"]++
				return nil
			}
			n := utf8.RuneCountInString(text)
			if n < *minRunes {
				st.Clean.TooShort++
				st.Dropped[kind+"_short"]++
				return nil
			}
			doc := embed.DocumentText("", text)
			if seen[doc] {
				st.IdenticalDocs++
			}
			seen[doc] = true
			items = append(items, Item{SID: cur, Chunk: s.chunk, Seq: s.seq, Kind: kind, Turn: tn, Tools: tools, Full: n, Doc: doc})
			st.ByHarness[h]++
			if kind == "prompt" {
				st.Prompts++
			} else {
				st.Finals++
			}
			produced++
			return nil
		}
		for i, t := range turns {
			if t.prompt != nil {
				st.Turns++
				if err := emit("prompt", i, t.prompt, false); err != nil {
					return err
				}
			}
			if t.last != nil {
				if t.prompt != nil {
					st.TurnsWithFinal++
				} else {
					st.Turn0Finals++
				}
				if t.toolsAfter {
					st.FinalsFollowedTool++
				}
				if err := emit("final", i, t.last, t.toolsAfter); err != nil {
					return err
				}
			}
		}
		if produced > 0 {
			st.SessionsWithItems++
		}
		turns, open = nil, nil
		return nil
	}

	for rows.Next() {
		var sid, chunk int64
		var seq, isPrompt, tools int
		var role, text string
		if err := rows.Scan(&sid, &chunk, &seq, &role, &isPrompt, &tools, &text); err != nil {
			return err
		}
		st.ChunkRows++
		if sid != cur {
			if err := flush(); err != nil {
				return err
			}
			cur = sid
			open = &turn{}
		}
		switch {
		case isPrompt == 1 && role == "user":
			st.PromptChunks++
			kind := classifyPrompt(text)
			st.PromptKinds[kind]++
			if !realPrompt(kind) {
				continue
			}
			if w := firstWord(text); w != "" {
				firstWords[w]++
			}
			if open != nil && (open.prompt != nil || open.last != nil) {
				turns = append(turns, *open)
			}
			open = &turn{prompt: &sel{chunk, seq, text}}
		case role == "assistant":
			st.AssistantTextChunks++
			if open == nil {
				open = &turn{}
			}
			open.last = &sel{chunk, seq, text}
			open.toolsAfter = tools > 0
		case tools > 0:
			if open != nil && open.last != nil {
				open.toolsAfter = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := flush(); err != nil {
		return err
	}
	for w, n := range firstWords {
		if n >= 15 {
			st.UnknownFirstWords[w] = n
		}
	}
	sort.SliceStable(items, func(a, b int) bool {
		if items[a].SID != items[b].SID {
			return items[a].SID < items[b].SID
		}
		if items[a].Turn != items[b].Turn {
			return items[a].Turn < items[b].Turn
		}
		return items[a].Kind == "prompt" && items[b].Kind != "prompt"
	})
	for i := range items {
		items[i].Idx = i
	}
	if err := writeJSONL(os.Stdout, items); err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stderr)
	enc.SetIndent("", " ")
	return enc.Encode(st)
}

// firstWord is the first word of a text when it is made of letters only and at
// most 20 characters: used for the aggregate that shows which harness phrases
// still pass as prompts. Only words that many texts start with are printed.
func firstWord(s string) string {
	f := strings.Fields(strings.TrimSpace(s))
	if len(f) == 0 {
		return ""
	}
	w := strings.TrimRight(f[0], ":,.;!?")
	if len(w) > 20 || w == "" {
		return ""
	}
	for _, r := range w {
		if !unicode.IsLetter(r) && r != '-' && r != '#' {
			return ""
		}
	}
	return w
}

// ---------------------------------------------------------------- prep-sessions

func cmdPrepSessions(args []string) error {
	fs := flag.NewFlagSet("prep-sessions", flag.ExitOnError)
	dbPath := fs.String("db", "", "database")
	fs.Parse(args)
	db, err := openRO(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id, harness, title, msg_count, started_at FROM sessions ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var out []SessionMeta
	for rows.Next() {
		var m SessionMeta
		if err := rows.Scan(&m.SID, &m.Harness, &m.Title, &m.Msgs, &m.Started); err != nil {
			return err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return writeJSONL(os.Stdout, out)
}

// ---------------------------------------------------------------- prep-queries

func cmdPrepQueries(args []string) error {
	fs := flag.NewFlagSet("prep-queries", flag.ExitOnError)
	dbPath := fs.String("db", "", "database")
	n := fs.Int("n", 300, "sample size")
	seed := fs.Uint64("seed", 20261020, "seed")
	minTitle := fs.Int("min-title", 15, "shortest title in characters")
	fs.Parse(args)
	db, err := openRO(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	type ev struct {
		sid  int64
		self bool
	}
	type ent struct {
		id    int64
		title string
		ev    []ev
	}
	rows, err := db.Query(`SELECT k.id, k.title, e.session_id, (k.session_ref != '' AND k.session_ref = s.external_id)
		FROM knowledge k JOIN knowledge_evidence e ON e.knowledge_id = k.id JOIN sessions s ON s.id = e.session_id
		WHERE k.status = 'active' AND length(trim(k.title)) >= ?
		GROUP BY k.id, e.session_id ORDER BY k.id, e.session_id`, *minTitle)
	if err != nil {
		return err
	}
	byID := map[int64]*ent{}
	var order []int64
	for rows.Next() {
		var id, sid int64
		var title string
		var self int
		if err := rows.Scan(&id, &title, &sid, &self); err != nil {
			return err
		}
		e := byID[id]
		if e == nil {
			e = &ent{id: id, title: title}
			byID[id] = e
			order = append(order, id)
		}
		e.ev = append(e.ev, ev{sid, self == 1})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	logf("eligible entries (active, evidence, title >= %d chars): %d", *minTitle, len(order))
	pick := shuffled(order, *seed)
	if *n < len(pick) {
		pick = pick[:*n]
	}
	var out []Query
	for _, id := range pick {
		e := byID[id]
		q := Query{ID: "k" + strconv.FormatInt(id, 10), Set: "title", Text: strings.Join(strings.Fields(e.title), " ")}
		allSelf := true
		for _, v := range e.ev {
			q.Targets = append(q.Targets, v.sid)
			allSelf = allSelf && v.self
		}
		q.Self = allSelf
		out = append(out, q)
	}
	return writeJSONL(os.Stdout, out)
}

// ---------------------------------------------------------------- pick-own

func cmdPickOwn(args []string) error {
	fs := flag.NewFlagSet("pick-own", flag.ExitOnError)
	sessions := fs.String("sessions", "", "prep-sessions output")
	itemsPath := fs.String("items", "", "prep-items output")
	n := fs.Int("n", 70, "candidates")
	seed := fs.Uint64("seed", 20261021, "seed")
	exclude := fs.String("exclude", "", "queries file whose target sessions are excluded")
	fs.Parse(args)
	metas, err := readJSONLFile[SessionMeta](*sessions)
	if err != nil {
		return err
	}
	items, err := readJSONLFile[Item](*itemsPath)
	if err != nil {
		return err
	}
	prompts, finals := map[int64]int{}, map[int64]int{}
	for _, it := range items {
		if it.Kind == "prompt" {
			prompts[it.SID]++
		} else {
			finals[it.SID]++
		}
	}
	skip := map[int64]bool{}
	if *exclude != "" {
		qs, err := readJSONLFile[Query](*exclude)
		if err != nil {
			return err
		}
		for _, q := range qs {
			for _, t := range q.Targets {
				skip[t] = true
			}
		}
	}
	var cand []SessionMeta
	for _, m := range metas {
		if strings.TrimSpace(m.Title) == "" || skip[m.SID] || prompts[m.SID] < 2 || finals[m.SID] < 1 || utf8.RuneCountInString(m.Title) < 12 {
			continue
		}
		m.Prompts, m.Finals = prompts[m.SID], finals[m.SID]
		cand = append(cand, m)
	}
	logf("candidates: %d", len(cand))
	pick := shuffled(cand, *seed)
	if *n < len(pick) {
		pick = pick[:*n]
	}
	return writeJSONL(os.Stdout, pick)
}

var _ = sql.ErrNoRows
