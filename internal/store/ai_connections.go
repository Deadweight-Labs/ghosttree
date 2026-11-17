package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
)

// AI connections, stage E1 of the workflows (REQ-694, IDEA-45, DEC-43 and
// DEC-47). A connection is a named way to a model provider: format, address,
// key, the models it may serve with their prices, and a monthly cap. They
// exist on three levels: the server (org_id 0, kept by the instance admin), an
// organization and a personal namespace (both an org row). The key is sealed
// by the caller (internal/secretbox) before it reaches the store and is only
// ever read back by the model proxy; nothing here returns it.
//
// A run never sees a key. It gets a token that is bound to the run and only
// valid while the run is running; the proxy resolves the model the run asks
// for (alias of the repository, then of the owner, then of the server), checks
// that the owner may use the connection, applies the caps and books every
// call into the ledger (ai_calls).

const (
	AIFormatOpenAI    = "openai"
	AIFormatAnthropic = "anthropic"

	AIReleaseAll      = "all"
	AIReleaseSelected = "selected"
	AIReleaseNone     = "none"

	AILevelServer = "server"
	AILevelOwner  = "owner"
	AILevelRepo   = "repo"

	// How an owner reaches a connection.
	AIViaOwn    = "own"
	AIViaServer = "server"
	AIViaLoan   = "loan"

	// The caps a run can reach; the name is the reason of a run that ended in
	// the state budget.
	AICapRun        = "cap.run"
	AICapWorkflow   = "cap.workflow"
	AICapOwner      = "cap.owner"
	AICapConnection = "cap.connection"
	AICapServer     = "cap.server"
	AICapLoan       = "cap.loan"

	// AIKeyPurpose is what a sealed key is bound to (secretbox purpose).
	AIKeyPurpose = "ghosttree/ai-connection-key"
	// AIRunTokenPrefix marks a run token, so that a leaked one is recognisable.
	AIRunTokenPrefix = "gtr_"

	aiMaxModels  = 20
	aiMaxPrice   = 10000
	aiMaxCap     = 1000000
	aiMaxPerKind = 50
)

// DefaultAIAliases are the aliases the server gets on the first import of
// llm.json, all on its model.
var DefaultAIAliases = []string{"klein", "mittel", "gründlich"}

var (
	ErrAIConnection = fmt.Errorf("%w: invalid AI connection", ErrInvalidInput)
	ErrAIAlias      = fmt.Errorf("%w: invalid model alias", ErrInvalidInput)
	ErrAICap        = fmt.Errorf("%w: invalid cap", ErrInvalidInput)
	ErrAILoan       = fmt.Errorf("%w: invalid loan", ErrInvalidInput)
	// ErrAINameTaken: the owner has a connection of that name already.
	ErrAINameTaken = errors.New("a connection of that name exists already")
	// ErrAIFromFile: the connection comes from llm.json; its address and key
	// are changed there.
	ErrAIFromFile = errors.New("this connection comes from llm.json")
	// ErrAIKeyMissing: a new connection needs a key.
	ErrAIKeyMissing = fmt.Errorf("%w: a new connection needs a key", ErrInvalidInput)
	// ErrNotServerAdmin: only the instance admin keeps the server's connections.
	ErrNotServerAdmin = errors.New("only the server's admin may do that")
	// ErrAIUnavailable: the owner may not use that connection (not its own,
	// not lent to it, not released to it) or the model is not allowed on it.
	ErrAIUnavailable = errors.New("no such model for this owner")
	// ErrAIToken: the token is unknown or its run is no longer running.
	ErrAIToken = errors.New("run token is not valid")
)

// AIBudgetError says a cap stops the call; Cap is one of the AICap names.
type AIBudgetError struct{ Cap string }

func (e *AIBudgetError) Error() string { return "budget reached: " + e.Cap }

// AIModel is a model a connection may serve, with its price in USD per
// million tokens.
type AIModel struct {
	Model  string  `json:"model"`
	InUSD  float64 `json:"in_usd_per_mtok"`
	OutUSD float64 `json:"out_usd_per_mtok"`
}

// AIConnection is a connection as an owner or the admin sees it. It never
// carries the key, only its last four characters.
type AIConnection struct {
	ID            int64     `json:"id"`
	OrgID         int64     `json:"org_id"`
	Name          string    `json:"name"`
	Format        string    `json:"format,omitempty"`
	BaseURL       string    `json:"base_url,omitempty"`
	KeyLast4      string    `json:"key_last4,omitempty"`
	FromFile      bool      `json:"from_file,omitempty"`
	MonthlyCapUSD float64   `json:"monthly_cap_usd,omitempty"`
	Models        []AIModel `json:"models,omitempty"`
	CreatedAt     string    `json:"created_at,omitempty"`
	// Seen from a namespace: how it reaches the connection, the loan and its
	// cap, and what the namespace spent on it this month.
	Via        string  `json:"via,omitempty"`
	LoanID     int64   `json:"loan_id,omitempty"`
	LoanCapUSD float64 `json:"loan_cap_usd,omitempty"`
	SpentMonth float64 `json:"spent_month_usd"`
}

// AIConnectionInput creates (ID 0) or changes a connection. KeySealed is
// required for a new one; on a change nil keeps the stored key.
type AIConnectionInput struct {
	ID            int64
	OrgID         int64
	Name          string
	Format        string
	BaseURL       string
	KeySealed     []byte
	KeyLast4      string
	MonthlyCapUSD float64
	Models        []AIModel
}

// AIFileConnection is the server connection read from llm.json at start.
type AIFileConnection struct {
	Name, Format, BaseURL, Model string
	InUSD, OutUSD                float64
	KeyLast4                     string
}

// AIGrant releases the server's connections to one namespace with a monthly
// cap of its own (0: none beyond the connection's).
type AIGrant struct {
	OrgID      int64   `json:"org_id"`
	Slug       string  `json:"slug"`
	Name       string  `json:"name"`
	CapUSD     float64 `json:"monthly_cap_usd"`
	SpentMonth float64 `json:"spent_month_usd"`
}

// AILoan is a connection of a personal namespace lent to an organization.
type AILoan struct {
	ID           int64   `json:"id"`
	ConnectionID int64   `json:"connection_id"`
	OrgID        int64   `json:"org_id"`
	OrgSlug      string  `json:"org_slug"`
	OrgName      string  `json:"org_name"`
	CapUSD       float64 `json:"monthly_cap_usd"`
	SpentMonth   float64 `json:"spent_month_usd"`
	CreatedAt    string  `json:"created_at"`
}

// AIAlias names a connection and model. Level server has OrgID 0, level repo
// names its Project.
type AIAlias struct {
	ID           int64  `json:"id"`
	Level        string `json:"level"`
	OrgID        int64  `json:"org_id"`
	Project      string `json:"project,omitempty"`
	Alias        string `json:"alias"`
	ConnectionID int64  `json:"connection_id"`
	Connection   string `json:"connection"`
	Model        string `json:"model"`
}

// AICap is a cap of an owner: Workflow "" is the owner over all workflows
// (and the default run cap), else one workflow. 0 is no cap.
type AICap struct {
	OrgID    int64   `json:"org_id"`
	Workflow string  `json:"workflow"`
	MonthUSD float64 `json:"month_usd"`
	RunUSD   float64 `json:"run_usd"`
}

// AIRoute is what the proxy needs for one call: the run, the connection with
// its sealed key, the real model and its price, and how much the tightest cap
// still allows.
type AIRoute struct {
	RunID       int64
	OrgID       int64
	Project     string
	Workflow    string
	Requested   string
	Connection  AIConnection
	KeySealed   []byte
	Model       AIModel
	Via         string
	LoanID      int64
	Remaining   float64 // USD; +Inf without any cap
	TightestCap string
}

// AICall is one booked call.
type AICall struct {
	RunID                                                        int64
	Route                                                        AIRoute
	InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens int
	CostUSD                                                      float64
	Status                                                       int
}

