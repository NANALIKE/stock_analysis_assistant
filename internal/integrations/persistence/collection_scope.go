package persistence

import (
	"context"
	"database/sql"
	"sort"
	"time"

	"stock_analysis_assistant/internal/marketdata"
	"stock_analysis_assistant/internal/securities"
)

func (r Collection) Universe(ctx context.Context) ([]securities.Instrument, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT id,symbol,name,exchange,listed_on,delisted_on,status FROM instruments WHERE exchange IN ('SH','SZ') ORDER BY symbol`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []securities.Instrument{}
	for rows.Next() {
		var i securities.Instrument
		if err = rows.Scan(&i.ID, &i.Symbol, &i.Name, &i.Exchange, &i.ListedOn, &i.DelistedOn, &i.Status); err != nil {
			return nil, err
		}
		result = append(result, i)
	}
	return result, rows.Err()
}

func (r Collection) ParentSymbols(ctx context.Context, version string) ([]string, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT DISTINCT i.symbol FROM dataset_instrument_ranges r JOIN instruments i ON i.id=r.instrument_id WHERE r.dataset_version=? ORDER BY i.symbol`, version)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var s string
		if err = rows.Scan(&s); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

func (r Collection) AddRanges(ctx context.Context, version string, instruments []securities.Instrument, from, to string) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, i := range instruments {
		rows, err := tx.QueryContext(ctx, `SELECT from_date,to_date FROM dataset_instrument_ranges WHERE dataset_version=? AND instrument_id=? ORDER BY from_date`, version, i.ID)
		if err != nil {
			return err
		}
		ranges := []marketdata.Range{{From: from, To: to}}
		for rows.Next() {
			var v marketdata.Range
			if err = rows.Scan(&v.From, &v.To); err != nil {
				rows.Close()
				return err
			}
			ranges = append(ranges, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		sort.Slice(ranges, func(a, b int) bool { return ranges[a].From < ranges[b].From })
		merged := []marketdata.Range{}
		for _, v := range ranges {
			if len(merged) > 0 {
				last := &merged[len(merged)-1]
				end, _ := time.Parse(time.DateOnly, last.To)
				if v.From <= end.AddDate(0, 0, 1).Format(time.DateOnly) {
					if v.To > last.To {
						last.To = v.To
					}
					continue
				}
			}
			merged = append(merged, v)
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM dataset_instrument_ranges WHERE dataset_version=? AND instrument_id=?`, version, i.ID); err != nil {
			return err
		}
		for _, v := range merged {
			if _, err = tx.ExecContext(ctx, `INSERT INTO dataset_instrument_ranges VALUES(?,?,?,?)`, version, i.ID, v.From, v.To); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (r Collection) Missing(ctx context.Context, version string, instruments []securities.Instrument, days []string) (map[string][]string, error) {
	result := map[string][]string{}
	for _, i := range instruments {
		rows, err := r.DB.QueryContext(ctx, `SELECT b.trade_date FROM dataset_partitions p JOIN daily_bars b ON b.dataset_version=p.data_version AND b.instrument_id=p.instrument_id
			AND substr(b.trade_date,1,4)=CAST(p.trade_year AS TEXT) WHERE p.dataset_version=? AND p.instrument_id=? AND b.adjustment='none'
			UNION SELECT s.trade_date FROM dataset_partitions p JOIN instrument_daily_status s ON s.dataset_version=p.data_version AND s.instrument_id=p.instrument_id
			AND substr(s.trade_date,1,4)=CAST(p.trade_year AS TEXT) WHERE p.dataset_version=? AND p.instrument_id=? AND s.trade_status='suspended'`, version, i.ID, version, i.ID)
		if err != nil {
			return nil, err
		}
		covered := map[string]bool{}
		for rows.Next() {
			var day string
			if err = rows.Scan(&day); err != nil {
				rows.Close()
				return nil, err
			}
			covered[day] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		for _, day := range days {
			if securities.Expected(i, day) && !covered[day] {
				result[i.Symbol] = append(result[i.Symbol], day)
			}
		}
	}
	return result, nil
}

func loadFactors(ctx context.Context, db *sql.DB, version, id string) ([]marketdata.Factor, error) {
	rows, err := db.QueryContext(ctx, `SELECT effective_date,fore_adjust_factor,back_adjust_factor,adjust_factor FROM adjustment_factors WHERE dataset_version=? AND instrument_id=? ORDER BY effective_date`, version, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []marketdata.Factor{}
	for rows.Next() {
		var f marketdata.Factor
		if err = rows.Scan(&f.Date, &f.Fore, &f.Back, &f.Value); err != nil {
			return nil, err
		}
		result = append(result, f)
	}
	return result, rows.Err()
}
