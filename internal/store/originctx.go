package store

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// originSchema (REQ-600, Entscheidung zur Form im Wissenseintrag): der
// Entstehungskontext ist eine eigene Tabelle mit je einer Zeile je
// Schreibvorgang, dazu eine Kind-Tabelle für die gewichteten Pfade und eine für
// die verdichteten Bereiche. An den acht Revisionstabellen des Git-Ankers hängt
// eine zweite nullbare Spalte origin_id.
//
// Warum nicht zur Anker-Tabelle dazu: Der Anker fehlt ohne Git, der Kontext
// ohne Session. Beides sind unabhängige Messungen mit eigener Lücke, und ein
// gemeinsamer Eintrag würde "keine Session" und "kein Git" verwechseln.
// Warum Kind-Tabellen statt JSON: der Bereichsfilter läuft in SQL über alle
// Einträge und braucht einen Index auf dem Bereich.
//
// Die Zeilen sind unveränderlich wie die Anker. Sie entstehen im selben Schreib-
// pfad (Handler setzt das Feld, der Store schreibt es mit dem Eintrag) und
// überleben die Aufräumung von path_activity, weil sie die Pfade kopieren.
const originSchema = `
CREATE TABLE IF NOT EXISTS origin_contexts(
  id INTEGER PRIMARY KEY,
  session_ref TEXT NOT NULL DEFAULT '',
  captured_at TEXT NOT NULL,
  backfilled INTEGER NOT NULL DEFAULT 0 CHECK(backfilled IN (0,1)));
CREATE INDEX IF NOT EXISTS origin_contexts_at ON origin_contexts(captured_at);
CREATE TABLE IF NOT EXISTS origin_paths(
  origin_id INTEGER NOT NULL REFERENCES origin_contexts(id),
  path TEXT NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('read','write','named')),
  weight INTEGER NOT NULL,
  PRIMARY KEY(origin_id, path)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS origin_areas(
  origin_id INTEGER NOT NULL REFERENCES origin_contexts(id),
  area TEXT NOT NULL,
  weight INTEGER NOT NULL,
  PRIMARY KEY(origin_id, area)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS origin_areas_area ON origin_areas(area);
CREATE TRIGGER IF NOT EXISTS origin_contexts_immutable BEFORE UPDATE ON origin_contexts
  BEGIN SELECT RAISE(ABORT,'origin_contexts is immutable'); END;
CREATE TRIGGER IF NOT EXISTS origin_contexts_no_delete BEFORE DELETE ON origin_contexts
  BEGIN SELECT RAISE(ABORT,'origin_contexts is immutable'); END;
CREATE TRIGGER IF NOT EXISTS origin_paths_immutable BEFORE UPDATE ON origin_paths
  BEGIN SELECT RAISE(ABORT,'origin_paths is immutable'); END;
CREATE TRIGGER IF NOT EXISTS origin_paths_no_delete BEFORE DELETE ON origin_paths
  BEGIN SELECT RAISE(ABORT,'origin_paths is immutable'); END;
CREATE TRIGGER IF NOT EXISTS origin_areas_immutable BEFORE UPDATE ON origin_areas
  BEGIN SELECT RAISE(ABORT,'origin_areas is immutable'); END;
CREATE TRIGGER IF NOT EXISTS origin_areas_no_delete BEFORE DELETE ON origin_areas
  BEGIN SELECT RAISE(ABORT,'origin_areas is immutable'); END;
`

// ensureOriginContexts legt Tabellen und Spalten additiv an, beliebig oft
// aufrufbar. Dieselben acht Tabellen wie beim Git-Anker (anchoredTables).
func ensureOriginContexts(db *sql.DB) error {
	if _, err := db.Exec(originSchema); err != nil {
		return err
	}
	// via nennt den Weg, wenn es keine Agenten-Session war (Web); additiv, damit
	// ein zurückgerolltes Binary die Spalte ignoriert.
	if err := ensureColumn(db, "origin_contexts", "via", `TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("origin_contexts.via: %w", err)
	}
	for _, table := range anchoredTables {
		if err := ensureColumn(db, table, "origin_id", `INTEGER REFERENCES origin_contexts(id)`); err != nil {
			return fmt.Errorf("%s.origin_id: %w", table, err)
		}
		if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS ` + table + `_origin ON ` + table + `(origin_id) WHERE origin_id IS NOT NULL`); err != nil {
			return err
		}
	}
	return relaxPainEventsTrigger(db)
}

