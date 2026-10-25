package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"dbmp/internal/auth"
	"dbmp/internal/authz"
	"dbmp/internal/secret"
	"dbmp/internal/session"
	"dbmp/internal/store"
	"dbmp/internal/targetdb"
	"dbmp/internal/web"
	webui "dbmp/web"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()
	if os.Getenv("DATABASE_URL") == "" {
		return errors.New("DATABASE_URL is required")
	}
	box, err := secret.NewBox([]byte(os.Getenv("APP_SECRET_KEY")))
	if err != nil {
		return errors.New("APP_SECRET_KEY is required (at least 16 characters)")
	}
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := store.Migrate(ctx, pool); err != nil {
		return err
	}

	st := store.New(pool)
	sessions := session.NewManager(st.Sessions(), session.DefaultConfig())
	targets := targetdb.NewManager(st, box)
	defer targets.Close()
	if err := bootstrapAdmin(ctx, st); err != nil {
		return err
	}

	srv := &web.Server{
		Store:        st,
		Sessions:     sessions,
		Auth:         auth.NewService(st, sessions),
		Policy:       authz.NewPolicy(st),
		Targets:      targets,
		Box:          box,
		Assets:       webui.FS,
		CookieSecure: os.Getenv("COOKIE_SECURE") == "true",
	}
	if err := srv.Init(); err != nil {
		return err
	}

	go func() {
		for range time.Tick(10 * time.Minute) {
			_, _ = sessions.Cleanup(ctx)
		}
	}()

	addr := os.Getenv("APP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	slog.Info("listening", "addr", addr)
	return (&http.Server{Addr: addr, Handler: srv, ReadHeaderTimeout: 10 * time.Second}).ListenAndServe()
}

func bootstrapAdmin(ctx context.Context, st *store.Store) error {
	n, err := st.CountUsers(ctx)
	name, pw := auth.NormalizeUsername(os.Getenv("ADMIN_USERNAME")), os.Getenv("ADMIN_PASSWORD")
	if err != nil || n > 0 || name == "" || pw == "" {
		return err
	}
	if err := auth.ValidateUsername(name); err != nil {
		return err
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	_, err = st.CreateUser(ctx, name, hash, true)
	slog.Info("created first admin", "username", name)
	return err
}
