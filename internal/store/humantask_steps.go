package store

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// Abhaken je Schritt (REQ-603 Kriterium 3): die Schritte einer Aufgabe stehen im
// Task selbst (human_tasks.steps); wer sie abgearbeitet hat, steht daneben, eine
// Zeile je (Aufgabe, Schritt). Das ist additiv; bestehende Aufgaben ändern sich
// nicht. Abgehakt wird nur in der Weboberfläche und nur, solange die Aufgabe
// offen ist; das Recht ist das des Erledigens (ActWork).
const taskStepSchema = `
CREATE TABLE IF NOT EXISTS human_task_steps_done(
  task_id INTEGER NOT NULL REFERENCES human_tasks(id) ON DELETE CASCADE,
  step INTEGER NOT NULL CHECK(step >= 1),
  done_by_account INTEGER NOT NULL DEFAULT 0,
  done_by_person TEXT NOT NULL DEFAULT '',
  done_at TEXT NOT NULL,
  PRIMARY KEY(task_id, step));
CREATE TABLE IF NOT EXISTS human_task_notes(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id INTEGER NOT NULL REFERENCES human_tasks(id) ON DELETE CASCADE,
  by_account INTEGER NOT NULL DEFAULT 0,
  by_person TEXT NOT NULL DEFAULT '',
  body TEXT NOT NULL,
  created_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS human_task_notes_task ON human_task_notes(task_id, id);
`

func ensureTaskSteps(db *sql.DB) error {
	_, err := db.Exec(taskStepSchema)
	return err
}

// TaskStepMark sagt, wer einen Schritt wann abgehakt hat.
type TaskStepMark struct {
	By string `json:"by,omitempty"`
	At string `json:"at"`
}

// TaskStepMarks liefert die abgehakten Schritte einer Aufgabe nach ihrer Nummer
// (ab 1), ohne Sichtprüfung: der Aufrufer hat die Aufgabe schon lesen dürfen.
func (s *Store) TaskStepMarks(id int64) (map[int]TaskStepMark, error) {
	if s.reader != nil {
		return s.reader.TaskStepMarks(id)
	}
	rows, err := s.db.Query(`SELECT step, done_by_person, done_at FROM human_task_steps_done WHERE task_id=? ORDER BY step`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]TaskStepMark{}
	for rows.Next() {
		var step int
		var m TaskStepMark
		if err := rows.Scan(&step, &m.By, &m.At); err != nil {
			return nil, err
		}
		out[step] = m
	}
	return out, rows.Err()
}

// SetTaskStep hakt einen Schritt (ab 1) einer offenen Aufgabe ab oder nimmt das
// Häkchen zurück. Das Recht ist das des Erledigens; eine Aufgabe, die der
// Betrachter nicht lesen darf, ist unbekannt. Nur aus der Weboberfläche.
func (a *ProjectAccess) SetTaskStep(id int64, step int, done bool, actor humantask.Actor) error {
	t, err := a.st.TaskByID(id)
	if err != nil {
		a.Filtered()
		return err
	}
	if err := a.checkTaskWorkable(t, map[int64]bool{}); err != nil {
		return err
	}
	if !actor.Web {
		return requestdomain.NewRuleError("web_only", "steps are ticked in the web interface", "open the page of the task", nil)
	}
	if step < 1 || step > len(t.Steps) {
		return requestdomain.NewRuleError("step_invalid", "the task has no such step", "name a step of the task, counted from 1", nil)
	}
	if t.State != humantask.Open {
		return humantask.InvalidTransition(t.State, humantask.Open)
	}
	return a.st.commitTaskStep(id, step, done, actor)
}

