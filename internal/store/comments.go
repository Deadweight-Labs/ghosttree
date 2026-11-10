package store

import (
	"database/sql"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// Kommentare an Issue, Idee und Pain. Eigene Tabellen, weil die Threads der
// Koordination etwas anderes sind: sie hängen am Raum und seiner Mitgliedschaft
// (CoordAccess, Gäste bekommen dort Sonderansichten), ihre Nachrichten sind
// unveränderlich und laufen unter der Chat-Aufbewahrung, und jeder Thread ist
// eine eigene Frage mit Zustand. Ein Kommentar dagegen gehört dem Objekt, erbt
// dessen Leserecht (ProjectAccess), wird vom Autor bearbeitet (mit allen
// Fassungen) und weich gelöscht.
const commentSchema = `
CREATE TABLE IF NOT EXISTS comments(
  id INTEGER PRIMARY KEY,
  project TEXT NOT NULL,
  object_kind TEXT NOT NULL CHECK(object_kind IN ('request','idea','pain')),
  object_id INTEGER NOT NULL,
  body TEXT NOT NULL CHECK(trim(body) != ''),
  person TEXT NOT NULL DEFAULT '',
  account_id INTEGER NOT NULL DEFAULT 0,
  session_id INTEGER NOT NULL DEFAULT 0,
  revision INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  edited_at TEXT NOT NULL DEFAULT '',
  deleted_at TEXT NOT NULL DEFAULT '',
  deleted_by TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS comments_object ON comments(object_kind, object_id, id);
CREATE TABLE IF NOT EXISTS comment_versions(
  id INTEGER PRIMARY KEY,
  comment_id INTEGER NOT NULL REFERENCES comments(id) ON DELETE RESTRICT,
  revision INTEGER NOT NULL,
  body TEXT NOT NULL,
  person TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  UNIQUE(comment_id, revision));
CREATE TRIGGER IF NOT EXISTS comment_versions_no_update BEFORE UPDATE OF comment_id,revision,body,person,created_at ON comment_versions
  BEGIN SELECT RAISE(ABORT,'comment_versions is append-only'); END;
CREATE TRIGGER IF NOT EXISTS comment_versions_no_delete BEFORE DELETE ON comment_versions
  BEGIN SELECT RAISE(ABORT,'comment_versions is append-only'); END;
`

// ensureComments legt die Tabellen additiv an, beliebig oft aufrufbar. Er läuft
// vor ensureGitAnchors: comment_versions trägt anchor_id und origin_id (siehe
// anchoredTables).
func ensureComments(db *sql.DB) error {
	_, err := db.Exec(commentSchema)
	return err
}

// Arten von Objekten, an die ein Kommentar hängt.
const (
	CommentOnRequest = "request"
	CommentOnIdea    = "idea"
	CommentOnPain    = "pain"
)

// MaxCommentChars begrenzt einen Kommentar, in Zeichen und nicht in Bytes: so zählt
// auch das Feld der Oberfläche, und ein deutscher Text mit Umlauten hat dort dieselbe
// Länge wie ohne. Die Grenze der Raum-Nachrichten (CoordMaxBody) zählt Bytes und ist davon unabhängig.
const MaxCommentChars = 64_000

// Comment ist ein Kommentar, wie ein Betrachter ihn sieht. Ein gelöschter
// Kommentar bleibt als Platzhalter in der Reihe (Deleted), ohne Text.
type Comment struct {
	ID         int64  `json:"id"`
	Project    string `json:"project"`
	ObjectKind string `json:"object_kind"`
	ObjectID   int64  `json:"object_id"`
	Body       string `json:"body,omitempty"`
	Person     string `json:"person,omitempty"`
	CreatedAt  string `json:"created_at"`
	// Revision zählt die Fassungen; EditedAt ist die Zeit der jüngsten.
	Revision int    `json:"revision"`
	EditedAt string `json:"edited_at,omitempty"`
	Deleted  bool   `json:"deleted,omitempty"`
	// DeletedBy und DeletedAt stehen nur an einem gelöschten Kommentar.
	DeletedBy string `json:"deleted_by,omitempty"`
	DeletedAt string `json:"deleted_at,omitempty"`
	// GitAnchor ist der Git-Stand der jüngsten Fassung (REQ-597), Via "web" bei
	// einem Kommentar aus dem Browser. Beides nur, wenn es das gibt.
	GitAnchor *gitanchor.Anchor `json:"git_anchor,omitempty"`
	Via       string            `json:"via,omitempty"`
	// CanEdit und CanDelete sagen, was der Betrachter an diesem Kommentar darf;
	// die Oberfläche zeigt die Knöpfe danach.
	CanEdit   bool `json:"can_edit"`
	CanDelete bool `json:"can_delete"`
	// AuthorID und SessionID verlassen den Server nie.
	AuthorID  int64 `json:"-"`
	SessionID int64 `json:"-"`
}

// CommentVersion ist eine Fassung eines Kommentars.
type CommentVersion struct {
	Revision  int               `json:"revision"`
	Body      string            `json:"body"`
	Person    string            `json:"person,omitempty"`
	CreatedAt string            `json:"created_at"`
	GitAnchor *gitanchor.Anchor `json:"git_anchor,omitempty"`
	Via       string            `json:"via,omitempty"`
}

// CommentActor ist, wer schreibt. Der Server setzt alles daraus aus dem Token
// (Person, Konto) und dem Header (Anker); aus der Weboberfläche steht
// gitanchor.Web() als Anker.
type CommentActor struct {
	AccountID int64
	Person    string
	SessionID int64
	GitAnchor *gitanchor.Anchor
	Origin    *originctx.Context
}

func commentNotFound(id int64) error {
	return requestdomain.NewRuleError("comment_not_found", fmt.Sprintf("no comment %d", id), "list the comments of the object to find its id", nil)
}

func commentTargetNotFound(kind string, id int64) error {
	return requestdomain.NewRuleError("comment_target_not_found", fmt.Sprintf("%s %d not found, or you may not read it", kind, id), "name an issue, idea or pain you can read", nil)
}

// commentTarget prüft, dass der Betrachter das Objekt lesen darf, und liefert
// dessen Projekt. Was es nicht gibt, was er nicht lesen darf und was kein
// Projekt hat, antwortet gleich (comment_target_not_found): ein Kommentar erbt
// das Leserecht des Objekts.
func (a *ProjectAccess) commentTarget(kind string, id int64) (string, error) {
	notFound := commentTargetNotFound(kind, id)
	switch kind {
	case CommentOnRequest:
		ref, err := a.st.RequestRef(RefRequest, id)
		if err != nil || strings.TrimSpace(ref.Project) == "" || !requestReadable(a, ref) {
			a.Filtered()
			return "", notFound
		}
		return ref.Project, nil
	case CommentOnIdea:
		i, err := a.st.IdeaByID(id)
		if err != nil || !a.CanSeeIdea(i) {
			a.Filtered()
			return "", notFound
		}
		return i.Project, nil
	case CommentOnPain:
		p, err := a.st.PainByID(id)
		if err != nil || a.CheckPain(p, ActRead) != nil {
			a.Filtered()
			return "", notFound
		}
		return p.Project, nil
	}
	return "", requestdomain.NewRuleError("invalid_comment_target", "comments hang on request, idea or pain", "use kind request, idea or pain", nil)
}

func checkCommentBody(body string) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", requestdomain.NewRuleError("comment_empty", "a comment needs text", "write what you want to say", nil)
	}
	if !utf8.ValidString(body) || utf8.RuneCountInString(body) > MaxCommentChars {
		return "", requestdomain.NewRuleError("comment_too_long", fmt.Sprintf("a comment has at most %d characters of text", MaxCommentChars), "shorten it or put the long text in a document", nil)
	}
	return body, nil
}

