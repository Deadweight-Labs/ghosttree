// Package query ist die Suchsprache nach dem Vorbild von GitHub (REQ-600): freier
// Text und Qualifier der Form key:value in einer Zeile. Es gibt genau einen
// Parser; Server, MCP und CLI geben die Zeile unverändert weiter und bekommen
// überall dieselbe Bedeutung.
//
// Grammatik (Leerzeichen trennen Wörter, "…" fasst zusammen):
//
//	query     = { word } ;
//	word      = [ "-" ] key ":" value | [ "-" ] "\"" phrase "\"" | text ;
//	value     = alt { "," alt } ;               ODER innerhalb eines Qualifiers
//	alt       = text | number | range | compare ;
//	range     = lo ".." hi | lo ".." | ".." hi ;
//	compare   = ( ">" | ">=" | "<" | "<=" ) text ;
//
// Ein Qualifier ist ein Wort, das mit Kleinbuchstaben, ":" und einem Wert
// beginnt. Ein unbekannter Schlüssel ist ein Fehler und wird nie still zu
// Text, denn "is:oen" als Text zu suchen ergäbe eine leere, scheinbar
// gültige Antwort. Eine URL (https://…) bleibt Text.
//
// Qualifier verschiedener Schlüssel gelten gemeinsam (UND), Alternativen eines
// Schlüssels mit Komma oder durch Wiederholen (ODER), "-" davor kehrt um.
//
// Das Paket kennt nur Syntax und Bedeutung. Wo ein Qualifier in SQL läuft,
// steht an der jeweiligen Abfrage; Match ist die eine Definition, was ein
// Treffer erfüllen muss.
package query

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
)

// Objektarten, die Treffer haben können.
const (
	KindKnowledge = "knowledge"
	KindRequest   = "request"
	KindPain      = "pain"
	KindIdea      = "idea"
	KindFeature   = "feature"
	KindCriterion = "criterion"
	KindTask      = "task"
	KindFile      = "file"
	// Raum-Nachrichten und Sessions haben keinen Zustand und kein Label; was sie
	// kennen, sind Projekt, Branch, Zeit, Urheber und (Nachrichten) Anker und Bereich.
	KindMessage = "message"
	KindSession = "session"
)

// Kinds sind alle Objektarten in fester Reihenfolge.
var Kinds = []string{KindKnowledge, KindRequest, KindPain, KindIdea, KindFeature, KindCriterion, KindTask, KindFile, KindMessage, KindSession}

var kindAlias = map[string]string{
	"knowledge": KindKnowledge, "wissen": KindKnowledge,
	"request": KindRequest, "requests": KindRequest, "issue": KindRequest, "issues": KindRequest, "req": KindRequest,
	"pain": KindPain, "pains": KindPain, "idea": KindIdea, "ideas": KindIdea,
	"feature": KindFeature, "features": KindFeature, "feat": KindFeature,
	"criterion": KindCriterion, "criteria": KindCriterion,
	"task": KindTask, "tasks": KindTask, "file": KindFile, "files": KindFile, "ghost": KindFile,
	"message": KindMessage, "messages": KindMessage, "nachricht": KindMessage, "session": KindSession, "sessions": KindSession,
}

// StateClasses sind die Zustandsklassen von is:.
const (
	ClassOpen   = "open"
	ClassClosed = "closed"
	ClassDone   = "done"
)

// StateClass ordnet den Zustand einer Objektart einer Klasse zu. Leer heißt:
// die Art hat keinen Zustand im Sinn von offen und abgeschlossen (Wissen,
// Dateien); is:open und is:closed treffen sie nie.
//
//	request:   open, done = done, dropped = closed
//	criterion: open, met = done, waived = closed
//	pain:      active und mitigated = open, resolved = done, accepted und obsolete = closed
//	idea:      open, accepted = done, dismissed = closed
//	feature:   hat keine Klasse (wie Wissen); is:active und is:retired sind die Zustände
//	task:      open = open, done und confirmed = done, dropped = closed
func StateClass(kind, state string) string {
	switch kind {
	case KindRequest:
		switch state {
		case "open":
			return ClassOpen
		case "done":
			return ClassDone
		case "dropped":
			return ClassClosed
		}
	case KindCriterion:
		switch state {
		case "open":
			return ClassOpen
		case "met":
			return ClassDone
		case "waived":
			return ClassClosed
		}
	case KindPain:
		switch state {
		case "active", "mitigated":
			return ClassOpen
		case "resolved":
			return ClassDone
		case "accepted", "obsolete":
			return ClassClosed
		}
	case KindIdea:
		switch state {
		case "open":
			return ClassOpen
		case "accepted":
			return ClassDone
		case "dismissed":
			return ClassClosed
		}
	case KindTask:
		switch state {
		case "open":
			return ClassOpen
		case "done", "confirmed":
			return ClassDone
		case "dropped":
			return ClassClosed
		}
	}
	return ""
}

