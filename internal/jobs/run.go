package jobs

import (
	"context"
	"log/slog"
	"time"

	"stock_analysis_assistant/internal/platform"
)

// Run drains persisted jobs with exactly one serial executor.
func Run(ctx context.Context, repository Repository, logger *slog.Logger, executor Executor) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		job, err := repository.Claim(ctx)
		if err != nil {
			logger.Error("claim generation", "error", err)
		} else if job != nil {
			err = executor.Execute(ctx, *job)
			if err != nil {
				failure := &platform.Error{Code: "INTERNAL_ERROR", Message: "collection failed"}
				if typed, ok := err.(*platform.Error); ok {
					failure = typed
				}
				// A cancelled process still records a terminal attempt; checkpoints survive.
				finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				if failErr := repository.Fail(finishCtx, *job, failure); failErr != nil {
					logger.Error("finish generation", "run_id", job.ID, "error", failErr)
				}
				cancel()
				logger.Error("generation failed", "run_id", job.ID, "attempt", job.Attempt, "error", err)
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
