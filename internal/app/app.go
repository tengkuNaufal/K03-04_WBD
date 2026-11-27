package app

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"dbmp/internal/obs"
	"dbmp/internal/secret"
	"dbmp/internal/store"
	"dbmp/internal/targetdb"
)

type Deps struct {
	Pool    *pgxpool.Pool
	Store   *store.Store
	Box     *secret.Box
	Targets *targetdb.Manager
	close   []func()
}

func (d *Deps) Close() {
	for i := len(d.close) - 1; i >= 0; i-- {
		d.close[i]()
	}
}

func Open(ctx context.Context, service string) (*Deps, error) {
	shutdown, err := obs.Init(ctx, service)
	if err != nil {
		return nil, err
	}
	d := &Deps{close: []func(){func() { _ = shutdown(context.Background()) }}}
	fail := func(err error) (*Deps, error) { d.Close(); return nil, err }

	if os.Getenv("DATABASE_URL") == "" {
		return fail(errors.New("DATABASE_URL is required"))
	}
	if d.Box, err = secret.NewBox([]byte(os.Getenv("APP_SECRET_KEY"))); err != nil {
		return fail(errors.New("APP_SECRET_KEY is required (at least 16 characters)"))
	}
	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		return fail(err)
	}
	cfg.ConnConfig.Tracer = &obs.QueryTracer{Pool: "platform", Level: slog.LevelDebug}
	if d.Pool, err = pgxpool.NewWithConfig(ctx, cfg); err != nil {
		return fail(err)
	}
	d.close = append(d.close, d.Pool.Close)
	if err := store.Migrate(ctx, d.Pool); err != nil {
		return fail(err)
	}
	d.Store = store.New(d.Pool)
	d.Targets = targetdb.NewManager(d.Store, d.Box)
	d.close = append(d.close, d.Targets.Close)
	return d, nil
}
