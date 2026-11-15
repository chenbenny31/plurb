package cache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// NewRedis connects to url (e.g. redis://localhost:6379/0) and returns
// Redis-backed implementations. Keys are namespaced under prefix.
func NewRedis(ctx context.Context, url, prefix string) (*Backend, *redis.Client, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, nil, err
	}
	rdb := redis.NewClient(opt)
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := rdb.Ping(pctx).Err(); err != nil {
		rdb.Close()
		return nil, nil, err
	}
	return &Backend{
		Cache:   &redisCache{rdb: rdb, prefix: prefix + "c:"},
		Limiter: &redisLimiter{rdb: rdb, prefix: prefix + "rl:"},
		Views:   &redisViews{rdb: rdb, prefix: prefix + "v:"},
		Name:    "redis",
	}, rdb, nil
}

type redisCache struct {
	rdb    *redis.Client
	prefix string
}

func (c *redisCache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	b, err := c.rdb.Get(ctx, c.prefix+key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	return b, err == nil, err
}

func (c *redisCache) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	return c.rdb.Set(ctx, c.prefix+key, val, ttl).Err()
}

func (c *redisCache) Delete(ctx context.Context, keys ...string) error {
	full := make([]string, len(keys))
	for i, k := range keys {
		full[i] = c.prefix + k
	}
	return c.rdb.Del(ctx, full...).Err()
}

// slidingWindow atomically trims events older than the window, and either
// records this event or reports how long until the oldest one expires.
// KEYS[1] = zset; ARGV = now_ms, window_ms, limit, member.
var slidingWindow = redis.NewScript(`
local key, now, window, limit = KEYS[1], tonumber(ARGV[1]), tonumber(ARGV[2]), tonumber(ARGV[3])
redis.call('ZREMRANGEBYSCORE', key, '-inf', now - window)
if redis.call('ZCARD', key) >= limit then
  local oldest = redis.call('ZRANGE', key, 0, 0, 'WITHSCORES')
  return tonumber(oldest[2]) + window - now
end
redis.call('ZADD', key, now, ARGV[4])
redis.call('PEXPIRE', key, window)
return -1
`)

type redisLimiter struct {
	rdb    *redis.Client
	prefix string
}

func (l *redisLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, time.Duration, error) {
	member := make([]byte, 8)
	rand.Read(member)
	now := time.Now().UnixMilli()
	wait, err := slidingWindow.Run(ctx, l.rdb, []string{l.prefix + key},
		now, window.Milliseconds(), limit, strconv.FormatInt(now, 10)+"-"+hex.EncodeToString(member)).Int64()
	if err != nil {
		return true, 0, err // fail open: a Redis outage shouldn't lock users out
	}
	if wait < 0 {
		return true, 0, nil
	}
	return false, time.Duration(wait) * time.Millisecond, nil
}

// redisViews dedupes visitors with SET NX + TTL, accumulates counts in a
// hash, and drains it with RENAME so increments arriving mid-flush land in
// a fresh hash instead of being lost.
type redisViews struct {
	rdb    *redis.Client
	prefix string
}

func (v *redisViews) Hit(ctx context.Context, postID int64, visitor string) error {
	first, err := v.rdb.SetNX(ctx, v.prefix+"seen:"+visitorKey(postID, visitor), 1, 24*time.Hour).Result()
	if err != nil || !first {
		return err
	}
	return v.rdb.HIncrBy(ctx, v.prefix+"pending", strconv.FormatInt(postID, 10), 1).Err()
}

func (v *redisViews) Run(ctx context.Context, interval time.Duration, flush FlushFunc) {
	pending, flushing, lock := v.prefix+"pending", v.prefix+"flushing", v.prefix+"flush-lock"
	runFlusher(ctx, interval, func(fctx context.Context) error {
		// One flusher at a time across all app instances.
		got, err := v.rdb.SetNX(fctx, lock, 1, 30*time.Second).Result()
		if err != nil || !got {
			return err
		}
		defer v.rdb.Del(context.Background(), lock)

		// A leftover "flushing" hash means the last flush failed: retry it
		// before taking new counts.
		exists, err := v.rdb.Exists(fctx, flushing).Result()
		if err != nil {
			return err
		}
		if exists == 0 {
			if err := v.rdb.Rename(fctx, pending, flushing).Err(); err != nil {
				if err.Error() == "ERR no such key" {
					return nil // nothing to flush
				}
				return err
			}
		}
		raw, err := v.rdb.HGetAll(fctx, flushing).Result()
		if err != nil {
			return err
		}
		counts := make(map[int64]int64, len(raw))
		for k, val := range raw {
			id, err1 := strconv.ParseInt(k, 10, 64)
			n, err2 := strconv.ParseInt(val, 10, 64)
			if err1 == nil && err2 == nil {
				counts[id] = n
			}
		}
		if err := flush(fctx, counts); err != nil {
			return err
		}
		return v.rdb.Del(fctx, flushing).Err()
	})
}
