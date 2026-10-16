package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/llmusage"
)

// llmUsageSchema is purely additive. llm_usage holds what each model-calling
// job used per UTC day, so a monthly limit holds across restarts and across the
// server and the command line. garden_checks remembers, per knowledge entry,
// when the gardener last looked at it and against which commit of the checkout,
// so that the next run rotates to entries it has not seen.
const llmUsageSchema = `
CREATE TABLE IF NOT EXISTS llm_usage(
  job TEXT NOT NULL,
  day TEXT NOT NULL,
  calls INTEGER NOT NULL DEFAULT 0,
  failures INTEGER NOT NULL DEFAULT 0,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens INTEGER NOT NULL DEFAULT 0,
  cache_write_tokens INTEGER NOT NULL DEFAULT 0,
  cost_micro_usd INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(job, day));
INSERT OR IGNORE INTO llm_usage(job, day, calls, failures, input_tokens, output_tokens)
  SELECT 'explain', day, calls, failures, input_tokens, output_tokens FROM explain_usage;
CREATE TABLE IF NOT EXISTS garden_checks(
  project TEXT NOT NULL,
  knowledge_id INTEGER NOT NULL,
  checked_at TEXT NOT NULL,
  repo_commit TEXT NOT NULL DEFAULT '',
  entry_hash TEXT NOT NULL DEFAULT '',
  verdict TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(project, knowledge_id));
`

func ensureLLMUsage(db *sql.DB) error {
	_, err := db.Exec(llmUsageSchema)
	return err
}

const usageColumns = `calls, failures, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_micro_usd`

func scanUsage(row interface{ Scan(...any) error }, u *llmusage.Usage) error {
	return row.Scan(&u.Calls, &u.Failures, &u.InputTokens, &u.OutputTokens, &u.CacheReadTokens, &u.CacheWriteTokens, &u.CostMicroUSD)
}

// LLMUsageOn is what a job used on one UTC day (zero when nothing ran).
func (s *Store) LLMUsageOn(job, day string) (llmusage.Usage, error) {
	if s.reader != nil {
		return s.reader.LLMUsageOn(job, day)
	}
	u := llmusage.Usage{Day: day}
	err := scanUsage(s.db.QueryRow(`SELECT `+usageColumns+` FROM llm_usage WHERE job = ? AND day = ?`, job, day), &u)
	if errors.Is(err, sql.ErrNoRows) {
		return llmusage.Usage{Day: day}, nil
	}
	return u, err
}

// LLMUsageMonth is the sum of a job over one UTC month, given as "2006-01".
func (s *Store) LLMUsageMonth(job, month string) (llmusage.Usage, error) {
	if s.reader != nil {
		return s.reader.LLMUsageMonth(job, month)
	}
	u := llmusage.Usage{Day: month}
	err := s.db.QueryRow(`SELECT COALESCE(SUM(calls),0), COALESCE(SUM(failures),0), COALESCE(SUM(input_tokens),0),
		COALESCE(SUM(output_tokens),0), COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(cache_write_tokens),0),
		COALESCE(SUM(cost_micro_usd),0) FROM llm_usage WHERE job = ? AND substr(day, 1, 7) = ?`, job, month).
		Scan(&u.Calls, &u.Failures, &u.InputTokens, &u.OutputTokens, &u.CacheReadTokens, &u.CacheWriteTokens, &u.CostMicroUSD)
	return u, err
}

