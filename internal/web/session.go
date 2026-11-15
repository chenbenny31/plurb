package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/chenbenny31/plurb/internal/store"
)

const (
	sessionCookie = "plurb_session"
	sessionTTL    = 30 * 24 * time.Hour
)

type ctxKey int

const userKey ctxKey = 0

func currentUser(r *http.Request) *store.User {
	u, _ := r.Context().Value(userKey).(*store.User)
	return u
}

// loadUser attaches the signed-in user (if any) to the request context.
// Static assets skip the lookup to save a query per file.
func (s *Server) loadUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/static/") || r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		c, err := r.Cookie(sessionCookie)
		if err != nil || c.Value == "" {
			next.ServeHTTP(w, r)
			return
		}
		u, err := s.store.UserBySession(r.Context(), c.Value)
		switch {
		case err == nil:
			r = r.WithContext(context.WithValue(r.Context(), userKey, u))
		case errors.Is(err, store.ErrNotFound):
			s.clearSessionCookie(w) // expired or revoked
		default:
			s.log.Error("load session", "err", err)
		}
		next.ServeHTTP(w, r)
	})
}

// requireUser sends anonymous visitors to the login page and back afterwards.
func (s *Server) requireUser(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if currentUser(r) == nil {
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		h(w, r)
	}
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	token, expires, err := s.store.CreateSession(r.Context(), userID, sessionTTL)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   !s.cfg.Dev,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: !s.cfg.Dev, SameSite: http.SameSiteLaxMode,
	})
}

// safeNext only allows redirects to paths on this site ("/x", not "//evil").
func safeNext(next, fallback string) string {
	if strings.HasPrefix(next, "/") && !strings.HasPrefix(next, "//") && !strings.HasPrefix(next, "/\\") {
		return next
	}
	return fallback
}