// AIRunUsage sums what a run used: cost, the connections and models it
// called, and the cap it reached.
type AIRunUsage struct {
	CostUSD     float64
	Calls       int
	Connections []string
	Models      []string
	Cap         string
}

// AICostRow is one line of the cost overview.
type AICostRow struct {
	Key     string  `json:"key"`
	Project string  `json:"project,omitempty"`
	RunID   int64   `json:"run_id,omitempty"`
	Calls   int     `json:"calls"`
	Tokens  int     `json:"tokens"`
	CostUSD float64 `json:"cost_usd"`
}

// AICostReport is what a namespace spent since a point in time, by
// workflow, run, connection and model.
type AICostReport struct {
	TotalUSD     float64     `json:"total_usd"`
	Calls        int         `json:"calls"`
	ByWorkflow   []AICostRow `json:"by_workflow"`
	ByRun        []AICostRow `json:"by_run"`
	ByConnection []AICostRow `json:"by_connection"`
	ByModel      []AICostRow `json:"by_model"`
}

const aiSchema = `
CREATE TABLE IF NOT EXISTS ai_connections(
  id INTEGER PRIMARY KEY,
  org_id INTEGER NOT NULL DEFAULT 0,
  name TEXT NOT NULL,
  format TEXT NOT NULL CHECK(format IN ('openai','anthropic')),
  base_url TEXT NOT NULL,
  key_sealed BLOB,
  key_last4 TEXT NOT NULL DEFAULT '',
  from_file INTEGER NOT NULL DEFAULT 0 CHECK(from_file IN (0,1)),
  monthly_cap_usd REAL NOT NULL DEFAULT 0 CHECK(monthly_cap_usd >= 0),
  created_by INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL DEFAULT '',
  UNIQUE(org_id, name));
CREATE TABLE IF NOT EXISTS ai_connection_models(
  connection_id INTEGER NOT NULL REFERENCES ai_connections(id) ON DELETE CASCADE,
  model TEXT NOT NULL,
  in_usd_per_mtok REAL NOT NULL CHECK(in_usd_per_mtok >= 0),
  out_usd_per_mtok REAL NOT NULL CHECK(out_usd_per_mtok >= 0),
  PRIMARY KEY(connection_id, model));
CREATE TABLE IF NOT EXISTS ai_server_release(
  id INTEGER PRIMARY KEY CHECK(id = 1),
  mode TEXT NOT NULL CHECK(mode IN ('all','selected','none')),
  updated_by INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS ai_server_grants(
  org_id INTEGER PRIMARY KEY REFERENCES orgs(id) ON DELETE CASCADE,
  monthly_cap_usd REAL NOT NULL DEFAULT 0 CHECK(monthly_cap_usd >= 0),
  created_by INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS ai_loans(
  id INTEGER PRIMARY KEY,
  connection_id INTEGER NOT NULL REFERENCES ai_connections(id) ON DELETE CASCADE,
  org_id INTEGER NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  monthly_cap_usd REAL NOT NULL CHECK(monthly_cap_usd > 0),
  lender INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  revoked_at TEXT NOT NULL DEFAULT '');
CREATE UNIQUE INDEX IF NOT EXISTS ai_loans_active ON ai_loans(connection_id, org_id) WHERE revoked_at='';
CREATE TABLE IF NOT EXISTS ai_aliases(
  id INTEGER PRIMARY KEY,
  level TEXT NOT NULL CHECK(level IN ('server','owner','repo')),
  org_id INTEGER NOT NULL DEFAULT 0,
  project TEXT NOT NULL DEFAULT '',
  alias TEXT NOT NULL,
  connection_id INTEGER NOT NULL REFERENCES ai_connections(id) ON DELETE CASCADE,
  model TEXT NOT NULL,
  updated_by INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL,
  UNIQUE(level, org_id, project, alias));
CREATE TABLE IF NOT EXISTS ai_caps(
  org_id INTEGER NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  workflow TEXT NOT NULL DEFAULT '',
  month_usd REAL NOT NULL DEFAULT 0 CHECK(month_usd >= 0),
  run_usd REAL NOT NULL DEFAULT 0 CHECK(run_usd >= 0),
  updated_by INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(org_id, workflow));
CREATE TABLE IF NOT EXISTS ai_run_tokens(
  token_hash TEXT PRIMARY KEY,
  run_id INTEGER NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
  created_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS ai_run_tokens_run ON ai_run_tokens(run_id);
CREATE TABLE IF NOT EXISTS ai_run_caps(
  run_id INTEGER PRIMARY KEY REFERENCES workflow_runs(id) ON DELETE CASCADE,
  cap TEXT NOT NULL,
  created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS ai_calls(
  id INTEGER PRIMARY KEY,
  run_id INTEGER NOT NULL,
  org_id INTEGER NOT NULL,
  project TEXT NOT NULL,
  workflow TEXT NOT NULL,
  connection_id INTEGER NOT NULL,
  connection TEXT NOT NULL,
  via TEXT NOT NULL CHECK(via IN ('own','server','loan')),
  loan_id INTEGER NOT NULL DEFAULT 0,
  requested TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens INTEGER NOT NULL DEFAULT 0,
  cache_write_tokens INTEGER NOT NULL DEFAULT 0,
  cost_usd REAL NOT NULL DEFAULT 0,
  status INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS ai_calls_run ON ai_calls(run_id);
CREATE INDEX IF NOT EXISTS ai_calls_org ON ai_calls(org_id, created_at);
CREATE INDEX IF NOT EXISTS ai_calls_connection ON ai_calls(connection_id, created_at);
CREATE INDEX IF NOT EXISTS ai_calls_loan ON ai_calls(loan_id, created_at) WHERE loan_id > 0;
`

func ensureAIConnections(db *sql.DB) error {
	_, err := db.Exec(aiSchema)
	return err
}

// aiMonthStart is the first second of the current month in UTC, as stored.
var aiNow = func() time.Time { return time.Now().UTC() }

func aiMonthStart() string {
	t := aiNow()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
}

func isServerAdminTx(q rowQuerier, account int64) bool {
	var admin int
	return q.QueryRow(`SELECT is_admin FROM persons WHERE id=?`, account).Scan(&admin) == nil && admin == 1
}

// aiName: letters, digits, dot, dash, underscore; 1 to 40 characters.
func cleanAIName(s string, max int) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len([]rune(s)) > max {
		return "", false
	}
	for _, r := range s {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.') {
			return "", false
		}
	}
	return s, true
}

func cleanAIModels(in []AIModel) ([]AIModel, bool) {
	if len(in) == 0 || len(in) > aiMaxModels {
		return nil, false
	}
	seen := map[string]bool{}
	out := make([]AIModel, 0, len(in))
	for _, m := range in {
		m.Model = strings.TrimSpace(m.Model)
		if m.Model == "" || len(m.Model) > 100 || strings.ContainsAny(m.Model, " \t\r\n/") || seen[m.Model] {
			return nil, false
		}
		for _, p := range []float64{m.InUSD, m.OutUSD} {
			if p < 0 || p > aiMaxPrice || math.IsNaN(p) || math.IsInf(p, 0) {
				return nil, false
			}
		}
		seen[m.Model] = true
		out = append(out, m)
	}
	return out, true
}

func validCap(c float64) bool { return c >= 0 && c <= aiMaxCap && !math.IsNaN(c) }

// checkAIBaseURL is the structural rule for an address: https, and plain http
// only for the server's own connections (where the proxy then dials loopback
// only). The proxy checks the resolved address again when it dials.
func checkAIBaseURL(raw string, server bool) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return u.Scheme == "https" || (server && u.Scheme == "http")
}

// canKeepAIOf: the admin keeps the server's connections, the owner those of
// a namespace.
func canKeepAIOf(q rowQuerier, orgID, account int64) error {
	if orgID == 0 {
		if !isServerAdminTx(q, account) {
			return ErrNotServerAdmin
		}
		return nil
	}
	if orgRoleTx(q, orgID, account) != OrgOwner {
		return ErrNotOrgOwner
	}
	return nil
}

