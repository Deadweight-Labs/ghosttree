package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/blobstrip"
	"github.com/Deadweight-Labs/ghosttree/internal/rawcodec"
	"github.com/Deadweight-Labs/ghosttree/internal/redact"
	"github.com/Deadweight-Labs/ghosttree/internal/slim"
)

// session_raw_dicts hält die Wörterbücher, mit denen session_chunks.raw gepackt
// ist (internal/rawcodec). Ein Wörterbuch ändert sich nie und wird nie
// gelöscht: jede gepackte Zeile nennt seine Version, ohne es ist sie nicht mehr
// zu lesen. Die Auslöser verweigern beides auch einem Zugriff am Code vorbei.
// Ein neues Wörterbuch bekommt die nächste Version; ältere bleiben für die
// Zeilen, die sie benutzen.
//
// session_chunks.slim_version ist die Version der Regel aus internal/slim, nach
// der die Zeile geschlankt und gepackt ist; 0 für den Bestand davor. Neue Zeilen
// tragen die aktuelle, der Bestandslauf (SlimSessions) setzt sie nach.
const rawCodecSchema = `
CREATE TABLE IF NOT EXISTS session_raw_dicts(
  version INTEGER PRIMARY KEY CHECK(version > 0),
  dict BLOB NOT NULL CHECK(length(dict) BETWEEN 1 AND 32768),
  sha256 TEXT NOT NULL,
  sample_lines INTEGER NOT NULL DEFAULT 0,
  sample_bytes INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL);
CREATE TRIGGER IF NOT EXISTS session_raw_dicts_keep BEFORE DELETE ON session_raw_dicts BEGIN
  SELECT RAISE(ABORT, 'session_raw_dicts: a dictionary is never deleted, stored lines depend on it');
END;
CREATE TRIGGER IF NOT EXISTS session_raw_dicts_fixed BEFORE UPDATE ON session_raw_dicts BEGIN
  SELECT RAISE(ABORT, 'session_raw_dicts: a dictionary never changes, add a new version instead');
END;
`

func ensureRawCodec(db *sql.DB) error {
	if _, err := db.Exec(rawCodecSchema); err != nil {
		return err
	}
	return addColumnIfMissing(db, "session_chunks", "slim_version", "INTEGER NOT NULL DEFAULT 0")
}

// rawQueryer ist, was das Laden eines Wörterbuchs braucht: eine Datenbank oder
// eine offene Transaktion.
type rawQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// rawState ist der Codec einer Datenbankdatei: die bekannten Wörterbücher zum
// Entpacken und der Encoder mit dem neuesten zum Packen.
type rawState struct {
	dec *rawcodec.Decoder

	mu      sync.Mutex
	enc     *rawcodec.Encoder
	checked time.Time
}

// rawStates teilt den Codec zwischen allen Store-Werten einer Datei: der
// Schreiber, der Leser und jede Sicht (WithContext) haben je eine eigene
// *sql.DB oder eine Kopie des Stores. Schlüssel ist der absolute Pfad der
// Datei, bei einer Datenbank im Speicher die *sql.DB. Ein Wörterbuch ändert sich nie; träfe ein Schlüssel doch einmal eine
// andere Datei, schlüge die Prüfsumme jedes Rahmens an, statt falschen Text zu
// liefern.
var rawStates sync.Map // string oder *sql.DB → *rawState

// rawEncoderTTL ist, wie lange ein Encoder gilt, bevor nachgesehen wird, ob ein
// neueres Wörterbuch dazugekommen ist (etwa vom Bestandslauf neben dem Server).
const rawEncoderTTL = time.Minute

func rawStateFor(key any) *rawState {
	if v, ok := rawStates.Load(key); ok {
		return v.(*rawState)
	}
	st := &rawState{dec: rawcodec.NewDecoder(func(int64) ([]byte, error) { return nil, rawcodec.ErrUnknownDict })}
	v, _ := rawStates.LoadOrStore(key, st)
	return v.(*rawState)
}

