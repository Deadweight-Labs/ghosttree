package storebench

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
)

type runJob struct {
	index   int
	readyAt time.Time
}

type runCompletion struct {
	job      runJob
	started  time.Time
	finished time.Time
	err      error
}

type durationSamples struct {
	queue     []int64
	execution []int64
	total     []int64
	errors    int
}

func Run(ctx context.Context, backend Backend, workload Workload, expected Expected, config RunConfig) (Report, error) {
	if backend == nil {
		return Report{}, fmt.Errorf("backend is required")
	}
	if config.Concurrency <= 0 {
		return Report{}, fmt.Errorf("concurrency must be positive")
	}
	if config.ArrivalMultiplier <= 0 || math.IsNaN(config.ArrivalMultiplier) || math.IsInf(config.ArrivalMultiplier, 0) {
		return Report{}, fmt.Errorf("arrival multiplier must be finite and positive")
	}
	graph, err := buildRunGraph(workload)
	if err != nil {
		return Report{}, err
	}
	startedAt := time.Now()
	ready := &readyQueue{}
	heap.Init(ready)
	readyAfter := make([]time.Time, len(workload.Operations))
	for index, operation := range workload.Operations {
		readyAfter[index] = startedAt.Add(scaleArrival(operation.At, config.ArrivalMultiplier))
		if graph.indegree[index] == 0 {
			heap.Push(ready, readyEntry{index: index, at: readyAfter[index]})
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan runJob, config.Concurrency)
	completions := make(chan runCompletion, config.Concurrency)
	var workers sync.WaitGroup
	for range config.Concurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-runCtx.Done():
					return
				case job, ok := <-jobs:
					if !ok {
						return
					}
					begin := time.Now()
					executeErr := backend.Execute(runCtx, workload.Operations[job.index])
					completion := runCompletion{job: job, started: begin, finished: time.Now(), err: executeErr}
					select {
					case completions <- completion:
					case <-runCtx.Done():
						return
					}
				}
			}
		}()
	}

	samples := map[Kind]*durationSamples{}
	completed, inFlight, operationErrors := 0, 0, 0
	var firstOperationError error
	for completed < len(workload.Operations) {
		now := time.Now()
		for inFlight < config.Concurrency && ready.Len() > 0 && !(*ready)[0].at.After(now) {
			entry := heap.Pop(ready).(readyEntry)
			jobs <- runJob{index: entry.index, readyAt: entry.at}
			inFlight++
		}
		if completed == len(workload.Operations) {
			break
		}
		if inFlight == 0 && ready.Len() == 0 {
			cancel()
			close(jobs)
			workers.Wait()
			return Report{}, fmt.Errorf("workload dependency graph contains a cycle")
		}

		var timer <-chan time.Time
		var clock *time.Timer
		if inFlight < config.Concurrency && ready.Len() > 0 {
			delay := time.Until((*ready)[0].at)
			if delay < 0 {
				delay = 0
			}
			clock = time.NewTimer(delay)
			timer = clock.C
		}
		select {
		case completion := <-completions:
			if clock != nil {
				clock.Stop()
			}
			inFlight--
			completed++
			operation := workload.Operations[completion.job.index]
			kindSamples := samples[operation.Kind]
			if kindSamples == nil {
				kindSamples = &durationSamples{}
				samples[operation.Kind] = kindSamples
			}
			kindSamples.queue = append(kindSamples.queue, nonNegative(completion.started.Sub(completion.job.readyAt).Nanoseconds()))
			kindSamples.execution = append(kindSamples.execution, nonNegative(completion.finished.Sub(completion.started).Nanoseconds()))
			kindSamples.total = append(kindSamples.total, nonNegative(completion.finished.Sub(completion.job.readyAt).Nanoseconds()))
			if completion.err != nil {
				kindSamples.errors++
				operationErrors++
				if firstOperationError == nil {
					firstOperationError = fmt.Errorf("%s %s: %w", operation.ID, operation.Kind, completion.err)
				}
			}
			for _, dependent := range graph.dependents[completion.job.index] {
				graph.indegree[dependent]--
				if completion.finished.After(readyAfter[dependent]) {
					readyAfter[dependent] = completion.finished
				}
				if graph.indegree[dependent] == 0 {
					heap.Push(ready, readyEntry{index: dependent, at: readyAfter[dependent]})
				}
			}
		case <-timer:
		case <-ctx.Done():
			if clock != nil {
				clock.Stop()
			}
			cancel()
			close(jobs)
			workers.Wait()
			return Report{}, ctx.Err()
		}
	}
	close(jobs)
	workers.Wait()
	finishedAt := time.Now()
	report := makeReport(backend, workload, config, samples, operationErrors, startedAt, finishedAt)
	verifyErr := backend.Verify(ctx, expected)
	if verifyErr != nil {
		report.VerificationError = verifyErr.Error()
	}
	return report, errors.Join(firstOperationError, verifyErr)
}

