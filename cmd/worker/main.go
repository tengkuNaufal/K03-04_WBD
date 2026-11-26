// Command worker runs queued migration jobs. Start as many as needed: the
// queue (SKIP LOCKED) and the per-database advisory lock coordinate them.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"dbmp/internal/app"
	"dbmp/internal/worker"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	d, err := app.Open(ctx, "dbmp-worker")
	if err != nil {
		slog.Error("fatal", "event", "worker.fatal", "error", err)
		os.Exit(1)
	}
	defer d.Close()

	host, _ := os.Hostname()
	w := &worker.Worker{Store: d.Store, Targets: d.Targets, ID: fmt.Sprintf("%s-%d", host, os.Getpid())}
	w.Run(ctx) // returns after the current job is recorded when a signal arrives
}
