// Package config loads runtime settings from environment variables.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

type Config struct {
	// Addr is the listen address, e.g. ":8080" or "127.0.0.1:8080".
	Addr string
	// DatabaseURL is a lib/pq connection string (URL or key=value form).
	DatabaseURL string
	// BaseURL is the public origin used in feeds, sitemaps and OG tags,
	// without a trailing slash, e.g. "https://plurb.org".
	BaseURL string
	// Dev relaxes cookie security (no Secure flag) for plain-http localhost.
	Dev bool
	// OpenSignup lets anyone create an account (form or GitHub). When false,
	// accounts are created with `plurb createuser` only.
	OpenSignup bool
	// RedisURL enables Redis for caching, rate limits and view counting;
	// empty means in-process memory (fine for a single instance).
	RedisURL string
	// TrustProxy takes the client IP from X-Real-IP (set by our nginx).
	// Only enable behind a proxy that overwrites that header.
	TrustProxy bool
	// GitHub OAuth app credentials; GitHub login is off when either is empty.
	GitHubClientID     string
	GitHubClientSecret string
}

func (c Config) GitHubEnabled() bool { return c.GitHubClientID != "" && c.GitHubClientSecret != "" }

// Load reads PLURB_* variables, applying defaults suitable for local dev.
func Load() (Config, error) {
	c := Config{
		Addr:        env("PLURB_ADDR", "127.0.0.1:8080"),
		DatabaseURL: env("DATABASE_URL", "postgres://plurb:plurb@localhost:5432/plurb?sslmode=disable"),
		BaseURL:     strings.TrimRight(env("PLURB_BASE_URL", "http://localhost:8080"), "/"),
		Dev:         env("PLURB_DEV", "") == "1",
		OpenSignup:  env("PLURB_SIGNUP", "closed") == "open",
		RedisURL:    env("REDIS_URL", ""),
		TrustProxy:  env("PLURB_TRUST_PROXY", "") == "1",

		GitHubClientID:     env("GITHUB_CLIENT_ID", ""),
		GitHubClientSecret: env("GITHUB_CLIENT_SECRET", ""),
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Config{}, fmt.Errorf("PLURB_BASE_URL must be an absolute http(s) URL, got %q", c.BaseURL)
	}
	return c, nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}
