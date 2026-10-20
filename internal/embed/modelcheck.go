package embed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// ProbeText is the text two embedding servers are both given to find out
// whether they serve the same model. It is fixed and short, and it has nothing
// to do with any entry.
const ProbeText = "ghosttree embedding model check"

// MinAgreement is the cosine of the two probe vectors from which two servers
// count as serving the same model. The same GGUF file on another thread count,
// another build or in another batch differs only by rounding (a cosine of
// 0.99999 and above); another model, another quantisation or another pooling is
// far below. 0.999 sits between the two.
const MinAgreement = 0.999

// Prober embeds one text. *Client is the implementation.
type Prober interface {
	Query(ctx context.Context, q string) ([]float32, error)
}

// DisagreementError says that two servers answered the probe with vectors that
// cannot come from the same model. It is a result, not a failure to ask: both
// servers answered.
type DisagreementError struct {
	// QueryDim and JobDim are the lengths of the two vectors.
	QueryDim, JobDim int
	// Cosine is their cosine; it is 0 when the lengths differ.
	Cosine float64
}

func (e *DisagreementError) Error() string {
	if e.QueryDim != e.JobDim {
		return fmt.Sprintf("the query server returns vectors of %d dimensions, the job server of %d", e.QueryDim, e.JobDim)
	}
	return fmt.Sprintf("the same text has a cosine of %.4f between the two servers (%d dimensions), at least %.3f is needed", e.Cosine, e.QueryDim, MinAgreement)
}

// Agreement is what a successful comparison found out.
type Agreement struct {
	Dimensions int
	Cosine     float64
}

// CompareServers embeds ProbeText on both servers at the same time and compares
// the vectors. A *DisagreementError says that both answered and are not the same
// model; any other error says that at least one did not answer, so nothing is
// known about the models.
func CompareServers(ctx context.Context, query, job Prober) (Agreement, error) {
	var qv, jv []float32
	var qerr, jerr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); qv, qerr = query.Query(ctx, ProbeText) }()
	go func() { defer wg.Done(); jv, jerr = job.Query(ctx, ProbeText) }()
	wg.Wait()
	if qerr != nil {
		qerr = fmt.Errorf("query server: %w", qerr)
	}
	if jerr != nil {
		jerr = fmt.Errorf("job server: %w", jerr)
	}
	if err := errors.Join(qerr, jerr); err != nil {
		return Agreement{}, err
	}
	if len(qv) == 0 || len(jv) == 0 || len(qv) != len(jv) {
		return Agreement{}, &DisagreementError{QueryDim: len(qv), JobDim: len(jv)}
	}
	var dot, qn, jn float64
	for i := range qv {
		a, b := float64(qv[i]), float64(jv[i])
		dot += a * b
		qn += a * a
		jn += b * b
	}
	if qn == 0 || jn == 0 {
		return Agreement{}, &DisagreementError{QueryDim: len(qv), JobDim: len(jv)}
	}
	cos := dot / math.Sqrt(qn*jn)
	if math.IsNaN(cos) || cos < MinAgreement {
		return Agreement{}, &DisagreementError{QueryDim: len(qv), JobDim: len(jv), Cosine: cos}
	}
	return Agreement{Dimensions: len(qv), Cosine: cos}, nil
}

// CheckConfig sets how often a ModelCheck looks. Zero values mean the defaults.
type CheckConfig struct {
	// Timeout bounds one probe request. Default one minute: the job server may be
	// working on a batch of documents and answers after it.
	Timeout time.Duration
	// Agree is the wait after a check that found the servers in agreement. A model
	// is exchanged by someone restarting a server, which is rare, and a ranking
	// made with a query vector of another model is worse, not wrong, so ten
	// minutes (the default) is early enough; each check costs two short texts, one
	// of them in the slot of the server that is busy with the background job.
	Agree time.Duration
	// Disagree is the wait after a check that found a difference. The way out is
	// that someone restarts a server, so it is short: one minute by default, and
	// the search is back one check after the fix.
	Disagree time.Duration
	// Retry is the wait after a check that could not be completed because a server
	// did not answer; it doubles with every further miss up to RetryMax. Defaults
	// 5 seconds and one minute: the servers load the model in seconds at boot.
	Retry, RetryMax time.Duration
	// Alarm is the least time between two log lines about the same state while it
	// lasts. Default ten minutes.
	Alarm time.Duration
}

