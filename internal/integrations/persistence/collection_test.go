package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"stock_analysis_assistant/internal/jobs"
	"stock_analysis_assistant/internal/marketdata"
	"stock_analysis_assistant/internal/platform"
	"stock_analysis_assistant/internal/platform/artifacts"
	"stock_analysis_assistant/internal/platform/database"
)

type fixtureSource struct {
	calls, replays map[string]int
	fail           string
	partial        bool
}

func (s *fixtureSource) Query(_ context.Context, _ string, _ string, q jobs.Query, replay *marketdata.SourceResponse) (marketdata.SourceResponse, marketdata.SourceManifest, error) {
	manifest := marketdata.SourceManifest{WorkerVersion: "fixture", DependencyLockSHA256: "test", AdapterVersion: "fixture"}
	if replay != nil {
		s.replays[q.ID]++
		return *replay, manifest, nil
	}
	s.calls[q.ID]++
	if s.fail == "transport:"+q.Operation {
		return marketdata.SourceResponse{}, manifest, fmt.Errorf("worker failed: fixture transport failure")
	}
	r := marketdata.SourceResponse{Kind: "source_response", QueryID: q.ID, Operation: q.Operation, Request: q.Params, Fields: []string{}, Rows: [][]string{}, Records: []json.RawMessage{}, Issues: []marketdata.Issue{}, FetchedAt: "2020-01-10T00:00:00Z", Complete: true, ErrorCode: "0"}
	if s.fail == "unsupported:"+q.Operation {
		r.Complete, r.ErrorCode = false, "UNSUPPORTED"
		return r, manifest, nil
	}
	if s.fail == q.Operation {
		return r, manifest, fmt.Errorf("fixture source unavailable")
	}
	add := func(f marketdata.Fact) { body, _ := json.Marshal(f); r.Records = append(r.Records, body) }
	switch q.Operation {
	case "stock_basic":
		instrument := marketdata.Fact{Kind: "instrument", Symbol: "600000.SH", Name: "fixture", Exchange: "SH", ListedOn: "2020-01-01", Status: "listed"}
		if s.partial {
			day := "2020-01-03"
			instrument.DelistedOn, instrument.Status = &day, "delisted"
		}
		add(instrument)
	case "trade_dates":
		start, _ := time.Parse(time.DateOnly, q.Params["start_date"])
		end, _ := time.Parse(time.DateOnly, q.Params["end_date"])
		for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
			open := day.Weekday() != time.Saturday && day.Weekday() != time.Sunday && day.Format(time.DateOnly) != "2020-01-01"
			add(marketdata.Fact{Kind: "calendar", TradeDate: day.Format(time.DateOnly), IsOpen: open})
		}
	case "all_stock":
		add(marketdata.Fact{Kind: "status", Symbol: "600000.SH", Name: "fixture", TradeStatus: "trading"})
	case "adjust_factor":
		if q.Params["end_date"] >= "2020-01-03" {
			add(marketdata.Fact{Kind: "factor", Symbol: "600000.SH", EffectiveDate: "2020-01-03", Fore: "0.5000000000", Back: "2.0000000000", Factor: "2.0000000000"})
		}
	case "history", "daily_history":
		from, to := q.Params["start_date"], q.Params["end_date"]
		if q.Operation == "daily_history" {
			from = q.Params["date"]
			to = from
		}
		start, _ := time.Parse(time.DateOnly, from)
		end, _ := time.Parse(time.DateOnly, to)
		r.Fields = []string{"date", "open", "high", "low", "close"}
		for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
			date := day.Format(time.DateOnly)
			if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday || date == "2020-01-01" {
				continue
			}
			if s.partial && date >= "2020-01-03" {
				if date == "2020-01-03" && q.Params["adjustflag"] != "2" {
					pe := -1.5
					add(marketdata.Fact{Kind: "daily_status", Symbol: "600000.SH", TradeDate: date, TradeStatus: "suspended", Valuation: marketdata.Valuation{PETTM: &pe}})
				}
				continue
			}
			price := int64(100000)
			if date >= "2020-01-03" {
				price = 50000
			}
			if q.Params["adjustflag"] == "2" {
				r.Rows = append(r.Rows, []string{date, "5.0000", "5.0000", "5.0000", "5.0000"})
				continue
			}
			add(marketdata.Fact{Kind: "daily", Symbol: "600000.SH", Bar: marketdata.FixedBar{TradeDate: date, Adjustment: "none", Open: price, High: price, Low: price, Close: price, PreviousClose: &price, TradeStatus: "trading", Volume: 100, Amount: 1000000}})
		}
	case "dividend":
	default:
		return r, manifest, fmt.Errorf("unsupported fixture operation %s", q.Operation)
	}
	return r, manifest, nil
}

