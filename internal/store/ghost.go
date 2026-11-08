package store

import (
	"database/sql"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
)

// GhostFile ist die Beschreibung eines Pfades. Die Identität ist
// (Project, Path) — eine Ghost-Datei GEHÖRT zu internal/store/knowledge.go, sie
// ist nicht Wissen, das dort zufällig gilt.
type GhostFile struct {
	ID          int64  `json:"id"`
	Project     string `json:"project"`
	Path        string `json:"path"`
	Kind        string `json:"kind"` // file|dir
	Description string `json:"description"`
	ContentSHA  string `json:"content_sha"`
	GitBlob     string `json:"git_blob,omitempty"`
	// Branch ist die Ebene der Beschreibung (#713): leer ist die Projektebene,
	// sonst gilt sie auf diesem Branch und in davon abgezweigten (ghostbranch.go).
	Branch      string `json:"branch,omitempty"`
	LineCount   int    `json:"line_count"`
	Person      string `json:"person,omitempty"`
	Harness     string `json:"harness,omitempty"`
	SessionRef  string `json:"session_ref,omitempty"`
	DescribedAt string `json:"described_at"`
	UpdatedAt   string `json:"updated_at"`
	// Match is set by a search and only to MatchSemantic: the embedding found the
	// description alone, its words do not match the query. A hit of the full-text
	// index leaves it empty, which is also how every other read leaves it.
	Match string `json:"match,omitempty"`
	// GitAnchor ist der Git-Stand, an dem diese Beschreibung geschrieben wurde
	// (REQ-597). Der Server setzt ihn beim Schreiben aus dem Header, nie aus
	// dem Body.
	GitAnchor *gitanchor.Anchor `json:"git_anchor,omitempty"`
	// OriginContext ist der Entstehungskontext dieser Beschreibung (REQ-600).
	// Der Pfad der Beschreibung selbst zählt darin als ausdrücklich genannt.
	OriginContext *originctx.Context `json:"origin_context,omitempty"`
}

const ghostCols = `id, project, path, kind, description, content_sha, git_blob,
	line_count, person, harness, session_ref, described_at, updated_at`

