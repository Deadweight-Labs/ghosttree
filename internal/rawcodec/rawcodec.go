// Package rawcodec packt die Rohzeilen der Transkripte (session_chunks.raw)
// verlustfrei: deflate (RFC 1951, compress/flate) mit einem vorab trainierten
// Wörterbuch von höchstens 32 KB.
//
// Gespeichert wird in einer von zwei SQLite-Speicherklassen, und die Klasse
// sagt, wie der Wert zu lesen ist:
//
//   - TEXT: die Zeile im Klartext. So stehen alle Zeilen vor der Kompression
//     da, und so bleiben kurze Zeilen, bei denen Packen nichts spart.
//   - BLOB: ein Rahmen. Das erste Byte ist der Codec, danach folgt, was der
//     Codec festlegt. CodecDeflate: die Wörterbuchversion als uvarint (0 heißt
//     ohne Wörterbuch), die CRC-32C (Castagnoli) der entpackten Zeile in vier
//     Bytes little-endian, dann der deflate-Strom. deflate selbst hat keine
//     Prüfsumme; ohne sie ergäbe ein falsches Wörterbuch still falschen Text.
//
// Ein Leser unterscheidet beide am Typ, den der Treiber liefert (string oder
// []byte), nie am Inhalt; eine Klartextzeile kann deshalb mit jedem Byte
// beginnen.
package rawcodec

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
	"sync"

	"compress/flate"
)

// CodecDeflate ist deflate mit optionalem Wörterbuch.
const CodecDeflate byte = 1

// DictSize ist die größte sinnvolle Wörterbuchgröße: deflate greift höchstens
// 32 KB zurück.
const DictSize = 32 << 10

// MinPack ist die kürzeste Zeile, die gepackt wird. Darunter frisst der Rahmen
// mit dem Ende des deflate-Stroms meist mehr, als das Packen spart.
const MinPack = 96

// Level ist die deflate-Stufe. Gemessen auf 132 MiB geschlankter Zeilen aus
// echten Transkripten mit einem trainierten Wörterbuch (compress/flate, Go
// 1.27): Stufe 6 ließ 33,4 % bei 13 MB/s, Stufe 8 32,1 % bei 31 MB/s, Stufe 9
// 32,0 % bei 25 MB/s. Mit Wörterbuch sind die mittleren Stufen hier langsamer
// als die hohen. Entpacken ist auf jeder Stufe gleich schnell.
const Level = 8

// MaxLine begrenzt, wie groß eine entpackte Zeile werden darf. Ein kaputter
// oder böswilliger Rahmen soll keinen beliebig großen Speicher anfordern.
const MaxLine = 256 << 20

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// ErrCorrupt meldet einen Rahmen, der sich nicht entpacken lässt.
var ErrCorrupt = errors.New("rawcodec: corrupt frame")

// ErrUnknownDict meldet eine Wörterbuchversion, die der Leser nicht kennt.
var ErrUnknownDict = errors.New("rawcodec: unknown dictionary")

// Dict ist ein Wörterbuch mit seiner Version. Version 0 ist das leere
// Wörterbuch (deflate ohne Vorgabe) und steht in keiner Tabelle.
type Dict struct {
	Version int64
	Data    []byte
}

// Encoder packt Zeilen mit einem festen Wörterbuch. Er ist sicher für
// gleichzeitige Nutzung.
type Encoder struct {
	dict    Dict
	writers sync.Pool
	dec     *Decoder
}

// NewEncoder legt einen Encoder für d an. Ein Wörterbuch über DictSize wird
// abgelehnt: deflate sähe den Anfang nie.
func NewEncoder(d Dict) (*Encoder, error) {
	if len(d.Data) > DictSize {
		return nil, fmt.Errorf("rawcodec: dictionary of %d bytes exceeds %d", len(d.Data), DictSize)
	}
	if d.Version < 0 || (d.Version == 0) != (len(d.Data) == 0) {
		return nil, fmt.Errorf("rawcodec: dictionary version %d with %d bytes", d.Version, len(d.Data))
	}
	e := &Encoder{dict: d}
	e.dec = NewDecoder(func(v int64) ([]byte, error) {
		if v == d.Version {
			return d.Data, nil
		}
		return nil, ErrUnknownDict
	})
	return e, nil
}

// Version ist die Wörterbuchversion, mit der der Encoder packt.
func (e *Encoder) Version() int64 { return e.dict.Version }

// Encode liefert, was für line gespeichert wird: die Zeile selbst (als string,
// SQLite-TEXT) oder einen Rahmen (als []byte, SQLite-BLOB). Gepackt wird nur,
// wenn der Rahmen kürzer ist. Jeder Rahmen wird vor der Rückgabe entpackt und
// mit line verglichen; weicht er ab, geht die Zeile im Klartext zurück. Ein
// Fehler im Codec kostet so Platz, nie Inhalt.
func (e *Encoder) Encode(line string) any {
	if len(line) < MinPack {
		return line
	}
	frame, err := e.pack(line)
	if err != nil || len(frame) >= len(line) {
		return line
	}
	if back, err := e.dec.Decode(frame); err != nil || back != line {
		return line
	}
	return frame
}

