// Package cache provides a key-value cache, a rate limiter and a buffered
// view counter, each backed by Redis in production or by process memory
// when no Redis is configured (dev, tests, single-instance deploys).
package cache

import (
	"context"
	"time"
)

// Cache stores opaque bytes with a TTL. A miss is (nil, false, nil).
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
	Delete(ctx context.Context, keys ...string) error
}

// Limiter enforces "at most limit events per window" per key using a
// sliding window. When denied, retryAfter says when the oldest event in the
// window expires.
type Limiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (ok bool, retryAfter time.Duration, err error)
}

// Views counts unique post views. Hit records a view by visitor (a hash
// that identifies the reader for a day); counts are buffered and handed to
// the flush function by Run.
type Views interface {
	Hit(ctx context.Context, postID int64, visitor string) error
	// Run flushes buffered counts every interval until ctx ends, then once more.
	Run(ctx context.Context, interval time.Duration, flush FlushFunc)
}

// FlushFunc persists accumulated view counts (post id -> new views).
type FlushFunc func(ctx context.Context, counts map[int64]int64) error

// Backend bundles the three so callers pick an implementation once.
type Backend struct {
	Cache   Cache
	Limiter Limiter
	Views   Views
	Name    string // "redis" or "memory", for logs
}