func (a *ProjectAccess) commentObject(c Comment) Object {
	return Object{Own: a.valid && a.account != 0 && c.AuthorID == a.account}
}

// decorate setzt, was der Betrachter an dem Kommentar darf.
func (a *ProjectAccess) decorateComment(c *Comment) {
	obj := a.commentObject(*c)
	c.CanEdit = !c.Deleted && a.Allow(c.Project, ResComment, ActEdit, obj)
	c.CanDelete = !c.Deleted && a.Allow(c.Project, ResComment, ActDelete, obj)
}

// AddComment schreibt einen Kommentar an ein Objekt, das der Betrachter lesen
// darf. Schreiben dürfen Mitglieder; ein Gast liest nur (403).
func (a *ProjectAccess) AddComment(kind string, objectID int64, body string, actor CommentActor) (Comment, error) {
	project, err := a.commentTarget(kind, objectID)
	if err != nil {
		return Comment{}, err
	}
	if err := a.Check(project, ResComment, ActCreate, Object{Own: true}); err != nil {
		return Comment{}, err
	}
	body, err = checkCommentBody(body)
	if err != nil {
		return Comment{}, err
	}
	actor.AccountID = a.account
	id, err := a.st.commitComment(project, kind, objectID, body, actor)
	if err != nil {
		return Comment{}, err
	}
	return a.commentView(id)
}