func (s *Store) rawState() *rawState {
	if f := sqliteFilePath(s.path); f != "" {
		if abs, err := filepath.Abs(f); err == nil {
			return rawStateFor(abs)
		}
	}
	// In-memory databases share no file, and every one has its own handle.
	return rawStateFor(s.db)
}

// line liefert die Zeile im Klartext. Ein unbekanntes Wörterbuch wird über q
// geladen, damit es auch in einer Transaktion auf der einzigen Verbindung geht.
func (r *rawState) line(ctx context.Context, q rawQueryer, v rawcodec.Value) (string, error) {
	s, err := v.Line(r.dec)
	if err == nil || !errors.Is(err, rawcodec.ErrUnknownDict) {
		return s, err
	}
	ver, ok := rawcodec.FrameDict(v.Frame)
	if !ok {
		return "", err
	}
	var dict []byte
	if qerr := q.QueryRowContext(ctx, `SELECT dict FROM session_raw_dicts WHERE version = ?`, ver).Scan(&dict); qerr != nil {
		return "", fmt.Errorf("raw dictionary %d: %w", ver, qerr)
	}
	r.dec.Add(ver, dict)
	return v.Line(r.dec)
}

// encoder liefert den Encoder mit dem neuesten Wörterbuch. Ohne Wörterbuch
// (neue Datenbank, oder eine noch nicht migrierte Datei in einem Wartungslauf)
// packt er ohne Vorgabe.
func (r *rawState) encoder(ctx context.Context, q rawQueryer) *rawcodec.Encoder {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.enc != nil && time.Since(r.checked) < rawEncoderTTL {
		return r.enc
	}
	d, err := latestRawDict(ctx, q)
	r.checked = time.Now()
	if err != nil && r.enc != nil {
		// Ein Lesefehler hält das Packen nicht auf; der bisherige Encoder
		// bleibt bis zum nächsten Versuch.
		return r.enc
	}
	if r.enc == nil || r.enc.Version() != d.Version {
		enc, eerr := rawcodec.NewEncoder(d)
		if eerr != nil {
			enc, _ = rawcodec.NewEncoder(rawcodec.Dict{})
		} else if d.Version > 0 {
			r.dec.Add(d.Version, d.Data)
		}
		r.enc = enc
	}
	return r.enc
}

// forget lässt den Encoder beim nächsten Packen neu nachsehen.
func (r *rawState) forget() {
	r.mu.Lock()
	r.enc = nil
	r.mu.Unlock()
}

func latestRawDict(ctx context.Context, q rawQueryer) (rawcodec.Dict, error) {
	var d rawcodec.Dict
	err := q.QueryRowContext(ctx, `SELECT version, dict FROM session_raw_dicts ORDER BY version DESC LIMIT 1`).Scan(&d.Version, &d.Data)
	if errors.Is(err, sql.ErrNoRows) || isNoSuchTable(err) {
		return rawcodec.Dict{}, nil
	}
	return d, err
}

func isNoSuchTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}

// rawLine entpackt einen gelesenen Wert von raw.
func (s *Store) rawLine(v rawcodec.Value) (string, error) {
	return s.rawState().line(s.ctx(), s.db, v)
}

// rawEncoder ist der Encoder für neue Zeilen. Nachgesehen wird über den Leser,
// wo es einen gibt: die Verbindung des Schreibers ist eine einzige und kann
// gerade eine Transaktion halten.
func (s *Store) rawEncoder() *rawcodec.Encoder {
	db := s.db
	if s.reader != nil {
		db = s.reader.db
	}
	return s.rawState().encoder(s.ctx(), db)
}

// sanitizeRaw ist der Weg einer Rohzeile in die Datenbank: Bilder ersetzen,
// Geheimnisse schwärzen, schlanken (keep false: die Zeile fällt weg). Die
// Reihenfolge begründet sanitizeChunks.
func sanitizeRaw(raw string) (string, bool) {
	raw, _ = blobstrip.Strip(raw)
	return slim.Line(redact.RedactJSON(raw))
}