// relaxPainEventsTrigger lässt am sonst unveränderlichen pain_events genau eine
// Änderung zu: origin_id darf einmal von NULL auf einen Wert gesetzt werden (der
// Nachtrag, ctx origin backfill). Jede andere Spalte bleibt gesperrt, und ein
// gesetzter Kontext lässt sich nicht mehr ändern. Der Trigger aus pain_schema.go
// sperrt alles; er wird hier einmal durch die engere Fassung ersetzt.
func relaxPainEventsTrigger(db *sql.DB) error {
	var def sql.NullString
	err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='pain_events_no_update'`).Scan(&def)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if def.Valid && !strings.Contains(def.String, "UPDATE OF") {
		if _, err := db.Exec(`DROP TRIGGER pain_events_no_update`); err != nil {
			return err
		}
		def.Valid = false
	}
	if def.Valid {
		return nil
	}
	_, err = db.Exec(`
CREATE TRIGGER IF NOT EXISTS pain_events_no_update
  BEFORE UPDATE OF id,pain_id,kind,from_state,to_state,remaining,evidence_kind,evidence_ref,note,data,person,account_id,session_id,created_at,anchor_id ON pain_events
  BEGIN SELECT RAISE(ABORT,'pain_events is append-only'); END;
CREATE TRIGGER IF NOT EXISTS pain_events_origin_once
  BEFORE UPDATE OF origin_id ON pain_events WHEN old.origin_id IS NOT NULL
  BEGIN SELECT RAISE(ABORT,'pain_events.origin_id is set once'); END;`)
	return err
}

// insertOriginTx speichert einen Kontext und liefert seine Id für die Spalte
// origin_id, für nil NULL. Es gibt nur eine Zeile, wenn es etwas festzuhalten
// gibt.
func insertOriginTx(tx txExec, c *originctx.Context) (any, error) {
	if c == nil {
		return nil, nil
	}
	at := c.CapturedAt
	if at == "" {
		at = now()
	}
	backfilled := 0
	if c.Backfilled {
		backfilled = 1
	}
	res, err := tx.Exec(`INSERT INTO origin_contexts(session_ref,captured_at,backfilled,via) VALUES(?,?,?,?)`, c.SessionRef, at, backfilled, c.Via)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	for _, p := range c.Paths {
		if _, err := tx.Exec(`INSERT INTO origin_paths(origin_id,path,kind,weight) VALUES(?,?,?,?)`, id, p.Path, p.Kind, p.Weight); err != nil {
			return nil, err
		}
	}
	for _, a := range c.Areas {
		if _, err := tx.Exec(`INSERT INTO origin_areas(origin_id,area,weight) VALUES(?,?,?)`, id, a.Path, a.Weight); err != nil {
			return nil, err
		}
	}
	return id, nil
}

// insertWriteMetaTx speichert Anker und Entstehungskontext eines Schreib-
// vorgangs und liefert beide Ids für die Spalten anchor_id und origin_id.
func insertWriteMetaTx(tx txExec, anchor *gitanchor.Anchor, origin *originctx.Context) (anchorID, originID any, err error) {
	if anchorID, err = insertAnchorTx(tx, anchor); err != nil {
		return nil, nil, err
	}
	// Ein Schreibzugriff der Weboberfläche hat keinen Anker, aber eine Herkunft:
	// "Web" steht am Entstehungskontext, der sonst fehlen würde.
	if anchor.IsWeb() && anchor.IsZero() {
		c := originctx.Context{}
		if origin != nil {
			c = *origin
		}
		c.Via = originctx.ViaWeb
		origin = &c
	}
	if originID, err = insertOriginTx(tx, origin); err != nil {
		return nil, nil, err
	}
	return anchorID, originID, nil
}

// viaFor liefert zu den Zeilen einer Tabelle den Weg ihres Entstehungskontexts
// (originctx.ViaWeb), nach Zeilen-Id. Zeilen ohne Weg fehlen im Ergebnis. table
// kommt nur aus anchoredTables.
func (s *Store) viaFor(table string, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	for len(ids) > 0 {
		part := ids
		if len(part) > 500 {
			part = part[:500]
		}
		ids = ids[len(part):]
		args := make([]any, len(part))
		for i, id := range part {
			args[i] = id
		}
		rows, err := s.db.Query(`SELECT t.id, o.via FROM `+table+` t JOIN origin_contexts o ON o.id=t.origin_id
			WHERE o.via != '' AND t.id IN (`+placeholders(len(part))+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var via string
			if err := rows.Scan(&id, &via); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = via
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// originsFor liefert die Kontexte der Zeilen einer Tabelle, nach Zeilen-Id.
// Zeilen ohne Kontext fehlen im Ergebnis. table kommt nur aus anchoredTables.
func (s *Store) originsFor(table string, ids []int64) (map[int64]*originctx.Context, error) {
	out := map[int64]*originctx.Context{}
	byOrigin := map[int64]*originctx.Context{}
	rowOrigin := map[int64]int64{}
	for len(ids) > 0 {
		part := ids
		if len(part) > 500 {
			part = part[:500]
		}
		ids = ids[len(part):]
		args := make([]any, len(part))
		for i, id := range part {
			args[i] = id
		}
		rows, err := s.db.Query(`SELECT t.id, o.id, o.session_ref, o.captured_at, o.backfilled, o.via FROM `+table+` t
			JOIN origin_contexts o ON o.id=t.origin_id WHERE t.id IN (`+placeholders(len(part))+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, oid, backfilled int64
			c := &originctx.Context{}
			if err := rows.Scan(&id, &oid, &c.SessionRef, &c.CapturedAt, &backfilled, &c.Via); err != nil {
				rows.Close()
				return nil, err
			}
			c.Backfilled = backfilled != 0
			if known, ok := byOrigin[oid]; ok {
				c = known
			} else {
				byOrigin[oid] = c
			}
			rowOrigin[id] = oid
			out[id] = c
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	if len(byOrigin) == 0 {
		return out, nil
	}
	oids := make([]int64, 0, len(byOrigin))
	for oid := range byOrigin {
		oids = append(oids, oid)
	}
	for len(oids) > 0 {
		part := oids
		if len(part) > 500 {
			part = part[:500]
		}
		oids = oids[len(part):]
		args := make([]any, len(part))
		for i, id := range part {
			args[i] = id
		}
		rows, err := s.db.Query(`SELECT origin_id,path,kind,weight FROM origin_paths WHERE origin_id IN (`+placeholders(len(part))+`)
			ORDER BY weight DESC, path`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var oid int64
			var p originctx.Path
			if err := rows.Scan(&oid, &p.Path, &p.Kind, &p.Weight); err != nil {
				rows.Close()
				return nil, err
			}
			byOrigin[oid].Paths = append(byOrigin[oid].Paths, p)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		rows, err = s.db.Query(`SELECT origin_id,area,weight FROM origin_areas WHERE origin_id IN (`+placeholders(len(part))+`)
			ORDER BY weight DESC, area`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var oid int64
			var a originctx.Area
			if err := rows.Scan(&oid, &a.Path, &a.Weight); err != nil {
				rows.Close()
				return nil, err
			}
			byOrigin[oid].Areas = append(byOrigin[oid].Areas, a)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// originWhere ist die Bedingung des Filters, zum Anhängen mit AND. originExpr
// ist ein SQL-Ausdruck, der die origin_id des Eintrags liefert (meist die
// Spalte o.origin_id), createdExpr der Zeitpunkt des Eintrags als Ersatz, wo es
// keinen Kontext gibt. Leer ohne aktiven Filter.
//
// Zeit: der Zeitpunkt des Kontexts, sonst der des Eintrags. Bereich: einer der
// Bereiche des Kontexts passt zu einem der Filter-Präfixe, gleich, darunter
// oder darüber, immer an der Verzeichnisgrenze (internal/store trifft nicht
// internal/storex).
func originWhere(f originctx.Filter, originExpr, createdExpr string) (string, []any) {
	if !f.Active() {
		return "", nil
	}
	var conds []string
	var args []any
	at := `COALESCE((SELECT captured_at FROM origin_contexts WHERE id=` + originExpr + `), ` + createdExpr + `)`
	if f.Since != "" {
		conds = append(conds, at+` >= ?`)
		args = append(args, f.Since)
	}
	if f.Until != "" {
		conds = append(conds, at+` <= ?`)
		args = append(args, f.Until)
	}
	if len(f.Areas) > 0 {
		var ors []string
		for _, a := range f.Areas {
			ors = append(ors, `(oa.area = ? OR substr(oa.area, 1, ?) = ? OR substr(?, 1, length(oa.area)+1) = oa.area || '/')`)
			args = append(args, a, len(a)+1, a+"/", a)
		}
		conds = append(conds, `EXISTS (SELECT 1 FROM origin_areas oa WHERE oa.origin_id=`+originExpr+` AND (`+strings.Join(ors, " OR ")+`))`)
	}
	return strings.Join(conds, " AND "), args
}

// validOriginFilter prüft, was Spec.Resolve nicht schon prüft: ein Filter, den
// ein Aufrufer ohne Spec baut. Ein ungültiger gehört abgelehnt, nicht ignoriert.
func validOriginFilter(f originctx.Filter) error {
	if f.Since != "" && f.Until != "" && f.Since > f.Until {
		return fmt.Errorf("since is after until")
	}
	return nil
}

// OriginFor ermittelt den Entstehungskontext für einen Schreibvorgang jetzt:
// die Pfade der Session aus path_activity (höchstens ActivityRetention alt, die
// jüngsten 1000), dazu die ausdrücklich genannten Pfade. Ohne Session und ohne
// genannte Pfade nil. account ist das Konto des Schreibers: Aktivität eines
// anderen Kontos unter derselben Kennung zählt nicht.
func (s *Store) OriginFor(sessionExternalID string, account int64, named []string) (*originctx.Context, error) {
	if s.reader != nil {
		return s.reader.OriginFor(sessionExternalID, account, named)
	}
	at := time.Now().UTC()
	from := at.Add(-ActivityRetention).Format(time.RFC3339)
	touches, err := s.originTouches(sessionExternalID, account, from, "")
	if err != nil {
		return nil, err
	}
	return originctx.Build(sessionExternalID, at.Format(time.RFC3339), touches, named), nil
}

// originTouches liest die Berührungen einer Session; until leer heißt ohne
// obere Grenze.
func (s *Store) originTouches(sessionExternalID string, account int64, from, until string) ([]originctx.Touch, error) {
	if sessionExternalID == "" {
		return nil, nil
	}
	q := `SELECT path, writes, checkout FROM path_activity
		WHERE session_external_id=? AND quality<>'unattributed' AND at>=?
		  AND (CASE WHEN account_id=0 THEN ? ELSE account_id END)=?`
	args := []any{sessionExternalID, from, instanceOwnerID(s.db), account}
	if until != "" {
		q += ` AND at<=?`
		args = append(args, until)
	}
	rows, err := s.db.Query(q+` ORDER BY at DESC, id DESC LIMIT 1000`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []originctx.Touch
	for rows.Next() {
		var t originctx.Touch
		var writes int
		if err := rows.Scan(&t.Path, &writes, &t.Checkout); err != nil {
			return nil, err
		}
		t.Writes = writes != 0
		out = append(out, t)
	}
	return out, rows.Err()
}

// SessionExternalByID liefert die Kennung (external_id) einer Session aus ihrer
// Zeilen-Id; leer, wenn es sie nicht gibt.
func (s *Store) SessionExternalByID(id int64) string {
	if s.reader != nil {
		return s.reader.SessionExternalByID(id)
	}
	var ext string
	if err := s.db.QueryRow(`SELECT external_id FROM sessions WHERE id=?`, id).Scan(&ext); err != nil {
		return ""
	}
	return ext
}

// OriginBackfillKind ist das Ergebnis des Nachtrags für eine Art von Einträgen.
type OriginBackfillKind struct {
	// Candidates sind Einträge ohne Kontext, die eine Session nennen.
	Candidates int `json:"candidates"`
	// Filled haben einen Kontext bekommen (bei dry-run: würden).
	Filled int `json:"filled"`
	// Empty blieben leer: Session unbekannt oder keine Pfaddaten mehr da.
	Empty int `json:"empty"`
}

// OriginBackfillReport sagt je Art, was nachgetragen wurde.
type OriginBackfillReport struct {
	DryRun    bool               `json:"dry_run"`
	Knowledge OriginBackfillKind `json:"knowledge"`
	Ghosts    OriginBackfillKind `json:"ghosts"`
	Requests  OriginBackfillKind `json:"requests"`
	Pains     OriginBackfillKind `json:"pains"`
}

// originBackfillSource beschreibt, woher eine Art ihre Session und ihren
// Zeitpunkt bekommt. sessionExpr liefert Kennung oder Zeilen-Id der Session.
//
// writerExpr liefert das Konto, das den Eintrag geschrieben hat (0 = unbekannt
// oder ohne Besitzer). Ein Kontext wird nur aus einer Session des Schreibers
// übernommen: sonst bekäme ein Eintrag mit fremder oder erfundener session_ref
// die Pfade aus der Session eines anderen Kontos, und die wären über den
// Bereichsfilter lesbar. Es gibt hier bewusst keinen Ersatz durch den
// Instanz-Owner: im Zweifel bleibt der Eintrag leer.
type originBackfillSource struct {
	table, sessionExpr, writerExpr, atExpr, from, where string
	byRowID                                             bool
	report                                              func(*OriginBackfillReport) *OriginBackfillKind
}

var originBackfillSources = []originBackfillSource{
	// Wissen: session_ref ist die Kennung der Session oder "session:<id>". Ein
	// Eintrag, der inhaltlich überarbeitet wurde (Historie vorhanden), gehört
	// nicht dazu: der Kontext beschreibt die Fassung, nicht den Eintrag.
	{table: "knowledge", sessionExpr: "t.session_ref", atExpr: "t.created_at",
		writerExpr: "COALESCE((SELECT a.account_id FROM knowledge_authors a WHERE a.knowledge_id=t.id),0)",
		from:       "knowledge t",
		where:      "t.session_ref<>'' AND NOT EXISTS (SELECT 1 FROM knowledge_versions v WHERE v.knowledge_id=t.id)",
		report:     func(r *OriginBackfillReport) *OriginBackfillKind { return &r.Knowledge }},
	{table: "ghost_files", sessionExpr: "t.session_ref", atExpr: "t.described_at",
		// Beschreibungen haben keine Autorentabelle: nur ein Name, der genau ein
		// Konto trägt, gilt als Schreiber.
		writerExpr: "(SELECT CASE WHEN COUNT(*)=1 THEN MIN(p.id) ELSE 0 END FROM persons p WHERE trim(t.person)<>'' AND lower(trim(p.name))=lower(trim(t.person)))",
		from:       "ghost_files t", where: "t.session_ref<>''",
		report: func(r *OriginBackfillReport) *OriginBackfillKind { return &r.Ghosts }},
	// Request: das Ereignis request.created trägt den Kontext des Anlegens, die
	// Session steht am Request.
	{table: "request_activity", sessionExpr: "r.session_ref", atExpr: "t.created_at",
		writerExpr: "COALESCE((SELECT a.account_id FROM request_authors a WHERE a.request_id=r.id),0)",
		from:       "request_activity t JOIN requests r ON r.id=t.request_id",
		where:      "t.kind='request.created' AND r.session_ref<>''",
		report:     func(r *OriginBackfillReport) *OriginBackfillKind { return &r.Requests }},
	// Pain: das Ereignis 'created' trägt die numerische Session-Id.
	{table: "pain_events", sessionExpr: "t.session_id", atExpr: "t.created_at",
		writerExpr: "t.account_id",
		from:       "pain_events t", where: "t.kind='created' AND t.session_id>0", byRowID: true,
		report: func(r *OriginBackfillReport) *OriginBackfillKind { return &r.Pains }},
}

// BackfillOrigins trägt den Entstehungskontext für Einträge nach, die keinen
// haben, soweit Session und Pfaddaten noch da sind: aus session_ref (bzw. der
// Session-Id) und path_activity bis zum Zeitpunkt des Eintrags. Wo beides nicht
// reicht, bleibt der Eintrag ohne Kontext. Idempotent: nur Zeilen ohne
// origin_id werden angefasst, und ein Lauf ohne neue Daten ändert nichts. Mit
// dryRun wird gezählt und nichts geschrieben. Nicht automatisch beim Start,
// sondern als Befehl (ctx origin backfill).
func (s *Store) BackfillOrigins(dryRun bool) (OriginBackfillReport, error) {
	if s.writer != nil {
		return queueValue(s, []any{dryRun}, func(d *Store, p []any) (OriginBackfillReport, error) {
			return d.BackfillOrigins(p[0].(bool))
		})
	}
	rep := OriginBackfillReport{DryRun: dryRun}
	for _, src := range originBackfillSources {
		kind := src.report(&rep)
		if err := s.backfillOriginSource(src, kind, dryRun); err != nil {
			return rep, fmt.Errorf("%s: %w", src.table, err)
		}
	}
	return rep, nil
}

type originCandidate struct {
	id      int64
	session string
	at      string
	writer  int64
}

func (s *Store) backfillOriginSource(src originBackfillSource, kind *OriginBackfillKind, dryRun bool) error {
	rows, err := s.db.Query(`SELECT t.id, CAST(` + src.sessionExpr + ` AS TEXT), ` + src.atExpr + `, ` + src.writerExpr + ` FROM ` + src.from +
		` WHERE t.origin_id IS NULL AND ` + src.where + ` ORDER BY t.id`)
	if err != nil {
		return err
	}
	var cands []originCandidate
	for rows.Next() {
		var c originCandidate
		if err := rows.Scan(&c.id, &c.session, &c.at, &c.writer); err != nil {
			rows.Close()
			return err
		}
		cands = append(cands, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	kind.Candidates = len(cands)
	owner := instanceOwnerID(s.db)
	for _, c := range cands {
		ext, ok := s.originSession(c.session, src.byRowID, c.writer, owner)
		if !ok {
			kind.Empty++
			continue
		}
		touches, err := s.originTouches(ext, c.writer, "", c.at)
		if err != nil {
			return err
		}
		ctx := originctx.Build(ext, c.at, touches, nil)
		if ctx == nil || len(ctx.Areas) == 0 {
			kind.Empty++
			continue
		}
		ctx.Backfilled = true
		kind.Filled++
		if dryRun {
			continue
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		oid, err := insertOriginTx(tx, ctx)
		if err == nil {
			_, err = tx.Exec(`UPDATE `+src.table+` SET origin_id=? WHERE id=? AND origin_id IS NULL`, oid, c.id)
		}
		if err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// originSession löst die Session eines Eintrags auf und gibt ihre Kennung nur
// zurück, wenn sie ausschließlich dem Schreiber des Eintrags gehört: jede
// Session-Zeile mit dieser Kennung muss (account_id 0 gilt als Instanz-Owner)
// auf das Konto writer zeigen. Gehört sie einem anderen Konto, gehört die
// Kennung mehreren Konten oder ist der Schreiber unbekannt (writer 0), ist der
// Eintrag nicht zuzuordnen und bleibt leer. Ein "session:<id>"-Verweis wird über
// die Zeilen-Id aufgelöst, sonst gilt der Wert als Kennung; ein Zusatz hinter
// "#" wird abgeschnitten.
func (s *Store) originSession(ref string, byRowID bool, writer, owner int64) (external string, ok bool) {
	if writer <= 0 {
		return "", false
	}
	ref, _, _ = strings.Cut(ref, "#")
	rowID := int64(-1)
	if byRowID {
		if n, err := strconv.ParseInt(ref, 10, 64); err == nil {
			rowID = n
		}
	} else if id, found := strings.CutPrefix(ref, "session:"); found {
		if n, err := strconv.ParseInt(id, 10, 64); err == nil {
			rowID = n
		}
	}
	external = ref
	if rowID >= 0 {
		if err := s.db.QueryRow(`SELECT external_id FROM sessions WHERE id=?`, rowID).Scan(&external); err != nil {
			return "", false
		}
	}
	if external == "" {
		return "", false
	}
	var total, mine int
	if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN (CASE WHEN account_id=0 THEN ? ELSE account_id END)=? THEN 1 ELSE 0 END),0)
		FROM sessions WHERE external_id=?`, owner, writer, external).Scan(&total, &mine); err != nil || total == 0 || total != mine {
		return "", false
	}
	return external, true
}

// SessionForeignTo sagt, dass es eine Session mit dieser Kennung gibt und sie
// dem Konto nicht gehört. Eine unbekannte Kennung ist nicht fremd: die Session
// kann noch nicht hochgeladen sein.
func (s *Store) SessionForeignTo(sessionExternalID, principalID string) bool {
	if s.reader != nil {
		return s.reader.SessionForeignTo(sessionExternalID, principalID)
	}
	if sessionExternalID == "" {
		return false
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE external_id=?`, sessionExternalID).Scan(&n); err != nil || n == 0 {
		return false
	}
	return !s.SessionOwnedBy(sessionExternalID, principalID)
}

// attachKnowledgeOrigins hängt jedem Eintrag den Kontext seiner aktuellen
// Fassung an.
func (s *Store) attachKnowledgeOrigins(ks []Knowledge) error {
	if len(ks) == 0 {
		return nil
	}
	ids := make([]int64, len(ks))
	for i, k := range ks {
		ids[i] = k.ID
	}
	origins, err := s.originsFor("knowledge", ids)
	if err != nil {
		return err
	}
	for i := range ks {
		ks[i].OriginContext = origins[ks[i].ID]
	}
	return nil
}

// attachRequestOrigins hängt Treffern den Kontext des Anlegens an: der steht am
// Ereignis request.created.
func (s *Store) attachRequestOrigins(hits []requestdomain.SearchHit) error {
	if len(hits) == 0 {
		return nil
	}
	byRequest := map[int64]int{}
	args := make([]any, len(hits))
	for i, h := range hits {
		byRequest[h.Request.ID] = i
		args[i] = h.Request.ID
	}
	rows, err := s.db.Query(`SELECT id, request_id FROM request_activity WHERE kind='request.created' AND origin_id IS NOT NULL
		AND request_id IN (`+placeholders(len(args))+`)`, args...)
	if err != nil {
		return err
	}
	actToRequest := map[int64]int64{}
	var actIDs []int64
	for rows.Next() {
		var act, req int64
		if err := rows.Scan(&act, &req); err != nil {
			rows.Close()
			return err
		}
		actToRequest[act] = req
		actIDs = append(actIDs, act)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(actIDs) == 0 {
		return err
	}
	origins, err := s.originsFor("request_activity", actIDs)
	if err != nil {
		return err
	}
	for act, c := range origins {
		hits[byRequest[actToRequest[act]]].OriginContext = c
	}
	return nil
}
