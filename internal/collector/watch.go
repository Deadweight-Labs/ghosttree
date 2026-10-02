package collector

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/redact"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/fsnotify/fsnotify"
)

type Uploader interface {
	UpsertSession(s store.Session) (int64, error)
	AppendChunks(id int64, chunks []store.Chunk) error
}

// RefUploader ist optional: ein Uploader, der Sessions über Nummer oder Adresse
// anspricht. Ein Gast bekommt vom Server keine Nummer, nur die Adresse; ohne
// diese Schnittstelle gilt die Nummer wie bisher.
type RefUploader interface {
	UpsertSessionRef(s store.Session) (store.SessionRef, error)
	AppendChunksRef(ref store.SessionRef, chunks []store.Chunk) error
}

// ActivityRecorder ist optional und wird per Typprüfung erkannt.
//
// Optional, weil das Einsammeln von Transkripten seit jeher funktioniert,
// ohne Aktivität zu kennen — ein Uploader, der sie nicht kann, soll weiter
// funktionieren, statt an einer erweiterten Schnittstelle zu brechen. Und
// weil ein Fehler beim Verbuchen von Aktivität das Archivieren des
// Transkripts nicht scheitern lassen darf: der Text ist der Bestand, die
// Aktivität ist eine Ableitung daraus.
type ActivityRecorder interface {
	RecordPathActivity(events []store.PathActivity) error
}

// uploadBatch bounds request size during the initial import of old transcripts.
const uploadBatch = 500

// uploadBatchBytes bounds the serialized size of one request (text and raw line
// as JSON, escaping included); the server refuses bodies above 64 MiB. A request
// the server refuses anyway is halved (see uploadSplit).
var uploadBatchBytes = 24 << 20

// metaScanLines is how far into a file we look for the session metadata line.
const metaScanLines = 200

func parserFor(harness string) func([]byte) ParsedLine {
	if harness == "codex" {
		return ParseCodexLine
	}
	return ParseClaudeLine
}

