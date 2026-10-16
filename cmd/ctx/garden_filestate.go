package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Deadweight-Labs/ghosttree/internal/llmusage"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// fileState is the gardener's memory (which entries it looked at, what each job
// used per day) in one JSON file of its own instead of the ghosttree database.
// The sandboxed unit of the gardener cannot reach the database: it holds every
// project's knowledge, and the session the gardener starts runs in the same
// sandbox.
type fileState struct {
	path string
	mu   sync.Mutex
	d    fileStateData
}

type fileStateData struct {
	Checks map[string]map[int64]store.GardenCheck `json:"checks"`
	Usage  map[string]map[string]llmusage.Usage   `json:"usage"`
}

func openFileState(path string) (*fileState, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("state file %q is not absolute", path)
	}
	f := &fileState{path: path}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(b, &f.d); err != nil {
			return nil, fmt.Errorf("state file %s: %w", path, err)
		}
	}
	if f.d.Checks == nil {
		f.d.Checks = map[string]map[int64]store.GardenCheck{}
	}
	if f.d.Usage == nil {
		f.d.Usage = map[string]map[string]llmusage.Usage{}
	}
	// A state file that cannot be written would run unmetered, so it is tried now.
	if err := f.save(); err != nil {
		return nil, err
	}
	return f, nil
}

func (f *fileState) save() error {
	b, err := json.MarshalIndent(f.d, "", " ")
	if err != nil {
		return err
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}

func (f *fileState) GardenChecks(project string) (map[int64]store.GardenCheck, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[int64]store.GardenCheck{}
	for id, c := range f.d.Checks[project] {
		out[id] = c
	}
	return out, nil
}

func (f *fileState) SaveGardenCheck(project string, c store.GardenCheck) error {
	if strings.TrimSpace(project) == "" || c.KnowledgeID <= 0 {
		return fmt.Errorf("a garden check needs a project and an entry")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.d.Checks[project] == nil {
		f.d.Checks[project] = map[int64]store.GardenCheck{}
	}
	f.d.Checks[project][c.KnowledgeID] = c
	return f.save()
}

func (f *fileState) LLMUsageOn(job, day string) (llmusage.Usage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.d.Usage[job][day]
	if !ok {
		return llmusage.Usage{Day: day}, nil
	}
	return u, nil
}

func (f *fileState) LLMUsageMonth(job, month string) (llmusage.Usage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sum := llmusage.Usage{Day: month}
	for day, u := range f.d.Usage[job] {
		if strings.HasPrefix(day, month) {
			sum.Calls += u.Calls
			sum.Failures += u.Failures
			sum.InputTokens += u.InputTokens
			sum.OutputTokens += u.OutputTokens
			sum.CacheReadTokens += u.CacheReadTokens
			sum.CacheWriteTokens += u.CacheWriteTokens
			sum.CostMicroUSD += u.CostMicroUSD
		}
	}
	return sum, nil
}

func (f *fileState) AddLLMUsage(job, day string, u llmusage.Usage) error {
	if job == "" || len(day) != 10 {
		return fmt.Errorf("usage needs a job and a day (YYYY-MM-DD)")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.d.Usage[job] == nil {
		f.d.Usage[job] = map[string]llmusage.Usage{}
	}
	t := f.d.Usage[job][day]
	t.Day = day
	t.Calls += u.Calls
	t.Failures += u.Failures
	t.InputTokens += u.InputTokens
	t.OutputTokens += u.OutputTokens
	t.CacheReadTokens += u.CacheReadTokens
	t.CacheWriteTokens += u.CacheWriteTokens
	t.CostMicroUSD += u.CostMicroUSD
	f.d.Usage[job][day] = t
	return f.save()
}
