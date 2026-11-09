package rawcodec

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// sampleLines sind Zeilen, wie sie Claude Code und Codex schreiben, mit
// wechselndem Inhalt.
func sampleLines(n int, seed uint64) []string {
	rng := rand.New(rand.NewPCG(seed, 7))
	words := []string{"store", "session", "chunk", "func", "return", "err", "nil", "test", "über", "Größe", "🙂", "\\n", "\\\"", "{", "}"}
	text := func(k int) string {
		var b strings.Builder
		for i := 0; i < k; i++ {
			b.WriteString(words[rng.IntN(len(words))])
			b.WriteByte(' ')
		}
		return b.String()
	}
	out := make([]string, n)
	for i := range out {
		switch i % 3 {
		case 0:
			out[i] = fmt.Sprintf(`{"parentUuid":"%08x-1111-2222-3333-444455556666","isSidechain":false,"userType":"external","cwd":"/home/u/Projects/ghosttree","sessionId":"abc","version":"2.1.301","gitBranch":"main","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":%q}]},"uuid":"%08x","timestamp":"2026-11-09T10:%02d:00Z"}`, rng.Uint32(), text(5+rng.IntN(200)), rng.Uint32(), i%60)
		case 1:
			out[i] = fmt.Sprintf(`{"timestamp":"2026-11-09T10:00:00Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_%d","output":%q}}`, i, text(rng.IntN(400)))
		default:
			out[i] = fmt.Sprintf(`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_%d","type":"tool_result","content":%q}]}}`, i, text(rng.IntN(50)))
		}
	}
	return out
}

