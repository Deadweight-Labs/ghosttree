package workflow

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// A Doc is the text of a definition cut into the pieces the block view of
// stage E4 (REQ-697) works on: one segment per top-level key, and segments for
// what is no key (comments, blank lines, lines the parser refuses, unknown
// keys). Joining the segments gives back the text byte for byte, and an edit
// rewrites only the lines of the key it changes, so that both views of a file
// edit the same text and nothing the block view cannot show gets lost.

// Segment kinds.
const (
	SegKey     = "key"     // a known key with its indented lines
	SegUnknown = "unknown" // a top-level key the format does not know, or a key set twice
	SegComment = "comment" // a line that holds only a comment
	SegBlank   = "blank"
	SegRaw     = "raw" // a line the parser refuses (no "key: value", a tab, an indented line without a key)
	// SegWhole: the text has no closed front matter; all of it is one block.
	SegWhole = "whole"
)

// Segment is a run of lines [From, To) of the text.
type Segment struct {
	Kind     string
	Key      string
	From, To int
}

// Doc is a definition text cut into segments.
type Doc struct {
	lines      []string
	ok         bool
	start, end int // the lines "---" that open and close the front matter
	segs       []Segment
}

// ErrEdit is the class of edits the block view cannot apply; the message says why.
var ErrEdit = errors.New("workflow: edit")

func editErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrEdit, fmt.Sprintf(format, args...))
}

// ParseDoc cuts a text into segments. It never fails: a text without front
// matter is one segment of kind SegWhole.
func ParseDoc(text string) *Doc {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	d := &Doc{lines: strings.Split(text, "\n"), start: -1, end: -1}
	start := 0
	for start < len(d.lines) && strings.TrimSpace(d.lines[start]) == "" {
		start++
	}
	if start < len(d.lines) && strings.TrimSpace(d.lines[start]) == "---" {
		for i := start + 1; i < len(d.lines); i++ {
			if strings.TrimRight(d.lines[i], " \t") == "---" {
				d.start, d.end, d.ok = start, i, true
				break
			}
		}
	}
	if !d.ok {
		d.segs = []Segment{{Kind: SegWhole, From: 0, To: len(d.lines)}}
		return d
	}
	d.segs = cutSegments(d.lines, d.start+1, d.end)
	return d
}

// lineClass says what a line of the front matter is.
func lineClass(line string) (kind, key string, indent int) {
	if strings.TrimSpace(line) == "" {
		return SegBlank, "", 0
	}
	if strings.TrimSpace(stripComment(line)) == "" {
		return SegComment, "", 0
	}
	if strings.Contains(line, "\t") {
		return SegRaw, "", 0
	}
	indent = len(line) - len(strings.TrimLeft(line, " "))
	if indent > 0 {
		return "indented", "", indent
	}
	k, _, ok := strings.Cut(strings.TrimSpace(stripComment(line)), ":")
	k = strings.TrimSpace(k)
	if !ok || k == "" || strings.ContainsAny(k, " \"'[]{}") {
		return SegRaw, "", 0
	}
	return SegKey, k, 0
}

// cutSegments groups the lines [from, to): a key takes its indented lines,
// with the blank and comment lines between them; what follows its last
// indented line stands on its own.
func cutSegments(lines []string, from, to int) []Segment {
	var segs []Segment
	seen := map[string]bool{}
	for i := from; i < to; {
		kind, key, _ := lineClass(lines[i])
		switch kind {
		case SegKey:
			last := i
			for j := i + 1; j < to; j++ {
				k, _, _ := lineClass(lines[j])
				if k == "indented" {
					last = j
					continue
				}
				if k == SegBlank || k == SegComment {
					continue
				}
				break
			}
			sk := SegKey
			if !slices.Contains(Keys, key) || seen[key] {
				sk = SegUnknown
			}
			seen[key] = true
			segs = append(segs, Segment{Kind: sk, Key: key, From: i, To: last + 1})
			i = last + 1
		case "indented":
			segs = append(segs, Segment{Kind: SegRaw, From: i, To: i + 1})
			i++
		default:
			segs = append(segs, Segment{Kind: kind, From: i, To: i + 1})
			i++
		}
	}
	return segs
}

