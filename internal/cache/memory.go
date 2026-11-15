package cache

import (
	"context"
	"strconv"
	"sync"
	"time"
)

// NewMemory returns in-process implementations. They're correct for a
// single server process; use Redis once there's more than one.
func NewMemory() *Backend {
	return &Backend{
		Cache:   &memCache{items: map[string]memItem{}, max: 10_000},
		Limiter: &memLimiter{events: map[string][]time.Time{}},
		Views:   &memViews{seen: map[string]time.Time{}, pending: map[int64]int64{}},
		Name:    "memory",
	}
}

type memItem struct {
	val     []byte
	expires time.Time
}

type memCache struct {
	mu    sync.Mutex
	items map[string]memItem
	max   int
}

func (c *memCache) Get(_ context.Context, key string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[key]
	if !ok || time.Now().After(it.expires) {
		delete(c.items, key)
		return nil, false, nil
	}
	return it.val, true, nil
}

func (c *memCache) Set(_ context.Context, key string, val []byte, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.items) >= c.max {
		c.evictExpired()
		if len(c.items) >= c.max {
			c.items = map[string]memItem{} // crude but bounded
		}
	}
	c.items[key] = memItem{val: val, expires: time.Now().Add(ttl)}
	return nil
}

func (c *memCache) Delete(_ context.Context, keys ...string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, k := range keys {
		delete(c.items, k)
	}
	return nil
}

func (c *memCache) evictExpired() {
	now := time.Now()
	for k, it := range c.items {
		if now.After(it.expires) {
			delete(c.items, k)
		}
	}
}

// memLimiter keeps a timestamp log per key (sliding window log).
type memLimiter struct {
	mu     sync.Mutex
	events map[string][]time.Time
	ops    int
}

func (l *memLimiter) Allow(_ context.Context, key string, limit int, window time.Duration) (bool, time.Duration, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-window)
	ev := l.events[key]
	i := 0
	for i < len(ev) && !ev[i].After(cutoff) {
		i++
	}
	ev = ev[i:]
	if len(ev) >= limit {
		l.events[key] = ev
		return false, ev[0].Sub(cutoff), nil
	}
	l.events[key] = append(ev, now)

	// Occasionally drop keys whose windows have fully passed.
	if l.ops++; l.ops%1000 == 0 {
		for k, e := range l.events {
			if len(e) == 0 || now.Sub(e[len(e)-1]) > time.Hour {
				delete(l.events, k)
			}
		}
	}
	return true, 0, nil
}

type memViews struct {
	mu      sync.Mutex
	seen    map[string]time.Time // post|visitor -> first view
	pending map[int64]int64      // post id -> unflushed views
}

func (v *memViews) Hit(_ context.Context, postID int64, visitor string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	key := visitorKey(postID, visitor)
	if t, ok := v.seen[key]; ok && time.Since(t) < 24*time.Hour {
		return nil
	}
	if len(v.seen) > 200_000 {
		v.seen = map[string]time.Time{}
	}
	v.seen[key] = time.Now()
	v.pending[postID]++
	return nil
}

func (v *memViews) take() map[int64]int64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := v.pending
	v.pending = map[int64]int64{}
	return out
}

func (v *memViews) Run(ctx context.Context, interval time.Duration, flush FlushFunc) {
	runFlusher(ctx, interval, func(fctx context.Context) error {
		counts := v.take()
		if len(counts) == 0 {
			return nil
		}
		if err := flush(fctx, counts); err != nil {
			// Put them back so the next tick retries.
			v.mu.Lock()
			for id, n := range counts {
				v.pending[id] += n
			}
			v.mu.Unlock()
			return err
		}
		return nil
	})
}

func visitorKey(postID int64, visitor string) string {
	return strconv.FormatInt(postID, 10) + "|" + visitor
}

// runFlusher calls flush every interval and once more on shutdown, with a
// fresh context so the final flush isn't cancelled along with ctx.
func runFlusher(ctx context.Context, interval time.Duration, flush func(context.Context) error) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			flush(fctx)
			cancel()
			return
		case <-t.C:
			flush(ctx)
		}
	}
}
