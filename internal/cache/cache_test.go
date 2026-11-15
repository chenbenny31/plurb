package cache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"sync"
	"testing"
	"time"
)

// backends returns memory always, plus Redis when PLURB_TEST_REDIS_URL is
// set, so both implementations pass the same tests.
func backends(t *testing.T) map[string]*Backend {
	out := map[string]*Backend{"memory": NewMemory()}
	if url := os.Getenv("PLURB_TEST_REDIS_URL"); url != "" {
		b := make([]byte, 4)
		rand.Read(b)
		be, rdb, err := NewRedis(context.Background(), url, "test:"+hex.EncodeToString(b)+":")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { rdb.Close() })
		out["redis"] = be
	}
	return out
}

func TestCache(t *testing.T) {
	for name, be := range backends(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			if _, ok, _ := be.Cache.Get(ctx, "k"); ok {
				t.Fatal("hit on empty cache")
			}
			be.Cache.Set(ctx, "k", []byte("v"), time.Minute)
			if v, ok, err := be.Cache.Get(ctx, "k"); !ok || string(v) != "v" || err != nil {
				t.Fatalf("get: %q %v %v", v, ok, err)
			}
			be.Cache.Delete(ctx, "k")
			if _, ok, _ := be.Cache.Get(ctx, "k"); ok {
				t.Fatal("hit after delete")
			}
			be.Cache.Set(ctx, "short", []byte("v"), 50*time.Millisecond)
			time.Sleep(120 * time.Millisecond)
			if _, ok, _ := be.Cache.Get(ctx, "short"); ok {
				t.Fatal("hit after ttl")
			}
		})
	}
}

func TestLimiterSlidingWindow(t *testing.T) {
	for name, be := range backends(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			window := 300 * time.Millisecond
			for i := 0; i < 3; i++ {
				if ok, _, err := be.Limiter.Allow(ctx, "ip:1", 3, window); !ok || err != nil {
					t.Fatalf("request %d denied (%v)", i, err)
				}
			}
			ok, retry, _ := be.Limiter.Allow(ctx, "ip:1", 3, window)
			if ok || retry <= 0 || retry > window {
				t.Fatalf("4th request: ok=%v retry=%v", ok, retry)
			}
			if ok, _, _ := be.Limiter.Allow(ctx, "ip:2", 3, window); !ok {
				t.Fatal("other key affected")
			}
			time.Sleep(retry + 20*time.Millisecond)
			if ok, _, _ := be.Limiter.Allow(ctx, "ip:1", 3, window); !ok {
				t.Fatal("still denied after the window slid")
			}
		})
	}
}

func TestViewsDedupeAndFlush(t *testing.T) {
	for name, be := range backends(t) {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			var mu sync.Mutex
			total := map[int64]int64{}
			flushed := make(chan struct{}, 10)
			done := make(chan struct{})
			go func() {
				be.Views.Run(ctx, 30*time.Millisecond, func(_ context.Context, c map[int64]int64) error {
					mu.Lock()
					for id, n := range c {
						total[id] += n
					}
					mu.Unlock()
					flushed <- struct{}{}
					return nil
				})
				close(done)
			}()

			be.Views.Hit(ctx, 1, "alice")
			be.Views.Hit(ctx, 1, "alice") // same reader, same day
			be.Views.Hit(ctx, 1, "bob")
			be.Views.Hit(ctx, 2, "alice")
			<-flushed
			cancel()
			<-done

			mu.Lock()
			defer mu.Unlock()
			if total[1] != 2 || total[2] != 1 {
				t.Fatalf("flushed counts = %v, want map[1:2 2:1]", total)
			}
		})
	}
}
