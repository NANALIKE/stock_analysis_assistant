ALTER TABLE generation_jobs ADD COLUMN completed_units INTEGER NOT NULL DEFAULT 0;
ALTER TABLE generation_jobs ADD COLUMN total_units INTEGER NOT NULL DEFAULT 0;
ALTER TABLE generation_jobs ADD COLUMN quality_artifact_id TEXT REFERENCES artifacts(id);
ALTER TABLE dataset_versions ADD COLUMN market_scope TEXT NOT NULL DEFAULT 'SH,SZ';
ALTER TABLE dataset_versions ADD COLUMN selection_scope TEXT NOT NULL DEFAULT 'selected';

CREATE TABLE generation_contexts (
    job_id TEXT PRIMARY KEY REFERENCES generation_jobs(id),
    parent_version TEXT REFERENCES dataset_versions(id),
    as_of TEXT NOT NULL,
    from_date TEXT NOT NULL,
    to_date TEXT NOT NULL,
    selection_scope TEXT NOT NULL CHECK (selection_scope IN ('all','selected')),
    symbols_json TEXT NOT NULL,
    plan_version TEXT NOT NULL,
    input_sha256 TEXT NOT NULL,
    master_snapshot_id TEXT REFERENCES source_snapshots(id),
    calendar_snapshot_id TEXT REFERENCES source_snapshots(id),
    created_at TEXT NOT NULL
);
CREATE TABLE generation_units (
    job_id TEXT NOT NULL REFERENCES generation_jobs(id),
    query_key TEXT NOT NULL,
    operation TEXT NOT NULL,
    params_json TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('pending','completed','failed')),
    snapshot_id TEXT REFERENCES source_snapshots(id),
    record_count INTEGER NOT NULL DEFAULT 0,
    validation_sha256 TEXT,
    error_json TEXT,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (job_id,query_key)
);
CREATE TABLE generation_unit_attempts (
    job_id TEXT NOT NULL,
    query_key TEXT NOT NULL,
    sequence INTEGER NOT NULL,
    job_attempt INTEGER NOT NULL,
    provider_call_id TEXT NOT NULL REFERENCES provider_calls(id),
    started_at TEXT NOT NULL,
    finished_at TEXT,
    FOREIGN KEY (job_id,query_key) REFERENCES generation_units(job_id,query_key),
    FOREIGN KEY (job_id,job_attempt) REFERENCES job_attempts(job_id,attempt),
    PRIMARY KEY (job_id,query_key,sequence)
);
CREATE INDEX generation_units_state ON generation_units(job_id,state);
CREATE TABLE dataset_instrument_ranges (
    dataset_version TEXT NOT NULL REFERENCES dataset_versions(id),
    instrument_id TEXT NOT NULL REFERENCES instruments(id),
    from_date TEXT NOT NULL,
    to_date TEXT NOT NULL CHECK (to_date >= from_date),
    PRIMARY KEY (dataset_version,instrument_id,from_date)
);
