package agentbench

import (
	"context"
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
	Campaign   string     `json:"campaign"`
	TaskID     string     `json:"task_id"`
	Repo       string     `json:"repo"`
	Arm        ArmName    `json:"arm"`
	Repetition int        `json:"repetition"`
	Category   Category   `json:"category"`
	Exposure   Exposure   `json:"exposure"`
	Score      Score      `json:"score"`
	Transcript Transcript `json:"transcript"`
	Failure    Failure    `json:"failure,omitempty"`
	FailureMsg string     `json:"failure_message,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
}

func blockOrder(seed uint64, taskIndex, repetition int, arms []ArmName) []ArmName {
	rng := rand.New(rand.NewPCG(seed, uint64(taskIndex)<<32|uint64(repetition)))
	order := make([]ArmName, len(arms))
	copy(order, arms)
	rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
	return order
}

func Run(ctx context.Context, campaign Campaign, tasks []Task, agent Agent) ([]RunRecord, error) {
	if err := campaign.Validate(tasks); err != nil {
		return nil, err
	}
	var records []RunRecord
	for repetition := 1; repetition <= campaign.Repetitions; repetition++ {
		for taskIndex, task := range tasks {
			for _, arm := range blockOrder(campaign.Seed, taskIndex, repetition, campaign.Arms) {
				records = append(records, runOne(ctx, campaign, task, arm, repetition, agent))
			}
		}
	}
	return records, nil
}

func runOne(ctx context.Context, campaign Campaign, task Task, arm ArmName, repetition int, agent Agent) RunRecord {
	record := RunRecord{
		Campaign: campaign.Name, TaskID: task.ID, Repo: task.Repo, Arm: arm,
		Repetition: repetition, Category: task.Category, Exposure: task.Exposure,
	}
	transcript, err := agent.Run(ctx, Invocation{Task: task, Arm: arm, Config: campaign.Agent})
	if err != nil {
		record.Failure = FailureProduct
		record.FailureMsg = err.Error()
		return record
	}
	record.Transcript = transcript
	form, err := ParseForm(transcript.Output)
	if err != nil {
		record.Failure = FailureScoring
		record.FailureMsg = err.Error()
		return record
	}
	record.Score = Grade(task, form)
	return record
}
