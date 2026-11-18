// Command plurb runs the plurb web server and its admin subcommands.
package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/chenbenny31/plurb/internal/auth"
	"github.com/chenbenny31/plurb/internal/cache"
	"github.com/chenbenny31/plurb/internal/config"
	"github.com/chenbenny31/plurb/internal/db"
	"github.com/chenbenny31/plurb/internal/legacy"
	"github.com/chenbenny31/plurb/internal/notify"
	"github.com/chenbenny31/plurb/internal/store"
	"github.com/chenbenny31/plurb/internal/web"
)

const usage = `usage: plurb <command>

commands:
  serve    apply pending migrations, then run the web server (default)
  migrate [-status]
           apply pending migrations and exit; -status lists applied and
           pending migrations without changing anything
  createuser [-admin] [-name "Display Name"] <username>
           create an account; the password is read from PLURB_PASSWORD
           or the first line of stdin
  passwd <username>
           set a user's password (same input rules as createuser)
  rep-rebuild
           recompute cached reputation from the append-only ledger
  import-legacy -from <old-database-url>
           copy users, posts, tags and series from the old Java plurb
           database into this (empty) one`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "plurb:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cmd := "serve"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		return serve(cfg, log)
	case "migrate":
		fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
		status := fs.Bool("status", false, "list migrations without applying them")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *status {
			return migrateStatus(cfg)
		}
		pool, err := openAndMigrate(context.Background(), cfg, log)
		if err != nil {
			return err
		}
		return pool.Close()
	case "createuser":
		return createUser(cfg, log, args)
	case "passwd":
		return setPassword(cfg, log, args)
	case "import-legacy":
		return importLegacy(cfg, log, args)
	case "rep-rebuild":
		ctx := context.Background()
		pool, err := openAndMigrate(ctx, cfg, log)
		if err != nil {
			return err
		}
		defer pool.Close()
		n, err := store.New(pool).RebuildReputation(ctx)
		if err != nil {
			return err
		}
		log.Info("reputation rebuilt from ledger", "users_corrected", n)
		return nil
	case "help", "-h", "--help":
		fmt.Println(usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n%s", cmd, usage)
	}
}

func openAndMigrate(ctx context.Context, cfg config.Config, log *slog.Logger) (*sql.DB, error) {
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	applied, err := db.Migrate(ctx, pool)
	if err != nil {
		pool.Close()
		return nil, err
	}
	for _, name := range applied {
		log.Info("applied migration", "name", name)
	}
	// After a binary rollback the schema can be ahead of this release; that's
	// fine as long as migrations stay backward compatible (see internal/db).
	if st, err := db.Status(ctx, pool); err == nil {
		for _, m := range st {
			if m.Unknown {
				log.Warn("database has a migration this binary doesn't know (rolled back?)", "name", m.Name)
			}
		}
	}
	return pool, nil
}

func migrateStatus(cfg config.Config) error {
	ctx := context.Background()
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	st, err := db.Status(ctx, pool)
	if err != nil {
		return err
	}
	pending := 0
	for _, m := range st {
		state := "applied " + m.AppliedAt.Format("2006-01-02 15:04")
		switch {
		case m.Unknown:
			state += "  (newer than this binary)"
		case m.Edited:
			state += "  EDITED SINCE APPLIED"
		case m.AppliedAt.IsZero():
			state = "pending"
			pending++
		}
		fmt.Printf("%-40s %s\n", m.Name, state)
	}
	fmt.Printf("%d pending\n", pending)
	return nil
}

