package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/chenbenny31/plurb/internal/markdown"
	"github.com/chenbenny31/plurb/internal/store"
)

// clientIP is the peer address, or nginx's X-Real-IP when configured to
// trust it (otherwise anyone could spoof the header to dodge rate limits).
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if ip := r.Header.Get("X-Real-IP"); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// allow applies a sliding-window limit and, when exceeded, answers 429 with
// Retry-After. Callers return immediately when it reports false.
func (s *Server) allow(w http.ResponseWriter, r *http.Request, key string, limit int, window time.Duration) bool {
	ok, retry, err := s.kv.Limiter.Allow(r.Context(), key, limit, window)
	if err != nil {
		s.log.Warn("rate limiter unavailable, allowing", "key", key, "err", err)
		return true
	}
	if ok {
		return true
	}
	secs := int(math.Ceil(retry.Seconds()))
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	s.render(w, r, http.StatusTooManyRequests, "error", s.newView(r, "Slow down",
		errorPage{Code: 429, Message: "Too many requests. Try again in " + strconv.Itoa(secs) + " seconds."}))
	return false
}

// Limits for write endpoints: generous for people, tight for scripts.
const (
	loginPerIP     = 20
	loginPerUser   = 10
	loginWindow    = 10 * time.Minute
	signupPerIP    = 5
	signupWindow   = time.Hour
	writesPerUser  = 60
	writesWindow   = time.Hour
	previewPerUser = 120
	previewWindow  = 10 * time.Minute
)

// ---- view counting ----

// visitor identifies a reader for view de-duplication without storing
// their IP: only a hash of IP and User-Agent ever reaches the cache.
func (s *Server) visitor(r *http.Request) string {
	sum := sha256.Sum256([]byte(s.clientIP(r) + "|" + r.UserAgent()))
	return hex.EncodeToString(sum[:12])
}

// FlushViews persists buffered view counts; main runs it on a ticker.
func (s *Server) FlushViews(ctx context.Context, counts map[int64]int64) error {
	return s.store.AddViews(ctx, counts)
}

// ---- rendered markdown cache ----

// renderPost returns a post's rendered body, cached under its id and last
// update time so an edit naturally produces a new key (no invalidation).
func (s *Server) renderPost(ctx context.Context, p *store.Post) (markdown.Rendered, error) {
	key := "md:" + strconv.FormatInt(p.ID, 10) + ":" + strconv.FormatInt(p.UpdatedAt.UnixNano(), 36)
	if b, ok, err := s.kv.Cache.Get(ctx, key); err == nil && ok {
		var out markdown.Rendered
		if json.Unmarshal(b, &out) == nil {
			return out, nil
		}
	}
	out, err := s.md.Render(p.BodyMD)
	if err != nil {
		return out, err
	}
	if b, err := json.Marshal(out); err == nil {
		if err := s.kv.Cache.Set(ctx, key, b, 24*time.Hour); err != nil {
			s.log.Warn("cache set", "err", err)
		}
	}
	return out, nil
}

// popularPosts is the "Popular" sidebar, recomputed at most every 5 minutes.
func (s *Server) popularPosts(ctx context.Context) ([]store.PostRef, error) {
	const key = "popular:v1"
	if b, ok, err := s.kv.Cache.Get(ctx, key); err == nil && ok {
		var refs []store.PostRef
		if json.Unmarshal(b, &refs) == nil {
			return refs, nil
		}
	}
	refs, err := s.store.PopularPosts(ctx, 5)
	if err != nil {
		return nil, err
	}
	if b, err := json.Marshal(refs); err == nil {
		s.kv.Cache.Set(ctx, key, b, 5*time.Minute)
	}
	return refs, nil
}
