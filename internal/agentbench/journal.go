package agentbench

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// runKey identifies one run of the plan. Task, arm and repetition together are
// what the campaign schedules, so they are also what "already done" means.
type runKey struct {
	task string
	arm  ArmName
	rep  int
}

func keyOf(record RunRecord) runKey {
	return runKey{task: record.TaskID, arm: record.Arm, rep: record.Repetition}
}

// RunJournal makes each judgement durable at the moment it is made, and is the
// ledger a resumed campaign reads to know what it need not run again.
//
// It exists because a campaign is long and the machine it runs on is not
// promised to stay up. The pilot lost 33 scored runs to a reboot at 01:35:
// every transcript survived on disk, because the agent writes one per run, but
// the records existed only in memory until the last of 72 runs returned. A
// campaign that can only ever finish or lose everything cannot be left alone
// overnight, which is exactly what a campaign is for.
//
// Resuming changes nothing about the design: the block order is derived from
// the seed, so the same runs land on the same arms in the same order whether
// the campaign runs in one stretch or five. Only the wall clock differs.
//
// A recorded failure stays recorded and is not run again. That is deliberate.
// Re-rolling failed runs until they succeed would quietly select for the runs
// that happened to work, and the rate at which an arm fails is itself a result.
type RunJournal struct {
	path    string
	file    *os.File
	done    map[runKey]bool
	records []RunRecord
}

// OpenRunJournal reads whatever is already recorded at path and opens it for
// appending. A missing file is the normal case of a fresh campaign.
func OpenRunJournal(path string) (*RunJournal, error) {
	journal := &RunJournal{path: path, done: map[runKey]bool{}}
	if err := journal.load(); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	journal.file = file
	return journal, nil
}

func (j *RunJournal) load() error {
	file, err := os.Open(j.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		var record RunRecord
		if err := json.Unmarshal([]byte(text), &record); err != nil {
			// Eine halbe Zeile am Ende ist der erwartete Abdruck eines
			// Absturzes mitten im Schreiben, kein Grund zur Panik — aber sie
			// wird benannt, nicht stillschweigend geschluckt, denn eine
			// unlesbare Zeile in der Mitte hiesse etwas ganz anderes.
			return fmt.Errorf("%s line %d is not a run record: %w", j.path, line, err)
		}
		j.records = append(j.records, record)
		j.done[keyOf(record)] = true
	}
	return scanner.Err()
}

// Done answers whether this run is already recorded.
func (j *RunJournal) Done(taskID string, arm ArmName, repetition int) bool {
	return j.done[runKey{task: taskID, arm: arm, rep: repetition}]
}

// Emit appends a record and flushes it to disk before returning. The extra
// sync per run is nothing against the minute the run itself took, and it is
// the whole point: the record must outlive the process that made it.
func (j *RunJournal) Emit(record RunRecord) error {
	line, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if _, err := j.file.Write(append(line, '\n')); err != nil {
		return err
	}
	if err := j.file.Sync(); err != nil {
		return err
	}
	j.records = append(j.records, record)
	j.done[keyOf(record)] = true
	return nil
}

// Records returns everything the journal holds — resumed and fresh alike. This
// is the set the report is built from, not the return value of a single Run
// call, which only knows about its own segment.
func (j *RunJournal) Records() []RunRecord { return j.records }

func (j *RunJournal) Close() error { return j.file.Close() }