func serve(cfg config.Config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := openAndMigrate(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	kv := cache.NewMemory()
	if cfg.RedisURL != "" {
		be, rdb, err := cache.NewRedis(ctx, cfg.RedisURL, "plurb:")
		if err != nil {
			return fmt.Errorf("redis: %w", err)
		}
		defer rdb.Close()
		kv = be
	}
	log.Info("cache backend", "kind", kv.Name)

	// One LISTEN connection per instance feeds the in-process SSE hub.
	hub := notify.NewHub(log)
	go func() {
		if err := hub.Listen(ctx, cfg.DatabaseURL, store.DefaultNotifyChannel); err != nil {
			log.Error("notify listener stopped", "err", err)
		}
	}()

	app, err := web.NewServer(cfg, log, pool, kv, hub)
	if err != nil {
		return err
	}
	go purgeSessions(ctx, store.New(pool), log)

	// Flush buffered view counts every 30s, and once more on shutdown.
	viewsDone := make(chan struct{})
	go func() {
		defer close(viewsDone)
		kv.Views.Run(ctx, 30*time.Second, func(c context.Context, counts map[int64]int64) error {
			err := app.FlushViews(c, counts)
			if err != nil {
				log.Error("flush views", "err", err)
			}
			return err
		})
	}()
	defer func() { <-viewsDone }()

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	srv.RegisterOnShutdown(app.CloseStreams) // don't let open SSE streams stall shutdown

	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr, "base_url", cfg.BaseURL)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// purgeSessions deletes expired sessions once an hour until ctx ends.
func purgeSessions(ctx context.Context, st *store.Store, log *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if n, err := st.PurgeExpiredSessions(ctx); err != nil && ctx.Err() == nil {
			log.Error("purge sessions", "err", err)
		} else if n > 0 {
			log.Info("purged sessions", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func createUser(cfg config.Config, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("createuser", flag.ContinueOnError)
	admin := fs.Bool("admin", false, "grant the admin role")
	name := fs.String("name", "", "display name (defaults to the username)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: plurb createuser [-admin] [-name NAME] <username>")
	}
	username := strings.ToLower(fs.Arg(0))
	if !store.ValidUsername(username) {
		return fmt.Errorf("username %q must match %s and not be reserved", username, store.UsernamePattern)
	}

	hash, err := readPasswordHash()
	if err != nil {
		return err
	}

	ctx := context.Background()
	pool, err := openAndMigrate(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	display := *name
	if display == "" {
		display = username
	}
	role := "member"
	if *admin {
		role = "admin"
	}
	u, err := store.New(pool).CreateUser(ctx, username, display, hash, role)
	if err != nil {
		return err
	}
	log.Info("created user", "id", u.ID, "username", u.Username, "role", u.Role)
	return nil
}

// readPasswordHash reads a password from PLURB_PASSWORD or stdin and hashes it.
func readPasswordHash() (string, error) {
	password := os.Getenv("PLURB_PASSWORD")
	if password == "" {
		fmt.Fprint(os.Stderr, "password: ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", fmt.Errorf("read password: %w", err)
		}
		password = strings.TrimRight(line, "\r\n")
	}
	return auth.Hash(password)
}

func setPassword(cfg config.Config, log *slog.Logger, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: plurb passwd <username>")
	}
	hash, err := readPasswordHash()
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := openAndMigrate(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()
	st := store.New(pool)
	u, err := st.UserByUsername(ctx, strings.ToLower(args[0]))
	if err != nil {
		return fmt.Errorf("user %q: %w", args[0], err)
	}
	if err := st.UpdatePassword(ctx, u.ID, hash); err != nil {
		return err
	}
	if err := st.DeleteUserSessions(ctx, u.ID); err != nil {
		return err
	}
	log.Info("password updated; existing sessions signed out", "username", u.Username)
	return nil
}

func importLegacy(cfg config.Config, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("import-legacy", flag.ContinueOnError)
	from := fs.String("from", "", "connection URL of the old Java plurb database")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *from == "" {
		return errors.New("usage: plurb import-legacy -from <old-database-url>")
	}
	ctx := context.Background()
	old, err := db.Open(ctx, *from)
	if err != nil {
		return fmt.Errorf("old database: %w", err)
	}
	defer old.Close()
	pool, err := openAndMigrate(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	rep, err := legacy.Import(ctx, old, pool)
	if err != nil {
		return err
	}
	for _, w := range rep.Warnings {
		log.Warn(w)
	}
	log.Info("import done", "users", rep.Users, "posts", rep.Posts, "tags", rep.Tags, "series", rep.Series)
	log.Info("imported accounts have no password yet: run `plurb passwd <username>`")
	return nil
}
