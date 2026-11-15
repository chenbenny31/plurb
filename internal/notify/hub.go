// Package notify fans Postgres NOTIFY events out to connected browsers.
//
// Every app instance LISTENs on the same channel, so a reply written through
// any instance reaches the recipient's open tabs on every instance — no
// separate message broker needed.
package notify

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/lib/pq"
)

// Hub tracks subscribers per user. Signals carry no data: a subscriber
// that receives one re-reads its state from the database, which keeps the
// hub trivially correct across missed or coalesced events.
type Hub struct {
	log  *slog.Logger
	mu   sync.Mutex
	subs map[int64]map[chan struct{}]struct{} // user id -> subscriber channels
}

func NewHub(log *slog.Logger) *Hub {
	return &Hub{log: log, subs: map[int64]map[chan struct{}]struct{}{}}
}

// Subscribe returns a channel that receives a signal whenever userID has
// something new, and a cancel func that must be called when done.
func (h *Hub) Subscribe(userID int64) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	if h.subs[userID] == nil {
		h.subs[userID] = map[chan struct{}]struct{}{}
	}
	h.subs[userID][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs[userID], ch)
		if len(h.subs[userID]) == 0 {
			delete(h.subs, userID)
		}
		h.mu.Unlock()
	}
}

// Publish signals all of a user's subscribers without blocking: a pending
// signal already means "re-read", so extra ones can be dropped.
func (h *Hub) Publish(userID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[userID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// broadcast signals everyone, used after a listener reconnect when
// notifications may have been missed.
func (h *Hub) broadcast() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, set := range h.subs {
		for ch := range set {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
}

// Count reports how many streams a user has open.
func (h *Hub) Count(userID int64) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs[userID])
}

// Subscribers reports how many streams are open (for tests and metrics).
func (h *Hub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, set := range h.subs {
		n += len(set)
	}
	return n
}

// Listen holds a dedicated LISTEN connection until ctx ends, reconnecting
// with backoff (handled by pq.Listener) and resyncing subscribers after
// each reconnect.
func (h *Hub) Listen(ctx context.Context, connStr, channel string) error {
	l := pq.NewListener(connStr, time.Second, time.Minute, func(ev pq.ListenerEventType, err error) {
		switch ev {
		case pq.ListenerEventConnectionAttemptFailed, pq.ListenerEventDisconnected:
			h.log.Warn("notify listener", "event", ev, "err", err)
		}
	})
	defer l.Close()
	if err := l.Listen(channel); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case n := <-l.Notify:
			if n == nil { // reconnected: we may have missed events
				h.broadcast()
				continue
			}
			if id, err := strconv.ParseInt(n.Extra, 10, 64); err == nil {
				h.Publish(id)
			}
		case <-time.After(90 * time.Second):
			go l.Ping() // detect dead connections promptly
		}
	}
}
