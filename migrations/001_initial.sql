CREATE TABLE instruments (
    id TEXT PRIMARY KEY,
    symbol TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    exchange TEXT NOT NULL CHECK (exchange IN ('SH', 'SZ', 'BJ')),
    listed_on TEXT NOT NULL,
    delisted_on TEXT,
    status TEXT NOT NULL CHECK (status IN ('listed', 'delisted'))
);
CREATE TABLE instrument_aliases (
    alias TEXT NOT NULL,
    instrument_id TEXT NOT NULL REFERENCES instruments(id),
    valid_from TEXT NOT NULL,
    valid_to TEXT,
    PRIMARY KEY (alias, valid_from)
);
CREATE TABLE trading_days (
    exchange TEXT NOT NULL,
    trade_date TEXT NOT NULL,
    is_open INTEGER NOT NULL CHECK (is_open IN (0, 1)),
    PRIMARY KEY (exchange, trade_date)
);
CREATE TABLE dataset_versions (
    id TEXT PRIMARY KEY,
    state TEXT NOT NULL CHECK (state IN ('building', 'active', 'rejected', 'superseded')),
    as_of TEXT NOT NULL,
    source TEXT NOT NULL,
    adjustment TEXT NOT NULL CHECK (adjustment IN ('none', 'qfq')),
    algorithm_version TEXT NOT NULL,
    input_sha256 TEXT,
    created_at TEXT NOT NULL,
    published_at TEXT
);
CREATE UNIQUE INDEX one_active_dataset ON dataset_versions(state) WHERE state = 'active';
CREATE TABLE generation_jobs (
    id TEXT PRIMARY KEY,
    idempotency_key TEXT NOT NULL UNIQUE,
    request_json TEXT NOT NULL,
    provider TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed')),
    stage TEXT NOT NULL,
    attempt INTEGER NOT NULL DEFAULT 0,
    processed_records INTEGER NOT NULL DEFAULT 0,
    expected_records INTEGER NOT NULL DEFAULT 0,
    coverage REAL,
    dataset_version TEXT REFERENCES dataset_versions(id),
    error_json TEXT,
    created_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT
);
CREATE INDEX generation_jobs_status ON generation_jobs(status, created_at);
CREATE TABLE job_attempts (
    job_id TEXT NOT NULL REFERENCES generation_jobs(id),
    attempt INTEGER NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
    worker_version TEXT,
    dependency_lock_sha256 TEXT,
    adapter_version TEXT,
    started_at TEXT NOT NULL,
    finished_at TEXT,
    error_json TEXT,
    PRIMARY KEY (job_id, attempt)
);
CREATE TABLE artifacts (
    id TEXT PRIMARY KEY,
    path TEXT NOT NULL UNIQUE,
    kind TEXT NOT NULL,
    size_bytes INTEGER NOT NULL CHECK (size_bytes >= 0),
    sha256 TEXT NOT NULL,
    job_id TEXT NOT NULL REFERENCES generation_jobs(id),
    schema_version INTEGER NOT NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE source_snapshots (
    id TEXT PRIMARY KEY,
    provider TEXT NOT NULL,
    artifact_id TEXT NOT NULL REFERENCES artifacts(id),
    dataset_version TEXT NOT NULL REFERENCES dataset_versions(id),
    as_of TEXT NOT NULL,
    created_at TEXT NOT NULL
);
-- OHLC uses 1/10000 CNY; amount uses 1/100 CNY; volume uses shares.
CREATE TABLE daily_bars (
    instrument_id TEXT NOT NULL REFERENCES instruments(id),
    trade_date TEXT NOT NULL,
    adjustment TEXT NOT NULL CHECK (adjustment IN ('none', 'qfq')),
    dataset_version TEXT NOT NULL REFERENCES dataset_versions(id),
    open INTEGER NOT NULL CHECK (open >= 0),
    high INTEGER NOT NULL CHECK (high >= open AND high >= close AND high >= low),
    low INTEGER NOT NULL CHECK (low >= 0 AND low <= open AND low <= close),
    close INTEGER NOT NULL CHECK (close >= 0),
    volume INTEGER NOT NULL CHECK (volume >= 0),
    amount INTEGER NOT NULL CHECK (amount >= 0),
    is_suspended INTEGER NOT NULL DEFAULT 0 CHECK (is_suspended IN (0, 1)),
    PRIMARY KEY (dataset_version, instrument_id, adjustment, trade_date)
);
CREATE TABLE corporate_actions (
    instrument_id TEXT NOT NULL REFERENCES instruments(id),
    effective_date TEXT NOT NULL,
    available_at TEXT NOT NULL,
    dataset_version TEXT NOT NULL REFERENCES dataset_versions(id),
    adjustment_factor REAL NOT NULL CHECK (adjustment_factor > 0),
    source_snapshot_id TEXT NOT NULL REFERENCES source_snapshots(id),
    PRIMARY KEY (dataset_version, instrument_id, effective_date, available_at)
);
CREATE TABLE quality_reports (
    dataset_version TEXT PRIMARY KEY REFERENCES dataset_versions(id),
    expected_records INTEGER NOT NULL,
    valid_records INTEGER NOT NULL,
    coverage REAL NOT NULL CHECK (coverage BETWEEN 0 AND 1),
    passed INTEGER NOT NULL CHECK (passed IN (0, 1)),
    report_json TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE data_quality_issues (
    id TEXT PRIMARY KEY,
    dataset_version TEXT NOT NULL REFERENCES dataset_versions(id),
    instrument_id TEXT REFERENCES instruments(id),
    trade_date TEXT,
    code TEXT NOT NULL,
    details_json TEXT NOT NULL
);
CREATE TABLE provider_calls (
    id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL,
    attempt INTEGER NOT NULL,
    provider TEXT NOT NULL,
    operation TEXT NOT NULL,
    started_at TEXT NOT NULL,
    finished_at TEXT,
    record_count INTEGER NOT NULL DEFAULT 0,
    error_json TEXT,
    FOREIGN KEY (job_id, attempt) REFERENCES job_attempts(job_id, attempt)
);