// Text is the text of the document.
func (d *Doc) Text() string { return strings.Join(d.lines, "\n") }

// OK says whether the text has a closed front matter.
func (d *Doc) OK() bool { return d.ok }

// Segments lists the segments in the order of the text.
func (d *Doc) Segments() []Segment { return slices.Clone(d.segs) }

// SegmentText is the text of a segment.
func (d *Doc) SegmentText(s Segment) string { return strings.Join(d.lines[s.From:s.To], "\n") }

func (d *Doc) keySeg(key string) (Segment, bool) {
	for _, s := range d.segs {
		if s.Kind == SegKey && s.Key == key {
			return s, true
		}
	}
	return Segment{}, false
}

// KV is one "key: value" line below a key (results, budget).
type KV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Key shapes: what a key's value looks like in the file.
const (
	ShapeScalar = "scalar"
	ShapeList   = "list"
	ShapeMap    = "map"
)

// KeyShape is the shape of the value of a known key.
func KeyShape(key string) string {
	switch key {
	case KeyBranches, KeyInputs:
		return ShapeList
	case KeyResults, KeyBudget:
		return ShapeMap
	}
	return ShapeScalar
}

// KeyValue is the value of a key as the file writes it, before it is checked:
// the block view shows what stands there, also when it is not valid.
type KeyValue struct {
	Key    string   `json:"key"`
	Line   int      `json:"line"`
	Scalar string   `json:"scalar,omitempty"`
	List   []string `json:"list,omitempty"`
	Pairs  []KV     `json:"pairs,omitempty"`
}

// Value reads the value of a known key; ok is false when the file does not set it.
func (d *Doc) Value(key string) (KeyValue, bool) {
	s, ok := d.keySeg(key)
	if !ok {
		return KeyValue{}, false
	}
	kv := KeyValue{Key: key, Line: s.From + 1}
	_, rest, _ := strings.Cut(stripComment(d.lines[s.From]), ":")
	rest = strings.TrimSpace(rest)
	switch {
	case strings.HasPrefix(rest, "["):
		kv.List, _ = inlineList(rest)
	case rest != "":
		kv.Scalar, _ = unquote(rest)
	}
	for _, line := range d.lines[s.From+1 : s.To] {
		body := strings.TrimSpace(stripComment(line))
		if body == "" {
			continue
		}
		if item, ok := strings.CutPrefix(body, "- "); ok || body == "-" {
			v, _ := unquote(strings.TrimSpace(item))
			kv.List = append(kv.List, v)
			continue
		}
		if k, v, ok := strings.Cut(body, ":"); ok {
			uv, _ := unquote(strings.TrimSpace(v))
			kv.Pairs = append(kv.Pairs, KV{Key: strings.TrimSpace(k), Value: uv})
		}
	}
	if KeyShape(key) == ShapeList && kv.List == nil && kv.Scalar != "" {
		kv.List, kv.Scalar = []string{kv.Scalar}, ""
	}
	return kv, true
}

// Body is the text below the front matter, as the file has it.
func (d *Doc) Body() string {
	if !d.ok || d.end+1 >= len(d.lines) {
		return ""
	}
	return strings.TrimSpace(strings.Join(d.lines[d.end+1:], "\n"))
}

// BodyLine is the line of the closing "---" (0 without front matter).
func (d *Doc) BodyLine() int {
	if !d.ok {
		return 0
	}
	return d.end + 1
}

// Edit operations of the block view.
const (
	EditSet    = "set"    // set a key: Value for scalars, List for lists, Pairs for maps; empty removes
	EditRemove = "remove" // remove a key
	EditBody   = "body"   // replace the text below the front matter
	EditBlock  = "block"  // replace the text of a segment that is no known key (Block, Old, Text)
)