const aiConnCols = `id, org_id, name, format, base_url, key_last4, from_file, monthly_cap_usd, created_at`

func scanAIConnection(r rowScanner) (AIConnection, error) {
	var c AIConnection
	var file int
	err := r.Scan(&c.ID, &c.OrgID, &c.Name, &c.Format, &c.BaseURL, &c.KeyLast4, &file, &c.MonthlyCapUSD, &c.CreatedAt)
	c.FromFile = file == 1
	return c, err
}

type aiQuerier interface {
	rowQuerier
	Query(query string, args ...any) (*sql.Rows, error)
}

func aiModelsTx(q aiQuerier, id int64) []AIModel {
	rows, err := q.Query(`SELECT model, in_usd_per_mtok, out_usd_per_mtok FROM ai_connection_models WHERE connection_id=? ORDER BY model`, id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []AIModel
	for rows.Next() {
		var m AIModel
		if rows.Scan(&m.Model, &m.InUSD, &m.OutUSD) == nil {
			out = append(out, m)
		}
	}
	return out
}

func aiConnectionTx(q aiQuerier, id int64) (AIConnection, error) {
	c, err := scanAIConnection(q.QueryRow(`SELECT `+aiConnCols+` FROM ai_connections WHERE id=?`, id))
	if err != nil {
		return AIConnection{}, err
	}
	c.Models = aiModelsTx(q, id)
	return c, nil
}

func aiConnectionsTx(q aiQuerier, query string, args ...any) ([]AIConnection, error) {
	rows, err := q.Query(`SELECT `+aiConnCols+` FROM ai_connections `+query, args...)
	if err != nil {
		return nil, err
	}
	var out []AIConnection
	for rows.Next() {
		c, err := scanAIConnection(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Models = aiModelsTx(q, out[i].ID)
	}
	return out, nil
}

func setAIModelsTx(tx *sql.Tx, id int64, models []AIModel) error {
	if _, err := tx.Exec(`DELETE FROM ai_connection_models WHERE connection_id=?`, id); err != nil {
		return err
	}
	for _, m := range models {
		if _, err := tx.Exec(`INSERT INTO ai_connection_models(connection_id, model, in_usd_per_mtok, out_usd_per_mtok) VALUES(?,?,?,?)`, id, m.Model, m.InUSD, m.OutUSD); err != nil {
			return err
		}
	}
	return nil
}

// SaveAIConnection creates or changes a connection of the server (OrgID 0,
// admin only) or of a namespace (its owner only).
func (s *Store) SaveAIConnection(actorPrincipal string, in AIConnectionInput) (AIConnection, error) {
	if s.writer != nil {
		return queueValue(s, []any{actorPrincipal, in}, func(d *Store, p []any) (AIConnection, error) {
			return d.SaveAIConnection(p[0].(string), p[1].(AIConnectionInput))
		})
	}
	actor, err := parsePersonPrincipalID(actorPrincipal)
	if err != nil {
		return AIConnection{}, err
	}
	name, ok := cleanAIName(in.Name, 40)
	if !ok || (in.Format != AIFormatOpenAI && in.Format != AIFormatAnthropic) || !validCap(in.MonthlyCapUSD) {
		return AIConnection{}, ErrAIConnection
	}
	models, ok := cleanAIModels(in.Models)
	if !ok {
		return AIConnection{}, ErrAIConnection
	}
	in.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	tx, err := s.db.Begin()
	if err != nil {
		return AIConnection{}, err
	}
	defer tx.Rollback()
	ts := now()
	id := in.ID
	if id == 0 {
		if err := canKeepAIOf(tx, in.OrgID, actor); err != nil {
			return AIConnection{}, err
		}
		if !checkAIBaseURL(in.BaseURL, in.OrgID == 0) {
			return AIConnection{}, ErrAIConnection
		}
		if len(in.KeySealed) == 0 {
			return AIConnection{}, ErrAIKeyMissing
		}
		var n int
		_ = tx.QueryRow(`SELECT COUNT(*) FROM ai_connections WHERE org_id=?`, in.OrgID).Scan(&n)
		if n >= aiMaxPerKind {
			return AIConnection{}, ErrAIConnection
		}
		res, err := tx.Exec(`INSERT INTO ai_connections(org_id, name, format, base_url, key_sealed, key_last4, monthly_cap_usd, created_by, created_at, updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?)`, in.OrgID, name, in.Format, in.BaseURL, in.KeySealed, in.KeyLast4, in.MonthlyCapUSD, actor, ts, ts)
		if err != nil {
			if isUniqueViolation(err) {
				return AIConnection{}, ErrAINameTaken
			}
			return AIConnection{}, err
		}
		id, _ = res.LastInsertId()
	} else {
		old, err := aiConnectionTx(tx, id)
		if err != nil {
			return AIConnection{}, ErrAIUnavailable
		}
		if err := canKeepAIOf(tx, old.OrgID, actor); err != nil {
			return AIConnection{}, err
		}
		if old.FromFile {
			// Address, format and key come from llm.json; cap and models are kept here.
			in.Format, in.BaseURL, in.KeySealed, name = old.Format, old.BaseURL, nil, old.Name
		} else if !checkAIBaseURL(in.BaseURL, old.OrgID == 0) {
			return AIConnection{}, ErrAIConnection
		} else if (in.BaseURL != old.BaseURL || in.Format != old.Format) && len(in.KeySealed) == 0 {
			// The stored key never goes to an address it was not entered for.
			return AIConnection{}, ErrAIKeyMissing
		}
		q := `UPDATE ai_connections SET name=?, format=?, base_url=?, monthly_cap_usd=?, updated_at=?`
		args := []any{name, in.Format, in.BaseURL, in.MonthlyCapUSD, ts}
		if len(in.KeySealed) > 0 {
			q += `, key_sealed=?, key_last4=?`
			args = append(args, in.KeySealed, in.KeyLast4)
		}
		if _, err := tx.Exec(q+` WHERE id=?`, append(args, id)...); err != nil {
			if isUniqueViolation(err) {
				return AIConnection{}, ErrAINameTaken
			}
			return AIConnection{}, err
		}
	}
	if err := setAIModelsTx(tx, id, models); err != nil {
		return AIConnection{}, err
	}
	// An alias whose model the connection no longer serves would fail at the
	// next run; it goes with the model.
	if _, err := tx.Exec(`DELETE FROM ai_aliases WHERE connection_id=? AND model NOT IN (SELECT model FROM ai_connection_models WHERE connection_id=?)`, id, id); err != nil {
		return AIConnection{}, err
	}
	c, err := aiConnectionTx(tx, id)
	if err != nil {
		return AIConnection{}, err
	}
	return c, tx.Commit()
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// DeleteAIConnection removes a connection with its models, loans and aliases.
// Its calls stay in the ledger under its name.
func (s *Store) DeleteAIConnection(actorPrincipal string, id int64) error {
	if s.writer != nil {
		return queueWrite(s, []any{actorPrincipal, id}, func(d *Store, p []any) error { return d.DeleteAIConnection(p[0].(string), p[1].(int64)) })
	}
	actor, err := parsePersonPrincipalID(actorPrincipal)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	c, err := aiConnectionTx(tx, id)
	if err != nil {
		return ErrAIUnavailable
	}
	if err := canKeepAIOf(tx, c.OrgID, actor); err != nil {
		return err
	}
	if c.FromFile {
		return ErrAIFromFile
	}
	for _, q := range []string{`DELETE FROM ai_aliases WHERE connection_id=?`, `DELETE FROM ai_loans WHERE connection_id=?`, `DELETE FROM ai_connection_models WHERE connection_id=?`, `DELETE FROM ai_connections WHERE id=?`} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ImportFileAIConnection keeps the server connection that llm.json describes
// (the way the server reached a model before E1). Its key stays in the file
// and is handed to the proxy in memory; the row carries the last four
// characters. The models are only set when the row is new, so an admin can
// change them; the first import also gives the server the default aliases.
func (s *Store) ImportFileAIConnection(in AIFileConnection) (AIConnection, error) {
	if s.writer != nil {
		return queueValue(s, []any{in}, func(d *Store, p []any) (AIConnection, error) {
			return d.ImportFileAIConnection(p[0].(AIFileConnection))
		})
	}
	name, ok := cleanAIName(in.Name, 40)
	if !ok || (in.Format != AIFormatOpenAI && in.Format != AIFormatAnthropic) || !checkAIBaseURL(in.BaseURL, true) {
		return AIConnection{}, ErrAIConnection
	}
	models, ok := cleanAIModels([]AIModel{{Model: in.Model, InUSD: in.InUSD, OutUSD: in.OutUSD}})
	if !ok {
		return AIConnection{}, ErrAIConnection
	}
	tx, err := s.db.Begin()
	if err != nil {
		return AIConnection{}, err
	}
	defer tx.Rollback()
	ts := now()
	var id int64
	var file int
	err = tx.QueryRow(`SELECT id, from_file FROM ai_connections WHERE org_id=0 AND name=?`, name).Scan(&id, &file)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, err := tx.Exec(`INSERT INTO ai_connections(org_id, name, format, base_url, key_last4, from_file, created_at, updated_at) VALUES(0,?,?,?,?,1,?,?)`,
			name, in.Format, strings.TrimRight(in.BaseURL, "/"), in.KeyLast4, ts, ts)
		if err != nil {
			return AIConnection{}, err
		}
		id, _ = res.LastInsertId()
		if err := setAIModelsTx(tx, id, models); err != nil {
			return AIConnection{}, err
		}
		var aliases int
		_ = tx.QueryRow(`SELECT COUNT(*) FROM ai_aliases WHERE level='server'`).Scan(&aliases)
		if aliases == 0 {
			for _, a := range DefaultAIAliases {
				if _, err := tx.Exec(`INSERT INTO ai_aliases(level, org_id, project, alias, connection_id, model, updated_at) VALUES('server',0,'',?,?,?,?)`, a, id, models[0].Model, ts); err != nil {
					return AIConnection{}, err
				}
			}
		}
	case err != nil:
		return AIConnection{}, err
	case file != 1:
		// An admin made a connection of that name by hand; it is not overwritten.
		return AIConnection{}, ErrAINameTaken
	default:
		if _, err := tx.Exec(`UPDATE ai_connections SET format=?, base_url=?, key_last4=?, updated_at=? WHERE id=?`, in.Format, strings.TrimRight(in.BaseURL, "/"), in.KeyLast4, ts, id); err != nil {
			return AIConnection{}, err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO ai_connection_models(connection_id, model, in_usd_per_mtok, out_usd_per_mtok) VALUES(?,?,?,?)`, id, models[0].Model, models[0].InUSD, models[0].OutUSD); err != nil {
			return AIConnection{}, err
		}
	}
	c, err := aiConnectionTx(tx, id)
	if err != nil {
		return AIConnection{}, err
	}
	return c, tx.Commit()
}

// AIConnectionKey is the sealed key of a connection, for the proxy alone.
// fromFile says the key lives in llm.json instead.
func (s *Store) AIConnectionKey(id int64) (sealed []byte, fromFile bool, err error) {
	if s.reader != nil {
		return s.reader.AIConnectionKey(id)
	}
	var file int
	err = s.db.QueryRow(`SELECT key_sealed, from_file FROM ai_connections WHERE id=?`, id).Scan(&sealed, &file)
	return sealed, file == 1, err
}

// ServerAIConnections are the server's connections with this month's spend,
// for the admin.
func (s *Store) ServerAIConnections() ([]AIConnection, error) {
	if s.reader != nil {
		return s.reader.ServerAIConnections()
	}
	out, err := aiConnectionsTx(s.db, `WHERE org_id=0 ORDER BY name`)
	month := aiMonthStart()
	for i := range out {
		out[i].Via = AIViaServer
		out[i].SpentMonth = aiSum(s.db, `connection_id=? AND created_at>=?`, out[i].ID, month)
	}
	return out, err
}

func aiSum(q rowQuerier, where string, args ...any) float64 {
	var v sql.NullFloat64
	_ = q.QueryRow(`SELECT SUM(cost_usd) FROM ai_calls WHERE `+where, args...).Scan(&v)
	return v.Float64
}

func aiReleaseModeTx(q rowQuerier) string {
	mode := AIReleaseNone
	_ = q.QueryRow(`SELECT mode FROM ai_server_release WHERE id=1`).Scan(&mode)
	return mode
}

// aiServerGrantTx says whether the server's connections are released to the
// namespace, and with which cap.
func aiServerGrantTx(q rowQuerier, orgID int64) (bool, float64) {
	if orgID <= 0 {
		return false, 0
	}
	var capUSD float64
	has := q.QueryRow(`SELECT monthly_cap_usd FROM ai_server_grants WHERE org_id=?`, orgID).Scan(&capUSD) == nil
	switch aiReleaseModeTx(q) {
	case AIReleaseAll:
		return true, capUSD
	case AIReleaseSelected:
		return has, capUSD
	}
	return false, 0
}

// AIConnectionsFor are the connections a namespace may use: its own (with
// address and the last four characters of the key), those lent to it (name
// only) and the server's, when they are released to it (name and models).
func (s *Store) AIConnectionsFor(orgID int64) ([]AIConnection, error) {
	if s.reader != nil {
		return s.reader.AIConnectionsFor(orgID)
	}
	month := aiMonthStart()
	own, err := aiConnectionsTx(s.db, `WHERE org_id=? AND org_id>0 ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]AIConnection, 0, len(own))
	for _, c := range own {
		c.Via = AIViaOwn
		c.SpentMonth = aiSum(s.db, `connection_id=? AND org_id=? AND created_at>=?`, c.ID, orgID, month)
		out = append(out, c)
	}
	rows, err := s.db.Query(`SELECT l.id, l.monthly_cap_usd, c.id, c.name FROM ai_loans l JOIN ai_connections c ON c.id=l.connection_id
		WHERE l.org_id=? AND l.revoked_at='' ORDER BY c.name`, orgID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		c := AIConnection{Via: AIViaLoan}
		if err := rows.Scan(&c.LoanID, &c.LoanCapUSD, &c.ID, &c.Name); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, c)
	}
	rows.Close()
	for i := range out {
		if out[i].Via == AIViaLoan {
			out[i].SpentMonth = aiSum(s.db, `loan_id=? AND created_at>=?`, out[i].LoanID, month)
		}
	}
	if ok, _ := aiServerGrantTx(s.db, orgID); ok {
		server, err := aiConnectionsTx(s.db, `WHERE org_id=0 ORDER BY name`)
		if err != nil {
			return nil, err
		}
		for _, c := range server {
			out = append(out, AIConnection{ID: c.ID, Name: c.Name, Models: c.Models, Via: AIViaServer,
				SpentMonth: aiSum(s.db, `connection_id=? AND org_id=? AND created_at>=?`, c.ID, orgID, month)})
		}
	}
	return out, nil
}

// AIRelease is how the server's connections are released, with the grants.
func (s *Store) AIRelease() (string, []AIGrant, error) {
	if s.reader != nil {
		return s.reader.AIRelease()
	}
	mode := aiReleaseModeTx(s.db)
	rows, err := s.db.Query(`SELECT g.org_id, o.slug, o.name, g.monthly_cap_usd FROM ai_server_grants g JOIN orgs o ON o.id=g.org_id ORDER BY o.slug`)
	if err != nil {
		return mode, nil, err
	}
	var out []AIGrant
	for rows.Next() {
		var g AIGrant
		if err := rows.Scan(&g.OrgID, &g.Slug, &g.Name, &g.CapUSD); err != nil {
			rows.Close()
			return mode, nil, err
		}
		out = append(out, g)
	}
	rows.Close()
	month := aiMonthStart()
	for i := range out {
		out[i].SpentMonth = aiSum(s.db, `org_id=? AND via='server' AND created_at>=?`, out[i].OrgID, month)
	}
	return mode, out, rows.Err()
}

// SetAIRelease says to whom the server's connections are released: all
// namespaces, the selected ones (grants), or none. Admin only.
func (s *Store) SetAIRelease(actorPrincipal, mode string) error {
	if s.writer != nil {
		return queueWrite(s, []any{actorPrincipal, mode}, func(d *Store, p []any) error { return d.SetAIRelease(p[0].(string), p[1].(string)) })
	}
	actor, err := parsePersonPrincipalID(actorPrincipal)
	if err != nil {
		return err
	}
	if mode != AIReleaseAll && mode != AIReleaseSelected && mode != AIReleaseNone {
		return fmt.Errorf("%w: release is all, selected or none", ErrInvalidInput)
	}
	if !isServerAdminTx(s.db, actor) {
		return ErrNotServerAdmin
	}
	_, err = s.db.Exec(`INSERT INTO ai_server_release(id, mode, updated_by, updated_at) VALUES(1,?,?,?)
		ON CONFLICT(id) DO UPDATE SET mode=excluded.mode, updated_by=excluded.updated_by, updated_at=excluded.updated_at`, mode, actor, now())
	return err
}

// SetAIGrant releases the server's connections to one namespace with its own
// monthly cap (0: no cap of its own). Admin only.
func (s *Store) SetAIGrant(actorPrincipal string, orgID int64, capUSD float64) error {
	if s.writer != nil {
		return queueWrite(s, []any{actorPrincipal, orgID, capUSD}, func(d *Store, p []any) error {
			return d.SetAIGrant(p[0].(string), p[1].(int64), p[2].(float64))
		})
	}
	actor, err := parsePersonPrincipalID(actorPrincipal)
	if err != nil {
		return err
	}
	if !validCap(capUSD) {
		return ErrAICap
	}
	if !isServerAdminTx(s.db, actor) {
		return ErrNotServerAdmin
	}
	if _, err := orgByIDTx(s.db, orgID); err != nil {
		return ErrOrgNotFound
	}
	_, err = s.db.Exec(`INSERT INTO ai_server_grants(org_id, monthly_cap_usd, created_by, created_at) VALUES(?,?,?,?)
		ON CONFLICT(org_id) DO UPDATE SET monthly_cap_usd=excluded.monthly_cap_usd`, orgID, capUSD, actor, now())
	return err
}

// DeleteAIGrant takes the release of one namespace back. Admin only.
func (s *Store) DeleteAIGrant(actorPrincipal string, orgID int64) error {
	if s.writer != nil {
		return queueWrite(s, []any{actorPrincipal, orgID}, func(d *Store, p []any) error { return d.DeleteAIGrant(p[0].(string), p[1].(int64)) })
	}
	actor, err := parsePersonPrincipalID(actorPrincipal)
	if err != nil {
		return err
	}
	if !isServerAdminTx(s.db, actor) {
		return ErrNotServerAdmin
	}
	_, err = s.db.Exec(`DELETE FROM ai_server_grants WHERE org_id=?`, orgID)
	return err
}

// LendAIConnection lends a connection of the actor's personal namespace to
// an organization the actor belongs to, with a monthly cap.
func (s *Store) LendAIConnection(actorPrincipal string, connectionID, orgID int64, capUSD float64) (AILoan, error) {
	if s.writer != nil {
		return queueValue(s, []any{actorPrincipal, connectionID, orgID, capUSD}, func(d *Store, p []any) (AILoan, error) {
			return d.LendAIConnection(p[0].(string), p[1].(int64), p[2].(int64), p[3].(float64))
		})
	}
	actor, err := parsePersonPrincipalID(actorPrincipal)
	if err != nil {
		return AILoan{}, err
	}
	if capUSD <= 0 || !validCap(capUSD) {
		return AILoan{}, ErrAILoan
	}
	tx, err := s.db.Begin()
	if err != nil {
		return AILoan{}, err
	}
	defer tx.Rollback()
	c, err := aiConnectionTx(tx, connectionID)
	if err != nil || c.OrgID == 0 {
		return AILoan{}, ErrAIUnavailable
	}
	from, err := orgByIDTx(tx, c.OrgID)
	if err != nil || from.Kind != OrgKindUser || from.AccountID != actor {
		return AILoan{}, ErrNotOrgOwner
	}
	to, err := orgByIDTx(tx, orgID)
	if err != nil || to.Kind == OrgKindUser || orgRoleTx(tx, orgID, actor) == "" {
		return AILoan{}, ErrOrgNotFound
	}
	ts := now()
	if _, err := tx.Exec(`UPDATE ai_loans SET monthly_cap_usd=? WHERE connection_id=? AND org_id=? AND revoked_at=''`, capUSD, connectionID, orgID); err != nil {
		return AILoan{}, err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO ai_loans(connection_id, org_id, monthly_cap_usd, lender, created_at) VALUES(?,?,?,?,?)`, connectionID, orgID, capUSD, actor, ts); err != nil {
		return AILoan{}, err
	}
	var l AILoan
	if err := tx.QueryRow(`SELECT id, created_at FROM ai_loans WHERE connection_id=? AND org_id=? AND revoked_at=''`, connectionID, orgID).Scan(&l.ID, &l.CreatedAt); err != nil {
		return AILoan{}, err
	}
	l.ConnectionID, l.OrgID, l.OrgSlug, l.OrgName, l.CapUSD = connectionID, orgID, to.Slug, to.Name, capUSD
	return l, tx.Commit()
}

// RevokeAILoan takes a loan back. It holds from the next call on: the proxy
// looks the loan up for every call. The aliases of the organization that
// pointed at it go too.
func (s *Store) RevokeAILoan(actorPrincipal string, loanID int64) error {
	if s.writer != nil {
		return queueWrite(s, []any{actorPrincipal, loanID}, func(d *Store, p []any) error { return d.RevokeAILoan(p[0].(string), p[1].(int64)) })
	}
	actor, err := parsePersonPrincipalID(actorPrincipal)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var connID, orgID, owner int64
	if err := tx.QueryRow(`SELECT l.connection_id, l.org_id, c.org_id FROM ai_loans l JOIN ai_connections c ON c.id=l.connection_id WHERE l.id=? AND l.revoked_at=''`, loanID).Scan(&connID, &orgID, &owner); err != nil {
		return ErrAIUnavailable
	}
	if orgRoleTx(tx, owner, actor) != OrgOwner {
		return ErrNotOrgOwner
	}
	if _, err := tx.Exec(`UPDATE ai_loans SET revoked_at=? WHERE id=?`, now(), loanID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM ai_aliases WHERE connection_id=? AND org_id=? AND level IN ('owner','repo')`, connID, orgID); err != nil {
		return err
	}
	return tx.Commit()
}

// AILoansOf are the active loans of a connection, for its lender.
func (s *Store) AILoansOf(connectionID int64) ([]AILoan, error) {
	if s.reader != nil {
		return s.reader.AILoansOf(connectionID)
	}
	rows, err := s.db.Query(`SELECT l.id, l.connection_id, l.org_id, o.slug, o.name, l.monthly_cap_usd, l.created_at FROM ai_loans l JOIN orgs o ON o.id=l.org_id
		WHERE l.connection_id=? AND l.revoked_at='' ORDER BY o.slug`, connectionID)
	if err != nil {
		return nil, err
	}
	var out []AILoan
	for rows.Next() {
		var l AILoan
		if err := rows.Scan(&l.ID, &l.ConnectionID, &l.OrgID, &l.OrgSlug, &l.OrgName, &l.CapUSD, &l.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, l)
	}
	rows.Close()
	month := aiMonthStart()
	for i := range out {
		out[i].SpentMonth = aiSum(s.db, `loan_id=? AND created_at>=?`, out[i].ID, month)
	}
	return out, rows.Err()
}

// aiUsableTx says how a namespace reaches a connection: its own, a loan, or a
// release of the server's. ok is false when it may not use it.
func aiUsableTx(q rowQuerier, orgID int64, c AIConnection) (via string, loanID int64, loanCap float64, ok bool) {
	if c.OrgID != 0 && c.OrgID == orgID {
		return AIViaOwn, 0, 0, true
	}
	if c.OrgID == 0 {
		if released, _ := aiServerGrantTx(q, orgID); released {
			return AIViaServer, 0, 0, true
		}
		return "", 0, 0, false
	}
	if q.QueryRow(`SELECT id, monthly_cap_usd FROM ai_loans WHERE connection_id=? AND org_id=? AND revoked_at=''`, c.ID, orgID).Scan(&loanID, &loanCap) == nil {
		return AIViaLoan, loanID, loanCap, true
	}
	return "", 0, 0, false
}

func aiHasModel(c AIConnection, model string) (AIModel, bool) {
	for _, m := range c.Models {
		if m.Model == model {
			return m, true
		}
	}
	return AIModel{}, false
}

func cleanAliasName(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	return cleanAIName(s, 40)
}

// SetAIAlias points an alias at a connection and model on the level of the
// server (admin), an owner or one of the owner's repositories (owner). The
// connection must be usable there and serve the model.
func (s *Store) SetAIAlias(actorPrincipal string, in AIAlias) (AIAlias, error) {
	if s.writer != nil {
		return queueValue(s, []any{actorPrincipal, in}, func(d *Store, p []any) (AIAlias, error) { return d.SetAIAlias(p[0].(string), p[1].(AIAlias)) })
	}
	actor, err := parsePersonPrincipalID(actorPrincipal)
	if err != nil {
		return AIAlias{}, err
	}
	alias, ok := cleanAliasName(in.Alias)
	if !ok {
		return AIAlias{}, ErrAIAlias
	}
	in.Alias, in.Model = alias, strings.TrimSpace(in.Model)
	tx, err := s.db.Begin()
	if err != nil {
		return AIAlias{}, err
	}
	defer tx.Rollback()
	switch in.Level {
	case AILevelServer:
		in.OrgID, in.Project = 0, ""
	case AILevelOwner:
		in.Project = ""
	case AILevelRepo:
		p, ok := projectTx(tx, in.Project)
		if !ok || p.OrgID != in.OrgID || in.OrgID == 0 {
			return AIAlias{}, ErrAIAlias
		}
	default:
		return AIAlias{}, ErrAIAlias
	}
	if err := canKeepAIOf(tx, in.OrgID, actor); err != nil {
		return AIAlias{}, err
	}
	c, err := aiConnectionTx(tx, in.ConnectionID)
	if err != nil {
		return AIAlias{}, ErrAIUnavailable
	}
	if in.Level == AILevelServer {
		if c.OrgID != 0 {
			return AIAlias{}, ErrAIUnavailable
		}
	} else if _, _, _, ok := aiUsableTx(tx, in.OrgID, c); !ok {
		return AIAlias{}, ErrAIUnavailable
	}
	if _, ok := aiHasModel(c, in.Model); !ok {
		return AIAlias{}, ErrAIUnavailable
	}
	ts := now()
	if _, err := tx.Exec(`INSERT INTO ai_aliases(level, org_id, project, alias, connection_id, model, updated_by, updated_at) VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(level, org_id, project, alias) DO UPDATE SET connection_id=excluded.connection_id, model=excluded.model, updated_by=excluded.updated_by, updated_at=excluded.updated_at`,
		in.Level, in.OrgID, in.Project, in.Alias, in.ConnectionID, in.Model, actor, ts); err != nil {
		return AIAlias{}, err
	}
	if err := tx.QueryRow(`SELECT id FROM ai_aliases WHERE level=? AND org_id=? AND project=? AND alias=?`, in.Level, in.OrgID, in.Project, in.Alias).Scan(&in.ID); err != nil {
		return AIAlias{}, err
	}
	in.Connection = c.Name
	return in, tx.Commit()
}

// DeleteAIAlias removes an alias, with the same rights as setting it.
func (s *Store) DeleteAIAlias(actorPrincipal string, id int64) error {
	if s.writer != nil {
		return queueWrite(s, []any{actorPrincipal, id}, func(d *Store, p []any) error { return d.DeleteAIAlias(p[0].(string), p[1].(int64)) })
	}
	actor, err := parsePersonPrincipalID(actorPrincipal)
	if err != nil {
		return err
	}
	var orgID int64
	if err := s.db.QueryRow(`SELECT org_id FROM ai_aliases WHERE id=?`, id).Scan(&orgID); err != nil {
		return ErrAIAlias
	}
	if err := canKeepAIOf(s.db, orgID, actor); err != nil {
		return err
	}
	_, err = s.db.Exec(`DELETE FROM ai_aliases WHERE id=?`, id)
	return err
}

const aiAliasCols = `a.id, a.level, a.org_id, a.project, a.alias, a.connection_id, c.name, a.model`

func aiAliasesTx(q aiQuerier, where string, args ...any) ([]AIAlias, error) {
	rows, err := q.Query(`SELECT `+aiAliasCols+` FROM ai_aliases a JOIN ai_connections c ON c.id=a.connection_id WHERE `+where+` ORDER BY a.level, a.project, a.alias`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AIAlias
	for rows.Next() {
		var a AIAlias
		if err := rows.Scan(&a.ID, &a.Level, &a.OrgID, &a.Project, &a.Alias, &a.ConnectionID, &a.Connection, &a.Model); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ServerAIAliases are the aliases of the server, for the admin.
func (s *Store) ServerAIAliases() ([]AIAlias, error) {
	if s.reader != nil {
		return s.reader.ServerAIAliases()
	}
	return aiAliasesTx(s.db, `a.level='server'`)
}

// AIAliasesFor are the aliases of a namespace and its repositories, and the
// server's that it can use (those on a connection released to it).
func (s *Store) AIAliasesFor(orgID int64) ([]AIAlias, error) {
	if s.reader != nil {
		return s.reader.AIAliasesFor(orgID)
	}
	out, err := aiAliasesTx(s.db, `a.level IN ('owner','repo') AND a.org_id=?`, orgID)
	if err != nil {
		return nil, err
	}
	if ok, _ := aiServerGrantTx(s.db, orgID); ok {
		server, err := aiAliasesTx(s.db, `a.level='server'`)
		if err != nil {
			return nil, err
		}
		out = append(out, server...)
	}
	// A loaned connection shows its name only; so does an alias on it.
	return out, nil
}

// AICapsOf are the caps of a namespace: the owner's (Workflow "") first,
// then one per workflow.
func (s *Store) AICapsOf(orgID int64) ([]AICap, error) {
	if s.reader != nil {
		return s.reader.AICapsOf(orgID)
	}
	rows, err := s.db.Query(`SELECT org_id, workflow, month_usd, run_usd FROM ai_caps WHERE org_id=? ORDER BY workflow`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AICap{{OrgID: orgID}}
	for rows.Next() {
		var c AICap
		if err := rows.Scan(&c.OrgID, &c.Workflow, &c.MonthUSD, &c.RunUSD); err != nil {
			return nil, err
		}
		if c.Workflow == "" {
			out[0] = c
			continue
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetAICap sets a cap of a namespace; a workflow cap with both values 0 goes.
// Owner only.
func (s *Store) SetAICap(actorPrincipal string, c AICap) error {
	if s.writer != nil {
		return queueWrite(s, []any{actorPrincipal, c}, func(d *Store, p []any) error { return d.SetAICap(p[0].(string), p[1].(AICap)) })
	}
	actor, err := parsePersonPrincipalID(actorPrincipal)
	if err != nil {
		return err
	}
	c.Workflow = strings.TrimSpace(c.Workflow)
	if c.OrgID <= 0 || !validCap(c.MonthUSD) || !validCap(c.RunUSD) || len(c.Workflow) > 200 || strings.ContainsAny(c.Workflow, " \t\r\n") {
		return ErrAICap
	}
	if orgRoleTx(s.db, c.OrgID, actor) != OrgOwner {
		return ErrNotOrgOwner
	}
	if c.Workflow != "" && c.MonthUSD == 0 && c.RunUSD == 0 {
		_, err = s.db.Exec(`DELETE FROM ai_caps WHERE org_id=? AND workflow=?`, c.OrgID, c.Workflow)
		return err
	}
	_, err = s.db.Exec(`INSERT INTO ai_caps(org_id, workflow, month_usd, run_usd, updated_by, updated_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(org_id, workflow) DO UPDATE SET month_usd=excluded.month_usd, run_usd=excluded.run_usd, updated_by=excluded.updated_by, updated_at=excluded.updated_at`,
		c.OrgID, c.Workflow, c.MonthUSD, c.RunUSD, actor, now())
	return err
}

// -------- run tokens

func hashRunToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// IssueAIRunToken gives a running run its token for the model proxy. Only the
// hash is stored; the token is valid while the run is running and goes with
// FinishWorkflowRun, AbandonWorkflowRuns or RevokeAIRunTokens.
func (s *Store) IssueAIRunToken(runID int64) (string, error) {
	if s.writer != nil {
		return queueValue(s, []any{runID}, func(d *Store, p []any) (string, error) { return d.IssueAIRunToken(p[0].(int64)) })
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := AIRunTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	res, err := s.db.Exec(`INSERT INTO ai_run_tokens(token_hash, run_id, created_at) SELECT ?, id, ? FROM workflow_runs WHERE id=? AND state='running'`, hashRunToken(token), now(), runID)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrWorkflowRun
	}
	return token, nil
}

// RevokeAIRunTokens ends the tokens of a run.
func (s *Store) RevokeAIRunTokens(runID int64) error {
	if s.writer != nil {
		return queueWrite(s, []any{runID}, func(d *Store, p []any) error { return d.RevokeAIRunTokens(p[0].(int64)) })
	}
	_, err := s.db.Exec(`DELETE FROM ai_run_tokens WHERE run_id=?`, runID)
	return err
}

func revokeRunTokensTx(tx *sql.Tx, where string, args ...any) error {
	_, err := tx.Exec(`DELETE FROM ai_run_tokens WHERE run_id IN (SELECT id FROM workflow_runs WHERE `+where+`)`, args...)
	return err
}

// AIRunOfToken is the run a token belongs to, only while it is running.
func (s *Store) AIRunOfToken(token string) (WorkflowRun, error) {
	if s.reader != nil {
		return s.reader.AIRunOfToken(token)
	}
	if !strings.HasPrefix(token, AIRunTokenPrefix) || len(token) > 200 {
		return WorkflowRun{}, ErrAIToken
	}
	run, err := scanWorkflowRun(s.db.QueryRow(`SELECT `+workflowRunCols+workflowRunFrom+` JOIN ai_run_tokens t ON t.run_id=r.id
		WHERE t.token_hash=? AND r.state='running'`, hashRunToken(token)))
	if err != nil {
		return WorkflowRun{}, ErrAIToken
	}
	return run, nil
}

// -------- routing, caps and the ledger

// AIRouteFor resolves what a running run asks for: an alias (repository, then
// owner, then server), "connection/model", or a model by its name on a
// connection of the owner or the server. A lent connection is reached through
// an alias only (aiCandidates). It
// refuses when the run reached a cap already, and says how much the tightest
// cap still allows.
func (s *Store) AIRouteFor(run WorkflowRun, requested string) (AIRoute, error) {
	if s.reader != nil {
		return s.reader.AIRouteFor(run, requested)
	}
	var capped string
	if s.db.QueryRow(`SELECT cap FROM ai_run_caps WHERE run_id=?`, run.ID).Scan(&capped) == nil {
		return AIRoute{}, &AIBudgetError{Cap: capped}
	}
	requested = strings.TrimSpace(requested)
	if requested == "" || len(requested) > 200 {
		return AIRoute{}, ErrAIUnavailable
	}
	route := AIRoute{RunID: run.ID, OrgID: run.OrgID, Project: run.Project, Workflow: run.Workflow, Requested: requested}
	pick := func(c AIConnection, model string) bool {
		via, loanID, _, ok := aiUsableTx(s.db, run.OrgID, c)
		if !ok {
			return false
		}
		m, ok := aiHasModel(c, model)
		if !ok {
			return false
		}
		route.Connection, route.Model, route.Via, route.LoanID = c, m, via, loanID
		return true
	}
	found := false
	if conn, model, ok := strings.Cut(requested, "/"); ok {
		for _, c := range s.aiCandidates(run.OrgID) {
			if c.Name == conn && pick(c, model) {
				found = true
				break
			}
		}
	} else if a, ok := s.aiAliasOf(run, strings.ToLower(requested)); ok {
		// The first level that names the alias decides; a connection the owner
		// may not use there is an error, not a reason to look further down.
		c, err := aiConnectionTx(s.db, a.ConnectionID)
		found = err == nil && pick(c, a.Model)
	} else {
		for _, c := range s.aiCandidates(run.OrgID) {
			if pick(c, requested) {
				found = true
				break
			}
		}
	}
	if !found {
		return AIRoute{}, ErrAIUnavailable
	}
	var sealed []byte
	_ = s.db.QueryRow(`SELECT key_sealed FROM ai_connections WHERE id=?`, route.Connection.ID).Scan(&sealed)
	route.KeySealed = sealed
	route.Remaining, route.TightestCap = s.aiRemaining(route)
	if route.Remaining <= 0 {
		return AIRoute{}, &AIBudgetError{Cap: route.TightestCap}
	}
	return route, nil
}

// aiCandidates are the connections a bare model name or "connection/model"
// is looked up on: the owner's own, then the server's. A lent connection is
// never among them: its lender chose its name and models, and could otherwise
// draw an organization's calls to itself by naming them like the owner's. It
// is reached only through an alias an owner of the organization set on it.
func (s *Store) aiCandidates(orgID int64) []AIConnection {
	own, _ := aiConnectionsTx(s.db, `WHERE org_id=? AND org_id>0 ORDER BY id`, orgID)
	server, _ := aiConnectionsTx(s.db, `WHERE org_id=0 ORDER BY id`)
	return slices.Concat(own, server)
}

func (s *Store) aiAliasOf(run WorkflowRun, alias string) (AIAlias, bool) {
	for _, level := range []struct {
		where string
		args  []any
	}{
		{`a.level='repo' AND a.org_id=? AND a.project=? AND a.alias=?`, []any{run.OrgID, run.Project, alias}},
		{`a.level='owner' AND a.org_id=? AND a.alias=?`, []any{run.OrgID, alias}},
		{`a.level='server' AND a.alias=?`, []any{alias}},
	} {
		if as, err := aiAliasesTx(s.db, level.where, level.args...); err == nil && len(as) > 0 {
			return as[0], true
		}
	}
	return AIAlias{}, false
}

// aiCapsFor lists every cap that applies to a call on a route, with what was
// spent against it.
func aiCapsFor(q rowQuerier, r AIRoute) []struct {
	name         string
	limit, spent float64
} {
	month := aiMonthStart()
	type capLine = struct {
		name         string
		limit, spent float64
	}
	var owner, wf AICap
	_ = q.QueryRow(`SELECT month_usd, run_usd FROM ai_caps WHERE org_id=? AND workflow=''`, r.OrgID).Scan(&owner.MonthUSD, &owner.RunUSD)
	_ = q.QueryRow(`SELECT month_usd, run_usd FROM ai_caps WHERE org_id=? AND workflow=?`, r.OrgID, r.Workflow).Scan(&wf.MonthUSD, &wf.RunUSD)
	runCap := wf.RunUSD
	if runCap == 0 {
		runCap = owner.RunUSD
	}
	// The budget in the file of a workflow (REQ-695) only tightens: the lower
	// of it and the caps set in the settings applies.
	var defRun, defMonth float64
	if r.Project != "" {
		_ = q.QueryRow(`SELECT run_usd, month_usd FROM workflow_defs WHERE project=? AND name=?`, r.Project, r.Workflow).Scan(&defRun, &defMonth)
	}
	runCap, wf.MonthUSD = tighterCap(runCap, defRun), tighterCap(wf.MonthUSD, defMonth)
	var conn float64
	_ = q.QueryRow(`SELECT monthly_cap_usd FROM ai_connections WHERE id=?`, r.Connection.ID).Scan(&conn)
	out := []capLine{
		{AICapRun, runCap, aiSum(q, `run_id=?`, r.RunID)},
		{AICapWorkflow, wf.MonthUSD, aiSum(q, `org_id=? AND workflow=? AND created_at>=?`, r.OrgID, r.Workflow, month)},
		{AICapOwner, owner.MonthUSD, aiSum(q, `org_id=? AND created_at>=?`, r.OrgID, month)},
		{AICapConnection, conn, aiSum(q, `connection_id=? AND created_at>=?`, r.Connection.ID, month)},
	}
	switch r.Via {
	case AIViaServer:
		_, grant := aiServerGrantTx(q, r.OrgID)
		out = append(out, capLine{AICapServer, grant, aiSum(q, `org_id=? AND via='server' AND created_at>=?`, r.OrgID, month)})
	case AIViaLoan:
		var loan float64
		_ = q.QueryRow(`SELECT monthly_cap_usd FROM ai_loans WHERE id=?`, r.LoanID).Scan(&loan)
		out = append(out, capLine{AICapLoan, loan, aiSum(q, `loan_id=? AND created_at>=?`, r.LoanID, month)})
	}
	return out
}

func (s *Store) aiRemaining(r AIRoute) (float64, string) {
	remaining, name := math.Inf(1), ""
	for _, c := range aiCapsFor(s.db, r) {
		if c.limit <= 0 {
			continue
		}
		if left := c.limit - c.spent; left < remaining {
			remaining, name = left, c.name
		}
	}
	return remaining, name
}

// MarkAIRunCap notes that a run reached a cap: no further call of the run
// goes through, and the run ends in the state budget with the cap as reason.
// The first cap reached stays.
func (s *Store) MarkAIRunCap(runID int64, capName string) error {
	if s.writer != nil {
		return queueWrite(s, []any{runID, capName}, func(d *Store, p []any) error { return d.MarkAIRunCap(p[0].(int64), p[1].(string)) })
	}
	_, err := s.db.Exec(`INSERT OR IGNORE INTO ai_run_caps(run_id, cap, created_at) SELECT id, ?, ? FROM workflow_runs WHERE id=?`, capName, now(), runID)
	return err
}

// RecordAICall books one call into the ledger. When it used up a cap, the
// run is marked (MarkAIRunCap) and the cap's name returned.
func (s *Store) RecordAICall(c AICall) (string, error) {
	if s.writer != nil {
		return queueValue(s, []any{c}, func(d *Store, p []any) (string, error) { return d.RecordAICall(p[0].(AICall)) })
	}
	if c.CostUSD < 0 || math.IsNaN(c.CostUSD) {
		c.CostUSD = 0
	}
	r := c.Route
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	ts := now()
	if _, err := tx.Exec(`INSERT INTO ai_calls(run_id, org_id, project, workflow, connection_id, connection, via, loan_id, requested, model,
		input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_usd, status, created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.RunID, r.OrgID, r.Project, r.Workflow, r.Connection.ID, r.Connection.Name, r.Via, r.LoanID, cutRunes(r.Requested, 200), r.Model.Model,
		c.InputTokens, c.OutputTokens, c.CacheReadTokens, c.CacheWriteTokens, c.CostUSD, c.Status, ts); err != nil {
		return "", err
	}
	reached := ""
	for _, cl := range aiCapsFor(tx, r) {
		if cl.limit > 0 && cl.spent >= cl.limit {
			reached = cl.name
			break
		}
	}
	if reached != "" {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO ai_run_caps(run_id, cap, created_at) VALUES(?,?,?)`, r.RunID, reached, ts); err != nil {
			return "", err
		}
	}
	return reached, tx.Commit()
}

// AIRunUsageOf sums the calls of a run and names the cap it reached.
func (s *Store) AIRunUsageOf(runID int64) (AIRunUsage, error) {
	if s.reader != nil {
		return s.reader.AIRunUsageOf(runID)
	}
	var u AIRunUsage
	_ = s.db.QueryRow(`SELECT cap FROM ai_run_caps WHERE run_id=?`, runID).Scan(&u.Cap)
	rows, err := s.db.Query(`SELECT connection, model, cost_usd FROM ai_calls WHERE run_id=? ORDER BY id`, runID)
	if err != nil {
		return u, err
	}
	defer rows.Close()
	for rows.Next() {
		var conn, model string
		var cost float64
		if err := rows.Scan(&conn, &model, &cost); err != nil {
			return u, err
		}
		u.Calls++
		u.CostUSD += cost
		if !slices.Contains(u.Connections, conn) {
			u.Connections = append(u.Connections, conn)
		}
		if !slices.Contains(u.Models, model) {
			u.Models = append(u.Models, model)
		}
	}
	return u, rows.Err()
}

// AICostReportOf is what a namespace spent since a time (RFC 3339), by
// workflow, run, connection and model. Every amount belongs to one of each.
func (s *Store) AICostReportOf(orgID int64, since string) (AICostReport, error) {
	if s.reader != nil {
		return s.reader.AICostReportOf(orgID, since)
	}
	var rep AICostReport
	group := func(cols string, limit int) ([]AICostRow, error) {
		rows, err := s.db.Query(`SELECT `+cols+`, COUNT(*), SUM(input_tokens+output_tokens+cache_read_tokens+cache_write_tokens), SUM(cost_usd)
			FROM ai_calls WHERE org_id=? AND created_at>=? GROUP BY `+cols+` ORDER BY SUM(cost_usd) DESC LIMIT ?`, orgID, since, limit)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []AICostRow
		for rows.Next() {
			var r AICostRow
			var dest []any
			switch cols {
			case "run_id, project, workflow":
				dest = []any{&r.RunID, &r.Project, &r.Key}
			default:
				dest = []any{&r.Key}
			}
			if err := rows.Scan(append(dest, &r.Calls, &r.Tokens, &r.CostUSD)...); err != nil {
				return nil, err
			}
			out = append(out, r)
		}
		return out, rows.Err()
	}
	var err error
	if rep.ByWorkflow, err = group("workflow", 100); err != nil {
		return rep, err
	}
	if rep.ByRun, err = group("run_id, project, workflow", 50); err != nil {
		return rep, err
	}
	if rep.ByConnection, err = group("connection", 100); err != nil {
		return rep, err
	}
	if rep.ByModel, err = group("model", 100); err != nil {
		return rep, err
	}
	for _, r := range rep.ByWorkflow {
		rep.TotalUSD += r.CostUSD
		rep.Calls += r.Calls
	}
	sort.SliceStable(rep.ByRun, func(i, j int) bool { return rep.ByRun[i].CostUSD > rep.ByRun[j].CostUSD })
	return rep, nil
}

// AIMonthStart is the start of the month the caps count, for the views.
func AIMonthStart() string { return aiMonthStart() }

// tighterCap is the lower of two caps, where 0 means no cap.
func tighterCap(a, b float64) float64 {
	switch {
	case a <= 0:
		return b
	case b <= 0:
		return a
	}
	return math.Min(a, b)
}
