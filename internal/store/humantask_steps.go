package store

import (
	"database/sql"

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
