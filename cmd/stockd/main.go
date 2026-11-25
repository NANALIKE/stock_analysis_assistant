package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"stock_analysis_assistant/internal/delivery"
	"stock_analysis_assistant/internal/integrations/persistence"
	"stock_analysis_assistant/internal/integrations/worker"
	"stock_analysis_assistant/internal/jobs"
	"stock_analysis_assistant/internal/marketdata"
	"stock_analysis_assistant/internal/platform/artifacts"
	"stock_analysis_assistant/internal/platform/config"
	"stock_analysis_assistant/internal/platform/database"
	"stock_analysis_assistant/internal/platform/observability"
)

func main() {
	logger := observability.NewLogger()
	configPath := flag.String("config", "configs/config.example.yaml", "YAML configuration")
	flag.Parse()
	c, err := config.Load(*configPath)
	if err != nil {
		logger.Error("load config", "error", err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	db, err := database.Open(ctx, c.Storage.Database)
	if err != nil {
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		logger.Error("migrate", "error", err)
		os.Exit(1)
	}
	for _, path := range []string{c.Storage.RawDir, c.Storage.ArtifactsDir, c.Storage.BackupDir} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			logger.Error("create storage", "error", err)
			os.Exit(1)
		}
	}
	securities := persistence.Securities{DB: db}
	repository := persistence.Jobs{DB: db}
	if err := repository.Recover(ctx); err != nil {
		logger.Error("recover jobs", "error", err)
		os.Exit(1)
	}
	api := delivery.API{Jobs: repository, Securities: securities,
		MarketData: persistence.MarketData{DB: db, Securities: securities}, Provider: c.MarketData.Provider, Logger: logger}
	client := worker.Client{Python: c.Worker.Python, Directory: c.Worker.Directory, Timeout: c.Worker.Timeout, MaxOutputBytes: c.Worker.MaxOutputBytes}
	if _, err := client.Execute(ctx, worker.Request{SchemaVersion: 1, RunID: "startup", Operation: "protocol_check", Provider: c.MarketData.Provider, AsOf: time.Now().Format(time.DateOnly), Params: map[string]any{"check_dependencies": true}}); err != nil {
		logger.Warn("worker protocol unavailable; queries remain available", "error", err)
	}
	executor := marketdata.Runner{Store: persistence.Collection{DB: db, Raw: artifacts.Store{Root: c.Storage.RawDir}, Reports: artifacts.Store{Root: c.Storage.ArtifactsDir}},
		Source: client, Interval: c.MarketData.RequestInterval, MaxAttempts: c.MarketData.MaxQueryAttempts, MinimumCoverage: c.MarketData.MinimumCoverage}
	server := &http.Server{Addr: c.Server.Address, Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: time.Minute}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		jobs.Run(ctx, repository, logger, executor)
	}()
	go func() {
		<-ctx.Done()
		shutdown, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if err := server.Shutdown(shutdown); err != nil {
			logger.Error("http shutdown", "error", err)
		}
	}()
	logger.Info("stockd listening", "address", c.Server.Address, "provider", c.MarketData.Provider, "collection_enabled", true)
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		logger.Error("serve", "error", err)
		cancel()
		wg.Wait()
		os.Exit(1)
	}
	wg.Wait()
}
