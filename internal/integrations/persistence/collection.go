package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"stock_analysis_assistant/internal/jobs"
	"stock_analysis_assistant/internal/marketdata"
	"stock_analysis_assistant/internal/platform"
	"stock_analysis_assistant/internal/platform/artifacts"
)

type Collection struct {
	DB      *sql.DB
	Raw     artifacts.Store
	Reports artifacts.Store
}

func timestamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (r Collection) Begin(ctx context.Context, job jobs.Job) (jobs.CollectionContext, string, error) {
	var frozen jobs.CollectionContext
	frozen.JobID = job.ID
	var symbols string
	var parent *string
	err := r.DB.QueryRowContext(ctx, `SELECT parent_version,as_of,from_date,to_date,selection_scope,symbols_json,plan_version
		FROM generation_contexts WHERE job_id=?`, job.ID).Scan(&parent, &frozen.AsOf, &frozen.From, &frozen.To, &frozen.SelectionScope, &symbols, &frozen.PlanVersion)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return frozen, "", err
	}
	if err == nil {
		if parent != nil {
			frozen.ParentVersion = *parent
		}
		if err = json.Unmarshal([]byte(symbols), &frozen.Symbols); err != nil {
			return frozen, "", err
		}
	} else {
		frozen.From, frozen.To = job.Request.From, job.Request.To
		if job.Request.Mode == "daily" {
			frozen.From, frozen.To = job.Request.TradeDate, job.Request.TradeDate
		}
		frozen.AsOf, frozen.Symbols, frozen.PlanVersion = frozen.To, job.Request.Symbols, jobs.PlanVersion
		frozen.SelectionScope = "selected"
		if len(frozen.Symbols) == 0 {
			frozen.SelectionScope = "all"
		}
		var parentAsOf string
		err = r.DB.QueryRowContext(ctx, `SELECT id,as_of FROM dataset_versions WHERE state='active'`).Scan(&frozen.ParentVersion, &parentAsOf)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return frozen, "", err
		}
		if parentAsOf > frozen.AsOf {
			frozen.AsOf = parentAsOf
		}
	}
	version := platform.NewID("ds")
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return frozen, "", err
	}
	defer tx.Rollback()
	body, _ := json.Marshal(frozen.Symbols)
	input, _ := json.Marshal(frozen)
	sum := sha256.Sum256(input)
	if _, err = tx.ExecContext(ctx, `INSERT INTO generation_contexts
		(job_id,parent_version,as_of,from_date,to_date,selection_scope,symbols_json,plan_version,input_sha256,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(job_id) DO NOTHING`,
		job.ID, nullable(frozen.ParentVersion), frozen.AsOf, frozen.From, frozen.To, frozen.SelectionScope, string(body), frozen.PlanVersion, hex.EncodeToString(sum[:]), timestamp()); err != nil {
		return frozen, "", err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO dataset_versions
		(id,state,as_of,source,default_adjustment,algorithm_version,created_at,parent_version,selection_scope,schema_version)
		VALUES(?,'building',?,'baostock','qfq',?,?,?, ?,3)`,
		version, frozen.AsOf, marketdata.AdjustmentAlgorithm, timestamp(), nullable(frozen.ParentVersion), frozen.SelectionScope); err != nil {
		return frozen, "", err
	}
	for _, statement := range []string{
		`INSERT INTO dataset_partitions SELECT ?,instrument_id,trade_year,data_version FROM dataset_partitions WHERE dataset_version=?`,
		`INSERT INTO dataset_factor_sets SELECT ?,instrument_id,factor_version,action_version,anchor_date,input_sha256,algorithm_version FROM dataset_factor_sets WHERE dataset_version=?`,
		`INSERT INTO dataset_instrument_ranges SELECT ?,instrument_id,from_date,to_date FROM dataset_instrument_ranges WHERE dataset_version=?`,
	} {
		if _, err = tx.ExecContext(ctx, statement, version, frozen.ParentVersion); err != nil {
			return frozen, "", err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE generation_jobs SET dataset_version=?,stage='reference' WHERE id=?`, version, job.ID); err != nil {
		return frozen, "", err
	}
	return frozen, version, tx.Commit()
}

