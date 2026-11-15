package web

import (
	"net/http"
	"net/url"
)

// crossOriginGuard rejects state-changing requests that a browser marks as
// coming from another site. It follows the same algorithm as Go 1.25's
// http.CrossOriginProtection (Sec-Fetch-Site first, then Origin vs Host);
// swap to the stdlib type once go.mod targets 1.25.
//
// Requests with neither header aren't from a modern browser, so they can't
// be a CSRF vector and are allowed. SameSite=Lax session cookies are the
// second layer for old browsers.
func crossOriginGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
			if site == "same-origin" || site == "none" {
				next.ServeHTTP(w, r)
				return
			}
			http.Error(w, "cross-origin request blocked", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host {
				http.Error(w, "cross-origin request blocked", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