// PutGhostFile ersetzt die Beschreibung eines Pfades und hebt die verdrängte
// Fassung auf.
//
// Ausgeliefert wird immer nur die aktuelle — eine alte Beschreibung eines
// dreimal umgeschriebenen Codes ist schlimmer als keine, und daran ändert sich
// nichts. Aufbewahrt wird sie trotzdem: ein Beschreiben ist ein Upsert ohne
// Rückfrage, und es gab zwei Wege, auf denen eine gute Beschreibung still
// verschwand. Ohne Aufbewahrung ist das unwiederbringlich; mit ihr kostet es
// eine Abfrage.
func (s *Store) PutGhostFile(g GhostFile) (int64, error) {
	if s.writer != nil {
		return queueValue(s, []any{g}, func(d *Store, p []any) (int64, error) { return d.PutGhostFile(p[0].(GhostFile)) })
	}
	if g.Kind == "" {
		g.Kind = "file"
	}
	ts := now()
	if g.DescribedAt == "" {
		g.DescribedAt = ts
	}
	if g.Branch != "" {
		return s.putGhostBranchFile(g, ts)
	}
	// Aufheben und Ersetzen gehören in dieselbe Transaktion. Sonst hinterlässt
	// ein fehlgeschlagener Upsert eine archivierte Fassung, die nie abgelöst
	// wurde — eine Historie, die etwas behauptet, das nicht stattgefunden hat.
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	// Der neue Anker ersetzt den alten auf der Kopfzeile; der alte ist mit der
	// verdrängten Fassung in der Historie (archiveGhostFileTx). Ohne Anker
	// bleibt die Spalte leer, sie erbt nichts.
	anchorID, originID, err := insertWriteMetaTx(tx, g.GitAnchor, g.OriginContext)
	if err != nil {
		return 0, err
	}
	id, err := putProjectGhostTx(tx, g, ts, "ersetzt", anchorID, originID)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// putProjectGhostTx hebt die verdrängte Fassung der Projektebene auf und
// ersetzt sie. Gemeinsam für das Beschreiben und die Merge-Übernahme einer
// Branch-Beschreibung (ghostbranch.go).
func putProjectGhostTx(tx *sql.Tx, g GhostFile, ts, reason string, anchorID, originID any) (int64, error) {
	if err := archiveGhostFileTx(tx, g.Project, g.Path, ts, reason); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`INSERT INTO ghost_files(project,path,kind,description,content_sha,git_blob,
		line_count,person,harness,session_ref,described_at,updated_at,anchor_id,origin_id)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(project,path) DO UPDATE SET
			kind=excluded.kind, description=excluded.description,
			content_sha=excluded.content_sha, git_blob=excluded.git_blob,
			line_count=excluded.line_count, person=excluded.person,
			harness=excluded.harness, session_ref=excluded.session_ref,
			described_at=excluded.described_at, updated_at=excluded.updated_at,
			anchor_id=excluded.anchor_id, origin_id=excluded.origin_id`,
		g.Project, g.Path, g.Kind, g.Description, g.ContentSHA, g.GitBlob,
		g.LineCount, g.Person, g.Harness, g.SessionRef, g.DescribedAt, ts, anchorID, originID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) GhostFileByPath(project, path string) (GhostFile, error) {
	if s.reader != nil {
		return s.reader.GhostFileByPath(project, path)
	}
	rows, err := s.db.Query(`SELECT `+ghostCols+` FROM ghost_files WHERE project=? AND path=?`, project, path)
	if err != nil {
		return GhostFile{}, err
	}
	gs, err := s.scanGhostFiles(rows)
	if err != nil {
		return GhostFile{}, err
	}
	if len(gs) == 0 {
		return GhostFile{}, sql.ErrNoRows
	}
	return gs[0], nil
}

// GhostFilesUnder liefert einen Ast. Ein leeres prefix meint den ganzen Baum.
// Der Vergleich hängt ein "/" an, damit internal/store nicht internal/server
// mitnimmt — ein reines LIKE 'internal/store%' täte genau das.
func (s *Store) GhostFilesUnder(project, prefix string) ([]GhostFile, error) {
	if s.reader != nil {
		return s.reader.GhostFilesUnder(project, prefix)
	}
	query := `SELECT ` + ghostCols + ` FROM ghost_files WHERE project=?`
	args := []any{project}
	if prefix != "" {
		query += ` AND (path = ? OR path LIKE ?)`
		args = append(args, prefix, prefix+"/%")
	}
	rows, err := s.db.Query(query+` ORDER BY path`, args...)
	if err != nil {
		return nil, err
	}
	return s.scanGhostFiles(rows)
}

// SearchGhostFiles sucht über Pfad und Beschreibung. Dieselbe Verknüpfung wie
// beim übrigen Wissen: ftsQuery verbindet die aussagekräftigen Terme mit OR und
// überlässt die Rangfolge bm25.
//
// Mit einem Embedding-Dienst und eingeschalteter Art ist es die hybride Suche
// von SearchGhostFilesWith, ohne Zeilenregel.
func (s *Store) SearchGhostFiles(q, project string, limit int) ([]GhostFile, error) {
	if s.reader != nil {
		return s.reader.SearchGhostFiles(q, project, limit)
	}
	return s.searchGhostFiles(q, GhostSearch{Project: project, Limit: limit})
}

// ftsGhostFiles is the full-text ranking as it has always been answered.
func (s *Store) ftsGhostFiles(q, project string, limit int, anchor AnchorFilter, origin originctx.Filter) ([]GhostFile, error) {
	if limit <= 0 {
		limit = 20
	}
	where, args := `(? = '' OR g.project = ?)`, []any{ftsQuery(q), project, project}
	if af, afArgs := anchor.Where("g.anchor_id"); af != "" {
		where += " AND " + af
		args = append(args, afArgs...)
	}
	if of, ofArgs := originWhere(origin, "g.origin_id", "g.described_at"); of != "" {
		where += " AND " + of
		args = append(args, ofArgs...)
	}
	rows, err := s.db.Query(`SELECT `+prefix(ghostCols, "g.")+`
		FROM ghost_files_fts f JOIN ghost_files g ON g.id=f.rowid
		WHERE ghost_files_fts MATCH ? AND `+where+`
		ORDER BY f.rank LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	return s.scanGhostFiles(rows)
}

// scanGhostFiles liest Zeilen von ghostCols und hängt ihnen den Anker an. Nicht
// innerhalb einer Transaktion zu benutzen: der Anker kommt über s.db, und bei
// einer einzigen Verbindung wartete das auf die eigene Transaktion.
func (s *Store) scanGhostFiles(rows *sql.Rows) ([]GhostFile, error) {
	out, err := scanGhostFilesRaw(rows)
	if err != nil || len(out) == 0 {
		return out, err
	}
	ids := make([]int64, len(out))
	for i, g := range out {
		ids[i] = g.ID
	}
	anchors, err := s.anchorsFor("ghost_files", ids)
	if err != nil {
		return nil, err
	}
	origins, err := s.originsFor("ghost_files", ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].GitAnchor = anchors[out[i].ID]
		out[i].OriginContext = origins[out[i].ID]
	}
	return out, nil
}

func scanGhostFilesRaw(rows *sql.Rows) ([]GhostFile, error) {
	defer rows.Close()
	out := []GhostFile{}
	for rows.Next() {
		var g GhostFile
		if err := rows.Scan(&g.ID, &g.Project, &g.Path, &g.Kind, &g.Description,
			&g.ContentSHA, &g.GitBlob, &g.LineCount, &g.Person, &g.Harness,
			&g.SessionRef, &g.DescribedAt, &g.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ParentPaths zählt die Vorfahren eines Pfades auf, Wurzel zuerst. Für
// "internal/store/knowledge.go" sind das "", "internal" und "internal/store".
func ParentPaths(path string) []string {
	out := []string{""}
	if path == "" {
		return out
	}
	parts := strings.Split(path, "/")
	for i := 1; i < len(parts); i++ {
		out = append(out, strings.Join(parts[:i], "/"))
	}
	return out
}

// GhostFilesForDelivery beantwortet, was ein Agent beim Anfassen dieses Pfades
// zu hören bekommt: die Beschreibung der Datei und die ihrer Vorfahren — jede
// genau einmal je Session. Ohne die Entdopplung stünde dieselbe
// Verzeichnisbeschreibung bei zwölf Dateien zwölfmal im Kontext.
//
// Hier wird NICHT mehr umgehängt. Das lief einmal hier, allein über den
// git-Blob, und war deshalb falsch: Verschiebung und Kopie unterscheiden sich
// daran, ob der alte Pfad noch existiert, und der Server hat die Repositorien
// nicht. Eine simple Dateikopie hängte damit die Beschreibung des noch
// existierenden Originals auf die Kopie um (REQ-179). Die Erkennung sitzt jetzt
// in ghost.DetectMoves, wo die vollständige Dateiliste vorliegt.
func (s *Store) GhostFilesForDelivery(project, path, sessionKey string) ([]GhostFile, error) {
	if s.writer != nil {
		return queueValue(s, []any{project, path, sessionKey}, func(d *Store, p []any) ([]GhostFile, error) {
			return d.GhostFilesForDeliveryAt(p[0].(string), p[1].(string), p[2].(string), "", nil)
		})
	}
	return s.GhostFilesForDeliveryAt(project, path, sessionKey, "", nil)
}

// GhostFilesForDeliveryAt ist die Auslieferung auf einem Branch: statt der
// Projektebene allein gilt je Pfad die Beschreibung des Branches, sonst die
// eines Vorfahren, sonst die der Projektebene (ghostbranch.go).
func (s *Store) GhostFilesForDeliveryAt(project, path, sessionKey, branch string, lineage []string) ([]GhostFile, error) {
	if s.writer != nil {
		return queueValue(s, []any{project, path, sessionKey, branch, lineage}, func(d *Store, p []any) ([]GhostFile, error) {
			return d.GhostFilesForDeliveryAt(p[0].(string), p[1].(string), p[2].(string), p[3].(string), p[4].([]string))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	candidates := append(ParentPaths(path), path)
	out := []GhostFile{}
	ts := now()
	for _, p := range candidates {
		var already int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM ghost_deliveries WHERE session_key=? AND project=? AND path=?`,
			sessionKey, project, p).Scan(&already); err != nil {
			return nil, err
		}
		if already > 0 {
			continue
		}
		// Der Pfad gilt als gesagt, sobald er betrachtet wurde — auch wenn es
		// keinen Eintrag gibt. Sonst wiederholt sich die Aufforderung "beschreib
		// diese Datei" bei jedem Schreibzugriff derselben Session.
		if _, err := tx.Exec(`INSERT INTO ghost_deliveries(session_key,project,path,at) VALUES(?,?,?,?)`,
			sessionKey, project, p, ts); err != nil {
			return nil, err
		}
		gs, err := ghostAtTx(tx, project, p, branch, lineage)
		if err != nil {
			return nil, err
		}
		out = append(out, gs...)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// OrphanGhostFiles nennt Beschreibungen, deren Pfad es im Repo nicht mehr gibt.
// existing ist die aktuelle Dateiliste; ist sie leer, wird nichts gemeldet —
// eine leere Liste heisst "wir konnten nicht nachsehen", nicht "es gibt nichts
// mehr", und der Unterschied entscheidet, ob ein doctor-Lauf ausserhalb eines
// Repos den ganzen Baum als Müll ausweist.
func (s *Store) OrphanGhostFiles(project string, existing []string) ([]GhostFile, error) {
	if s.reader != nil {
		return s.reader.OrphanGhostFiles(project, existing)
	}
	if len(existing) == 0 {
		return nil, nil
	}
	all, err := s.GhostFilesUnder(project, "")
	if err != nil {
		return nil, err
	}
	live := make(map[string]bool, len(existing))
	for _, p := range existing {
		live[p] = true
		for _, parent := range ParentPaths(p) {
			live[parent] = true
		}
	}
	var out []GhostFile
	for _, g := range all {
		if !live[g.Path] {
			out = append(out, g)
		}
	}
	return out, nil
}