func (s *Store) commitTaskStep(id int64, step int, done bool, actor humantask.Actor) error {
	if s.writer != nil {
		return queueWrite(s, []any{id, step, done, actor}, func(d *Store, p []any) error {
			return d.commitTaskStep(p[0].(int64), p[1].(int), p[2].(bool), p[3].(humantask.Actor))
		})
	}
	if !done {
		_, err := s.db.Exec(`DELETE FROM human_task_steps_done WHERE task_id=? AND step=?`, id, step)
		return err
	}
	_, err := s.db.Exec(`INSERT INTO human_task_steps_done(task_id, step, done_by_account, done_by_person, done_at) VALUES(?,?,?,?,?)
		ON CONFLICT(task_id, step) DO NOTHING`, id, step, actor.AccountID, actor.Person, now())
	return err
}

// Notiz an den Agenten (REQ-679): eine Zeile des Menschen an einer offenen Aufgabe,
// ohne sie als erledigt zu melden. Sie steht an der Aufgabe (Task.Notes, so liest sie
// der Agent mit dem Task) und, wenn die Aufgabe an einem Request hängt, in dessen
// Verlauf. Das Recht ist das des Erledigens; nur aus der Weboberfläche.

// MaxTaskNotes caps how many notes one task keeps, so a loop cannot grow it forever.
const MaxTaskNotes = 100

// AddTaskNote hängt eine Notiz an eine offene Aufgabe. Ein Geheimnis im Text wird
// abgelehnt wie bei der Erledigt-Meldung.
func (a *ProjectAccess) AddTaskNote(id int64, note string, actor humantask.Actor) error {
	note = strings.TrimSpace(note)
	if note == "" {
		return requestdomain.NewRuleError("note_required", "the note is empty", "write what the agent should know", nil)
	}
	if err := humantask.CheckText("note", note); err != nil {
		return err
	}
	t, err := a.st.TaskByID(id)
	if err != nil {
		a.Filtered()
		return err
	}
	if err := a.checkTaskWorkable(t, map[int64]bool{}); err != nil {
		return err
	}
	if !actor.Web {
		return requestdomain.NewRuleError("web_only", "notes are written in the web interface", "open the page of the task", nil)
	}
	if t.State != humantask.Open {
		return humantask.InvalidTransition(t.State, humantask.Open)
	}
	if len(t.Notes) >= MaxTaskNotes {
		return requestdomain.NewRuleError("too_many_notes", "this task has all the notes it takes", "report the task done or ask in the room", nil)
	}
	return a.st.commitTaskNote(t, note, actor)
}

func (s *Store) commitTaskNote(t humantask.Task, note string, actor humantask.Actor) error {
	if s.writer != nil {
		return queueWrite(s, []any{t, note, actor}, func(d *Store, p []any) error {
			return d.commitTaskNote(p[0].(humantask.Task), p[1].(string), p[2].(humantask.Actor))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	if _, err := tx.Exec(`INSERT INTO human_task_notes(task_id,by_account,by_person,body,created_at) VALUES(?,?,?,?,?)`,
		t.ID, actor.AccountID, actor.Person, note, ts); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE human_tasks SET updated_at=? WHERE id=?`, ts, t.ID); err != nil {
		return err
	}
	if t.RequestID != 0 {
		if err := linkActivity(tx, t.RequestID, "task.note", fmt.Sprintf("TASK-%d: %s", t.ID, oneLine(note, 300)), actor.Person, actor.SessionID, ts); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// attachTaskNotes fills Task.Notes of the given tasks, oldest first.
func (s *Store) attachTaskNotes(tasks []humantask.Task) error {
	if len(tasks) == 0 {
		return nil
	}
	index := make(map[int64]int, len(tasks))
	ids := make([]int64, 0, len(tasks))
	for i, t := range tasks {
		index[t.ID] = i
		ids = append(ids, t.ID)
	}
	return chunked(ids, func(part []int64) error {
		rows, err := s.db.Query(`SELECT task_id, by_person, body, created_at FROM human_task_notes WHERE task_id IN (`+placeholders(len(part))+`) ORDER BY id`, factIDArgs(part)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var n humantask.Note
			if err := rows.Scan(&id, &n.By, &n.Text, &n.At); err != nil {
				return err
			}
			tasks[index[id]].Notes = append(tasks[index[id]].Notes, n)
		}
		return rows.Err()
	})
}
