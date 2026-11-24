package database_test

import (
	"context"
	"path/filepath"
	"testing"

	"stock_analysis_assistant/internal/platform/database"
	"stock_analysis_assistant/migrations"
)

func TestUpgradePreservesLegacyDataAndAmountUnits(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "v1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v1, err := migrations.Files.ReadFile("001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, string(v1)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE schema_migrations(version TEXT PRIMARY KEY,applied_at TEXT NOT NULL);
		INSERT INTO schema_migrations VALUES ('001_initial.sql','2026-11-19T10:00:00Z');
		INSERT INTO instruments VALUES ('ins_v1','000001.SZ','legacy','SZ','2000-01-01',NULL,'listed');
		INSERT INTO dataset_versions VALUES ('dv_v1','active','2026-11-19','baostock','qfq','v1',NULL,'2026-11-19T10:00:00Z',NULL);
		INSERT INTO daily_bars VALUES ('ins_v1','2026-11-19','qfq','dv_v1',110000,110000,110000,110000,0,12345,1);
	`); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal("repeat migration", err)
	}
	var amount int64
	var previousClose *int64
	var suspended bool
	if err := db.QueryRowContext(ctx, "SELECT amount,previous_close,is_suspended FROM daily_bars").Scan(&amount, &previousClose, &suspended); err != nil {
		t.Fatal(err)
	}
	if amount != 1234500 || previousClose != nil || !suspended {
		t.Fatalf("amount=%d previous_close=%v suspended=%v", amount, previousClose, suspended)
	}
	var owner, status string
	if err := db.QueryRowContext(ctx, "SELECT data_version FROM dataset_partitions").Scan(&owner); err != nil || owner != "dv_v1" {
		t.Fatal(owner, err)
	}
	if err := db.QueryRowContext(ctx, "SELECT trade_status FROM instrument_daily_status").Scan(&status); err != nil || status != "suspended" {
		t.Fatal(status, err)
	}
	var violations int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM pragma_foreign_key_check").Scan(&violations); err != nil || violations != 0 {
		t.Fatal("foreign key violations", violations, err)
	}
	var schemaVersion int
	if err := db.QueryRowContext(ctx, "SELECT schema_version FROM dataset_versions").Scan(&schemaVersion); err != nil || schemaVersion != 1 {
		t.Fatal("legacy version mislabelled", schemaVersion, err)
	}
}
