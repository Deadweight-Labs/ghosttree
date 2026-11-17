package workflow

// DocBlock is a segment the block view shows as a text block: a comment, a
// line the parser refuses, an unknown key, or the whole text when it has no
// front matter. Owner is the known key it stands above, so that the view puts
// it beside that block; empty when no key follows.
type DocBlock struct {
	ID    int    `json:"id"`
	Kind  string `json:"kind"`
	Key   string `json:"key,omitempty"`
	Text  string `json:"text"`
	Line  int    `json:"line"`
	Owner string `json:"owner,omitempty"`
}

// DocView is what the block view draws: the known keys in the order of the
// file with what they say, the text blocks, the instruction below the front
// matter, and the check of the text.
type DocView struct {
	OK       bool       `json:"ok"`
	Keys     []KeyValue `json:"keys"`
	Blocks   []DocBlock `json:"blocks"`
	Body     string     `json:"body"`
	BodyLine int        `json:"body_line,omitempty"`
	Stage    int        `json:"stage"`
	Errors   []Problem  `json:"errors"`
	Warnings []Problem  `json:"warnings"`
}

// ViewOf reads a text for the block view and checks it with env.
func ViewOf(text string, env Env) DocView {
	d := ParseDoc(text)
	v := DocView{OK: d.OK(), Keys: []KeyValue{}, Blocks: []DocBlock{}, Errors: []Problem{}, Warnings: []Problem{}}
	segs := d.Segments()
	for i, s := range segs {
		switch s.Kind {
		case SegKey:
			if kv, ok := d.Value(s.Key); ok {
				v.Keys = append(v.Keys, kv)
			}
		case SegBlank:
		default:
			b := DocBlock{ID: i, Kind: s.Kind, Key: s.Key, Text: d.SegmentText(s), Line: s.From + 1}
			for _, next := range segs[i+1:] {
				if next.Kind == SegKey {
					b.Owner = next.Key
					break
				}
			}
			v.Blocks = append(v.Blocks, b)
		}
	}
	v.Body, v.BodyLine = d.Body(), d.BodyLine()
	_, rep := Check(text, env)
	v.Stage = rep.Stage
	v.Errors = append(v.Errors, rep.Errors...)
	v.Warnings = append(v.Warnings, rep.Warnings...)
	return v
}
