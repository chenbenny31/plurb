package notify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/chenbenny31/plurb/internal/db/dbtest"
)

func TestHubFanOut(t *testing.T) {
	h := NewHub(slog.New(slog.NewTextHandler(io.Discard, nil)))
	a1, cancelA1 := h.Subscribe(1)
	a2, cancelA2 := h.Subscribe(1)
	b, cancelB := h.Subscribe(2)
	defer cancelA2()
	defer cancelB()

	h.Publish(1)
	h.Publish(1) // coalesces: one pending signal is enough
	for _, ch := range []<-chan struct{}{a1, a2} {
		select {
		case <-ch:
		default:
			t.Fatal("subscriber of user 1 not signalled")
		}
	}
	select {
	case <-b:
		t.Fatal("user 2 signalled for user 1's event")
	default:
	}
	cancelA1()
	if h.Count(1) != 1 || h.Subscribers() != 2 {
		t.Fatalf("counts after cancel: user1=%d total=%d", h.Count(1), h.Subscribers())
	}
}

func TestListenDeliversPostgresNotify(t *testing.T) {
	pool, url := dbtest.NewWithURL(t)
	b := make([]byte, 4)
	rand.Read(b)
	channel := "test_" + hex.EncodeToString(b)

	h := NewHub(slog.New(slog.NewTextHandler(io.Discard, nil)))
	sig, cancel := h.Subscribe(42)
	defer cancel()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go h.Listen(ctx, url, channel)

	// LISTEN is asynchronous; keep notifying until the hub hears it.
	deadline := time.After(5 * time.Second)
	for {
		pool.Exec(`SELECT pg_notify($1, '42')`, channel)
		select {
		case <-sig:
			return
		case <-deadline:
			t.Fatal("NOTIFY never reached the subscriber")
		case <-time.After(100 * time.Millisecond):
		}
	}
}
