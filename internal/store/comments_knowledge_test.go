package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

const oldCommentsDDL = `CREATE TABLE comments(
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
  deleted_by TEXT NOT NULL DEFAULT '')`

// The table of comments learns the kind knowledge without losing a comment, a
// number or a version, and a second start changes nothing. There is no way back
// (see allowKnowledgeComments); the old stand is rebuilt here only to have
// something to migrate.
func TestCommentsLearnTheKnowledgeKindWithoutLosingRowsOrVersions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`PRAGMA foreign_keys=OFF`,
		`DROP TABLE comments`,
		oldCommentsDDL,
		`CREATE INDEX comments_object ON comments(object_kind, object_id, id)`,
		`INSERT INTO comments(id,project,object_kind,object_id,body,person,revision,created_at,edited_at) VALUES(7,'github.com/x/y','pain',1,'alt','mia',2,'t1','t2')`,
		`INSERT INTO comments(id,project,object_kind,object_id,body,person,created_at,deleted_at,deleted_by) VALUES(9,'github.com/x/y','idea',4,'weg','rex','t1','t3','rex')`,
		`INSERT INTO comment_versions(comment_id,revision,body,person,created_at) VALUES(7,1,'erste','mia','t1')`,
		`INSERT INTO comment_versions(comment_id,revision,body,person,created_at) VALUES(7,2,'alt','mia','t2')`,
	} {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := st.DB().Exec(`INSERT INTO comments(project,object_kind,object_id,body,created_at) VALUES('p','knowledge',1,'x','t')`); err == nil {
		t.Fatal("the old table accepted the kind knowledge")
	}
	st.Close()
	for run := 1; run <= 2; run++ {
		st, err = Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", run, err)
		}
		var n, versions int
		var body, edited string
		st.DB().QueryRow(`SELECT COUNT(*) FROM comments`).Scan(&n)
		st.DB().QueryRow(`SELECT COUNT(*) FROM comment_versions`).Scan(&versions)
		// The second start finds the two old rows and the one written after the first.
		if err := st.DB().QueryRow(`SELECT body, edited_at FROM comments WHERE id=7`).Scan(&body, &edited); err != nil || body != "alt" || edited != "t2" || n != run+1 || versions != 2 {
			t.Fatalf("open %d: rows %d, versions %d, comment 7 %q %q %v", run, n, versions, body, edited, err)
		}
		if run == 1 {
			if _, err := st.DB().Exec(`INSERT INTO comments(project,object_kind,object_id,body,created_at) VALUES('github.com/x/y','knowledge',1,'neu','t')`); err != nil {
				t.Fatalf("the migrated table refuses the kind knowledge: %v", err)
			}
			if _, err := st.DB().Exec(`INSERT INTO comments(project,object_kind,object_id,body,created_at) VALUES('p','nonsense',1,'x','t')`); err == nil {
				t.Error("the migrated table accepts any kind")
			}
			// Foreign keys are on again, and the versions still point at their comment.
			var fk int
			if err := st.DB().QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
				t.Errorf("foreign_keys = %d %v", fk, err)
			}
			if rows, err := st.DB().Query(`PRAGMA foreign_key_check(comment_versions)`); err != nil {
				t.Fatal(err)
			} else {
				if rows.Next() {
					t.Error("a version points at a comment that is gone")
				}
				rows.Close()
			}
			if _, err := st.DB().Exec(`DELETE FROM comments WHERE id=7`); err == nil {
				t.Error("the comment with versions could be deleted (RESTRICT is gone)")
			}
		}
		if !strings.Contains(tableSQL(t, st, "comments"), "'knowledge'") {
			t.Errorf("open %d: the table does not know knowledge", run)
		}
		st.Close()
	}
	// A new database has the new definition from the start and migrates nothing.
	fresh, err := Open(filepath.Join(t.TempDir(), "new.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if !strings.Contains(tableSQL(t, fresh, "comments"), "'knowledge'") {
		t.Error("a new database lacks the kind knowledge")
	}
}