// Edit is one change the block view makes.
type Edit struct {
	Op    string   `json:"op"`
	Key   string   `json:"key,omitempty"`
	Value string   `json:"value,omitempty"`
	List  []string `json:"list,omitempty"`
	Pairs []KV     `json:"pairs,omitempty"`
	Text  string   `json:"text,omitempty"`
	// Block is the index of the segment (Doc.Segments) and Old its text when
	// the view was drawn; a block that changed since is not overwritten.
	Block int    `json:"block,omitempty"`
	Old   string `json:"old,omitempty"`
}

// ApplyEdit applies one edit to a text and returns the new text. Every line
// the edit does not concern stays as it was.
func ApplyEdit(text string, e Edit) (string, error) {
	d := ParseDoc(text)
	if err := d.Apply(e); err != nil {
		return text, err
	}
	return d.Text(), nil
}

// Apply changes the document by one edit.
func (d *Doc) Apply(e Edit) error {
	switch e.Op {
	case EditBlock:
		return d.setBlock(e.Block, e.Old, e.Text)
	case EditBody:
		return d.setBody(e.Text)
	case EditRemove:
		if !slices.Contains(Keys, e.Key) {
			return editErr("unknown key %s", e.Key)
		}
		d.ensureFront()
		if !d.ok {
			return editErr("the text has no settings between two lines ---")
		}
		if s, ok := d.keySeg(e.Key); ok {
			d.replace(s.From, s.To, d.keptComments(s, nil, 0))
		}
		return nil
	case EditSet:
		if !slices.Contains(Keys, e.Key) {
			return editErr("unknown key %s", e.Key)
		}
		d.ensureFront()
		if !d.ok {
			return editErr("the text has no settings between two lines ---")
		}
		return d.setKey(e)
	}
	return editErr("unknown operation %q", e.Op)
}

// ensureFront gives an empty text the two lines "---", so that the first block
// of a new workflow has a place.
func (d *Doc) ensureFront() {
	if d.ok || strings.TrimSpace(d.Text()) != "" {
		return
	}
	*d = *ParseDoc("---\n---\n")
}

// replace puts lines in place of [from, to) and cuts the segments again.
func (d *Doc) replace(from, to int, repl []string) {
	out := make([]string, 0, len(d.lines)-(to-from)+len(repl))
	out = append(out, d.lines[:from]...)
	out = append(out, repl...)
	out = append(out, d.lines[to:]...)
	*d = *ParseDoc(strings.Join(out, "\n"))
}

