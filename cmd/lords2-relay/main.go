// Command lords2-relay runs the Lords2HD multiplayer lobby (HTTP) and relay (TCP)
// in one process. See docs/architecture.md and docs/protocol.md.
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

	"github.com/gqzomer/lords2-relay/internal/config"
	"github.com/gqzomer/lords2-relay/internal/lobby"
	"github.com/gqzomer/lords2-relay/internal/relay"
)

func main() {
	cfg := config.FromEnv()
	var level slog.Level
	levelErr := level.UnmarshalText([]byte(cfg.LogLevel))
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	if levelErr != nil {
		log.Warn("bad LOG_LEVEL, using info", "got", cfg.LogLevel)
	}

	// ctx is cancelled on Ctrl+C or when Docker stops the container (SIGTERM).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rooms := lobby.NewRegistry()

	lobbySrv := &http.Server{
		Addr:              cfg.LobbyAddr,
		Handler:           lobby.NewHandler(rooms, log),
		ReadHeaderTimeout: 5 * time.Second,
	}
	relaySrv := relay.New(cfg.RelayAddr, rooms, log)

	go func() {
		log.Info("lobby listening", "addr", cfg.LobbyAddr)
		if err := lobbySrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("lobby stopped", "err", err)
			stop()
		}
	}()

	go func() {
		log.Info("relay listening", "addr", cfg.RelayAddr)
		if err := relaySrv.ListenAndServe(ctx); err != nil {
			log.Error("relay stopped", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = lobbySrv.Shutdown(shutdownCtx)
}