// A comment or reaction on an entry follows the rights of the entry and of a
// comment on an issue: a member writes, a guest reads, a stranger finds nothing;
// an entry without a project takes none.
func TestKnowledgeCommentsAndReactionsFollowTheRights(t *testing.T) {
	w := newPainWorld(t)
	id, err := w.st.InsertKnowledge(Knowledge{Type: "note", Title: "Lock order", Body: "Store lock first.", Person: "mia", Scope: scope.Axes{Project: roleProject}})
	if err != nil {
		t.Fatal(err)
	}
	global, err := w.st.InsertKnowledge(Knowledge{Type: "note", Title: "Global", Body: "x", Person: "mia"})
	if err != nil {
		t.Fatal(err)
	}
	actor := CommentActor{Person: "mia"}
	c, err := w.pa["mia"].AddComment(CommentOnKnowledge, id, "Gilt das auch bei verschachtelten Sperren?", actor)
	if err != nil || c.ObjectKind != CommentOnKnowledge || c.Project != roleProject || !c.CanEdit {
		t.Fatalf("member comment: %+v %v", c, err)
	}
	if _, err := w.pa["gus"].AddComment(CommentOnKnowledge, id, "Gast", CommentActor{Person: "gus"}); !errors.Is(err, ErrAccessForbidden) {
		t.Errorf("guest comment: %v, want forbidden", err)
	}
	list, err := w.pa["gus"].Comments(CommentOnKnowledge, id)
	if err != nil || len(list) != 1 || list[0].CanEdit || list[0].CanDelete {
		t.Errorf("guest list: %+v %v", list, err)
	}
	if _, err := w.pa["nora"].Comments(CommentOnKnowledge, id); err == nil {
		t.Error("a stranger reads the comments of an entry of a project without a role")
	}
	if _, err := w.pa["nora"].AddComment(CommentOnKnowledge, id, "x", CommentActor{Person: "nora"}); err == nil {
		t.Error("a stranger comments")
	}
	if _, err := w.pa["mia"].AddComment(CommentOnKnowledge, global, "x", actor); err == nil {
		t.Error("an entry without a project takes a comment")
	}
	if _, err := w.pa["mia"].Comments(CommentOnKnowledge, 999999); err == nil {
		t.Error("an entry that does not exist has comments")
	}
	// Reactions.
	if _, err := w.pa["gus"].React(ReactOnKnowledge, id, "heart"); !errors.Is(err, ErrAccessForbidden) {
		t.Errorf("guest reaction: %v, want forbidden", err)
	}
	s, err := w.pa["mia"].React(ReactOnKnowledge, id, "heart")
	if err != nil || len(s.Items) != 1 || !s.CanReact || !s.Items[0].Mine {
		t.Fatalf("member reaction: %+v %v", s, err)
	}
	if s, err := w.pa["gus"].Reactions(ReactOnKnowledge, id); err != nil || s.CanReact || len(s.Items) != 1 || s.Items[0].Count != 1 {
		t.Errorf("guest sees: %+v %v", s, err)
	}
	if _, err := w.pa["nora"].Reactions(ReactOnKnowledge, id); err == nil {
		t.Error("a stranger reads reactions of the entry")
	}
	if _, err := w.pa["mia"].React(ReactOnKnowledge, global, "heart"); err == nil {
		t.Error("an entry without a project takes a reaction")
	}
}

// The check after the rebuild counts only what points at comments: a version row
// is also checked against every other foreign key of its table, and a break there
// is not the business of this migration and must not stop the start.
func TestCommentsRebuildCheckCountsOnlyReferencesToComments(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "fk.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	conn, err := st.DB().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, q := range []string{
		`PRAGMA defer_foreign_keys=ON`,
		`DROP TABLE comment_versions`,
		`CREATE TABLE zz_other(id INTEGER PRIMARY KEY)`,
		`CREATE TABLE comment_versions(id INTEGER PRIMARY KEY,
		   comment_id INTEGER NOT NULL REFERENCES comments(id),
		   other_id INTEGER REFERENCES zz_other(id))`,
		`INSERT INTO comments(id,project,object_kind,object_id,body,created_at) VALUES(1,'p','pain',1,'x','t')`,
		// Broken against the other table only.
		`INSERT INTO comment_versions(id,comment_id,other_id) VALUES(1,1,77)`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if broken, err := commentsReferenceBroken(ctx, tx); err != nil || broken {
		t.Fatalf("a break against another table counted as one against comments: %v %v", broken, err)
	}
	// Broken against comments: counted, together with the other one.
	if _, err := tx.ExecContext(ctx, `INSERT INTO comment_versions(id,comment_id,other_id) VALUES(2,555,NULL)`); err != nil {
		t.Fatal(err)
	}
	if broken, err := commentsReferenceBroken(ctx, tx); err != nil || !broken {
		t.Fatalf("a version of a missing comment was not found: %v %v", broken, err)
	}
}
