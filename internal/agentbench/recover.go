package agentbench

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// transcriptName matches what writeRaw produces: task, arm and repetition are
// in the file name, and the arm is repeated by the directory it sits in.
var transcriptName = regexp.MustCompile(`^(.+)--(.+)--r(\d+)\.jsonl$`)

// RecoverFromTranscripts rebuilds run records from the raw transcripts of a
// campaign whose records were lost.
//
// It rests on the same fact the regrade path rests on: the transcript is the
// evidence and the record is only a judgement about it. Everything a record
// holds is either in the transcript, in the file name, or in the task
// definition — so a lost record can always be made again, at no cost and with
// no new sampling noise, while a lost transcript is gone for good.
//
// Transcripts whose task is not in the given set are skipped rather than
// refused: a task removed from the set after the fact (a broken question, say)
// leaves its transcripts behind, and they must not block the recovery of the
// rest. The caller sees the count and can compare it against the files.
func RecoverFromTranscripts(campaign Campaign, tasks []Task, rawDir string) ([]RunRecord, error) {
	byID := make(map[string]Task, len(tasks))
	for _, task := range tasks {
		byID[task.ID] = task
	}

	var skeletons []RunRecord
	err := filepath.WalkDir(rawDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		match := transcriptName.FindStringSubmatch(entry.Name())
		if match == nil {
			return nil
		}
		// Der Armname steht im Dateinamen und im Verzeichnis. Genommen wird
		// das Verzeichnis: Aufgabenkennungen duerfen Bindestriche enthalten,
		// Verzeichnisse sind eindeutig.
		arm := ArmName(filepath.Base(filepath.Dir(path)))
		taskID := strings.TrimSuffix(match[1], "--"+string(arm))
		task, known := byID[taskID]
		if !known {
			return nil
		}
		repetition, err := strconv.Atoi(match[3])
		if err != nil {
			return err
		}
		skeletons = append(skeletons, RunRecord{
			Campaign: campaign.Name, TaskID: task.ID, Repo: task.Repo, Arm: arm,
			Repetition: repetition, Category: task.Category, Exposure: task.Exposure,
			DevelopmentData: task.DevelopmentData,
			Transcript:      Transcript{RawPath: path},
		})
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading transcripts under %s: %w", rawDir, err)
	}
	return Regrade(skeletons, tasks)
}
