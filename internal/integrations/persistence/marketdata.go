package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"stock_analysis_assistant/internal/marketdata"
	"stock_analysis_assistant/internal/platform"
	"stock_analysis_assistant/internal/securities"
)

type MarketData struct {
	DB         *sql.DB
	Securities securities.Repository
}

func (r MarketData) Active(ctx context.Context) (marketdata.Dataset, error) {
	var dataset marketdata.Dataset
	var reportJSON string
	err := r.DB.QueryRowContext(ctx, `SELECT d.id,d.as_of,d.source,d.default_adjustment,q.coverage,d.market_scope,d.selection_scope,q.expected_records,q.report_json
		FROM dataset_versions d JOIN quality_reports q ON q.dataset_version=d.id
		WHERE d.state='active' AND q.passed=1`).
		Scan(&dataset.ID, &dataset.AsOf, &dataset.Source, &dataset.Adjustment, &dataset.Coverage, &dataset.MarketScope, &dataset.SelectionScope, &dataset.ExpectedRecords, &reportJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return dataset, &platform.Error{Code: "NO_ACTIVE_DATASET", Message: "no validated dataset has been published"}
	}
	if err != nil {
		return dataset, err
	}
	var report marketdata.QualityReport
	if err = json.Unmarshal([]byte(reportJSON), &report); err != nil {
		return dataset, err
	}
	dataset.CoveredRecords = report.Covered
	if err = r.DB.QueryRowContext(ctx, `SELECT COALESCE(MIN(from_date),''),COALESCE(MAX(to_date),''),COUNT(DISTINCT instrument_id) FROM dataset_instrument_ranges WHERE dataset_version=?`, dataset.ID).Scan(&dataset.From, &dataset.To, &dataset.UniverseSize); err != nil {
		return dataset, err
	}
	var patterns int
	if err = r.DB.QueryRowContext(ctx, `SELECT COUNT(DISTINCT pattern) FROM (
		SELECT instrument_id,group_concat(from_date||':'||to_date,';') pattern FROM
		(SELECT instrument_id,from_date,to_date FROM dataset_instrument_ranges WHERE dataset_version=? ORDER BY instrument_id,from_date) GROUP BY instrument_id)`, dataset.ID).Scan(&patterns); err != nil {
		return dataset, err
	}
	dataset.RangesUniform = patterns == 1
	if err = r.DB.QueryRowContext(ctx, `SELECT quality_artifact_id FROM generation_jobs WHERE dataset_version=?`, dataset.ID).Scan(&dataset.QualityArtifactID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return dataset, err
	}
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return dataset, err
	}
	now := time.Now().In(location)
	closed := now.Format(time.DateOnly)
	if now.Hour() < 15 {
		closed = now.AddDate(0, 0, -1).Format(time.DateOnly)
	}
	var latest *string
	if err = r.DB.QueryRowContext(ctx, `SELECT MAX(trade_date) FROM trading_days WHERE exchange='SH' AND is_open=1 AND trade_date<=?`, closed).Scan(&latest); err != nil {
		return dataset, err
	}
	dataset.Stale = latest == nil || dataset.AsOf < *latest
	var calendarToday int
	if err = r.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM trading_days WHERE exchange='SH' AND trade_date=?`, now.Format(time.DateOnly)).Scan(&calendarToday); err != nil {
		return dataset, err
	}
	if calendarToday == 0 {
		dataset.Stale = true
	}
	return dataset, nil
}

func (r MarketData) Bars(ctx context.Context, id, from, to, adjustment string) (marketdata.DailyBars, error) {
	var result marketdata.DailyBars
	instrument, err := r.Securities.Get(ctx, id)
	if err != nil {
		return result, err
	}
	dataset, err := r.Active(ctx)
	if err != nil {
		return result, err
	}
	result.Instrument, result.Dataset = instrument, dataset
	result.Adjustment = adjustment
	result.Bars = make([]marketdata.Bar, 0)
	result.DateStatuses = make([]marketdata.DateStatus, 0)
	rows, err := r.DB.QueryContext(ctx, `SELECT b.trade_date,b.open,b.high,b.low,b.close,b.volume,b.amount,b.is_suspended,
		b.previous_close,b.turnover_rate,b.change_rate,b.trade_status,b.is_st,
		v.trade_date,v.pe_ttm,v.pb_mrq,v.ps_ttm,v.pcf_ncf_ttm
		FROM dataset_partitions p
		JOIN daily_bars b ON b.dataset_version=p.data_version AND b.instrument_id=p.instrument_id
		    AND CAST(substr(b.trade_date,1,4) AS INTEGER)=p.trade_year
		JOIN dataset_versions owner ON owner.id=p.data_version AND owner.state IN ('active','superseded')
		LEFT JOIN daily_valuations v ON v.dataset_version=b.dataset_version AND v.instrument_id=b.instrument_id AND v.trade_date=b.trade_date
		WHERE p.dataset_version=? AND p.instrument_id=? AND b.adjustment=? AND b.trade_date BETWEEN ? AND ?
		ORDER BY b.trade_date`,
		dataset.ID, id, adjustment, from, to)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var bar marketdata.Bar
		var open, high, low, close, amount int64
		var previousClose *int64
		var valuationDate *string
		var valuation marketdata.Valuation
		if err := rows.Scan(&bar.TradeDate, &open, &high, &low, &close, &bar.Volume, &amount, &bar.IsSuspended,
			&previousClose, &bar.TurnoverRate, &bar.ChangeRate, &bar.TradeStatus, &bar.IsST,
			&valuationDate, &valuation.PETTM, &valuation.PBMRQ, &valuation.PSTTM, &valuation.PCFNCFTTM); err != nil {
			return result, err
		}
		bar.Open, bar.High, bar.Low, bar.Close = float64(open)/10000, float64(high)/10000, float64(low)/10000, float64(close)/10000
		bar.Amount = float64(amount) / 10000
		if previousClose != nil {
			value := float64(*previousClose) / 10000
			bar.PreviousClose = &value
		}
		if valuationDate != nil {
			bar.Valuation = &valuation
		}
		result.Bars = append(result.Bars, bar)
	}
	err = rows.Err()
	rows.Close() // Release the sole connection before querying status records.
	if err != nil {
		return result, err
	}
	statusRows, err := r.DB.QueryContext(ctx, `SELECT s.trade_date,s.code_name,s.trade_status,s.is_st,s.reason_code
		FROM dataset_partitions p JOIN instrument_daily_status s
		  ON s.dataset_version=p.data_version AND s.instrument_id=p.instrument_id
		  AND CAST(substr(s.trade_date,1,4) AS INTEGER)=p.trade_year
		JOIN dataset_versions owner ON owner.id=p.data_version AND owner.state IN ('active','superseded')
		WHERE p.dataset_version=? AND p.instrument_id=? AND s.trade_date BETWEEN ? AND ? ORDER BY s.trade_date`,
		dataset.ID, id, from, to)
	if err != nil {
		return result, err
	}
	defer statusRows.Close()
	for statusRows.Next() {
		var status marketdata.DateStatus
		if err := statusRows.Scan(&status.TradeDate, &status.CodeName, &status.TradeStatus, &status.IsST, &status.ReasonCode); err != nil {
			return result, err
		}
		result.DateStatuses = append(result.DateStatuses, status)
	}
	err = statusRows.Err()
	statusRows.Close()
	if err != nil {
		return result, err
	}
	result.CoverageRanges = []marketdata.Range{}
	rangeRows, err := r.DB.QueryContext(ctx, `SELECT from_date,to_date FROM dataset_instrument_ranges WHERE dataset_version=? AND instrument_id=? ORDER BY from_date`, dataset.ID, id)
	if err != nil {
		return result, err
	}
	defer rangeRows.Close()
	for rangeRows.Next() {
		var v marketdata.Range
		if err = rangeRows.Scan(&v.From, &v.To); err != nil {
			return result, err
		}
		result.CoverageRanges = append(result.CoverageRanges, v)
	}
	return result, rangeRows.Err()
}