// SyncFile uploads everything appended to path since the last confirmed run.
// The offset only advances after the server accepted a batch.
func SyncFile(path, harness string, up Uploader, st *State, machine string) error {
	fs := st.file(path)
	ref := store.SessionRef{ID: fs.SessionID, PublicID: fs.PublicID}
	if ref.Zero() || fs.MetadataVersion < 1 {
		var err error
		ref, err = registerSession(path, harness, up, machine)
		if err != nil {
			return err
		}
		fs.SessionID, fs.PublicID = ref.ID, ref.PublicID
		fs.MetadataVersion = 1
		if err := st.Save(); err != nil {
			return err
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(fs.Offset, io.SeekStart); err != nil {
		return err
	}
	parse := parserFor(harness)
	r := bufio.NewReaderSize(f, 1<<20)
	offset := fs.Offset
	var batch []store.Chunk
	batchBytes := 0
	var touches []store.PathActivity
	var proofs []ControlProof
	ident := sessionIdentity(path, harness, machine)
	seq := fs.Seq
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := uploadSplit(up, ref, batch); err != nil {
			return err
		}
		// Ein Pausenbeleg geht vor dem Offset-Fortschritt raus; scheitert er,
		// wird der Stapel wiederholt (der Server hält jeden Beleg nur einmal).
		if rec, ok := up.(ControlProofRecorder); ok {
			for _, p := range proofs {
				if err := rec.RecordControlProof(p.ControlID, store.ControlEvent{
					Kind: store.ControlEventProof, ToolUseID: p.ToolUseID, SessionID: ident.externalID,
				}); err != nil {
					return err
				}
			}
		}
		proofs = proofs[:0]
		// Erst nach dem bestätigten Upload und ohne den Lauf zu gefährden:
		// eine fehlgeschlagene Ableitung darf ein archiviertes Transkript
		// nicht zurücknehmen.
		if rec, ok := up.(ActivityRecorder); ok && len(touches) > 0 {
			// Ein Fehler hält das Archivieren nicht auf, bleibt aber nicht stumm:
			// ein 403 hieße, dass Aktivität dieser Session nirgends ankommt.
			if err := rec.RecordPathActivity(touches); err != nil {
				activityWarn.printf("activity of session %s not recorded: %v", ident.externalID, err)
			}
		}
		touches = touches[:0]
		fs.Offset = offset
		fs.Seq = seq
		batch = batch[:0]
		batchBytes = 0
		return st.Save()
	}
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			// A trailing line without newline is still being written: leave it.
			break
		}
		offset += int64(len(line))
		trimmed := strings.TrimRight(string(line), "\r\n")
		if trimmed == "" {
			// Blank lines still advance the offset once a batch is confirmed.
			continue
		}
		p := parse([]byte(trimmed))
		batch = append(batch, store.Chunk{
			Seq:  seq,
			Role: p.Role,
			Text: redact.Redact(p.Text),
			Raw:  redact.Redact(trimmed),
		})
		touches = append(touches, activityFrom(ident, trimmed)...)
		if harness != "codex" {
			if p, ok := ControlProofFrom([]byte(trimmed)); ok {
				proofs = append(proofs, p)
			}
		}
		seq++
		batchBytes += wireSize(batch[len(batch)-1])
		if len(batch) >= uploadBatch || batchBytes >= uploadBatchBytes {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}

// wireSize ist die Größe eines Chunks im Anfragekörper: Text und Rohzeile als
// JSON, mit Escapes. Gezählt wird, was der Server begrenzt, nicht der Rohtext.
func wireSize(c store.Chunk) int {
	b, err := json.Marshal(c)
	if err != nil {
		return len(c.Text) + len(c.Raw)
	}
	return len(b) + 1
}

// serverLineLimit ist die Größe, bis zu der der Server eine einzelne Zeile
// annimmt (server.maxChunkBody, 64 MiB). Nur eine Zeile darüber geht als
// Platzhalter hinaus; alles darunter ist ein anderes Limit, das der Betreiber
// beheben muss.
var serverLineLimit = 64 << 20

// uploadSplit lädt einen Stapel hoch. Lehnt der Server ihn als zu groß ab (413),
// wird er halbiert und beide Hälften einzeln gesendet; ein Wiederholen
// desselben Stapels käme nie durch. Eine einzelne Zeile, die auch allein über
// serverLineLimit liegt, geht als Platzhalter mit derselben Nummer hinaus, damit die Folge
// lückenlos bleibt und der Lauf weiterkommt.
func uploadSplit(up Uploader, ref store.SessionRef, batch []store.Chunk) error {
	err := sendChunks(up, ref, batch)
	if err == nil || !tooLarge(err) {
		return err
	}
	if len(batch) == 1 {
		c := batch[0]
		if wireSize(c) <= serverLineLimit {
			// Unter der Grenze des Servers: die Ablehnung kommt von woanders
			// (Proxy-Limit). Ein Platzhalter verlöre die Zeile für immer; die
			// Datei hält an und wird im nächsten Lauf neu versucht.
			err := fmt.Errorf("session line %d (%d bytes) was refused with 413 although it is within the server limit of %d bytes (a proxy limit?); file paused, nothing was replaced: %w", c.Seq, wireSize(c), serverLineLimit, err)
			log.Print(err)
			return err
		}
		note, _ := json.Marshal(map[string]any{"type": "ghosttree-omitted", "reason": "line too large for upload", "bytes": len(c.Raw)})
		log.Printf("session line %d is %d bytes and was refused by the server; uploading a marker instead", c.Seq, len(c.Raw))
		return sendChunks(up, ref, []store.Chunk{{Seq: c.Seq, Role: "other", Raw: string(note)}})
	}
	mid := len(batch) / 2
	if err := uploadSplit(up, ref, batch[:mid]); err != nil {
		return err
	}
	return uploadSplit(up, ref, batch[mid:])
}

func sendChunks(up Uploader, ref store.SessionRef, batch []store.Chunk) error {
	if ru, ok := up.(RefUploader); ok {
		return ru.AppendChunksRef(ref, batch)
	}
	return up.AppendChunks(ref.ID, batch)
}

// tooLarge sagt, ob der Server die Anfrage wegen ihrer Größe abgelehnt hat.
func tooLarge(err error) bool {
	var h interface{ HTTPStatus() int }
	return errors.As(err, &h) && h.HTTPStatus() == http.StatusRequestEntityTooLarge
}

func registerSession(path, harness string, up Uploader, machine string) (store.SessionRef, error) {
	head, err := firstLines(path, metaScanLines)
	if err != nil {
		return store.SessionRef{}, err
	}
	var externalID, cwd, project, branch string
	if harness == "codex" {
		externalID, cwd, project, branch = CodexSessionMeta(head)
	} else {
		externalID, cwd, branch = ClaudeSessionMeta(path, head)
	}
	if externalID == "" {
		externalID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	}
	if project == "" && cwd != "" {
		p, b := GitInfo(cwd)
		project = p
		if branch == "" {
			branch = b
		}
	}
	started := ""
	if fi, err := os.Stat(path); err == nil {
		started = fi.ModTime().UTC().Format(time.RFC3339)
	}
	sess := store.Session{
		Harness:    harness,
		ExternalID: externalID,
		Scope:      scope.Axes{Project: project, Branch: branch, Machine: machine},
		CWD:        cwd,
		StartedAt:  started,
	}
	if ru, ok := up.(RefUploader); ok {
		ref, err := ru.UpsertSessionRef(sess)
		if err == nil && ref.Zero() {
			err = errors.New("server named no session")
		}
		return ref, err
	}
	id, err := up.UpsertSession(sess)
	return store.SessionRef{ID: id}, err
}

func firstLines(path string, n int) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for len(out) < n && sc.Scan() {
		out = append(out, append([]byte(nil), sc.Bytes()...))
	}
	// A scan error here only limits metadata detection, never the sync itself.
	return out, nil
}

