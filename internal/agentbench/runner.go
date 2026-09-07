package agentbench

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"
)

type Failure string

const (
	FailureNone           Failure = ""
	FailureTransport      Failure = "transport"
	FailureProduct        Failure = "product"
	FailureInfrastructure Failure = "infrastructure"
	FailureScoring        Failure = "scoring"
)

type RunRecord struct {
	Campaign   string   `json:"campaign"`
	TaskID     string   `json:"task_id"`
	Repo       string   `json:"repo"`
	Arm        ArmName  `json:"arm"`
	Repetition int      `json:"repetition"`
	Category   Category `json:"category"`
	Exposure   Exposure `json:"exposure"`
	// DevelopmentData traegt die Aufgabe in den Datensatz, damit der Bericht
	// entwickelte von zurueckgehaltenen Aufgaben trennen kann, ohne die
	// Aufgabendefinitionen noch einmal lesen zu muessen.
	DevelopmentData bool       `json:"development_data,omitempty"`
	Score           Score      `json:"score"`
	Transcript      Transcript `json:"transcript"`
	Failure         Failure    `json:"failure,omitempty"`
	FailureMsg      string     `json:"failure_message,omitempty"`
	StartedAt       time.Time  `json:"started_at"`
}

func blockOrder(seed uint64, taskIndex, repetition int, arms []ArmName) []ArmName {
	rng := rand.New(rand.NewPCG(seed, uint64(taskIndex)<<32|uint64(repetition)))
	order := make([]ArmName, len(arms))
	copy(order, arms)
	rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
	return order
}

// AgentFor hands out the agent belonging to one arm. Each arm has its own
// workspace, its own HOME and its own tool allowlist, so one shared agent
// would quietly run every arm inside the first arm's environment.
type AgentFor interface {
	For(arm ArmName) (Agent, error)
}

// AfterRunChecker is checked after every run when the AgentFor implements it.
//
// A campaign hands each arm one workspace and one HOME for all of its runs, so
// anything an agent leaves behind is visible to the arm's next run. In 72 pilot
// runs no agent wrote a memory — the directories were created and stayed empty
// — but nothing prevented it. An arm that quietly accumulates notes across
// tasks stops being the arm it is named after, and by the time the numbers look
// odd the campaign is spent.
type AfterRunChecker interface {
	AfterRun(arm ArmName) error
}

type AgentForFunc func(arm ArmName) (Agent, error)

func (f AgentForFunc) For(arm ArmName) (Agent, error) { return f(arm) }

// SameAgent adapts a single agent to every arm. Only correct where the arms
// share an environment by construction, as in tests.
func SameAgent(agent Agent) AgentFor {
	return AgentForFunc(func(ArmName) (Agent, error) { return agent, nil })
}

// startupFailureLimit is how many opening runs may fail before the campaign
// gives up. An expired credential, a broken image or an unreachable model
// fails every run alike; without this a campaign spends its whole wall clock
// producing the same error several hundred times.
const startupFailureLimit = 3

// startupGuard watches only the opening runs. Once anything has succeeded the
// campaign is viable and later failures are data, not a reason to stop.
type startupGuard struct {
	succeeded bool
	failures  int
	lastMsg   string
}

func (g *startupGuard) observe(record RunRecord) error {
	if record.Failure == FailureNone {
		g.succeeded = true
		return nil
	}
	if g.succeeded {
		return nil
	}
	g.failures++
	g.lastMsg = record.FailureMsg
	if g.failures >= startupFailureLimit {
		return fmt.Errorf("the first %d runs all failed and none succeeded; last error: %s",
			g.failures, g.lastMsg)
	}
	return nil
}

// RunLedger takes each record the moment it exists and says which runs are
// already made. A campaign that keeps its records only in memory loses every
// judgement to a crash, and a campaign that cannot skip what it already did
// cannot be resumed at all.
type RunLedger interface {
	Emit(RunRecord) error
	Done(taskID string, arm ArmName, repetition int) bool
}

// Run executes the plan and returns the records this call produced. On a
// resumed campaign that is the new segment only; the full set lives in the
// ledger.
//
// A nil ledger is allowed and means the records exist only in memory — right
// for a test, never for a campaign that costs money.
func Run(ctx context.Context, campaign Campaign, tasks []Task, agents AgentFor, ledger RunLedger) ([]RunRecord, error) {
	if err := campaign.Validate(tasks); err != nil {
		return nil, err
	}
	var records []RunRecord
	var guard startupGuard
	for repetition := 1; repetition <= campaign.Repetitions; repetition++ {
		for taskIndex, task := range tasks {
			for _, arm := range blockOrder(campaign.Seed, taskIndex, repetition, campaign.Arms) {
				if ledger != nil && ledger.Done(task.ID, arm, repetition) {
					continue
				}
				record := runOne(ctx, campaign, task, arm, repetition, agents)
				records = append(records, record)
				if ledger != nil {
					// Ein Datensatz, der nicht auf die Platte kommt, ist
					// verlorene Rechenzeit: die Kampagne bricht ab, statt
					// weiter Geld fuer Ergebnisse auszugeben, die niemand
					// mehr lesen kann.
					if err := ledger.Emit(record); err != nil {
						return records, fmt.Errorf("recording the run for %q/%s: %w", task.ID, arm, err)
					}
				}
				if err := guard.observe(record); err != nil {
					return records, err
				}
				// Eine Verunreinigung ist kein Ergebnis eines Laufs, sondern
				// das Ende der Gueltigkeit dieses Arms: jeder folgende Lauf
				// saehe, was der Agent hinterlassen hat. Also abbrechen —
				// fortsetzen kann die Kampagne danach, das Journal steht.
				if checker, ok := agents.(AfterRunChecker); ok {
					if err := checker.AfterRun(arm); err != nil {
						return records, fmt.Errorf("after the run for %q/%s: %w", task.ID, arm, err)
					}
				}
			}
		}
	}
	return records, nil
}

func runOne(ctx context.Context, campaign Campaign, task Task, arm ArmName, repetition int, agents AgentFor) RunRecord {
	record := RunRecord{
		Campaign: campaign.Name, TaskID: task.ID, Repo: task.Repo, Arm: arm,
		Repetition: repetition, Category: task.Category, Exposure: task.Exposure,
		DevelopmentData: task.DevelopmentData,
	}
	agent, err := agents.For(arm)
	if err != nil {
		record.Failure = FailureInfrastructure
		record.FailureMsg = err.Error()
		return record
	}
	transcript, err := agent.Run(ctx, Invocation{Task: task, Arm: arm, Config: campaign.Agent, Repetition: repetition})
	record.Transcript = transcript
	if err != nil {
		record.Failure = FailureProduct
		record.FailureMsg = err.Error()
		return record
	}
	if transcript.AgentError != "" {
		record.Failure = FailureProduct
		record.FailureMsg = transcript.AgentError
		return record
	}
	form, err := FormFrom(transcript)
	if err != nil {
		// Wer sein Zugbudget verbraucht und nichts liefert, hat sich der
		// Antwort enthalten. Das ist das Ergebnis des Laufs und gehoert
		// gewertet — es trifft den Arm, der die Antwort nicht hat, und ihn aus
		// der Wertung zu nehmen hiesse, ihm sein schlechtestes Ergebnis zu
		// erlassen.
		if transcript.MaxTurnsExceeded {
			record.Score = Grade(task, ResponseForm{Slots: map[string]SlotValue{}})
			return record
		}
		record.Failure = FailureScoring
		record.FailureMsg = err.Error()
		return record
	}
	record.Score = Grade(task, form)
	return record
}
