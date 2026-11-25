package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"stock_analysis_assistant/internal/marketdata"
	"stock_analysis_assistant/internal/platform"
)

func instrumentID(ctx context.Context, tx *sql.Tx, symbol string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM instruments WHERE symbol=?`, symbol).Scan(&id)
	return id, err
}

func addIssue(ctx context.Context, tx *sql.Tx, version, id, day, code string, details any) error {
	body, _ := json.Marshal(details)
	_, err := tx.ExecContext(ctx, `INSERT INTO data_quality_issues(id,dataset_version,instrument_id,trade_date,code,details_json)
		VALUES(?,?,?,?,?,?)`, platform.NewID("issue"), version, nullable(id), nullable(day), code, string(body))
	return err
}

func ensurePartition(ctx context.Context, tx *sql.Tx, version, id, day string) error {
	year := day[:4]
	var old string
	err := tx.QueryRowContext(ctx, `SELECT data_version FROM dataset_partitions WHERE dataset_version=? AND instrument_id=? AND trade_year=?`,
		version, id, year).Scan(&old)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if old == version {
		return nil
	}
	if old != "" {
		for _, s := range []string{
			`INSERT INTO daily_bars(dataset_version,instrument_id,trade_date,adjustment,open,high,low,close,previous_close,volume,amount,turnover_rate,change_rate,trade_status,is_st,source_snapshot_id)
			SELECT ?,instrument_id,trade_date,adjustment,open,high,low,close,previous_close,volume,amount,turnover_rate,change_rate,trade_status,is_st,source_snapshot_id FROM daily_bars
			WHERE dataset_version=? AND instrument_id=? AND substr(trade_date,1,4)=?`,
			`INSERT INTO daily_valuations SELECT ?,instrument_id,trade_date,pe_ttm,pb_mrq,ps_ttm,pcf_ncf_ttm,source_snapshot_id FROM daily_valuations
			WHERE dataset_version=? AND instrument_id=? AND substr(trade_date,1,4)=?`,
			`INSERT INTO instrument_daily_status SELECT ?,instrument_id,trade_date,code_name,trade_status,is_st,reason_code,source_snapshot_id,observed_at
			FROM instrument_daily_status WHERE dataset_version=? AND instrument_id=? AND substr(trade_date,1,4)=?`,
		} {
			if _, err = tx.ExecContext(ctx, s, version, old, id, year); err != nil {
				return err
			}
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO dataset_partitions VALUES(?,?,?,?) ON CONFLICT(dataset_version,instrument_id,trade_year)
		DO UPDATE SET data_version=excluded.data_version`, version, id, year, version)
	return err
}