func (r Collection) SetTarget(ctx context.Context, jobID, version, asOf string) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var parent *string
	var selection, symbols string
	if err = tx.QueryRowContext(ctx, `SELECT parent_version,selection_scope,symbols_json FROM generation_contexts WHERE job_id=?`, jobID).Scan(&parent, &selection, &symbols); err != nil {
		return err
	}
	if parent != nil && selection == "selected" {
		var parentAsOf string
		if err = tx.QueryRowContext(ctx, `SELECT as_of FROM dataset_versions WHERE id=?`, *parent).Scan(&parentAsOf); err != nil {
			return err
		}
		if asOf > parentAsOf {
			var missing int
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(DISTINCT i.symbol) FROM dataset_instrument_ranges r JOIN instruments i ON i.id=r.instrument_id
				WHERE r.dataset_version=? AND i.symbol NOT IN (SELECT value FROM json_each(?))`, *parent, symbols).Scan(&missing); err != nil {
				return err
			}
			if missing > 0 {
				return &platform.Error{Code: "INVALID_ARGUMENT", Message: "selected request cannot advance other instruments; use all-market daily/backfill"}
			}
		}
	}
	frozen := jobs.CollectionContext{JobID: jobID, AsOf: asOf, SelectionScope: selection}
	if parent != nil {
		frozen.ParentVersion = *parent
	}
	if err = json.Unmarshal([]byte(symbols), &frozen.Symbols); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, `SELECT from_date,to_date,plan_version FROM generation_contexts WHERE job_id=?`, jobID).Scan(&frozen.From, &frozen.To, &frozen.PlanVersion); err != nil {
		return err
	}
	input, err := json.Marshal(frozen)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(input)
	if _, err = tx.ExecContext(ctx, `UPDATE generation_contexts SET as_of=?,input_sha256=? WHERE job_id=?`, asOf, hex.EncodeToString(sum[:]), jobID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE dataset_versions SET as_of=? WHERE id=? AND state='building'`, asOf, version); err != nil {
		return err
	}
	return tx.Commit()
}

func (r Collection) Stage(ctx context.Context, jobID, stage string) error {
	_, err := r.DB.ExecContext(ctx, `UPDATE generation_jobs SET stage=? WHERE id=? AND status='running'`, stage, jobID)
	return err
}

func (r Collection) RecordManifest(ctx context.Context, job jobs.Job, manifest marketdata.SourceManifest) error {
	_, err := r.DB.ExecContext(ctx, `UPDATE job_attempts SET worker_version=?,dependency_lock_sha256=?,adapter_version=? WHERE job_id=? AND attempt=?`,
		manifest.WorkerVersion, manifest.DependencyLockSHA256, manifest.AdapterVersion, job.ID, job.Attempt)
	return err
}

func (r Collection) Unit(ctx context.Context, jobID string, q jobs.Query) (string, *marketdata.SourceResponse, error) {
	body, _ := json.Marshal(q.Params)
	if _, err := r.DB.ExecContext(ctx, `INSERT INTO generation_units(job_id,query_key,operation,params_json,state,updated_at)
		VALUES(?,?,?,?,'pending',?) ON CONFLICT(job_id,query_key) DO NOTHING`, jobID, q.ID, q.Operation, string(body), timestamp()); err != nil {
		return "", nil, err
	}
	if _, err := r.DB.ExecContext(ctx, `UPDATE generation_jobs SET total_units=(SELECT COUNT(*) FROM generation_units WHERE job_id=?),
		completed_units=(SELECT COUNT(*) FROM generation_units WHERE job_id=? AND state='completed') WHERE id=?`, jobID, jobID, jobID); err != nil {
		return "", nil, err
	}
	var snapshot string
	var meta artifacts.Metadata
	err := r.DB.QueryRowContext(ctx, `SELECT u.snapshot_id,a.path,a.size_bytes,a.sha256
		FROM generation_units u JOIN source_snapshots s ON s.id=u.snapshot_id JOIN artifacts a ON a.id=s.artifact_id
		WHERE u.job_id=? AND u.query_key=? AND u.state='completed'`, jobID, q.ID).Scan(&snapshot, &meta.Path, &meta.SizeBytes, &meta.SHA256)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	raw, err := r.Raw.Read(meta)
	var response marketdata.SourceResponse
	if err == nil {
		err = json.Unmarshal(raw, &response)
	}
	if err == nil && response.Matches(q) && response.Complete && response.ErrorCode == "0" {
		return snapshot, &response, nil
	}
	// A corrupt checkpoint is not reusable. Preserve its old snapshot for audit.
	_, err = r.DB.ExecContext(ctx, `UPDATE generation_units SET state='pending',snapshot_id=NULL WHERE job_id=? AND query_key=?`, jobID, q.ID)
	return "", nil, err
}

