package delivery_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"stock_analysis_assistant/internal/delivery"
	"stock_analysis_assistant/internal/integrations/persistence"
	"stock_analysis_assistant/internal/jobs"
	"stock_analysis_assistant/internal/platform"
	"stock_analysis_assistant/internal/platform/database"
)

func TestBootstrapAPIAndRecovery(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "stock.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal("repeat migration", err)
	}
	repository := persistence.Jobs{DB: db}
	securities := persistence.Securities{DB: db}
	api := delivery.API{Jobs: repository, Securities: securities,
		MarketData: persistence.MarketData{DB: db, Securities: securities}, Provider: "baostock",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	handler := api.Handler()
	call := func(method, path, body string, expected int) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		if w.Code != expected {
			t.Fatalf("%s %s: expected %d, got %d: %s", method, path, expected, w.Code, w.Body.String())
		}
		if w.Header().Get("X-Request-ID") == "" {
			t.Fatal("missing request ID")
		}
		return w
	}
	if response := call("GET", "/api/v1/market-data/status", "", 503); !strings.Contains(response.Body.String(), "NO_ACTIVE_DATASET") {
		t.Fatal(response.Body.String())
	}
	call("GET", "/api/v1/instruments", "", 200)
	call("POST", "/api/v1/market-data/generations", `{"mode":"daily"}`, 400)
	call("POST", "/api/v1/market-data/generations", `{"mode":"daily","trade_date":"2026-11-19","provider":"other"}`, 400)
	first := call("POST", "/api/v1/market-data/generations", `{"mode":"daily","trade_date":"2026-11-19","symbols":["000001.sz","000001.SZ"]}`, 202)
	second := call("POST", "/api/v1/market-data/generations", `{"mode":"daily","trade_date":"2026-11-19","symbols":["000001.SZ"],"adjustment":"qfq"}`, 202)
	var a, b map[string]string
	if err := json.Unmarshal(first.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second.Body.Bytes(), &b); err != nil || a["generation_id"] != b["generation_id"] {
		t.Fatal("non-idempotent generation", err)
	}
	claimed, err := repository.Claim(ctx)
	if err != nil || claimed == nil || claimed.Attempt != 1 || claimed.Request.Adjustment != "qfq" {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	if err := repository.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := repository.Get(ctx, claimed.ID)
	if err != nil || recovered.Status != "failed" || recovered.Error == nil || recovered.FinishedAt == nil {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	call("GET", a["status_url"], "", 200)
	call("GET", "/api/v1/market-data/generations/unknown", "", http.StatusNotFound)

	// Fixed offline database fixture: building/rejected records must stay invisible.
	_, err = db.ExecContext(ctx, `
		INSERT INTO instruments(id,symbol,name,exchange,listed_on,status) VALUES ('ins_test','000001.SZ','test','SZ','2000-01-01','listed');
		INSERT INTO dataset_versions(id,state,as_of,source,default_adjustment,algorithm_version,created_at)
		VALUES ('dv_active','active','2026-11-19','baostock','qfq','v1','2026-11-19T10:00:00Z'),
		       ('dv_building','building','2026-11-20','baostock','qfq','v1','2026-11-20T10:00:00Z'),
		       ('dv_old','superseded','2025-12-31','baostock','qfq','v1','2025-12-31T10:00:00Z');
		INSERT INTO quality_reports VALUES ('dv_active',1,1,1,1,'{}','2026-11-19T10:00:00Z');
		INSERT INTO daily_bars(dataset_version,instrument_id,trade_date,adjustment,open,high,low,close,previous_close,volume,amount,turnover_rate,change_rate,trade_status,is_st)
		VALUES ('dv_active','ins_test','2026-11-19','qfq',112000,114500,110800,113600,220000,100,139876541234,0.003925,-0.005618,'trading',1),
		       ('dv_active','ins_test','2026-11-19','none',224000,229000,221600,227200,220000,100,139876541234,0.003925,-0.005618,'trading',1),
		       ('dv_building','ins_test','2026-11-20','qfq',112000,114500,110800,113600,220000,100,139876541234,0.003925,-0.005618,'trading',1),
		       ('dv_old','ins_test','2025-12-31','qfq',112000,114500,110800,113600,NULL,100,139876541234,NULL,NULL,'trading',NULL);
		INSERT INTO daily_valuations(dataset_version,instrument_id,trade_date,pe_ttm,pb_mrq,ps_ttm,pcf_ncf_ttm)
		VALUES ('dv_active','ins_test','2026-11-19',NULL,0.962863,2.226877,-58.658491);
		INSERT INTO instrument_daily_status(dataset_version,instrument_id,trade_date,code_name,trade_status,is_st,reason_code)
		VALUES ('dv_active','ins_test','2026-11-19','test','trading',1,NULL),
		       ('dv_active','ins_test','2026-11-20','test','missing',NULL,'SOURCE_MISSING');
		INSERT INTO dataset_partitions VALUES ('dv_active','ins_test',2026,'dv_active'),
		                                      ('dv_active','ins_test',2025,'dv_old');`)
	if err != nil {
		t.Fatal(err)
	}
	call("GET", "/api/v1/market-data/status", "", 200)
	qfq := call("GET", "/api/v1/instruments/ins_test/daily-bars?from=2026-11-19&to=2026-11-20", "", 200)
	none := call("GET", "/api/v1/instruments/ins_test/daily-bars?from=2026-11-19&to=2026-11-20&adjustment=none", "", 200)
	for _, response := range []*httptest.ResponseRecorder{qfq, none} {
		var result struct {
			Version string `json:"dataset_version"`
			Bars    []struct {
				Close         float64  `json:"close"`
				Amount        float64  `json:"amount"`
				PreviousClose *float64 `json:"previous_close"`
				TurnoverRate  *float64 `json:"turnover_rate"`
				ChangeRate    *float64 `json:"change_rate"`
				IsST          *bool    `json:"is_st"`
				Valuation     *struct {
					PETTM     *float64 `json:"pe_ttm"`
					PCFNCFTTM *float64 `json:"pcf_ncf_ttm"`
				} `json:"valuation"`
			} `json:"bars"`
			DateStatuses []struct {
				TradeStatus string  `json:"trade_status"`
				ReasonCode  *string `json:"reason_code"`
			} `json:"date_statuses"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Version != "dv_active" || len(result.Bars) != 1 {
			t.Fatalf("active query: %s error=%v", response.Body.String(), err)
		}
		bar := result.Bars[0]
		if bar.Amount != 13987654.1234 || bar.PreviousClose == nil || *bar.PreviousClose != 22 {
			t.Fatal("amount changed with adjustment")
		}
		if bar.TurnoverRate == nil || *bar.TurnoverRate != 0.003925 || bar.ChangeRate == nil || *bar.ChangeRate != -0.005618 || bar.IsST == nil || !*bar.IsST {
			t.Fatal("missing daily fields", response.Body.String())
		}
		if bar.Valuation == nil || bar.Valuation.PETTM != nil || bar.Valuation.PCFNCFTTM == nil || *bar.Valuation.PCFNCFTTM != -58.658491 {
			t.Fatal("valuation null/negative values lost", response.Body.String())
		}
		if len(result.DateStatuses) != 2 || result.DateStatuses[1].TradeStatus != "missing" || result.DateStatuses[1].ReasonCode == nil {
			t.Fatal("missing-date reason lost", response.Body.String())
		}
	}
	history := call("GET", "/api/v1/instruments/ins_test/daily-bars?from=2025-12-31&to=2026-11-20", "", 200)
	if !strings.Contains(history.Body.String(), `"trade_date":"2025-12-31"`) {
		t.Fatal("inherited historical partition disappeared", history.Body.String())
	}
	other, err := repository.Create(ctx, jobs.Request{Mode: "daily", TradeDate: "2026-11-20"}, "baostock")
	if err != nil {
		t.Fatal(err)
	}
	otherClaimed, err := repository.Claim(ctx)
	if err != nil || otherClaimed == nil || otherClaimed.ID != other.ID {
		t.Fatal("claim second request", err)
	}
	if err := repository.Fail(ctx, *otherClaimed, &platform.Error{Code: "SOURCE_UNAVAILABLE", Message: "offline"}); err != nil {
		t.Fatal(err)
	}
	call("GET", "/api/v1/market-data/status", "", 200)
	if _, err := db.ExecContext(ctx, `UPDATE dataset_versions SET state='active' WHERE id='dv_building'`); err == nil {
		t.Fatal("multiple active versions allowed")
	}
	backup := filepath.Join(t.TempDir(), "backup.db")
	if err := database.Backup(ctx, db, backup); err != nil {
		t.Fatal(err)
	}
	restored, err := database.Open(ctx, backup)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var count int
	if err := restored.QueryRowContext(ctx, "SELECT COUNT(*) FROM daily_bars").Scan(&count); err != nil || count != 4 {
		t.Fatal("backup did not preserve WAL records", err)
	}
}
