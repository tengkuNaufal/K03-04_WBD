// Command server runs the API and serves the SPA.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"dbmp/internal/app"
	"dbmp/internal/auth"
	"dbmp/internal/authz"
	"dbmp/internal/obs"
	"dbmp/internal/session"
	"dbmp/internal/store"
	"dbmp/internal/web"
	webui "dbmp/web"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "event", "server.fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	d, err := app.Open(ctx, "dbmp-server")
	if err != nil {
		return err
	}
	defer d.Close()

	sessions := session.NewManager(d.Store.Sessions(), session.DefaultConfig())
	if err := bootstrapAdmin(ctx, d.Store); err != nil {
		return err
	}
	obs.RegisterJobCounts(d.Store.JobCounts)

	srv := &web.Server{
		Store:        d.Store,
		Sessions:     sessions,
		Auth:         auth.NewService(d.Store, sessions),
		Policy:       authz.NewPolicy(d.Store),
		Targets:      d.Targets,
		Box:          d.Box,
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
	hs := &http.Server{Addr: addr, Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutdownCtx)
	}()
	slog.Info("listening", "event", "server.start", "addr", addr)
	if err := hs.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
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
	slog.Info("created first admin", "event", "admin.bootstrap", "actor", name)
	return err
}
