package store

import (
	"context"
	"sync"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
)

// queryVectors joins the embeddings of the same query text that are asked for at
// the same time. One search of the web page asks for the vector once per kind, at
// the same moment; the first asks the embedding server, the others wait for that
// answer instead of asking again. Nothing is kept once the answer is there: the
// next search asks anew, so a server that went away is noticed at once.
type queryVectors struct {
	mu      sync.Mutex
	pending map[string]*queryVectorCall
}

type queryVectorCall struct {
	done chan struct{}
	vec  []float32
	err  error
}

// queryVectorTimeout is how long one embedding of a query may take.
func queryVectorTimeout(cfg *EmbedSearchConfig) time.Duration {
	if cfg.Timeout > 0 {
		return cfg.Timeout
	}
	return embed.DefaultQueryTimeout
}

// vector returns the vector of q under the embedder of cfg. The embedding itself
// runs with the timeout of cfg and not with ctx: a caller that gives up only stops
// waiting, and the others that wait for the same text still get the answer.
func (c *queryVectors) vector(ctx context.Context, cfg *EmbedSearchConfig, q string) ([]float32, error) {
	key := cfg.Embedder.Model() + "\x00" + cfg.Embedder.PrefixVersion() + "\x00" + q
	c.mu.Lock()
	if c.pending == nil {
		c.pending = map[string]*queryVectorCall{}
	}
	call, ok := c.pending[key]
	if !ok {
		call = &queryVectorCall{done: make(chan struct{})}
		c.pending[key] = call
		go c.run(cfg, key, q, call)
	}
	c.mu.Unlock()
	select {
	case <-call.done:
		return call.vec, call.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *queryVectors) run(cfg *EmbedSearchConfig, key, q string, call *queryVectorCall) {
	ctx, cancel := context.WithTimeout(context.Background(), queryVectorTimeout(cfg))
	call.vec, call.err = cfg.Embedder.Query(ctx, q)
	cancel()
	c.mu.Lock()
	if c.pending[key] == call {
		delete(c.pending, key)
	}
	c.mu.Unlock()
	close(call.done)
}

// reset forgets the calls under way; another embedder may answer differently.
func (c *queryVectors) reset() {
	c.mu.Lock()
	c.pending = nil
	c.mu.Unlock()
}