// rawStates sind die Zustandsnamen, die is: zusätzlich zu den Klassen kennt.
var rawStates = map[string]bool{
	"dropped": true, "met": true, "waived": true,
	"active": true, "mitigated": true, "resolved": true, "obsolete": true,
	"accepted": true, "dismissed": true, "confirmed": true, "retired": true,
}

// Schlüssel der Qualifier, kanonisch.
const (
	KeyIs      = "is"
	KeyKind    = "kind"
	KeyType    = "type"
	KeyRepo    = "repo"
	KeyBranch  = "branch"
	KeyCommit  = "commit"
	KeyPR      = "pr"
	KeyPackage = "package"
	KeyReq     = "req"
	KeyTask    = "task"
	KeyPain    = "pain"
	KeyIdea    = "idea"
	KeyFeature = "feature"
	KeyParent  = "parent"
	KeyBlocks  = "blocks"
	KeyAuthor  = "author"
	KeyFor     = "for"
	KeyLabel   = "label"
	KeyArea    = "area"
	KeyTopic   = "topic"
	KeyCreated = "created"
	KeyUpdated = "updated"
	KeySince   = "since"
	KeyUntil   = "until"
)

var keyAlias = map[string]string{
	"is": KeyIs, "kind": KeyKind, "type": KeyType, "repo": KeyRepo, "branch": KeyBranch,
	"commit": KeyCommit, "pr": KeyPR, "package": KeyPackage, "pkg": KeyPackage,
	"req": KeyReq, "task": KeyTask, "pain": KeyPain, "idea": KeyIdea,
	"feature": KeyFeature, "feat": KeyFeature,
	"parent": KeyParent, "blocks": KeyBlocks,
	"author": KeyAuthor, "for": KeyFor, "assignee": KeyFor,
	"label": KeyLabel, "area": KeyArea, "topic": KeyTopic,
	"created": KeyCreated, "updated": KeyUpdated, "since": KeySince, "until": KeyUntil,
}

