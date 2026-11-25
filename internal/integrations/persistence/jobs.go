package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"stock_analysis_assistant/internal/jobs"
	"stock_analysis_assistant/internal/platform"
)

type Jobs struct{ DB *sql.DB }

const jobColumns = `id,status,stage,attempt,request_json,processed_records,expected_records,coverage,dataset_version,created_at,started_at,finished_at,error_json,completed_units,total_units,quality_artifact_id`

type scanner interface{ Scan(...any) error }

func scanJob(row scanner) (jobs.Job, error) {
	var job jobs.Job
	var request string
	var errorJSON *string
	if err := row.Scan(&job.ID, &job.Status, &job.Stage, &job.Attempt, &request, &job.ProcessedRecords, &job.ExpectedRecords,
		&job.Coverage, &job.DatasetVersion, &job.CreatedAt, &job.StartedAt, &job.FinishedAt, &errorJSON,
		&job.CompletedUnits, &job.TotalUnits, &job.QualityArtifactID); err != nil {
		return job, err
	}
	if err := json.Unmarshal([]byte(request), &job.Request); err != nil {
		return job, err
	}
	if errorJSON != nil {
		if err := json.Unmarshal([]byte(*errorJSON), &job.Error); err != nil {
			return job, err
		}
	}
	return job, nil
}

func (r Jobs) Retry(ctx context.Context, id string) (jobs.Job, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return jobs.Job{}, err
	}
	defer tx.Rollback()
	// Known failed adjustment inputs must be refreshed, not replayed unchanged.
	if _, err := tx.ExecContext(ctx, `UPDATE generation_units SET state='pending',snapshot_id=NULL
		WHERE job_id=? AND operation IN ('adjust_factor','history')
		AND EXISTS(SELECT 1 FROM generation_jobs WHERE id=? AND status='failed'
		AND error_json LIKE '%ADJUSTMENT_VALIDATION_FAILED%')`, id, id); err != nil {
		return jobs.Job{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_jobs SET status='queued',stage='queued',
		error_json=NULL,finished_at=NULL,started_at=NULL
		WHERE id=? AND status='failed'`, id); err != nil {
		return jobs.Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return jobs.Job{}, err
	}
	return r.Get(ctx, id)
}

func (r Jobs) Create(ctx context.Context, request jobs.Request, provider string) (jobs.Job, error) {
	if err := request.Normalize(); err != nil {
		return jobs.Job{}, &platform.Error{Code: "INVALID_ARGUMENT", Message: err.Error()}
	}
	body, err := json.Marshal(request)
	if err != nil {
		return jobs.Job{}, err
	}
	key := request.Key(provider)
	_, err = r.DB.ExecContext(ctx, `INSERT INTO generation_jobs
		(id,idempotency_key,request_json,provider,status,stage,created_at) VALUES (?,?,?,?,'queued','queued',?)
		ON CONFLICT(idempotency_key) DO NOTHING`, platform.NewID("gen"), key, string(body), provider, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return jobs.Job{}, err
	}
	return scanJob(r.DB.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM generation_jobs WHERE idempotency_key=?`, key))
}

func (r Jobs) Get(ctx context.Context, id string) (jobs.Job, error) {
	job, err := scanJob(r.DB.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM generation_jobs WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return job, &platform.Error{Code: "GENERATION_NOT_FOUND", Message: "generation not found"}
	}
	return job, err
}

func (r Jobs) Latest(ctx context.Context) (*jobs.Job, error) {
	job, err := scanJob(r.DB.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM generation_jobs ORDER BY created_at DESC LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &job, err
}

func (r Jobs) Claim(ctx context.Context) (*jobs.Job, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	job, err := scanJob(tx.QueryRowContext(ctx, `UPDATE generation_jobs SET status='running',stage='collecting',attempt=attempt+1,started_at=?
		WHERE id=(SELECT id FROM generation_jobs WHERE status='queued' ORDER BY created_at LIMIT 1) RETURNING `+jobColumns,
		time.Now().UTC().Format(time.RFC3339Nano)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO job_attempts(job_id,attempt,status,started_at) VALUES (?,?,'running',?)`,
		job.ID, job.Attempt, job.StartedAt); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &job, nil
}

func (r Jobs) Fail(ctx context.Context, job jobs.Job, failure *platform.Error) error {
	body, _ := json.Marshal(failure)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE dataset_versions SET state='rejected'
		WHERE state='building' AND id=(SELECT dataset_version FROM generation_jobs WHERE id=?)`, job.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_jobs SET status='failed',stage='failed',error_json=?,finished_at=? WHERE id=? AND status='running'`,
		string(body), now, job.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE job_attempts SET status='failed',error_json=?,finished_at=? WHERE job_id=? AND attempt=?`,
		string(body), now, job.ID, job.Attempt); err != nil {
		return err
	}
	return tx.Commit()
}

// Recover gives interrupted attempts an explicit terminal state. Queued requests
// remain queued and are drained by the next stockd process.
func (r Jobs) Recover(ctx context.Context) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	body := `{"code":"INTERNAL_ERROR","message":"generation interrupted by process restart"}`
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE job_attempts SET status='failed',error_json=?,finished_at=? WHERE status='running'`, body, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE dataset_versions SET state='rejected' WHERE state='building' AND id IN
		(SELECT dataset_version FROM generation_jobs WHERE status='running')`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_jobs SET status='failed',stage='interrupted',error_json=?,finished_at=? WHERE status='running'`, body, now); err != nil {
		return err
	}
	return tx.Commit()
}
