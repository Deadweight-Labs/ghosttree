// Package cache provides a small TTL cache and an HTTP response cache built on
// top of it.
package cache

import (
	"sync"
	"time"
)

type entry[V any] struct {
	value   V
	expires time.Time
}

// TTL is a concurrency-safe map whose entries expire after a fixed duration.
type TTL[K comparable, V any] struct {
	mu   sync.Mutex
	ttl  time.Duration
	data map[K]entry[V]
	now  func() time.Time
	max  int
}

// NewTTL creates a cache. max bounds the number of entries; 0 means 1024.
func NewTTL[K comparable, V any](ttl time.Duration, max int) *TTL[K, V] {
	if max <= 0 {
		max = 1024
	}
	return &TTL[K, V]{ttl: ttl, data: map[K]entry[V]{}, now: time.Now, max: max}
}

// Get returns the value for k if present and not expired.
func (c *TTL[K, V]) Get(k K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.data[k]
	if !ok || c.now().After(e.expires) {
		var zero V
		if ok {
			delete(c.data, k)
		}
		return zero, false
	}
	return e.value, true
}

// Set stores v under k.
func (c *TTL[K, V]) Set(k K, v V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.data) >= c.max {
		c.evictLocked()
	}
	c.data[k] = entry[V]{value: v, expires: c.now().Add(c.ttl)}
}

// Purge drops all entries.
func (c *TTL[K, V]) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data = map[K]entry[V]{}
}

// Len reports the number of entries, including expired ones not yet evicted.
func (c *TTL[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.data)
}

func (c *TTL[K, V]) evictLocked() {
	now := c.now()
	for k, e := range c.data {
		if now.After(e.expires) {
			delete(c.data, k)
		}
	}
	if len(c.data) < c.max {
		return
	}
	// Still full: drop the entry closest to expiry.
	var victim K
	var first = true
	var soonest time.Time
	for k, e := range c.data {
		if first || e.expires.Before(soonest) {
			victim, soonest, first = k, e.expires, false
		}
	}
	delete(c.data, victim)
}