func (r Collection) StartCall(ctx context.Context, job jobs.Job, q jobs.Query) (string, error) {
	id, now := platform.NewID("call"), timestamp()
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO provider_calls(id,job_id,attempt,provider,operation,started_at) VALUES(?,?,?,'baostock',?,?)`,
		id, job.ID, job.Attempt, q.Operation, now); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO generation_unit_attempts(job_id,query_key,sequence,job_attempt,provider_call_id,started_at)
		SELECT ?,?,COALESCE(MAX(sequence),0)+1,?,?,? FROM generation_unit_attempts WHERE job_id=? AND query_key=?`,
		job.ID, q.ID, job.Attempt, id, now, job.ID, q.ID); err != nil {
		return "", err
	}
	return id, tx.Commit()
}

func (r Collection) CallFailed(ctx context.Context, job jobs.Job, q jobs.Query, callID string, failure error) error {
	body, _ := json.Marshal(map[string]string{"code": "SOURCE_UNAVAILABLE", "message": failure.Error()})
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE provider_calls SET finished_at=?,error_json=? WHERE id=?`, timestamp(), string(body), callID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE generation_unit_attempts SET finished_at=? WHERE provider_call_id=?`, timestamp(), callID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE generation_units SET state='failed',error_json=?,updated_at=? WHERE job_id=? AND query_key=?`,
		string(body), timestamp(), job.ID, q.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (r Collection) SaveResponse(ctx context.Context, job jobs.Job, version string, q jobs.Query, callID string, response marketdata.SourceResponse, manifest marketdata.SourceManifest) (string, error) {
	if !response.Matches(q) {
		return "", fmt.Errorf("source response does not match query")
	}
	body, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	meta, err := r.Raw.Put(bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	artifact, snapshot := "raw_"+meta.SHA256, platform.NewID("snap")
	request, _ := json.Marshal(response.Request)
	fields, _ := json.Marshal(response.Fields)
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO artifacts(id,path,kind,size_bytes,sha256,job_id,schema_version,created_at)
		VALUES(?,?,'source_response',?,?,?,1,?) ON CONFLICT(path) DO NOTHING`, artifact, meta.Path, meta.SizeBytes, meta.SHA256, job.ID, timestamp()); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO source_snapshots
		(id,provider,artifact_id,dataset_version,as_of,created_at,operation,request_json,fields_json,record_count,fetched_at,provider_published_at,error_code,error_message,adapter_version)
		VALUES(?,'baostock',?,?,(SELECT as_of FROM dataset_versions WHERE id=?),?,?,?,?,?,?,?,?,?,?)`,
		snapshot, artifact, version, version, timestamp(), response.Operation, string(request), string(fields), len(response.Rows), response.FetchedAt, response.ProviderPublishedAt, response.ErrorCode, response.ErrorMessage, manifest.AdapterVersion); err != nil {
		return "", err
	}
	passed := response.Complete && response.ErrorCode == "0"
	state := "failed"
	var failure any
	if passed {
		state = "completed"
	} else {
		b, _ := json.Marshal(map[string]string{"code": response.ErrorCode, "message": response.ErrorMessage})
		failure = string(b)
	}
	if passed {
		if err = r.importResponse(ctx, tx, version, snapshot, response); err != nil {
			return "", err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE provider_calls SET finished_at=?,record_count=?,error_json=? WHERE id=?`,
		timestamp(), len(response.Rows), failure, callID); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE generation_unit_attempts SET finished_at=? WHERE provider_call_id=?`, timestamp(), callID); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE generation_units SET state=?,snapshot_id=?,record_count=?,validation_sha256=?,error_json=?,updated_at=? WHERE job_id=? AND query_key=?`,
		state, snapshot, len(response.Rows), meta.SHA256, failure, timestamp(), job.ID, q.ID); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE job_attempts SET worker_version=?,dependency_lock_sha256=?,adapter_version=? WHERE job_id=? AND attempt=?`,
		manifest.WorkerVersion, manifest.DependencyLockSHA256, manifest.AdapterVersion, job.ID, job.Attempt); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE generation_jobs SET completed_units=(SELECT COUNT(*) FROM generation_units WHERE job_id=? AND state='completed') WHERE id=?`, job.ID, job.ID); err != nil {
		return "", err
	}
	if response.Operation == "stock_basic" && response.Request["code"] == "" {
		_, err = tx.ExecContext(ctx, `UPDATE generation_contexts SET master_snapshot_id=? WHERE job_id=?`, snapshot, job.ID)
	} else if response.Operation == "trade_dates" {
		_, err = tx.ExecContext(ctx, `UPDATE generation_contexts SET calendar_snapshot_id=? WHERE job_id=?`, snapshot, job.ID)
	}
	if err != nil {
		return "", err
	}
	return snapshot, tx.Commit()
}

func (r Collection) Replay(ctx context.Context, version, snapshot string, response marketdata.SourceResponse) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = r.importResponse(ctx, tx, version, snapshot, response); err != nil {
		return err
	}
	return tx.Commit()
}