func (c CheckConfig) withDefaults() CheckConfig {
	def := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&c.Timeout, time.Minute)
	def(&c.Agree, 10*time.Minute)
	def(&c.Disagree, time.Minute)
	def(&c.Retry, 5*time.Second)
	def(&c.RetryMax, time.Minute)
	def(&c.Alarm, 10*time.Minute)
	if c.RetryMax < c.Retry {
		c.RetryMax = c.Retry
	}
	return c
}

// ModelCheck keeps watching that the server which embeds search queries and the
// server which embeds the documents serve the same model. A query vector is only
// worth something next to vectors of the same model.
//
// The verdict errs towards searching: Err reports a disagreement only after a
// check found one, and it keeps reporting it until a check finds the servers in
// agreement. Before the first answer, and while a server does not answer, the
// last verdict stands, so a job server that is down or busy does not switch the
// vector part off.
type ModelCheck struct {
	query, job Prober
	cfg        CheckConfig
	log        *slog.Logger

	disagreement atomic.Pointer[DisagreementError]
}

// NewModelCheck returns a check of the query server against the job server. Start
// Run in a goroutine.
func NewModelCheck(query, job Prober, cfg CheckConfig, logger *slog.Logger) *ModelCheck {
	if logger == nil {
		logger = slog.Default()
	}
	return &ModelCheck{query: query, job: job, cfg: cfg.withDefaults(), log: logger}
}

// Err is nil unless the last conclusive check found that the two servers do not
// serve the same model. It is safe for concurrent use and cheap.
func (m *ModelCheck) Err() error {
	if d := m.disagreement.Load(); d != nil {
		return d
	}
	return nil
}

func sleepFor(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func addressOf(p Prober) string {
	if u, ok := p.(interface{ URL() string }); ok {
		return u.URL()
	}
	return ""
}

// Run checks until ctx ends.
func (m *ModelCheck) Run(ctx context.Context) {
	var (
		misses     int
		announced  bool
		lastLogged time.Time
	)
	quiet := func() bool { return !lastLogged.IsZero() && time.Since(lastLogged) < m.cfg.Alarm }
	for {
		pctx, cancel := context.WithTimeout(ctx, m.cfg.Timeout)
		ag, err := CompareServers(pctx, m.query, m.job)
		cancel()
		if ctx.Err() != nil {
			return
		}
		var diff *DisagreementError
		var wait time.Duration
		switch {
		case err == nil:
			misses = 0
			wait = m.cfg.Agree
			if m.disagreement.Swap(nil) != nil || !announced {
				announced, lastLogged = true, time.Now()
				m.log.Info("embed_models_agree", "dimensions", ag.Dimensions, "cosine", math.Round(ag.Cosine*1e5)/1e5,
					"query_server", addressOf(m.query), "job_server", addressOf(m.job),
					"note", "the query server and the job server serve the same model; searches use the vectors")
			}
		case errors.As(err, &diff):
			misses = 0
			wait = m.cfg.Disagree
			announced = true
			if m.disagreement.Swap(diff) == nil || !quiet() {
				lastLogged = time.Now()
				m.log.Error("embed_models_differ", "error", diff.Error(),
					"query_server", addressOf(m.query), "job_server", addressOf(m.job),
					"note", "the two embedding servers do not serve the same model, so a query vector cannot be compared with the stored vectors: searches answer from full text only until they do. "+
						"Start both from the same GGUF file. After exchanging the model on both, give it a new --embed-model id so that the stored vectors are computed again")
			}
		default:
			misses++
			wait = m.cfg.Retry
			for i := 1; i < misses && wait < m.cfg.RetryMax; i++ {
				wait *= 2
			}
			wait = min(wait, m.cfg.RetryMax)
			if misses == 1 || !quiet() {
				lastLogged = time.Now()
				m.log.Warn("embed_models_unverified", "error", err.Error(), "retry_in", wait.String(),
					"note", "could not compare the query server with the job server; searches go on as before until a check finds a difference")
			}
		}
		if !sleepFor(ctx, wait) {
			return
		}
	}
}