// Sweep syncs every transcript under the roots once.
func Sweep(roots map[string]string, up Uploader, st *State, machine string) error {
	for root, harness := range roots {
		err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
				return nil
			}
			if err := SyncFile(p, harness, up, st, machine); err != nil {
				log.Printf("sync %s: %v", p, err)
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			log.Printf("walk %s: %v", root, err)
		}
	}
	return st.Save()
}

// Watch reacts to filesystem events and additionally sweeps every interval,
// because fsnotify alone misses whatever changed while the daemon was down.
func Watch(roots map[string]string, up Uploader, st *State, machine string, interval time.Duration) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()
	harnessOf := func(p string) string {
		for root, h := range roots {
			if strings.HasPrefix(p, root) {
				return h
			}
		}
		return "claude-code"
	}
	addTree := func(root string) {
		filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				w.Add(p)
			}
			return nil
		})
	}
	for root := range roots {
		addTree(root)
	}
	if err := Sweep(roots, up, st, machine); err != nil {
		log.Printf("initial sweep: %v", err)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
				addTree(ev.Name)
				continue
			}
			if !strings.HasSuffix(ev.Name, ".jsonl") {
				continue
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}
			if err := SyncFile(ev.Name, harnessOf(ev.Name), up, st, machine); err != nil {
				log.Printf("sync %s: %v", ev.Name, err)
			}
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			log.Printf("watcher: %v", err)
		case <-ticker.C:
			for root := range roots {
				addTree(root)
			}
			if err := Sweep(roots, up, st, machine); err != nil {
				log.Printf("sweep: %v", err)
			}
		}
	}
}