// ------------------------------------------------------------- Wörterbuch

// RawDictInfo beschreibt ein gespeichertes Wörterbuch.
type RawDictInfo struct {
	Version     int64  `json:"version"`
	Bytes       int    `json:"bytes"`
	SHA256      string `json:"sha256"`
	SampleLines int    `json:"sample_lines"`
	SampleBytes int64  `json:"sample_bytes"`
	CreatedAt   string `json:"created_at"`
}

// Woraus ein Wörterbuch gelernt wird: höchstens so viele zufällig gewählte
// Zeilen, jede auf rawDictLineCap gekürzt, zusammen höchstens
// rawDictSampleBytes. Lange Werkzeugausgaben sind in jeder Zeile anders und
// lehren das Wörterbuch nichts; was sich wiederholt, steht vorn.
const (
	rawDictSampleLines = 20000
	rawDictSampleBytes = 24 << 20
	rawDictLineCap     = 16 << 10
)

// sampleRawLines zieht eine Stichprobe aus session_chunks und schlankt jede
// Zeile, wie AppendChunks es tut: das Wörterbuch soll die Zeilen kennen, die
// künftig gespeichert werden, nicht den Ballast des Bestands. Jede Zeile ist
// ein Sprung über die Nummer im B-Baum; durchlaufen wird nichts.
func sampleRawLines(ctx context.Context, db *sql.DB, state *rawState, seed uint64) ([][]byte, int64, error) {
	var highest sql.NullInt64
	if err := db.QueryRowContext(ctx, maxChunkIDQuery).Scan(&highest); err != nil {
		return nil, 0, err
	}
	if highest.Int64 == 0 {
		return nil, 0, nil
	}
	stmt, err := db.PrepareContext(ctx, `SELECT id, raw FROM session_chunks WHERE id >= ? ORDER BY id LIMIT 1`)
	if err != nil {
		return nil, 0, err
	}
	defer stmt.Close()
	rng := rand.New(rand.NewPCG(seed, seed>>1|1))
	seen := map[int64]bool{}
	var out [][]byte
	var total int64
	for tries := 0; tries < 2*rawDictSampleLines && len(out) < rawDictSampleLines && total < rawDictSampleBytes; tries++ {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		var id int64
		var v rawcodec.Value
		err := stmt.QueryRowContext(ctx, 1+rng.Int64N(highest.Int64)).Scan(&id, &v)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, 0, err
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		line, err := state.line(ctx, db, v)
		if err != nil {
			continue
		}
		line, keep := sanitizeRaw(line)
		if !keep {
			continue
		}
		if len(line) > rawDictLineCap {
			line = line[:rawDictLineCap]
		}
		out = append(out, []byte(line))
		total += int64(len(line))
	}
	return out, total, nil
}

// trainRawDict lernt ein Wörterbuch aus einer Stichprobe von db und legt es als
// nächste Version ab. Mit zu wenigen Zeilen geschieht nichts (ok false).
func trainRawDict(ctx context.Context, db *sql.DB, state *rawState, seed uint64) (RawDictInfo, bool, error) {
	samples, total, err := sampleRawLines(ctx, db, state, seed)
	if err != nil || len(samples) < 2 {
		return RawDictInfo{}, false, err
	}
	dict := rawcodec.Train(samples, rawcodec.DictSize)
	if len(dict) == 0 {
		return RawDictInfo{}, false, nil
	}
	info, err := insertRawDict(ctx, db, dict, len(samples), total)
	if err != nil {
		return RawDictInfo{}, false, err
	}
	state.forget()
	return info, true, nil
}

func insertRawDict(ctx context.Context, q rawQueryer, dict []byte, lines int, bytes int64) (RawDictInfo, error) {
	sum := sha256.Sum256(dict)
	info := RawDictInfo{Bytes: len(dict), SHA256: hex.EncodeToString(sum[:]), SampleLines: lines, SampleBytes: bytes, CreatedAt: now()}
	err := q.QueryRowContext(ctx, `INSERT INTO session_raw_dicts(version, dict, sha256, sample_lines, sample_bytes, created_at)
		VALUES((SELECT COALESCE(MAX(version),0)+1 FROM session_raw_dicts), ?, ?, ?, ?, ?) RETURNING version`,
		dict, info.SHA256, lines, bytes, info.CreatedAt).Scan(&info.Version)
	return info, err
}

