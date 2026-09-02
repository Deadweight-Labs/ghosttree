package agentbench

import (
	"bytes"
	"fmt"
	"os"
)

// Regrade scores an existing campaign again from its raw transcripts.
//
// It exists because a grading mistake is not the same kind of mistake as a
// broken run. The agent's answer is on disk, complete and unchanged; only the
// judgement of it was wrong. Re-running the campaign to fix a wrong list of
// accepted spellings would spend hours and real money to reproduce answers
// that are already recorded — and it would introduce new sampling noise on top,
// so the corrected numbers would not even be comparable to the old ones.
//
// The transcript is authoritative and never rewritten. What changes is only
// the task definition it is scored against.
func Regrade(records []RunRecord, tasks []Task) ([]RunRecord, error) {
	byID := make(map[string]Task, len(tasks))
	for _, task := range tasks {
		byID[task.ID] = task
	}

	out := make([]RunRecord, 0, len(records))
	for _, record := range records {
		task, ok := byID[record.TaskID]
		if !ok {
			return nil, fmt.Errorf("run for task %q has no task definition", record.TaskID)
		}
		// Ein Lauf, der gar nicht bis zur Bewertung kam, bleibt wie er ist.
		// Nur ein Bewertungsfehler wird korrigiert, kein Produktfehler
		// nachtraeglich in einen Treffer verwandelt.
		if record.Failure != FailureNone && record.Failure != FailureScoring {
			out = append(out, record)
			continue
		}
		// Die Herkunftsmarkierung kommt aus der Aufgabendefinition, nicht aus
		// dem alten Datensatz: sie kann sich aendern, ohne dass sich an der
		// Bewertung etwas aendert.
		record.DevelopmentData = task.DevelopmentData
		if record.Transcript.RawPath == "" {
			return nil, fmt.Errorf("run %q/%s has no raw transcript to regrade from",
				record.TaskID, record.Arm)
		}
		raw, err := os.ReadFile(record.Transcript.RawPath)
		if err != nil {
			return nil, err
		}
		transcript, err := parseStreamJSON(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		// Aufwandszahlen werden mitgezogen: ein aelteres Binary hat sie
		// vielleicht noch nicht in den Datensatz geschrieben, im Transkript
		// stehen sie trotzdem.
		record.Transcript.Turns = transcript.Turns
		record.Transcript.CostUSD = transcript.CostUSD
		record.Transcript.ToolCalls = transcript.ToolCalls
		record.Transcript.DelegatedToolCalls = transcript.DelegatedToolCalls
		record.Transcript.FilesRead = transcript.FilesRead
		record.Transcript.MaxTurnsExceeded = transcript.MaxTurnsExceeded

		form, err := ParseForm(transcript.Output)
		if err != nil {
			// Erschoepftes Zugbudget ist eine Enthaltung, kein Bewertungsfehler
			// — siehe runOne.
			if transcript.MaxTurnsExceeded {
				record.Failure, record.FailureMsg = FailureNone, ""
				record.Score = Grade(task, ResponseForm{Slots: map[string]SlotValue{}})
				out = append(out, record)
				continue
			}
			record.Failure = FailureScoring
			record.FailureMsg = err.Error()
			record.Score = Score{}
			out = append(out, record)
			continue
		}
		record.Failure, record.FailureMsg = FailureNone, ""
		record.Score = Grade(task, form)
		out = append(out, record)
	}
	return out, nil
}