// DefaultRoots maps the known transcript directories to their harness.
func DefaultRoots(home string) map[string]string {
	return map[string]string{
		filepath.Join(home, ".claude", "projects"): "claude-code",
		filepath.Join(home, ".codex", "sessions"):  "codex",
	}
}

// touchIdentity ist, was eine Aktivitätszeile über ihre Herkunft weiß.
type touchIdentity struct {
	externalID string
	project    string
	// checkout ist das Arbeitsverzeichnis der Session. Es unterscheidet zwei
	// Worktrees desselben Repos — derselbe Pfad dort ist ein anderes Risiko
	// als derselbe Pfad im selben Checkout.
	checkout string
}

// sessionIdentity liest Herkunft aus denselben Kopfzeilen, aus denen auch die
// Sitzung selbst gebildet wird. Bewusst noch einmal gelesen statt im
// Zustandsfile mitgeschleppt: der Zustand ist ein Fortschrittszähler, und ihn
// mit Fachdaten zu füllen macht jede spätere Änderung zu einer Migration.
func sessionIdentity(path, harness, machine string) touchIdentity {
	head, err := firstLines(path, metaScanLines)
	if err != nil {
		return touchIdentity{externalID: strings.TrimSuffix(filepath.Base(path), ".jsonl")}
	}
	var externalID, cwd, project string
	if harness == "codex" {
		externalID, cwd, project, _ = CodexSessionMeta(head)
	} else {
		externalID, cwd, _ = ClaudeSessionMeta(path, head)
	}
	if externalID == "" {
		externalID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	}
	if project == "" && cwd != "" {
		project, _ = GitInfo(cwd)
	}
	return touchIdentity{externalID: externalID, project: project, checkout: cwd}
}

// activityFrom zieht die Pfadbezüge einer Transkriptzeile.
//
// Was hier entsteht, ist ausschließlich ABSICHT: an dieser Stelle steht, dass
// ein Werkzeug mit diesem Pfad gerufen wurde. Ob es funktioniert hat, steht
// im Ergebnisblock und wird nicht mit verbucht — eine Absicht als Änderung zu
// zählen ist genau die Übertreibung, vor der beide Fassungen der Spec warnen.
func activityFrom(ident touchIdentity, line string) []store.PathActivity {
	var l struct {
		Message *struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal([]byte(line), &l) != nil || l.Message == nil {
		return nil
	}
	touches := ToolPathTouches(l.Message.Content)
	if len(touches) == 0 {
		return nil
	}
	at := time.Now().UTC().Format(time.RFC3339)
	out := make([]store.PathActivity, 0, len(touches))
	for _, t := range touches {
		out = append(out, store.PathActivity{
			Project: ident.project, SessionExternalID: ident.externalID,
			Checkout: ident.checkout, Tool: t.Tool, Path: t.Path,
			Writes: TouchWrites(t.Tool), Quality: store.ActivityIntent, At: at,
		})
	}
	return out
}

// limitedLog schreibt höchstens alle interval eine Zeile und zählt, was es
// unterdrückt hat. Der Collector ruft den Weg je Transkriptstapel; ein dauerhaft
// verweigerter Upload soll sichtbar sein, ohne das Log zu fluten.
type limitedLog struct {
	mu         sync.Mutex
	interval   time.Duration
	now        func() time.Time
	out        func(string)
	last       time.Time
	suppressed int
}

var activityWarn = &limitedLog{interval: time.Minute, now: time.Now, out: func(s string) { log.Print(s) }}

func (l *limitedLog) printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if !l.last.IsZero() && now.Sub(l.last) < l.interval {
		l.suppressed++
		return
	}
	msg := fmt.Sprintf(format, args...)
	if l.suppressed > 0 {
		msg += fmt.Sprintf(" (%d similar messages suppressed)", l.suppressed)
		l.suppressed = 0
	}
	l.last = now
	l.out(msg)
}
