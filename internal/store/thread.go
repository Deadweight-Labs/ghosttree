package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DormantAfter ist die Frist, nach der ein offener Thread ohne Aktivität als
// ruhend gilt. Spec §B5 nennt 14 Tage als Startwert für Tests.
//
// Ruhend wird ABGELEITET und nicht gespeichert. Das ist Absicht: ein neuer
// Beitrag macht den Thread damit von selbst wieder aktiv, ohne dass jemand
// einen Zustand zurücksetzen muss. Ein gespeicherter Ruhezustand wäre ein
// zweiter Wahrheitskanal neben updated_at und würde irgendwann davon
// abweichen.
const DormantAfter = 14 * 24 * time.Hour

// Thread-Zustände. Ruhend fehlt hier bewusst — siehe DormantAfter. Und
// archiviert ist kein Zustand, sondern eine Sichtbarkeitsentscheidung
// daneben: Spec §B5, "Archivierung und Löschung sind verschiedene Vorgänge",
// und beide sind etwas anderes als "die Frage ist beantwortet".
const (
	ThreadOpen     = "open"
	ThreadResolved = "resolved"
	ThreadDeferred = "deferred"
)

// Thread ist ein dauerhaftes Thema, keine Session und kein Request.
//
// Der entscheidende Satz dazu steht in der Spec §B6 und gilt für den ganzen
// Typ: ein Thread kann beendet sein, obwohl die Umsetzung erst beginnt.
// "Resolved" heißt, die Frage ist beantwortet — nicht, dass jemand etwas
// gebaut hat. Wer das verschmilzt, bekommt einen Agenten, der "wir sind uns
// einig" schreibt und damit einen Request schließt.
type Thread struct {
	ID       int64  `json:"id,omitempty"`
	Project  string `json:"project"`
	Title    string `json:"title"`
	Question string `json:"question,omitempty"`
	State    string `json:"state,omitempty"`
	Archived bool   `json:"archived,omitempty"`
	Person   string `json:"person,omitempty"`
	// Dormant ist abgeleitet und wird nicht gespeichert. Ruhend ist NICHT
	// gelöst: ein ruhender Thread trägt weiterhin state=open.
	Dormant    bool   `json:"dormant,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
	ResolvedAt string `json:"resolved_at,omitempty"`
}

// ThreadLink hängt einen Thread an ein bestehendes Ghosttree-Objekt. Derselbe
// Thread kann an mehreren hängen — das ist der Punkt: eine Diskussion an
// einem Pitfall, im Projektraum und an einem Request ist EINE Diskussion und
// nicht drei Kopien mit auseinanderlaufendem Verlauf.
//
// Revision trägt dieselbe Bedeutung wie bei CoordRef: leer heißt
// veränderlicher Head.
type ThreadLink struct {
	ThreadID  int64  `json:"thread_id,omitempty"`
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Revision  string `json:"revision,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// ThreadSummary ist eine ABGELEITETE Ansicht, kein Ersatz für den Verlauf.
//
// CoversThrough ist der Grund, warum sie überhaupt brauchbar ist: sie sagt,
// bis zu welcher Beitragssequenz sie reicht. Kommen danach Beiträge, ist sie
// sichtbar unvollständig, statt unbemerkt zu veralten. Spec §B3.
type ThreadSummary struct {
	ThreadID      int64  `json:"thread_id,omitempty"`
	Revision      int    `json:"revision"`
	Body          string `json:"body"`
	OpenQuestions string `json:"open_questions,omitempty"`
	CoversThrough int64  `json:"covers_through_sequence"`
	Person        string `json:"person,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
}

// Zustände eines Thread-Ergebnisses. Ein Thread SCHLÄGT VOR; die Übernahme
// ins dauerhafte Wissen ist eine eigene Entscheidung mit eigener Freigabe.
// Spec §B6: zwei zustimmende Agenten machen aus einer Vermutung keine
// geprüfte Tatsache, und dieselbe weitergereichte Behauptung ist nicht
// mehrfach belegt.
const (
	OutcomeProposed = "proposed"
	OutcomeAccepted = "accepted"
	OutcomeRejected = "rejected"
)

// ThreadOutcome verbindet einen Thread mit dem, was aus ihm hervorging —
// oder ausdrücklich nicht hervorging. Ein abgelehnter Vorschlag ist ein
// Ergebnis: er hält fest, was geprüft und verworfen wurde, und verhindert,
// dass dieselbe Idee in drei Monaten erneut durchdiskutiert wird.
type ThreadOutcome struct {
	ThreadID  int64  `json:"thread_id,omitempty"`
	Kind      string `json:"kind"`
	RefID     string `json:"ref_id"`
	State     string `json:"state,omitempty"`
	Note      string `json:"note,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	DecidedAt string `json:"decided_at,omitempty"`
}

// ThreadDestinationID bildet die Ziel-Kennung, unter der die Beiträge eines
// Threads liegen. Beiträge sind gewöhnliche CoordMessages mit
// destination_kind=discussion — dieselbe Primitive wie im Raum, wie Spec §3
// verlangt: gemeinsame Hülle, eigene Aggregate.
func ThreadDestinationID(threadID int64) string {
	return strconv.FormatInt(threadID, 10)
}

func (s *Store) CreateThread(t Thread) (int64, error) {
	if s.writer != nil {
		return queueValue(s, []any{t}, func(d *Store, p []any) (int64, error) {
			return d.CreateThread(p[0].(Thread))
		})
	}
	if strings.TrimSpace(t.Title) == "" {
		return 0, fmt.Errorf("a thread needs a title: it is the question someone will search for later")
	}
	if t.Project == "" {
		return 0, fmt.Errorf("a thread needs a project")
	}
	at := t.CreatedAt
	if at == "" {
		at = now()
	}
	state := t.State
	if state == "" {
		state = ThreadOpen
	}
	res, err := s.db.Exec(`INSERT INTO threads(project,title,question,state,archived,person,created_at,updated_at)
		VALUES(?,?,?,?,0,?,?,?)`,
		t.Project, t.Title, t.Question, state, t.Person, at, at)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// TouchThread schreibt die letzte Aktivität fort. Ohne das bliebe ein
// lebhaft diskutierter Thread nach 14 Tagen Erstellungsalter ruhend, obwohl
// gerade jemand geschrieben hat.
func (s *Store) TouchThread(id int64) error {
	if s.writer != nil {
		return queueWrite(s, []any{id}, func(d *Store, p []any) error {
			return d.TouchThread(p[0].(int64))
		})
	}
	_, err := s.db.Exec(`UPDATE threads SET updated_at=? WHERE id=?`, now(), id)
	return err
}

// SetThreadState ändert den fachlichen Zustand. Wiedereröffnen ist derselbe
// Aufruf mit ThreadOpen: ein eigener Zustand "reopened" wäre eine dritte
// Bedeutung für dieselbe Lage — die Frage ist wieder offen, und mehr sagt
// er nicht.
func (s *Store) SetThreadState(id int64, state string) error {
	switch state {
	case ThreadOpen, ThreadResolved, ThreadDeferred:
	default:
		return fmt.Errorf("unknown thread state %q", state)
	}
	if s.writer != nil {
		return queueWrite(s, []any{id, state}, func(d *Store, p []any) error {
			return d.SetThreadState(p[0].(int64), p[1].(string))
		})
	}
	ts := now()
	var resolved any
	if state == ThreadResolved {
		resolved = ts
	}
	_, err := s.db.Exec(`UPDATE threads SET state=?, resolved_at=?, updated_at=? WHERE id=?`,
		state, resolved, ts, id)
	return err
}

// SetThreadArchived blendet einen Thread aus der Standardansicht aus.
// Getrennt vom Zustand, weil Archivierung nichts über die Frage aussagt —
// und weil sie nicht löscht: der Inhalt bleibt such- und lesbar.
func (s *Store) SetThreadArchived(id int64, archived bool) error {
	if s.writer != nil {
		return queueWrite(s, []any{id, archived}, func(d *Store, p []any) error {
			return d.SetThreadArchived(p[0].(int64), p[1].(bool))
		})
	}
	flag := 0
	if archived {
		flag = 1
	}
	_, err := s.db.Exec(`UPDATE threads SET archived=?, updated_at=? WHERE id=?`, flag, now(), id)
	return err
}

func (s *Store) ThreadByID(id int64) (Thread, error) {
	if s.reader != nil {
		return s.reader.ThreadByID(id)
	}
	row := s.db.QueryRow(`SELECT id,project,title,question,state,archived,person,
			created_at,updated_at,COALESCE(resolved_at,'') FROM threads WHERE id=?`, id)
	return scanThread(row)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanThread(row rowScanner) (Thread, error) {
	var t Thread
	var archived int
	if err := row.Scan(&t.ID, &t.Project, &t.Title, &t.Question, &t.State, &archived,
		&t.Person, &t.CreatedAt, &t.UpdatedAt, &t.ResolvedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Thread{}, fmt.Errorf("thread not found")
		}
		return Thread{}, err
	}
	t.Archived = archived != 0
	t.Dormant = threadIsDormant(t)
	return t, nil
}

// threadIsDormant leitet den Ruhezustand ab. Nur ein OFFENER Thread kann
// ruhen: ein beantworteter ist fertig, kein vergessener. Eine unlesbare
// Zeitangabe gilt nicht als ruhend — eine kaputte Angabe darf ein Thema
// nicht stillschweigend aus der Ansicht nehmen.
func threadIsDormant(t Thread) bool {
	if t.State != ThreadOpen || t.Archived {
		return false
	}
	last, err := time.Parse(time.RFC3339, t.UpdatedAt)
	if err != nil {
		return false
	}
	return time.Since(last) > DormantAfter
}

// SearchThreads listet die Themen eines Projekts. Archivierte bleiben
// draußen, solange niemand ausdrücklich danach fragt — sie sind nicht weg,
// nur nicht im Weg.
func (s *Store) SearchThreads(project, query string, includeArchived bool, limit int) ([]Thread, error) {
	if s.reader != nil {
		return s.reader.SearchThreads(project, query, includeArchived, limit)
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	sql := `SELECT id,project,title,question,state,archived,person,created_at,updated_at,
			COALESCE(resolved_at,'') FROM threads WHERE project=?`
	args := []any{project}
	if !includeArchived {
		sql += ` AND archived=0`
	}
	if q := strings.TrimSpace(query); q != "" {
		sql += ` AND (title LIKE ? OR question LIKE ?)`
		args = append(args, "%"+q+"%", "%"+q+"%")
	}
	sql += ` ORDER BY updated_at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Thread
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) LinkThread(l ThreadLink) error {
	if s.writer != nil {
		return queueWrite(s, []any{l}, func(d *Store, p []any) error {
			return d.LinkThread(p[0].(ThreadLink))
		})
	}
	if l.Kind == "" || l.ID == "" {
		return fmt.Errorf("a thread link needs an object kind and id")
	}
	_, err := s.db.Exec(`INSERT OR IGNORE INTO thread_links(thread_id,object_kind,object_id,object_revision,created_at)
		VALUES(?,?,?,?,?)`, l.ThreadID, l.Kind, l.ID, l.Revision, now())
	return err
}

func (s *Store) ThreadLinks(threadID int64) ([]ThreadLink, error) {
	if s.reader != nil {
		return s.reader.ThreadLinks(threadID)
	}
	rows, err := s.db.Query(`SELECT thread_id,object_kind,object_id,object_revision,created_at
		FROM thread_links WHERE thread_id=? ORDER BY object_kind,object_id`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ThreadLink
	for rows.Next() {
		var l ThreadLink
		if err := rows.Scan(&l.ThreadID, &l.Kind, &l.ID, &l.Revision, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ThreadsForObject beantwortet die Frage aus der anderen Richtung: welche
// Diskussionen hängen an diesem Pitfall, diesem Request, diesem Dokument?
// Das ist der Grund, warum ein Thread mehrfach verlinkt sein darf und
// trotzdem einer bleibt.
func (s *Store) ThreadsForObject(kind, id string) ([]Thread, error) {
	if s.reader != nil {
		return s.reader.ThreadsForObject(kind, id)
	}
	rows, err := s.db.Query(`SELECT t.id,t.project,t.title,t.question,t.state,t.archived,t.person,
			t.created_at,t.updated_at,COALESCE(t.resolved_at,'')
		FROM threads t JOIN thread_links l ON l.thread_id=t.id
		WHERE l.object_kind=? AND l.object_id=?
		ORDER BY t.updated_at DESC, t.id DESC`, kind, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Thread
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// PutThreadSummary legt eine neue Fassung an. Fassungen werden ergänzt, nie
// ersetzt: eine frühere Karte bleibt lesbar, und wer wissen will, wie sich
// der Stand verschoben hat, kann das nachlesen. Spec §B4 warnt ausdrücklich
// davor, Zusammenfassungen von Zusammenfassungen zu bauen — dafür muss die
// vorherige noch da sein.
func (s *Store) PutThreadSummary(sum ThreadSummary) (int, error) {
	if s.writer != nil {
		return queueValue(s, []any{sum}, func(d *Store, p []any) (int, error) {
			return d.PutThreadSummary(p[0].(ThreadSummary))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var next int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(revision),0)+1 FROM thread_summaries WHERE thread_id=?`,
		sum.ThreadID).Scan(&next); err != nil {
		return 0, err
	}
	at := sum.CreatedAt
	if at == "" {
		at = now()
	}
	if _, err := tx.Exec(`INSERT INTO thread_summaries(thread_id,revision,body,open_questions,
			covers_through_sequence,person,created_at) VALUES(?,?,?,?,?,?,?)`,
		sum.ThreadID, next, sum.Body, sum.OpenQuestions, sum.CoversThrough, sum.Person, at); err != nil {
		return 0, err
	}
	return next, tx.Commit()
}

// LatestThreadSummary liefert die jüngste Fassung. Fehlt eine, ist das kein
// Fehler: ein Thread ohne Zusammenfassung ist lesbar, nur eben roh. Spec
// §B4 verlangt genau das — fällt der Zusammenfassungsdienst aus, bleibt der
// Thread benutzbar.
func (s *Store) LatestThreadSummary(threadID int64) (ThreadSummary, bool, error) {
	if s.reader != nil {
		return s.reader.LatestThreadSummary(threadID)
	}
	var sum ThreadSummary
	err := s.db.QueryRow(`SELECT thread_id,revision,body,open_questions,covers_through_sequence,
			person,created_at FROM thread_summaries WHERE thread_id=?
		ORDER BY revision DESC LIMIT 1`, threadID).
		Scan(&sum.ThreadID, &sum.Revision, &sum.Body, &sum.OpenQuestions,
			&sum.CoversThrough, &sum.Person, &sum.CreatedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ThreadSummary{}, false, nil
	case err != nil:
		return ThreadSummary{}, false, err
	}
	return sum, true, nil
}

func (s *Store) PutThreadOutcome(o ThreadOutcome) error {
	switch o.State {
	case "", OutcomeProposed, OutcomeAccepted, OutcomeRejected:
	default:
		return fmt.Errorf("unknown outcome state %q", o.State)
	}
	if s.writer != nil {
		return queueWrite(s, []any{o}, func(d *Store, p []any) error {
			return d.PutThreadOutcome(p[0].(ThreadOutcome))
		})
	}
	state := o.State
	if state == "" {
		// Ein Thread schlägt vor. Angenommen wird anderswo, mit eigener
		// Freigabe — deshalb ist proposed der einzig mögliche Default.
		state = OutcomeProposed
	}
	ts := now()
	var decided any
	if state != OutcomeProposed {
		decided = ts
	}
	_, err := s.db.Exec(`INSERT INTO thread_outcomes(thread_id,kind,ref_id,state,note,created_at,decided_at)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(thread_id,kind,ref_id) DO UPDATE SET
			state=excluded.state, note=excluded.note, decided_at=excluded.decided_at`,
		o.ThreadID, o.Kind, o.RefID, state, o.Note, ts, decided)
	return err
}

func (s *Store) ThreadOutcomes(threadID int64) ([]ThreadOutcome, error) {
	if s.reader != nil {
		return s.reader.ThreadOutcomes(threadID)
	}
	rows, err := s.db.Query(`SELECT thread_id,kind,ref_id,state,note,created_at,COALESCE(decided_at,'')
		FROM thread_outcomes WHERE thread_id=? ORDER BY kind,ref_id`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ThreadOutcome
	for rows.Next() {
		var o ThreadOutcome
		if err := rows.Scan(&o.ThreadID, &o.Kind, &o.RefID, &o.State, &o.Note,
			&o.CreatedAt, &o.DecidedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