func (d *Doc) setBlock(idx int, old, text string) error {
	if idx < 0 || idx >= len(d.segs) {
		return editErr("there is no block %d", idx)
	}
	s := d.segs[idx]
	if s.Kind == SegKey {
		return editErr("block %d is the key %s; change it as a key", idx, s.Key)
	}
	if d.SegmentText(s) != old {
		return editErr("block %d has changed since it was shown", idx)
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if s.Kind == SegWhole {
		*d = *ParseDoc(text)
		return nil
	}
	text = strings.TrimRight(text, "\n")
	var repl []string
	if text != "" {
		repl = strings.Split(text, "\n")
	}
	for _, l := range repl {
		if strings.TrimRight(l, " \t") == "---" {
			return editErr("a line --- would end the settings; it belongs in the text view")
		}
	}
	d.replace(s.From, s.To, repl)
	return nil
}

func (d *Doc) setBody(text string) error {
	if !d.ok {
		return editErr("the text has no settings between two lines ---")
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if strings.TrimSpace(text) == d.Body() {
		return nil
	}
	head := slices.Clone(d.lines[:d.end+1])
	body := strings.Trim(text, "\n")
	if strings.TrimSpace(body) != "" {
		head = append(head, strings.Split(body, "\n")...)
	}
	*d = *ParseDoc(strings.Join(head, "\n") + "\n")
	return nil
}

// trailing is the comment at the end of a line with its leading spaces, or "".
func trailing(line string) string {
	cut := stripComment(line)
	if len(cut) >= len(strings.TrimRight(line, " ")) {
		return ""
	}
	rest := line[len(cut):]
	return rest
}

// itemComments are the comments around one item of a block value.
type itemComments struct {
	lead  []string
	trail string
}

// blockComments reads the comments inside a key's block: per item (list value
// or map key) the comment lines above it and the comment after it, and the
// comment lines below the last item.
func (d *Doc) blockComments(s Segment) (map[string]itemComments, []string, int) {
	items := map[string]itemComments{}
	var pending []string
	indent := 0
	for _, line := range d.lines[s.From+1 : s.To] {
		kind, _, ind := lineClass(line)
		switch kind {
		case SegComment:
			pending = append(pending, line)
			continue
		case SegBlank:
			continue
		}
		if indent == 0 {
			indent = ind
		}
		body := strings.TrimSpace(stripComment(line))
		id := body
		if item, ok := strings.CutPrefix(body, "- "); ok {
			id, _ = unquote(strings.TrimSpace(item))
		} else if k, _, ok := strings.Cut(body, ":"); ok {
			id = strings.TrimSpace(k)
		}
		c := items[id]
		c.lead = append(c.lead, pending...)
		if t := trailing(line); t != "" {
			c.trail = t
		}
		items[id] = c
		pending = nil
	}
	return items, pending, indent
}

// keptComments are the comment lines of a segment that is removed or rewritten
// without the items they belonged to; nothing a person wrote disappears.
func (d *Doc) keptComments(s Segment, keep map[string]bool, indent int) []string {
	var out []string
	if t := strings.TrimSpace(trailing(d.lines[s.From])); t != "" && keep == nil {
		out = append(out, t)
	}
	items, rest, _ := d.blockComments(s)
	ids := make([]string, 0, len(items))
	for id := range items {
		ids = append(ids, id)
	}
	// Keep the order of the file.
	slices.SortFunc(ids, func(a, b string) int { return d.itemLine(s, a) - d.itemLine(s, b) })
	for _, id := range ids {
		if keep[id] {
			continue
		}
		c := items[id]
		for _, l := range c.lead {
			out = append(out, strings.Repeat(" ", indent)+strings.TrimSpace(l))
		}
		if t := strings.TrimSpace(c.trail); t != "" {
			out = append(out, strings.Repeat(" ", indent)+t)
		}
	}
	for _, l := range rest {
		out = append(out, strings.Repeat(" ", indent)+strings.TrimSpace(l))
	}
	return out
}

func (d *Doc) itemLine(s Segment, id string) int {
	for i := s.From + 1; i < s.To; i++ {
		body := strings.TrimSpace(stripComment(d.lines[i]))
		if item, ok := strings.CutPrefix(body, "- "); ok {
			if v, _ := unquote(strings.TrimSpace(item)); v == id {
				return i
			}
		} else if k, _, ok := strings.Cut(body, ":"); ok && strings.TrimSpace(k) == id {
			return i
		}
	}
	return s.To
}

// FormatScalar writes a value so that Parse reads it back unchanged: plain
// where it can, in double or single quotes where it must.
func FormatScalar(v string) (string, error) {
	if strings.ContainsAny(v, "\n\r\t") {
		return "", editErr("a value has to stand on one line, without tabs")
	}
	needs := v != strings.TrimSpace(v) || strings.Contains(v, " #") ||
		(v != "" && strings.ContainsAny(v[:1], "\"'&*!|>{%@`[#"))
	if !needs {
		return v, nil
	}
	switch {
	case !strings.Contains(v, `"`):
		return `"` + v + `"`, nil
	case !strings.Contains(v, "'"):
		return "'" + v + "'", nil
	}
	return "", editErr("a value that needs quotes cannot hold both kinds of quote")
}

func (d *Doc) setKey(e Edit) error {
	shape := KeyShape(e.Key)
	empty := (shape == ShapeScalar && strings.TrimSpace(e.Value) == "") ||
		(shape == ShapeList && len(e.List) == 0) || (shape == ShapeMap && len(e.Pairs) == 0)
	old, exists := d.keySeg(e.Key)
	if empty {
		if kv, _ := d.Value(e.Key); exists && kv.Scalar == "" && len(kv.List) == 0 && len(kv.Pairs) == 0 {
			return nil // "description:" without a value stays as it is
		}
		return d.Apply(Edit{Op: EditRemove, Key: e.Key})
	}
	var items map[string]itemComments
	indent := 2
	keyTrail := ""
	blockStyle := shape == ShapeMap
	if exists {
		keyTrail = trailing(d.lines[old.From])
		var ind int
		items, _, ind = d.blockComments(old)
		if ind > 0 {
			indent = ind
		}
		// A list written as "- item" lines stays so.
		_, rest, _ := strings.Cut(stripComment(d.lines[old.From]), ":")
		if shape == ShapeList && strings.TrimSpace(rest) == "" {
			blockStyle = true
		}
	}
	pad := strings.Repeat(" ", indent)
	var out []string
	keep := map[string]bool{}
	item := func(id, line string) {
		c := items[id]
		keep[id] = true
		for _, l := range c.lead {
			out = append(out, pad+strings.TrimSpace(l))
		}
		out = append(out, line+c.trail)
	}
	switch shape {
	case ShapeScalar:
		v, err := FormatScalar(strings.TrimSpace(e.Value))
		if err != nil {
			return err
		}
		out = append(out, e.Key+": "+v+keyTrail)
	case ShapeList:
		var ids, vals []string
		for _, it := range e.List {
			it = strings.TrimSpace(it)
			if it == "" {
				continue
			}
			v, err := FormatScalar(it)
			if err != nil {
				return err
			}
			if strings.ContainsAny(it, ",[]") {
				blockStyle = true
			}
			ids, vals = append(ids, it), append(vals, v)
		}
		if len(vals) == 0 {
			return d.Apply(Edit{Op: EditRemove, Key: e.Key})
		}
		if blockStyle {
			out = append(out, e.Key+":"+keyTrail)
			for i, v := range vals {
				item(ids[i], pad+"- "+v)
			}
		} else {
			out = append(out, e.Key+": ["+strings.Join(vals, ", ")+"]"+keyTrail)
		}
	case ShapeMap:
		out = append(out, e.Key+":"+keyTrail)
		seen := map[string]bool{}
		for _, p := range e.Pairs {
			k := strings.TrimSpace(p.Key)
			if k == "" || strings.ContainsAny(k, " :\"'[]{}#") {
				return editErr("%q cannot be a key below %s", p.Key, e.Key)
			}
			if seen[k] {
				return editErr("%s.%s is set twice", e.Key, k)
			}
			seen[k] = true
			v, err := FormatScalar(strings.TrimSpace(p.Value))
			if err != nil {
				return err
			}
			if v == "" {
				return editErr("%s.%s needs a value", e.Key, k)
			}
			item(k, pad+k+": "+v)
		}
	}
	if !exists {
		at := d.insertAt(e.Key)
		d.replace(at, at, out)
		return nil
	}
	// Comments of items that are gone stay: below the block, or above the key
	// when the value now stands on the key's line.
	left := d.keptComments(old, keep, indent)
	if len(out) > 1 {
		out = append(out, left...)
	} else {
		for i := range left {
			left[i] = strings.TrimSpace(left[i])
		}
		out = append(left, out...)
	}
	d.replace(old.From, old.To, out)
	return nil
}

// insertAt is the line where a key that the file does not have yet goes: in
// the order of Keys, before the comments that lead into the next key, or after
// the last key.
func (d *Doc) insertAt(key string) int {
	rank := slices.Index(Keys, key)
	lastEnd := -1
	for i, s := range d.segs {
		if s.Kind != SegKey {
			continue
		}
		if slices.Index(Keys, s.Key) > rank {
			at := i
			for at > 0 && d.segs[at-1].Kind == SegComment {
				at--
			}
			if lastEnd >= 0 && d.segs[at].From < lastEnd {
				return lastEnd
			}
			return d.segs[at].From
		}
		lastEnd = s.To
	}
	if lastEnd >= 0 {
		return lastEnd
	}
	return d.end
}
