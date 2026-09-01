package agentbench

import (
	"fmt"
	"sort"
	"strings"
)

// TaskSuspicion flags a task whose ground truth is more likely wrong than the
// agents are.
type TaskSuspicion struct {
	TaskID string  `json:"task_id"`
	Runs   int     `json:"runs"`
	Arms   int     `json:"arms"`
	Recall float64 `json:"recall"`
	Reason string  `json:"reason"`
}

// SuspectTasks finds tasks that every arm failed in the same way.
//
// The rule comes from a real case in the first pilot: asked how many DNS
// provider implementations the repository has, all three arms answered "one"
// against a ground truth of "two". The arms were right — the second directory
// is a dry-run wrapper around a provider, not a provider — and the task author
// had counted directories instead of reading them.
//
// Unanimity across arms that otherwise disagree is evidence about the task, not
// about the arms. A memory system cannot make three differently-equipped agents
// agree on the same wrong answer; a badly worded question can. Such a task is
// therefore reported and excluded from the headline numbers rather than counted
// as a shared failure — because counted, it silently pushes every arm down by
// the same amount and dilutes every contrast toward zero.
func SuspectTasks(records []RunRecord, arms []ArmName) []TaskSuspicion {
	type agg struct {
		runs     int
		recall   float64
		arms     map[ArmName]bool
		negative bool
	}
	byTask := map[string]*agg{}
	for _, record := range records {
		if record.Failure != FailureNone {
			continue
		}
		a := byTask[record.TaskID]
		if a == nil {
			a = &agg{arms: map[ArmName]bool{}}
			byTask[record.TaskID] = a
		}
		a.runs++
		a.recall += record.Score.FactRecall
		a.arms[record.Arm] = true
		a.negative = record.Category == CategoryNegative
	}

	out := convergedRejections(records, arms)
	for id, a := range byTask {
		// Nur wenn wirklich jeder Arm angetreten ist: fehlt einer, ist die
		// Einstimmigkeit kein Argument.
		if len(a.arms) < len(arms) || len(arms) < 2 {
			continue
		}
		// Auf einer Negativkontrolle ist die einstimmige Null das Ziel: die
		// Frage hat keine Antwort, und jeder Arm soll sich enthalten. Sie hier
		// zu melden hiesse, den Erfolg des Versuchsaufbaus als Fehler zu
		// drucken.
		if a.negative {
			continue
		}
		mean := a.recall / float64(a.runs)
		if mean > 0 {
			continue
		}
		out = append(out, TaskSuspicion{
			TaskID: id, Runs: a.runs, Arms: len(a.arms), Recall: mean,
			Reason: "every arm scored zero; check the ground truth before the arms",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TaskID < out[j].TaskID })
	return out
}

// convergedRejections finds tasks where differently equipped arms independently
// gave the same answer the key rejects.
//
// It catches the case unanimity misses. Asked which function keeps two hosts'
// adopted proxy configuration from colliding in the central store, the bare arm
// named AdoptedArtifactID — the expected answer — while the other two both
// named GetConfigArtifactByTarget and pointed at a UNIQUE(agent_id, target_kind,
// target_path) constraint. That is a second, equally true answer to a question
// that admitted only one. Scored as written it reads as "memory made the agent
// worse", which is not what happened.
//
// Two arms are enough. They differ in what they know and search separately; the
// same rejected string from both is far more likely to be a real answer the key
// does not list than the same mistake made twice.
func convergedRejections(records []RunRecord, arms []ArmName) []TaskSuspicion {
	if len(arms) < 2 {
		return nil
	}
	type claim struct{ task, slot, value string }
	sayers := map[claim]map[ArmName]bool{}
	// Gruppiert wird kleingeschrieben, berichtet wird, wie es dastand: die
	// Meldung soll zitieren, was der Agent gesagt hat.
	spelling := map[claim]string{}
	runsPerTask := map[string]int{}
	for _, record := range records {
		if record.Failure != FailureNone {
			continue
		}
		runsPerTask[record.TaskID]++
		for _, rejected := range record.Score.Rejected {
			// Nur Antworten aus einem grossen Wertevorrat. Zwei Arme, die
			// denselben Dateipfad nennen, haben etwas gefunden; zwei Arme, die
			// "false" sagen, haben sich per Muenzwurf getroffen — bei einem
			// Boolean liegt genau das in der Haelfte aller Faelle vor.
			if rejected.Type != SlotString && rejected.Type != SlotPath {
				continue
			}
			said := strings.TrimSpace(rejected.Value)
			key := claim{record.TaskID, rejected.Slot, strings.ToLower(said)}
			if sayers[key] == nil {
				sayers[key] = map[ArmName]bool{}
				spelling[key] = said
			}
			sayers[key][record.Arm] = true
		}
	}

	// Ueber eine Map zu laufen waere hier nicht harmlos: bei mehreren
	// uebereinstimmenden Antworten zur selben Aufgabe entschiede der Zufall,
	// welche im Bericht steht, und zwei Auswertungen desselben Laufs
	// widersprechen sich.
	keys := make([]claim, 0, len(sayers))
	for key := range sayers {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].task != keys[j].task {
			return keys[i].task < keys[j].task
		}
		if keys[i].slot != keys[j].slot {
			return keys[i].slot < keys[j].slot
		}
		return keys[i].value < keys[j].value
	})

	seen := map[string]bool{}
	var out []TaskSuspicion
	for _, key := range keys {
		byArm := sayers[key]
		if len(byArm) < 2 || key.value == "" || seen[key.task] {
			continue
		}
		seen[key.task] = true
		out = append(out, TaskSuspicion{
			TaskID: key.task, Runs: runsPerTask[key.task], Arms: len(byArm),
			Reason: fmt.Sprintf("%d arms independently answered %q for slot %q and the key rejects it; "+
				"check whether the question has a second true answer", len(byArm), spelling[key], key.slot),
		})
	}
	return out
}
