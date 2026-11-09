package store

import (
	"errors"
	"strconv"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func TestTaskStepsAreTickedAndUntickedInTheWebByWhoMayDoTheTask(t *testing.T) {
	w := newPainWorld(t)
	rid := w.request(t, "Zahlung")
	task := w.task(t, "mia", humantask.CreateInput{RequestID: rid, Kind: "action", Title: "Key anlegen",
		Steps: []humantask.Step{{Text: "Tresor öffnen"}, {Text: "Datei benennen"}}})
	pa := w.pa["mia"]
	acct, _ := accountNumericID(pa.principal.ID)
	web := humantask.Actor{AccountID: acct, Person: "mia", Web: true}

	if err := pa.SetTaskStep(task.ID, 1, true, web); err != nil {
		t.Fatal(err)
	}
	marks, err := w.st.TaskStepMarks(task.ID)
	if err != nil || len(marks) != 1 || marks[1].By != "mia" || marks[1].At == "" {
		t.Fatalf("marks: %+v %v", marks, err)
	}
	// Ticking twice changes nothing, unticking removes it, the other step is untouched.
	if err := pa.SetTaskStep(task.ID, 1, true, web); err != nil {
		t.Fatal(err)
	}
	if err := pa.SetTaskStep(task.ID, 2, true, web); err != nil {
		t.Fatal(err)
	}
	if err := pa.SetTaskStep(task.ID, 1, false, web); err != nil {
		t.Fatal(err)
	}
	if marks, _ = w.st.TaskStepMarks(task.ID); len(marks) != 1 || marks[2].By != "mia" {
		t.Fatalf("after unticking: %+v", marks)
	}

	// Not from a token, not outside the steps, not without the right to do the task.
	if err := pa.SetTaskStep(task.ID, 1, true, humantask.Actor{AccountID: acct, Person: "mia"}); ruleCode(err) != "web_only" {
		t.Errorf("from a token: %v", err)
	}
	for _, step := range []int{0, 3, -1} {
		if err := pa.SetTaskStep(task.ID, step, true, web); ruleCode(err) != "step_invalid" {
			t.Errorf("step %d: %v", step, err)
		}
	}
	if err := w.pa["gus"].SetTaskStep(task.ID, 1, true, humantask.Actor{Person: "gus", Web: true}); !errors.Is(err, ErrAccessForbidden) && !errors.Is(err, ErrAccessNotFound) {
		t.Errorf("guest of a task of everyone: %v", err)
	}
	if err := pa.SetTaskStep(99999, 1, true, web); err == nil {
		t.Error("an unknown task takes a tick")
	}
	// A task that is done takes no tick any more.
	if _, err := pa.MarkTasksDone([]int64{task.ID}, "", web); err != nil {
		t.Fatal(err)
	}
	if err := pa.SetTaskStep(task.ID, 1, true, web); ruleCode(err) != "invalid_transition" {
		t.Errorf("done task: %v", err)
	}
}

// Wer das Projekt liest, aber den Auftrag hinter einer Aufgabe nicht sehen darf,
// erfährt nicht, dass es sie gibt: Lesen und Sicht kommen vor dem Recht zu arbeiten.
func TestTaskOnAHiddenRequestAnswersLikeAnUnknownOneBeforeTheWorkRight(t *testing.T) {
	w := newPainWorld(t)
	rid := w.request(t, "Verborgen")
	assigned := w.task(t, "mia", humantask.CreateInput{RequestID: rid, Kind: "action", Title: "an Lena", Assignee: "lena", Steps: []humantask.Step{{Text: "eins"}}})
	free := w.task(t, "mia", humantask.CreateInput{RequestID: rid, Kind: "action", Title: "frei", Steps: []humantask.Step{{Text: "eins"}}})
	sid, err := w.st.UpsertSession(Session{Harness: "claude-code", ExternalID: "priv-hidden-task", AccountID: 3, Scope: scope.Axes{Project: roleProject}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.st.DB().Exec(`UPDATE requests SET origin='distilled', person='', session_ref=? WHERE id=?`, "session:"+strconv.FormatInt(sid, 10), rid); err != nil {
		t.Fatal(err)
	}
	rex := w.pa["rex"]
	web := humantask.Actor{AccountID: 4, Person: "rex", Web: true}
	if err := rex.SetTaskStep(assigned.ID, 1, true, web); !errors.Is(err, ErrAccessNotFound) {
		t.Errorf("step of a task assigned to another on a hidden request: %v, want not found", err)
	}
	if err := rex.SetTaskStep(free.ID, 1, true, web); !errors.Is(err, ErrAccessNotFound) {
		t.Errorf("step of a task on a hidden request: %v, want not found", err)
	}
	if _, err := rex.MarkTasksDone([]int64{assigned.ID}, "", web); !errors.Is(err, ErrAccessNotFound) {
		t.Errorf("done on a task assigned to another on a hidden request: %v, want not found", err)
	}
	if _, err := rex.MarkTasksDone([]int64{free.ID}, "", web); !errors.Is(err, ErrAccessNotFound) {
		t.Errorf("done on a task on a hidden request: %v, want not found", err)
	}
	if got, _ := w.st.TaskByID(free.ID); got.State != humantask.Open {
		t.Errorf("the hidden task was marked done: %s", got.State)
	}
	// Wer den Auftrag sehen darf, behält die alte Antwort für fremde Aufgaben.
	if _, err := w.pa["mia"].MarkTasksDone([]int64{assigned.ID}, "", humantask.Actor{AccountID: 3, Web: true}); !errors.Is(err, ErrAccessForbidden) {
		t.Errorf("the author of the request on a task of another: %v", err)
	}
}