// Comments listet die Kommentare eines Objekts, die ältesten zuerst, gelöschte
// als Platzhalter ohne Text. Wer das Objekt nicht lesen darf, bekommt dieselbe
// Antwort wie für eines, das es nicht gibt.
func (a *ProjectAccess) Comments(kind string, objectID int64) ([]Comment, error) {
	project, err := a.commentTarget(kind, objectID)
	if err != nil {
		return nil, err
	}
	list, err := a.st.commentsOf(kind, objectID)
	if err != nil {
		return nil, err
	}
	for i := range list {
		// Das Projekt des Objekts gilt jetzt, nicht das beim Schreiben gemerkte:
		// ein verschobenes Objekt nimmt seine Kommentare mit.
		list[i].Project = project
		a.decorateComment(&list[i])
	}
	return list, nil
}

// commentFor lädt einen Kommentar, dessen Objekt der Betrachter lesen darf.
func (a *ProjectAccess) commentFor(id int64) (Comment, error) {
	c, err := a.st.commentByID(id)
	if err != nil {
		a.Filtered()
		return Comment{}, commentNotFound(id)
	}
	project, err := a.commentTarget(c.ObjectKind, c.ObjectID)
	if err != nil {
		return Comment{}, commentNotFound(id)
	}
	// Jede Rechteprüfung am Kommentar nimmt das jetzige Projekt des Objekts, nicht
	// das beim Schreiben gemerkte (comments.project): das Objekt kann inzwischen
	// in ein anderes Projekt verschoben worden sein.
	c.Project = project
	return c, nil
}

func (a *ProjectAccess) commentView(id int64) (Comment, error) {
	c, err := a.commentFor(id)
	if err != nil {
		return Comment{}, err
	}
	a.decorateComment(&c)
	return c, nil
}

// CommentView liefert einen Kommentar für den Betrachter.
func (a *ProjectAccess) CommentView(id int64) (Comment, error) { return a.commentView(id) }

// EditComment ersetzt den Text durch eine neue Fassung; die alten bleiben
// (CommentVersions). Nur der Autor darf, und nur, solange er Mitglied ist; ein
// gelöschter Kommentar bleibt, wie er ist.
func (a *ProjectAccess) EditComment(id int64, body string, actor CommentActor) (Comment, error) {
	c, err := a.commentFor(id)
	if err != nil {
		return Comment{}, err
	}
	if c.Deleted {
		return Comment{}, commentNotFound(id)
	}
	if err := a.Check(c.Project, ResComment, ActEdit, a.commentObject(c)); err != nil {
		return Comment{}, err
	}
	body, err = checkCommentBody(body)
	if err != nil {
		return Comment{}, err
	}
	actor.AccountID = a.account
	if body != c.Body {
		if err := a.st.commitCommentEdit(id, body, actor); err != nil {
			return Comment{}, err
		}
	}
	return a.commentView(id)
}

