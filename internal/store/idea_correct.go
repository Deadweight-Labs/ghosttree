package store

import (
	"database/sql"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// The correction of an idea (UI review P13.3): title and text can be changed, the
// reason is written down, and what stood before is kept, so the page can say
// "corrected the title: old → new" the way a pain does. A pain keeps this in its
// events; an idea has none, so the corrections are a table of their own, append
// only like the events of a pain. Nothing else about the idea changes: its state,
// its workshop and its comments stay.
const ideaEditSchema = `
CREATE TABLE IF NOT EXISTS idea_edits(
  id INTEGER PRIMARY KEY,
  idea_id INTEGER NOT NULL REFERENCES ideas(id) ON DELETE RESTRICT,
  changed TEXT NOT NULL,
  old_title TEXT NOT NULL,
  old_body TEXT NOT NULL,
  reason TEXT NOT NULL,
  person TEXT NOT NULL DEFAULT '',
  account_id INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS idea_edits_idea ON idea_edits(idea_id, id);
CREATE TRIGGER IF NOT EXISTS idea_edits_no_update BEFORE UPDATE ON idea_edits
  BEGIN SELECT RAISE(ABORT,'idea_edits is append-only'); END;
CREATE TRIGGER IF NOT EXISTS idea_edits_no_delete BEFORE DELETE ON idea_edits
  BEGIN SELECT RAISE(ABORT,'idea_edits is append-only'); END;
`

func ensureIdeaEdits(db *sql.DB) error {
	_, err := db.Exec(ideaEditSchema)
	return err
}

// IdeaEdit is one correction: Changed is "title", "body" or "title,body"; Old*
// is what stood before. Person and AccountID are who corrected it, as a pain
// event keeps them.
type IdeaEdit struct {
	ID                int64
	Changed           string
	OldTitle, OldBody string
	Reason, Person    string
	AccountID         int64
	CreatedAt         string
}

// IdeaEditsOf lists the corrections of an idea, oldest first. The caller checks
// that the viewer may read the idea.
func (s *Store) IdeaEditsOf(ideaID int64) ([]IdeaEdit, error) {
	if s.reader != nil {
		return s.reader.IdeaEditsOf(ideaID)
	}
	rows, err := s.db.QueryContext(s.ctx(), `SELECT id, changed, old_title, old_body, reason, person, account_id, created_at FROM idea_edits WHERE idea_id=? ORDER BY id`, ideaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IdeaEdit
	for rows.Next() {
		var e IdeaEdit
		if err := rows.Scan(&e.ID, &e.Changed, &e.OldTitle, &e.OldBody, &e.Reason, &e.Person, &e.AccountID, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CorrectIdea changes the title or the text of an idea. Who may: whoever may edit
// the idea (its author, a maintainer), like correcting an issue. The reason is
// required.
func (a *ProjectAccess) CorrectIdea(id int64, title, body *string, reason string, actor idea.Actor) (idea.Idea, error) {
	i, err := a.st.IdeaByID(id)
	if err != nil {
		a.Filtered()
		return idea.Idea{}, ideaNotFound(id)
	}
	if err := a.CheckIdea(i, ActEdit); err != nil {
		if err == ErrAccessNotFound {
			return idea.Idea{}, ideaNotFound(id)
		}
		return idea.Idea{}, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return idea.Idea{}, requestdomain.NewRuleError("reason_required", "this change needs a reason", "say why in reason", nil)
	}
	newTitle, newBody := i.Title, i.Body
	var changed []string
	if title != nil {
		t, err := idea.CleanTitle(*title)
		if err != nil {
			return idea.Idea{}, err
		}
		if t != i.Title {
			newTitle = t
			changed = append(changed, "title")
		}
	}
	if body != nil {
		b, err := idea.CleanBody(*body)
		if err != nil {
			return idea.Idea{}, err
		}
		if b != i.Body {
			newBody = b
			changed = append(changed, "body")
		}
	}
	if len(changed) == 0 {
		return idea.Idea{}, requestdomain.NewRuleError("nothing_to_change", "give a new title or text", "pass title or body", nil)
	}
	if err := a.st.commitIdeaCorrection(i, newTitle, newBody, reason, strings.Join(changed, ","), actor); err != nil {
		return idea.Idea{}, err
	}
	return a.IdeaView(id)
}

func (s *Store) commitIdeaCorrection(old idea.Idea, title, body, reason, changed string, actor idea.Actor) error {
	if s.writer != nil {
		return queueWrite(s, []any{old, title, body, reason, changed, actor}, func(d *Store, p []any) error {
			return d.commitIdeaCorrection(p[0].(idea.Idea), p[1].(string), p[2].(string), p[3].(string), p[4].(string), p[5].(idea.Actor))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	res, err := tx.Exec(`UPDATE ideas SET title=?, body=?, updated_at=? WHERE id=? AND title=? AND body=?`, title, body, ts, old.ID, old.Title, old.Body)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return requestdomain.NewRuleError("idea_changed", "the idea changed in the meantime", "read it again", nil)
	}
	if _, err := tx.Exec(`INSERT INTO idea_edits(idea_id,changed,old_title,old_body,reason,person,account_id,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		old.ID, changed, old.Title, old.Body, reason, actor.Person, actor.AccountID, ts); err != nil {
		return err
	}
	return tx.Commit()
}
