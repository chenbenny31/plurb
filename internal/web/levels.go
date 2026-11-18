package web

import (
	"context"
	"sync"
	"time"

	"github.com/chenbenny31/plurb/internal/rep"
)

// levelsTTL is how long a computed distribution is trusted. Levels move
// slowly, so recomputing on demand every few minutes is plenty.
const levelsTTL = 10 * time.Minute

// levelCache holds the last computed level cut points. It's refreshed
// lazily: the first page view after it goes stale pays for one aggregate
// query; everyone else reads the snapshot.
type levelCache struct {
	mu     sync.RWMutex
	levels rep.Levels
	at     time.Time
	ttl    time.Duration
}

// refreshLevels recomputes the snapshot if it's stale. On a database error
// it keeps serving the old snapshot rather than failing the page.
func (s *Server) refreshLevels(ctx context.Context) {
	c := &s.lv
	c.mu.RLock()
	fresh := !c.at.IsZero() && time.Since(c.at) < c.ttl
	c.mu.RUnlock()
	if fresh {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.at.IsZero() && time.Since(c.at) < c.ttl {
		return // another request refreshed it while we waited
	}
	l, err := s.store.RepLevels(ctx)
	if err != nil {
		s.log.Warn("compute reputation levels", "err", err)
		return
	}
	c.levels, c.at = l, time.Now()
}

func (s *Server) level(reputation int) int {
	s.lv.mu.RLock()
	defer s.lv.mu.RUnlock()
	return s.lv.levels.Of(reputation)
}

func (s *Server) levelFuncs() map[string]any {
	return map[string]any{
		"level":     s.level,
		"levelName": func(reputation int) string { return rep.LevelName(s.level(reputation)) },
		"levelBand": func(reputation int) string { return rep.LevelBand(s.level(reputation)) },
	}
}