// LLMUsageRecent lists the last days a job ran, newest first.
func (s *Store) LLMUsageRecent(job string, days int) ([]llmusage.Usage, error) {
	if s.reader != nil {
		return s.reader.LLMUsageRecent(job, days)
	}
	rows, err := s.db.Query(`SELECT day, `+usageColumns+` FROM llm_usage WHERE job = ? ORDER BY day DESC LIMIT ?`, job, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []llmusage.Usage
	for rows.Next() {
		var u llmusage.Usage
		if err := rows.Scan(&u.Day, &u.Calls, &u.Failures, &u.InputTokens, &u.OutputTokens, &u.CacheReadTokens, &u.CacheWriteTokens, &u.CostMicroUSD); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// AddLLMUsage adds what a job used to its day.
func (s *Store) AddLLMUsage(job, day string, u llmusage.Usage) error {
	if job == "" || len(day) != 10 {
		return fmt.Errorf("usage needs a job and a day (YYYY-MM-DD)")
	}
	if s.writer != nil {
		return queueWrite(s, []any{job, day, u}, func(d *Store, p []any) error {
			return d.AddLLMUsage(p[0].(string), p[1].(string), p[2].(llmusage.Usage))
		})
	}
	_, err := s.db.Exec(`INSERT INTO llm_usage(job, day, calls, failures, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_micro_usd)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(job, day) DO UPDATE SET calls=calls+excluded.calls, failures=failures+excluded.failures,
		  input_tokens=input_tokens+excluded.input_tokens, output_tokens=output_tokens+excluded.output_tokens,
		  cache_read_tokens=cache_read_tokens+excluded.cache_read_tokens, cache_write_tokens=cache_write_tokens+excluded.cache_write_tokens,
		  cost_micro_usd=cost_micro_usd+excluded.cost_micro_usd`,
		job, day, u.Calls, u.Failures, u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens, u.CostMicroUSD)
	return err
}

// GardenCheck is the last look the gardener took at one knowledge entry.
type GardenCheck struct {
	KnowledgeID int64
	CheckedAt   string
	// Commit is the head of the checkout the entry was judged against.
	Commit string
	// EntryHash identifies the wording that was judged.
	EntryHash string
	Verdict   string
}

// GardenChecks returns the last look at each entry of a project, by entry id.
func (s *Store) GardenChecks(project string) (map[int64]GardenCheck, error) {
	if s.reader != nil {
		return s.reader.GardenChecks(project)
	}
	rows, err := s.db.Query(`SELECT knowledge_id, checked_at, repo_commit, entry_hash, verdict FROM garden_checks WHERE project = ?`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]GardenCheck{}
	for rows.Next() {
		var c GardenCheck
		if err := rows.Scan(&c.KnowledgeID, &c.CheckedAt, &c.Commit, &c.EntryHash, &c.Verdict); err != nil {
			return nil, err
		}
		out[c.KnowledgeID] = c
	}
	return out, rows.Err()
}

// SaveGardenCheck records a look at an entry, replacing the earlier one.
func (s *Store) SaveGardenCheck(project string, c GardenCheck) error {
	if strings.TrimSpace(project) == "" || c.KnowledgeID <= 0 {
		return fmt.Errorf("a garden check needs a project and an entry")
	}
	if c.CheckedAt == "" {
		c.CheckedAt = now()
	}
	if s.writer != nil {
		return queueWrite(s, []any{project, c}, func(d *Store, p []any) error { return d.SaveGardenCheck(p[0].(string), p[1].(GardenCheck)) })
	}
	_, err := s.db.Exec(`INSERT INTO garden_checks(project, knowledge_id, checked_at, repo_commit, entry_hash, verdict) VALUES(?,?,?,?,?,?)
		ON CONFLICT(project, knowledge_id) DO UPDATE SET checked_at=excluded.checked_at, repo_commit=excluded.repo_commit,
		  entry_hash=excluded.entry_hash, verdict=excluded.verdict`,
		project, c.KnowledgeID, c.CheckedAt, c.Commit, c.EntryHash, c.Verdict)
	return err
}

// GardenCheckSummary says how many entries the gardener has looked at, over all
// projects, and when the oldest of those looks was.
func (s *Store) GardenCheckSummary() (n int, oldest string, err error) {
	if s.reader != nil {
		return s.reader.GardenCheckSummary()
	}
	err = s.db.QueryRow(`SELECT COUNT(*), COALESCE(MIN(checked_at), '') FROM garden_checks`).Scan(&n, &oldest)
	return n, oldest, err
}
