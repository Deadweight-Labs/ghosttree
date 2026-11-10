package web

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// transcriptsAdminView is the administrator's view of what the stored
// transcript lines take, per line type, from a sample (REQ-622).
type transcriptsAdminView struct {
	Summary string
	Rows    []transcriptKindRow
}

type transcriptKindRow struct{ Kind, Lines, Share, Stored string }

// transcriptStatsCache keeps one sample for a while: drawing it reads a
// thousand lines, which is cheap but not something every page view should do.
type transcriptStatsCache struct {
	mu  sync.Mutex
	at  time.Time
	rep store.ChunkStatsReport
	ok  bool
}

const (
	transcriptStatsSample = 1000
	transcriptStatsTTL    = 10 * time.Minute
	transcriptStatsRows   = 12
)

func (c *transcriptStatsCache) get(ctx context.Context, st *store.Store) (store.ChunkStatsReport, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ok && time.Since(c.at) < transcriptStatsTTL {
		return c.rep, true
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rep, err := st.TranscriptStats(ctx, store.ChunkStatsOptions{Sample: transcriptStatsSample, Pause: -1})
	if err != nil {
		return store.ChunkStatsReport{}, false
	}
	c.rep, c.at, c.ok = rep, time.Now(), true
	return rep, true
}

func (a *app) transcriptsAdmin(ctx context.Context, l lang) *transcriptsAdminView {
	rep, ok := a.transcripts.get(ctx, a.store)
	if !ok || rep.Sampled == 0 {
		return nil
	}
	v := &transcriptsAdminView{Summary: l.msg("adm.transcripts.summary", rep.HighestID, rep.Sampled,
		share(rep.StoredBytes, rep.Bytes), share(rep.PackedBytes, rep.Bytes))}
	var restLines int
	var restBytes, restStored int64
	for i, k := range rep.Kinds {
		if i >= transcriptStatsRows {
			restLines += k.Chunks
			restBytes += k.Bytes
			restStored += k.StoredBytes
			continue
		}
		v.Rows = append(v.Rows, transcriptKindRow{Kind: k.Kind, Lines: fmt.Sprint(k.Chunks),
			Share: share(k.Bytes, rep.Bytes), Stored: share(k.StoredBytes, k.Bytes)})
	}
	if restLines > 0 {
		v.Rows = append(v.Rows, transcriptKindRow{Kind: l.msg("adm.transcripts.rest"), Lines: fmt.Sprint(restLines),
			Share: share(restBytes, rep.Bytes), Stored: share(restStored, restBytes)})
	}
	return v
}

func share(a, b int64) string {
	if b <= 0 {
		return "–"
	}
	return fmt.Sprintf("%.0f %%", 100*float64(a)/float64(b))
}

// transcriptsAdminFor is the block for an administrator, nil for anyone else.
func (a *app) transcriptsAdminFor(r *http.Request) *transcriptsAdminView {
	if !a.isAdmin(browserPrincipal(r)) {
		return nil
	}
	return a.transcriptsAdmin(r.Context(), a.langFor(r))
}
