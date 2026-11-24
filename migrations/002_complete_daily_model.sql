-- Forward migration: retain v1 rows and convert amount from cents to 1/10000 CNY.
ALTER TABLE instruments ADD COLUMN security_type TEXT NOT NULL DEFAULT 'stock'
    CHECK (security_type = 'stock');
ALTER TABLE dataset_versions RENAME COLUMN adjustment TO default_adjustment;
ALTER TABLE dataset_versions ADD COLUMN parent_version TEXT REFERENCES dataset_versions(id);
ALTER TABLE dataset_versions ADD COLUMN schema_version INTEGER NOT NULL DEFAULT 2;
UPDATE dataset_versions SET schema_version = 1;

ALTER TABLE source_snapshots ADD COLUMN operation TEXT;
ALTER TABLE source_snapshots ADD COLUMN request_json TEXT;
ALTER TABLE source_snapshots ADD COLUMN fields_json TEXT;
ALTER TABLE source_snapshots ADD COLUMN record_count INTEGER CHECK (record_count >= 0);
ALTER TABLE source_snapshots ADD COLUMN fetched_at TEXT;
ALTER TABLE source_snapshots ADD COLUMN provider_published_at TEXT;
ALTER TABLE source_snapshots ADD COLUMN error_code TEXT;
ALTER TABLE source_snapshots ADD COLUMN error_message TEXT;
ALTER TABLE source_snapshots ADD COLUMN adapter_version TEXT;
-- New snapshots must populate the collection envelope. Legacy metadata stays NULL.

ALTER TABLE daily_bars RENAME TO legacy_daily_bars;
CREATE TABLE daily_bars (
    dataset_version TEXT NOT NULL REFERENCES dataset_versions(id),
    instrument_id TEXT NOT NULL REFERENCES instruments(id),
    trade_date TEXT NOT NULL,
    adjustment TEXT NOT NULL CHECK (adjustment IN ('none', 'qfq')),
    open INTEGER NOT NULL CHECK (open >= 0),
    high INTEGER NOT NULL CHECK (high >= open AND high >= close AND high >= low),
    low INTEGER NOT NULL CHECK (low >= 0 AND low <= open AND low <= close),
    close INTEGER NOT NULL CHECK (close >= 0),
    previous_close INTEGER CHECK (previous_close >= 0),
    volume INTEGER NOT NULL CHECK (volume >= 0),
    amount INTEGER NOT NULL CHECK (amount >= 0),
    turnover_rate REAL CHECK (turnover_rate >= 0),
    change_rate REAL,
    trade_status TEXT NOT NULL CHECK (trade_status IN ('trading', 'suspended')),
    is_st INTEGER CHECK (is_st IN (0, 1)),
    source_snapshot_id TEXT REFERENCES source_snapshots(id),
    is_suspended INTEGER GENERATED ALWAYS AS (trade_status = 'suspended') VIRTUAL,
    PRIMARY KEY (dataset_version, instrument_id, adjustment, trade_date)
);
-- OHLC, previous_close and amount: 1/10000 CNY; volume: shares.
-- previous_close is the source's unadjusted exchange reference price even for qfq.
-- turnover_rate/change_rate are fractions: source percentage divided by 100.
INSERT INTO daily_bars (
    dataset_version,instrument_id,trade_date,adjustment,open,high,low,close,
    volume,amount,trade_status
)
SELECT dataset_version,instrument_id,trade_date,adjustment,open,high,low,close,
       volume,amount*100,CASE is_suspended WHEN 1 THEN 'suspended' ELSE 'trading' END
FROM legacy_daily_bars;
DROP TABLE legacy_daily_bars;

CREATE TABLE daily_valuations (
    dataset_version TEXT NOT NULL REFERENCES dataset_versions(id),
    instrument_id TEXT NOT NULL REFERENCES instruments(id),
    trade_date TEXT NOT NULL,
    pe_ttm REAL,
    pb_mrq REAL,
    ps_ttm REAL,
    pcf_ncf_ttm REAL,
    source_snapshot_id TEXT REFERENCES source_snapshots(id),
    PRIMARY KEY (dataset_version, instrument_id, trade_date)
);
-- Ratios may be negative or NULL. They are not multiplied by a price adjustment.