// DeleteComment löscht weich: der Kommentar bleibt als Platzhalter, sein Text
// und seine Fassungen bleiben in der Datenbank, sind aber nicht mehr zu lesen.
// Der Autor darf, ein Lead oder Owner auch.
func (a *ProjectAccess) DeleteComment(id int64, actor CommentActor) (Comment, error) {
	c, err := a.commentFor(id)
	if err != nil {
		return Comment{}, err
	}
	if c.Deleted {
		return Comment{}, commentNotFound(id)
	}
	if err := a.Check(c.Project, ResComment, ActDelete, a.commentObject(c)); err != nil {
		return Comment{}, err
	}
	if err := a.st.commitCommentDelete(id, actor.Person); err != nil {
		return Comment{}, err
	}
	return a.commentView(id)
}

// CommentVersions liefert die Fassungen eines Kommentars, die älteste zuerst.
// Ein gelöschter Kommentar gibt keine mehr her.
func (a *ProjectAccess) CommentVersions(id int64) ([]CommentVersion, error) {
	c, err := a.commentFor(id)
	if err != nil {
		return nil, err
	}
	if c.Deleted {
		return nil, commentNotFound(id)
	}
	return a.st.commentVersionsOf(id)
}

func (s *Store) commitComment(project, kind string, objectID int64, body string, actor CommentActor) (int64, error) {
	if s.writer != nil {
		return queueValue(s, []any{project, kind, objectID, body, actor}, func(d *Store, p []any) (int64, error) {
			return d.commitComment(p[0].(string), p[1].(string), p[2].(int64), p[3].(string), p[4].(CommentActor))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ts := now()
	res, err := tx.Exec(`INSERT INTO comments(project,object_kind,object_id,body,person,account_id,session_id,revision,created_at) VALUES(?,?,?,?,?,?,?,1,?)`,
		project, kind, objectID, body, actor.Person, actor.AccountID, actor.SessionID, ts)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	if err := insertCommentVersionTx(tx, id, 1, body, actor, ts); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func insertCommentVersionTx(tx *sql.Tx, id int64, revision int, body string, actor CommentActor, ts string) error {
	anchorID, originID, err := insertWriteMetaTx(tx, actor.GitAnchor, actor.Origin)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO comment_versions(comment_id,revision,body,person,created_at,anchor_id,origin_id) VALUES(?,?,?,?,?,?,?)`,
		id, revision, body, actor.Person, ts, anchorID, originID)
	return err
}

func (s *Store) commitCommentEdit(id int64, body string, actor CommentActor) error {
	if s.writer != nil {
		return queueWrite(s, []any{id, body, actor}, func(d *Store, p []any) error {
			return d.commitCommentEdit(p[0].(int64), p[1].(string), p[2].(CommentActor))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	res, err := tx.Exec(`UPDATE comments SET body=?, revision=revision+1, edited_at=? WHERE id=? AND deleted_at=''`, body, ts, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return commentNotFound(id)
	}
	var revision int
	if err := tx.QueryRow(`SELECT revision FROM comments WHERE id=?`, id).Scan(&revision); err != nil {
		return err
	}
	if err := insertCommentVersionTx(tx, id, revision, body, actor, ts); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) commitCommentDelete(id int64, by string) error {
	if s.writer != nil {
		return queueWrite(s, []any{id, by}, func(d *Store, p []any) error {
			return d.commitCommentDelete(p[0].(int64), p[1].(string))
		})
	}
	res, err := s.db.Exec(`UPDATE comments SET deleted_at=?, deleted_by=? WHERE id=? AND deleted_at=''`, now(), by, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return commentNotFound(id)
	}
	return nil
}

const commentCols = `c.id,c.project,c.object_kind,c.object_id,c.body,c.person,c.account_id,c.session_id,c.revision,c.created_at,c.edited_at,c.deleted_at,c.deleted_by,
	COALESCE((SELECT v.id FROM comment_versions v WHERE v.comment_id=c.id ORDER BY v.revision DESC LIMIT 1),0)`

func scanComments(rows *sql.Rows) ([]Comment, []int64, error) {
	var out []Comment
	var versions []int64
	for rows.Next() {
		var c Comment
		var version int64
		if err := rows.Scan(&c.ID, &c.Project, &c.ObjectKind, &c.ObjectID, &c.Body, &c.Person, &c.AuthorID, &c.SessionID, &c.Revision,
			&c.CreatedAt, &c.EditedAt, &c.DeletedAt, &c.DeletedBy, &version); err != nil {
			return nil, nil, err
		}
		if c.DeletedAt != "" {
			c.Deleted, c.Body = true, ""
		} else {
			c.DeletedBy = ""
		}
		out = append(out, c)
		versions = append(versions, version)
	}
	return out, versions, rows.Err()
}

// attachCommentMeta hängt Anker und Weg der jüngsten Fassung an.
func (s *Store) attachCommentMeta(list []Comment, versions []int64) error {
	if len(list) == 0 {
		return nil
	}
	anchors, err := s.anchorsFor("comment_versions", versions)
	if err != nil {
		return err
	}
	via, err := s.viaFor("comment_versions", versions)
	if err != nil {
		return err
	}
	for i := range list {
		list[i].GitAnchor = anchors[versions[i]]
		list[i].Via = via[versions[i]]
		if list[i].Deleted {
			list[i].GitAnchor, list[i].Via = nil, ""
		}
	}
	return nil
}

func (s *Store) commentByID(id int64) (Comment, error) {
	if s.reader != nil {
		return s.reader.commentByID(id)
	}
	rows, err := s.db.Query(`SELECT `+commentCols+` FROM comments c WHERE c.id=?`, id)
	if err != nil {
		return Comment{}, err
	}
	list, versions, err := scanComments(rows)
	rows.Close()
	if err != nil {
		return Comment{}, err
	}
	if len(list) == 0 {
		return Comment{}, sql.ErrNoRows
	}
	if err := s.attachCommentMeta(list, versions); err != nil {
		return Comment{}, err
	}
	return list[0], nil
}

func (s *Store) commentsOf(kind string, objectID int64) ([]Comment, error) {
	if s.reader != nil {
		return s.reader.commentsOf(kind, objectID)
	}
	rows, err := s.db.Query(`SELECT `+commentCols+` FROM comments c WHERE c.object_kind=? AND c.object_id=? ORDER BY c.id`, kind, objectID)
	if err != nil {
		return nil, err
	}
	list, versions, err := scanComments(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err := s.attachCommentMeta(list, versions); err != nil {
		return nil, err
	}
	if list == nil {
		list = []Comment{}
	}
	return list, nil
}

func (s *Store) commentVersionsOf(id int64) ([]CommentVersion, error) {
	if s.reader != nil {
		return s.reader.commentVersionsOf(id)
	}
	rows, err := s.db.Query(`SELECT id,revision,body,person,created_at FROM comment_versions WHERE comment_id=? ORDER BY revision`, id)
	if err != nil {
		return nil, err
	}
	var out []CommentVersion
	var ids []int64
	for rows.Next() {
		var v CommentVersion
		var vid int64
		if err := rows.Scan(&vid, &v.Revision, &v.Body, &v.Person, &v.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, v)
		ids = append(ids, vid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, sql.ErrNoRows
	}
	anchors, err := s.anchorsFor("comment_versions", ids)
	if err != nil {
		return nil, err
	}
	via, err := s.viaFor("comment_versions", ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].GitAnchor, out[i].Via = anchors[ids[i]], via[ids[i]]
	}
	return out, nil
}
