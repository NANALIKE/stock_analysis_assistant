package jobs

import (
	"context"
	"log/slog"
	"time"

	"stock_analysis_assistant/internal/platform"
)

// Run drains persisted jobs serially. The collection pipeline is added after
// provider admission; the bootstrap never publishes synthetic or unchecked data.
func Run(ctx context.Context, repository Repository, logger *slog.Logger) {
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
			err = repository.Fail(ctx, *job, &platform.Error{
				Code: "SOURCE_UNAVAILABLE", Message: "BaoStock collection is not enabled; complete provider admission and the generation pipeline first",
			})
			if err != nil {
				logger.Error("finish generation", "run_id", job.ID, "error", err)
			} else {
				logger.Info("generation failed", "run_id", job.ID, "attempt", job.Attempt, "code", "SOURCE_UNAVAILABLE")
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