func trainedEncoder(t testing.TB) *Encoder {
	t.Helper()
	var samples [][]byte
	for _, l := range sampleLines(600, 1) {
		samples = append(samples, []byte(l))
	}
	d := Train(samples, DictSize)
	if len(d) == 0 || len(d) > DictSize {
		t.Fatalf("dictionary of %d bytes", len(d))
	}
	e, err := NewEncoder(Dict{Version: 3, Data: d})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func decoderFor(e *Encoder) *Decoder {
	return NewDecoder(func(v int64) ([]byte, error) {
		if v == e.dict.Version {
			return e.dict.Data, nil
		}
		return nil, ErrUnknownDict
	})
}

func roundtrip(t testing.TB, e *Encoder, d *Decoder, line string) any {
	t.Helper()
	stored := e.Encode(line)
	var v Value
	if err := v.Scan(stored); err != nil {
		t.Fatal(err)
	}
	got, err := v.Line(d)
	if err != nil {
		t.Fatalf("decode %q: %v", line, err)
	}
	if got != line {
		t.Fatalf("roundtrip lost content:\nwant %q\ngot  %q", line, got)
	}
	return stored
}

func TestRoundtripEveryLine(t *testing.T) {
	plain, err := NewEncoder(Dict{})
	if err != nil {
		t.Fatal(err)
	}
	trained := trainedEncoder(t)
	for _, e := range []*Encoder{plain, trained} {
		d := decoderFor(e)
		var in, out int
		for _, l := range sampleLines(3000, 2) {
			s := roundtrip(t, e, d, l)
			in += len(l)
			if b, ok := s.([]byte); ok {
				out += len(b)
			} else {
				out += len(s.(string))
			}
		}
		if out >= in {
			t.Fatalf("dictionary %d: %d bytes in, %d stored", e.Version(), in, out)
		}
		t.Logf("dictionary %d: %d -> %d bytes (%.1f%%)", e.Version(), in, out, 100*float64(out)/float64(in))
	}
}

func TestTrainedDictionaryBeatsNone(t *testing.T) {
	plain, _ := NewEncoder(Dict{})
	trained := trainedEncoder(t)
	size := func(e *Encoder) (n int) {
		for _, l := range sampleLines(1000, 9) {
			switch s := e.Encode(l).(type) {
			case []byte:
				n += len(s)
			case string:
				n += len(s)
			}
		}
		return n
	}
	if a, b := size(plain), size(trained); b >= a {
		t.Fatalf("trained dictionary stores %d bytes, none %d", b, a)
	}
}

func TestShortAndIncompressibleStayText(t *testing.T) {
	e := trainedEncoder(t)
	if s, ok := e.Encode(`{"type":"x"}`).(string); !ok || s != `{"type":"x"}` {
		t.Fatalf("short line packed: %#v", s)
	}
	rng := rand.New(rand.NewPCG(1, 2))
	noise := make([]byte, 4000)
	for i := range noise {
		noise[i] = byte(rng.Uint32())
	}
	if _, ok := e.Encode(string(noise)).(string); !ok {
		t.Fatal("random bytes were packed although they cannot shrink")
	}
	// Klartext darf mit jedem Byte beginnen, auch mit dem Codec-Byte.
	odd := string([]byte{CodecDeflate, 0}) + strings.Repeat("abc", 100)
	roundtrip(t, e, decoderFor(e), odd)
	var v Value
	_ = v.Scan(odd)
	if got, _ := v.Line(decoderFor(e)); got != odd {
		t.Fatal("text that starts with the codec byte was decoded as a frame")
	}
}

func TestCorruptFramesFail(t *testing.T) {
	e := trainedEncoder(t)
	d := decoderFor(e)
	line := sampleLines(1, 4)[0] + strings.Repeat(" padding", 50)
	frame, ok := e.Encode(line).([]byte)
	if !ok {
		t.Fatal("line was not packed")
	}
	cases := map[string][]byte{
		"empty":         {},
		"codec only":    {CodecDeflate},
		"unknown codec": append([]byte{9}, frame[1:]...),
		"truncated":     frame[:len(frame)/2],
		"trailing":      append(bytes.Clone(frame), 0, 1, 2),
	}
	for name, f := range cases {
		if got, err := d.Decode(f); err == nil && got == line {
			t.Errorf("%s: decoded to the original line", name)
		} else if err == nil {
			t.Errorf("%s: no error, got %d bytes", name, len(got))
		}
	}
	other := NewDecoder(func(int64) ([]byte, error) { return nil, ErrUnknownDict })
	if _, err := other.Decode(frame); !errors.Is(err, ErrUnknownDict) {
		t.Fatalf("unknown dictionary: %v", err)
	}
}

func TestDecoderAsksForADictionaryOnce(t *testing.T) {
	e := trainedEncoder(t)
	calls := 0
	d := NewDecoder(func(v int64) ([]byte, error) { calls++; return e.dict.Data, nil })
	for _, l := range sampleLines(50, 5) {
		roundtrip(t, e, d, l)
	}
	if calls != 1 {
		t.Fatalf("dictionary looked up %d times", calls)
	}
}

func TestEncoderRejectsBadDictionaries(t *testing.T) {
	for _, d := range []Dict{
		{Version: 1},
		{Version: 0, Data: []byte("x")},
		{Version: 1, Data: make([]byte, DictSize+1)},
		{Version: -1, Data: []byte("x")},
	} {
		if _, err := NewEncoder(d); err == nil {
			t.Errorf("accepted version %d with %d bytes", d.Version, len(d.Data))
		}
	}
}

func TestTrainIsDeterministicAndBounded(t *testing.T) {
	var samples [][]byte
	for _, l := range sampleLines(300, 6) {
		samples = append(samples, []byte(l))
	}
	a, b := Train(samples, 4096), Train(samples, 4096)
	if !bytes.Equal(a, b) {
		t.Fatal("same sample, different dictionaries")
	}
	if len(a) == 0 || len(a) > 4096 {
		t.Fatalf("dictionary of %d bytes", len(a))
	}
	if Train(nil, DictSize) != nil || Train([][]byte{[]byte("short")}, DictSize) != nil {
		t.Fatal("dictionary from nothing")
	}
	// Eine riesige Zeile neben kleinen: das Training bleibt in Grenzen.
	big := bytes.Repeat([]byte(`{"a":"bcdefghij"}`), 200000)
	if d := Train(append(samples, big), DictSize); len(d) > DictSize {
		t.Fatalf("dictionary of %d bytes", len(d))
	}
}

func FuzzRoundtrip(f *testing.F) {
	for _, l := range sampleLines(20, 8) {
		f.Add(l)
	}
	f.Add("")
	f.Add(string([]byte{CodecDeflate, 0, 0, 0}))
	f.Add(strings.Repeat("\x00", 500))
	e := trainedEncoder(f)
	plain, _ := NewEncoder(Dict{})
	f.Fuzz(func(t *testing.T, line string) {
		roundtrip(t, e, decoderFor(e), line)
		roundtrip(t, plain, decoderFor(plain), line)
	})
}

func FuzzDecodeNeverPanics(f *testing.F) {
	e := trainedEncoder(f)
	for _, l := range sampleLines(10, 10) {
		if b, ok := e.Encode(l + strings.Repeat(" x", 80)).([]byte); ok {
			f.Add(b)
		}
	}
	f.Add([]byte{CodecDeflate, 0})
	d := decoderFor(e)
	f.Fuzz(func(t *testing.T, frame []byte) {
		_, _ = d.Decode(frame)
	})
}

func TestWrongDictionaryIsAnErrorNotOtherText(t *testing.T) {
	e := trainedEncoder(t)
	var samples [][]byte
	for _, l := range sampleLines(400, 77) {
		samples = append(samples, []byte(strings.ToUpper(l)))
	}
	other := Train(samples, DictSize)
	wrong := NewDecoder(func(int64) ([]byte, error) { return other, nil })
	n := 0
	for _, l := range sampleLines(200, 3) {
		frame, ok := e.Encode(l).([]byte)
		if !ok {
			continue
		}
		n++
		if got, err := wrong.Decode(frame); err == nil {
			t.Fatalf("decoded with the wrong dictionary without error (same text: %v)", got == l)
		}
	}
	if n == 0 {
		t.Fatal("nothing was packed")
	}
	if v, ok := FrameDict(e.Encode(sampleLines(1, 1)[0] + strings.Repeat(" abc", 60)).([]byte)); !ok || v != 3 {
		t.Fatalf("FrameDict = %d, %v", v, ok)
	}
}
