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
	"sort"
	"strings"

	"stock_analysis_assistant/internal/jobs"
	"stock_analysis_assistant/internal/marketdata"
	"stock_analysis_assistant/internal/platform"
	"stock_analysis_assistant/internal/platform/artifacts"
)

type storedBar struct {
	bar      marketdata.FixedBar
	snapshot string
}

func (r Collection) rebuild(ctx context.Context, version, id, anchor string, factors []marketdata.Factor) error {
	rows, err := r.DB.QueryContext(ctx, `SELECT b.trade_date,b.open,b.high,b.low,b.close,b.previous_close,b.volume,b.amount,b.turnover_rate,b.change_rate,b.trade_status,b.is_st,b.source_snapshot_id
		FROM dataset_partitions p JOIN daily_bars b ON b.dataset_version=p.data_version AND b.instrument_id=p.instrument_id AND substr(b.trade_date,1,4)=CAST(p.trade_year AS TEXT)
		WHERE p.dataset_version=? AND p.instrument_id=? AND b.adjustment='none' ORDER BY b.trade_date`, version, id)
	if err != nil {
		return err
	}
	bars := []storedBar{}
	for rows.Next() {
		var b storedBar
		b.bar.Adjustment = "none"
		if err = rows.Scan(&b.bar.TradeDate, &b.bar.Open, &b.bar.High, &b.bar.Low, &b.bar.Close, &b.bar.PreviousClose, &b.bar.Volume, &b.bar.Amount, &b.bar.TurnoverRate, &b.bar.ChangeRate, &b.bar.TradeStatus, &b.bar.IsST, &b.snapshot); err != nil {
			rows.Close()
			return err
		}
		bars = append(bars, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// Transactions are bounded to one instrument/year, never the entire history.
	for start := 0; start < len(bars); {
		end := start + 1
		for end < len(bars) && bars[end].bar.TradeDate[:4] == bars[start].bar.TradeDate[:4] {
			end++
		}
		tx, err := r.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if err = ensurePartition(ctx, tx, version, id, bars[start].bar.TradeDate); err == nil {
			for _, b := range bars[start:end] {
				var adjusted marketdata.FixedBar
				adjusted, err = marketdata.Adjust(b.bar, factors, anchor)
				if err != nil {
					break
				}
				if err = writeBar(ctx, tx, version, id, b.snapshot, adjusted); err != nil {
					break
				}
			}
		}
		if err != nil {
			tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		start = end
	}
	inputHash, err := r.factorInputHash(ctx, version, id, anchor, factors)
	if err != nil {
		return err
	}
	_, err = r.DB.ExecContext(ctx, `INSERT INTO dataset_factor_sets VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(dataset_version,instrument_id) DO UPDATE SET factor_version=excluded.factor_version,
		action_version=excluded.action_version,anchor_date=excluded.anchor_date,input_sha256=excluded.input_sha256,algorithm_version=excluded.algorithm_version`,
		version, id, version, version, anchor, inputHash, marketdata.AdjustmentAlgorithm)
	return err
}

func (r Collection) factorInputHash(ctx context.Context, version, id, anchor string, factors []marketdata.Factor) (string, error) {
	hash := sha256.New()
	hash.Write([]byte(marketdata.FactorHash(factors, anchor) + "\n"))
	for _, query := range []string{
		`SELECT record_sha256 FROM corporate_actions WHERE dataset_version=? AND instrument_id=? ORDER BY record_sha256`,
		`SELECT DISTINCT a.sha256 FROM generation_jobs j JOIN generation_units u ON u.job_id=j.id
			JOIN source_snapshots s ON s.id=u.snapshot_id JOIN artifacts a ON a.id=s.artifact_id
			JOIN instruments i ON i.id=? WHERE j.dataset_version=? AND u.state='completed'
			AND u.operation IN ('adjust_factor','dividend')
			AND json_extract(u.params_json,'$.code')=lower(i.exchange)||'.'||substr(i.symbol,1,6) ORDER BY a.sha256`,
	} {
		args := []any{version, id}
		if strings.Contains(query, "generation_jobs") {
			args = []any{id, version}
		}
		rows, err := r.DB.QueryContext(ctx, query, args...)
		if err != nil {
			return "", err
		}
		for rows.Next() {
			var value string
			if err = rows.Scan(&value); err != nil {
				rows.Close()
				return "", err
			}
			hash.Write([]byte(value + "\n"))
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (r Collection) adjustmentChecks(ctx context.Context, jobID string, factorSets map[string][]marketdata.Factor, anchor string, report *marketdata.QualityReport) ([]marketdata.AdjustmentDifference, map[string]bool, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT params_json FROM generation_units WHERE job_id=? AND operation='history' AND state='completed' AND json_extract(params_json,'$.adjustflag')='2'`, jobID)
	if err != nil {
		return nil, nil, err
	}
	params := []map[string]string{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return nil, nil, err
		}
		var p map[string]string
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			rows.Close()
			return nil, nil, err
		}
		params = append(params, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	checks := []marketdata.AdjustmentDifference{}
	verified := map[string]bool{}
	for _, p := range params {
		_, supplier, err := r.Unit(ctx, jobID, jobs.NewQuery("history", p))
		if err != nil {
			return nil, nil, err
		}
		noneParams := map[string]string{}
		for k, v := range p {
			noneParams[k] = v
		}
		noneParams["adjustflag"] = "3"
		_, none, err := r.Unit(ctx, jobID, jobs.NewQuery("history", noneParams))
		if err != nil {
			return nil, nil, err
		}
		if supplier == nil || none == nil {
			return nil, nil, fmt.Errorf("missing paired adjustment samples")
		}
		unadjusted := map[string]marketdata.FixedBar{}
		for _, raw := range none.Records {
			var f marketdata.Fact
			if err = json.Unmarshal(raw, &f); err != nil {
				return nil, nil, err
			}
			if f.Kind == "daily" {
				unadjusted[f.Bar.TradeDate] = f.Bar
			}
		}
		samples := map[string]map[string]string{}
		for _, row := range supplier.Rows {
			if len(row) != len(supplier.Fields) {
				return nil, nil, fmt.Errorf("sample row width mismatch")
			}
			values := map[string]string{}
			for i, k := range supplier.Fields {
				values[k] = row[i]
			}
			if _, ok := unadjusted[values["date"]]; ok {
				samples[values["date"]] = values
			}
		}
		dates := []string{}
		for day := range samples {
			dates = append(dates, day)
		}
		sort.Strings(dates)
		if len(dates) == 0 {
			continue
		}
		referenceDay := dates[len(dates)-1]
		symbol := providerSymbol(p["code"])
		factors, ok := factorSets[symbol]
		if !ok {
			return nil, nil, fmt.Errorf("sample factors absent")
		}
		sampleChecks := []marketdata.AdjustmentDifference{}
		for index, day := range dates {
			bar := unadjusted[day]
			if bar.Close == 0 || unadjusted[referenceDay].Close == 0 {
				continue
			}
			d, err := marketdata.VerifySample(bar, unadjusted[referenceDay], samples[day], samples[referenceDay], factors, anchor)
			if err != nil {
				return nil, nil, err
			}
			sampleChecks = append(sampleChecks, d...)
			if index > 0 && marketdata.BackAt(factors, dates[index-1]) != marketdata.BackAt(factors, day) {
				reference, err := marketdata.VerifyExchangeReference(unadjusted[dates[index-1]], bar, factors)
				if err != nil {
					return nil, nil, err
				}
				sampleChecks = append(sampleChecks, reference)
			}
			verified[symbol] = true
		}
		sampleBody, err := json.Marshal(map[string]any{"symbol": symbol, "query": jobs.NewQuery("history", p), "anchor": anchor, "algorithm": marketdata.AdjustmentAlgorithm, "checks": sampleChecks})
		if err != nil {
			return nil, nil, err
		}
		meta, err := r.Reports.Put(bytes.NewReader(sampleBody))
		if err != nil {
			return nil, nil, err
		}
		id := "qfq_" + meta.SHA256
		if _, err = r.DB.ExecContext(ctx, `INSERT INTO artifacts VALUES(?,?,'qfq_verification',?,?,?,1,?) ON CONFLICT(path) DO NOTHING`, id, meta.Path, meta.SizeBytes, meta.SHA256, jobID, timestamp()); err != nil {
			return nil, nil, err
		}
		report.AdjustmentArtifacts = append(report.AdjustmentArtifacts, id)
		report.CheckedValues += int64(len(sampleChecks))
		for _, d := range sampleChecks {
			if !d.Passed {
				checks = append(checks, d)
			}
		}
	}
	return checks, verified, nil
}

// expectedFacts scopes the denominator to declared, disjoint ranges. The last
// delisting day is included only if a bar or membership confirms its presence.
const expectedFacts = ` FROM dataset_instrument_ranges r
	JOIN instruments i ON i.id=r.instrument_id
	JOIN trading_days t ON t.exchange=i.exchange AND t.is_open=1 AND t.trade_date BETWEEN r.from_date AND r.to_date AND t.trade_date>=i.listed_on
	LEFT JOIN dataset_partitions p ON p.dataset_version=r.dataset_version AND p.instrument_id=i.id AND p.trade_year=CAST(substr(t.trade_date,1,4) AS INTEGER)
	LEFT JOIN daily_bars b ON b.dataset_version=p.data_version AND b.instrument_id=i.id AND b.trade_date=t.trade_date AND b.adjustment='none'
	LEFT JOIN instrument_daily_status s ON s.dataset_version=p.data_version AND s.instrument_id=i.id AND s.trade_date=t.trade_date
	WHERE r.dataset_version=? AND (i.delisted_on IS NULL OR t.trade_date<i.delisted_on OR (t.trade_date=i.delisted_on AND (b.trade_date IS NOT NULL OR s.trade_status IN ('trading','suspended'))))`

func (r Collection) coverage(ctx context.Context, version string, report *marketdata.QualityReport) error {
	rows, err := r.DB.QueryContext(ctx, `SELECT t.trade_date,COUNT(*),SUM(CASE WHEN b.trade_date IS NOT NULL OR s.trade_status='suspended' THEN 1 ELSE 0 END),COUNT(b.trade_date)`+expectedFacts+` GROUP BY t.trade_date ORDER BY t.trade_date`, version)
	if err != nil {
		return err
	}
	for rows.Next() {
		var c marketdata.CoverageCount
		if err = rows.Scan(&c.Period, &c.Expected, &c.Covered, &c.Bars); err != nil {
			rows.Close()
			return err
		}
		report.Daily = append(report.Daily, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var total int64
	var pe, pb, ps, pcf int64
	if err = r.DB.QueryRowContext(ctx, `SELECT COUNT(*),COUNT(v.pe_ttm),COUNT(v.pb_mrq),COUNT(v.ps_ttm),COUNT(v.pcf_ncf_ttm)
		FROM dataset_partitions p JOIN daily_valuations v ON v.dataset_version=p.data_version AND v.instrument_id=p.instrument_id
		AND substr(v.trade_date,1,4)=CAST(p.trade_year AS TEXT) WHERE p.dataset_version=?`, version).Scan(&total, &pe, &pb, &ps, &pcf); err != nil {
		return err
	}
	report.ValuationNonNull = map[string]float64{"pe_ttm": 0, "pb_mrq": 0, "ps_ttm": 0, "pcf_ncf_ttm": 0}
	if total > 0 {
		for k, v := range map[string]int64{"pe_ttm": pe, "pb_mrq": pb, "ps_ttm": ps, "pcf_ncf_ttm": pcf} {
			report.ValuationNonNull[k] = float64(v) / float64(total)
		}
	}
	return nil
}

func (r Collection) verifyManifest(ctx context.Context, version string) (string, error) {
	var invalid int
	if err := r.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM dataset_partitions p JOIN dataset_versions d ON d.id=p.data_version
		WHERE p.dataset_version=? AND d.id<>? AND d.state NOT IN ('active','superseded')`, version, version).Scan(&invalid); err != nil {
		return "", err
	}
	if invalid > 0 {
		return "", fmt.Errorf("partition references rejected/unpublished owner")
	}
	if err := r.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM dataset_factor_sets f JOIN dataset_versions d ON d.id=f.factor_version
		LEFT JOIN dataset_versions a ON a.id=f.action_version WHERE f.dataset_version=? AND
		((d.id<>? AND d.state NOT IN ('active','superseded')) OR f.action_version IS NULL OR (a.id<>? AND a.state NOT IN ('active','superseded')))`, version, version, version).Scan(&invalid); err != nil {
		return "", err
	}
	if invalid > 0 {
		return "", fmt.Errorf("factor/action references invalid owner")
	}
	if err := r.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM dataset_partitions p JOIN daily_bars b ON b.dataset_version=p.data_version AND b.instrument_id=p.instrument_id
		AND substr(b.trade_date,1,4)=CAST(p.trade_year AS TEXT) LEFT JOIN source_snapshots s ON s.id=b.source_snapshot_id
		LEFT JOIN daily_bars q ON q.dataset_version=b.dataset_version AND q.instrument_id=b.instrument_id AND q.trade_date=b.trade_date AND q.adjustment='qfq'
		WHERE p.dataset_version=? AND b.adjustment='none' AND (s.id IS NULL OR s.error_code<>'0' OR q.trade_date IS NULL)`, version).Scan(&invalid); err != nil {
		return "", err
	}
	if invalid > 0 {
		return "", fmt.Errorf("unpaired or unreferenced base bars")
	}
	rows, err := r.DB.QueryContext(ctx, `SELECT DISTINCT a.path,a.size_bytes,a.sha256 FROM artifacts a JOIN source_snapshots s ON s.artifact_id=a.id
		WHERE s.id IN (
		SELECT b.source_snapshot_id FROM dataset_partitions p JOIN daily_bars b ON b.dataset_version=p.data_version AND b.instrument_id=p.instrument_id AND substr(b.trade_date,1,4)=CAST(p.trade_year AS TEXT) WHERE p.dataset_version=?
		UNION SELECT v.source_snapshot_id FROM dataset_partitions p JOIN daily_valuations v ON v.dataset_version=p.data_version AND v.instrument_id=p.instrument_id AND substr(v.trade_date,1,4)=CAST(p.trade_year AS TEXT) WHERE p.dataset_version=?
		UNION SELECT s.source_snapshot_id FROM dataset_partitions p JOIN instrument_daily_status s ON s.dataset_version=p.data_version AND s.instrument_id=p.instrument_id AND substr(s.trade_date,1,4)=CAST(p.trade_year AS TEXT) WHERE p.dataset_version=?
		UNION SELECT f.source_snapshot_id FROM dataset_factor_sets d JOIN adjustment_factors f ON f.dataset_version=d.factor_version AND f.instrument_id=d.instrument_id WHERE d.dataset_version=?
		UNION SELECT c.source_snapshot_id FROM dataset_factor_sets d JOIN corporate_actions c ON c.dataset_version=d.action_version AND c.instrument_id=d.instrument_id WHERE d.dataset_version=?
		UNION SELECT u.snapshot_id FROM generation_units u JOIN generation_jobs j ON j.id=u.job_id WHERE j.dataset_version=? AND u.state='completed')
		ORDER BY a.sha256`, version, version, version, version, version, version)
	if err != nil {
		return "", err
	}
	metas := []artifacts.Metadata{}
	for rows.Next() {
		var m artifacts.Metadata
		if err = rows.Scan(&m.Path, &m.SizeBytes, &m.SHA256); err != nil {
			rows.Close()
			return "", err
		}
		metas = append(metas, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	for _, m := range metas {
		if _, err = r.Raw.Read(m); err != nil {
			return "", err
		}
		hash.Write([]byte(m.SHA256))
	}
	// Include the logical ranges and physical ownership, in deterministic order.
	for _, q := range []string{
		`SELECT instrument_id||':'||trade_year||':'||data_version FROM dataset_partitions WHERE dataset_version=? ORDER BY instrument_id,trade_year`,
		`SELECT instrument_id||':'||from_date||':'||to_date FROM dataset_instrument_ranges WHERE dataset_version=? ORDER BY instrument_id,from_date`,
		`SELECT instrument_id||':'||factor_version||':'||action_version||':'||anchor_date||':'||input_sha256||':'||algorithm_version FROM dataset_factor_sets WHERE dataset_version=? ORDER BY instrument_id`,
	} {
		rows, err := r.DB.QueryContext(ctx, q, version)
		if err != nil {
			return "", err
		}
		for rows.Next() {
			var s string
			if err = rows.Scan(&s); err != nil {
				rows.Close()
				return "", err
			}
			hash.Write([]byte(s + "\n"))
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (r Collection) completeStatuses(ctx context.Context, version string) error {
	rows, err := r.DB.QueryContext(ctx, `SELECT DISTINCT instrument_id FROM dataset_instrument_ranges WHERE dataset_version=? ORDER BY instrument_id`, version)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = r.completeInstrumentStatuses(ctx, version, id); err != nil {
			return err
		}
	}
	return nil
}

func (r Collection) completeInstrumentStatuses(ctx context.Context, version, id string) error {
	rows, err := r.DB.QueryContext(ctx, `SELECT i.id,t.trade_date,
		CASE WHEN t.trade_date<i.listed_on THEN 'not_listed' WHEN i.delisted_on IS NOT NULL AND t.trade_date>=i.delisted_on THEN 'delisted' ELSE 'missing' END,
		c.master_snapshot_id FROM dataset_instrument_ranges r JOIN instruments i ON i.id=r.instrument_id
		JOIN trading_days t ON t.exchange=i.exchange AND t.is_open=1 AND t.trade_date BETWEEN r.from_date AND r.to_date
		JOIN generation_jobs j ON j.dataset_version=r.dataset_version JOIN generation_contexts c ON c.job_id=j.id
		LEFT JOIN dataset_partitions p ON p.dataset_version=r.dataset_version AND p.instrument_id=i.id AND p.trade_year=CAST(substr(t.trade_date,1,4) AS INTEGER)
		LEFT JOIN instrument_daily_status s ON s.dataset_version=p.data_version AND s.instrument_id=i.id AND s.trade_date=t.trade_date
		WHERE r.dataset_version=? AND r.instrument_id=? AND s.trade_date IS NULL ORDER BY t.trade_date`, version, id)
	if err != nil {
		return err
	}
	type status struct{ id, day, value, snapshot string }
	statuses := []status{}
	for rows.Next() {
		var s status
		if err = rows.Scan(&s.id, &s.day, &s.value, &s.snapshot); err != nil {
			rows.Close()
			return err
		}
		statuses = append(statuses, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for start := 0; start < len(statuses); {
		end := start + 1
		for end < len(statuses) && statuses[end].id == statuses[start].id && statuses[end].day[:4] == statuses[start].day[:4] {
			end++
		}
		tx, err := r.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if err = ensurePartition(ctx, tx, version, statuses[start].id, statuses[start].day); err == nil {
			for _, s := range statuses[start:end] {
				_, err = tx.ExecContext(ctx, `INSERT INTO instrument_daily_status(dataset_version,instrument_id,trade_date,trade_status,reason_code,source_snapshot_id,observed_at)
					VALUES(?,?,?,?,'calendar_listing_evidence',?,?)`, version, s.id, s.day, s.value, s.snapshot, timestamp())
				if err != nil {
					break
				}
			}
		}
		if err != nil {
			tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		start = end
	}
	return nil
}

func (r Collection) Finish(ctx context.Context, job jobs.Job, version string, minimum float64) error {
	report := marketdata.QualityReport{DatasetVersion: version, MarketScope: "SH,SZ A shares (BaoStock)", SourceGaps: []string{"BJ A shares unsupported"},
		Algorithm: marketdata.AdjustmentAlgorithm, Daily: []marketdata.CoverageCount{}, Yearly: []marketdata.CoverageCount{}, Failures: []string{}}
	if err := r.DB.QueryRowContext(ctx, `SELECT as_of,selection_scope FROM dataset_versions WHERE id=? AND state='building'`, version).Scan(&report.AsOf, &report.SelectionScope); err != nil {
		return err
	}
	universe, err := r.Universe(ctx)
	if err != nil {
		return err
	}
	symbols, err := r.ParentSymbols(ctx, version)
	if err != nil {
		return err
	}
	logical := map[string]bool{}
	for _, s := range symbols {
		logical[s] = true
	}
	factorSets := map[string][]marketdata.Factor{}
	for _, i := range universe {
		if !logical[i.Symbol] || i.ListedOn > report.AsOf {
			continue
		}
		q := jobs.NewQuery("adjust_factor", map[string]string{"code": providerCodeFor(i.Symbol), "start_date": i.ListedOn, "end_date": report.AsOf})
		_, saved, err := r.Unit(ctx, job.ID, q)
		if err != nil {
			return err
		}
		if saved == nil {
			report.Failures = append(report.Failures, "incomplete factor query: "+i.Symbol)
			continue
		}
		factors, err := loadFactors(ctx, r.DB, version, i.ID)
		if err != nil {
			return err
		}
		if err = marketdata.ValidateFactors(factors); err != nil {
			report.Failures = append(report.Failures, "invalid factors: "+i.Symbol)
			continue
		}
		factorSets[i.Symbol] = factors
		if err = r.rebuild(ctx, version, i.ID, report.AsOf, factors); err != nil {
			return err
		}
	}
	checks, verified, err := r.adjustmentChecks(ctx, job.ID, factorSets, report.AsOf, &report)
	report.AdjustmentChecks = checks
	adjustmentFailure := err != nil
	if err != nil {
		report.Failures = append(report.Failures, "adjustment verification: "+err.Error())
	}
	for s := range factorSets {
		var count int
		if err = r.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM dataset_partitions p JOIN instruments i ON i.id=p.instrument_id JOIN daily_bars b ON b.dataset_version=p.data_version AND b.instrument_id=p.instrument_id AND substr(b.trade_date,1,4)=CAST(p.trade_year AS TEXT)
			WHERE p.dataset_version=? AND i.symbol=? AND b.adjustment='none'`, version, s).Scan(&count); err != nil {
			return err
		}
		if count > 0 && !verified[s] {
			report.Failures = append(report.Failures, "unverified qfq: "+s)
			adjustmentFailure = true
		}
	}
	for _, d := range report.AdjustmentChecks {
		if !d.Passed {
			adjustmentFailure = true
		}
	}
	if err = r.completeStatuses(ctx, version); err != nil {
		return err
	}
	if err = r.coverage(ctx, version, &report); err != nil {
		return err
	}
	var issues int
	if err = r.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM data_quality_issues WHERE dataset_version=? AND code NOT IN ('EXCLUDED_SECURITY')`, version).Scan(&issues); err != nil {
		return err
	}
	if issues > 0 {
		report.Failures = append(report.Failures, fmt.Sprintf("%d unresolved source/record issues", issues))
	}
	report.ManifestHash, err = r.verifyManifest(ctx, version)
	if err != nil {
		report.Failures = append(report.Failures, "manifest: "+err.Error())
	}
	report.Evaluate(minimum)
	body, err := json.Marshal(report)
	if err != nil {
		return err
	}
	meta, err := r.Reports.Put(bytes.NewReader(body))
	if err != nil {
		return err
	}
	artifact := "quality_" + meta.SHA256
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO artifacts VALUES(?,?,'quality_report',?,?,?,1,?) ON CONFLICT(path) DO NOTHING`, artifact, meta.Path, meta.SizeBytes, meta.SHA256, job.ID, timestamp()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO quality_reports VALUES(?,?,?,?,?,?,?)`, version, report.Expected, report.ValidBars, report.Coverage, report.Passed, string(body), timestamp()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE generation_jobs SET processed_records=?,expected_records=?,coverage=?,quality_artifact_id=? WHERE id=?`, report.ValidBars, report.Expected, report.Coverage, artifact, job.ID); err != nil {
		return err
	}
	if !report.Passed {
		if err = tx.Commit(); err != nil {
			return err
		}
		code := "QUALITY_VALIDATION_FAILED"
		if adjustmentFailure {
			code = "ADJUSTMENT_VALIDATION_FAILED"
		}
		return &platform.Error{Code: code, Message: "quality gate failed; inspect saved quality report"}
	}
	// Publication binds the persisted report and all verification artifacts.
	for _, id := range append([]string{artifact}, report.AdjustmentArtifacts...) {
		var m artifacts.Metadata
		if err = tx.QueryRowContext(ctx, `SELECT path,size_bytes,sha256 FROM artifacts WHERE id=?`, id).Scan(&m.Path, &m.SizeBytes, &m.SHA256); err != nil {
			return err
		}
		if _, err = r.Reports.Read(m); err != nil {
			return err
		}
	}
	var parent *string
	var active string
	if err = tx.QueryRowContext(ctx, `SELECT parent_version FROM dataset_versions WHERE id=? AND state='building'`, version).Scan(&parent); err != nil {
		return err
	}
	err = tx.QueryRowContext(ctx, `SELECT id FROM dataset_versions WHERE state='active'`).Scan(&active)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if (parent == nil && active != "") || (parent != nil && *parent != active) {
		if err = tx.Commit(); err != nil {
			return err
		}
		return &platform.Error{Code: "PARENT_VERSION_CONFLICT", Message: "active parent changed; publication refused"}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE dataset_versions SET state='superseded' WHERE state='active'`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE dataset_versions SET state='active',published_at=?,input_sha256=? WHERE id=? AND state='building'`, timestamp(), report.ManifestHash, version); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE generation_jobs SET status='succeeded',stage='published',finished_at=?,error_json=NULL WHERE id=? AND status='running' AND attempt=?`, timestamp(), job.ID, job.Attempt); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE job_attempts SET status='succeeded',finished_at=? WHERE job_id=? AND attempt=? AND status='running'`, timestamp(), job.ID, job.Attempt); err != nil {
		return err
	}
	return tx.Commit()
}

func providerCodeFor(symbol string) string {
	return strings.ToLower(symbol[7:]) + "." + symbol[:6]
}