func (e *Encoder) pack(line string) ([]byte, error) {
	var buf bytes.Buffer
	buf.Grow(len(line)/3 + 16)
	buf.WriteByte(CodecDeflate)
	var v [binary.MaxVarintLen64]byte
	buf.Write(v[:binary.PutUvarint(v[:], uint64(e.dict.Version))])
	buf.Write(binary.LittleEndian.AppendUint32(nil, crc32.Checksum([]byte(line), castagnoli)))
	w, _ := e.writers.Get().(*flate.Writer)
	if w == nil {
		var err error
		if w, err = flate.NewWriterDict(&buf, Level, e.dict.Data); err != nil {
			return nil, err
		}
	} else {
		w.Reset(&buf)
	}
	defer e.writers.Put(w)
	if _, err := io.WriteString(w, line); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Decoder entpackt Rahmen. dicts liefert das Wörterbuch einer Version (nie für
// 0); es wird je Version einmal gefragt und das Ergebnis behalten, denn ein
// Wörterbuch ändert sich nie.
type Decoder struct {
	dicts   func(int64) ([]byte, error)
	mu      sync.RWMutex
	known   map[int64][]byte
	readers sync.Pool
}

// NewDecoder legt einen Decoder an.
func NewDecoder(dicts func(int64) ([]byte, error)) *Decoder {
	return &Decoder{dicts: dicts, known: map[int64][]byte{0: nil}}
}

// Add macht ein Wörterbuch bekannt, ohne dicts zu fragen. Für einen Aufrufer,
// der es auf eigenem Weg geladen hat (in einer offenen Transaktion).
func (d *Decoder) Add(v int64, data []byte) {
	d.mu.Lock()
	d.known[v] = data
	d.mu.Unlock()
}

func (d *Decoder) dict(v int64) ([]byte, error) {
	d.mu.RLock()
	b, ok := d.known[v]
	d.mu.RUnlock()
	if ok {
		return b, nil
	}
	b, err := d.dicts(v)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.known[v] = b
	d.mu.Unlock()
	return b, nil
}

// Decode entpackt einen Rahmen.
func (d *Decoder) Decode(frame []byte) (string, error) {
	if len(frame) < 2 {
		return "", ErrCorrupt
	}
	if frame[0] != CodecDeflate {
		return "", fmt.Errorf("%w: codec %d", ErrCorrupt, frame[0])
	}
	v, n := binary.Uvarint(frame[1:])
	if n <= 0 || v > 1<<62 || len(frame) < 1+n+4 {
		return "", fmt.Errorf("%w: header", ErrCorrupt)
	}
	sum := binary.LittleEndian.Uint32(frame[1+n:])
	dict, err := d.dict(int64(v))
	if err != nil {
		return "", fmt.Errorf("dictionary %d: %w", v, err)
	}
	src := bytes.NewReader(frame[1+n+4:])
	r, _ := d.readers.Get().(io.ReadCloser)
	if r == nil {
		r = flate.NewReaderDict(src, dict)
	} else if err := r.(flate.Resetter).Reset(src, dict); err != nil {
		return "", err
	}
	defer d.readers.Put(r)
	var out strings.Builder
	out.Grow(len(frame) * 4)
	m, err := io.Copy(&out, io.LimitReader(r, MaxLine+1))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if m > MaxLine {
		return "", fmt.Errorf("%w: line exceeds %d bytes", ErrCorrupt, MaxLine)
	}
	// Ein vollständiger Strom endet mit seinem letzten Block; Bytes dahinter
	// gehören nicht dazu und deuten auf einen verfälschten Rahmen.
	if src.Len() != 0 {
		return "", fmt.Errorf("%w: %d trailing bytes", ErrCorrupt, src.Len())
	}
	line := out.String()
	if crc32.Checksum([]byte(line), castagnoli) != sum {
		return "", fmt.Errorf("%w: checksum (wrong dictionary?)", ErrCorrupt)
	}
	return line, nil
}

// Value ist ein gelesener Wert von raw: Klartext oder Rahmen. Er erfüllt
// sql.Scanner und hält fest, in welcher Speicherklasse der Wert stand.
type Value struct {
	Text  string
	Frame []byte
	// Packed ist wahr, wenn der Wert ein BLOB war.
	Packed bool
}

// Scan übernimmt einen Wert aus der Datenbank. string ist TEXT, []byte BLOB;
// der Rahmen wird kopiert, weil der Treiber seinen Puffer wiederverwenden darf.
func (v *Value) Scan(src any) error {
	switch x := src.(type) {
	case string:
		*v = Value{Text: x}
	case []byte:
		*v = Value{Frame: bytes.Clone(x), Packed: true}
	case nil:
		*v = Value{}
	default:
		return fmt.Errorf("rawcodec: cannot scan %T", src)
	}
	return nil
}

// Stored ist der Wert, wie er in der Datenbank steht, für einen Vergleich in
// SQL (WHERE raw = ?).
func (v Value) Stored() any {
	if v.Packed {
		return v.Frame
	}
	return v.Text
}

// StoredLen ist die Länge des gespeicherten Werts in Bytes.
func (v Value) StoredLen() int {
	if v.Packed {
		return len(v.Frame)
	}
	return len(v.Text)
}

// Line liefert die Zeile im Klartext.
func (v Value) Line(d *Decoder) (string, error) {
	if !v.Packed {
		return v.Text, nil
	}
	return d.Decode(v.Frame)
}

// FrameDict liest die Wörterbuchversion aus einem Rahmen.
func FrameDict(frame []byte) (int64, bool) {
	if len(frame) < 2 || frame[0] != CodecDeflate {
		return 0, false
	}
	v, n := binary.Uvarint(frame[1:])
	if n <= 0 || v > 1<<62 {
		return 0, false
	}
	return int64(v), true
}