CREATE TABLE instrument_daily_status (
    dataset_version TEXT NOT NULL REFERENCES dataset_versions(id),
    instrument_id TEXT NOT NULL REFERENCES instruments(id),
    trade_date TEXT NOT NULL,
    code_name TEXT,
    trade_status TEXT NOT NULL CHECK (
        trade_status IN ('trading', 'suspended', 'not_listed', 'delisted', 'missing')
    ),
    is_st INTEGER CHECK (is_st IN (0, 1)),
    reason_code TEXT,
    source_snapshot_id TEXT REFERENCES source_snapshots(id),
    observed_at TEXT,
    PRIMARY KEY (dataset_version, instrument_id, trade_date)
);
INSERT INTO instrument_daily_status(dataset_version,instrument_id,trade_date,trade_status)
SELECT dataset_version,instrument_id,trade_date,
       CASE MAX(is_suspended) WHEN 1 THEN 'suspended' ELSE 'trading' END
FROM daily_bars GROUP BY dataset_version,instrument_id,trade_date;

-- A factor observation is not a dividend event. Preserve all provider factors
-- as decimal text; validate and compute with Decimal/rational arithmetic.
CREATE TABLE adjustment_factors (
    dataset_version TEXT NOT NULL REFERENCES dataset_versions(id),
    instrument_id TEXT NOT NULL REFERENCES instruments(id),
    effective_date TEXT NOT NULL,
    fore_adjust_factor TEXT NOT NULL,
    back_adjust_factor TEXT NOT NULL,
    adjust_factor TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    provider_published_at TEXT,
    source_snapshot_id TEXT NOT NULL REFERENCES source_snapshots(id),
    PRIMARY KEY (dataset_version, instrument_id, effective_date)
);

-- v1's ambiguous adjustment_factor cannot be mapped to three provider factors.
-- Retain its records for audit rather than invent factor semantics.
ALTER TABLE corporate_actions RENAME TO legacy_corporate_actions;
CREATE TABLE corporate_actions (
    id TEXT PRIMARY KEY,
    dataset_version TEXT NOT NULL REFERENCES dataset_versions(id),
    instrument_id TEXT NOT NULL REFERENCES instruments(id),
    record_sha256 TEXT NOT NULL,
    query_year INTEGER NOT NULL,
    year_type TEXT NOT NULL CHECK (year_type IN ('report', 'operate')),
    pre_notice_date TEXT,
    agm_announce_date TEXT,
    plan_announce_date TEXT,
    implementation_announce_date TEXT,
    record_date TEXT,
    effective_date TEXT,
    pay_date TEXT,
    stock_listing_date TEXT,
    cash_per_share_before_tax TEXT,
    cash_per_share_after_tax_raw TEXT,
    stock_dividend_per_share TEXT,
    reserve_transfer_per_share TEXT,
    description TEXT,
    observed_at TEXT NOT NULL,
    source_snapshot_id TEXT NOT NULL REFERENCES source_snapshots(id),
    UNIQUE (dataset_version, instrument_id, record_sha256)
);

-- A logical dataset inherits a complete manifest from its parent, replacing
-- only changed instrument/year partitions. data_version owns immutable facts.
-- Each partition includes the year's bars, statuses and valuations together.
CREATE TABLE dataset_partitions (
    dataset_version TEXT NOT NULL REFERENCES dataset_versions(id),
    instrument_id TEXT NOT NULL REFERENCES instruments(id),
    trade_year INTEGER NOT NULL CHECK (trade_year BETWEEN 1990 AND 9999),
    data_version TEXT NOT NULL REFERENCES dataset_versions(id),
    PRIMARY KEY (dataset_version, instrument_id, trade_year)
);
INSERT INTO dataset_partitions(dataset_version,instrument_id,trade_year,data_version)
SELECT DISTINCT dataset_version,instrument_id,CAST(substr(trade_date,1,4) AS INTEGER),dataset_version
FROM daily_bars;

CREATE TABLE dataset_factor_sets (
    dataset_version TEXT NOT NULL REFERENCES dataset_versions(id),
    instrument_id TEXT NOT NULL REFERENCES instruments(id),
    factor_version TEXT NOT NULL REFERENCES dataset_versions(id),
    action_version TEXT REFERENCES dataset_versions(id),
    anchor_date TEXT NOT NULL,
    input_sha256 TEXT NOT NULL,
    algorithm_version TEXT NOT NULL,
    PRIMARY KEY (dataset_version, instrument_id)
);
-- Empty factor sets are valid only after a successful, recorded full-range
-- query. There are no legacy entries because v1 did not record factor meaning.

CREATE INDEX daily_status_date ON instrument_daily_status(dataset_version,trade_date,trade_status);
CREATE INDEX quality_issues_instrument_date ON data_quality_issues(dataset_version,instrument_id,trade_date);
