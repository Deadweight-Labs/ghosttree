package agentbench

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type SlotType string

const (
	SlotPath    SlotType = "path"
	SlotInteger SlotType = "integer"
	SlotBoolean SlotType = "boolean"
	SlotString  SlotType = "string"
)

type Category string

const (
	CategoryLocalization Category = "localization"
	CategoryCrossCutting Category = "cross_cutting"
	CategoryRationale    Category = "rationale"
	CategoryLedgerState  Category = "ledger_state"
	CategoryMutation     Category = "mutation"
	CategoryNegative     Category = "negative_control"
)

type Exposure struct {
	ExactPromptSeen bool `json:"exact_prompt_seen_in_memory_corpus"`
	EquivalentSeen  bool `json:"semantically_equivalent_question_seen"`
	AnswerFactSeen  bool `json:"answer_fact_seen"`
}

type FactSlot struct {
	ID           string   `json:"id"`
	Type         SlotType `json:"type"`
	Weight       int      `json:"weight"`
	Accepted     []string `json:"accepted,omitempty"`
	ExpectedInt  *int     `json:"expected_int,omitempty"`
	ExpectedBool *bool    `json:"expected_bool,omitempty"`
	Contradicts  []string `json:"contradicts,omitempty"`
	// OnlyInMemory marks a fact that cannot be established from the repository
	// — not from the code, not from the git history. Whether a piece of work is
	// still open in the ledger is such a fact.
	//
	// These two kinds of fact answer two different questions and must not be
	// added up. On a fact that is in the repository, every arm can in principle
	// find it, and a difference measures how well a memory guides the search.
	// On a fact that is only in the memory, an arm without it scores zero by
	// construction, and the difference measures something else entirely: what
	// it is worth to have written the thing down at all. A headline number that
	// mixes both is unreadable, and a critic is right to say so.
	OnlyInMemory bool `json:"only_in_memory,omitempty"`
}

type Task struct {
	ID       string     `json:"id"`
	Repo     string     `json:"repo"`
	Commit   string     `json:"commit"`
	Prompt   string     `json:"prompt"`
	Category Category   `json:"category"`
	Exposure Exposure   `json:"exposure"`
	Facts    []FactSlot `json:"facts"`
}

func (t Task) Validate() error {
	if t.ID == "" || t.Repo == "" || t.Commit == "" || t.Prompt == "" {
		return fmt.Errorf("task %q: id, repo, commit and prompt are required", t.ID)
	}
	if len(t.Facts) == 0 {
		return fmt.Errorf("task %q: at least one fact slot is required", t.ID)
	}
	seen := make(map[string]bool, len(t.Facts))
	for _, slot := range t.Facts {
		if slot.ID == "" {
			return fmt.Errorf("task %q: a fact slot is missing its id", t.ID)
		}
		if seen[slot.ID] {
			return fmt.Errorf("task %q: duplicate fact slot %q", t.ID, slot.ID)
		}
		seen[slot.ID] = true
		if slot.Weight <= 0 {
			return fmt.Errorf("task %q slot %q: weight must be positive", t.ID, slot.ID)
		}
		if err := slot.validateExpectation(t.ID, t.Category); err != nil {
			return err
		}
	}
	return nil
}

func (s FactSlot) validateExpectation(taskID string, category Category) error {
	// Eine Negativkontrolle hat per Definition keine richtige Antwort: gefragt
	// wird nach etwas, das es nicht gibt, und gemessen wird, ob der Agent sich
	// enthaelt statt zu erfinden. Sie braucht deshalb keine akzeptierten Werte,
	// wohl aber die plausiblen Erfindungen — ohne sie zaehlte eine erfundene
	// Antwort nur als falsch statt als Widerspruch, und die Kategorie koennte
	// gar nichts messen.
	if category == CategoryNegative {
		if len(s.Accepted) > 0 {
			return fmt.Errorf("task %q slot %q: a negative control must not have accepted values", taskID, s.ID)
		}
		if len(s.Contradicts) == 0 {
			return fmt.Errorf("task %q slot %q: a negative control needs the plausible inventions in contradicts", taskID, s.ID)
		}
		return nil
	}
	switch s.Type {
	case SlotPath, SlotString:
		if len(s.Accepted) == 0 {
			return fmt.Errorf("task %q slot %q: accepted values are required", taskID, s.ID)
		}
	case SlotInteger:
		if s.ExpectedInt == nil {
			return fmt.Errorf("task %q slot %q: expected_int is required", taskID, s.ID)
		}
	case SlotBoolean:
		if s.ExpectedBool == nil {
			return fmt.Errorf("task %q slot %q: expected_bool is required", taskID, s.ID)
		}
	default:
		return fmt.Errorf("task %q slot %q: unknown slot type %q", taskID, s.ID, s.Type)
	}
	return nil
}

func LoadTasks(dir string) ([]Task, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var tasks []Task
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var task Task
		if err := json.Unmarshal(raw, &task); err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name(), err)
		}
		if err := task.Validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name(), err)
		}
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	return tasks, nil
}