func listRawDicts(ctx context.Context, db *sql.DB) ([]RawDictInfo, error) {
	rows, err := db.QueryContext(ctx, `SELECT version, length(dict), sha256, sample_lines, sample_bytes, created_at FROM session_raw_dicts ORDER BY version DESC`)
	if isNoSuchTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RawDictInfo
	for rows.Next() {
		var d RawDictInfo
		if err := rows.Scan(&d.Version, &d.Bytes, &d.SHA256, &d.SampleLines, &d.SampleBytes, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// rawDictMinChunks ist, ab wie vielen Zeilen der Server von sich aus ein
// Wörterbuch lernt. Darunter ist die Stichprobe zu klein; die Zeilen werden
// ohne Vorgabe gepackt.
const rawDictMinChunks = 5000

// RawDictSample zieht die Stichprobe für ein erstes Wörterbuch, wenn noch keines
// da ist und die Datenbank genug Zeilen hat; sonst nil.
func (s *Store) RawDictSample(ctx context.Context, seed uint64) ([][]byte, int64, error) {
	if s.reader != nil {
		return s.reader.RawDictSample(ctx, seed)
	}
	var have int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM session_raw_dicts`).Scan(&have); err != nil || have > 0 {
		return nil, 0, err
	}
	var highest sql.NullInt64
	if err := s.db.QueryRowContext(ctx, maxChunkIDQuery).Scan(&highest); err != nil || highest.Int64 < rawDictMinChunks {
		return nil, 0, err
	}
	return sampleRawLines(ctx, s.db, s.rawState(), seed)
}

// AddRawDict legt ein gelerntes Wörterbuch als nächste Version ab; neue Zeilen
// werden ab dann damit gepackt.
func (s *Store) AddRawDict(ctx context.Context, dict []byte, lines int, bytes int64) (RawDictInfo, error) {
	if s.writer != nil {
		return queueContextValue(ctx, s, []any{dict, lines, bytes}, 0, func(d *Store, p []any) (RawDictInfo, error) {
			return d.AddRawDict(ctx, p[0].([]byte), p[1].(int), p[2].(int64))
		})
	}
	if len(dict) == 0 || len(dict) > rawcodec.DictSize {
		return RawDictInfo{}, fmt.Errorf("dictionary of %d bytes", len(dict))
	}
	info, err := insertRawDict(ctx, s.db, dict, lines, bytes)
	if err == nil {
		s.rawState().forget()
	}
	return info, err
}

// EnsureRawDict lernt ein erstes Wörterbuch, wenn keines da ist und die
// Datenbank genug Zeilen hat. Der Server ruft es nach dem Start im Hintergrund
// auf: die Stichprobe liest der Leser, gelernt wird auf dem Aufrufer, und der
// Schreiber legt nur die eine Zeile ab.
func EnsureRawDict(ctx context.Context, s *Store) (RawDictInfo, bool, error) {
	samples, total, err := s.RawDictSample(ctx, uint64(time.Now().UnixNano()))
	if err != nil || len(samples) < 2 {
		return RawDictInfo{}, false, err
	}
	dict := rawcodec.Train(samples, rawcodec.DictSize)
	if len(dict) == 0 {
		return RawDictInfo{}, false, nil
	}
	info, err := s.AddRawDict(ctx, dict, len(samples), total)
	return info, err == nil, err
}

// DecodeRaw entpackt einen Wert von session_chunks.raw, gelesen über db (als
// rawcodec.Value). Für Werkzeuge und Tests, die die Tabelle selbst lesen.
func DecodeRaw(ctx context.Context, db *sql.DB, v rawcodec.Value) (string, error) {
	return rawStateFor(db).line(ctx, db, v)
}
