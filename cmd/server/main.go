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

	"github.com/yiwili/work-tracker-server/internal/auth"
	"github.com/yiwili/work-tracker-server/internal/config"
	"github.com/yiwili/work-tracker-server/internal/db"
	"github.com/yiwili/work-tracker-server/internal/publish"
	"github.com/yiwili/work-tracker-server/internal/server"
	"github.com/yiwili/work-tracker-server/internal/sync"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	ctx, cancelMain := context.WithCancel(context.Background())
	defer cancelMain()

	pool, err := db.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns, "")
	if err != nil {
		slog.Error("failed to open database pool", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool); err != nil {
		slog.Error("failed to run database migrations", "error", err)
		os.Exit(1)
	}

	authSvc := auth.NewService(pool, cfg)
	authHandler := auth.NewHandler(authSvc)

	syncSvc := sync.NewService(pool)
	syncHandler := sync.NewHandler(syncSvc)

	publishSvc := publish.NewService(pool, cfg, syncSvc)
	publishHandler := publish.NewHandler(publishSvc)

	modules := []server.Module{
		syncHandler,
		publishHandler,
	}

	router := server.New(server.Deps{
		Config:      cfg,
		Logger:      logger,
		Pool:        pool,
		AuthHandler: authHandler,
		Modules:     modules,
	})

	// Background sweeper
	go func() {
		ticker := time.NewTicker(cfg.SweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := publishSvc.Sweep(ctx); err != nil {
					slog.Error("publish sweeper failed", "error", err)
				}
			}
		}
	}()

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		slog.Info("server listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down server...")
	cancelMain()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("server forced to shutdown", "error", err)
	}

	slog.Info("server exited cleanly")
}
