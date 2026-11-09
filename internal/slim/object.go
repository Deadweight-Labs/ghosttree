package slim

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// object ist ein JSON-Objekt mit erhaltener Schlüsselreihenfolge. Werte bleiben
// als Rohbytes stehen, bis sie geändert werden; nur geänderte Ebenen werden neu
// geschrieben.
type object struct {
	members []member
	changed bool
}

type member struct {
	key string
	val json.RawMessage
}

// parseObject liest raw als Objekt. ok ist false für alles andere, auch für
// ungültiges JSON.
func parseObject(raw []byte) (*object, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, false
	}
	o := &object{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, false
		}
		k, ok := t.(string)
		if !ok {
			return nil, false
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, false
		}
		o.members = append(o.members, member{key: k, val: v})
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}
	return o, true
}

func (o *object) index(k string) int {
	for i, m := range o.members {
		if m.key == k {
			return i
		}
	}
	return -1
}

func (o *object) get(k string) json.RawMessage {
	if i := o.index(k); i >= 0 {
		return o.members[i].val
	}
	return nil
}

// str liefert den Wert von k, wenn er ein String ist.
func (o *object) str(k string) string {
	v := o.get(k)
	if len(v) == 0 || v[0] != '"' {
		return ""
	}
	var s string
	if json.Unmarshal(v, &s) != nil {
		return ""
	}
	return s
}

func (o *object) del(keys ...string) {
	for _, k := range keys {
		for i := o.index(k); i >= 0; i = o.index(k) {
			o.members = append(o.members[:i], o.members[i+1:]...)
			o.changed = true
		}
	}
}

// delIfEmpty entfernt k, wenn der Wert null, {} oder [] ist.
func (o *object) delIfEmpty(keys ...string) {
	for _, k := range keys {
		if v := o.get(k); v != nil && emptyValue(v) {
			o.del(k)
		}
	}
}

func emptyValue(v json.RawMessage) bool {
	switch strings.TrimSpace(string(v)) {
	case "null", "{}", "[]", `""`:
		return true
	}
	return false
}

// set ersetzt den Wert von k, wenn er sich unterscheidet. Ein neuer Schlüssel
// wird angehängt.
func (o *object) set(k string, v json.RawMessage) {
	if i := o.index(k); i >= 0 {
		if bytes.Equal(o.members[i].val, v) {
			return
		}
		o.members[i].val = v
		o.changed = true
		return
	}
	o.members = append(o.members, member{key: k, val: v})
	o.changed = true
}

// edit wendet f auf das Unterobjekt k an und schreibt es zurück, wenn f etwas
// geändert hat.
func (o *object) edit(k string, f func(*object)) {
	v := o.get(k)
	if v == nil {
		return
	}
	sub, ok := parseObject(v)
	if !ok {
		return
	}
	f(sub)
	if sub.changed {
		o.set(k, sub.bytes())
	}
}

func (o *object) bytes() []byte {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range o.members {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(encodeString(m.key))
		b.WriteByte(':')
		b.Write(m.val)
	}
	b.WriteByte('}')
	return b.Bytes()
}

// encodeString schreibt s als JSON-String ohne HTML-Escapes (<, >, & bleiben
// stehen wie in den Transkripten).
func encodeString(s string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return bytes.TrimRight(b.Bytes(), "\n")
}

// parseArray liest raw als Liste von Rohwerten.
func parseArray(raw json.RawMessage) ([]json.RawMessage, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return nil, false
	}
	var out []json.RawMessage
	if json.Unmarshal(raw, &out) != nil {
		return nil, false
	}
	return out, true
}

func joinArray(items []json.RawMessage) json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('[')
	for i, it := range items {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(it)
	}
	b.WriteByte(']')
	return b.Bytes()
}
