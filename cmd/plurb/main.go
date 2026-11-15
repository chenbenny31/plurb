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
	"github.com/chenbenny31/plurb/internal/config"
	"github.com/chenbenny31/plurb/internal/db"
	"github.com/chenbenny31/plurb/internal/store"
	"github.com/chenbenny31/plurb/internal/web"
)

const usage = `usage: plurb <command>

commands:
  serve    apply pending migrations, then run the web server (default)
  migrate  apply pending migrations and exit
  createuser [-admin] [-name "Display Name"] <username>
           create an account; the password is read from PLURB_PASSWORD
           or the first line of stdin`

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
		pool, err := openAndMigrate(context.Background(), cfg, log)
		if err != nil {
			return err
		}
		return pool.Close()
	case "createuser":
		return createUser(cfg, log, args)
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
	return pool, nil
}

func serve(cfg config.Config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := openAndMigrate(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	app, err := web.NewServer(cfg, log, pool)
	if err != nil {
		return err
	}
	go purgeSessions(ctx, store.New(pool), log)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

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

	password := os.Getenv("PLURB_PASSWORD")
	if password == "" {
		fmt.Fprint(os.Stderr, "password: ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return fmt.Errorf("read password: %w", err)
		}
		password = strings.TrimRight(line, "\r\n")
	}
	hash, err := auth.Hash(password)
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