type runGraph struct {
	indegree   []int
	dependents [][]int
}

func buildRunGraph(workload Workload) (runGraph, error) {
	indexByID := make(map[string]int, len(workload.Operations))
	for index, operation := range workload.Operations {
		if operation.ID == "" {
			return runGraph{}, fmt.Errorf("operation %d has no id", index)
		}
		if operation.At < 0 {
			return runGraph{}, fmt.Errorf("operation %s has negative arrival time", operation.ID)
		}
		if _, exists := indexByID[operation.ID]; exists {
			return runGraph{}, fmt.Errorf("duplicate operation id %q", operation.ID)
		}
		indexByID[operation.ID] = index
	}
	graph := runGraph{indegree: make([]int, len(workload.Operations)), dependents: make([][]int, len(workload.Operations))}
	for index, operation := range workload.Operations {
		for _, dependency := range operation.DependsOn {
			parent, exists := indexByID[dependency]
			if !exists {
				return runGraph{}, fmt.Errorf("operation %s depends on missing %s", operation.ID, dependency)
			}
			graph.indegree[index]++
			graph.dependents[parent] = append(graph.dependents[parent], index)
		}
	}
	return graph, nil
}

func makeReport(backend Backend, workload Workload, config RunConfig, samples map[Kind]*durationSamples, operationErrors int, started, finished time.Time) Report {
	kinds := make(map[Kind]KindReport, len(samples))
	for kind, values := range samples {
		kinds[kind] = KindReport{Operations: len(values.total), Errors: values.errors,
			SchedulerWait: summarizeDurations(values.queue), Execution: summarizeDurations(values.execution), Total: summarizeDurations(values.total)}
	}
	duration := finished.Sub(started)
	throughput := 0.0
	if duration > 0 {
		throughput = float64(len(workload.Operations)) / duration.Seconds()
	}
	return Report{RunID: config.RunID, Backend: backend.Name(), Seed: workload.Seed, Scale: workload.Scale,
		Config: config, StartedAt: started.UTC().Format(time.RFC3339Nano), FinishedAt: finished.UTC().Format(time.RFC3339Nano),
		DurationNS: duration.Nanoseconds(), Throughput: throughput, Operations: len(workload.Operations), Errors: operationErrors,
		Kinds: kinds, BackendStats: backend.Stats()}
}

func summarizeValues(samples []int64) ValueSummary {
	if len(samples) == 0 {
		return ValueSummary{}
	}
	values := append([]int64(nil), samples...)
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return ValueSummary{Count: len(values), P50: nearestRank(values, 0.50), P95: nearestRank(values, 0.95),
		P99: nearestRank(values, 0.99), Max: values[len(values)-1]}
}

func summarizeDurations(samples []int64) DurationSummary {
	if len(samples) == 0 {
		return DurationSummary{}
	}
	values := append([]int64(nil), samples...)
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return DurationSummary{Count: len(values), P50NS: nearestRank(values, 0.50), P95NS: nearestRank(values, 0.95),
		P99NS: nearestRank(values, 0.99), MaxNS: values[len(values)-1]}
}

func nearestRank(values []int64, percentile float64) int64 {
	index := int(math.Ceil(percentile*float64(len(values)))) - 1
	if index < 0 {
		index = 0
	}
	return values[index]
}

func scaleArrival(value time.Duration, multiplier float64) time.Duration {
	return time.Duration(float64(value) / multiplier)
}

func nonNegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

type readyEntry struct {
	index int
	at    time.Time
}

type readyQueue []readyEntry

func (q readyQueue) Len() int { return len(q) }
func (q readyQueue) Less(i, j int) bool {
	if q[i].at.Equal(q[j].at) {
		return q[i].index < q[j].index
	}
	return q[i].at.Before(q[j].at)
}
func (q readyQueue) Swap(i, j int)   { q[i], q[j] = q[j], q[i] }
func (q *readyQueue) Push(value any) { *q = append(*q, value.(readyEntry)) }
func (q *readyQueue) Pop() any {
	old := *q
	last := old[len(old)-1]
	*q = old[:len(old)-1]
	return last
}