func collectionFixture(t *testing.T) (*sql.DB, Collection, Jobs, *fixtureSource, marketdata.Runner) {
	t.Helper()
	root := t.TempDir()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(root, "stock.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := Collection{DB: db, Raw: artifacts.Store{Root: filepath.Join(root, "raw")}, Reports: artifacts.Store{Root: filepath.Join(root, "reports")}}
	source := &fixtureSource{calls: map[string]int{}, replays: map[string]int{}}
	runner := marketdata.Runner{Store: store, Source: source, MaxAttempts: 1, MinimumCoverage: .995, Now: func() time.Time { return time.Date(2020, 1, 10, 16, 0, 0, 0, time.FixedZone("Shanghai", 8*3600)) }}
	return db, store, Jobs{DB: db}, source, runner
}

func executeFixture(t *testing.T, repo Jobs, runner marketdata.Runner, request jobs.Request) (jobs.Job, error) {
	t.Helper()
	ctx := context.Background()
	job, err := repo.Create(ctx, request, "baostock")
	if err != nil {
		t.Fatal(err)
	}
	if job.Status == "succeeded" {
		return job, nil
	}
	claimed, err := repo.Claim(ctx)
	if err != nil || claimed == nil {
		t.Fatal(claimed, err)
	}
	err = runner.Execute(ctx, *claimed)
	if err != nil {
		failure := &platform.Error{Code: "INTERNAL_ERROR", Message: err.Error()}
		if typed, ok := err.(*platform.Error); ok {
			failure = typed
		}
		if finish := repo.Fail(ctx, *claimed, failure); finish != nil {
			t.Fatal(finish)
		}
	}
	job, getErr := repo.Get(ctx, job.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	return job, err
}

func TestCollectionPublishInheritanceAndFailure(t *testing.T) {
	db, _, repo, source, runner := collectionFixture(t)
	ctx := context.Background()
	request := jobs.Request{Mode: "backfill", From: "2020-01-02", To: "2020-01-03", Symbols: []string{"600000.SH"}}
	first, err := executeFixture(t, repo, runner, request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "succeeded" || first.ProcessedRecords != 2 || first.ExpectedRecords != 2 || first.QualityArtifactID == nil {
		t.Fatalf("%+v", first)
	}
	queries := MarketData{DB: db, Securities: Securities{DB: db}}
	bars, err := queries.Bars(ctx, "ins_600000_SH", "2020-01-02", "2020-01-03", "qfq")
	if err != nil {
		t.Fatal(err)
	}
	if len(bars.Bars) != 2 || bars.Bars[0].Close != 5 || len(bars.CoverageRanges) != 1 {
		t.Fatalf("%+v", bars)
	}
	request.From, request.To = "2020-01-08", "2020-01-08"
	second, err := executeFixture(t, repo, runner, request)
	if err != nil {
		t.Fatal(err)
	}
	bars, err = queries.Bars(ctx, "ins_600000_SH", "2020-01-02", "2020-01-08", "none")
	if err != nil {
		t.Fatal(err)
	}
	if len(bars.Bars) != 3 || bars.Bars[0].Close != 10 || len(bars.CoverageRanges) != 2 {
		t.Fatalf("%+v", bars)
	}
	source.fail = "adjust_factor"
	request.From, request.To = "2020-01-09", "2020-01-09"
	failed, err := executeFixture(t, repo, runner, request)
	if err == nil || failed.Status != "failed" {
		t.Fatal(failed, err)
	}
	active, err := queries.Active(ctx)
	if err != nil || active.ID != *second.DatasetVersion {
		t.Fatal(active, err)
	}
	var violations int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatal(violations, err)
	}
}

func TestCollectionExplicitRetryReplaysSuccessfulUnits(t *testing.T) {
	db, _, repo, source, runner := collectionFixture(t)
	ctx := context.Background()
	source.fail = "adjust_factor"
	request := jobs.Request{Mode: "backfill", From: "2020-01-02", To: "2020-01-03", Symbols: []string{"600000.SH"}}
	failed, err := executeFixture(t, repo, runner, request)
	if err == nil {
		t.Fatal("expected source failure")
	}
	duplicate, err := repo.Create(ctx, request, "baostock")
	if err != nil || duplicate.ID != failed.ID || duplicate.Status != "failed" {
		t.Fatal(duplicate, err)
	}
	source.fail = ""
	if _, err = repo.Retry(ctx, failed.ID); err != nil {
		t.Fatal(err)
	}
	success, err := executeFixture(t, repo, runner, request)
	if err != nil {
		t.Fatal(err)
	}
	if success.ID != failed.ID || success.Attempt != 2 || success.Status != "succeeded" {
		t.Fatalf("%+v", success)
	}
	master := jobs.NewQuery("stock_basic", map[string]string{"code": ""})
	if source.calls[master.ID] != 1 || source.replays[master.ID] != 1 {
		t.Fatal(source.calls[master.ID], source.replays[master.ID])
	}
	var workerVersion string
	if err = db.QueryRowContext(ctx, `SELECT worker_version FROM job_attempts WHERE job_id=? AND attempt=2`, success.ID).Scan(&workerVersion); err != nil || workerVersion != "fixture" {
		t.Fatal("replay must record the executing worker manifest", workerVersion, err)
	}
	var owner string
	if err = db.QueryRowContext(ctx, `SELECT DISTINCT data_version FROM dataset_partitions WHERE dataset_version=?`, *success.DatasetVersion).Scan(&owner); err != nil || owner != *success.DatasetVersion {
		t.Fatal(owner, err)
	}
}

func TestFutureDateRejectedBeforeSource(t *testing.T) {
	_, _, repo, source, runner := collectionFixture(t)
	_, err := executeFixture(t, repo, runner, jobs.Request{Mode: "daily", TradeDate: "2020-01-11", Symbols: []string{}})
	if err == nil || len(source.calls) != 0 {
		t.Fatal(err, source.calls)
	}
}

func TestHistoricalRepairPreservesAnchorAndReusesArtifacts(t *testing.T) {
	db, _, repo, _, runner := collectionFixture(t)
	ctx := context.Background()
	first, err := executeFixture(t, repo, runner, jobs.Request{Mode: "backfill", From: "2020-01-08", To: "2020-01-08", Symbols: []string{"600000.SH"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := executeFixture(t, repo, runner, jobs.Request{Mode: "backfill", From: "2020-01-02", To: "2020-01-03", Symbols: []string{"600000.SH"}})
	if err != nil {
		t.Fatal(err)
	}
	query := MarketData{DB: db, Securities: Securities{DB: db}}
	bars, err := query.Bars(ctx, "ins_600000_SH", "2020-01-02", "2020-01-08", "qfq")
	if err != nil {
		t.Fatal(err)
	}
	if bars.AsOf != "2020-01-08" || len(bars.Bars) != 3 || bars.Bars[0].Close != 5 || second.ExpectedRecords != 3 {
		t.Fatalf("%+v, %+v", bars, second)
	}
	var shared int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM quality_reports q1 JOIN quality_reports q2
		ON EXISTS(SELECT 1 FROM json_each(q1.report_json,'$.adjustment_artifact_ids') a
		JOIN json_each(q2.report_json,'$.adjustment_artifact_ids') b ON a.value=b.value)
		WHERE q1.dataset_version=? AND q2.dataset_version=?`, *first.DatasetVersion, *second.DatasetVersion).Scan(&shared); err != nil || shared != 1 {
		t.Fatal("verification artifacts should be content-addressed and reusable", shared, err)
	}
}

func TestTransportFailureEndsJobWithoutPanic(t *testing.T) {
	_, _, repo, source, runner := collectionFixture(t)
	source.fail = "transport:stock_basic"
	job, err := executeFixture(t, repo, runner, jobs.Request{Mode: "backfill", From: "2020-01-02", To: "2020-01-03", Symbols: []string{"600000.SH"}})
	if err == nil || job.Status != "failed" || job.Error == nil || job.Error.Code != "SOURCE_UNAVAILABLE" {
		t.Fatal(job, err)
	}
}

func TestAllMarketBatchAndSingleStockFallbackAgree(t *testing.T) {
	for _, failure := range []string{"", "unsupported:daily_history"} {
		t.Run(failure, func(t *testing.T) {
			db, _, repo, source, runner := collectionFixture(t)
			source.fail = failure
			job, err := executeFixture(t, repo, runner, jobs.Request{Mode: "backfill", From: "2020-01-02", To: "2020-01-03", Symbols: []string{}})
			if err != nil {
				t.Fatal(err)
			}
			if job.Status != "succeeded" || job.Coverage == nil || *job.Coverage != 1 || job.ExpectedRecords != 2 {
				t.Fatal(job)
			}
			query := MarketData{DB: db, Securities: Securities{DB: db}}
			bars, err := query.Bars(context.Background(), "ins_600000_SH", "2020-01-02", "2020-01-03", "qfq")
			if err != nil {
				t.Fatal(err)
			}
			if bars.SelectionScope != "all" || len(bars.Bars) != 2 || bars.Bars[0].Close != 5 || bars.Bars[1].Close != 5 || len(bars.DateStatuses) != 2 {
				t.Fatalf("%+v", bars)
			}
			batch := jobs.NewQuery("daily_history", map[string]string{"date": "2020-01-02"})
			if source.calls[batch.ID] != 1 {
				t.Fatal("market batch must be attempted and audited")
			}
		})
	}
}

func TestPartialDelistingRowPreservesStatusWithoutBar(t *testing.T) {
	db, _, repo, source, runner := collectionFixture(t)
	source.partial = true
	job, err := executeFixture(t, repo, runner, jobs.Request{Mode: "backfill", From: "2020-01-02", To: "2020-01-06", Symbols: []string{"600000.SH"}})
	if err != nil {
		t.Fatal(err)
	}
	query := MarketData{DB: db, Securities: Securities{DB: db}}
	bars, err := query.Bars(context.Background(), "ins_600000_SH", "2020-01-02", "2020-01-06", "none")
	if err != nil {
		t.Fatal(err)
	}
	if job.ExpectedRecords != 1 || len(bars.Bars) != 1 || len(bars.DateStatuses) != 3 ||
		bars.DateStatuses[1].TradeStatus != "delisted" || bars.DateStatuses[2].TradeStatus != "delisted" {
		t.Fatal(job, bars)
	}
	var pe float64
	if err = db.QueryRow(`SELECT pe_ttm FROM daily_valuations WHERE dataset_version=? AND trade_date='2020-01-03'`, *job.DatasetVersion).Scan(&pe); err != nil || pe != -1.5 {
		t.Fatal("partial-row valuation was discarded", pe, err)
	}
}
