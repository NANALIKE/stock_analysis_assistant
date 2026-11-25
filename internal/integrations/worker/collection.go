package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"stock_analysis_assistant/internal/jobs"
	"stock_analysis_assistant/internal/marketdata"
)

func (c Client) Query(ctx context.Context, runID, asOf string, query jobs.Query, replay *marketdata.SourceResponse) (marketdata.SourceResponse, marketdata.SourceManifest, error) {
	params := map[string]any{"queries": []jobs.Query{query}}
	if replay != nil {
		params["replay"] = replay
	}
	batch, err := c.Execute(ctx, Request{SchemaVersion: SchemaVersion, RunID: runID,
		Operation: "collect", Provider: "baostock", AsOf: asOf, Params: params})
	var response marketdata.SourceResponse
	manifest := marketdata.SourceManifest{WorkerVersion: batch.Manifest.WorkerVersion,
		DependencyLockSHA256: batch.Manifest.DependencyLockSHA256, AdapterVersion: batch.Manifest.AdapterVersion}
	if err != nil {
		return response, manifest, err
	}
	if len(batch.Records) != 1 {
		return response, manifest, fmt.Errorf("expected one source response")
	}
	err = json.Unmarshal(batch.Records[0], &response)
	if err == nil && !response.Matches(query) {
		err = fmt.Errorf("source response query mismatch")
	}
	return response, manifest, err
}