// KnownKeys für die Fehlermeldung, sortiert.
func KnownKeys() []string {
	seen := map[string]bool{}
	for _, k := range keyAlias {
		seen[k] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Num ist eine Zahl oder ein Zahlenbereich, beide Grenzen einschließlich.
type Num struct {
	Lo, Hi       int64
	HasLo, HasHi bool
}

// Has sagt, ob n im Bereich liegt.
func (n Num) Has(v int64) bool {
	return (!n.HasLo || v >= n.Lo) && (!n.HasHi || v <= n.Hi)
}

// Exact ist die Zahl, wenn der Bereich genau eine ist.
func (n Num) Exact() (int64, bool) {
	if n.HasLo && n.HasHi && n.Lo == n.Hi {
		return n.Lo, true
	}
	return 0, false
}

// Span ist ein Zeitraum als RFC3339 in UTC, beide Grenzen einschließlich, leer
// heißt offen.
type Span struct{ From, To string }

// Has sagt, ob t (RFC3339) im Zeitraum liegt. Ein leerer Zeitpunkt liegt nie darin.
func (s Span) Has(t string) bool {
	if t == "" {
		return false
	}
	return (s.From == "" || t >= s.From) && (s.To == "" || t <= s.To)
}

// Term ist ein Qualifier.
type Term struct {
	Key string
	Neg bool
	// Raw ist der Wert, wie er geschrieben wurde (für Ausgabe und Fehler).
	Raw string
	// Values sind die Alternativen als Text, kleingeschrieben bei Schlüsseln, die
	// Groß- und Kleinschreibung nicht unterscheiden.
	Values []string
	// Nums sind die Alternativen bei Zahlen-Schlüsseln, Spans bei Zeit-Schlüsseln.
	Nums  []Num
	Spans []Span
}

// Query ist eine geparste Suchzeile.
type Query struct {
	// Text ist der freie Text ohne Qualifier, Wörter durch Leerzeichen verbunden;
	// Anführungszeichen bleiben stehen.
	Text  string
	Terms []Term
}

// Error ist ein Fehler der Suchzeile. Er nennt das Wort und was zu tun ist.
type Error struct {
	Token string
	Msg   string
	// Unknown ist der Schlüssel, wenn der Fehler ein unbekannter Qualifier ist
	// (ohne Doppelpunkt, wie geschrieben), sonst leer. Oberflächen sagen damit
	// in ihrer Sprache, welcher Qualifier es war.
	Unknown string
}

func (e *Error) Error() string { return fmt.Sprintf("search query: %s (at %q)", e.Msg, e.Token) }

func fail(tok, format string, a ...any) error {
	return &Error{Token: tok, Msg: fmt.Sprintf(format, a...)}
}

// split zerlegt die Zeile in Wörter. Ein Anführungszeichen schließt Leerzeichen
// ein, auch mitten im Wort (label:"good first").
func split(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inQuote, started := false, false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
			started = true
		case unicode.IsSpace(r) && !inQuote:
			if started {
				out = append(out, cur.String())
				cur.Reset()
				started = false
			}
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	if inQuote {
		return nil, fail(s, "a quotation mark is not closed")
	}
	if started {
		out = append(out, cur.String())
	}
	return out, nil
}

// keyOf liefert Schlüssel und Wert, wenn das Wort wie ein Qualifier aussieht.
func keyOf(word string) (key, value string, ok bool) {
	i := strings.IndexByte(word, ':')
	if i <= 0 {
		return "", "", false
	}
	key = word[:i]
	for _, r := range key {
		if r < 'a' || r > 'z' {
			return "", "", false
		}
	}
	value = word[i+1:]
	if strings.HasPrefix(value, "//") {
		return "", "", false
	}
	return key, value, true
}

// unquote entfernt Anführungszeichen um einen Wert.
func unquote(v string) string {
	v = strings.ReplaceAll(v, `"`, "")
	return strings.TrimSpace(v)
}

// splitAlts trennt Alternativen am Komma außerhalb von Anführungszeichen.
func splitAlts(v string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	for _, r := range v {
		switch {
		case r == '"':
			inQuote = !inQuote
		case r == ',' && !inQuote:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	out = append(out, cur.String())
	return out
}

// Parse liest eine Suchzeile. now dient den relativen Zeitangaben (7d).
func Parse(line string, now time.Time) (Query, error) {
	words, err := split(line)
	if err != nil {
		return Query{}, err
	}
	var q Query
	var text []string
	for _, w := range words {
		neg := false
		body := w
		if strings.HasPrefix(body, "-") && len(body) > 1 {
			neg = true
			body = body[1:]
		}
		key, value, ok := keyOf(body)
		if !ok {
			if neg {
				return Query{}, fail(w, "a word cannot be negated, only a qualifier such as -label:bug")
			}
			text = append(text, w)
			continue
		}
		canon, known := keyAlias[key]
		if !known {
			e := fail(w, "unknown qualifier %q; known qualifiers: %s", key+":", strings.Join(KnownKeys(), ", ")).(*Error)
			e.Unknown = key
			return Query{}, e
		}
		t, err := parseTerm(canon, neg, value, w, now)
		if err != nil {
			return Query{}, err
		}
		q.Terms = append(q.Terms, t)
	}
	q.Text = strings.Join(text, " ")
	return q, nil
}

func parseTerm(key string, neg bool, value, word string, now time.Time) (Term, error) {
	t := Term{Key: key, Neg: neg, Raw: unquote(value)}
	if t.Raw == "" {
		return Term{}, fail(word, "%s: needs a value", key)
	}
	for _, alt := range splitAlts(value) {
		alt = strings.TrimSpace(alt)
		if alt == "" {
			return Term{}, fail(word, "%s: empty alternative", key)
		}
		switch key {
		case KeyPR, KeyPackage, KeyReq, KeyTask, KeyPain, KeyIdea, KeyFeature, KeyParent, KeyBlocks:
			n, err := parseNum(key, alt)
			if err != nil {
				return Term{}, fail(word, "%v", err)
			}
			t.Nums = append(t.Nums, n)
		case KeyCreated, KeyUpdated, KeySince, KeyUntil:
			sp, err := parseSpan(key, alt, now)
			if err != nil {
				return Term{}, fail(word, "%v", err)
			}
			t.Spans = append(t.Spans, sp)
		case KeyIs:
			v := strings.ToLower(unquote(alt))
			if _, isKind := kindAlias[v]; isKind {
				t.Values = append(t.Values, kindAlias[v])
				continue
			}
			if v != ClassOpen && v != ClassClosed && v != ClassDone && !rawStates[v] {
				return Term{}, fail(word, "is:%s is not known; use open, closed, done, a state such as resolved or accepted, or a kind (%s)", alt, strings.Join(Kinds, ", "))
			}
			t.Values = append(t.Values, v)
		case KeyKind:
			v := strings.ToLower(unquote(alt))
			k, isKind := kindAlias[v]
			if !isKind {
				return Term{}, fail(word, "kind:%s is not known; use %s", alt, strings.Join(Kinds, ", "))
			}
			t.Values = append(t.Values, k)
		case KeyCommit:
			v := strings.ToLower(unquote(alt))
			if len(v) < 7 || strings.Trim(v, "0123456789abcdef") != "" {
				return Term{}, fail(word, "commit: needs at least 7 hexadecimal characters")
			}
			t.Values = append(t.Values, v)
		case KeyArea:
			a, err := originctx.NormalizeArea(unquote(alt))
			if err != nil {
				return Term{}, fail(word, "%v", err)
			}
			t.Values = append(t.Values, a)
		case KeyLabel, KeyTopic, KeyType, KeyAuthor, KeyFor, KeyRepo, KeyBranch:
			v := unquote(alt)
			if bad := controlOrLine(v); bad {
				return Term{}, fail(word, "%s: no control characters", key)
			}
			if key != KeyBranch {
				v = strings.ToLower(v)
			}
			t.Values = append(t.Values, v)
		}
	}
	return t, nil
}

func controlOrLine(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// numRef liest 596, #596 oder REQ-596 (Präfix je nach Schlüssel).
func numRef(key, s string) (int64, error) {
	s = strings.TrimSpace(unquote(s))
	up := strings.ToUpper(s)
	for _, p := range []string{"REQ-", "PAIN-", "IDEA-", "TASK-", "PKG-", "#"} {
		up = strings.TrimPrefix(up, p)
	}
	n, err := strconv.ParseInt(up, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s: %q is not a positive number", key, s)
	}
	return n, nil
}

func parseNum(key, v string) (Num, error) {
	if lo, hi, found := strings.Cut(v, ".."); found {
		var n Num
		if strings.TrimSpace(lo) != "" {
			x, err := numRef(key, lo)
			if err != nil {
				return n, err
			}
			n.Lo, n.HasLo = x, true
		}
		if strings.TrimSpace(hi) != "" {
			x, err := numRef(key, hi)
			if err != nil {
				return n, err
			}
			n.Hi, n.HasHi = x, true
		}
		if !n.HasLo && !n.HasHi {
			return n, fmt.Errorf("%s: a range needs a bound", key)
		}
		if n.HasLo && n.HasHi && n.Lo > n.Hi {
			return n, fmt.Errorf("%s: the range %q runs backwards", key, v)
		}
		return n, nil
	}
	for _, op := range []string{">=", "<=", ">", "<"} {
		if rest, ok := strings.CutPrefix(v, op); ok {
			x, err := numRef(key, rest)
			if err != nil {
				return Num{}, err
			}
			switch op {
			case ">=":
				return Num{Lo: x, HasLo: true}, nil
			case ">":
				return Num{Lo: x + 1, HasLo: true}, nil
			case "<=":
				return Num{Hi: x, HasHi: true}, nil
			default:
				return Num{Hi: x - 1, HasHi: true}, nil
			}
		}
	}
	x, err := numRef(key, v)
	if err != nil {
		return Num{}, err
	}
	return Num{Lo: x, Hi: x, HasLo: true, HasHi: true}, nil
}

// bound liest eine Zeitangabe und liefert Anfang und Ende des Zeitraums, den
// sie bezeichnet: ein Datum den ganzen Tag, ein Zeitpunkt genau sich.
func bound(key, v string, now time.Time) (from, to string, err error) {
	v = unquote(v)
	f, err := originctx.ParseTime(v, now, false)
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", key, err)
	}
	t, err := originctx.ParseTime(v, now, true)
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", key, err)
	}
	return f, t, nil
}

func parseSpan(key, v string, now time.Time) (Span, error) {
	if lo, hi, found := strings.Cut(v, ".."); found {
		var s Span
		if strings.TrimSpace(lo) != "" {
			f, _, err := bound(key, lo, now)
			if err != nil {
				return s, err
			}
			s.From = f
		}
		if strings.TrimSpace(hi) != "" {
			_, t, err := bound(key, hi, now)
			if err != nil {
				return s, err
			}
			s.To = t
		}
		if s.From == "" && s.To == "" {
			return s, fmt.Errorf("%s: a range needs a bound", key)
		}
		if s.From != "" && s.To != "" && s.From > s.To {
			return s, fmt.Errorf("%s: the range %q runs backwards", key, v)
		}
		return s, nil
	}
	for _, op := range []string{">=", "<=", ">", "<"} {
		if rest, ok := strings.CutPrefix(v, op); ok {
			f, t, err := bound(key, rest, now)
			if err != nil {
				return Span{}, err
			}
			switch op {
			case ">=":
				return Span{From: f}, nil
			case ">":
				return Span{From: plusSecond(t)}, nil
			case "<=":
				return Span{To: t}, nil
			default:
				return Span{To: minusSecond(f)}, nil
			}
		}
	}
	f, t, err := bound(key, v, now)
	if err != nil {
		return Span{}, err
	}
	if key == KeySince {
		return Span{From: f}, nil
	}
	if key == KeyUntil {
		return Span{To: t}, nil
	}
	return Span{From: f, To: t}, nil
}

func plusSecond(rfc string) string {
	t, _ := time.Parse(time.RFC3339, rfc)
	return t.Add(time.Second).UTC().Format(time.RFC3339)
}

func minusSecond(rfc string) string {
	t, _ := time.Parse(time.RFC3339, rfc)
	return t.Add(-time.Second).UTC().Format(time.RFC3339)
}

// Has sagt, ob es einen Qualifier dieses Schlüssels gibt (positiv oder negativ).
func (q Query) Has(key string) bool {
	for _, t := range q.Terms {
		if t.Key == key {
			return true
		}
	}
	return false
}

// Empty sagt, dass die Zeile weder Text noch Qualifier trägt.
func (q Query) Empty() bool { return strings.TrimSpace(q.Text) == "" && len(q.Terms) == 0 }

// Qualified sagt, ob die Zeile mindestens einen Qualifier trägt.
func (q Query) Qualified() bool { return len(q.Terms) > 0 }

// Kinds sind die Objektarten, die die Zeile zulässt: die positiven is:/kind:
// und die Nummern-Qualifier (req:5 meint Requests). nil heißt alle. Die Arten
// aus verneinten Qualifiern bleiben außen vor; die schließt Match aus.
func (q Query) Kinds() []string {
	var out []string
	add := func(vs ...string) {
		for _, v := range vs {
			if !contains(out, v) {
				out = append(out, v)
			}
		}
	}
	restricted := false
	// is: und kind: schränken je Qualifier weiter ein: der Schnitt über alle.
	// Die Nummern-Qualifier (req:, pain:, idea:, task:) sind untereinander
	// Alternativen und bilden zusammen eine Gruppe.
	var groups [][]string
	var numbered []string
	for _, t := range q.Terms {
		if t.Neg {
			continue
		}
		switch t.Key {
		case KeyIs, KeyKind:
			var ks []string
			for _, v := range t.Values {
				if _, ok := kindAlias[v]; ok && contains(Kinds, v) {
					ks = append(ks, v)
				}
			}
			if t.Key == KeyKind || len(ks) == len(t.Values) {
				groups = append(groups, ks)
			}
		case KeyReq, KeyPain, KeyIdea, KeyTask:
			for _, k := range numKind(t.Key) {
				if !contains(numbered, k) {
					numbered = append(numbered, k)
				}
			}
		}
	}
	if numbered != nil {
		groups = append(groups, numbered)
	}
	for i, g := range groups {
		restricted = true
		if i == 0 {
			add(g...)
			continue
		}
		var keep []string
		for _, k := range out {
			if contains(g, k) {
				keep = append(keep, k)
			}
		}
		out = keep
	}
	if !restricted {
		return nil
	}
	if out == nil {
		return []string{}
	}
	return out
}

// WantsKind sagt, ob die Objektart nach den Qualifiern in Frage kommt.
func (q Query) WantsKind(kind string) bool {
	if ks := q.Kinds(); ks != nil && !contains(ks, kind) {
		return false
	}
	for _, t := range q.Terms {
		if t.Neg && (t.Key == KeyIs || t.Key == KeyKind) && len(t.Values) > 0 && allKinds(t.Values) && contains(t.Values, kind) {
			return false
		}
	}
	return true
}

func allKinds(vs []string) bool {
	for _, v := range vs {
		if !contains(Kinds, v) {
			return false
		}
	}
	return true
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// Values sind alle Textwerte der positiven Qualifier eines Schlüssels.
func (q Query) Values(key string) []string {
	var out []string
	for _, t := range q.Terms {
		if t.Key == key && !t.Neg {
			out = append(out, t.Values...)
		}
	}
	return out
}

// Topics sind die Namen aus topic:, positiv und verneint.
func (q Query) Topics() []string {
	var out []string
	for _, t := range q.Terms {
		if t.Key == KeyTopic {
			for _, v := range t.Values {
				if !contains(out, v) {
					out = append(out, v)
				}
			}
		}
	}
	return out
}

// ResolveTopics ersetzt topic:name durch area: mit den Präfixen des Themas.
// Ein unbekanntes Thema ist ein Fehler, kein leeres Ergebnis. Mehrere Themen
// (und area:) im selben Qualifier sind ODER-verknüpft.
func (q Query) ResolveTopics(prefixes func(name string) ([]string, bool)) (Query, error) {
	var out []Term
	for _, t := range q.Terms {
		if t.Key != KeyTopic {
			out = append(out, t)
			continue
		}
		var areas []string
		for _, name := range t.Values {
			p, ok := prefixes(name)
			if !ok {
				return Query{}, fail("topic:"+name, "there is no topic %q in this project", name)
			}
			areas = append(areas, p...)
		}
		out = append(out, Term{Key: KeyArea, Neg: t.Neg, Raw: t.Raw, Values: areas})
	}
	q.Terms = out
	return q, nil
}

// ResolveMe ersetzt @me in author: und for: durch den Namen des Fragenden.
func (q Query) ResolveMe(person string) Query {
	out := make([]Term, len(q.Terms))
	copy(out, q.Terms)
	for i, t := range out {
		if t.Key != KeyAuthor && t.Key != KeyFor {
			continue
		}
		vs := make([]string, len(t.Values))
		for j, v := range t.Values {
			if v == "@me" {
				v = strings.ToLower(strings.TrimSpace(person))
			}
			vs[j] = v
		}
		out[i].Values = vs
	}
	q.Terms = out
	return q
}

// Origin ist der Teil der Zeile, den der Entstehungskontext in SQL beantwortet:
// die positiven Bereiche und die Zeitgrenzen aus since:/until:. Ein Bereich
// aus mehreren Qualifiern ist ODER-verknüpft, wie bei Spec.
func (q Query) Origin() originctx.Spec {
	var s originctx.Spec
	for _, t := range q.Terms {
		if t.Neg {
			continue
		}
		switch t.Key {
		case KeyArea:
			s.Areas = append(s.Areas, t.Values...)
		case KeySince:
			for _, sp := range t.Spans {
				if sp.From != "" && (s.Since == "" || sp.From > s.Since) {
					s.Since = sp.From
				}
			}
		case KeyUntil:
			for _, sp := range t.Spans {
				if sp.To != "" && (s.Until == "" || sp.To < s.Until) {
					s.Until = sp.To
				}
			}
		}
	}
	return s
}

// Commit ist der eine positive Commit-Präfix der Zeile, "" bei keinem oder
// mehreren (dann prüft nur Match).
func (q Query) Commit() string {
	vs := q.Values(KeyCommit)
	if len(vs) == 1 {
		return vs[0]
	}
	return ""
}

// PR ist die eine positive PR-Nummer, 0 bei keiner oder mehreren.
func (q Query) PR() int64 {
	var n int64
	count := 0
	for _, t := range q.Terms {
		if t.Key == KeyPR && !t.Neg {
			for _, x := range t.Nums {
				v, ok := x.Exact()
				if !ok {
					return 0
				}
				n = v
				count++
			}
		}
	}
	if count == 1 {
		return n
	}
	return 0
}

// Repo ist das eine positive Repo der Zeile, "" bei keinem oder mehreren.
func (q Query) Repo() string {
	vs := q.Values(KeyRepo)
	if len(vs) == 1 {
		return vs[0]
	}
	return ""
}

// String gibt die Zeile in kanonischer Form aus (für Fehler und Anzeige).
func (q Query) String() string {
	var parts []string
	if q.Text != "" {
		parts = append(parts, q.Text)
	}
	for _, t := range q.Terms {
		p := t.Key + ":" + t.Raw
		if t.Neg {
			p = "-" + p
		}
		if strings.ContainsAny(t.Raw, " \t") {
			p = strings.Replace(p, ":"+t.Raw, `:"`+t.Raw+`"`, 1)
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, " ")
}

// Facts sind die Eigenschaften eines Treffers, gegen die Match prüft. Was der
// Aufrufer nicht kennt, bleibt leer: ein positiver Qualifier auf ein leeres
// Feld trifft nicht, ein verneinter schon.
type Facts struct {
	Kind  string
	State string
	Type  string
	Repo  string
	// Number ist die Nummer des Objekts (REQ, PAIN, IDEA, TASK); bei einem
	// Kriterium die seines Requests.
	Number   int64
	Author   string
	Assignee string
	Branches []string
	Commits  []string
	PRs      []int64
	Packages []int64
	Labels   []string
	Areas    []string
	Parents  []int64
	Blocks   []int64
	// Features sind die Features, auf die sich das Objekt bezieht (REQ-612): eine
	// Idee, die ein Feature ändert, ein Issue, das es ändert oder aus dem es
	// entstand. Bei einem Feature selbst steht seine Nummer in Number.
	Features []int64
	// Created und Updated sind RFC3339; OriginAt der Zeitpunkt des
	// Entstehungskontexts (sonst Created).
	Created, Updated, OriginAt string
}

func lowerAll(s []string) []string {
	out := make([]string, len(s))
	for i, v := range s {
		out[i] = strings.ToLower(v)
	}
	return out
}

// areaMatch: gleich, darunter oder darüber, an der Verzeichnisgrenze.
func areaMatch(have, want string) bool {
	have, want = path.Clean(have), path.Clean(want)
	return have == want || strings.HasPrefix(have, want+"/") || strings.HasPrefix(want, have+"/")
}

func repoMatch(have, want string) bool {
	have, want = strings.ToLower(have), strings.ToLower(want)
	return have != "" && (have == want || strings.HasSuffix(have, "/"+want))
}

func anyNum(have []int64, nums []Num) bool {
	for _, h := range have {
		for _, n := range nums {
			if n.Has(h) {
				return true
			}
		}
	}
	return false
}

// Match sagt, ob die Eigenschaften alle Qualifier erfüllen. Der freie Text
// gehört nicht dazu, den beantwortet die Volltextsuche.
func (q Query) Match(f Facts) bool {
	// Nummern-Qualifier derselben Art sind eine Gruppe: req:1 req:2 heißt eines
	// von beiden. Verschiedene Arten (req:1 pain:2) sind ebenfalls ODER, denn
	// ein Treffer hat nur eine Art.
	var numTerms, negNumTerms []Term
	for _, t := range q.Terms {
		switch t.Key {
		case KeyReq, KeyTask, KeyPain, KeyIdea:
			if t.Neg {
				negNumTerms = append(negNumTerms, t)
			} else {
				numTerms = append(numTerms, t)
			}
			continue
		}
		if matchTerm(t, f) == t.Neg {
			return false
		}
	}
	if len(numTerms) > 0 {
		ok := false
		for _, t := range numTerms {
			if matchTerm(t, f) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	for _, t := range negNumTerms {
		if matchTerm(t, f) {
			return false
		}
	}
	return true
}

// numKind sagt, welche Objektart ein Nummern-Qualifier meint.
func numKind(key string) []string {
	switch key {
	case KeyReq:
		return []string{KindRequest, KindCriterion}
	case KeyTask:
		return []string{KindTask}
	case KeyPain:
		return []string{KindPain}
	case KeyIdea:
		return []string{KindIdea}
	}
	return nil
}

// matchTerm sagt, ob der Treffer den Qualifier erfüllt, ohne die Verneinung.
func matchTerm(t Term, f Facts) bool {
	switch t.Key {
	case KeyIs:
		class := StateClass(f.Kind, f.State)
		for _, v := range t.Values {
			switch {
			case contains(Kinds, v):
				if f.Kind == v {
					return true
				}
			case v == ClassOpen:
				if class == ClassOpen {
					return true
				}
			case v == ClassDone:
				if class == ClassDone {
					return true
				}
			case v == ClassClosed:
				// geschlossen heißt nicht offen: erledigt zählt dazu.
				if class == ClassClosed || class == ClassDone {
					return true
				}
			default:
				if f.State == v {
					return true
				}
			}
		}
		return false
	case KeyKind:
		return contains(t.Values, f.Kind)
	case KeyType:
		return f.Type != "" && contains(t.Values, strings.ToLower(f.Type))
	case KeyRepo:
		for _, v := range t.Values {
			if repoMatch(f.Repo, v) {
				return true
			}
		}
		return false
	case KeyBranch:
		for _, v := range t.Values {
			if contains(f.Branches, v) {
				return true
			}
		}
		return false
	case KeyCommit:
		for _, v := range t.Values {
			for _, c := range f.Commits {
				if strings.HasPrefix(strings.ToLower(c), v) {
					return true
				}
			}
		}
		return false
	case KeyPR:
		return anyNum(f.PRs, t.Nums)
	case KeyPackage:
		return anyNum(f.Packages, t.Nums)
	case KeyParent:
		return anyNum(f.Parents, t.Nums)
	case KeyBlocks:
		return anyNum(f.Blocks, t.Nums)
	case KeyFeature:
		// feature:3 trifft FEAT-3 selbst und jedes Objekt, das sich auf es bezieht.
		if f.Kind == KindFeature {
			return anyNum([]int64{f.Number}, t.Nums)
		}
		return anyNum(f.Features, t.Nums)
	case KeyReq, KeyTask, KeyPain, KeyIdea:
		if !contains(numKind(t.Key), f.Kind) {
			return false
		}
		return anyNum([]int64{f.Number}, t.Nums)
	case KeyAuthor:
		return f.Author != "" && contains(t.Values, strings.ToLower(f.Author))
	case KeyFor:
		return f.Assignee != "" && contains(t.Values, strings.ToLower(f.Assignee))
	case KeyLabel:
		have := lowerAll(f.Labels)
		for _, v := range t.Values {
			if contains(have, v) {
				return true
			}
		}
		return false
	case KeyArea:
		for _, v := range t.Values {
			for _, a := range f.Areas {
				if areaMatch(a, v) {
					return true
				}
			}
		}
		return false
	case KeyTopic:
		// Ein Thema ist vor Match zu Bereichen aufgelöst (ResolveTopics).
		return false
	case KeyCreated:
		return anySpan(f.Created, t.Spans)
	case KeyUpdated:
		return anySpan(f.Updated, t.Spans)
	case KeySince, KeyUntil:
		at := f.OriginAt
		if at == "" {
			at = f.Created
		}
		return anySpan(at, t.Spans)
	}
	return false
}

func anySpan(at string, spans []Span) bool {
	for _, s := range spans {
		if s.Has(at) {
			return true
		}
	}
	return false
}