func writeBar(ctx context.Context, tx *sql.Tx, version, id, snapshot string, b marketdata.FixedBar) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO daily_bars
		(dataset_version,instrument_id,trade_date,adjustment,open,high,low,close,previous_close,volume,amount,turnover_rate,change_rate,trade_status,is_st,source_snapshot_id)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(dataset_version,instrument_id,adjustment,trade_date) DO UPDATE SET
		open=excluded.open,high=excluded.high,low=excluded.low,close=excluded.close,
		previous_close=excluded.previous_close,volume=excluded.volume,amount=excluded.amount,
		turnover_rate=excluded.turnover_rate,change_rate=excluded.change_rate,
		trade_status=excluded.trade_status,is_st=excluded.is_st,source_snapshot_id=excluded.source_snapshot_id`,
		version, id, b.TradeDate, b.Adjustment, b.Open, b.High, b.Low, b.Close, b.PreviousClose, b.Volume, b.Amount, b.TurnoverRate, b.ChangeRate, b.TradeStatus, b.IsST, snapshot)
	return err
}

func (r Collection) importResponse(ctx context.Context, tx *sql.Tx, version, snapshot string, response marketdata.SourceResponse) error {
	var selected string
	var from, to, jobID string
	if err := tx.QueryRowContext(ctx, `SELECT c.symbols_json,c.from_date,c.to_date,c.job_id
		FROM generation_contexts c JOIN generation_jobs j ON j.id=c.job_id WHERE j.dataset_version=?`, version).Scan(&selected, &from, &to, &jobID); err != nil {
		return err
	}
	var symbols []string
	if err := json.Unmarshal([]byte(selected), &symbols); err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, s := range symbols {
		allowed[s] = true
	}
	for _, issue := range response.Issues {
		if response.Operation == "daily_history" && issue.Code == "FIELD_CONTRACT" {
			continue
		} // Audited raw response; bounded history supplies facts.
		if err := addIssue(ctx, tx, version, "", "", issue.Code, map[string]any{"snapshot_id": snapshot, "issue": issue}); err != nil {
			return err
		}
	}
	if response.Operation == "adjust_factor" {
		var id string
		s := providerSymbol(response.Request["code"])
		if err := tx.QueryRowContext(ctx, `SELECT id FROM instruments WHERE symbol=?`, s).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM adjustment_factors WHERE dataset_version=? AND instrument_id=?`, version, id); err != nil {
			return err
		}
	}
	if response.Operation == "dividend" {
		id, err := instrumentID(ctx, tx, providerSymbol(response.Request["code"]))
		if err != nil {
			return err
		}
		// First inherit complete actions, then refresh a requested observation year.
		if err = ensureActions(ctx, tx, version, id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM corporate_actions WHERE dataset_version=? AND instrument_id=? AND query_year=? AND year_type=?`,
			version, id, response.Request["year"], response.Request["year_type"]); err != nil {
			return err
		}
	}
	for _, raw := range response.Records {
		var fact marketdata.Fact
		if err := json.Unmarshal(raw, &fact); err != nil {
			return err
		}
		if fact.Kind == "instrument" {
			id := "ins_" + strings.ReplaceAll(fact.Symbol, ".", "_")
			if _, err := tx.ExecContext(ctx, `INSERT INTO instruments(id,symbol,name,exchange,listed_on,delisted_on,status)
				VALUES(?,?,?,?,?,?,?) ON CONFLICT(symbol) DO UPDATE SET name=excluded.name,exchange=excluded.exchange,
				listed_on=excluded.listed_on,delisted_on=excluded.delisted_on,status=excluded.status`,
				id, fact.Symbol, fact.Name, fact.Exchange, fact.ListedOn, fact.DelistedOn, fact.Status); err != nil {
				return err
			}
			continue
		}
		if fact.Kind == "calendar" {
			for _, exchange := range []string{"SH", "SZ"} {
				if _, err := tx.ExecContext(ctx, `INSERT INTO trading_days VALUES(?,?,?) ON CONFLICT(exchange,trade_date) DO UPDATE SET is_open=excluded.is_open`,
					exchange, fact.TradeDate, fact.IsOpen); err != nil {
					return err
				}
			}
			continue
		}
		if fact.Kind == "qfq_sample" {
			continue
		}
		if len(allowed) > 0 && !allowed[fact.Symbol] {
			continue
		}
		id, err := instrumentID(ctx, tx, fact.Symbol)
		if errors.Is(err, sql.ErrNoRows) && fact.Kind == "status" {
			continue
		} // Discovery is resolved by explicit stock_basic.
		if err != nil {
			return err
		}
		switch fact.Kind {
		case "daily_status":
			day := fact.TradeDate
			if day < from || day > to {
				continue
			}
			var listed string
			var delisted *string
			if err = tx.QueryRowContext(ctx, `SELECT listed_on,delisted_on FROM instruments WHERE id=?`, id).Scan(&listed, &delisted); err != nil {
				return err
			}
			status := fact.TradeStatus
			if day < listed {
				status = "not_listed"
			} else if delisted != nil && day >= *delisted {
				status = "delisted"
			}
			if err = ensurePartition(ctx, tx, version, id, day); err != nil {
				return err
			}
			if err = checkStatusConflict(ctx, tx, version, id, day, status, snapshot, jobID); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `DELETE FROM daily_bars WHERE dataset_version=? AND instrument_id=? AND trade_date=?`, version, id, day); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO instrument_daily_status
				(dataset_version,instrument_id,trade_date,trade_status,is_st,reason_code,source_snapshot_id,observed_at)
				VALUES(?,?,?,?,?,'source_daily_partial',?,?) ON CONFLICT(dataset_version,instrument_id,trade_date)
				DO UPDATE SET trade_status=excluded.trade_status,is_st=excluded.is_st,reason_code=excluded.reason_code,source_snapshot_id=excluded.source_snapshot_id,observed_at=excluded.observed_at`,
				version, id, day, status, fact.IsST, snapshot, response.FetchedAt); err != nil {
				return err
			}
			v := fact.Valuation
			if _, err = tx.ExecContext(ctx, `INSERT INTO daily_valuations VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(dataset_version,instrument_id,trade_date)
				DO UPDATE SET pe_ttm=excluded.pe_ttm,pb_mrq=excluded.pb_mrq,ps_ttm=excluded.ps_ttm,pcf_ncf_ttm=excluded.pcf_ncf_ttm,source_snapshot_id=excluded.source_snapshot_id`,
				version, id, day, v.PETTM, v.PBMRQ, v.PSTTM, v.PCFNCFTTM, snapshot); err != nil {
				return err
			}
		case "daily":
			b := fact.Bar
			if b.TradeDate < from || b.TradeDate > to {
				continue
			}
			if err = ensurePartition(ctx, tx, version, id, b.TradeDate); err != nil {
				return err
			}
			var prior, priorSnapshot string
			var po, ph, pl, pc, pv, pa int64
			err = tx.QueryRowContext(ctx, `SELECT b.open,b.high,b.low,b.close,b.volume,b.amount,b.trade_status,b.source_snapshot_id
				FROM daily_bars b JOIN source_snapshots s ON s.id=b.source_snapshot_id JOIN artifacts a ON a.id=s.artifact_id
				WHERE b.dataset_version=? AND b.instrument_id=? AND b.trade_date=? AND b.adjustment='none' AND a.job_id=?`,
				version, id, b.TradeDate, jobID).Scan(&po, &ph, &pl, &pc, &pv, &pa, &prior, &priorSnapshot)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil && (po != b.Open || ph != b.High || pl != b.Low || pc != b.Close || pv != b.Volume || pa != b.Amount || prior != b.TradeStatus) {
				if err = addIssue(ctx, tx, version, id, b.TradeDate, "SOURCE_CONFLICT", map[string]string{"first": priorSnapshot, "second": snapshot}); err != nil {
					return err
				}
			}
			if err = checkStatusConflict(ctx, tx, version, id, b.TradeDate, b.TradeStatus, snapshot, jobID); err != nil {
				return err
			}
			if err = writeBar(ctx, tx, version, id, snapshot, b); err != nil {
				return err
			}
			v := fact.Valuation
			if _, err = tx.ExecContext(ctx, `INSERT INTO daily_valuations VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(dataset_version,instrument_id,trade_date)
				DO UPDATE SET pe_ttm=excluded.pe_ttm,pb_mrq=excluded.pb_mrq,ps_ttm=excluded.ps_ttm,pcf_ncf_ttm=excluded.pcf_ncf_ttm,source_snapshot_id=excluded.source_snapshot_id`,
				version, id, b.TradeDate, v.PETTM, v.PBMRQ, v.PSTTM, v.PCFNCFTTM, snapshot); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO instrument_daily_status
				(dataset_version,instrument_id,trade_date,trade_status,is_st,reason_code,source_snapshot_id,observed_at)
				VALUES(?,?,?,?,?,'source_daily',?,?) ON CONFLICT(dataset_version,instrument_id,trade_date)
				DO UPDATE SET trade_status=excluded.trade_status,is_st=excluded.is_st,reason_code=excluded.reason_code,
				source_snapshot_id=excluded.source_snapshot_id,observed_at=excluded.observed_at`,
				version, id, b.TradeDate, b.TradeStatus, b.IsST, snapshot, response.FetchedAt); err != nil {
				return err
			}
		case "status":
			day := response.Request["day"]
			if err = ensurePartition(ctx, tx, version, id, day); err != nil {
				return err
			}
			if err = checkStatusConflict(ctx, tx, version, id, day, fact.TradeStatus, snapshot, jobID); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO instrument_daily_status
				(dataset_version,instrument_id,trade_date,code_name,trade_status,reason_code,source_snapshot_id,observed_at)
				VALUES(?,?,?,?,?,'source_membership',?,?) ON CONFLICT(dataset_version,instrument_id,trade_date)
				DO UPDATE SET code_name=excluded.code_name`,
				version, id, day, fact.Name, fact.TradeStatus, snapshot, response.FetchedAt); err != nil {
				return err
			}
		case "factor":
			if _, err = tx.ExecContext(ctx, `INSERT INTO adjustment_factors VALUES(?,?,?,?,?,?,?,?,?)`,
				version, id, fact.EffectiveDate, fact.Fore, fact.Back, fact.Factor, response.FetchedAt, response.ProviderPublishedAt, snapshot); err != nil {
				return err
			}
		case "dividend":
			var d map[string]any
			if err = json.Unmarshal(raw, &d); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO corporate_actions
				(id,dataset_version,instrument_id,record_sha256,query_year,year_type,pre_notice_date,agm_announce_date,plan_announce_date,
				implementation_announce_date,record_date,effective_date,pay_date,stock_listing_date,cash_per_share_before_tax,
				cash_per_share_after_tax_raw,stock_dividend_per_share,reserve_transfer_per_share,description,observed_at,source_snapshot_id)
				VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(dataset_version,instrument_id,record_sha256) DO NOTHING`,
				platform.NewID("action"), version, id, d["record_sha256"], response.Request["year"], response.Request["year_type"],
				d["pre_notice_date"], d["agm_announce_date"], d["plan_announce_date"], d["implementation_announce_date"], d["record_date"], d["effective_date"],
				d["pay_date"], d["stock_listing_date"], d["cash_per_share_before_tax"], d["cash_per_share_after_tax_raw"], d["stock_dividend_per_share"],
				d["reserve_transfer_per_share"], d["description"], response.FetchedAt, snapshot); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown normalized kind %q", fact.Kind)
		}
	}
	return nil
}

func providerSymbol(code string) string {
	if len(code) != 9 {
		return ""
	}
	return code[3:] + "." + strings.ToUpper(code[:2])
}

func checkStatusConflict(ctx context.Context, tx *sql.Tx, version, id, day, status, snapshot, jobID string) error {
	var old, source string
	err := tx.QueryRowContext(ctx, `SELECT s.trade_status,s.source_snapshot_id FROM instrument_daily_status s
		JOIN source_snapshots snap ON snap.id=s.source_snapshot_id JOIN artifacts a ON a.id=snap.artifact_id
		WHERE s.dataset_version=? AND s.instrument_id=? AND s.trade_date=? AND a.job_id=?`, version, id, day, jobID).Scan(&old, &source)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if old != status {
		return addIssue(ctx, tx, version, id, day, "STATUS_CONFLICT", map[string]string{"first": source, "second": snapshot})
	}
	return nil
}

func ensureActions(ctx context.Context, tx *sql.Tx, version, id string) error {
	// The sentinel factor-set action pointer records that inheritance is complete,
	// including a genuinely empty parent action set.
	var owner *string
	err := tx.QueryRowContext(ctx, `SELECT action_version FROM dataset_factor_sets WHERE dataset_version=? AND instrument_id=?`, version, id).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if owner == nil || *owner == version {
		return nil
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO corporate_actions
		SELECT id||'_'||?, ?,instrument_id,record_sha256,query_year,year_type,pre_notice_date,agm_announce_date,plan_announce_date,
		implementation_announce_date,record_date,effective_date,pay_date,stock_listing_date,cash_per_share_before_tax,
		cash_per_share_after_tax_raw,stock_dividend_per_share,reserve_transfer_per_share,description,observed_at,source_snapshot_id
		FROM corporate_actions WHERE dataset_version=? AND instrument_id=?`, version, version, *owner, id); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE dataset_factor_sets SET action_version=? WHERE dataset_version=? AND instrument_id=?`, version, version, id)
	return err
}
